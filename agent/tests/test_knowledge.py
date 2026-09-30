from __future__ import annotations

import asyncio
import logging
from typing import Any
from uuid import UUID, uuid4

import pytest
from livekit.agents.llm.utils import build_legacy_openai_schema

from callgo_agent.backend_client import BackendError
from callgo_agent.knowledge import (
    ERROR_REPLY,
    FILLER_TEXT,
    NOT_FOUND_REPLY,
    KnowledgeRetriever,
    active_knowledge,
    build_lookup_tool,
    format_hits,
    knowledge_instructions,
    run_with_filler,
    session_filler,
)
from callgo_agent.schemas import (
    AgentProfile,
    Bootstrap,
    Call,
    CallDirection,
    CallStatus,
    KnowledgeHit,
    KnowledgeInfo,
    Organization,
)

KB = UUID("66666666-6666-6666-6666-666666666666")
ORG = UUID("11111111-1111-1111-1111-111111111111")


def hit(content: str, score: float, filename: str = "guide.pdf", heading: str = "") -> KnowledgeHit:
    return KnowledgeHit(
        chunk_id=uuid4(),
        document_id=uuid4(),
        filename=filename,
        heading=heading,
        content=content,
        score=score,
    )


class FakeSearcher:
    def __init__(
        self,
        hits: list[KnowledgeHit] | None = None,
        *,
        error: Exception | None = None,
        delay: float = 0.0,
    ) -> None:
        self.hits = hits or []
        self.error = error
        self.delay = delay
        self.calls: list[tuple[UUID, str, int]] = []

    async def knowledge_search(
        self, knowledge_base_id: UUID, query: str, k: int = 5
    ) -> list[KnowledgeHit]:
        self.calls.append((knowledge_base_id, query, k))
        if self.delay:
            await asyncio.sleep(self.delay)
        if self.error is not None:
            raise self.error
        return list(self.hits)


class Clock:
    def __init__(self) -> None:
        self.now = 1000.0

    def __call__(self) -> float:
        return self.now


class FakeSession:
    def __init__(self, fail: bool = False) -> None:
        self.said: list[tuple[str, dict[str, Any]]] = []
        self.fail = fail

    def say(self, text: str, **kw: Any) -> object:
        self.said.append((text, kw))
        if self.fail:
            raise RuntimeError("AgentSession isn't running")
        return object()


class FakeRunContext:
    def __init__(self, session: FakeSession | None = None) -> None:
        self.session = session or FakeSession()


# ---- retriever -----------------------------------------------------------------------------


async def test_retriever_sorts_and_caches_within_ttl() -> None:
    searcher = FakeSearcher([hit("low", 0.2), hit("high", 0.9)])
    clock = Clock()
    retriever = KnowledgeRetriever(searcher, KB, ttl=60, clock=clock)

    first = await retriever.search("  Хүргэлтийн   үнэ ", 5)
    assert [h.content for h in first] == ["high", "low"]
    assert searcher.calls == [(KB, "Хүргэлтийн   үнэ", 5)]

    clock.now += 59
    again = await retriever.search("хүргэлтийн үнэ", 5)  # same normalised query
    assert [h.content for h in again] == ["high", "low"]
    assert len(searcher.calls) == 1

    await retriever.search("хүргэлтийн үнэ", 3)  # different k is a different entry
    assert len(searcher.calls) == 2

    clock.now += 2  # first entry is now 61 s old
    await retriever.search("хүргэлтийн үнэ", 5)
    assert len(searcher.calls) == 3


async def test_retriever_lru_eviction_and_returned_lists_are_copies() -> None:
    searcher = FakeSearcher([hit("a", 0.5)])
    retriever = KnowledgeRetriever(searcher, KB, max_entries=2, clock=Clock())
    await retriever.search("one")
    await retriever.search("two")
    (await retriever.search("one")).clear()  # touch "one": "two" becomes the oldest
    await retriever.search("three")
    assert len(retriever) == 2
    assert len(searcher.calls) == 3

    assert [h.content for h in await retriever.search("one")] == ["a"]  # still cached
    assert len(searcher.calls) == 3
    await retriever.search("two")  # evicted
    assert len(searcher.calls) == 4


