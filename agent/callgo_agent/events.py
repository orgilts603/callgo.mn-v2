"""Buffered producer of live events (docs/EVENTS.md) for one call.

:class:`EventEmitter` builds :class:`~callgo_agent.schemas.Event` envelopes (uuid4 ids,
UTC timestamps, org/call ids), buffers them and posts them in batches to the backend every
``settings.event_flush_interval_ms`` and on :meth:`EventEmitter.flush` /
:meth:`EventEmitter.aclose`.

It sits on the real-time call path, so no method ever raises: failures are logged, and
events that the backend persists are re-queued (bounded) for the next flush.
"""

from __future__ import annotations

import asyncio
import contextlib
import logging
import uuid
from datetime import UTC, datetime
from typing import Any, Protocol
from uuid import UUID

from .config import settings
from .schemas import (
    AgentState,
    Call,
    CallEndedPayload,
    Event,
    EventType,
    Speaker,
    TranscriptTurn,
)

log = logging.getLogger("callgo.events")

# Events that are only useful live; dropped instead of re-queued when a post fails.
EPHEMERAL_TYPES: frozenset[str] = frozenset({"transcript.partial", "agent.state"})


class EventSink(Protocol):
    async def post_events(self, events: list[Event]) -> int: ...


def utcnow() -> datetime:
    return datetime.now(UTC)


def _dump(model: Any) -> dict[str, Any]:
    return model.model_dump(mode="json", by_alias=True, exclude_none=True)  # type: ignore[no-any-return]


class EventEmitter:
    """Per-call event buffer with periodic background flushing."""

    def __init__(
        self,
        sink: EventSink,
        *,
        org_id: UUID,
        call_id: UUID | None,
        flush_interval_ms: int | None = None,
        max_batch: int = 100,
        max_buffer: int = 2000,
    ) -> None:
        self._sink = sink
        self.org_id = org_id
        self.call_id = call_id
        interval_ms = (
            settings.event_flush_interval_ms if flush_interval_ms is None else flush_interval_ms
        )
        self._interval = max(0.01, interval_ms / 1000.0)
        self._max_batch = max(1, max_batch)
        self._max_buffer = max(self._max_batch, max_buffer)
        self._buffer: list[Event] = []
        self._lock = asyncio.Lock()
        self._wake = asyncio.Event()
        self._task: asyncio.Task[None] | None = None
        self._closed = False
        self._failures = 0
        self.sent = 0
        self.dropped = 0

    # ---- lifecycle -----------------------------------------------------------------

    @property
    def pending(self) -> int:
        return len(self._buffer)

    @property
    def closed(self) -> bool:
        return self._closed

    def start(self) -> None:
        """Start the periodic flusher (requires a running event loop). Idempotent."""
        if self._task is None and not self._closed:
            self._task = asyncio.get_running_loop().create_task(
                self._run(), name="callgo-event-flusher"
            )

    async def aclose(self) -> None:
        """Stop the flusher and post everything still buffered (best effort)."""
        if self._closed:
            return
        self._closed = True
        if self._task is not None:
            self._task.cancel()
            with contextlib.suppress(asyncio.CancelledError, Exception):
                await self._task
            self._task = None
        await self.flush()
        if self._buffer:
            log.error("dropping %d undelivered events at close", len(self._buffer))
            self.dropped += len(self._buffer)
            self._buffer.clear()

    async def _run(self) -> None:
        while not self._closed:
            if self._failures:
                # back off while the backend is unhealthy; ignore wake-ups meanwhile
                await asyncio.sleep(self._interval * min(2**self._failures, 40))
            else:
                with contextlib.suppress(TimeoutError):
                    await asyncio.wait_for(self._wake.wait(), timeout=self._interval)
            self._wake.clear()
            await self.flush()

    async def flush(self) -> int:
        """Post all buffered events now. Returns how many were delivered. Never raises."""
        delivered = 0
        try:
            async with self._lock:
                while self._buffer:
                    batch = self._buffer[: self._max_batch]
                    del self._buffer[: len(batch)]
                    try:
                        await self._sink.post_events(batch)
                    except Exception as exc:  # noqa: BLE001 - never raise into the call path
                        self._failures += 1
                        keep = [e for e in batch if e.type not in EPHEMERAL_TYPES]
                        self.dropped += len(batch) - len(keep)
                        self._buffer[0:0] = keep
                        self._trim()
                        log.warning(
                            "posting %d events failed (%s); %d re-queued",
                            len(batch),
                            exc,
                            len(keep),
                        )
                        break
                    else:
                        self._failures = 0
                        delivered += len(batch)
                        self.sent += len(batch)
        except Exception:
            log.exception("unexpected error while flushing events")
        return delivered

    def _trim(self) -> None:
        overflow = len(self._buffer) - self._max_buffer
        if overflow > 0:
            del self._buffer[:overflow]
            self.dropped += overflow
            log.error("event buffer full, dropped %d oldest events", overflow)

    # ---- producing -----------------------------------------------------------------

    def emit(self, type: EventType, payload: dict[str, Any] | None = None) -> Event | None:
        """Build and buffer one event. Returns it, or ``None`` if it could not be built."""
        try:
            event = Event(
                id=str(uuid.uuid4()),
                type=type,
                org_id=self.org_id,
                call_id=self.call_id,
                at=utcnow(),
                payload=payload or {},
            )
        except Exception:
            log.exception("failed to build %s event", type)
            return None
        if self._closed:
            log.warning("emitter closed; dropping %s event", type)
            self.dropped += 1
            return event
        self._buffer.append(event)
        self._trim()
        if len(self._buffer) >= self._max_batch:
            self._wake.set()
        return event

    def call_answered(self, call: Call | None = None) -> Event | None:
        return self.emit("call.answered", {"call": _dump(call)} if call is not None else {})

    def call_updated(self, call: Call) -> Event | None:
        return self.emit("call.updated", {"call": _dump(call)})

    def transcript_partial(self, speaker: Speaker | str, text: str, start_ms: int) -> Event | None:
        return self.emit(
            "transcript.partial",
            {"speaker": Speaker(speaker).value, "text": text, "startMs": int(start_ms)},
        )

    def transcript_final(self, turn: TranscriptTurn) -> Event | None:
        return self.emit("transcript.final", {"turn": _dump(turn)})

    def agent_state(self, state: AgentState, llm_model: str = "") -> Event | None:
        return self.emit("agent.state", {"state": state, "llmModel": llm_model})

    def call_ended(self, payload: CallEndedPayload) -> Event | None:
        return self.emit("call.ended", _dump(payload))

    def system(self, message: str) -> Event | None:
        return self.emit("system", {"message": message})
