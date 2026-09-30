from __future__ import annotations

import asyncio
from datetime import UTC
from uuid import UUID, uuid4

import pytest

from callgo_agent.events import EventEmitter
from callgo_agent.schemas import (
    Call,
    CallDirection,
    CallEndedPayload,
    CallStatus,
    Event,
    Sentiment,
    Speaker,
    TranscriptTurn,
)

ORG = UUID("11111111-1111-1111-1111-111111111111")
CALL = UUID("22222222-2222-2222-2222-222222222222")


class FakeSink:
    def __init__(self, fail_times: int = 0) -> None:
        self.batches: list[list[Event]] = []
        self.fail_times = fail_times
        self.attempts = 0

    async def post_events(self, events: list[Event]) -> int:
        self.attempts += 1
        if self.fail_times > 0:
            self.fail_times -= 1
            raise RuntimeError("backend down")
        self.batches.append(list(events))
        return len(events)

    @property
    def events(self) -> list[Event]:
        return [e for b in self.batches for e in b]


def make(sink: FakeSink, **kw: int) -> EventEmitter:
    return EventEmitter(sink, org_id=ORG, call_id=CALL, **kw)


async def test_emit_builds_envelope() -> None:
    em = make(FakeSink())
    ev = em.agent_state("thinking", "google/gemini-2.5-flash")
    assert ev is not None
    assert UUID(ev.id).version == 4
    assert ev.org_id == ORG and ev.call_id == CALL
    assert ev.at.tzinfo is not None and ev.at.utcoffset() == UTC.utcoffset(None)
    assert ev.payload == {"state": "thinking", "llmModel": "google/gemini-2.5-flash"}
    assert em.pending == 1


async def test_flush_batches_by_max_batch() -> None:
    sink = FakeSink()
    em = make(sink, max_batch=2, flush_interval_ms=60_000)
    for i in range(5):
        em.transcript_partial(Speaker.CUSTOMER, f"t{i}", i)
    assert await em.flush() == 5
    assert [len(b) for b in sink.batches] == [2, 2, 1]
    assert [e.payload["text"] for e in sink.events] == ["t0", "t1", "t2", "t3", "t4"]
    assert em.pending == 0
    await em.aclose()


async def test_periodic_flush() -> None:
    sink = FakeSink()
    em = make(sink, flush_interval_ms=20)
    em.start()
    em.call_answered()
    await asyncio.sleep(0.15)
    assert [e.type for e in sink.events] == ["call.answered"]
    await em.aclose()


async def test_full_batch_wakes_flusher_early() -> None:
    sink = FakeSink()
    em = make(sink, flush_interval_ms=60_000, max_batch=3)
    em.start()
    for i in range(3):
        em.system(f"m{i}")
    await asyncio.sleep(0.05)
    assert len(sink.events) == 3
    await em.aclose()


async def test_failed_flush_requeues_persistent_and_drops_ephemeral() -> None:
    sink = FakeSink(fail_times=1)
    em = make(sink, flush_interval_ms=60_000)
    em.transcript_partial("customer", "partial", 0)
    turn = TranscriptTurn(call_id=CALL, speaker=Speaker.CUSTOMER, text="final")
    em.transcript_final(turn)
    em.agent_state("listening")

    assert await em.flush() == 0  # never raises
    assert em.pending == 1
    assert em.dropped == 2

    assert await em.flush() == 1
    assert [e.type for e in sink.events] == ["transcript.final"]
    await em.aclose()


async def test_requeued_events_keep_order_before_new_ones() -> None:
    sink = FakeSink(fail_times=1)
    em = make(sink, flush_interval_ms=60_000)
    em.system("a")
    await em.flush()
    em.system("b")
    await em.flush()
    assert [e.payload["message"] for e in sink.events] == ["a", "b"]


async def test_buffer_is_bounded() -> None:
    sink = FakeSink(fail_times=100)
    em = make(sink, flush_interval_ms=60_000, max_batch=2, max_buffer=3)
    for i in range(5):
        em.system(str(i))
    assert em.pending == 3
    assert [e.payload["message"] for e in em._buffer] == ["2", "3", "4"]


async def test_aclose_flushes_and_later_emits_are_dropped() -> None:
    sink = FakeSink()
    em = make(sink, flush_interval_ms=60_000)
    em.start()
    em.call_ended(CallEndedPayload(end_reason="hangup_agent", summary="s", duration_sec=12))
    await em.aclose()
    assert [e.type for e in sink.events] == ["call.ended"]
    assert em.closed
    em.system("late")  # must not raise
    assert em.pending == 0
    await em.aclose()  # idempotent


async def test_aclose_with_dead_backend_does_not_raise() -> None:
    sink = FakeSink(fail_times=10)
    em = make(sink)
    em.system("x")
    await em.aclose()
    assert em.dropped == 1


async def test_payload_shapes() -> None:
    em = make(FakeSink())
    turn = TranscriptTurn(
        call_id=CALL,
        seq=3,
        speaker=Speaker.AGENT,
        text="Сайн байна уу",
        raw_text="Сайн байна уу",
        confidence=1.0,
        start_ms=100,
        end_ms=900,
    )
    final = em.transcript_final(turn)
    assert final is not None
    assert final.payload == {
        "turn": {
            "callId": str(CALL),
            "seq": 3,
            "speaker": "agent",
            "text": "Сайн байна уу",
            "rawText": "Сайн байна уу",
            "confidence": 1.0,
            "startMs": 100,
            "endMs": 900,
            "isFinal": True,
        }
    }

    partial = em.transcript_partial("customer", "сай", 50)
    assert partial is not None
    assert partial.payload == {"speaker": "customer", "text": "сай", "startMs": 50}

    ended = em.call_ended(
        CallEndedPayload(
            end_reason="max_duration",
            summary="Хураангуй",
            sentiment=Sentiment.POSITIVE,
            intent="order_status",
            duration_sec=61,
            llm_model_used="openai/gpt-4o-mini",
        )
    )
    assert ended is not None
    assert ended.payload == {
        "endReason": "max_duration",
        "summary": "Хураангуй",
        "sentiment": "positive",
        "intent": "order_status",
        "durationSec": 61,
        "llmModelUsed": "openai/gpt-4o-mini",
    }

    call = Call(
        id=CALL,
        org_id=ORG,
        direction=CallDirection.INBOUND,
        status=CallStatus.ACTIVE,
        from_number="+976991",
    )
    answered = em.call_answered(call)
    assert answered is not None
    assert answered.payload["call"]["status"] == "active"
    assert answered.payload["call"]["fromNumber"] == "+976991"


async def test_invalid_event_type_is_logged_not_raised(caplog: pytest.LogCaptureFixture) -> None:
    em = make(FakeSink())
    assert em.emit("not.a.type", {}) is None  # type: ignore[arg-type]
    assert em.pending == 0


async def test_emitter_without_call_id() -> None:
    sink = FakeSink()
    em = EventEmitter(sink, org_id=uuid4(), call_id=None)
    em.system("hello")
    await em.flush()
    assert sink.events[0].call_id is None
