from __future__ import annotations

import asyncio
import sys
import types
from collections import defaultdict
from collections.abc import AsyncIterator, Callable
from datetime import datetime
from typing import Any, ClassVar, Self
from uuid import UUID
from zoneinfo import ZoneInfo

import pytest
from livekit import rtc
from livekit.agents import (
    Agent,
    AgentStateChangedEvent,
    CloseEvent,
    CloseReason,
    ConversationItemAddedEvent,
    UserInputTranscribedEvent,
    UserStateChangedEvent,
    UserTurnExceededEvent,
    llm,
    stt,
)
from livekit.agents.metrics import LLMMetrics, STTMetrics

from callgo_agent import routing
from callgo_agent import session as sess
from callgo_agent.config import settings
from callgo_agent.events import EventEmitter
from callgo_agent.schemas import (
    AgentProfile,
    Bootstrap,
    Call,
    CallDirection,
    CallStatus,
    CampaignInfo,
    CampaignOutcome,
    Contact,
    EntitlementInfo,
    Event,
    HandoffInfo,
    JobMetadata,
    KnowledgeHit,
    KnowledgeInfo,
    LexiconEntry,
    LexiconScope,
    LLMConfig,
    LLMProvider,
    MenuOption,
    Organization,
    ResolvedRoute,
    Sentiment,
    Speaker,
    TranscriptTurn,
)
from callgo_agent.session import (
    CallFinalizer,
    CallGoAgent,
    CallRecorder,
    PipelineFactories,
    SipInfo,
    analysis_prompt,
    analyze_call,
    build_instructions,
    build_turn_handling,
    call_tools,
    enabled_tool_names,
    end_reason_for_close,
    enforce_max_duration,
    fallback_analysis,
    final_outcome,
    outbound_opening_instructions,
    parse_analysis,
    parse_job_metadata,
    render_greeting,
    resolve_numbers,
    run_call,
    select_llm_chain,
    sentence_chunks,
    substitute_vars,
    template_vars,
    wait_for_answer,
)
from callgo_agent.tools import CallbackRequest, CallState
from callgo_agent.usage import CostRates, UsageTracker

ORG = UUID("11111111-1111-1111-1111-111111111111")
CALL = UUID("22222222-2222-2222-2222-222222222222")
HIT_ID = UUID("55555555-5555-5555-5555-555555555555")


def make_llm_config(name: str = "primary", model: str = "gemini-2.5-flash") -> LLMConfig:
    return LLMConfig(
        id=UUID(int=len(name)), org_id=ORG, name=name, provider=LLMProvider.GOOGLE, model=model
    )


def make_bootstrap(
    *,
    direction: CallDirection = CallDirection.INBOUND,
    language: str = "mn",
    system_prompt: str = "Та {{org}}-ийн туслах {{agent_name}}. Харилцагч: {{name}}.",
    greeting: str = "Сайн байна уу {{name}}!",
    contact: Contact | None = None,
    campaign: CampaignInfo | None = None,
    lexicon: list[LexiconEntry] | None = None,
    tools: list[str] | None = None,
    llm_cfg: LLMConfig | None = None,
    knowledge: KnowledgeInfo | None = None,
    transfer_number: str = "",
) -> Bootstrap:
    return Bootstrap(
        call=Call(
            id=CALL,
            org_id=ORG,
            direction=direction,
            status=CallStatus.RINGING,
            from_number="+97699112233",
            to_number="+97677001100",
            room_name="call-1",
        ),
        org=Organization(id=ORG, name="Demo LLC", slug="demo"),
        profile=AgentProfile(
            id=UUID(int=3),
            org_id=ORG,
            name="Сараа",
            system_prompt=system_prompt,
            greeting=greeting,
            language=language,
            tools=tools or [],
            transfer_number=transfer_number,
        ),
        llm=llm_cfg,
        knowledge=knowledge,
        lexicon=lexicon or [],
        contact=contact,
        campaign=campaign,
    )


CONTACT = Contact(
    id=UUID(int=9),
    org_id=ORG,
    phone="+97699112233",
    name="Бат",
    meta={"order": "A-17", "discount": "30%"},
)
CAMPAIGN = CampaignInfo(
    id=UUID(int=8),
    name="Намрын урамшуулал",
    script="{{name}}-д {{discount}} хөнгөлөлт санал болго. Захиалга: {{ order }}. {{missing}}",
    vars={"discount": "20%", "product": "Tea"},
)
OUTCOMES = [
    CampaignOutcome(code="agreed", label="Зөвшөөрсөн", description="Санал болголтыг хүлээн авсан"),
    CampaignOutcome(code="declined", label="Татгалзсан", description="Сонирхолгүй гэсэн"),
    CampaignOutcome(code="callback", label="Дахин залгах", terminal=False),
    CampaignOutcome(code="no_contact", label="Холбогдоогүй"),
]
OUTCOME_CAMPAIGN = CAMPAIGN.model_copy(update={"outcomes": OUTCOMES})


# ---- templates & instructions --------------------------------------------------------


def test_substitute_vars() -> None:
    assert (
        substitute_vars("Hi {{name}}, {{ x }}!{{nope}}", {"name": "Bat", "x": "1"}) == "Hi Bat, 1!"
    )
    assert substitute_vars("no vars", {}) == "no vars"


def test_template_vars_precedence() -> None:
    v = template_vars(make_bootstrap(contact=CONTACT, campaign=CAMPAIGN))
    assert v["name"] == "Бат"
    assert v["org"] == "Demo LLC"
    assert v["product"] == "Tea"  # campaign var
    assert v["discount"] == "30%"  # contact meta overrides campaign var
    assert v["campaign"] == "Намрын урамшуулал"
    no_contact = template_vars(make_bootstrap())
    assert no_contact["phone"] == "+97699112233" and no_contact["name"] == ""


def test_build_instructions_mn() -> None:
    boot = make_bootstrap(contact=CONTACT, campaign=CAMPAIGN)
    text = build_instructions(boot, now="2026-09-30 Wednesday 10:00")
    assert text.startswith("Та Demo LLC-ийн туслах Сараа. Харилцагч: Бат.")
    assert "Бат-д 30% хөнгөлөлт санал болго. Захиалга: A-17." in text
    assert "{{" not in text
    assert "Name: Бат" in text and "order: A-17" in text
    assert "The customer called you." in text
    assert "Always answer in Mongolian" in text
    assert "2026-09-30 Wednesday 10:00" in text


def test_build_instructions_outcome_section() -> None:
    text = build_instructions(make_bootstrap(campaign=OUTCOME_CAMPAIGN))
    assert "# Call outcome" in text
    assert "- agreed: Зөвшөөрсөн — Санал болголтыг хүлээн авсан" in text
    assert "- callback: Дахин залгах\n" in text  # no description -> no dash
    assert "- no_contact: Холбогдоогүй" in text
    assert "Never read them" in text
    assert "record_outcome" in text
    assert text.index("# Call outcome") < text.index("# Language")

    assert "# Call outcome" not in build_instructions(make_bootstrap(campaign=CAMPAIGN))
    assert "# Call outcome" not in build_instructions(make_bootstrap())


def test_build_instructions_other_language_and_defaults() -> None:
    boot = make_bootstrap(language="en", system_prompt="", direction=CallDirection.OUTBOUND)
    text = build_instructions(boot)
    assert "You are Сараа" in text
    assert "Always answer in English." in text
    assert "Mongolian" not in text
    assert "outbound call" in text


KB_ID = UUID("66666666-6666-6666-6666-666666666666")
KB_TOOL = KnowledgeInfo(id=KB_ID, name="Гарын авлага", mode="tool")
KB_CONTEXT = KnowledgeInfo(
    id=KB_ID, name="Гарын авлага", mode="context", context_text="Ажлын цаг: 09:00-18:00."
)


def test_build_instructions_knowledge_tool_mode() -> None:
    boot = make_bootstrap(
        knowledge=KB_TOOL, tools=["transfer_call", "end_call"], transfer_number="+97611"
    )
    text = build_instructions(boot)
    assert "# Company knowledge" in text
    assert "ALWAYS call lookup_knowledge" in text
    assert "(transfer_call)" in text
    assert "# Мэдлэгийн сан" not in text
    # static knowledge section sits right after the persona, before per-call context
    assert text.index("# Company knowledge") < text.index("# Customer") < text.index("# Language")

    no_transfer = build_instructions(make_bootstrap(knowledge=KB_TOOL, tools=["transfer_call"]))
    assert "transfer_call" not in no_transfer  # enabled but no number: not offered


def test_build_instructions_knowledge_context_mode() -> None:
    text = build_instructions(make_bootstrap(knowledge=KB_CONTEXT))
    assert "# Мэдлэгийн сан" in text
    assert "Ажлын цаг: 09:00-18:00." in text
    assert "truncated" not in text
    assert "lookup_knowledge" not in text and "# Company knowledge" not in text

    cut = build_instructions(
        make_bootstrap(knowledge=KB_CONTEXT.model_copy(update={"truncated": True}))
    )
    assert "has been truncated" in cut


def test_build_instructions_without_knowledge() -> None:
    for knowledge in (None, KB_TOOL.model_copy(update={"mode": "off"})):
        text = build_instructions(make_bootstrap(knowledge=knowledge))
        assert "# Company knowledge" not in text and "# Мэдлэгийн сан" not in text


def test_enabled_tool_names() -> None:
    boot = make_bootstrap(tools=["schedule_callback", "transfer_call", "x", "end_call"])
    assert enabled_tool_names(boot.profile) == ["end_call", "schedule_callback"]
    boot = make_bootstrap(tools=["transfer_call"], transfer_number="+97611")
    assert enabled_tool_names(boot.profile) == ["transfer_call"]


class FakeSearchClient:
    def __init__(self) -> None:
        self.queries: list[tuple[UUID, str, int]] = []

    async def knowledge_search(self, knowledge_base_id: UUID, query: str, k: int = 5) -> list[Any]:
        self.queries.append((knowledge_base_id, query, k))
        return [
            KnowledgeHit(
                chunk_id=UUID(int=1), document_id=UUID(int=2), content="09:00-18:00", score=0.9
            )
        ]


