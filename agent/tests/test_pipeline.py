"""STT/TTS factory selection and validation (offline)."""

from __future__ import annotations

import json
from pathlib import Path
from types import SimpleNamespace
from typing import Any
from uuid import uuid4

import pytest
from livekit.agents import stt, tts

from callgo_agent import pipeline
from callgo_agent.config import settings
from callgo_agent.schemas import AgentProfile
from callgo_agent.stt_local import WhisperSTT
from callgo_agent.tts_local import PiperTTS, PiperVoiceNotFoundError


def _profile(**kw: Any) -> AgentProfile:
    return AgentProfile(id=uuid4(), org_id=uuid4(), name="p", **kw)


class _Recorder:
    def __init__(self, kind: str) -> None:
        self.kind = kind
        self.calls: list[dict[str, Any]] = []

    def __call__(self, **kwargs: Any) -> SimpleNamespace:
        self.calls.append(kwargs)
        return SimpleNamespace(kind=self.kind, kwargs=kwargs)


@pytest.fixture
def fake_plugins(monkeypatch: pytest.MonkeyPatch) -> dict[str, SimpleNamespace]:
    mods = {
        name: SimpleNamespace(STT=_Recorder(f"{name}.STT"), TTS=_Recorder(f"{name}.TTS"))
        for name in ("openai", "google", "groq")
    }
    loaded: list[str] = []

    def load(name: str) -> SimpleNamespace:
        loaded.append(name)
        return mods[name]

    monkeypatch.setattr(pipeline, "_load_plugin", load)
    return mods


