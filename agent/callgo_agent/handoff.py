"""Operator handoff: a human joins the LiveKit room and the voice agent steps back.

The backend gives the operator a LiveKit token whose participant attributes carry
``callgo.role=operator`` and ``callgo.userId`` (docs/API.md, Operator handoff).
:class:`HandoffController` watches the room (``participant_connected``,
``participant_attributes_changed``, ``participant_disconnected``):

* first operator in -> interrupt the agent, say "Оператор холбогдлоо." once, go *passive*
  (``CallState.handoff == "active"``: :class:`~callgo_agent.session.CallGoAgent` raises
  ``StopResponse`` from ``on_user_turn_completed`` so the LLM never answers, while STT keeps
  transcribing the customer), publish ``agent.state = "handoff"`` and
  ``call.updated {"handoff": "active", "operatorId"}``;
* last operator out -> ``call.updated {"handoff": "ended"}``, say "Би үргэлжлүүлье." and
  resume normal replies.

Operator speech: the session's ``RoomIO`` is linked to the SIP caller only, and
``UserInputTranscribedEvent`` in livekit-agents 1.8.3 has no participant identity (only a
diarization ``speaker_id``). So the operator's microphone track is transcribed by a
separate STT stream (:class:`OperatorTranscriber`), and those turns are reported as
``speaker = "human"``.
"""

from __future__ import annotations

import asyncio
import contextlib
import logging
from collections.abc import Awaitable, Callable, Mapping
from typing import Any, Protocol

from livekit import rtc
from livekit.agents import stt

from .events import EventEmitter
from .tools import CallState

log = logging.getLogger("callgo.handoff")

ROLE_ATTRIBUTE = "callgo.role"
OPERATOR_ROLE = "operator"
USER_ID_ATTRIBUTE = "callgo.userId"
OPERATOR_JOINED_MESSAGE = "Оператор холбогдлоо."
AGENT_RESUMED_MESSAGE = "Би үргэлжлүүлье."
OPERATOR_SAMPLE_RATE = 16000


def is_operator(participant: Any) -> bool:
    attrs: Mapping[str, str] = getattr(participant, "attributes", None) or {}
    return attrs.get(ROLE_ATTRIBUTE, "") == OPERATOR_ROLE


def operator_id(participant: Any) -> str:
    attrs: Mapping[str, str] = getattr(participant, "attributes", None) or {}
    return attrs.get(USER_ID_ATTRIBUTE, "")


class HandoffObserver(Protocol):
    """What the controller tells the call recorder (implemented by ``CallRecorder``)."""

    def handoff_started(self) -> None: ...

    def handoff_ended(self) -> None: ...


class _Room(Protocol):
    def on(self, event: str, callback: Callable[..., Any]) -> Any: ...

    def off(self, event: str, callback: Callable[..., Any]) -> Any: ...


Transcribe = Callable[[Any], Awaitable[None]]
"""Coroutine function transcribing one operator participant until cancelled."""


