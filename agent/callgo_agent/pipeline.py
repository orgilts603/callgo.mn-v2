"""STT / TTS factories: map an :class:`AgentProfile` to LiveKit plugin instances.

``profile.stt_provider``:
    ``faster_whisper`` (default, local) · ``openai`` · ``google`` · ``groq``
``profile.tts_provider``:
    ``piper`` (default, local) · ``openai`` · ``google``

Cloud plugins are imported lazily so a worker running fully local models never
imports (or needs credentials for) them. Credentials come from the usual
provider env vars (``OPENAI_API_KEY``, ``GROQ_API_KEY``,
``GOOGLE_APPLICATION_CREDENTIALS``).

Both local engines are non-streaming; ``AgentSession`` adapts them itself
(``stt.StreamAdapter`` with the session VAD, ``tts.StreamAdapter`` with a
sentence tokenizer), so pass a VAD to the session (``silero.VAD.load()``).
"""

from __future__ import annotations

import importlib
from types import ModuleType

from livekit.agents import stt, tts

from .config import settings
from .schemas import AgentProfile

STT_PROVIDERS = ("faster_whisper", "openai", "google", "groq")
TTS_PROVIDERS = ("piper", "openai", "google")

_STT_ALIASES = {
    "": "faster_whisper",
    "faster-whisper": "faster_whisper",
    "fasterwhisper": "faster_whisper",
    "whisper": "faster_whisper",
    "local": "faster_whisper",
}
_TTS_ALIASES = {"": "piper", "local": "piper"}

# BCP-47 region defaults for providers that want a locale (Google).
_GOOGLE_LOCALES = {
    "mn": "mn-MN",
    "en": "en-US",
    "ru": "ru-RU",
    "kk": "kk-KZ",
    "zh": "cmn-Hans-CN",
    "ko": "ko-KR",
    "ja": "ja-JP",
}


class PluginUnavailableError(RuntimeError):
    """A cloud provider was selected but its LiveKit plugin is not installed."""


def _load_plugin(name: str) -> ModuleType:
    """Import ``livekit.plugins.<name>`` (indirection so tests can stub it)."""
    try:
        return importlib.import_module(f"livekit.plugins.{name}")
    except ImportError as e:
        raise PluginUnavailableError(
            f"livekit-plugins-{name} is not installed; install it or pick another provider"
        ) from e


def _normalize(value: str, aliases: dict[str, str]) -> str:
    key = (value or "").strip().lower()
    return aliases.get(key, key.replace("-", "_"))


def _language(profile: AgentProfile) -> str:
    return (profile.language or settings.default_language).strip() or "mn"


def _base_language(lang: str) -> str:
    return lang.replace("_", "-").split("-", 1)[0].lower()


def google_locale(lang: str) -> str:
    """``mn`` → ``mn-MN``; codes that already carry a region pass through."""
    norm = lang.replace("_", "-")
    if "-" in norm:
        return norm
    return _GOOGLE_LOCALES.get(norm.lower(), norm)


def build_stt(profile: AgentProfile) -> stt.STT:
    """Build the (possibly non-streaming) STT selected by ``profile.stt_provider``."""
    provider = _normalize(profile.stt_provider, _STT_ALIASES)
    lang = _language(profile)
    model = (profile.stt_model or "").strip()

    if provider == "faster_whisper":
        from .stt_local import WhisperSTT

        return WhisperSTT(model=model or settings.whisper_model, language=lang)

    if provider == "openai":
        openai = _load_plugin("openai")
        # whisper-1 is served by the batch transcription endpoint (non-streaming)
        return openai.STT(model=_cloud_model(model, "whisper-1"), language=_base_language(lang))  # type: ignore[no-any-return]

    if provider == "google":
        google = _load_plugin("google")
        kwargs: dict[str, object] = {"languages": [google_locale(lang)], "detect_language": False}
        if model and not _is_whisper_model(model):
            kwargs["model"] = model
        return google.STT(**kwargs)  # type: ignore[no-any-return]

    if provider == "groq":
        groq = _load_plugin("groq")
        return groq.STT(  # type: ignore[no-any-return]
            model=_cloud_model(model, "whisper-large-v3-turbo"), language=_base_language(lang)
        )

    raise ValueError(
        f"unknown stt_provider {profile.stt_provider!r}; expected one of {', '.join(STT_PROVIDERS)}"
    )


def build_tts(profile: AgentProfile) -> tts.TTS:
    """Build the TTS selected by ``profile.tts_provider``."""
    provider = _normalize(profile.tts_provider, _TTS_ALIASES)
    voice = (profile.tts_voice or "").strip()

    if provider == "piper":
        from .tts_local import PiperTTS

        return PiperTTS(voice=voice or None)

    if provider == "openai":
        openai = _load_plugin("openai")
        return openai.TTS(voice=voice or "alloy")  # type: ignore[no-any-return]

    if provider == "google":
        google = _load_plugin("google")
        kwargs: dict[str, object] = {"language": google_locale(_language(profile))}
        if voice:
            kwargs["voice_name"] = voice
        return google.TTS(**kwargs)  # type: ignore[no-any-return]

    raise ValueError(
        f"unknown tts_provider {profile.tts_provider!r}; expected one of {', '.join(TTS_PROVIDERS)}"
    )


def _is_whisper_model(model: str) -> bool:
    """Local faster-whisper ids ("large-v3", "small", …) that a cloud STT would reject."""
    m = model.lower()
    return m in {
        "tiny",
        "base",
        "small",
        "medium",
        "large",
        "large-v1",
        "large-v2",
        "large-v3",
        "large-v3-turbo",
        "turbo",
        "distil-large-v3",
    } or m.startswith(("systran/", "tiny.", "base.", "small.", "medium.", "distil-"))


def _cloud_model(model: str, default: str) -> str:
    """The profile's default ``stt_model`` is ``large-v3`` (a local id): map it to ``default``."""
    if not model or _is_whisper_model(model):
        return default
    return model