@pytest.fixture
def voices_dir(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    d = tmp_path / "piper"
    d.mkdir()
    for name, rate in (("kk_KZ-issai-high", 22050), ("mn_MN-custom-medium", 16000)):
        (d / f"{name}.onnx").write_bytes(b"x")
        (d / f"{name}.onnx.json").write_text(json.dumps({"audio": {"sample_rate": rate}}))
    monkeypatch.setattr(settings, "piper_voices_dir", str(d))
    monkeypatch.setattr(settings, "piper_default_voice", "kk_KZ-issai-high")
    return d


# ---- STT -------------------------------------------------------------------------


def test_default_stt_is_local_whisper() -> None:
    s = pipeline.build_stt(_profile())
    assert isinstance(s, WhisperSTT)
    assert s.model == "large-v3"
    assert s.options.language == "mn"
    assert s.capabilities.streaming is False


@pytest.mark.parametrize("alias", ["faster_whisper", "faster-whisper", "Whisper", "local", ""])
def test_whisper_aliases(alias: str) -> None:
    s = pipeline.build_stt(_profile(stt_provider=alias, stt_model="small", language="en"))
    assert isinstance(s, WhisperSTT)
    assert s.model == "small"
    assert s.options.language == "en"


def test_whisper_empty_model_uses_settings(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(settings, "whisper_model", "medium")
    s = pipeline.build_stt(_profile(stt_model=""))
    assert isinstance(s, WhisperSTT) and s.model == "medium"


def test_openai_stt(fake_plugins: dict[str, SimpleNamespace]) -> None:
    # the profile default stt_model ("large-v3") is a local id → mapped to whisper-1
    s: Any = pipeline.build_stt(_profile(stt_provider="openai", language="mn-MN"))
    assert s.kind == "openai.STT"
    assert s.kwargs == {"model": "whisper-1", "language": "mn"}

    s = pipeline.build_stt(_profile(stt_provider="openai", stt_model="gpt-4o-transcribe"))
    assert s.kwargs["model"] == "gpt-4o-transcribe"


def test_google_stt(fake_plugins: dict[str, SimpleNamespace]) -> None:
    s: Any = pipeline.build_stt(_profile(stt_provider="google", language="mn"))
    assert s.kind == "google.STT"
    assert s.kwargs == {"languages": ["mn-MN"], "detect_language": False}

    s = pipeline.build_stt(_profile(stt_provider="google", stt_model="chirp_2", language="en-GB"))
    assert s.kwargs == {"languages": ["en-GB"], "detect_language": False, "model": "chirp_2"}


def test_groq_stt(fake_plugins: dict[str, SimpleNamespace]) -> None:
    s: Any = pipeline.build_stt(_profile(stt_provider="groq"))
    assert s.kind == "groq.STT"
    assert s.kwargs == {"model": "whisper-large-v3-turbo", "language": "mn"}

    s = pipeline.build_stt(_profile(stt_provider="groq", stt_model="whisper-large-v3"))
    assert s.kwargs["model"] == "whisper-large-v3"


def test_unknown_stt_provider() -> None:
    with pytest.raises(ValueError, match="unknown stt_provider 'deepgram'"):
        pipeline.build_stt(_profile(stt_provider="deepgram"))


def test_real_openai_and_groq_plugins_construct(monkeypatch: pytest.MonkeyPatch) -> None:
    """The real (installed) plugins accept the kwargs we pass. No network at construction."""
    monkeypatch.setenv("OPENAI_API_KEY", "sk-test")
    monkeypatch.setenv("GROQ_API_KEY", "gsk-test")
    s = pipeline.build_stt(_profile(stt_provider="openai"))
    assert isinstance(s, stt.STT) and s.model == "whisper-1"
    assert s.capabilities.streaming is False
    g = pipeline.build_stt(_profile(stt_provider="groq"))
    assert isinstance(g, stt.STT) and g.model == "whisper-large-v3-turbo"
    t = pipeline.build_tts(_profile(tts_provider="openai", tts_voice="nova"))
    assert isinstance(t, tts.TTS)


def test_missing_plugin_is_reported(monkeypatch: pytest.MonkeyPatch) -> None:
    import importlib

    def boom(name: str) -> Any:
        raise ImportError(name)

    monkeypatch.setattr(importlib, "import_module", boom)
    with pytest.raises(pipeline.PluginUnavailableError, match="livekit-plugins-google"):
        pipeline.build_stt(_profile(stt_provider="google"))


# ---- TTS -------------------------------------------------------------------------


def test_default_tts_is_piper(voices_dir: Path) -> None:
    t = pipeline.build_tts(_profile())
    assert isinstance(t, PiperTTS)
    assert t.model == "kk_KZ-issai-high"  # empty tts_voice → default voice
    assert t.sample_rate == 22050


def test_piper_profile_voice(voices_dir: Path) -> None:
    t = pipeline.build_tts(_profile(tts_provider="piper", tts_voice="mn_MN-custom-medium"))
    assert isinstance(t, PiperTTS)
    assert t.model == "mn_MN-custom-medium"
    assert t.sample_rate == 16000


def test_piper_missing_voice_falls_back(voices_dir: Path) -> None:
    t = pipeline.build_tts(_profile(tts_voice="does-not-exist"))
    assert isinstance(t, PiperTTS) and t.model == "kk_KZ-issai-high"


def test_piper_without_any_voice_errors(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(settings, "piper_voices_dir", str(tmp_path / "none"))
    with pytest.raises(PiperVoiceNotFoundError):
        pipeline.build_tts(_profile())


def test_openai_tts(fake_plugins: dict[str, SimpleNamespace]) -> None:
    t: Any = pipeline.build_tts(_profile(tts_provider="openai"))
    assert t.kind == "openai.TTS" and t.kwargs == {"voice": "alloy"}
    t = pipeline.build_tts(_profile(tts_provider="openai", tts_voice="nova"))
    assert t.kwargs == {"voice": "nova"}


def test_google_tts(fake_plugins: dict[str, SimpleNamespace]) -> None:
    t: Any = pipeline.build_tts(_profile(tts_provider="google", language="mn"))
    assert t.kind == "google.TTS" and t.kwargs == {"language": "mn-MN"}
    t = pipeline.build_tts(
        _profile(tts_provider="google", language="en", tts_voice="en-US-Chirp3-HD-Aoede")
    )
    assert t.kwargs == {"language": "en-US", "voice_name": "en-US-Chirp3-HD-Aoede"}


def test_unknown_tts_provider() -> None:
    with pytest.raises(ValueError, match="unknown tts_provider 'elevenlabs'"):
        pipeline.build_tts(_profile(tts_provider="elevenlabs"))


def test_google_locale() -> None:
    assert pipeline.google_locale("mn") == "mn-MN"
    assert pipeline.google_locale("mn_MN") == "mn-MN"
    assert pipeline.google_locale("xx") == "xx"


# ---- scripts/download_models.py (pure helpers, offline) ---------------------------


def _download_script() -> Any:
    import importlib.util

    path = Path(__file__).resolve().parent.parent / "scripts" / "download_models.py"
    spec = importlib.util.spec_from_file_location("download_models", path)
    assert spec and spec.loader
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def test_download_pick_voice_prefers_mongolian_then_fallback() -> None:
    dm = _download_script()
    catalog = {"kk_KZ-issai-high": {}, "ru_RU-irina-medium": {}, "en_US-lessac-medium": {}}
    assert dm.pick_voice(catalog) == ("kk_KZ-issai-high", False)
    catalog |= {"mn_MN-a-high": {}, "mn_MN-b-medium": {}}
    assert dm.pick_voice(catalog) == ("mn_MN-b-medium", True)


def test_download_voice_repo_paths() -> None:
    dm = _download_script()
    assert dm._voice_repo_paths("kk_KZ-issai-high", None) == (
        "kk/kk_KZ/issai/high/kk_KZ-issai-high.onnx",
        "kk/kk_KZ/issai/high/kk_KZ-issai-high.onnx.json",
    )
    with pytest.raises(ValueError):
        dm._voice_repo_paths("not a voice", None)
    assert dm.whisper_repo_id("large-v3") == "Systran/faster-whisper-large-v3"
    assert dm.main(["whisper", "--model", "definitely-not-a-model"]) == 2
