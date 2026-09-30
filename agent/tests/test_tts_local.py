"""PiperTTS with a fake PiperVoice (offline)."""

from __future__ import annotations

import json
import os
import threading
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import numpy as np
import pytest
from livekit.agents import APIError, tts

from callgo_agent import tts_local
from callgo_agent.tts_local import PiperTTS, PiperVoiceNotFoundError, resolve_voice

SR = 22050


@dataclass
class FakeChunk:
    sample_rate: int
    samples: np.ndarray

    @property
    def audio_int16_bytes(self) -> bytes:
        return self.samples.astype(np.int16).tobytes()


@dataclass
class FakeConfig:
    sample_rate: int = SR
    espeak_voice: str = "kk"


class FakeVoice:
    """Yields one int16 chunk per '.'-separated sentence (length ∝ characters)."""

    def __init__(self, sample_rate: int = SR, delay: float = 0.0, fail: bool = False) -> None:
        self.config = FakeConfig(sample_rate=sample_rate)
        self.delay = delay
        self.fail = fail
        self.calls: list[tuple[str, Any]] = []
        self.threads: list[str] = []

    @staticmethod
    def samples_for(sentence: str) -> int:
        return 100 * len(sentence)

    def synthesize(self, text: str, syn_config: Any = None) -> Any:
        self.calls.append((text, syn_config))
        for sentence in [s.strip() for s in text.split(".") if s.strip()]:
            self.threads.append(threading.current_thread().name)
            if self.delay:
                time.sleep(self.delay)
            if self.fail:
                raise RuntimeError("onnx exploded")
            n = self.samples_for(sentence)
            yield FakeChunk(self.config.sample_rate, (np.ones(n) * 1000).astype(np.int16))


class LoaderSpy:
    def __init__(self, voice: FakeVoice) -> None:
        self.voice = voice
        self.calls: list[tuple[Path, Path, bool]] = []

    def __call__(self, model: Path, config: Path, use_cuda: bool) -> FakeVoice:
        self.calls.append((model, config, use_cuda))
        return self.voice


def _write_voice(d: Path, name: str, sample_rate: int = SR) -> None:
    d.mkdir(parents=True, exist_ok=True)
    (d / f"{name}.onnx").write_bytes(b"fake-onnx")
    (d / f"{name}.onnx.json").write_text(
        json.dumps({"audio": {"sample_rate": sample_rate}, "espeak": {"voice": "kk"}})
    )


@pytest.fixture(autouse=True)
def _clear_cache() -> Any:
    tts_local.clear_voice_cache()
    yield
    tts_local.clear_voice_cache()


@pytest.fixture
def voices(tmp_path: Path) -> Path:
    d = tmp_path / "piper"
    _write_voice(d, "kk_KZ-issai-high")
    _write_voice(d, "mn_MN-test-medium", sample_rate=16000)
    return d


# ---- voice resolution -------------------------------------------------------------


def test_resolve_requested_voice(voices: Path) -> None:
    v = resolve_voice("mn_MN-test-medium", voices_dir=voices, default_voice="kk_KZ-issai-high")
    assert v.name == "mn_MN-test-medium"
    assert v.model_path == voices / "mn_MN-test-medium.onnx"
    assert v.config_path == voices / "mn_MN-test-medium.onnx.json"


def test_resolve_missing_voice_falls_back_to_default(voices: Path) -> None:
    v = resolve_voice("nope", voices_dir=voices, default_voice="kk_KZ-issai-high")
    assert v.name == "kk_KZ-issai-high"


def test_resolve_missing_default_uses_any_available(voices: Path) -> None:
    v = resolve_voice("", voices_dir=voices, default_voice="mn_MN-default-medium")
    assert v.name == "kk_KZ-issai-high"  # first in sorted order