async def test_call_tools_adds_lookup_only_in_tool_mode() -> None:
    client = FakeSearchClient()
    state = CallState(bootstrap=make_bootstrap(knowledge=KB_TOOL, tools=["end_call"]))
    tools = call_tools(state, FakeControl(), client)
    assert [t.info.name for t in tools] == ["end_call", "lookup_knowledge"]  # type: ignore[union-attr]

    lookup = tools[-1]
    ctx = types.SimpleNamespace(session=types.SimpleNamespace(say=lambda *a, **k: None))
    assert "09:00-18:00" in await lookup(ctx, question="ажлын цаг")  # type: ignore[operator]
    assert client.queries == [(KB_ID, "ажлын цаг", 5)]
    assert (state.knowledge_lookups, state.knowledge_misses) == (1, 0)

    for knowledge in (KB_CONTEXT, None, KB_TOOL.model_copy(update={"mode": "off"})):
        state = CallState(bootstrap=make_bootstrap(knowledge=knowledge, tools=["end_call"]))
        assert [t.info.name for t in call_tools(state, FakeControl(), client)] == ["end_call"]  # type: ignore[union-attr]


def test_greeting_and_outbound_opening() -> None:
    boot = make_bootstrap(contact=CONTACT)
    assert render_greeting(boot) == "Сайн байна уу Бат!"
    opening = outbound_opening_instructions(boot)
    assert "answered your outbound call" in opening
    assert '"Сайн байна уу Бат!"' in opening
    assert "Start with" not in outbound_opening_instructions(make_bootstrap(greeting=""))


# ---- metadata / SIP -----------------------------------------------------------------


def test_parse_job_metadata() -> None:
    assert parse_job_metadata("") == JobMetadata()
    assert parse_job_metadata(None) == JobMetadata()
    assert parse_job_metadata("not json") == JobMetadata()
    assert parse_job_metadata("[1]") == JobMetadata()
    assert parse_job_metadata('{"callId": "bad"}') == JobMetadata()
    meta = parse_job_metadata(
        f'{{"callId": "{CALL}", "campaignId": "{UUID(int=8)}", '
        '"direction": "outbound", "toNumber": "+976"}'
    )
    assert meta.call_id == CALL
    assert meta.campaign_id == UUID(int=8)
    assert meta.direction == CallDirection.OUTBOUND
    assert meta.to_number == "+976"


def test_sip_info_and_numbers() -> None:
    sip = SipInfo.from_attributes(
        "sip_1",
        {
            "sip.phoneNumber": "+97699112233",
            "sip.trunkPhoneNumber": "+97677001100",
            "sip.callID": "abc",
            "sip.callStatus": "active",
        },
    )
    assert (sip.phone_number, sip.trunk_phone_number, sip.call_id, sip.call_status) == (
        "+97699112233",
        "+97677001100",
        "abc",
        "active",
    )
    assert resolve_numbers(JobMetadata(), sip) == ("+97699112233", "+97677001100")
    out = JobMetadata(direction=CallDirection.OUTBOUND)
    assert resolve_numbers(out, sip) == ("+97677001100", "+97699112233")
    assert resolve_numbers(out.model_copy(update={"to_number": "+1"}), sip)[1] == "+1"


# ---- normalizer hooks -------------------------------------------------------------------


class FakeNormalizer:
    def __init__(self) -> None:
        self.stt_calls: list[tuple[str, list[LexiconEntry]]] = []
        self.tts_calls: list[tuple[str, list[LexiconEntry]]] = []

    def module(self) -> types.ModuleType:
        mod = types.ModuleType("callgo_agent.normalizer")

        def normalize_stt(text: str, lexicon: list[LexiconEntry]) -> tuple[str, list[LexiconEntry]]:
            self.stt_calls.append((text, lexicon))
            hits = [e for e in lexicon if e.wrong in text]
            for e in hits:
                text = text.replace(e.wrong, e.correct)
            return text, hits

        def normalize_for_tts(text: str, lexicon: list[LexiconEntry]) -> str:
            self.tts_calls.append((text, lexicon))
            return text.replace("3", "гурав").upper()

        mod.normalize_stt = normalize_stt  # type: ignore[attr-defined]
        mod.normalize_for_tts = normalize_for_tts  # type: ignore[attr-defined]
        return mod


LEXICON = [
    LexiconEntry(wrong="калл го", correct="CallGo", scope=LexiconScope.STT, id=HIT_ID),
    LexiconEntry(wrong="SMS", correct="эс эм эс", scope=LexiconScope.TTS),
    LexiconEntry(wrong="ххк", correct="ХХК", scope=LexiconScope.BOTH),
]


@pytest.fixture
def normalizer(monkeypatch: pytest.MonkeyPatch) -> FakeNormalizer:
    fake = FakeNormalizer()
    monkeypatch.setitem(sys.modules, "callgo_agent.normalizer", fake.module())
    return fake


def _speech(kind: stt.SpeechEventType, text: str, confidence: float = 0.8) -> stt.SpeechEvent:
    return stt.SpeechEvent(
        type=kind,
        alternatives=[stt.SpeechData(language="mn", text=text, confidence=confidence)],
    )


def test_stt_hook_normalizes_and_reports_finals(normalizer: FakeNormalizer) -> None:
    finals: list[tuple[str, str, float, list[LexiconEntry]]] = []
    agent = CallGoAgent(
        bootstrap=make_bootstrap(lexicon=LEXICON),
        on_stt_final=lambda *a: finals.append(a),
    )
    final = _speech(stt.SpeechEventType.FINAL_TRANSCRIPT, "калл го ххк сайн уу", 0.9)
    out = agent.process_speech_event(final)
    assert isinstance(out, stt.SpeechEvent)
    assert out.alternatives[0].text == "CallGo ХХК сайн уу"
    assert out.alternatives[0].confidence == 0.9
    assert final.alternatives[0].text == "калл го ххк сайн уу"  # original not mutated
    # STT only sees stt + both scoped entries
    assert [e.wrong for e in normalizer.stt_calls[0][1]] == ["калл го", "ххк"]
    assert len(finals) == 1
    text, raw, conf, hits = finals[0]
    assert (text, raw, conf) == ("CallGo ХХК сайн уу", "калл го ххк сайн уу", 0.9)
    assert [h.id for h in hits] == [HIT_ID, None]

    interim = agent.process_speech_event(_speech(stt.SpeechEventType.INTERIM_TRANSCRIPT, "калл го"))
    assert isinstance(interim, stt.SpeechEvent) and interim.alternatives[0].text == "CallGo"
    assert len(finals) == 1  # interim results are not reported

    eos = stt.SpeechEvent(type=stt.SpeechEventType.END_OF_SPEECH)
    assert agent.process_speech_event(eos) is eos


def test_stt_hook_passes_through_when_normalizer_fails(monkeypatch: pytest.MonkeyPatch) -> None:
    broken = types.ModuleType("callgo_agent.normalizer")

    def boom(*_: Any) -> Any:
        raise ValueError("boom")

    broken.normalize_stt = boom  # type: ignore[attr-defined]
    broken.normalize_for_tts = boom  # type: ignore[attr-defined]
    monkeypatch.setitem(sys.modules, "callgo_agent.normalizer", broken)
    agent = CallGoAgent(bootstrap=make_bootstrap(lexicon=LEXICON))
    ev = _speech(stt.SpeechEventType.FINAL_TRANSCRIPT, "калл го")
    assert agent.process_speech_event(ev) is ev
    assert agent.normalize_for_tts(" 3 SMS ") == " 3 SMS "


