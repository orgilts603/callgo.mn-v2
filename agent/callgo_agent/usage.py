"""Per-call usage metering (LLM tokens, STT audio seconds, TTS characters) and its cost.

:class:`UsageTracker` listens to ``AgentSession`` ``metrics_collected`` events
(:class:`~livekit.agents.MetricsCollectedEvent`, still emitted by livekit-agents 1.8.3
next to the newer ``session_usage_updated``) and to the LLM used for the post-call
analysis, then reports a :class:`~callgo_agent.schemas.CallUsage` in ``call.ended``.

Cost rates (MNT) come from the environment so operators can tune them without a release:

* ``CALLGO_COST_LLM_PER_1K_MNT``        per 1000 LLM tokens (prompt + completion), default 5
* ``CALLGO_COST_STT_PER_MIN_MNT``       per minute of transcribed audio, default 15
* ``CALLGO_COST_TTS_PER_1K_CHARS_MNT``  per 1000 synthesized characters, default 2
"""

from __future__ import annotations

import contextlib
import logging
import math
import os
from collections.abc import Iterator, Mapping
from contextlib import contextmanager
from dataclasses import dataclass
from typing import Any

from livekit.agents import MetricsCollectedEvent
from livekit.agents.metrics import LLMMetrics, STTMetrics, TTSMetrics

from .schemas import CallUsage

log = logging.getLogger("callgo.usage")

ENV_LLM_RATE = "CALLGO_COST_LLM_PER_1K_MNT"
ENV_STT_RATE = "CALLGO_COST_STT_PER_MIN_MNT"
ENV_TTS_RATE = "CALLGO_COST_TTS_PER_1K_CHARS_MNT"
DEFAULT_LLM_PER_1K_MNT = 5.0
DEFAULT_STT_PER_MIN_MNT = 15.0
DEFAULT_TTS_PER_1K_CHARS_MNT = 2.0


def _rate(env: Mapping[str, str], name: str, default: float) -> float:
    raw = env.get(name, "").strip()
    if not raw:
        return default
    try:
        value = float(raw)
    except ValueError:
        log.warning("%s=%r is not a number; using %s", name, raw, default)
        return default
    if value < 0 or not math.isfinite(value):
        log.warning("%s=%r must be a finite number >= 0; using %s", name, raw, default)
        return default
    return value


@dataclass(frozen=True, slots=True)
class CostRates:
    """Tariff used to price one call, in MNT."""

    llm_per_1k_tokens: float = DEFAULT_LLM_PER_1K_MNT
    stt_per_minute: float = DEFAULT_STT_PER_MIN_MNT
    tts_per_1k_chars: float = DEFAULT_TTS_PER_1K_CHARS_MNT

    @classmethod
    def from_env(cls, env: Mapping[str, str] | None = None) -> CostRates:
        e = os.environ if env is None else env
        return cls(
            llm_per_1k_tokens=_rate(e, ENV_LLM_RATE, DEFAULT_LLM_PER_1K_MNT),
            stt_per_minute=_rate(e, ENV_STT_RATE, DEFAULT_STT_PER_MIN_MNT),
            tts_per_1k_chars=_rate(e, ENV_TTS_RATE, DEFAULT_TTS_PER_1K_CHARS_MNT),
        )

    def cost_mnt(self, *, tokens: int, stt_seconds: float, tts_chars: int) -> int:
        """Total cost rounded half-up to whole MNT."""
        total = (
            tokens / 1000.0 * self.llm_per_1k_tokens
            + stt_seconds / 60.0 * self.stt_per_minute
            + tts_chars / 1000.0 * self.tts_per_1k_chars
        )
        return max(0, math.floor(total + 0.5))


class UsageTracker:
    """Accumulates one call's usage from framework metrics. Never raises into the call."""

    def __init__(self, llm_model: str = "", rates: CostRates | None = None) -> None:
        self.llm_model = llm_model
        self.rates = rates or CostRates.from_env()
        self.llm_tokens_in = 0
        self.llm_tokens_out = 0
        self.stt_seconds = 0.0
        self.tts_chars = 0
        # The session re-emits the LLM's own metrics: count each LLM request once.
        self._llm_requests: set[str] = set()

    # -- wiring --

    def attach(self, session: Any) -> None:
        """Subscribe to ``session.on("metrics_collected")`` (an ``AgentSession``)."""
        session.on("metrics_collected", self.on_metrics_collected)

    def detach(self, session: Any) -> None:
        with contextlib.suppress(Exception):
            session.off("metrics_collected", self.on_metrics_collected)

    @contextmanager
    def watch(self, source: Any) -> Iterator[None]:
        """Count metrics emitted directly by ``source`` (e.g. the post-call analysis LLM)."""
        if source is None or not hasattr(source, "on"):
            yield
            return
        source.on("metrics_collected", self.add)
        try:
            yield
        finally:
            with contextlib.suppress(Exception):
                source.off("metrics_collected", self.add)

    # -- accumulation --

    def on_metrics_collected(self, ev: MetricsCollectedEvent) -> None:
        self.add(ev.metrics)

    def add(self, metrics: Any) -> None:
        try:
            if isinstance(metrics, LLMMetrics):
                if metrics.request_id:
                    if metrics.request_id in self._llm_requests:
                        return
                    self._llm_requests.add(metrics.request_id)
                self.llm_tokens_in += max(0, int(metrics.prompt_tokens))
                self.llm_tokens_out += max(0, int(metrics.completion_tokens))
            elif isinstance(metrics, STTMetrics):
                self.stt_seconds += max(0.0, float(metrics.audio_duration))
            elif isinstance(metrics, TTSMetrics):
                self.tts_chars += max(0, int(metrics.characters_count))
        except Exception:  # metering must not break the call
            log.exception("could not account metrics %r", metrics)

    # -- reporting --

    @property
    def cost_mnt(self) -> int:
        return self.rates.cost_mnt(
            tokens=self.llm_tokens_in + self.llm_tokens_out,
            stt_seconds=self.stt_seconds,
            tts_chars=self.tts_chars,
        )

    def snapshot(self) -> CallUsage:
        return CallUsage(
            llm_tokens_in=self.llm_tokens_in,
            llm_tokens_out=self.llm_tokens_out,
            stt_seconds=round(self.stt_seconds, 2),
            tts_chars=self.tts_chars,
            llm_model=self.llm_model,
            cost_mnt=self.cost_mnt,
        )
