"""Company knowledge base (RAG) for a call, driven by ``Bootstrap.knowledge`` (docs/API.md).

Two modes, chosen per agent profile:

* ``tool`` — the LLM gets a ``lookup_knowledge(question)`` function tool backed by
  ``POST /internal/agent/knowledge/search`` (hybrid pgvector + full-text search). While a slow
  search runs, a short Mongolian filler is spoken so the line does not go silent.
* ``context`` — the whole (small) base is appended to the system prompt under
  "# Мэдлэгийн сан".

:func:`knowledge_instructions` renders the prompt section for either mode.
"""

from __future__ import annotations

import asyncio
import contextlib
import logging
import time
from collections import OrderedDict
from collections.abc import Awaitable, Callable, Iterable, Sequence
from typing import Any, Protocol, TypeVar
from uuid import UUID

from livekit.agents.llm import FunctionTool, function_tool
from livekit.agents.voice import RunContext

from .schemas import Bootstrap, KnowledgeHit, KnowledgeInfo
from .tools import TOOL_LOOKUP_KNOWLEDGE, TOOL_SCHEDULE_CALLBACK, TOOL_TRANSFER_CALL

log = logging.getLogger("callgo.knowledge")

T = TypeVar("T")

DEFAULT_K = 5
MIN_SCORE = 0.15
MAX_PASSAGE_CHARS = 1800
CACHE_TTL_SEC = 60.0
CACHE_SIZE = 64
# Whole-lookup deadline (all retries included); the per-attempt HTTP timeout is 2.5 s.
LOOKUP_TIMEOUT_SEC = 6.0
FILLER_DELAY_SEC = 0.6
FILLER_TEXT = "Түр хүлээгээрэй, шалгаад хэлье."

NOT_FOUND_REPLY = "Мэдээлэл олдсонгүй"
EMPTY_QUESTION_REPLY = (
    "Асуулт хоосон байна. The question was empty: ask the customer what exactly they would "
    "like to know, then call lookup_knowledge again."
)
ERROR_REPLY = (
    "Мэдлэгийн сангаас хайж чадсангүй. The knowledge base is unavailable right now. Do not "
    "guess: tell the customer you cannot check this at the moment and offer to connect them "
    "to a colleague or to arrange a callback."
)

LOOKUP_DESCRIPTION = (
    "Компанийн мэдлэгийн сангаас мэдээлэл хайх. Бүтээгдэхүүн, үйлчилгээ, үнэ, журам, "
    "нөхцөл, ажлын цаг зэрэг компанийн талаарх баримт мэдээллийн асуултад хариулахаасаа "
    "өмнө заавал ашигла.\n"
    "Search the company knowledge base.\n"
    "Call it whenever the customer asks about products, services, prices, policies, "
    "procedures, opening hours, addresses or anything else factual about the company, "
    "before you answer. Pass the customer's question as a short, self-contained search "
    "query (in Mongolian when the customer speaks Mongolian).\n"
    'Returns numbered passages ("[1] file › section" followed by the text); answer only '
    'from them. Returns "Мэдээлэл олдсонгүй" when nothing relevant was found.'
)

_SEP = "\n\n"
_DEDUP_PREFIX = 80
_MIN_TRIMMED_CONTENT = 80


# ---- search ------------------------------------------------------------------------------


class KnowledgeSearcher(Protocol):
    """What the retriever needs from :class:`~callgo_agent.backend_client.BackendClient`."""

    async def knowledge_search(
        self, knowledge_base_id: UUID, query: str, k: int = DEFAULT_K
    ) -> list[KnowledgeHit]: ...


def _cache_key(query: str, k: int) -> tuple[str, int]:
    return " ".join(query.split()).casefold(), k


class KnowledgeRetriever:
    """Searches one knowledge base, with a small TTL'd LRU cache (query -> hits).

    Callers often repeat or rephrase-identically within a call (the LLM re-asks after an
    interruption); the cache avoids a second round trip. Errors are not cached.
    """

    def __init__(
        self,
        client: KnowledgeSearcher,
        knowledge_base_id: UUID,
        *,
        ttl: float = CACHE_TTL_SEC,
        max_entries: int = CACHE_SIZE,
        clock: Callable[[], float] = time.monotonic,
    ) -> None:
        self._client = client
        self.knowledge_base_id = knowledge_base_id
        self._ttl = ttl
        self._max_entries = max(1, max_entries)
        self._clock = clock
        self._cache: OrderedDict[tuple[str, int], tuple[float, list[KnowledgeHit]]] = OrderedDict()

    def __len__(self) -> int:
        return len(self._cache)

    async def search(self, query: str, k: int = DEFAULT_K) -> list[KnowledgeHit]:
        """Hits for ``query``, best first. Raises whatever the client raises."""
        key = _cache_key(query, k)
        now = self._clock()
        cached = self._cache.get(key)
        if cached is not None:
            stored_at, hits = cached
            if now - stored_at < self._ttl:
                self._cache.move_to_end(key)
                return list(hits)
            del self._cache[key]

        hits = await self._client.knowledge_search(self.knowledge_base_id, query.strip(), k)
        hits = sorted(hits, key=lambda h: h.score, reverse=True)
        self._cache[key] = (self._clock(), hits)
        self._cache.move_to_end(key)
        while len(self._cache) > self._max_entries:
            self._cache.popitem(last=False)
        return list(hits)


