"""Local speech-to-text with faster-whisper, exposed as a LiveKit Agents ``stt.STT``.

Whisper is a *batch* recogniser: :class:`WhisperSTT` declares
``streaming=False`` and only implements ``_recognize_impl`` (one call per
utterance). Inside an ``AgentSession`` that is enough: when the STT is not
streaming, ``Agent.default.stt_node`` (livekit-agents 1.8.3,
``voice/agent.py``) wraps it on the fly in ``stt.StreamAdapter(stt, vad)``
using the session's VAD, so each VAD-detected utterance is sent to
``recognize()`` and returned as a ``FINAL_TRANSCRIPT``. Without a VAD it raises
``RuntimeError``. :meth:`WhisperSTT.streaming` builds the same adapter
explicitly (with Silero VAD) for code that calls ``stt.stream()`` directly.

The CTranslate2 model is loaded lazily in a worker thread, once per process
per ``(model, device, compute_type, download_root)``, and shared by every
``WhisperSTT`` instance/session in the process.
"""

from __future__ import annotations

import asyncio
import logging
import math
import threading
from collections.abc import Callable, Sequence
from dataclasses import dataclass, replace
from typing import TYPE_CHECKING, Any, Protocol

import numpy as np
from livekit import rtc
from livekit.agents import stt, utils
from livekit.agents.types import (
    DEFAULT_API_CONNECT_OPTIONS,
    NOT_GIVEN,
    APIConnectOptions,
    NotGivenOr,
)
from livekit.agents.utils import AudioBuffer, is_given

from .config import settings

if TYPE_CHECKING:
    from livekit.agents import vad as lk_vad

    from .schemas import AgentProfile

logger = logging.getLogger("callgo.stt_local")

WHISPER_SAMPLE_RATE = 16000
"""faster-whisper expects mono float32 PCM at 16 kHz in [-1, 1]."""

FALLBACK_TEMPERATURES: tuple[float, ...] = (0.0, 0.2, 0.4, 0.6, 0.8, 1.0)
"""faster-whisper's default fallback schedule. More robust, but on noise every step can
decode up to the token limit, multiplying latency by up to 6x, so it is opt-in here."""

MAX_COMPRESSION_RATIO = 2.4
"""Segments above this gzip ratio are repetition loops ("A-A-A-A…") and are dropped."""

_MAX_NEW_TOKENS_CAP = 220
"""Whisper's context is 448 tokens; the prompt (initial_prompt/hotwords) may take ~225."""

_AUTO_LANGUAGES = frozenset({"", "auto", "detect"})


class WhisperSTTError(RuntimeError):
    """Local model load / decode failure.

    Deliberately *not* an ``APIError``: ``STT.recognize`` retries every ``APIError``
    (ignoring ``retryable``), which is pointless for a missing model file.
    """


class _Segment(Protocol):
    start: float
    end: float
    text: str
    avg_logprob: float


class _WhisperModelLike(Protocol):
    def transcribe(self, audio: np.ndarray, **kwargs: Any) -> tuple[Any, Any]: ...


ModelFactory = Callable[..., _WhisperModelLike]


def _default_model_factory(model: str, **kwargs: Any) -> _WhisperModelLike:
    from faster_whisper import WhisperModel  # heavy import (ctranslate2), keep lazy

    return WhisperModel(model, **kwargs)  # type: ignore[no-any-return]


# ---- process-wide model cache ------------------------------------------------

_ModelKey = tuple[str, str, str, str | None]
_models: dict[_ModelKey, _WhisperModelLike] = {}
_models_lock = threading.Lock()
_key_locks: dict[_ModelKey, threading.Lock] = {}


def _get_or_load_model(
    key: _ModelKey, factory: ModelFactory, extra: dict[str, Any]
) -> _WhisperModelLike:
    """Return the cached model for ``key``, loading it (blocking) on first use."""
    with _models_lock:
        cached = _models.get(key)
        if cached is not None:
            return cached
        key_lock = _key_locks.setdefault(key, threading.Lock())

    # load outside the global lock so different models can load concurrently,
    # while concurrent loads of the *same* model wait for the first one.
    with key_lock:
        with _models_lock:
            cached = _models.get(key)
        if cached is not None:
            return cached

        model, device, compute_type, download_root = key
        logger.info(
            "loading faster-whisper model",
            extra={"model": model, "device": device, "compute_type": compute_type},
        )
        loaded = factory(
            model,
            device=device,
            compute_type=compute_type,
            download_root=download_root,
            **extra,
        )
        with _models_lock:
            _models[key] = loaded
        return loaded


def clear_model_cache() -> None:
    """Drop every cached model (tests / hot reload)."""
    with _models_lock:
        _models.clear()
        _key_locks.clear()


