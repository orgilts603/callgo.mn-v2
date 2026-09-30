"""Local text-to-speech with Piper (piper-tts >= 1.3 API), as a LiveKit ``tts.TTS``.

:class:`PiperTTS` is a non-streaming (``ChunkedStream``) TTS. Inside an
``AgentSession`` it is wrapped automatically in ``tts.StreamAdapter`` with a
sentence tokenizer (``Agent.default.tts_node``), so every LLM sentence becomes
one ``synthesize()`` call and the first sentence is spoken while the LLM is
still generating.

Piper renders one sentence at a time (``PiperVoice.synthesize`` yields an
``AudioChunk`` per sentence). Each sentence is synthesized in a worker thread
and pushed to the ``AudioEmitter`` in ~``chunk_ms`` slices of raw int16 PCM as
soon as it is ready, with ``sentence_silence`` seconds of silence between
sentences.

Voice files live in ``settings.piper_voices_dir`` as ``<voice>.onnx`` +
``<voice>.onnx.json`` (the layout produced by ``python -m piper.download_voices``
and ``scripts/download_models.py``). Loaded voices are cached per process.
"""

from __future__ import annotations

import asyncio
import json
import logging
import threading
from collections.abc import Callable, Iterable, Iterator
from dataclasses import dataclass, replace
from pathlib import Path
from typing import Any, Protocol

import numpy as np
from livekit.agents import APIError, tts, utils
from livekit.agents.types import (
    DEFAULT_API_CONNECT_OPTIONS,
    NOT_GIVEN,
    APIConnectOptions,
    NotGivenOr,
)
from livekit.agents.utils import is_given

from .config import settings

logger = logging.getLogger("callgo.tts_local")

NUM_CHANNELS = 1
DEFAULT_SAMPLE_RATE = 22050


class _AudioChunkLike(Protocol):
    sample_rate: int

    @property
    def audio_int16_bytes(self) -> bytes: ...


class _PiperVoiceLike(Protocol):
    config: Any

    def synthesize(self, text: str, syn_config: Any = None) -> Iterable[_AudioChunkLike]: ...


VoiceLoader = Callable[[Path, Path, bool], _PiperVoiceLike]


def _default_voice_loader(model_path: Path, config_path: Path, use_cuda: bool) -> _PiperVoiceLike:
    from piper import PiperVoice  # onnxruntime import, keep lazy

    return PiperVoice.load(model_path, config_path=config_path, use_cuda=use_cuda)  # type: ignore[return-value]


def _make_syn_config(opts: PiperOptions) -> Any:
    from piper import SynthesisConfig

    return SynthesisConfig(
        speaker_id=opts.speaker_id,
        length_scale=opts.length_scale,
        noise_scale=opts.noise_scale,
        noise_w_scale=opts.noise_w_scale,
        volume=opts.volume,
    )


# ---- voice resolution --------------------------------------------------------


class PiperVoiceNotFoundError(FileNotFoundError):
    """No usable Piper voice (``.onnx`` + ``.onnx.json``) could be found."""


@dataclass(frozen=True)
class ResolvedVoice:
    name: str
    model_path: Path
    config_path: Path


def _voice_files(voices_dir: Path, voice: str) -> ResolvedVoice | None:
    if not voice:
        return None
    candidate = Path(voice)
    if candidate.suffix == ".onnx" and candidate.is_absolute():
        model = candidate
    else:
        model = voices_dir / (voice if voice.endswith(".onnx") else f"{voice}.onnx")
    config = model.with_name(model.name + ".json")
    if model.is_file() and config.is_file():
        return ResolvedVoice(
            name=model.name.removesuffix(".onnx"), model_path=model, config_path=config
        )
    return None


def available_voices(voices_dir: str | Path | None = None) -> list[str]:
    """Voice names in ``voices_dir`` that have both ``.onnx`` and ``.onnx.json``."""
    d = Path(voices_dir if voices_dir is not None else settings.piper_voices_dir)
    if not d.is_dir():
        return []
    return sorted(
        p.name.removesuffix(".onnx")
        for p in d.glob("*.onnx")
        if p.with_name(p.name + ".json").is_file()
    )


