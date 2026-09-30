from __future__ import annotations

import asyncio
from collections import defaultdict
from collections.abc import AsyncIterator, Callable
from typing import Any
from uuid import UUID

import pytest
from livekit.agents import stt

from callgo_agent.events import EventEmitter
from callgo_agent.handoff import (
    AGENT_RESUMED_MESSAGE,
    OPERATOR_JOINED_MESSAGE,
    HandoffController,
    OperatorTranscriber,
    is_operator,
    operator_id,
)
from callgo_agent.schemas import (
    AgentProfile,
    Bootstrap,
    Call,
    CallDirection,
    CallStatus,
    Event,
    HandoffInfo,
    Organization,
)
from callgo_agent.session import CallRecorder
from callgo_agent.tools import CallState

ORG = UUID("11111111-1111-1111-1111-111111111111")
CALL = UUID("22222222-2222-2222-2222-222222222222")


def make_bootstrap() -> Bootstrap:
    return Bootstrap(
        call=Call(id=CALL, org_id=ORG, direction=CallDirection.INBOUND, status=CallStatus.ACTIVE),
        org=Organization(id=ORG, name="Demo", slug="demo"),
        profile=AgentProfile(id=UUID(int=3), org_id=ORG, name="Сараа"),
        handoff=HandoffInfo(enabled=True),
    )


class Participant:
    def __init__(self, identity: str, role: str = "operator", user: str = "") -> None:
        self.identity = identity
        self.attributes: dict[str, str] = {}
        if role:
            self.attributes["callgo.role"] = role
        if user:
            self.attributes["callgo.userId"] = user


class FakeRoom:
    def __init__(self) -> None:
        self.handlers: dict[str, list[Callable[..., None]]] = defaultdict(list)
        self.remote_participants: dict[str, Any] = {}

    def on(self, name: str, cb: Callable[..., None]) -> None:
        self.handlers[name].append(cb)

    def off(self, name: str, cb: Callable[..., None]) -> None:
        self.handlers[name].remove(cb)

    def emit(self, name: str, *args: Any) -> None:
        for cb in list(self.handlers[name]):
            cb(*args)


class FakeSession:
    def __init__(self, *, closing: bool = False) -> None:
        self.said: list[tuple[str, dict[str, Any]]] = []
        self.interrupts: list[bool] = []
        self.closing = closing

    def say(self, text: str, **kw: Any) -> None:
        if self.closing:
            raise RuntimeError("AgentSession is closing")
        self.said.append((text, kw))

    def interrupt(self, *, force: bool = False) -> asyncio.Future[None]:
        if self.closing:
            raise RuntimeError("AgentSession isn't running")
        self.interrupts.append(force)
        fut: asyncio.Future[None] = asyncio.get_running_loop().create_future()
        fut.set_exception(RuntimeError("nothing to interrupt"))  # consumed, no warning
        return fut


class Sink:
    def __init__(self) -> None:
        self.events: list[Event] = []

    async def post_events(self, events: list[Event]) -> int:
        self.events.extend(events)
        return len(events)


class Harness:
    def __init__(self, *, transcribe: Any = None, session: FakeSession | None = None) -> None:
        self.room = FakeRoom()
        self.session = session or FakeSession()
        self.boot = make_bootstrap()
        self.state = CallState(bootstrap=self.boot)
        self.sink = Sink()
        self.emitter = EventEmitter(self.sink, org_id=ORG, call_id=CALL, flush_interval_ms=60_000)
        self.recorder = CallRecorder(self.emitter, self.boot, llm_label="google/x")
        self.recorder.passive = lambda: self.state.passive
        self.ctl = HandoffController(
            self.room,
            self.session,
            self.state,
            self.emitter,
            customer_identity="sip_caller",
            observer=self.recorder,
            transcribe=transcribe,
        )

    async def events(self) -> list[tuple[str, dict[str, Any]]]:
        await self.emitter.flush()
        return [(e.type, e.payload) for e in self.sink.events]


def test_operator_attributes() -> None:
    assert (
        is_operator(Participant("op", user="u1"))
        and operator_id(Participant("op", user="u1")) == "u1"
    )
    assert not is_operator(Participant("x", role="customer"))
    assert not is_operator(object())
    assert operator_id(object()) == ""