# ---- audio conversion --------------------------------------------------------


def _resample_linear(pcm: np.ndarray, src_rate: int, dst_rate: int) -> np.ndarray:
    """Linear-interpolation resampler (float32 in, float32 out). Fallback only."""
    if src_rate == dst_rate or pcm.size == 0:
        return pcm
    n_out = max(1, round(pcm.size * dst_rate / src_rate))
    x_old = np.arange(pcm.size, dtype=np.float64) / src_rate
    x_new = np.arange(n_out, dtype=np.float64) / dst_rate
    return np.interp(x_new, x_old, pcm).astype(np.float32)


def _resample_int16(pcm: np.ndarray, src_rate: int, dst_rate: int) -> np.ndarray:
    """Resample mono int16 PCM, preferring LiveKit's (sox-quality) resampler."""
    if src_rate == dst_rate or pcm.size == 0:
        return pcm
    try:
        resampler = rtc.AudioResampler(
            src_rate, dst_rate, num_channels=1, quality=rtc.AudioResamplerQuality.HIGH
        )
        frame = rtc.AudioFrame(
            data=pcm.tobytes(),
            sample_rate=src_rate,
            num_channels=1,
            samples_per_channel=pcm.size,
        )
        out = resampler.push(frame) + resampler.flush()
        if not out:
            raise RuntimeError("resampler produced no audio")
        return np.concatenate([np.frombuffer(f.data, dtype=np.int16) for f in out])
    except Exception:  # pragma: no cover - native resampler unavailable
        logger.warning(
            "rtc.AudioResampler failed, falling back to linear resampling", exc_info=True
        )
        resampled = _resample_linear(pcm.astype(np.float32), src_rate, dst_rate)
        return np.clip(np.rint(resampled), -32768, 32767).astype(np.int16)


def frames_to_whisper_audio(buffer: AudioBuffer) -> np.ndarray:
    """Convert a LiveKit ``AudioBuffer`` to mono float32 @16 kHz in [-1, 1].

    Multi-channel audio is down-mixed by averaging; other sample rates are
    resampled with ``rtc.AudioResampler`` (numpy linear interpolation fallback).
    """
    if isinstance(buffer, list):
        if not buffer:
            return np.zeros(0, dtype=np.float32)
        frame = utils.merge_frames(buffer)
    else:
        frame = buffer

    pcm = np.frombuffer(frame.data, dtype=np.int16)
    channels = max(1, frame.num_channels)
    if channels > 1:
        pcm = pcm.reshape(-1, channels).astype(np.int32).mean(axis=1).astype(np.int16)

    pcm = _resample_int16(pcm, frame.sample_rate, WHISPER_SAMPLE_RATE)
    return (pcm.astype(np.float32) / 32768.0).clip(-1.0, 1.0)


def _whisper_language(language: str | None) -> str | None:
    """Map a BCP-47-ish code ("mn", "mn-MN", "mn_MN") to Whisper's ISO-639-1, None=detect."""
    if language is None:
        return None
    lang = language.strip().lower().replace("_", "-")
    if lang in _AUTO_LANGUAGES:
        return None
    return lang.split("-", 1)[0]


def _is_repetition(seg: _Segment) -> bool:
    ratio = getattr(seg, "compression_ratio", None)
    return isinstance(ratio, int | float) and ratio > MAX_COMPRESSION_RATIO


def _segment_confidence(seg: _Segment) -> float:
    try:
        return float(min(1.0, max(0.0, math.exp(float(seg.avg_logprob)))))
    except (TypeError, ValueError, OverflowError):
        return 0.0


# ---- STT ---------------------------------------------------------------------


@dataclass
class WhisperOptions:
    model: str
    language: str | None
    device: str
    compute_type: str
    beam_size: int
    initial_prompt: str | None
    hotwords: str | None
    download_root: str | None
    cpu_threads: int
    num_workers: int
    local_files_only: bool
    temperature: float | tuple[float, ...]
    max_tokens_per_second: float | None