def resolve_voice(
    voice: str | None,
    *,
    voices_dir: str | Path | None = None,
    default_voice: str | None = None,
) -> ResolvedVoice:
    """Find the voice files: requested voice → default voice → any voice in the dir.

    Raises :class:`PiperVoiceNotFoundError` with download instructions when the
    directory holds no voice at all.
    """
    d = Path(voices_dir if voices_dir is not None else settings.piper_voices_dir)
    default = default_voice if default_voice is not None else settings.piper_default_voice

    if voice:
        found = _voice_files(d, voice)
        if found:
            return found
        logger.warning("piper voice %r not found in %s, falling back to %r", voice, d, default)

    found = _voice_files(d, default)
    if found:
        return found

    others = available_voices(d)
    if others:
        logger.warning("default piper voice %r not found in %s, using %r", default, d, others[0])
        found = _voice_files(d, others[0])
        if found:
            return found

    raise PiperVoiceNotFoundError(
        f"no Piper voice found in {d.resolve()} (requested {voice or '-'!r}, default "
        f"{default!r}). Each voice needs <name>.onnx and <name>.onnx.json. Download one "
        "with `python scripts/download_models.py piper` or set CALLGO_PIPER_VOICES_DIR / "
        "CALLGO_PIPER_DEFAULT_VOICE."
    )


def _read_sample_rate(config_path: Path) -> int:
    try:
        with config_path.open(encoding="utf-8") as f:
            cfg = json.load(f)
        return int(cfg.get("audio", {}).get("sample_rate", DEFAULT_SAMPLE_RATE))
    except (OSError, ValueError, TypeError) as e:
        raise PiperVoiceNotFoundError(f"invalid Piper voice config {config_path}: {e}") from e


# ---- process-wide voice cache -----------------------------------------------

_VoiceKey = tuple[str, bool]
_voices: dict[_VoiceKey, _PiperVoiceLike] = {}
_voices_lock = threading.Lock()
_voice_key_locks: dict[_VoiceKey, threading.Lock] = {}


def _get_or_load_voice(
    resolved: ResolvedVoice, use_cuda: bool, loader: VoiceLoader
) -> _PiperVoiceLike:
    key: _VoiceKey = (str(resolved.model_path), use_cuda)
    with _voices_lock:
        cached = _voices.get(key)
        if cached is not None:
            return cached
        key_lock = _voice_key_locks.setdefault(key, threading.Lock())
    with key_lock:
        with _voices_lock:
            cached = _voices.get(key)
        if cached is not None:
            return cached
        logger.info("loading piper voice %s", resolved.model_path)
        loaded = loader(resolved.model_path, resolved.config_path, use_cuda)
        with _voices_lock:
            _voices[key] = loaded
        return loaded


def clear_voice_cache() -> None:
    with _voices_lock:
        _voices.clear()
        _voice_key_locks.clear()


# ---- TTS ---------------------------------------------------------------------


@dataclass
class PiperOptions:
    voice: str
    length_scale: float | None
    noise_scale: float | None
    noise_w_scale: float | None
    sentence_silence: float
    speaker_id: int | None
    volume: float
    chunk_ms: int
    espeak_voice: str | None


