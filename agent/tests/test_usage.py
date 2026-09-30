from __future__ import annotations

from collections import defaultdict
from collections.abc import Callable
from typing import Any

import pytest
from livekit.agents import MetricsCollectedEvent
from livekit.agents.metrics import LLMMetrics, STTMetrics, TTSMetrics, VADMetrics

from callgo_agent.usage import (
    DEFAULT_LLM_PER_1K_MNT,
    DEFAULT_STT_PER_MIN_MNT,
    DEFAULT_TTS_PER_1K_CHARS_MNT,
    ENV_LLM_RATE,
    ENV_STT_RATE,
    ENV_TTS_RATE,
    CostRates,
    UsageTracker,
)


def llm_metrics(prompt: int, completion: int, request_id: str = "r1") -> LLMMetrics:
    return LLMMetrics(
        label="llm",
        request_id=request_id,
        timestamp=0,
        duration=0.5,
        ttft=0.1,
        cancelled=False,
        completion_tokens=completion,
        prompt_tokens=prompt,
        prompt_cached_tokens=0,
        total_tokens=prompt + completion,
        tokens_per_second=10,
    )


def stt_metrics(seconds: float) -> STTMetrics:
    return STTMetrics(
        label="stt",
        request_id="",
        timestamp=0,
        duration=0.2,
        audio_duration=seconds,
        streamed=False,
    )


def tts_metrics(chars: int) -> TTSMetrics:
    return TTSMetrics(
        label="tts",
        request_id="t",
        timestamp=0,
        ttfb=0.1,
        duration=0.3,
        audio_duration=1.0,
        cancelled=False,
        characters_count=chars,
        streamed=False,
    )


class FakeEmitter:
    def __init__(self) -> None:
        self.handlers: dict[str, list[Callable[[Any], None]]] = defaultdict(list)

    def on(self, name: str, cb: Callable[[Any], None]) -> None:
        self.handlers[name].append(cb)

    def off(self, name: str, cb: Callable[[Any], None]) -> None:
        self.handlers[name].remove(cb)

    def emit(self, name: str, ev: Any) -> None:
        for cb in list(self.handlers[name]):
            cb(ev)


def test_rates_defaults_and_env() -> None:
    assert CostRates.from_env({}) == CostRates(
        DEFAULT_LLM_PER_1K_MNT, DEFAULT_STT_PER_MIN_MNT, DEFAULT_TTS_PER_1K_CHARS_MNT
    )
    assert CostRates.from_env({}) == CostRates(5, 15, 2)
    rates = CostRates.from_env({ENV_LLM_RATE: "7.5", ENV_STT_RATE: " 20 ", ENV_TTS_RATE: "0"})
    assert rates == CostRates(7.5, 20, 0)


@pytest.mark.parametrize("bad", ["abc", "-1", "nan", "inf"])
def test_rates_invalid_env_falls_back(bad: str, caplog: pytest.LogCaptureFixture) -> None:
    rates = CostRates.from_env({ENV_LLM_RATE: bad})
    assert rates.llm_per_1k_tokens == DEFAULT_LLM_PER_1K_MNT
    assert ENV_LLM_RATE in caplog.text


def test_rates_read_process_environment(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv(ENV_TTS_RATE, "3")
    assert UsageTracker().rates.tts_per_1k_chars == 3


def test_cost_rounding() -> None:
    rates = CostRates(5, 15, 2)
    # 1500 tokens -> 7.5, 90 s -> 22.5, 2500 chars -> 5.0 : 35.0
    assert rates.cost_mnt(tokens=1500, stt_seconds=90, tts_chars=2500) == 35
    assert rates.cost_mnt(tokens=100, stt_seconds=0, tts_chars=0) == 1  # 0.5 rounds up
    assert rates.cost_mnt(tokens=99, stt_seconds=0, tts_chars=0) == 0
    assert rates.cost_mnt(tokens=0, stt_seconds=0, tts_chars=0) == 0


def test_tracker_accumulates_session_metrics() -> None:
    session = FakeEmitter()
    tracker = UsageTracker("google/gemini-2.5-flash", rates=CostRates(5, 15, 2))
    tracker.attach(session)
    for m in (
        llm_metrics(1200, 150, "a"),
        llm_metrics(800, 50, "b"),
        llm_metrics(800, 50, "b"),  # same request re-emitted: counted once
        stt_metrics(45.0),
        stt_metrics(15.5),
        tts_metrics(1800),
        tts_metrics(700),
        VADMetrics(
            label="vad", timestamp=0, idle_time=0, inference_duration_total=0, inference_count=1
        ),
    ):
        session.emit("metrics_collected", MetricsCollectedEvent(metrics=m))

    usage = tracker.snapshot()
    assert (usage.llm_tokens_in, usage.llm_tokens_out) == (2000, 200)
    assert usage.stt_seconds == 60.5
    assert usage.tts_chars == 2500
    assert usage.llm_model == "google/gemini-2.5-flash"
    # 2200 tokens -> 11, 60.5 s -> 15.125, 2500 chars -> 5 : 31.125
    assert usage.cost_mnt == 31
    assert usage.model_dump(mode="json") == {
        "llmTokensIn": 2000,
        "llmTokensOut": 200,
        "sttSeconds": 60.5,
        "ttsChars": 2500,
        "llmModel": "google/gemini-2.5-flash",
        "costMnt": 31,
    }

    tracker.detach(session)
    tracker.detach(session)  # idempotent
    session.emit("metrics_collected", MetricsCollectedEvent(metrics=tts_metrics(1000)))
    assert tracker.tts_chars == 2500


def test_llm_metrics_without_request_id_are_all_counted() -> None:
    tracker = UsageTracker(rates=CostRates())
    tracker.add(llm_metrics(10, 1, ""))
    tracker.add(llm_metrics(10, 1, ""))
    assert tracker.llm_tokens_in == 20


def test_watch_counts_direct_llm_metrics_and_unsubscribes() -> None:
    model = FakeEmitter()
    tracker = UsageTracker(rates=CostRates())
    with tracker.watch(model):
        model.emit("metrics_collected", llm_metrics(300, 30, "analysis"))
    assert model.handlers["metrics_collected"] == []
    model.emit("metrics_collected", llm_metrics(300, 30, "later"))
    assert (tracker.llm_tokens_in, tracker.llm_tokens_out) == (300, 30)

    with tracker.watch(None):  # no model: no-op
        pass
    with tracker.watch(object()):  # type: ignore[arg-type]
        pass


@pytest.mark.filterwarnings("ignore::UserWarning")  # repr of the corrupted model
def test_tracker_never_raises_on_bad_metrics(caplog: pytest.LogCaptureFixture) -> None:
    tracker = UsageTracker(rates=CostRates())
    broken = llm_metrics(1, 1, "x")
    object.__setattr__(broken, "prompt_tokens", "many")
    tracker.add(broken)
    tracker.add("not metrics")
    assert tracker.snapshot().llm_tokens_in == 0
    assert "could not account metrics" in caplog.text