async def test_retriever_does_not_cache_errors() -> None:
    searcher = FakeSearcher(error=BackendError("down"))
    retriever = KnowledgeRetriever(searcher, KB, clock=Clock())
    with pytest.raises(BackendError):
        await retriever.search("q")
    searcher.error = None
    searcher.hits = [hit("ok", 0.4)]
    assert [h.content for h in await retriever.search("q")] == ["ok"]
    assert len(searcher.calls) == 2


# ---- formatting ----------------------------------------------------------------------------


def test_format_hits_orders_numbers_and_labels() -> None:
    text = format_hits(
        [
            hit("Second passage.", 0.3, filename="faq.md"),
            hit("Best passage.", 0.8, filename="price.pdf", heading="Хүргэлт  \n үнэ"),
            hit("No source.", 0.1, filename="", heading=""),
        ]
    )
    assert text == (
        "[1] price.pdf › Хүргэлт үнэ\nBest passage.\n\n[2] faq.md\nSecond passage.\n\n[3]\nNo source."
    )


def test_format_hits_dedups_by_content_prefix_and_skips_empty() -> None:
    body = "Дэлгүүр өдөр бүр 09:00-21:00 цагт ажиллана. " * 3
    text = format_hits(
        [
            hit(body + "A", 0.5, filename="a.txt"),
            hit(body + "B", 0.9, filename="b.txt"),
            hit("   ", 0.95),
            hit("Other.", 0.2, filename="c.txt"),
        ]
    )
    assert text.startswith("[1] b.txt\n")
    assert "a.txt" not in text
    assert "[2] c.txt\nOther." in text


def test_format_hits_respects_budget() -> None:
    hits = [hit(f"Passage {i} " + "үг " * 200, 1 - i / 10, filename=f"f{i}.md") for i in range(5)]
    for budget in (1800, 700, 300, 120, 40):
        text = format_hits(hits, max_chars=budget)
        assert 0 < len(text) <= budget, budget
        assert text.startswith("[1] f0.md")
    text = format_hits(hits, max_chars=1800)
    assert text.count("[") >= 2 and text.endswith("…")
    assert format_hits([]) == ""


def test_format_hits_keeps_whole_passages_when_they_fit() -> None:
    hits = [hit("short one", 0.9), hit("short two", 0.8)]
    assert "…" not in format_hits(hits, max_chars=1800)
    # the second passage is dropped rather than trimmed to a useless stub
    tight = format_hits([hit("x" * 50, 0.9), hit("y" * 500, 0.8)], max_chars=100)
    assert tight == "[1] guide.pdf\n" + "x" * 50


# ---- filler --------------------------------------------------------------------------------


async def test_run_with_filler_speaks_once_when_slow() -> None:
    said: list[str] = []

    async def slow() -> str:
        await asyncio.sleep(0.05)
        return "done"

    assert await run_with_filler(slow(), said.append, text="wait", delay=0.01) == "done"
    assert said == ["wait"]


async def test_run_with_filler_silent_when_fast_or_disabled() -> None:
    said: list[str] = []

    async def fast() -> int:
        return 7

    assert await run_with_filler(fast(), said.append, delay=0.05) == 7
    assert await run_with_filler(fast(), None, delay=0.0) == 7
    assert said == []


async def test_run_with_filler_propagates_errors_and_ignores_say_failures() -> None:
    def broken_say(_: str) -> None:
        raise RuntimeError("closed")

    async def failing() -> None:
        await asyncio.sleep(0.03)
        raise ValueError("boom")

    with pytest.raises(ValueError, match="boom"):
        await run_with_filler(failing(), broken_say, delay=0.0)


async def test_run_with_filler_cancels_work_when_cancelled() -> None:
    started = asyncio.Event()
    cancelled = asyncio.Event()

    async def work() -> None:
        started.set()
        try:
            await asyncio.sleep(10)
        except asyncio.CancelledError:
            cancelled.set()
            raise

    task = asyncio.create_task(run_with_filler(work(), None))
    await started.wait()
    task.cancel()
    with pytest.raises(asyncio.CancelledError):
        await task
    assert cancelled.is_set()