class PiperTTS(tts.TTS):
    """Piper VITS voice as a non-streaming LiveKit TTS (mono int16 PCM)."""

    def __init__(
        self,
        *,
        voice: str | None = None,
        voices_dir: str | Path | None = None,
        default_voice: str | None = None,
        length_scale: float | None = None,
        noise_scale: float | None = None,
        noise_w_scale: float | None = None,
        sentence_silence: float = 0.2,
        speaker_id: int | None = None,
        volume: float = 1.0,
        chunk_ms: int = 40,
        espeak_voice: str | None = None,
        use_cuda: bool = False,
        voice_loader: VoiceLoader | None = None,
    ) -> None:
        """
        Args:
            voice: voice name (``<voices_dir>/<voice>.onnx``) or absolute ``.onnx`` path.
                Missing → ``default_voice`` → first voice found in ``voices_dir``.
            voices_dir / default_voice: default ``CALLGO_PIPER_VOICES_DIR`` /
                ``CALLGO_PIPER_DEFAULT_VOICE``.
            length_scale: phoneme duration (>1 slower, <1 faster); None = voice default.
            noise_scale / noise_w_scale: generator / duration noise; None = voice default.
            sentence_silence: seconds of silence inserted between sentences.
            speaker_id: speaker index for multi-speaker voices.
            chunk_ms: size of the PCM slices pushed to the emitter (and of emitted frames).
            espeak_voice: override the voice's espeak-ng phonemizer language, e.g. ``"mn"``
                to phonemize Mongolian text with a voice trained on a related language.
            use_cuda: run ONNX inference with CUDAExecutionProvider.

        Raises:
            PiperVoiceNotFoundError: no voice files exist at all.
        """
        resolved = resolve_voice(voice, voices_dir=voices_dir, default_voice=default_voice)
        super().__init__(
            capabilities=tts.TTSCapabilities(streaming=False),
            sample_rate=_read_sample_rate(resolved.config_path),
            num_channels=NUM_CHANNELS,
        )
        self._resolved = resolved
        self._use_cuda = use_cuda
        self._voice_loader: VoiceLoader = voice_loader or _default_voice_loader
        self._opts = PiperOptions(
            voice=resolved.name,
            length_scale=length_scale,
            noise_scale=noise_scale,
            noise_w_scale=noise_w_scale,
            sentence_silence=max(0.0, sentence_silence),
            speaker_id=speaker_id,
            volume=volume,
            chunk_ms=max(10, chunk_ms),
            espeak_voice=espeak_voice or None,
        )
        self._prewarm_thread: threading.Thread | None = None

    @property
    def model(self) -> str:
        return self._resolved.name

    @property
    def provider(self) -> str:
        return "piper"

    @property
    def voice(self) -> ResolvedVoice:
        return self._resolved

    @property
    def options(self) -> PiperOptions:
        return replace(self._opts)

    def update_options(
        self,
        *,
        length_scale: NotGivenOr[float | None] = NOT_GIVEN,
        noise_scale: NotGivenOr[float | None] = NOT_GIVEN,
        noise_w_scale: NotGivenOr[float | None] = NOT_GIVEN,
        sentence_silence: NotGivenOr[float] = NOT_GIVEN,
        speaker_id: NotGivenOr[int | None] = NOT_GIVEN,
        volume: NotGivenOr[float] = NOT_GIVEN,
    ) -> None:
        if is_given(length_scale):
            self._opts.length_scale = length_scale
        if is_given(noise_scale):
            self._opts.noise_scale = noise_scale
        if is_given(noise_w_scale):
            self._opts.noise_w_scale = noise_w_scale
        if is_given(sentence_silence):
            self._opts.sentence_silence = max(0.0, sentence_silence)
        if is_given(speaker_id):
            self._opts.speaker_id = speaker_id
        if is_given(volume):
            self._opts.volume = volume

    # -- voice loading ------------------------------------------------------------

    def _voice_key(self) -> _VoiceKey:
        return (str(self._resolved.model_path), self._use_cuda)

    def _apply_overrides(self, voice: _PiperVoiceLike) -> _PiperVoiceLike:
        if self._opts.espeak_voice and hasattr(voice.config, "espeak_voice"):
            # NB: the loaded voice is shared per process, so the override applies to
            # every PiperTTS using this model file.
            voice.config.espeak_voice = self._opts.espeak_voice
        return voice

    def load_voice(self) -> _PiperVoiceLike:
        """Blocking load of the (process-shared) voice."""
        voice = _get_or_load_voice(self._resolved, self._use_cuda, self._voice_loader)
        return self._apply_overrides(voice)

    async def ensure_voice(self) -> _PiperVoiceLike:
        """Load (once, in a worker thread) and return the shared voice."""
        with _voices_lock:
            cached = _voices.get(self._voice_key())
        if cached is None:
            return await asyncio.to_thread(self.load_voice)
        return self._apply_overrides(cached)

    def prewarm(self) -> None:
        """Non-blocking: load the voice in a background thread."""
        with _voices_lock:
            if self._voice_key() in _voices:
                return
        if self._prewarm_thread is not None and self._prewarm_thread.is_alive():
            return

        def _load() -> None:
            try:
                self.load_voice()
            except Exception:
                logger.exception("failed to prewarm piper voice %s", self._resolved.model_path)

        self._prewarm_thread = threading.Thread(target=_load, name="piper-prewarm", daemon=True)
        self._prewarm_thread.start()

    def synthesize(
        self, text: str, *, conn_options: APIConnectOptions = DEFAULT_API_CONNECT_OPTIONS
    ) -> PiperChunkedStream:
        return PiperChunkedStream(tts=self, input_text=text, conn_options=conn_options)