# ---- formatting --------------------------------------------------------------------------


def _hit_header(index: int, hit: KnowledgeHit) -> str:
    label = " › ".join(p for p in (hit.filename.strip(), " ".join(hit.heading.split())) if p)
    return f"[{index}] {label}" if label else f"[{index}]"


def _trim(text: str, limit: int) -> str:
    if len(text) <= limit:
        return text
    if limit <= 1:
        return "…"[:limit]
    cut = text[: limit - 1]
    ws = cut.rfind(" ")
    if ws > limit // 2:
        cut = cut[:ws]
    return cut.rstrip() + "…"


def format_hits(hits: Iterable[KnowledgeHit], max_chars: int = MAX_PASSAGE_CHARS) -> str:
    """Render hits as ``[n] <filename> › <heading>\\n<content>`` blocks within ``max_chars``.

    Best score first; passages whose content starts the same (first 80 normalised chars)
    as a better one are dropped; the last block that does not fit is trimmed with "…"
    (or left out when too little room remains). Returns ``""`` for no hits.
    """
    ordered = sorted(hits, key=lambda h: h.score, reverse=True)
    seen: set[str] = set()
    blocks: list[str] = []
    used = 0
    for hit in ordered:
        content = hit.content.strip()
        if not content:
            continue
        prefix = " ".join(content.split())[:_DEDUP_PREFIX].casefold()
        if prefix in seen:
            continue
        seen.add(prefix)

        header = _hit_header(len(blocks) + 1, hit)
        sep = len(_SEP) if blocks else 0
        room = max_chars - used - sep
        block = f"{header}\n{content}"
        if len(block) <= room:
            blocks.append(block)
            used += sep + len(block)
            continue
        content_room = room - len(header) - 1
        if content_room >= _MIN_TRIMMED_CONTENT or (not blocks and content_room > 0):
            blocks.append(f"{header}\n{_trim(content, content_room)}")
        elif not blocks:
            blocks.append(_trim(block, max_chars))
        break
    return _SEP.join(blocks)


# ---- filler speech -----------------------------------------------------------------------


async def run_with_filler(
    work: Awaitable[T],
    say: Callable[[str], object] | None,
    *,
    text: str = FILLER_TEXT,
    delay: float = FILLER_DELAY_SEC,
) -> T:
    """Await ``work``; if it is still running after ``delay`` seconds, ``say(text)`` once.

    ``say`` is typically ``lambda t: session.say(t, add_to_chat_ctx=False)``; ``None``
    disables the filler. A failing ``say`` is logged and ignored.
    """
    task: asyncio.Future[T] = asyncio.ensure_future(work)
    if say is None or delay < 0:
        return await task
    try:
        done, _ = await asyncio.wait({task}, timeout=delay)
        if not done:
            try:
                say(text)
            except Exception as exc:  # noqa: BLE001 - the filler is best effort
                log.debug("filler speech failed: %s", exc)
        return await task
    finally:
        if not task.done():
            task.cancel()
            with contextlib.suppress(BaseException):
                await task


def session_filler(ctx: Any) -> Callable[[str], object] | None:
    """A ``say`` function speaking through ``ctx.session`` (a :class:`RunContext`), if any."""
    session = getattr(ctx, "session", None)
    say = getattr(session, "say", None)
    if say is None:
        return None

    def _say(text: str) -> object:
        return say(text, add_to_chat_ctx=False)

    return _say


# ---- the tool ----------------------------------------------------------------------------