def test_hooks_pass_through_when_normalizer_missing(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setitem(sys.modules, "callgo_agent.normalizer", None)
    agent = CallGoAgent(bootstrap=make_bootstrap(lexicon=LEXICON))
    ev = _speech(stt.SpeechEventType.FINAL_TRANSCRIPT, "калл го")
    assert agent.process_speech_event(ev) is ev
    assert agent.normalize_for_tts("SMS") == "SMS"


async def _aiter(items: list[Any]) -> AsyncIterator[Any]:
    for item in items:
        await asyncio.sleep(0)
        yield item


async def test_sentence_chunks() -> None:
    sentinel = object()
    tokens = ["Сайн ", "байна уу. ", "Үнэ ", "3.5 ", "сая! Та", "ны захиалга", sentinel, " дууслаа"]
    out = [c async for c in sentence_chunks(_aiter(tokens), lambda s: f"<{s}>")]
    assert out == [
        "<Сайн байна уу. >",
        "<Үнэ 3.5 сая! >",
        "<Таны захиалга>",
        sentinel,
        "< дууслаа>",
    ]


async def test_sentence_chunks_splits_long_text_on_whitespace() -> None:
    text = "үг " * 100
    out = [c async for c in sentence_chunks(_aiter([text]), lambda s: s, max_chars=50)]
    assert "".join(out) == text
    assert all(len(c) <= 50 for c in out)


async def test_stt_and_tts_nodes_apply_normalizer(
    normalizer: FakeNormalizer, monkeypatch: pytest.MonkeyPatch
) -> None:
    seen_tts_text: list[str] = []

    async def fake_default_stt(agent: Any, audio: Any, ms: Any) -> AsyncIterator[Any]:
        yield _speech(stt.SpeechEventType.FINAL_TRANSCRIPT, "калл го")

    async def fake_default_tts(agent: Any, text: Any, ms: Any) -> AsyncIterator[str]:
        async for chunk in text:
            seen_tts_text.append(chunk)
        yield "frame"

    monkeypatch.setattr(Agent.default, "stt_node", fake_default_stt)
    monkeypatch.setattr(Agent.default, "tts_node", fake_default_tts)
    agent = CallGoAgent(bootstrap=make_bootstrap(lexicon=LEXICON))

    events = [e async for e in agent.stt_node(_aiter([]), None)]  # type: ignore[arg-type]
    assert events[0].alternatives[0].text == "CallGo"

    frames = [f async for f in agent.tts_node(_aiter(["Үнэ 3 ", "SMS. ", "за"]), None)]  # type: ignore[arg-type]
    assert frames == ["frame"]
    assert seen_tts_text == ["ҮНЭ ГУРАВ SMS. ", "ЗА"]
    # TTS sees tts + both scoped entries
    assert [e.wrong for e in normalizer.tts_calls[0][1]] == ["SMS", "ххк"]


class FakeSpeechHandle:
    def __init__(self) -> None:
        self.interrupted = False

    async def wait_for_playout(self) -> None:
        await asyncio.sleep(0)

    def interrupt(self, *, force: bool = False) -> None:
        self.interrupted = True

    def __await__(self) -> Any:
        return self.wait_for_playout().__await__()


class FakeAgentSession:
    """Minimal stand-in for AgentSession (event emitter + say/generate_reply/shutdown)."""

    instances: ClassVar[list[FakeAgentSession]] = []

    def __init__(self, **kw: Any) -> None:
        self.kw = kw
        self.handlers: dict[str, list[Callable[[Any], None]]] = defaultdict(list)
        self.said: list[tuple[str, dict[str, Any]]] = []
        self.replies: list[dict[str, Any]] = []
        self.shutdowns: list[bool] = []
        self.started = False
        self.agent: Any = None
        self.room_options: Any = None
        self.agents: list[Any] = []
        self.interrupts: list[bool] = []
        self.closed = False
        FakeAgentSession.instances.append(self)

    def on(self, name: str, cb: Callable[[Any], None]) -> Callable[[Any], None]:
        self.handlers[name].append(cb)
        return cb

    def off(self, name: str, cb: Callable[[Any], None]) -> None:
        if cb in self.handlers[name]:
            self.handlers[name].remove(cb)

    def update_agent(self, agent: Any) -> None:
        self.agent = agent
        self.agents.append(agent)

    def interrupt(self, *, force: bool = False) -> asyncio.Future[None]:
        self.interrupts.append(force)
        fut: asyncio.Future[None] = asyncio.get_running_loop().create_future()
        fut.set_result(None)
        return fut

    async def aclose(self) -> None:
        self.closed = True

    def emit(self, name: str, ev: Any) -> None:
        for cb in list(self.handlers[name]):
            cb(ev)

    async def start(self, agent: Any, *, room: Any, room_options: Any) -> None:
        self.agent, self.room_options, self.started = agent, room_options, True
        self.agents.append(agent)

    def say(self, text: str, **kw: Any) -> FakeSpeechHandle:
        self.said.append((text, kw))
        return FakeSpeechHandle()

    def generate_reply(self, **kw: Any) -> FakeSpeechHandle:
        self.replies.append(kw)
        return FakeSpeechHandle()

    def shutdown(self, *, drain: bool = True) -> None:
        self.shutdowns.append(drain)
        asyncio.get_running_loop().call_soon(
            self.emit, "close", CloseEvent(reason=CloseReason.USER_INITIATED)
        )


async def test_on_enter_inbound_says_greeting_outbound_generates(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    fake = FakeAgentSession()
    monkeypatch.setattr(CallGoAgent, "session", property(lambda self: fake))

    await CallGoAgent(bootstrap=make_bootstrap(contact=CONTACT)).on_enter()
    assert fake.said == [("Сайн байна уу Бат!", {})]

    await CallGoAgent(bootstrap=make_bootstrap(greeting="")).on_enter()
    assert "Greet the caller" in fake.replies[-1]["instructions"]

    await CallGoAgent(bootstrap=make_bootstrap(direction=CallDirection.OUTBOUND)).on_enter()
    assert "outbound call" in fake.replies[-1]["instructions"]
    assert len(fake.said) == 1


# ---- recorder -------------------------------------------------------------------------


class FakeSink:
    def __init__(self) -> None:
        self.events: list[Event] = []
        self.hits: list[list[str]] = []
        self.closed = False

    async def post_events(self, events: list[Event]) -> int:
        self.events.extend(events)
        return len(events)

    async def lexicon_hit(self, ids: Any) -> None:
        self.hits.append([str(i) for i in ids])

    def of(self, type_: str) -> list[Event]:
        return [e for e in self.events if e.type == type_]


class Clock:
    def __init__(self, t: float = 1000.0) -> None:
        self.t = t

    def __call__(self) -> float:
        return self.t


async def test_recorder_maps_session_events() -> None:
    sink = FakeSink()
    emitter = EventEmitter(sink, org_id=ORG, call_id=CALL, flush_interval_ms=60_000)
    clock = Clock()
    rec = CallRecorder(emitter, make_bootstrap(), llm_label="google/x", hit_sink=sink, clock=clock)
    rec.mark_answered()

    rec.on_user_state_changed(
        UserStateChangedEvent(old_state="listening", new_state="speaking", created_at=1001.0)
    )
    rec.on_user_input_transcribed(
        UserInputTranscribedEvent(transcript="калл", is_final=False, created_at=1001.5)
    )
    rec.on_stt_final("CallGo сайн уу", "калл го сайн уу", 0.7, [LEXICON[0], LEXICON[2]])
    rec.on_user_input_transcribed(
        UserInputTranscribedEvent(transcript="CallGo сайн уу", is_final=True, created_at=1002.25)
    )
    msg = llm.ChatMessage(
        role="assistant",
        content=["Сайн байна уу, юугаар туслах вэ?"],
        metrics={"started_speaking_at": 1003.0, "stopped_speaking_at": 1004.5},
    )
    rec.on_conversation_item_added(ConversationItemAddedEvent(item=msg))
    rec.on_conversation_item_added(
        ConversationItemAddedEvent(item=llm.ChatMessage(role="user", content=["ignored"]))
    )
    for state in ("listening", "thinking", "thinking", "speaking"):
        rec.on_agent_state_changed(
            AgentStateChangedEvent(old_state="idle", new_state=state)  # type: ignore[arg-type]
        )
    await rec.drain()
    await emitter.flush()

    partial = sink.of("transcript.partial")[0].payload
    assert partial == {"speaker": "customer", "text": "калл", "startMs": 1000}

    finals = [e.payload["turn"] for e in sink.of("transcript.final")]
    assert finals[0] == {
        "callId": str(CALL),
        "seq": 1,
        "speaker": "customer",
        "text": "CallGo сайн уу",
        "rawText": "калл го сайн уу",
        "confidence": 0.7,
        "startMs": 1000,
        "endMs": 2250,
        "isFinal": True,
    }
    assert finals[1]["speaker"] == "agent"
    assert finals[1]["seq"] == 2
    assert (finals[1]["startMs"], finals[1]["endMs"]) == (3000, 4500)
    assert len(finals) == 2

    states = [e.payload for e in sink.of("agent.state")]
    assert [s["state"] for s in states] == ["listening", "thinking", "speaking"]
    assert all(s["llmModel"] == "google/x" for s in states)

    assert sink.hits == [[str(HIT_ID)]]  # entries without id are not reported
    assert [t.speaker for t in rec.turns] == [Speaker.CUSTOMER, Speaker.AGENT]
    assert rec.transcript_text().startswith("Customer: CallGo сайн уу\nAgent: ")
    clock.t = 1061.4
    assert rec.duration_sec() == 61


async def test_recorder_prompts_away_user_once() -> None:
    emitter = EventEmitter(FakeSink(), org_id=ORG, call_id=CALL)
    rec = CallRecorder(emitter, make_bootstrap())
    fake = FakeAgentSession()
    rec.attach(fake)  # type: ignore[arg-type]
    away = UserStateChangedEvent(old_state="listening", new_state="away")
    fake.emit("user_state_changed", away)
    fake.emit("user_state_changed", away)
    assert len(fake.replies) == 1
    fake.emit("user_state_changed", UserStateChangedEvent(old_state="away", new_state="speaking"))
    fake.emit("user_state_changed", away)
    assert len(fake.replies) == 2


# ---- post-call analysis ---------------------------------------------------------------


class FakeStream:
    def __init__(self, parts: list[str], delay: float = 0.0) -> None:
        self.parts, self.delay = parts, delay

    async def __aenter__(self) -> Self:
        return self

    async def __aexit__(self, *exc: object) -> None:
        return None

    def __aiter__(self) -> AsyncIterator[llm.ChatChunk]:
        return self._gen()

    async def _gen(self) -> AsyncIterator[llm.ChatChunk]:
        for p in self.parts:
            if self.delay:
                await asyncio.sleep(self.delay)
            yield llm.ChatChunk(id="c", delta=llm.ChoiceDelta(role="assistant", content=p))


class FakeLLM:
    def __init__(self, parts: list[str], *, error: Exception | None = None, delay: float = 0.0):
        self.parts, self.error, self.delay = parts, error, delay
        self.contexts: list[llm.ChatContext] = []
        self.closed = False

    def chat(self, *, chat_ctx: llm.ChatContext, **_: Any) -> FakeStream:
        self.contexts.append(chat_ctx)
        if self.error:
            raise self.error
        return FakeStream(self.parts, self.delay)

    async def aclose(self) -> None:
        self.closed = True


class MeteredLLM(FakeLLM):
    """FakeLLM that emits ``metrics_collected`` (LLMMetrics) for each completion."""

    def __init__(self, parts: list[str]) -> None:
        super().__init__(parts)
        self.handlers: list[Callable[[Any], None]] = []

    def on(self, name: str, cb: Callable[[Any], None]) -> None:
        assert name == "metrics_collected"
        self.handlers.append(cb)

    def off(self, name: str, cb: Callable[[Any], None]) -> None:
        self.handlers.remove(cb)

    def chat(self, *, chat_ctx: llm.ChatContext, **kw: Any) -> FakeStream:
        stream = super().chat(chat_ctx=chat_ctx, **kw)
        metrics = LLMMetrics(
            label="fake",
            request_id=f"req-{len(self.contexts)}",
            timestamp=0,
            duration=0.1,
            ttft=0.05,
            cancelled=False,
            completion_tokens=20,
            prompt_tokens=100,
            prompt_cached_tokens=0,
            total_tokens=120,
            tokens_per_second=200,
        )
        for cb in list(self.handlers):
            cb(metrics)
            cb(metrics)  # duplicates (session re-emission) are counted once
        return stream


TURNS = [
    TranscriptTurn(call_id=CALL, speaker=Speaker.AGENT, text="Сайн байна уу?"),
    TranscriptTurn(call_id=CALL, speaker=Speaker.CUSTOMER, text="Захиалгаа шалгах гэсэн юм."),
]


def test_parse_analysis_variants() -> None:
    ok = parse_analysis(
        '{"summary": "Харилцагч захиалгаа шалгасан.", "sentiment": "Positive",'
        ' "intent": "Order Status"}'
    )
    assert ok is not None
    assert (ok.summary, ok.sentiment, ok.intent) == (
        "Харилцагч захиалгаа шалгасан.",
        Sentiment.POSITIVE,
        "order_status",
    )
    fenced = parse_analysis('```json\n{"summary": "S", "sentiment": "angry"}\n```')
    assert fenced is not None and fenced.sentiment == Sentiment.NEUTRAL and fenced.intent == ""
    prose = parse_analysis('Here you go: {"summary": "S", "sentiment": "negative"} thanks')
    assert prose is not None and prose.sentiment == Sentiment.NEGATIVE
    assert parse_analysis('{"summary": ""}') is None
    assert parse_analysis("no json at all") is None
    assert parse_analysis("{broken json}") is None
    assert parse_analysis("[1, 2]") is None


def test_analysis_prompt_with_outcomes() -> None:
    assert analysis_prompt() == sess.ANALYSIS_PROMPT
    assert "outcomeNote" not in sess.ANALYSIS_PROMPT
    prompt = analysis_prompt(OUTCOMES)
    assert prompt.startswith(sess.ANALYSIS_PROMPT.split("\n")[0])
    assert '"outcome": "<code>", "outcomeNote": "..."' in prompt
    assert "- agreed: Зөвшөөрсөн — Санал болголтыг хүлээн авсан" in prompt
    assert "- declined: Татгалзсан — Сонирхолгүй гэсэн" in prompt
    assert "Дуудлагын үр дүнгийн хүснэгт" in prompt
    assert "200 тэмдэгт" in prompt


def test_parse_analysis_outcome_validation(caplog: pytest.LogCaptureFixture) -> None:
    def parse(outcome: object, note: str = "Тэр зөвшөөрсөн.") -> Any:
        import json

        body = {"summary": "S", "sentiment": "positive", "outcome": outcome, "outcomeNote": note}
        result = parse_analysis(json.dumps(body, ensure_ascii=False), OUTCOMES)
        assert result is not None
        return result

    valid = parse("agreed")
    assert (valid.outcome, valid.outcome_note) == ("agreed", "Тэр зөвшөөрсөн.")
    assert parse(" DECLINED ").outcome == "declined"  # code, case-insensitive
    assert parse("зөвшөөрсөн").outcome == "agreed"  # label, case-insensitive
    assert parse("Дахин залгах").outcome == "callback"
    assert parse(None).outcome == ""
    assert parse("").outcome == ""

    caplog.clear()
    unknown = parse("maybe_later")
    assert unknown.outcome == ""
    assert "maybe_later" in caplog.text

    long_note = parse("agreed", "үг " * 150).outcome_note
    assert len(long_note) == 200 and long_note.endswith("…")

    snake = parse_analysis('{"summary": "S", "outcome": "agreed", "outcome_note": "N"}', OUTCOMES)
    assert snake is not None and snake.outcome_note == "N"

    # a campaign without outcomes ignores whatever the model says
    plain = parse_analysis('{"summary": "S", "outcome": "agreed", "outcomeNote": "N"}')
    assert plain is not None and (plain.outcome, plain.outcome_note) == ("", "")


async def test_analyze_call_uses_llm_json() -> None:
    model = FakeLLM(
        [
            '{"summary": "Захиалга ',
            'шалгав.", "sentiment": "positive", ',
            '"intent": "order_status"}',
        ]
    )
    result = await analyze_call(
        model,  # type: ignore[arg-type]
        TURNS,
        end_reason="hangup_agent",
        callbacks=[CallbackRequest(when="маргааш", note="")],
    )
    assert result.from_llm
    assert result.summary == "Захиалга шалгав."
    assert result.sentiment == Sentiment.POSITIVE
    assert result.intent == "order_status"
    ctx = model.contexts[0]
    system, user = ctx.messages()
    assert system.role == "system" and "JSON" in (system.text_content or "")
    assert "Customer: Захиалгаа шалгах гэсэн юм." in (user.text_content or "")
    assert "Call end reason: hangup_agent" in (user.text_content or "")
    assert "Callback requested: маргааш" in (user.text_content or "")


@pytest.mark.parametrize(
    "model",
    [
        FakeLLM([], error=RuntimeError("provider down")),
        FakeLLM(["I cannot do that"]),
        FakeLLM(['{"summary": "late"}'], delay=0.2),
    ],
    ids=["error", "garbage", "timeout"],
)
async def test_analyze_call_falls_back(model: FakeLLM) -> None:
    result = await analyze_call(model, TURNS, timeout=0.05)  # type: ignore[arg-type]
    assert not result.from_llm
    assert result.sentiment == Sentiment.NEUTRAL
    assert "Захиалгаа шалгах гэсэн юм." in result.summary


async def test_analyze_call_asks_for_outcome() -> None:
    model = FakeLLM(
        [
            (
                '```json\n{"summary": "Харилцагч зөвшөөрөв.", "sentiment": "positive", '
                '"intent": "offer_accepted", "outcome": "Зөвшөөрсөн", '
                '"outcomeNote": "Урамшууллыг авахаар тохиролцсон."}\n```'
            )
        ]
    )
    result = await analyze_call(
        model,  # type: ignore[arg-type]
        TURNS,
        end_reason="hangup_agent",
        outcomes=OUTCOMES,
        recorded_outcome="agreed",
    )
    assert result.from_llm
    assert (result.outcome, result.outcome_note) == ("agreed", "Урамшууллыг авахаар тохиролцсон.")
    system, user = model.contexts[0].messages()
    assert "Дуудлагын үр дүнгийн хүснэгт" in (system.text_content or "")
    assert "- no_contact: Холбогдоогүй" in (system.text_content or "")
    assert "Outcome recorded during the call: agreed" in (user.text_content or "")


async def test_analyze_call_outcome_fallbacks() -> None:
    # LLM failure after a real conversation: no guess
    failed = await analyze_call(
        FakeLLM([], error=RuntimeError("down")),  # type: ignore[arg-type]
        TURNS,
        outcomes=OUTCOMES,
    )
    assert not failed.from_llm and (failed.outcome, failed.outcome_note) == ("", "")

    # the customer never spoke: no LLM call, no_contact
    model = FakeLLM(['{"summary": "x", "outcome": "agreed"}'])
    silent = await analyze_call(model, TURNS[:1], outcomes=OUTCOMES)  # type: ignore[arg-type]
    assert model.contexts == []
    assert silent.outcome == "no_contact"
    assert silent.outcome_note == "Харилцагч ярианд оролцоогүй."

    unanswered = await analyze_call(None, [], end_reason="no_answer", outcomes=OUTCOMES)
    assert (unanswered.outcome, unanswered.outcome_note) == (
        "no_contact",
        "Дуудлагад хариу өгөөгүй.",
    )
    busy = fallback_analysis([], OUTCOMES, "busy")
    assert busy.outcome == "no_contact" and "завгүй" in busy.outcome_note

    # no_contact not defined by the campaign, or no outcomes at all
    without = [o for o in OUTCOMES if o.code != "no_contact"]
    assert fallback_analysis([], without, "no_answer").outcome == ""
    assert fallback_analysis([]).outcome == ""
    assert fallback_analysis(TURNS, OUTCOMES).outcome == ""


def test_final_outcome_precedence() -> None:
    state = CallState(bootstrap=make_bootstrap(campaign=OUTCOME_CAMPAIGN))
    analysis = sess.CallAnalysis(summary="S", outcome="declined", outcome_note="Татгалзсан.")
    assert final_outcome(state, analysis) == ("declined", "Татгалзсан.")

    assert state.record_outcome("agreed", "Зөвшөөрсөн гэж хэлсэн.") == "agreed"
    assert final_outcome(state, analysis) == ("agreed", "Зөвшөөрсөн гэж хэлсэн.")

    # recorded without a note: the analysis note is used only if it agrees
    state.outcome_note = ""
    assert final_outcome(state, analysis) == ("agreed", "")
    agreeing = sess.CallAnalysis(summary="S", outcome="agreed", outcome_note="Тохиролцов.")
    assert final_outcome(state, agreeing) == ("agreed", "Тохиролцов.")

    empty = CallState(bootstrap=make_bootstrap(campaign=OUTCOME_CAMPAIGN))
    assert final_outcome(empty, sess.CallAnalysis(summary="S", outcome_note="x")) == ("", "")


async def test_analyze_call_skips_llm_without_customer_speech() -> None:
    model = FakeLLM(['{"summary": "x"}'])
    result = await analyze_call(model, TURNS[:1])  # type: ignore[arg-type]
    assert model.contexts == []
    assert result.summary == "Харилцагч ярианд оролцоогүй."
    assert (await analyze_call(None, [])).summary == "Харилцан яриа бүртгэгдээгүй."


class FakeControl:
    def __init__(self) -> None:
        self.hangups = 0
        self.transfers: list[str] = []

    async def hangup(self) -> None:
        self.hangups += 1

    async def transfer(self, to: str) -> None:
        self.transfers.append(to)


async def test_finalizer_emits_call_ended_once() -> None:
    sink = FakeSink()
    boot = make_bootstrap()
    emitter = EventEmitter(sink, org_id=ORG, call_id=CALL, flush_interval_ms=60_000)
    clock = Clock()
    rec = CallRecorder(emitter, boot, clock=clock)
    rec.mark_answered()
    rec.turns.extend(TURNS)
    state = CallState(bootstrap=boot)
    due = datetime(2026, 10, 1, 10, 0, tzinfo=ZoneInfo("Asia/Ulaanbaatar"))
    state.callbacks.append(CallbackRequest(when="маргааш 10:00", note="үнийн санал", due_at=due))
    control = FakeControl()
    model = FakeLLM(['{"summary": "Захиалга шалгав.", "sentiment": "negative", "intent": "x"}'])
    usage = UsageTracker(rates=CostRates(10, 60, 1000))
    usage.add(
        STTMetrics(
            label="s", request_id="r", timestamp=0, duration=0, audio_duration=30.0, streamed=False
        )
    )
    fin = CallFinalizer(
        state=state,
        recorder=rec,
        emitter=emitter,
        control=control,
        model=model,  # type: ignore[arg-type]
        llm_label="google/gemini-2.5-flash",
        usage=usage,
    )
    clock.t += 42.4

    payload = await fin.finalize(CloseReason.PARTICIPANT_DISCONNECTED)
    again = await fin.finalize(CloseReason.ERROR)

    assert again is payload
    assert control.hangups == 1
    assert model.closed
    assert emitter.closed
    ended = sink.of("call.ended")
    assert len(ended) == 1
    assert ended[0].payload == {
        "endReason": "hangup_customer",
        "summary": "Захиалга шалгав. Буцаж залгах хүсэлт: маргааш 10:00 — үнийн санал.",
        "sentiment": "negative",
        "intent": "x",
        "durationSec": 42,
        "llmModelUsed": "google/gemini-2.5-flash",
        "outcome": "",
        "outcomeNote": "",
        "usage": {
            "llmTokensIn": 0,
            "llmTokensOut": 0,
            "sttSeconds": 30.0,
            "ttsChars": 0,
            "llmModel": "google/gemini-2.5-flash",
            "costMnt": 30,
        },
        "callbacks": [{"dueAt": "2026-10-01T02:00:00Z", "note": "үнийн санал"}],
    }


async def test_finalizer_counts_analysis_tokens_and_defaults_usage() -> None:
    sink = FakeSink()
    boot = make_bootstrap()
    emitter = EventEmitter(sink, org_id=ORG, call_id=CALL, flush_interval_ms=60_000)
    rec = CallRecorder(emitter, boot)
    rec.turns.extend(TURNS)
    model = MeteredLLM(['{"summary": "S"}'])
    usage = UsageTracker(rates=CostRates(1000, 0, 0))
    fin = CallFinalizer(
        state=CallState(bootstrap=boot),
        recorder=rec,
        emitter=emitter,
        model=model,  # type: ignore[arg-type]
        llm_label="openai/gpt",
        usage=usage,
    )
    await fin.finalize()
    ended = sink.of("call.ended")[0].payload
    assert ended["usage"]["llmTokensIn"] == 100 and ended["usage"]["llmTokensOut"] == 20
    assert ended["usage"]["llmModel"] == "openai/gpt"
    assert ended["usage"]["costMnt"] == 120
    assert "callbacks" not in ended
    assert model.handlers == []  # the analysis watch is removed again

    # without a tracker the payload still carries a zero usage block
    sink2 = FakeSink()
    em2 = EventEmitter(sink2, org_id=ORG, call_id=CALL)
    fin2 = CallFinalizer(
        state=CallState(bootstrap=boot), recorder=CallRecorder(em2, boot), emitter=em2
    )
    await fin2.finalize()
    assert sink2.of("call.ended")[0].payload["usage"]["costMnt"] == 0


async def test_finalizer_recorded_outcome_wins_over_analysis() -> None:
    sink = FakeSink()
    boot = make_bootstrap(campaign=OUTCOME_CAMPAIGN)
    emitter = EventEmitter(sink, org_id=ORG, call_id=CALL, flush_interval_ms=60_000)
    rec = CallRecorder(emitter, boot)
    rec.turns.extend(TURNS)
    state = CallState(bootstrap=boot)
    state.record_outcome("callback", "Маргааш залгана.")
    model = FakeLLM(
        ['{"summary": "S", "sentiment": "neutral", "outcome": "declined", "outcomeNote": "N"}']
    )
    fin = CallFinalizer(state=state, recorder=rec, emitter=emitter, model=model)  # type: ignore[arg-type]
    payload = await fin.finalize()
    assert (payload.outcome, payload.outcome_note) == ("callback", "Маргааш залгана.")
    ended = sink.of("call.ended")[0].payload
    assert ended["outcome"] == "callback" and ended["outcomeNote"] == "Маргааш залгана."
    user = model.contexts[0].messages()[1]
    assert "Outcome recorded during the call: callback" in (user.text_content or "")


async def test_finalizer_uses_analysis_outcome_when_none_recorded() -> None:
    sink = FakeSink()
    boot = make_bootstrap(campaign=OUTCOME_CAMPAIGN)
    emitter = EventEmitter(sink, org_id=ORG, call_id=CALL, flush_interval_ms=60_000)
    rec = CallRecorder(emitter, boot)
    rec.turns.extend(TURNS)
    model = FakeLLM(['{"summary": "S", "outcome": "declined", "outcomeNote": "Сонирхолгүй."}'])
    fin = CallFinalizer(
        state=CallState(bootstrap=boot),
        recorder=rec,
        emitter=emitter,
        model=model,  # type: ignore[arg-type]
    )
    payload = await fin.finalize()
    assert (payload.outcome, payload.outcome_note) == ("declined", "Сонирхолгүй.")


async def test_finalizer_logs_knowledge_lookups_without_touching_summary(
    caplog: pytest.LogCaptureFixture,
) -> None:
    sink = FakeSink()
    boot = make_bootstrap(knowledge=KB_TOOL)
    emitter = EventEmitter(sink, org_id=ORG, call_id=CALL, flush_interval_ms=60_000)
    rec = CallRecorder(emitter, boot)
    rec.turns.extend(TURNS)
    state = CallState(bootstrap=boot)
    state.record_knowledge_lookup(2)
    state.record_knowledge_lookup(0)
    model = FakeLLM(['{"summary": "Ажлын цаг асуув.", "sentiment": "neutral", "intent": "info"}'])
    fin = CallFinalizer(state=state, recorder=rec, emitter=emitter, model=model)  # type: ignore[arg-type]
    with caplog.at_level("INFO", logger="callgo.session"):
        payload = await fin.finalize(CloseReason.PARTICIPANT_DISCONNECTED)
    assert payload.summary == "Ажлын цаг асуув."
    assert "knowledge lookups: 2 (1 without results)" in caplog.text


async def test_finalizer_respects_tool_end_reason() -> None:
    sink = FakeSink()
    boot = make_bootstrap()
    emitter = EventEmitter(sink, org_id=ORG, call_id=CALL)
    state = CallState(bootstrap=boot)
    state.set_end_reason("transferred")
    fin = CallFinalizer(state=state, recorder=CallRecorder(emitter, boot), emitter=emitter)
    payload = await fin.finalize(CloseReason.ERROR)
    assert payload.end_reason == "transferred"
    assert payload.duration_sec == 0


def test_end_reason_for_close() -> None:
    assert end_reason_for_close(CloseReason.ERROR) == "failed"
    assert end_reason_for_close("error") == "failed"
    assert end_reason_for_close(CloseReason.PARTICIPANT_DISCONNECTED) == "hangup_customer"
    assert end_reason_for_close(None) == "hangup_customer"


def test_turn_handling_and_llm_chain() -> None:
    th = build_turn_handling("vad")
    assert th["turn_detection"] == "vad"
    assert th["interruption"]["enabled"] is True
    assert th["interruption"]["mode"] == "vad"
    assert th["endpointing"]["min_delay"] >= 0.5

    primary, fb1, fb2 = make_llm_config("a"), make_llm_config("bb"), make_llm_config("ccc")
    boot = make_bootstrap(llm_cfg=primary).model_copy(update={"llm_fallbacks": [fb1, fb2]})
    assert select_llm_chain(boot) == (primary, [fb1, fb2])
    no_primary = boot.model_copy(update={"llm": None})
    assert select_llm_chain(no_primary) == (fb1, [fb2])
    assert select_llm_chain(no_primary.model_copy(update={"llm_fallbacks": []})) is None


async def test_enforce_max_duration() -> None:
    fake = FakeAgentSession()
    state = CallState(bootstrap=make_bootstrap())
    await enforce_max_duration(fake, state, 0.01, "mn")  # type: ignore[arg-type]
    assert state.end_reason == "max_duration"
    assert fake.said[0][0].startswith("Уучлаарай")
    assert fake.said[0][1] == {"allow_interruptions": False}
    assert fake.shutdowns == [False]

    other = FakeAgentSession()
    state.end_reason = "hangup_agent"
    await enforce_max_duration(other, state, 0.0, "en")  # type: ignore[arg-type]
    assert other.said == [] and other.shutdowns == []


# ---- job-level flow with fakes -----------------------------------------------------------


class FakeRoom:
    def __init__(self, name: str = "call-1") -> None:
        self.name = name
        self.handlers: dict[str, list[Callable[..., None]]] = defaultdict(list)
        self.remote_participants: dict[str, Any] = {}

    def on(self, name: str, cb: Callable[..., None]) -> Callable[..., None]:
        self.handlers[name].append(cb)
        return cb

    def off(self, name: str, cb: Callable[..., None]) -> None:
        if cb in self.handlers[name]:
            self.handlers[name].remove(cb)

    def emit(self, name: str, *args: Any) -> None:
        for cb in list(self.handlers[name]):
            cb(*args)


class FakeParticipant:
    def __init__(self, status: str = "active") -> None:
        self.identity = "sip_+97699112233"
        self.kind = rtc.ParticipantKind.PARTICIPANT_KIND_SIP
        self.attributes = {
            "sip.phoneNumber": "+97699112233",
            "sip.trunkPhoneNumber": "+97677001100",
            "sip.callID": "SCL_1",
            "sip.callStatus": status,
        }


class _RoomSvc:
    def __init__(self) -> None:
        self.deleted: list[str] = []

    async def delete_room(self, req: Any) -> None:
        self.deleted.append(req.room)


class _Api:
    def __init__(self) -> None:
        self.room = _RoomSvc()
        self.sip = None


class FakeJobContext:
    def __init__(self, metadata: str = "", participant: FakeParticipant | None = None) -> None:
        self.job = types.SimpleNamespace(metadata=metadata)
        self.room = FakeRoom()
        self.api = _Api()
        self.proc = types.SimpleNamespace(userdata={"vad": "VAD"})
        self.participant = participant or FakeParticipant()
        self.log_context_fields: dict[str, Any] = {}
        self.shutdown_callbacks: list[Callable[[str], Any]] = []
        self.shutdown_reasons: list[str] = []
        self.connected = False

    async def connect(self) -> None:
        self.connected = True

    async def wait_for_participant(self, *, kind: Any = None) -> FakeParticipant:
        assert kind == rtc.ParticipantKind.PARTICIPANT_KIND_SIP
        return self.participant

    def add_shutdown_callback(self, cb: Callable[[str], Any]) -> None:
        self.shutdown_callbacks.append(cb)

    def shutdown(self, reason: str = "") -> None:
        self.shutdown_reasons.append(reason)

    async def run_shutdown_callbacks(self) -> None:
        for cb in self.shutdown_callbacks:
            await cb("test")


class FakeClient(FakeSink):
    def __init__(
        self,
        boot: Bootstrap | None = None,
        error: Exception | None = None,
        by_profile: dict[UUID, Bootstrap] | None = None,
    ) -> None:
        super().__init__()
        self.boot, self.error = boot, error
        self.by_profile = by_profile or {}
        self.bootstrap_kwargs: dict[str, Any] = {}
        self.bootstrap_calls: list[dict[str, Any]] = []

    async def bootstrap(self, **kw: Any) -> Bootstrap:
        self.bootstrap_kwargs = kw
        self.bootstrap_calls.append(kw)
        if self.error:
            raise self.error
        if kw.get("profile_id") is not None:
            return self.by_profile[kw["profile_id"]]
        assert self.boot is not None
        return self.boot

    async def aclose(self) -> None:
        self.closed = True


class FakeEngine:
    """STT/TTS stand-in; compares equal to its name (not a ``str``: ``Agent`` would treat a
    string as a LiveKit inference model id)."""

    def __init__(self, name: str) -> None:
        self.name = name

    def __eq__(self, other: object) -> bool:
        return other == self.name if isinstance(other, str) else other is self

    def __hash__(self) -> int:
        return hash(self.name)


def make_factories(
    model: FakeLLM | None = None,
    fail: bool = False,
    built: list[str] | None = None,
    models: dict[str, Any] | None = None,
) -> PipelineFactories:
    log = built if built is not None else []

    def build_llm(cfg: LLMConfig, fallbacks: list[LLMConfig]) -> Any:
        log.append(f"llm:{cfg.name}")
        if fail:
            raise RuntimeError("no api key")
        if models and cfg.name in models:
            return models[cfg.name]
        return model

    def build_stt(p: AgentProfile) -> Any:
        log.append(f"stt:{p.name}")
        return FakeEngine("STT")

    def build_tts(p: AgentProfile) -> Any:
        log.append(f"tts:{p.name}")
        return FakeEngine("TTS")

    return PipelineFactories(
        build_stt=build_stt,
        build_tts=build_tts,
        build_llm=build_llm,
        describe_llm=lambda c: f"{c.provider.value}/{c.model}",
        turn_detection=lambda: "vad",
        load_vad=lambda: pytest.fail("VAD must come from prewarm"),  # type: ignore[arg-type,return-value]
    )


async def test_run_call_bootstrap_failure_hangs_up() -> None:
    ctx = FakeJobContext()
    client = FakeClient(error=RuntimeError("backend down"))
    await run_call(ctx, client, factories=make_factories(), close_client=True)  # type: ignore[arg-type]
    assert ctx.api.room.deleted == ["call-1"]
    assert ctx.shutdown_reasons == ["bootstrap failed"]
    assert client.events == []
    assert client.bootstrap_kwargs == {
        "room": "call-1",
        "sip_number": "+97677001100",
        "from_number": "+97699112233",
        "to_number": "+97677001100",
        "direction": CallDirection.INBOUND,
        "call_id": None,
    }
    await ctx.run_shutdown_callbacks()
    assert client.closed


async def test_run_call_pipeline_failure_reports_failed_call() -> None:
    ctx = FakeJobContext()
    client = FakeClient(boot=make_bootstrap(llm_cfg=make_llm_config()))
    await run_call(ctx, client, factories=make_factories(fail=True))  # type: ignore[arg-type]
    ended = client.of("call.ended")
    assert len(ended) == 1 and ended[0].payload["endReason"] == "failed"
    assert ended[0].call_id == CALL and ended[0].org_id == ORG
    assert ctx.api.room.deleted == ["call-1"]
    assert ctx.shutdown_reasons == ["pipeline build failed"]
    await ctx.run_shutdown_callbacks()  # finalize is idempotent
    assert len(client.of("call.ended")) == 1
    assert not client.closed  # close_client defaults to False


async def test_run_call_outbound_no_answer() -> None:
    meta = f'{{"callId": "{CALL}", "direction": "outbound", "toNumber": "+97699112233"}}'
    ctx = FakeJobContext(metadata=meta, participant=FakeParticipant(status="dialing"))
    client = FakeClient(boot=make_bootstrap(direction=CallDirection.OUTBOUND))
    task = asyncio.create_task(
        run_call(ctx, client, factories=make_factories())  # type: ignore[arg-type]
    )
    for _ in range(50):
        await asyncio.sleep(0)
        if ctx.room.handlers["participant_disconnected"]:
            break
    assert client.bootstrap_kwargs["call_id"] == CALL
    assert client.bootstrap_kwargs["from_number"] == "+97677001100"
    ctx.room.emit("participant_disconnected", ctx.participant)
    await asyncio.wait_for(task, 2)
    ended = client.of("call.ended")
    assert ended[0].payload["endReason"] == "no_answer"
    assert ended[0].payload["durationSec"] == 0
    assert ctx.shutdown_reasons == ["no answer"]


async def test_run_call_outbound_no_answer_sets_no_contact() -> None:
    meta = f'{{"callId": "{CALL}", "direction": "outbound", "toNumber": "+97699112233"}}'
    ctx = FakeJobContext(metadata=meta, participant=FakeParticipant(status="dialing"))
    boot = make_bootstrap(
        direction=CallDirection.OUTBOUND, campaign=OUTCOME_CAMPAIGN, llm_cfg=make_llm_config()
    )
    client = FakeClient(boot=boot)
    model = FakeLLM(['{"summary": "x"}'])
    task = asyncio.create_task(
        run_call(ctx, client, factories=make_factories(model))  # type: ignore[arg-type]
    )
    for _ in range(50):
        await asyncio.sleep(0)
        if ctx.room.handlers["participant_disconnected"]:
            break
    ctx.room.emit("participant_disconnected", ctx.participant)
    await asyncio.wait_for(task, 2)
    ended = client.of("call.ended")[0].payload
    assert ended["endReason"] == "no_answer"
    assert ended["outcome"] == "no_contact"
    assert ended["outcomeNote"] == "Дуудлагад хариу өгөөгүй."
    assert model.contexts == []  # no LLM involved


async def test_run_call_pipeline_failure_sets_no_contact() -> None:
    ctx = FakeJobContext()
    boot = make_bootstrap(llm_cfg=make_llm_config(), campaign=OUTCOME_CAMPAIGN)
    client = FakeClient(boot=boot)
    await run_call(ctx, client, factories=make_factories(fail=True))  # type: ignore[arg-type]
    ended = client.of("call.ended")[0].payload
    assert (ended["endReason"], ended["outcome"]) == ("failed", "no_contact")


async def test_wait_for_answer_resolves_on_active() -> None:
    room = FakeRoom()
    p = FakeParticipant(status="ringing")
    task = asyncio.create_task(wait_for_answer(room, p, timeout=1))  # type: ignore[arg-type]
    await asyncio.sleep(0)
    p.attributes["sip.callStatus"] = "active"
    room.emit("participant_attributes_changed", {"sip.callStatus": "active"}, p)
    assert await task is True
    assert room.handlers["participant_attributes_changed"] == []
    assert await wait_for_answer(room, FakeParticipant("ringing"), timeout=0.01) is False  # type: ignore[arg-type]


async def test_run_call_happy_path(
    monkeypatch: pytest.MonkeyPatch, normalizer: FakeNormalizer
) -> None:
    FakeAgentSession.instances.clear()
    monkeypatch.setattr(sess, "AgentSession", FakeAgentSession)
    ctx = FakeJobContext(metadata="")
    boot = make_bootstrap(llm_cfg=make_llm_config(), tools=["end_call"], lexicon=LEXICON)
    client = FakeClient(boot=boot)
    model = FakeLLM(['{"summary": "Мэндчилсэн.", "sentiment": "positive", "intent": "greeting"}'])

    task = asyncio.create_task(
        run_call(ctx, client, factories=make_factories(model), close_client=True)  # type: ignore[arg-type]
    )
    for _ in range(100):
        await asyncio.sleep(0)
        if FakeAgentSession.instances and FakeAgentSession.instances[0].started:
            break
    session = FakeAgentSession.instances[0]
    assert session.kw["vad"] == "VAD" and session.kw["stt"] == "STT"
    assert session.kw["tts"] == "TTS" and session.kw["llm"] is model
    assert session.kw["turn_handling"]["interruption"]["mode"] == "vad"
    assert isinstance(session.kw["userdata"], CallState)
    assert session.room_options.participant_identity == "sip_+97699112233"
    assert isinstance(session.agent, CallGoAgent)
    assert [t.info.name for t in session.agent.tools] == ["end_call"]
    assert "# Call outcome" not in session.agent.instructions

    # speech flows through the agent's STT hook, then the session reports it
    session.agent.process_speech_event(
        _speech(stt.SpeechEventType.FINAL_TRANSCRIPT, "калл го сайн уу")
    )
    session.emit(
        "user_input_transcribed",
        UserInputTranscribedEvent(transcript="CallGo сайн уу", is_final=True),
    )
    session.emit(
        "conversation_item_added",
        ConversationItemAddedEvent(item=llm.ChatMessage(role="assistant", content=["Сайн уу"])),
    )
    session.emit(
        "agent_state_changed", AgentStateChangedEvent(old_state="idle", new_state="listening")
    )

    ctx.room.emit("participant_disconnected", ctx.participant)  # customer hangs up
    await asyncio.wait_for(task, 2)
    await ctx.run_shutdown_callbacks()

    types_ = [e.type for e in client.events]
    assert types_[0] == "call.answered"
    assert client.events[0].payload["call"]["status"] == "active"
    assert "transcript.final" in types_ and "agent.state" in types_
    assert types_[-1] == "call.ended"
    customer = client.of("transcript.final")[0].payload["turn"]
    assert customer["rawText"] == "калл го сайн уу"
    ended = client.events[-1].payload
    assert ended["endReason"] == "hangup_customer"
    assert ended["summary"] == "Мэндчилсэн."
    assert ended["sentiment"] == "positive"
    assert ended["llmModelUsed"] == "google/gemini-2.5-flash"
    assert client.hits == [[str(HIT_ID)]]
    assert ctx.api.room.deleted == ["call-1"]
    assert ctx.shutdown_reasons == ["hangup_customer"]
    assert model.closed and client.closed


@pytest.mark.parametrize("knowledge", [KB_TOOL, KB_CONTEXT, None])
async def test_run_call_knowledge_wiring(
    monkeypatch: pytest.MonkeyPatch, knowledge: KnowledgeInfo | None
) -> None:
    FakeAgentSession.instances.clear()
    monkeypatch.setattr(sess, "AgentSession", FakeAgentSession)
    ctx = FakeJobContext()
    boot = make_bootstrap(llm_cfg=make_llm_config(), tools=["end_call"], knowledge=knowledge)
    client = FakeClient(boot=boot)
    model = FakeLLM(['{"summary": "x"}'])
    task = asyncio.create_task(
        run_call(ctx, client, factories=make_factories(model))  # type: ignore[arg-type]
    )
    for _ in range(100):
        await asyncio.sleep(0)
        if FakeAgentSession.instances and FakeAgentSession.instances[0].started:
            break
    agent = FakeAgentSession.instances[0].agent
    names = [t.info.name for t in agent.tools]
    text = agent.instructions
    mode = knowledge.mode if knowledge else "off"

    assert ("lookup_knowledge" in names) == (mode == "tool")
    assert names[0] == "end_call"
    assert ("# Company knowledge" in text) == (mode == "tool")
    assert ("# Мэдлэгийн сан" in text) == (mode == "context")
    assert ("Ажлын цаг: 09:00-18:00." in text) == (mode == "context")

    ctx.room.emit("participant_disconnected", ctx.participant)
    await asyncio.wait_for(task, 2)
    assert client.of("call.ended")[0].payload["endReason"] == "hangup_customer"


async def test_run_call_session_start_failure(monkeypatch: pytest.MonkeyPatch) -> None:
    class BrokenSession(FakeAgentSession):
        async def start(self, agent: Any, *, room: Any, room_options: Any) -> None:
            raise RuntimeError("rtc failure")

    monkeypatch.setattr(sess, "AgentSession", BrokenSession)
    ctx = FakeJobContext()
    client = FakeClient(boot=make_bootstrap(llm_cfg=make_llm_config()))
    model = FakeLLM([])
    await run_call(ctx, client, factories=make_factories(model))  # type: ignore[arg-type]
    assert [e.type for e in client.events] == ["call.answered", "call.ended"]
    assert client.events[-1].payload["endReason"] == "failed"
    assert ctx.shutdown_reasons == ["session start failed"]
    assert ctx.room.handlers["participant_disconnected"] == []
    assert model.closed


# ---- entitlements, inbound routing, handoff ----------------------------------------------


async def _start_call(
    monkeypatch: pytest.MonkeyPatch,
    boot: Bootstrap,
    *,
    client: FakeClient | None = None,
    factories: PipelineFactories | None = None,
) -> tuple[FakeJobContext, FakeClient, asyncio.Task[None]]:
    FakeAgentSession.instances.clear()
    monkeypatch.setattr(sess, "AgentSession", FakeAgentSession)
    monkeypatch.setattr(settings, "event_flush_interval_ms", 1)
    ctx = FakeJobContext()
    client = client or FakeClient(boot=boot)
    task = asyncio.create_task(
        run_call(ctx, client, factories=factories or make_factories(FakeLLM(['{"summary": "x"}'])))  # type: ignore[arg-type]
    )
    for _ in range(200):
        await asyncio.sleep(0)
        if FakeAgentSession.instances and FakeAgentSession.instances[0].started:
            break
    return ctx, client, task


async def _settle(n: int = 20) -> None:
    for _ in range(n):
        await asyncio.sleep(0)
    await asyncio.sleep(0.02)  # let the emitter flush (interval 1 ms in these tests)


def _no_llm_factories(built: list[str]) -> PipelineFactories:
    f = make_factories(built=built)
    f.build_llm = lambda c, fb: pytest.fail("no LLM for refused calls")  # type: ignore[assignment,return-value]
    return f


async def test_run_call_refuses_when_quota_exceeded(monkeypatch: pytest.MonkeyPatch) -> None:
    boot = make_bootstrap(llm_cfg=make_llm_config()).model_copy(
        update={"entitlements": EntitlementInfo(can_start=False, reason="minutes")}
    )
    built: list[str] = []
    ctx, client, task = await _start_call(monkeypatch, boot, factories=_no_llm_factories(built))
    await asyncio.wait_for(task, 2)

    session = FakeAgentSession.instances[0]
    assert set(session.kw) == {"tts", "userdata"}  # TTS only: no STT, no LLM
    assert isinstance(session.agent, sess.SilentAgent)
    assert session.room_options.audio_input is False
    assert session.said == [
        ("Уучлаарай, одоогоор үйлчилгээ авах боломжгүй байна.", {"allow_interruptions": False})
    ]
    assert session.closed
    assert built == ["tts:Сараа"]
    assert [e.type for e in client.events] == ["call.ended"]  # never "answered"
    ended = client.events[0].payload
    assert ended["endReason"] == "quota_exceeded"
    assert ended["summary"] == sess.GATE_SUMMARIES["quota_exceeded"]
    assert ended["usage"]["llmTokensIn"] == 0
    assert ctx.api.room.deleted == ["call-1"]
    assert ctx.shutdown_reasons == ["quota_exceeded"]


async def test_run_call_after_hours_message_hangs_up(monkeypatch: pytest.MonkeyPatch) -> None:
    route = ResolvedRoute(mode="after_hours", message="Ажлын цаг 09:00-18:00. Баярлалаа.")
    boot = make_bootstrap(llm_cfg=make_llm_config()).model_copy(update={"route": route})
    built: list[str] = []
    ctx, client, task = await _start_call(monkeypatch, boot, factories=_no_llm_factories(built))
    await asyncio.wait_for(task, 2)
    session = FakeAgentSession.instances[0]
    assert session.said[0][0] == "Ажлын цаг 09:00-18:00. Баярлалаа."
    ended = client.of("call.ended")[0].payload
    assert ended["endReason"] == "after_hours"
    assert ctx.shutdown_reasons == ["after_hours"]


async def test_run_call_refusal_survives_tts_failure(monkeypatch: pytest.MonkeyPatch) -> None:
    boot = make_bootstrap().model_copy(update={"entitlements": EntitlementInfo(can_start=False)})
    f = _no_llm_factories([])
    f.build_tts = lambda p: (_ for _ in ()).throw(RuntimeError("no voice"))  # type: ignore[assignment]
    FakeAgentSession.instances.clear()
    monkeypatch.setattr(sess, "AgentSession", FakeAgentSession)
    ctx = FakeJobContext()
    client = FakeClient(boot=boot)
    await run_call(ctx, client, factories=f)  # type: ignore[arg-type]
    assert FakeAgentSession.instances == []
    assert client.of("call.ended")[0].payload["endReason"] == "quota_exceeded"
    assert ctx.api.room.deleted == ["call-1"]


async def test_run_call_after_hours_with_profile_continues(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    route = ResolvedRoute(mode="after_hours", agent_profile_id=UUID(int=3), message="x")
    boot = make_bootstrap(llm_cfg=make_llm_config()).model_copy(update={"route": route})
    ctx, client, task = await _start_call(monkeypatch, boot)
    session = FakeAgentSession.instances[0]
    assert type(session.agent) is CallGoAgent
    assert session.kw["llm"] is not None
    ctx.room.emit("participant_disconnected", ctx.participant)
    await asyncio.wait_for(task, 2)
    assert client.of("call.ended")[0].payload["endReason"] == "hangup_customer"


SALES = UUID(int=41)
SUPPORT = UUID(int=42)
MENU_ROUTE = ResolvedRoute(
    mode="menu",
    menu_prompt="Борлуулалт 1, тусламж 2 дарна уу.",
    menu=[
        MenuOption(key="1", label="Борлуулалт", agent_profile_id=SALES),
        MenuOption(key="2", label="Тусламж", agent_profile_id=SUPPORT),
    ],
    menu_timeout_sec=5,
    menu_repeat=1,
)


def _profile_boot(profile_id: UUID, name: str, voice: str, llm_cfg: LLMConfig) -> Bootstrap:
    base = make_bootstrap(llm_cfg=llm_cfg)
    profile = base.profile.model_copy(
        update={"id": profile_id, "name": name, "tts_voice": voice, "greeting": f"{name} байна."}
    )
    return base.model_copy(update={"profile": profile})


async def test_run_call_menu_dtmf_switches_profile(monkeypatch: pytest.MonkeyPatch) -> None:
    boot = make_bootstrap(llm_cfg=make_llm_config()).model_copy(update={"route": MENU_ROUTE})
    sales = _profile_boot(
        SALES, "Борлуулагч", "mn-voice-2", make_llm_config("sales-llm", model="m-sales-llm")
    )
    client = FakeClient(boot=boot, by_profile={SALES: sales})
    built: list[str] = []
    default_model = FakeLLM(['{"summary": "x"}'])
    sales_model = FakeLLM(['{"summary": "Борлуулалт."}'])
    f = make_factories(default_model, built=built, models={"sales-llm": sales_model})
    ctx, client, task = await _start_call(monkeypatch, boot, client=client, factories=f)

    session = FakeAgentSession.instances[0]
    assert isinstance(session.agent, sess.SilentAgent)  # menu first, no LLM greeting
    await _settle()
    assert session.said[0][0] == MENU_ROUTE.menu_prompt
    assert ctx.room.handlers["sip_dtmf_received"]

    # a DTMF digit from someone else is ignored; the caller's "1" selects sales
    other = types.SimpleNamespace(identity="op-1")
    ctx.room.emit("sip_dtmf_received", rtc.SipDTMF(code=1, digit="2", participant=other))  # type: ignore[arg-type]
    ctx.room.emit("sip_dtmf_received", rtc.SipDTMF(code=1, digit="1", participant=ctx.participant))  # type: ignore[arg-type]
    await _settle(50)

    assert client.bootstrap_calls[-1]["profile_id"] == SALES
    assert client.bootstrap_calls[-1]["call_id"] == CALL
    agent = session.agent
    assert type(agent) is CallGoAgent and agent.bootstrap is sales
    assert agent.llm is sales_model  # the chosen profile's LLM
    assert agent.tts == "TTS"  # its own voice -> a new TTS engine
    assert built == ["llm:primary", "stt:Сараа", "tts:Сараа", "llm:sales-llm", "tts:Борлуулагч"]
    assert ctx.room.handlers["sip_dtmf_received"] == []  # detached after the menu

    ctx.room.emit("participant_disconnected", ctx.participant)
    await asyncio.wait_for(task, 2)
    ended = client.of("call.ended")[0].payload
    assert ended["llmModelUsed"] == "google/m-sales-llm"
    assert ended["usage"]["llmModel"] == "google/m-sales-llm"
    assert default_model.closed and sales_model.closed


async def test_run_call_menu_spoken_choice(monkeypatch: pytest.MonkeyPatch) -> None:
    boot = make_bootstrap(llm_cfg=make_llm_config()).model_copy(update={"route": MENU_ROUTE})
    support = _profile_boot(SUPPORT, "Туслах", "", make_llm_config())
    client = FakeClient(boot=boot, by_profile={SUPPORT: support})
    ctx, client, task = await _start_call(monkeypatch, boot, client=client)
    session = FakeAgentSession.instances[0]
    await _settle()
    # no LLM reply while the menu runs: the away prompt is suppressed
    session.emit(
        "user_state_changed", UserStateChangedEvent(old_state="listening", new_state="away")
    )
    assert session.replies == []
    session.emit(
        "user_input_transcribed", UserInputTranscribedEvent(transcript="хоёрыг", is_final=True)
    )
    await _settle(50)
    agent = session.agent
    assert type(agent) is CallGoAgent and agent.bootstrap is support
    assert not isinstance(agent.llm, FakeLLM)  # same LLM config -> session LLM is kept
    ctx.room.emit("participant_disconnected", ctx.participant)
    await asyncio.wait_for(task, 2)


async def test_run_call_menu_timeout_falls_back_to_default(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(routing, "MENU_TIMEOUT_MIN_SEC", 0.01)
    route = MENU_ROUTE.model_copy(update={"menu_timeout_sec": 0, "menu_repeat": 1})
    boot = make_bootstrap(llm_cfg=make_llm_config()).model_copy(update={"route": route})
    ctx, client, task = await _start_call(monkeypatch, boot)
    session = FakeAgentSession.instances[0]
    for _ in range(100):
        await asyncio.sleep(0.01)
        if type(session.agent) is CallGoAgent:
            break
    assert [t for t, _ in session.said] == [route.menu_prompt, route.menu_prompt]
    assert type(session.agent) is CallGoAgent and session.agent.bootstrap is boot
    assert len(client.bootstrap_calls) == 1  # no re-bootstrap
    ctx.room.emit("participant_disconnected", ctx.participant)
    await asyncio.wait_for(task, 2)


async def test_run_call_menu_rebootstrap_failure_falls_back(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    boot = make_bootstrap(llm_cfg=make_llm_config()).model_copy(update={"route": MENU_ROUTE})
    client = FakeClient(boot=boot, by_profile={})  # KeyError for any profile id
    ctx, client, task = await _start_call(monkeypatch, boot, client=client)
    session = FakeAgentSession.instances[0]
    await _settle()
    ctx.room.emit("sip_dtmf_received", rtc.SipDTMF(code=2, digit="2", participant=ctx.participant))  # type: ignore[arg-type]
    await _settle(50)
    assert type(session.agent) is CallGoAgent and session.agent.bootstrap is boot
    ctx.room.emit("participant_disconnected", ctx.participant)
    await asyncio.wait_for(task, 2)


class Operator:
    def __init__(self, identity: str = "op-7", role: str = "operator") -> None:
        self.identity = identity
        self.attributes = {"callgo.role": role, "callgo.userId": "user-7"}


async def test_run_call_handoff_wiring(monkeypatch: pytest.MonkeyPatch) -> None:
    transcribed: list[str] = []

    class FakeTranscriber:
        def __init__(self, engine: Any, vad: Any, on_final: Callable[..., None]) -> None:
            self.on_final = on_final

        async def __call__(self, participant: Any) -> None:
            transcribed.append(participant.identity)
            self.on_final("сайн байна уу", 0.9, 1.0)
            await asyncio.Event().wait()

    monkeypatch.setattr(sess, "OperatorTranscriber", FakeTranscriber)
    boot = make_bootstrap(llm_cfg=make_llm_config()).model_copy(
        update={"handoff": HandoffInfo(enabled=True)}
    )
    ctx, client, task = await _start_call(monkeypatch, boot)
    session = FakeAgentSession.instances[0]
    agent = session.agent
    assert "request_operator" in [t.info.name for t in agent.tools]
    assert "# Human operator" in agent.instructions

    state: CallState = session.kw["userdata"]
    tool = next(t for t in agent.tools if t.info.name == "request_operator")
    await tool(types.SimpleNamespace(session=session))
    await _settle()
    assert client.of("call.updated")[-1].payload == {"handoff": "requested"}

    ctx.room.emit("participant_connected", Operator())
    await _settle()
    assert state.passive and transcribed == ["op-7"]
    assert session.said[-1][0] == "Оператор холбогдлоо."
    assert client.of("call.updated")[-1].payload == {"handoff": "active", "operatorId": "user-7"}
    assert client.of("agent.state")[-1].payload["state"] == "handoff"
    human = client.of("transcript.final")[-1].payload["turn"]
    assert human["speaker"] == "human" and human["text"] == "сайн байна уу"
    with pytest.raises(llm.StopResponse):
        await agent.on_user_turn_completed(
            llm.ChatContext.empty(), llm.ChatMessage(role="user", content=["Сайн уу"])
        )
    # the session's own state changes are not published while passive
    session.emit(
        "agent_state_changed", AgentStateChangedEvent(old_state="idle", new_state="listening")
    )
    assert client.of("agent.state")[-1].payload["state"] == "handoff"

    ctx.room.emit("participant_disconnected", Operator())
    await _settle()
    assert not state.passive
    assert session.said[-1][0] == "Би үргэлжлүүлье."
    assert client.of("call.updated")[-1].payload == {"handoff": "ended", "operatorId": "user-7"}
    await agent.on_user_turn_completed(
        llm.ChatContext.empty(), llm.ChatMessage(role="user", content=["Сайн уу"])
    )  # replies again

    ctx.room.emit("participant_disconnected", ctx.participant)
    await asyncio.wait_for(task, 2)
    assert ctx.room.handlers["participant_connected"] == []


async def test_run_call_without_handoff_ignores_operators(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    boot = make_bootstrap(llm_cfg=make_llm_config())
    ctx, _client, task = await _start_call(monkeypatch, boot)
    session = FakeAgentSession.instances[0]
    assert "request_operator" not in [t.info.name for t in session.agent.tools]
    assert ctx.room.handlers["participant_connected"] == []
    ctx.room.emit("participant_disconnected", ctx.participant)
    await asyncio.wait_for(task, 2)


async def test_silent_agent_never_replies_or_greets(monkeypatch: pytest.MonkeyPatch) -> None:
    fake = FakeAgentSession()
    monkeypatch.setattr(CallGoAgent, "session", property(lambda self: fake))
    agent = sess.SilentAgent(bootstrap=make_bootstrap())
    await agent.on_enter()
    assert fake.said == [] and fake.replies == []
    assert agent.passive and agent.tools == []
    with pytest.raises(llm.StopResponse):
        await agent.on_user_turn_completed(
            llm.ChatContext.empty(), llm.ChatMessage(role="user", content=["1"])
        )


async def test_user_turn_exceeded_is_ignored_while_passive(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    fake = FakeAgentSession()
    monkeypatch.setattr(CallGoAgent, "session", property(lambda self: fake))
    passive = [True]
    agent = CallGoAgent(bootstrap=make_bootstrap(), is_passive=lambda: passive[0])
    ev = UserTurnExceededEvent(
        transcript="урт яриа",
        accumulated_transcript="урт яриа",
        accumulated_word_count=2,
        duration=30.0,
    )
    await agent.on_user_turn_exceeded(ev)
    assert fake.replies == []
    passive[0] = False
    await agent.on_user_turn_exceeded(ev)
    assert fake.replies and fake.replies[0]["user_input"] == "урт яриа"


async def test_passive_agent_keeps_customer_turn_in_context() -> None:
    passive = [True]
    agent = CallGoAgent(bootstrap=make_bootstrap(), is_passive=lambda: passive[0])
    msg = llm.ChatMessage(role="user", content=["Оператортой ярьж байна"])
    with pytest.raises(llm.StopResponse):
        await agent.on_user_turn_completed(agent.chat_ctx.copy(), msg)
    assert agent.chat_ctx.items[-1].id == msg.id
    passive[0] = False
    await agent.on_user_turn_completed(agent.chat_ctx.copy(), msg)  # no StopResponse


def test_recorder_operator_turn_and_passive_away() -> None:
    sink = FakeSink()
    emitter = EventEmitter(sink, org_id=ORG, call_id=CALL, flush_interval_ms=60_000)
    clock = Clock()
    rec = CallRecorder(emitter, make_bootstrap(), clock=clock)
    rec.mark_answered()
    clock.t += 5
    rec.add_operator_turn(" Сайн байна уу ", "сайн байна уу", 0.8, 1.5)
    rec.add_operator_turn("  ")
    turn = rec.turns[-1]
    assert (turn.speaker, turn.text, turn.raw_text) == (
        Speaker.HUMAN,
        "Сайн байна уу",
        "сайн байна уу",
    )
    assert (turn.start_ms, turn.end_ms) == (3500, 5000)
    assert "Operator: Сайн байна уу" in rec.transcript_text()

    fake = FakeAgentSession()
    rec.attach(fake)  # type: ignore[arg-type]
    rec.passive = lambda: True
    fake.emit("user_state_changed", UserStateChangedEvent(old_state="listening", new_state="away"))
    assert fake.replies == []


def test_worker_server_registration() -> None:
    import pickle

    from callgo_agent import worker
    from callgo_agent.config import settings

    server = worker.build_server(http=False)
    assert server._agent_name == settings.agent_name
    assert server.setup_fnc is worker.prewarm
    assert server._entrypoint_fnc is worker.entrypoint
    pickle.dumps(worker.entrypoint)  # job processes receive it by reference
    pickle.dumps(worker.prewarm)


def test_prewarm_loads_telephony_vad(monkeypatch: pytest.MonkeyPatch) -> None:
    from callgo_agent import worker

    calls: list[dict[str, Any]] = []

    class FakeVAD:
        @staticmethod
        def load(**kw: Any) -> str:
            calls.append(kw)
            return "vad"

    monkeypatch.setattr(worker, "silero", types.SimpleNamespace(VAD=FakeVAD))
    proc = types.SimpleNamespace(userdata={})
    worker.prewarm(proc)  # type: ignore[arg-type]
    assert proc.userdata == {"vad": "vad"}
    assert calls and calls[0]["min_silence_duration"] < 0.55