_END = object()


def _pcm_slices(pcm: bytes, slice_bytes: int) -> Iterator[bytes]:
    for i in range(0, len(pcm), slice_bytes):
        yield pcm[i : i + slice_bytes]


class PiperChunkedStream(tts.ChunkedStream):
    def __init__(self, *, tts: PiperTTS, input_text: str, conn_options: APIConnectOptions) -> None:
        super().__init__(tts=tts, input_text=input_text, conn_options=conn_options)
        self._tts: PiperTTS = tts
        self._opts = replace(tts._opts)

    async def _run(self, output_emitter: tts.AudioEmitter) -> None:
        sample_rate = self._tts.sample_rate
        output_emitter.initialize(
            request_id=utils.shortuuid(),
            sample_rate=sample_rate,
            num_channels=NUM_CHANNELS,
            mime_type="audio/pcm",
            frame_size_ms=self._opts.chunk_ms,
        )

        text = self._input_text
        if not text.strip():
            output_emitter.flush()
            return

        try:
            voice = await self._tts.ensure_voice()
            syn_config = _make_syn_config(self._opts)
        except Exception as e:
            raise APIError(f"failed to load piper voice: {e}", retryable=False) from e

        bytes_per_sample = 2 * NUM_CHANNELS
        slice_bytes = max(1, sample_rate * self._opts.chunk_ms // 1000) * bytes_per_sample
        silence = b"\x00" * (int(sample_rate * self._opts.sentence_silence) * bytes_per_sample)

        chunks: Iterator[_AudioChunkLike] = iter(())
        pushed = 0
        try:
            chunks = iter(voice.synthesize(text, syn_config))
            while True:
                # each next() phonemizes (first call) and runs ONNX for one sentence
                chunk: Any = await asyncio.to_thread(next, chunks, _END)
                if chunk is _END:
                    break
                pcm: bytes = chunk.audio_int16_bytes
                if not pcm:
                    continue
                if chunk.sample_rate != sample_rate:
                    raise APIError(
                        f"piper produced {chunk.sample_rate} Hz audio, expected {sample_rate} Hz",
                        retryable=False,
                    )
                if pushed and silence:
                    output_emitter.push(silence)
                for piece in _pcm_slices(pcm, slice_bytes):
                    output_emitter.push(piece)
                pushed += 1
        except APIError:
            raise
        except Exception as e:
            raise APIError(f"piper synthesis failed: {e}", retryable=False) from e
        finally:
            close = getattr(chunks, "close", None)
            if callable(close):
                try:
                    close()
                except ValueError:
                    pass  # still running in a worker thread after cancellation

        if pushed == 0:
            # text with nothing pronounceable (e.g. only punctuation): emit a short
            # silence rather than failing the turn with "no audio frames were pushed".
            output_emitter.push(np.zeros(sample_rate // 20, dtype=np.int16).tobytes())
        output_emitter.flush()