def test_resolve_absolute_path(voices: Path) -> None:
    path = str(voices / "mn_MN-test-medium.onnx")
    assert resolve_voice(path, voices_dir="/nonexistent", default_voice="x").name == (
        "mn_MN-test-medium"
    )


def test_resolve_requires_config_json(tmp_path: Path) -> None:
    (tmp_path / "lonely.onnx").write_bytes(b"x")
    with pytest.raises(PiperVoiceNotFoundError, match="download_models.py"):
        resolve_voice("lonely", voices_dir=tmp_path, default_voice="lonely")


def test_no_voice_at_all_raises_at_construction(tmp_path: Path) -> None:
    with pytest.raises(PiperVoiceNotFoundError, match="no Piper voice found"):
        PiperTTS(voice="mn", voices_dir=tmp_path / "empty", default_voice="mn_MN-default-medium")


def test_sample_rate_from_voice_config(voices: Path) -> None:
    t = PiperTTS(voice="mn_MN-test-medium", voices_dir=voices, voice_loader=LoaderSpy(FakeVoice()))
    assert t.sample_rate == 16000
    assert t.num_channels == 1
    assert t.capabilities.streaming is False
    assert t.model == "mn_MN-test-medium"
    assert t.provider == "piper"


# ---- synthesis -----------------------------------------------------------------------


async def test_synthesize_emits_pcm_frames(voices: Path) -> None:
    voice = FakeVoice()
    loader = LoaderSpy(voice)
    t = PiperTTS(
        voice="kk_KZ-issai-high",
        voices_dir=voices,
        voice_loader=loader,
        sentence_silence=0.1,
        length_scale=1.2,
        noise_scale=0.5,
        chunk_ms=40,
    )
    text = "Сайн байна уу. Танд юугаар туслах вэ."
    frames = [ev.frame async for ev in t.synthesize(text)]

    assert frames
    assert all(f.sample_rate == SR and f.num_channels == 1 for f in frames)
    total = sum(f.samples_per_channel for f in frames)
    sentences = [s.strip() for s in text.split(".") if s.strip()]
    expected = sum(FakeVoice.samples_for(s) for s in sentences) + int(SR * 0.1)
    assert total == expected
    # progressive framing: first frame is ≤ 20 ms, none larger than chunk_ms
    assert frames[0].samples_per_channel <= SR * 20 // 1000
    assert max(f.samples_per_channel for f in frames) <= SR * 40 // 1000 + SR // 100
    # silence is between the sentences, not before the first one
    first = np.frombuffer(frames[0].data, dtype=np.int16)
    assert first[0] == 1000

    syn_text, syn_config = voice.calls[0]
    assert syn_text == text
    assert syn_config.length_scale == 1.2
    assert syn_config.noise_scale == 0.5
    assert voice.threads[0] != threading.main_thread().name
    assert loader.calls == [
        (voices / "kk_KZ-issai-high.onnx", voices / "kk_KZ-issai-high.onnx.json", False)
    ]


async def test_collect_returns_single_frame(voices: Path) -> None:
    t = PiperTTS(voice="kk_KZ-issai-high", voices_dir=voices, voice_loader=LoaderSpy(FakeVoice()))
    frame = await t.synthesize("Нэг.").collect()
    assert frame.samples_per_channel == FakeVoice.samples_for("Нэг")


async def test_first_audio_before_full_synthesis(voices: Path) -> None:
    voice = FakeVoice(delay=0.2)
    t = PiperTTS(voice="kk_KZ-issai-high", voices_dir=voices, voice_loader=LoaderSpy(voice))
    await t.ensure_voice()
    t0 = time.perf_counter()
    first_at: float | None = None
    async for _ in t.synthesize("Нэгдүгээр өгүүлбэр. Хоёрдугаар өгүүлбэр. Гурав дахь."):
        if first_at is None:
            first_at = time.perf_counter() - t0
    total = time.perf_counter() - t0
    assert first_at is not None
    assert first_at < 0.4  # after the 1st sentence, not after all three (≥0.6 s)
    assert total >= 0.6