class WhisperSTT(stt.STT):
    """faster-whisper recogniser (non-streaming; use a VAD / ``streaming()``)."""

    def __init__(
        self,
        *,
        model: str | None = None,
        language: str | None = None,
        device: str | None = None,
        compute_type: str | None = None,
        beam_size: int = 5,
        initial_prompt: str | None = None,
        hotwords: str | None = None,
        download_root: str | None = None,
        cpu_threads: int = 0,
        num_workers: int = 1,
        local_files_only: bool = False,
        temperature: float | tuple[float, ...] = 0.0,
        max_tokens_per_second: float | None = 20.0,
        model_factory: ModelFactory | None = None,
    ) -> None:
        """
        Args:
            model: faster-whisper size (``tiny`` … ``large-v3``, ``turbo``), a HF repo id
                (``Systran/faster-whisper-large-v3``) or a local CTranslate2 model dir.
                Defaults to ``CALLGO_WHISPER_MODEL``.
            language: default recognition language (``"mn"``); ``"auto"``/``""`` lets
                Whisper detect it. Defaults to ``CALLGO_DEFAULT_LANGUAGE``.
            device: ``cpu`` | ``cuda`` | ``auto`` (default ``CALLGO_WHISPER_DEVICE``).
            compute_type: CTranslate2 compute type, e.g. ``int8`` on CPU, ``float16`` on GPU,
                ``auto`` (default ``CALLGO_WHISPER_COMPUTE_TYPE``).
            beam_size: decoding beam size (1 = greedy, fastest).
            initial_prompt: optional text prompt biasing vocabulary/spelling.
            hotwords: optional space-separated hot words (faster-whisper ``hotwords``).
            download_root: model cache dir (default: Hugging Face cache, ``HF_HOME``).
            local_files_only: never hit the network when resolving the model.
            temperature: decoding temperature; pass ``FALLBACK_TEMPERATURES`` for Whisper's
                fallback schedule (more robust, slower worst case).
            max_tokens_per_second: bounds decoding to ``20 tok/s`` of audio (+16) so noise
                cannot make Whisper generate 448 tokens; Mongolian runs ~1.5 chars/token,
                i.e. ~10 tok/s of fast speech. ``None`` disables the bound.
            model_factory: injectable ``WhisperModel`` constructor (tests).
        """
        super().__init__(
            capabilities=stt.STTCapabilities(
                streaming=False,
                interim_results=False,
                offline_recognize=True,
                keyterms=True,
            )
        )
        self._opts = WhisperOptions(
            model=model or settings.whisper_model,
            language=language if language is not None else settings.default_language,
            device=device or settings.whisper_device,
            compute_type=compute_type or settings.whisper_compute_type,
            beam_size=max(1, beam_size),
            initial_prompt=initial_prompt or None,
            hotwords=hotwords or None,
            download_root=download_root,
            cpu_threads=cpu_threads,
            num_workers=max(1, num_workers),
            local_files_only=local_files_only,
            temperature=temperature,
            max_tokens_per_second=max_tokens_per_second,
        )
        self._model_factory: ModelFactory = model_factory or _default_model_factory
        self._session_keyterms: list[str] = []
        self._prewarm_thread: threading.Thread | None = None

    # -- construction helpers -------------------------------------------------

    @classmethod
    def for_profile(cls, profile: AgentProfile, **kwargs: Any) -> WhisperSTT:
        """Build from an :class:`AgentProfile` (``stt_model`` / ``language``)."""
        kwargs.setdefault("model", profile.stt_model or settings.whisper_model)
        kwargs.setdefault("language", profile.language or settings.default_language)
        return cls(**kwargs)

    def streaming(self, vad: lk_vad.VAD | None = None, **vad_options: Any) -> stt.StreamAdapter:
        """Wrap in ``stt.StreamAdapter`` driven by a VAD (Silero by default).

        ``vad_options`` are forwarded to ``silero.VAD.load`` when no ``vad`` is given.
        Not needed inside ``AgentSession(vad=...)``, which adapts automatically.
        """
        if vad is None:
            from livekit.plugins import silero

            vad = silero.VAD.load(**vad_options)
        return stt.StreamAdapter(stt=self, vad=vad)

    # -- metadata ---------------------------------------------------------------

    @property
    def model(self) -> str:
        return self._opts.model

    @property
    def provider(self) -> str:
        return "faster-whisper"

    @property
    def options(self) -> WhisperOptions:
        return replace(self._opts)

    def update_options(
        self,
        *,
        language: NotGivenOr[str | None] = NOT_GIVEN,
        beam_size: NotGivenOr[int] = NOT_GIVEN,
        initial_prompt: NotGivenOr[str | None] = NOT_GIVEN,
        hotwords: NotGivenOr[str | None] = NOT_GIVEN,
    ) -> None:
        if is_given(language):
            self._opts.language = language
        if is_given(beam_size):
            self._opts.beam_size = max(1, beam_size)
        if is_given(initial_prompt):
            self._opts.initial_prompt = initial_prompt or None
        if is_given(hotwords):
            self._opts.hotwords = hotwords or None

    def _update_session_keyterms(self, keyterms: list[str]) -> None:
        self._session_keyterms = [k.strip() for k in keyterms if k.strip()]

    def _effective_hotwords(self) -> str | None:
        parts: list[str] = []
        if self._opts.hotwords:
            parts.append(self._opts.hotwords)
        parts.extend(self._session_keyterms)
        return " ".join(parts) or None

    # -- model loading ----------------------------------------------------------

    def _model_key(self) -> _ModelKey:
        o = self._opts
        return (o.model, o.device, o.compute_type, o.download_root)

    def load_model(self) -> _WhisperModelLike:
        """Blocking load (use from a job-process ``prewarm_fnc`` or a thread)."""
        o = self._opts
        extra: dict[str, Any] = {
            "cpu_threads": o.cpu_threads,
            "num_workers": o.num_workers,
            "local_files_only": o.local_files_only,
        }
        return _get_or_load_model(self._model_key(), self._model_factory, extra)

    async def ensure_model(self) -> _WhisperModelLike:
        """Load (once, in a worker thread) and return the shared model."""
        with _models_lock:
            cached = _models.get(self._model_key())
        if cached is not None:
            return cached
        return await asyncio.to_thread(self.load_model)

    def prewarm(self) -> None:
        """Non-blocking: start loading the model in a background thread.

        Called synchronously on the event loop by ``AgentActivity`` when the
        session starts, so it must not block.
        """
        with _models_lock:
            if self._model_key() in _models:
                return
        if self._prewarm_thread is not None and self._prewarm_thread.is_alive():
            return

        def _load() -> None:
            try:
                self.load_model()
            except Exception:
                logger.exception("failed to prewarm faster-whisper model %s", self._opts.model)

        self._prewarm_thread = threading.Thread(target=_load, name="whisper-prewarm", daemon=True)
        self._prewarm_thread.start()

    # -- recognition --------------------------------------------------------------

    def _transcribe_sync(
        self, model: _WhisperModelLike, buffer: AudioBuffer, language: str | None
    ) -> stt.SpeechEvent:
        audio = frames_to_whisper_audio(buffer)
        request_id = utils.shortuuid()
        requested_lang = _whisper_language(language)
        fallback_lang = requested_lang or _whisper_language(settings.default_language) or "mn"

        if audio.size == 0:
            return stt.SpeechEvent(
                type=stt.SpeechEventType.FINAL_TRANSCRIPT,
                request_id=request_id,
                alternatives=[stt.SpeechData(language=fallback_lang, text="")],
            )

        o = self._opts
        max_new_tokens: int | None = None
        if o.max_tokens_per_second:
            window_s = min(audio.size / WHISPER_SAMPLE_RATE, 30.0)  # per 30 s Whisper window
            max_new_tokens = min(
                _MAX_NEW_TOKENS_CAP, int(math.ceil(window_s * o.max_tokens_per_second)) + 16
            )
        segments_iter, info = model.transcribe(
            audio,
            language=requested_lang,
            beam_size=o.beam_size,
            vad_filter=False,
            condition_on_previous_text=False,
            initial_prompt=o.initial_prompt,
            hotwords=self._effective_hotwords(),
            temperature=o.temperature,
            without_timestamps=False,
            max_new_tokens=max_new_tokens,
        )
        # the generator performs the actual decoding: consume it here, in the thread
        segments: Sequence[_Segment] = [s for s in segments_iter if not _is_repetition(s)]

        texts = [s.text.strip() for s in segments if s.text and s.text.strip()]
        text = " ".join(texts)
        confidences = [_segment_confidence(s) for s in segments]
        confidence = float(sum(confidences) / len(confidences)) if confidences else 0.0
        detected = getattr(info, "language", None) or fallback_lang

        return stt.SpeechEvent(
            type=stt.SpeechEventType.FINAL_TRANSCRIPT,
            request_id=request_id,
            alternatives=[
                stt.SpeechData(
                    language=detected,
                    text=text,
                    start_time=float(segments[0].start) if segments else 0.0,
                    end_time=float(segments[-1].end) if segments else 0.0,
                    confidence=confidence,
                )
            ],
        )

    async def _recognize_impl(
        self,
        buffer: AudioBuffer,
        *,
        language: NotGivenOr[str] = NOT_GIVEN,
        conn_options: APIConnectOptions = DEFAULT_API_CONNECT_OPTIONS,
    ) -> stt.SpeechEvent:
        lang = language if is_given(language) else self._opts.language
        try:
            model = await self.ensure_model()
        except Exception as e:
            # a missing / corrupt model will not fix itself on retry
            raise WhisperSTTError(
                f"failed to load faster-whisper model {self._opts.model!r}: {e}"
            ) from e
        try:
            return await asyncio.to_thread(self._transcribe_sync, model, buffer, lang)
        except Exception as e:
            raise WhisperSTTError(f"faster-whisper transcription failed: {e}") from e