async def test_join_and_leave_sequence() -> None:
    h = Harness()
    h.ctl.start()
    h.ctl.start()  # idempotent
    assert len(h.room.handlers["participant_connected"]) == 1

    h.room.emit("participant_connected", Participant("viewer", role=""))
    assert not h.ctl.active and h.session.said == []

    op = Participant("op-1", user="user-1")
    h.room.emit("participant_connected", op)
    await asyncio.sleep(0)
    assert h.ctl.active and h.state.passive and h.state.handoff == "active"
    assert h.session.interrupts == [True]
    assert h.session.said == [(OPERATOR_JOINED_MESSAGE, {"allow_interruptions": False})]

    # a second operator neither repeats the message nor re-publishes
    h.room.emit("participant_connected", Participant("op-2", user="user-2"))
    h.room.emit("participant_disconnected", op)
    assert h.state.passive and len(h.session.said) == 1

    h.room.emit("participant_disconnected", Participant("op-2", user="user-2"))
    assert not h.ctl.active and h.state.handoff == "ended" and not h.state.passive
    assert h.session.said[-1] == (AGENT_RESUMED_MESSAGE, {"allow_interruptions": False})

    assert await h.events() == [
        ("agent.state", {"state": "handoff", "llmModel": "google/x"}),
        ("call.updated", {"handoff": "active", "operatorId": "user-1"}),
        ("call.updated", {"handoff": "ended", "operatorId": "user-2"}),
    ]
    assert h.ctl.activations == 1

    # a new operator later -> a new activation with the message again
    h.room.emit("participant_connected", Participant("op-3"))
    assert h.ctl.activations == 2 and h.session.said[-1][0] == OPERATOR_JOINED_MESSAGE
    await h.ctl.aclose()
    assert h.room.handlers["participant_connected"] == []


async def test_agent_states_are_muted_while_passive() -> None:
    from livekit.agents import AgentStateChangedEvent

    h = Harness()
    h.ctl.start()
    h.room.emit("participant_connected", Participant("op-1"))
    h.recorder.on_agent_state_changed(
        AgentStateChangedEvent(old_state="idle", new_state="speaking")
    )
    h.room.emit("participant_disconnected", Participant("op-1"))
    h.recorder.on_agent_state_changed(
        AgentStateChangedEvent(old_state="speaking", new_state="speaking")
    )
    states = [p["state"] for t, p in await h.events() if t == "agent.state"]
    assert states == ["handoff", "speaking"]


async def test_attribute_changes_promote_and_demote() -> None:
    h = Harness()
    h.ctl.start()
    p = Participant("browser-user", role="")
    h.room.emit("participant_connected", p)
    assert not h.ctl.active
    p.attributes["callgo.role"] = "operator"
    h.room.emit("participant_attributes_changed", {"callgo.role": "operator"}, p)
    assert h.ctl.active
    h.room.emit("participant_attributes_changed", {"x": "1"}, p)  # still operator
    assert h.ctl.operators == ["browser-user"]
    p.attributes["callgo.role"] = "viewer"
    h.room.emit("participant_attributes_changed", {"callgo.role": "viewer"}, p)
    assert not h.ctl.active and h.state.handoff == "ended"

    # the SIP caller can never become an operator
    caller = Participant("sip_caller")
    h.room.emit("participant_connected", caller)
    h.room.emit("participant_attributes_changed", {}, caller)
    assert not h.ctl.active


async def test_operator_already_in_room_on_start() -> None:
    h = Harness()
    h.room.remote_participants = {
        "op": Participant("op"),
        "sip_caller": Participant("sip_caller", role=""),
    }
    h.ctl.start()
    assert h.ctl.operators == ["op"] and h.state.passive


async def test_session_errors_do_not_break_handoff() -> None:
    h = Harness(session=FakeSession(closing=True))
    h.ctl.start()
    h.room.emit("participant_connected", Participant("op"))
    assert h.state.passive
    h.room.emit("participant_disconnected", Participant("op"))
    assert h.state.handoff == "ended"


async def test_transcribers_follow_operators() -> None:
    started: list[str] = []
    cancelled: list[str] = []

    async def transcribe(participant: Any) -> None:
        started.append(participant.identity)
        try:
            await asyncio.Event().wait()
        except asyncio.CancelledError:
            cancelled.append(participant.identity)
            raise

    h = Harness(transcribe=transcribe)
    h.ctl.start()
    h.room.emit("participant_connected", Participant("op-1"))
    h.room.emit("participant_connected", Participant("op-2"))
    await asyncio.sleep(0)
    assert started == ["op-1", "op-2"]
    h.room.emit("participant_disconnected", Participant("op-1"))
    await asyncio.sleep(0)
    assert cancelled == ["op-1"]
    await h.ctl.aclose()
    assert cancelled == ["op-1", "op-2"]