async def test_voice_loaded_once_across_instances(voices: Path) -> None:
    loader = LoaderSpy(FakeVoice())
    a = PiperTTS(voice="kk_KZ-issai-high", voices_dir=voices, voice_loader=loader)
    b = PiperTTS(voice="kk_KZ-issai-high", voices_dir=voices, voice_loader=loader)
    await a.synthesize("Нэг.").collect()
    await b.synthesize("Хоёр.").collect()
    assert len(loader.calls) == 1


async def test_espeak_voice_override(voices: Path) -> None:
    voice = FakeVoice()
    t = PiperTTS(
        voice="kk_KZ-issai-high",
        voices_dir=voices,
        voice_loader=LoaderSpy(voice),
        espeak_voice="mn",
    )
    await t.synthesize("Нэг.").collect()
    assert voice.config.espeak_voice == "mn"


async def test_unpronounceable_text_emits_short_silence(voices: Path) -> None:
    t = PiperTTS(voice="kk_KZ-issai-high", voices_dir=voices, voice_loader=LoaderSpy(FakeVoice()))
    frames = [ev.frame async for ev in t.synthesize("...")]
    total = sum(f.samples_per_channel for f in frames)
    assert total == SR // 20


async def test_empty_text_yields_no_audio(voices: Path) -> None:
    t = PiperTTS(voice="kk_KZ-issai-high", voices_dir=voices, voice_loader=LoaderSpy(FakeVoice()))
    frames = [ev.frame async for ev in t.synthesize("   ")]
    assert sum(f.samples_per_channel for f in frames) == 0


async def test_synthesis_failure_raises_api_error(voices: Path) -> None:
    t = PiperTTS(
        voice="kk_KZ-issai-high", voices_dir=voices, voice_loader=LoaderSpy(FakeVoice(fail=True))
    )
    errors: list[Any] = []
    t.on("error", errors.append)
    with pytest.raises(APIError, match="onnx exploded"):
        async for _ in t.synthesize("Нэг."):
            pass
    assert len(errors) == 1 and errors[0].recoverable is False


async def test_works_through_stream_adapter(voices: Path) -> None:
    """The AgentSession path: tts.StreamAdapter + sentence tokenizer over PiperTTS."""
    from livekit.agents.tokenize import basic

    voice = FakeVoice()
    t = PiperTTS(voice="kk_KZ-issai-high", voices_dir=voices, voice_loader=LoaderSpy(voice))
    adapter = tts.StreamAdapter(tts=t, sentence_tokenizer=basic.SentenceTokenizer())
    stream = adapter.stream()
    stream.push_text("Сайн байна уу. ")
    stream.push_text("Танд юугаар туслах вэ.")
    stream.end_input()
    frames = [ev.frame async for ev in stream]
    await stream.aclose()
    assert sum(f.samples_per_channel for f in frames) > 0
    assert all(f.sample_rate == SR for f in frames)


# ---- optional: real Piper voice --------------------------------------------------------


@pytest.mark.skipif(
    not os.environ.get("CALLGO_TEST_PIPER_VOICES_DIR"),
    reason="set CALLGO_TEST_PIPER_VOICES_DIR=<dir with *.onnx + *.onnx.json> to run",
)
async def test_real_piper_voice() -> None:
    d = Path(os.environ["CALLGO_TEST_PIPER_VOICES_DIR"])
    t = PiperTTS(voice=os.environ.get("CALLGO_TEST_PIPER_VOICE", ""), voices_dir=d)
    frames = [ev.frame async for ev in t.synthesize("Сайн байна уу. Би танд туслах болно.")]
    assert frames and all(f.sample_rate == t.sample_rate for f in frames)
    seconds = sum(f.samples_per_channel for f in frames) / t.sample_rate
    assert 0.8 < seconds < 15
