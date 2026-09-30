from __future__ import annotations

import asyncio
import sys
import types
from collections import defaultdict
from collections.abc import AsyncIterator, Callable
from typing import Any, ClassVar, Self
from uuid import UUID

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
    llm,
    stt,
)

from callgo_agent import session as sess
from callgo_agent.events import EventEmitter
from callgo_agent.schemas import (
    AgentProfile,
    Bootstrap,
    Call,
    CallDirection,
    CallStatus,
    CampaignInfo,
    Contact,
    Event,
    JobMetadata,
    LexiconEntry,
    LexiconScope,
    LLMConfig,
    LLMProvider,
    Organization,
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
    analyze_call,
    build_instructions,
    build_turn_handling,
    end_reason_for_close,
    enforce_max_duration,
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
        ),
        llm=llm_cfg,
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


# ---- templates & instructions --------------------------------------------------------


def test_substitute_vars() -> None:
    assert substitute_vars("Hi {{name}}, {{ x }}!{{nope}}", {"name": "Bat", "x": "1"}) == "Hi Bat, 1!"
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


def test_build_instructions_other_language_and_defaults() -> None:
    boot = make_bootstrap(language="en", system_prompt="", direction=CallDirection.OUTBOUND)
    text = build_instructions(boot)
    assert "You are Сараа" in text
    assert "Always answer in English." in text
    assert "Mongolian" not in text
    assert "outbound call" in text


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
    async def wait_for_playout(self) -> None:
        await asyncio.sleep(0)


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
        FakeAgentSession.instances.append(self)

    def on(self, name: str, cb: Callable[[Any], None]) -> Callable[[Any], None]:
        self.handlers[name].append(cb)
        return cb

    def emit(self, name: str, ev: Any) -> None:
        for cb in list(self.handlers[name]):
            cb(ev)

    async def start(self, agent: Any, *, room: Any, room_options: Any) -> None:
        self.agent, self.room_options, self.started = agent, room_options, True

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


TURNS = [
    TranscriptTurn(call_id=CALL, speaker=Speaker.AGENT, text="Сайн байна уу?"),
    TranscriptTurn(call_id=CALL, speaker=Speaker.CUSTOMER, text="Захиалгаа шалгах гэсэн юм."),
]


def test_parse_analysis_variants() -> None:
    ok = parse_analysis('{"summary": "Харилцагч захиалгаа шалгасан.", "sentiment": "Positive",'
                        ' "intent": "Order Status"}')
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


async def test_analyze_call_uses_llm_json() -> None:
    model = FakeLLM(['{"summary": "Захиалга ', 'шалгав.", "sentiment": "positive", ',
                     '"intent": "order_status"}'])
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
    state.callbacks.append(CallbackRequest(when="маргааш 10:00", note="үнийн санал"))
    control = FakeControl()
    model = FakeLLM(['{"summary": "Захиалга шалгав.", "sentiment": "negative", "intent": "x"}'])
    fin = CallFinalizer(
        state=state,
        recorder=rec,
        emitter=emitter,
        control=control,
        model=model,  # type: ignore[arg-type]
        llm_label="google/gemini-2.5-flash",
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
    }


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
    def __init__(self, boot: Bootstrap | None = None, error: Exception | None = None) -> None:
        super().__init__()
        self.boot, self.error = boot, error
        self.bootstrap_kwargs: dict[str, Any] = {}

    async def bootstrap(self, **kw: Any) -> Bootstrap:
        self.bootstrap_kwargs = kw
        if self.error:
            raise self.error
        assert self.boot is not None
        return self.boot

    async def aclose(self) -> None:
        self.closed = True


def make_factories(model: FakeLLM | None = None, fail: bool = False) -> PipelineFactories:
    def build_llm(cfg: LLMConfig, fallbacks: list[LLMConfig]) -> Any:
        if fail:
            raise RuntimeError("no api key")
        return model

    return PipelineFactories(
        build_stt=lambda p: "STT",  # type: ignore[arg-type,return-value]
        build_tts=lambda p: "TTS",  # type: ignore[arg-type,return-value]
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

    # speech flows through the agent's STT hook, then the session reports it
    session.agent.process_speech_event(
        _speech(stt.SpeechEventType.FINAL_TRANSCRIPT, "калл го сайн уу")
    )
    session.emit(
        "user_input_transcribed", UserInputTranscribedEvent(transcript="CallGo сайн уу", is_final=True)
    )
    session.emit(
        "conversation_item_added",
        ConversationItemAddedEvent(item=llm.ChatMessage(role="assistant", content=["Сайн уу"])),
    )
    session.emit("agent_state_changed", AgentStateChangedEvent(old_state="idle", new_state="listening"))

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