def test_session_filler_uses_session_say_without_chat_ctx() -> None:
    ctx = FakeRunContext()
    say = session_filler(ctx)
    assert say is not None
    say("hi")
    assert ctx.session.said == [("hi", {"add_to_chat_ctx": False})]
    assert session_filler(object()) is None


# ---- the tool ------------------------------------------------------------------------------


def make_tool(searcher: FakeSearcher, **kw: Any) -> tuple[Any, list[int]]:
    counts: list[int] = []
    retriever = KnowledgeRetriever(searcher, KB)
    return build_lookup_tool(retriever, on_lookup=counts.append, **kw), counts


def test_tool_schema_and_description() -> None:
    tool, _ = make_tool(FakeSearcher())
    schema = build_legacy_openai_schema(tool)["function"]
    assert schema["name"] == "lookup_knowledge"
    assert schema["parameters"]["required"] == ["question"]
    assert set(schema["parameters"]["properties"]) == {"question"}
    desc = schema["description"]
    assert "мэдлэгийн сан" in desc and "opening hours" in desc and "prices" in desc
    assert NOT_FOUND_REPLY in desc


async def test_tool_returns_passages_above_threshold() -> None:
    searcher = FakeSearcher(
        [
            hit("Хүргэлт 5000 төгрөг.", 0.72, filename="price.pdf", heading="Хүргэлт"),
            hit("Irrelevant.", 0.1),
        ]
    )
    tool, counts = make_tool(searcher)
    ctx = FakeRunContext()
    reply = await tool(ctx, question="  хүргэлт   хэд вэ ")
    assert reply == "[1] price.pdf › Хүргэлт\nХүргэлт 5000 төгрөг."
    assert searcher.calls == [(KB, "хүргэлт хэд вэ", 5)]
    assert counts == [1]
    assert ctx.session.said == []  # fast lookup: no filler


async def test_tool_not_found_when_nothing_scores() -> None:
    tool, counts = make_tool(FakeSearcher([hit("weak", 0.149)]))
    assert await tool(FakeRunContext(), question="ажлын цаг") == NOT_FOUND_REPLY
    tool, counts2 = make_tool(FakeSearcher([]))
    assert await tool(FakeRunContext(), question="ажлын цаг") == NOT_FOUND_REPLY
    assert counts == [0] and counts2 == [0]


async def test_tool_never_raises_on_backend_error(caplog: pytest.LogCaptureFixture) -> None:
    tool, counts = make_tool(FakeSearcher(error=BackendError("HTTP 500")))
    with caplog.at_level(logging.WARNING, logger="callgo.knowledge"):
        reply = await tool(FakeRunContext(), question="үнэ")
    assert reply == ERROR_REPLY
    assert "HTTP 500" in caplog.text
    assert counts == []


async def test_tool_times_out_into_error_reply() -> None:
    tool, _ = make_tool(FakeSearcher([hit("late", 0.9)], delay=1.0), timeout=0.02)
    ctx = FakeRunContext()
    assert await tool(ctx, question="үнэ") == ERROR_REPLY


async def test_tool_empty_question_does_not_search() -> None:
    searcher = FakeSearcher([hit("x", 0.9)])
    tool, counts = make_tool(searcher)
    reply = await tool(FakeRunContext(), question="   ")
    assert "хоосон" in reply
    assert searcher.calls == [] and counts == []


async def test_tool_says_filler_when_slow() -> None:
    searcher = FakeSearcher([hit("Нээлттэй.", 0.5)], delay=0.05)
    tool, counts = make_tool(searcher, filler_delay=0.01)
    ctx = FakeRunContext()
    assert await tool(ctx, question="цаг") == "[1] guide.pdf\nНээлттэй."
    assert ctx.session.said == [(FILLER_TEXT, {"add_to_chat_ctx": False})]
    assert counts == [1]
    assert FILLER_TEXT == "Түр хүлээгээрэй, шалгаад хэлье."

    # a session that cannot speak does not break the lookup
    ctx = FakeRunContext(FakeSession(fail=True))
    tool, _ = make_tool(FakeSearcher([hit("Нээлттэй.", 0.5)], delay=0.05), filler_delay=0.01)
    assert await tool(ctx, question="цаг") == "[1] guide.pdf\nНээлттэй."

    # filler disabled
    ctx = FakeRunContext()
    tool, _ = make_tool(FakeSearcher([], delay=0.03), filler_text=None, filler_delay=0.0)
    assert await tool(ctx, question="цаг") == NOT_FOUND_REPLY
    assert ctx.session.said == []