def build_lookup_tool(
    retriever: KnowledgeRetriever,
    *,
    on_lookup: Callable[[int], None] | None = None,
    k: int = DEFAULT_K,
    min_score: float = MIN_SCORE,
    max_chars: int = MAX_PASSAGE_CHARS,
    timeout: float = LOOKUP_TIMEOUT_SEC,
    filler_text: str | None = FILLER_TEXT,
    filler_delay: float = FILLER_DELAY_SEC,
) -> FunctionTool[Any, Any]:
    """The ``lookup_knowledge(question)`` function tool.

    It never raises: failures come back as a string the LLM can relay. ``on_lookup`` is
    called with the number of usable passages (score ≥ ``min_score``) after every completed
    search, for metrics.
    """

    async def lookup_knowledge(ctx: RunContext, question: str) -> str:
        """Search the company knowledge base.

        Args:
            question: The customer's question as a short search query, e.g. "хүргэлтийн үнэ".
        """
        query = " ".join(question.split())
        if not query:
            return EMPTY_QUESTION_REPLY
        say = session_filler(ctx) if filler_text else None
        started = time.monotonic()
        try:
            hits = await run_with_filler(
                asyncio.wait_for(retriever.search(query, k), timeout),
                say,
                text=filler_text or "",
                delay=filler_delay,
            )
        except Exception as exc:  # noqa: BLE001 - reported to the LLM, never raised
            log.warning("knowledge lookup %r failed: %r", query[:120], exc)
            return ERROR_REPLY
        relevant = [h for h in hits if h.score >= min_score]
        log.info(
            "knowledge lookup %r: %d/%d hits >= %.2f in %.0f ms",
            query[:120],
            len(relevant),
            len(hits),
            min_score,
            (time.monotonic() - started) * 1000,
        )
        if on_lookup is not None:
            try:
                on_lookup(len(relevant))
            except Exception:
                log.exception("on_lookup callback failed")
        passages = format_hits(relevant, max_chars=max_chars)
        return passages or NOT_FOUND_REPLY

    return function_tool(
        lookup_knowledge, name=TOOL_LOOKUP_KNOWLEDGE, description=LOOKUP_DESCRIPTION
    )


# ---- prompt ------------------------------------------------------------------------------


def active_knowledge(bootstrap: Bootstrap) -> KnowledgeInfo | None:
    """``bootstrap.knowledge`` when it is usable for this call, else ``None``.

    ``context`` mode needs a non-empty ``contextText``.
    """
    info = bootstrap.knowledge
    if info is None or info.mode == "off":
        return None
    if info.mode == "context" and not info.context_text.strip():
        log.warning("knowledge base %s is in context mode but has no text", info.id)
        return None
    return info


def _fallback_offer(available_tools: Sequence[str]) -> str:
    tools = set(available_tools)
    if TOOL_TRANSFER_CALL in tools and TOOL_SCHEDULE_CALLBACK in tools:
        return (
            "offer to connect them to a colleague (transfer_call) or to arrange a callback "
            "(schedule_callback)"
        )
    if TOOL_TRANSFER_CALL in tools:
        return "offer to connect them to a colleague (transfer_call)"
    if TOOL_SCHEDULE_CALLBACK in tools:
        return "offer to arrange a callback from a colleague (schedule_callback)"
    return "offer that a colleague will call them back with the answer"


def knowledge_instructions(info: KnowledgeInfo, available_tools: Sequence[str] = ()) -> str:
    """The system-prompt section for ``info.mode`` (``""`` for ``off``).

    ``available_tools`` are the other tool names enabled for the call; they decide what the
    agent offers when the answer is not in the knowledge base.
    """
    offer = _fallback_offer(available_tools)
    name = f' ("{info.name}")' if info.name.strip() else ""
    if info.mode == "tool":
        return (
            "# Company knowledge\n"
            f"You can search the company knowledge base{name} with the lookup_knowledge "
            "tool. ALWAYS call lookup_knowledge before answering any factual question about "
            "the company: products, services, prices, policies, procedures, opening hours, "
            "addresses and the like. Never answer such questions from memory and never "
            "guess.\n"
            "Answer only from the passages it returns. Keep the answer short and spoken: one "
            "or two plain sentences, no lists, and never read out passage numbers, file "
            "names, headings or sources.\n"
            "Do not announce the search yourself; a short waiting phrase is played "
            "automatically when it takes a moment.\n"
            'If it returns "Мэдээлэл олдсонгүй" or the passages do not answer the question, '
            f"say honestly that you do not have that information and {offer}."
        )
    if info.mode == "context":
        text = info.context_text.strip()
        if not text:
            return ""
        truncated = (
            "\n(The knowledge base was too long and has been truncated here; some "
            "information may be missing.)"
            if info.truncated
            else ""
        )
        return (
            "# Мэдлэгийн сан\n"
            f"Company knowledge base{name}. Answer factual questions about the company "
            "(products, services, prices, policies, procedures, opening hours and the like) "
            "only from the text between the markers below, never from memory or guesses.\n"
            "<<<KNOWLEDGE\n"
            f"{text}\n"
            f"KNOWLEDGE>>>{truncated}\n"
            "Keep answers short and spoken: one or two plain sentences, no lists, and never "
            "read out document names, headings or sources. If the answer is not in this "
            f"text, say honestly that you do not have that information and {offer}."
        )
    return ""
