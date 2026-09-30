"""WhisperSTT with a scripted fake WhisperModel (offline)."""

from __future__ import annotations

import asyncio
import math
import os
import threading
import time
import wave
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import numpy as np
import pytest
from livekit import rtc
from livekit.agents import stt

from callgo_agent import stt_local
from callgo_agent.stt_local import WhisperSTT, WhisperSTTError, frames_to_whisper_audio


@dataclass
class FakeSegment:
    start: float
    end: float
    text: str
    avg_logprob: float


@dataclass
class FakeInfo:
    language: str
    language_probability: float = 0.99


class FakeWhisperModel:
    def __init__(self, segments: list[FakeSegment], language: str = "mn") -> None:
        self.segments = segments
        self.language = language
        self.calls: list[tuple[np.ndarray, dict[str, Any]]] = []
        self.threads: list[str] = []

    def transcribe(self, audio: np.ndarray, **kwargs: Any) -> tuple[Any, FakeInfo]:
        self.calls.append((audio, kwargs))
        self.threads.append(threading.current_thread().name)
        lang = kwargs.get("language") or self.language
        return iter(self.segments), FakeInfo(language=lang)


class FactorySpy:
    def __init__(self, model: FakeWhisperModel, delay: float = 0.0) -> None:
        self.model = model
        self.delay = delay
        self.calls: list[tuple[str, dict[str, Any]]] = []

    def __call__(self, name: str, **kwargs: Any) -> FakeWhisperModel:
        self.calls.append((name, kwargs))
        if self.delay:
            time.sleep(self.delay)
        return self.model


@pytest.fixture(autouse=True)
def _clear_cache() -> Any:
    stt_local.clear_model_cache()
    yield
    stt_local.clear_model_cache()


def _frame(samples: np.ndarray, sample_rate: int, channels: int = 1) -> rtc.AudioFrame:
    data = samples.astype(np.int16)
    return rtc.AudioFrame(
        data=data.tobytes(),
        sample_rate=sample_rate,
        num_channels=channels,
        samples_per_channel=data.size // channels,
    )


def _tone(seconds: float, sample_rate: int, amp: int = 16000) -> np.ndarray:
    t = np.arange(int(seconds * sample_rate)) / sample_rate
    return (np.sin(2 * np.pi * 220 * t) * amp).astype(np.int16)


def _chunks(pcm: np.ndarray, sample_rate: int, ms: int = 20) -> list[rtc.AudioFrame]:
    step = sample_rate * ms // 1000
    return [_frame(pcm[i : i + step], sample_rate) for i in range(0, pcm.size, step)]


# ---- audio conversion ------------------------------------------------------------


def test_convert_16k_int16_to_float32() -> None:
    pcm = np.array([0, 16384, -16384, 32767, -32768], dtype=np.int16)
    out = frames_to_whisper_audio(_frame(pcm, 16000))
    assert out.dtype == np.float32
    np.testing.assert_allclose(out, [0.0, 0.5, -0.5, 32767 / 32768, -1.0], atol=1e-6)


def test_convert_merges_frame_list_and_resamples_48k() -> None:
    pcm = _tone(1.0, 48000)
    out = frames_to_whisper_audio(_chunks(pcm, 48000))
    assert out.dtype == np.float32
    assert abs(out.size - 16000) <= 32
    assert float(np.abs(out).max()) <= 1.0
    # energy survives resampling (220 Hz tone, well inside the passband)
    assert 0.3 < float(np.sqrt(np.mean(out**2))) < 0.4


def test_convert_resamples_8k_telephony_up() -> None:
    out = frames_to_whisper_audio(_frame(_tone(0.5, 8000), 8000))
    assert abs(out.size - 8000) <= 32


def test_convert_downmixes_stereo() -> None:
    left = np.full(1600, 8000, dtype=np.int16)
    right = np.full(1600, -8000, dtype=np.int16)
    stereo = np.stack([left, right], axis=1).reshape(-1)
    out = frames_to_whisper_audio(_frame(stereo, 16000, channels=2))
    assert out.size == 1600
    assert float(np.abs(out).max()) < 1e-3


def test_linear_fallback_resampler() -> None:
    pcm = np.linspace(-1, 1, 441, dtype=np.float32)
    out = stt_local._resample_linear(pcm, 44100, 16000)
    assert out.size == 160
    assert out[0] == pytest.approx(-1.0) and out[-1] <= 1.0


def test_empty_buffer_converts_to_empty() -> None:
    assert frames_to_whisper_audio([]).size == 0


def test_whisper_language_mapping() -> None:
    assert stt_local._whisper_language("mn-MN") == "mn"
    assert stt_local._whisper_language("mn_MN") == "mn"
    assert stt_local._whisper_language("auto") is None
    assert stt_local._whisper_language("") is None
    assert stt_local._whisper_language(None) is None