async def test_transcriber_failure_is_logged(caplog: pytest.LogCaptureFixture) -> None:
    async def transcribe(participant: Any) -> None:
        raise RuntimeError("no track")

    h = Harness(transcribe=transcribe)
    h.ctl.start()
    h.room.emit("participant_connected", Participant("op-1"))
    await asyncio.sleep(0.01)
    assert "operator transcription for op-1 failed" in caplog.text
    assert h.state.passive  # handoff itself is unaffected
    await h.ctl.aclose()


# ---- OperatorTranscriber ------------------------------------------------------------


def final(text: str, start: float = 0.0, end: float = 0.0) -> stt.SpeechEvent:
    return stt.SpeechEvent(
        type=stt.SpeechEventType.FINAL_TRANSCRIPT,
        alternatives=[
            stt.SpeechData(language="mn", text=text, confidence=0.7, start_time=start, end_time=end)  # type: ignore[arg-type]
        ],
    )


class FakeRecognizeStream:
    def __init__(self, events: list[stt.SpeechEvent]) -> None:
        self.events = events
        self.frames: list[Any] = []
        self.ended = asyncio.Event()
        self.closed = False

    def push_frame(self, frame: Any) -> None:
        self.frames.append(frame)

    def end_input(self) -> None:
        self.ended.set()

    async def aclose(self) -> None:
        self.closed = True

    def __aiter__(self) -> AsyncIterator[stt.SpeechEvent]:
        return self._gen()

    async def _gen(self) -> AsyncIterator[stt.SpeechEvent]:
        await self.ended.wait()
        for ev in self.events:
            yield ev


class FakeSTT:
    def __init__(self, streaming: bool, events: list[stt.SpeechEvent] | None = None) -> None:
        self.capabilities = stt.STTCapabilities(streaming=streaming, interim_results=False)
        self.stream_obj = FakeRecognizeStream(events or [])
        self.handlers: list[Any] = []

    def stream(self, **kw: Any) -> FakeRecognizeStream:
        return self.stream_obj

    def on(self, name: str, cb: Any) -> None:
        self.handlers.append(cb)

    def off(self, name: str, cb: Any) -> None:
        self.handlers.remove(cb)


class FakeAudio:
    def __init__(self, n: int) -> None:
        self.n = n
        self.closed = False

    def __aiter__(self) -> AsyncIterator[Any]:
        return self._gen()

    async def _gen(self) -> AsyncIterator[Any]:
        for i in range(self.n):
            yield type("Ev", (), {"frame": f"frame-{i}"})()

    async def aclose(self) -> None:
        self.closed = True


async def test_operator_transcriber_reports_finals() -> None:
    engine = FakeSTT(
        True,
        [
            stt.SpeechEvent(type=stt.SpeechEventType.START_OF_SPEECH),
            final("  "),
            final("Сайн байна уу", 1.0, 2.5),
            final("Би туслая"),
        ],
    )
    audio = FakeAudio(3)
    got: list[tuple[str, float, float]] = []
    tr = OperatorTranscriber(
        engine,  # type: ignore[arg-type]
        None,
        lambda t, c, d: got.append((t, c, d)),
        audio_stream=lambda p: audio,
    )
    await asyncio.wait_for(tr(Participant("op")), 1)
    assert got == [("Сайн байна уу", 0.7, 1.5), ("Би туслая", 0.7, 0.0)]
    assert engine.stream_obj.frames == ["frame-0", "frame-1", "frame-2"]
    assert engine.stream_obj.closed and audio.closed


def test_operator_transcriber_wraps_non_streaming_stt() -> None:
    engine = FakeSTT(False)
    tr = OperatorTranscriber(engine, object(), lambda *a: None)  # type: ignore[arg-type]
    assert isinstance(tr._streaming_engine(), stt.StreamAdapter)
    with pytest.raises(RuntimeError, match="VAD"):
        OperatorTranscriber(engine, None, lambda *a: None)._streaming_engine()  # type: ignore[arg-type]