async def test_tool_uses_cache_and_survives_on_lookup_errors() -> None:
    searcher = FakeSearcher([hit("A", 0.9)])

    def bad_metrics(_: int) -> None:
        raise RuntimeError("metrics down")

    tool = build_lookup_tool(KnowledgeRetriever(searcher, KB), on_lookup=bad_metrics)
    assert await tool(FakeRunContext(), question="q") == "[1] guide.pdf\nA"
    assert await tool(FakeRunContext(), question="Q") == "[1] guide.pdf\nA"
    assert len(searcher.calls) == 1


# ---- instructions --------------------------------------------------------------------------


def test_tool_mode_instructions() -> None:
    info = KnowledgeInfo(id=KB, name="Үйлчилгээний гарын авлага", mode="tool")
    text = knowledge_instructions(info)
    assert text.startswith("# Company knowledge\n")
    assert '"Үйлчилгээний гарын авлага"' in text
    assert "ALWAYS call lookup_knowledge" in text
    assert "Answer only from the passages" in text
    assert "no lists" in text and "Мэдээлэл олдсонгүй" in text
    assert "a colleague will call them back" in text
    assert "transfer_call" not in text

    with_transfer = knowledge_instructions(info, ["end_call", "transfer_call"])
    assert "connect them to a colleague (transfer_call)" in with_transfer
    both = knowledge_instructions(info, ["transfer_call", "schedule_callback"])
    assert "transfer_call" in both and "schedule_callback" in both
    callback = knowledge_instructions(info, ["schedule_callback"])
    assert "arrange a callback" in callback and "transfer_call" not in callback


def test_context_mode_instructions_and_truncation() -> None:
    info = KnowledgeInfo(
        id=KB, name="FAQ", mode="context", context_text="  Ажлын цаг: 09-18.\nХаяг: БЗД  "
    )
    text = knowledge_instructions(info, ["transfer_call"])
    assert text.startswith("# Мэдлэгийн сан\n")
    assert "Ажлын цаг: 09-18.\nХаяг: БЗД\nKNOWLEDGE>>>" in text
    assert "only from the text" in text
    assert "(transfer_call)" in text
    assert "truncated" not in text
    assert "lookup_knowledge" not in text

    cut = knowledge_instructions(info.model_copy(update={"truncated": True}))
    assert "KNOWLEDGE>>>\n(The knowledge base was too long and has been truncated" in cut

    assert knowledge_instructions(info.model_copy(update={"context_text": " "})) == ""
    assert knowledge_instructions(info.model_copy(update={"mode": "off"})) == ""


def _boot(knowledge: KnowledgeInfo | None) -> Bootstrap:
    return Bootstrap(
        call=Call(
            id=uuid4(), org_id=ORG, direction=CallDirection.INBOUND, status=CallStatus.RINGING
        ),
        org=Organization(id=ORG, name="Demo", slug="demo"),
        profile=AgentProfile(id=uuid4(), org_id=ORG, name="Sara"),
        knowledge=knowledge,
    )


def test_active_knowledge() -> None:
    assert active_knowledge(_boot(None)) is None
    assert active_knowledge(_boot(KnowledgeInfo(id=KB, name="x", mode="off"))) is None
    assert active_knowledge(_boot(KnowledgeInfo(id=KB, name="x", mode="context"))) is None
    tool = KnowledgeInfo(id=KB, name="x", mode="tool")
    assert active_knowledge(_boot(tool)) == tool
    ctx = KnowledgeInfo(id=KB, name="x", mode="context", context_text="t")
    assert active_knowledge(_boot(ctx)) == ctx


def test_knowledge_info_parses_camel_case() -> None:
    info = KnowledgeInfo.model_validate(
        {"id": str(KB), "name": "n", "mode": "context", "contextText": "abc", "truncated": True}
    )
    assert info.context_text == "abc" and info.truncated