# ---- recognition -----------------------------------------------------------------


async def test_recognize_returns_final_transcript() -> None:
    model = FakeWhisperModel(
        [
            FakeSegment(0.1, 1.2, " Сайн байна уу", math.log(0.9)),
            FakeSegment(1.3, 2.0, " танд юугаар туслах вэ?", math.log(0.7)),
        ]
    )
    factory = FactorySpy(model)
    whisper = WhisperSTT(
        model="small",
        language="mn",
        device="cpu",
        compute_type="int8",
        beam_size=3,
        initial_prompt="CallGo",
        model_factory=factory,
    )
    assert whisper.capabilities.streaming is False
    assert whisper.capabilities.interim_results is False
    assert whisper.model == "small"
    assert whisper.provider == "faster-whisper"

    ev = await whisper.recognize(_chunks(_tone(2.0, 48000), 48000))

    assert ev.type == stt.SpeechEventType.FINAL_TRANSCRIPT
    assert ev.request_id
    alt = ev.alternatives[0]
    assert alt.text == "Сайн байна уу танд юугаар туслах вэ?"
    assert alt.language == "mn"
    assert alt.confidence == pytest.approx(0.8, abs=1e-6)
    assert alt.start_time == pytest.approx(0.1)
    assert alt.end_time == pytest.approx(2.0)

    audio, kwargs = model.calls[0]
    assert audio.dtype == np.float32
    assert abs(audio.size - 32000) <= 64  # 2 s @ 16 kHz after resampling from 48 kHz
    assert kwargs["language"] == "mn"
    assert kwargs["beam_size"] == 3
    assert kwargs["vad_filter"] is False
    assert kwargs["condition_on_previous_text"] is False
    assert kwargs["initial_prompt"] == "CallGo"
    # decoding ran off the event loop thread
    assert model.threads[0] != threading.main_thread().name

    name, fkwargs = factory.calls[0]
    assert name == "small"
    assert fkwargs["device"] == "cpu" and fkwargs["compute_type"] == "int8"


async def test_language_override_and_detection() -> None:
    model = FakeWhisperModel([FakeSegment(0, 1, "hello", -0.1)], language="en")
    whisper = WhisperSTT(model="tiny", language="auto", model_factory=FactorySpy(model))
    ev = await whisper.recognize(_frame(_tone(1.0, 16000), 16000))
    assert model.calls[0][1]["language"] is None  # detection
    assert ev.alternatives[0].language == "en"

    await whisper.recognize(_frame(_tone(1.0, 16000), 16000), language="ru-RU")
    assert model.calls[1][1]["language"] == "ru"


async def test_model_loaded_once_and_shared() -> None:
    model = FakeWhisperModel([FakeSegment(0, 1, "x", -0.2)])
    factory = FactorySpy(model, delay=0.05)
    a = WhisperSTT(model="base", device="cpu", compute_type="int8", model_factory=factory)
    b = WhisperSTT(model="base", device="cpu", compute_type="int8", model_factory=factory)
    frame = _frame(_tone(0.5, 16000), 16000)
    await asyncio.gather(a.recognize(frame), b.recognize(frame), a.recognize(frame))
    assert len(factory.calls) == 1
    assert len(model.calls) == 3

    other = WhisperSTT(model="medium", device="cpu", compute_type="int8", model_factory=factory)
    await other.recognize(frame)
    assert [c[0] for c in factory.calls] == ["base", "medium"]


async def test_prewarm_is_non_blocking() -> None:
    model = FakeWhisperModel([])
    factory = FactorySpy(model, delay=0.3)
    whisper = WhisperSTT(model="tiny", model_factory=factory)
    t0 = time.perf_counter()
    whisper.prewarm()
    assert time.perf_counter() - t0 < 0.1
    await whisper.ensure_model()
    assert len(factory.calls) == 1


async def test_empty_audio_and_no_segments() -> None:
    model = FakeWhisperModel([])
    whisper = WhisperSTT(model="tiny", language="mn", model_factory=FactorySpy(model))
    ev = await whisper.recognize([])
    assert ev.alternatives[0].text == ""
    assert model.calls == []  # nothing to decode

    ev = await whisper.recognize(_frame(np.zeros(1600, dtype=np.int16), 16000))
    assert ev.alternatives[0].text == ""
    assert ev.alternatives[0].confidence == 0.0