class HandoffController:
    """Tracks operators in the room and switches the agent between active and passive."""

    def __init__(
        self,
        room: _Room,
        session: Any,
        state: CallState,
        emitter: EventEmitter,
        *,
        customer_identity: str = "",
        observer: HandoffObserver | None = None,
        transcribe: Transcribe | None = None,
        joined_message: str = OPERATOR_JOINED_MESSAGE,
        resumed_message: str = AGENT_RESUMED_MESSAGE,
    ) -> None:
        self._room = room
        self._session = session
        self._state = state
        self._emitter = emitter
        self._customer = customer_identity
        self._observer = observer
        self._transcribe = transcribe
        self._joined_message = joined_message
        self._resumed_message = resumed_message
        self._operators: dict[str, Any] = {}
        self._transcribers: dict[str, asyncio.Task[None]] = {}
        self._started = False
        self.activations = 0

    @property
    def active(self) -> bool:
        return bool(self._operators)

    @property
    def operators(self) -> list[str]:
        return list(self._operators)

    # -- lifecycle --

    def start(self) -> None:
        if self._started:
            return
        self._started = True
        self._room.on("participant_connected", self._on_connected)
        self._room.on("participant_disconnected", self._on_disconnected)
        self._room.on("participant_attributes_changed", self._on_attributes_changed)
        # an operator may already be in the room (e.g. joined while the agent started)
        for participant in list(getattr(self._room, "remote_participants", {}).values()):
            self._on_connected(participant)

    async def aclose(self) -> None:
        if self._started:
            self._started = False
            for event, cb in (
                ("participant_connected", self._on_connected),
                ("participant_disconnected", self._on_disconnected),
                ("participant_attributes_changed", self._on_attributes_changed),
            ):
                with contextlib.suppress(Exception):
                    self._room.off(event, cb)
        tasks = list(self._transcribers.values())
        self._transcribers.clear()
        for task in tasks:
            task.cancel()
        if tasks:
            await asyncio.gather(*tasks, return_exceptions=True)

    # -- room events --

    def _on_connected(self, participant: Any) -> None:
        if is_operator(participant) and participant.identity != self._customer:
            self._operator_joined(participant)

    def _on_disconnected(self, participant: Any) -> None:
        if participant.identity in self._operators:
            self._operator_left(participant)

    def _on_attributes_changed(self, changed: Mapping[str, str], participant: Any) -> None:
        if participant.identity == self._customer:
            return
        known = participant.identity in self._operators
        if is_operator(participant) and not known:
            self._operator_joined(participant)
        elif known and not is_operator(participant):
            self._operator_left(participant)

    # -- transitions --

    def _operator_joined(self, participant: Any) -> None:
        first = not self._operators
        self._operators[participant.identity] = participant
        log.info("operator %s joined the call", participant.identity)
        self._start_transcriber(participant)
        if not first:
            return
        self.activations += 1
        self._state.handoff = "active"
        self._interrupt()
        self._say(self._joined_message)
        if self._observer is not None:
            self._observer.handoff_started()
        else:
            self._emitter.agent_state("handoff")
        self._emitter.call_handoff("active", operator_id(participant))

    def _operator_left(self, participant: Any) -> None:
        self._operators.pop(participant.identity, None)
        self._stop_transcriber(participant.identity)
        log.info("operator %s left the call", participant.identity)
        if self._operators:
            return
        self._state.handoff = "ended"
        self._emitter.call_handoff("ended", operator_id(participant))
        if self._observer is not None:
            self._observer.handoff_ended()
        self._say(self._resumed_message)

    # -- session helpers --

    def _interrupt(self) -> None:
        try:
            fut = self._session.interrupt(force=True)
        except Exception as exc:  # noqa: BLE001 - nothing to interrupt / session closing
            log.debug("interrupt before handoff failed: %s", exc)
            return
        if isinstance(fut, asyncio.Future):
            fut.add_done_callback(_consume_exception)

    def _say(self, text: str) -> None:
        if not text:
            return
        try:
            self._session.say(text, allow_interruptions=False)
        except Exception as exc:  # noqa: BLE001 - session closing
            log.warning("could not say %r: %s", text, exc)

    def _start_transcriber(self, participant: Any) -> None:
        if self._transcribe is None or participant.identity in self._transcribers:
            return
        try:
            task = asyncio.get_running_loop().create_task(
                self._run_transcriber(participant), name=f"operator-stt-{participant.identity}"
            )
        except RuntimeError:
            return
        self._transcribers[participant.identity] = task

    def _stop_transcriber(self, identity: str) -> None:
        task = self._transcribers.pop(identity, None)
        if task is not None:
            task.cancel()

    async def _run_transcriber(self, participant: Any) -> None:
        assert self._transcribe is not None
        try:
            await self._transcribe(participant)
        except asyncio.CancelledError:
            raise
        except Exception:
            log.exception("operator transcription for %s failed", participant.identity)


def _consume_exception(fut: asyncio.Future[Any]) -> None:
    if not fut.cancelled():
        fut.exception()


OperatorFinal = Callable[[str, float, float], None]
"""``(text, confidence, duration_sec)`` for each final operator transcript."""


class OperatorTranscriber:
    """Transcribes an operator's microphone with its own STT stream (best effort).

    Non-streaming engines (the local Whisper STT) are wrapped in ``stt.StreamAdapter``
    with the call's VAD, exactly like ``Agent.default.stt_node`` does for the caller.
    Recognitions surface through the wrapped STT's ``metrics_collected``, so the
    session's own adapter forwards them to usage metering as well.
    """

    def __init__(
        self,
        engine: stt.STT,
        vad: Any,
        on_final: OperatorFinal,
        *,
        language: str = "",
        audio_stream: Callable[[Any], Any] | None = None,
    ) -> None:
        self._engine = engine
        self._vad = vad
        self._on_final = on_final
        self._language = language
        self._audio_stream = audio_stream or _microphone_stream

    def _streaming_engine(self) -> stt.STT:
        if self._engine.capabilities.streaming:
            return self._engine
        if self._vad is None:
            raise RuntimeError("a VAD is required to stream a non-streaming STT")
        return stt.StreamAdapter(stt=self._engine, vad=self._vad)

    async def __call__(self, participant: Any) -> None:
        engine = self._streaming_engine()
        audio = self._audio_stream(participant)
        stream = engine.stream(language=self._language) if self._language else engine.stream()

        async def pump() -> None:
            async for ev in audio:
                stream.push_frame(ev.frame)
            stream.end_input()

        pump_task = asyncio.create_task(pump(), name="operator-audio-pump")
        try:
            async for ev in stream:
                if ev.type != stt.SpeechEventType.FINAL_TRANSCRIPT or not ev.alternatives:
                    continue
                alt = ev.alternatives[0]
                text = alt.text.strip()
                if not text:
                    continue
                duration = max(0.0, float(alt.end_time) - float(alt.start_time))
                try:
                    self._on_final(text, float(alt.confidence), duration)
                except Exception:
                    log.exception("operator transcript callback failed")
        finally:
            pump_task.cancel()
            with contextlib.suppress(BaseException):
                await pump_task
            with contextlib.suppress(Exception):
                await stream.aclose()
            with contextlib.suppress(Exception):
                await audio.aclose()
            if engine is not self._engine:
                with contextlib.suppress(Exception):
                    await engine.aclose()


def _microphone_stream(participant: Any) -> rtc.AudioStream:
    return rtc.AudioStream.from_participant(
        participant=participant,
        track_source=rtc.TrackSource.SOURCE_MICROPHONE,
        sample_rate=OPERATOR_SAMPLE_RATE,
        num_channels=1,
    )