async def test_keyterms_become_hotwords() -> None:
    model = FakeWhisperModel([FakeSegment(0, 1, "x", -0.2)])
    whisper = WhisperSTT(model="tiny", hotwords="CallGo", model_factory=FactorySpy(model))
    whisper._update_session_keyterms(["Хаан банк", " ", "Юнител"])
    await whisper.recognize(_frame(_tone(0.2, 16000), 16000))
    assert model.calls[0][1]["hotwords"] == "CallGo Хаан банк Юнител"


async def test_model_load_failure_raises_api_error() -> None:
    def broken(name: str, **kwargs: Any) -> Any:
        raise RuntimeError("model.bin missing")

    whisper = WhisperSTT(model="nope", model_factory=broken)
    errors: list[Any] = []
    whisper.on("error", errors.append)
    with pytest.raises(WhisperSTTError, match="model.bin missing"):
        await whisper.recognize(_frame(_tone(0.2, 16000), 16000))
    assert len(errors) == 1 and errors[0].recoverable is False  # no retries


def test_for_profile_uses_profile_fields() -> None:
    from uuid import uuid4

    from callgo_agent.schemas import AgentProfile

    profile = AgentProfile(id=uuid4(), org_id=uuid4(), name="p", language="mn", stt_model="medium")
    whisper = WhisperSTT.for_profile(profile, model_factory=FactorySpy(FakeWhisperModel([])))
    assert whisper.model == "medium"
    assert whisper.options.language == "mn"


async def test_stream_adapter_segments_with_vad() -> None:
    """streaming() wraps in StreamAdapter; a scripted VAD drives recognize()."""
    from livekit.agents import vad as lk_vad

    class ScriptedVADStream(lk_vad.VADStream):
        async def _main_task(self) -> None:
            frames: list[rtc.AudioFrame] = []
            async for item in self._input_ch:
                if isinstance(item, rtc.AudioFrame):
                    frames.append(item)
            self._event_ch.send_nowait(
                lk_vad.VADEvent(
                    type=lk_vad.VADEventType.START_OF_SPEECH,
                    samples_index=0,
                    timestamp=0.0,
                    speech_duration=0.0,
                    silence_duration=0.0,
                )
            )
            self._event_ch.send_nowait(
                lk_vad.VADEvent(
                    type=lk_vad.VADEventType.END_OF_SPEECH,
                    samples_index=0,
                    timestamp=0.0,
                    speech_duration=1.0,
                    silence_duration=0.0,
                    frames=frames,
                )
            )

    class ScriptedVAD(lk_vad.VAD):
        def __init__(self) -> None:
            super().__init__(capabilities=lk_vad.VADCapabilities(update_interval=0.032))

        def stream(self) -> lk_vad.VADStream:
            return ScriptedVADStream(self)

    model = FakeWhisperModel([FakeSegment(0, 1, " Баярлалаа", -0.1)])
    whisper = WhisperSTT(model="tiny", language="mn", model_factory=FactorySpy(model))
    adapter = whisper.streaming(vad=ScriptedVAD())
    assert isinstance(adapter, stt.StreamAdapter)
    assert adapter.capabilities.streaming is True

    stream = adapter.stream()
    for f in _chunks(_tone(1.0, 16000), 16000):
        stream.push_frame(f)
    stream.end_input()
    events = [ev async for ev in stream]
    types = [ev.type for ev in events]
    assert stt.SpeechEventType.FINAL_TRANSCRIPT in types
    final = next(ev for ev in events if ev.type == stt.SpeechEventType.FINAL_TRANSCRIPT)
    assert final.alternatives[0].text == "Баярлалаа"
    assert abs(model.calls[0][0].size - 16000) <= 32
    await stream.aclose()
    await adapter.aclose()


# ---- optional: real faster-whisper model --------------------------------------


@pytest.mark.skipif(
    not os.environ.get("CALLGO_TEST_WHISPER_MODEL"),
    reason="set CALLGO_TEST_WHISPER_MODEL=<size|path> to run against a real model",
)
async def test_real_faster_whisper_model() -> None:
    fixture = Path(__file__).parent / "fixtures" / "speechlike.wav"
    if fixture.is_file():
        with wave.open(str(fixture), "rb") as w:
            rate = w.getframerate()
            pcm = np.frombuffer(w.readframes(w.getnframes()), dtype=np.int16)
            if w.getnchannels() > 1:
                pcm = pcm[:: w.getnchannels()]
    else:
        rate, pcm = 16000, _tone(1.0, 16000)
    whisper = WhisperSTT(
        model=os.environ["CALLGO_TEST_WHISPER_MODEL"],
        language="mn",
        device="cpu",
        compute_type="int8",
        beam_size=1,
    )
    ev = await whisper.recognize(_frame(pcm, rate))
    assert ev.type == stt.SpeechEventType.FINAL_TRANSCRIPT
    assert ev.alternatives and 0.0 <= ev.alternatives[0].confidence <= 1.0
