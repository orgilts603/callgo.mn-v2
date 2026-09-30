"""Audio helpers for the offline harness and tests.

Everything here works on ``int16`` mono ``numpy`` arrays. WAV I/O uses only the
stdlib ``wave`` module; resampling is plain linear interpolation, which is good
enough for speech-band test material (not for production audio paths).
"""

from __future__ import annotations

import io
import math
import wave
from collections.abc import Iterable, Iterator
from dataclasses import dataclass
from pathlib import Path

import numpy as np
from livekit import rtc

INT16_MAX = 32767


def _to_int16(x: np.ndarray) -> np.ndarray:
    return np.clip(np.rint(x), -32768, INT16_MAX).astype(np.int16)


def resample_linear(pcm: np.ndarray, src_rate: int, dst_rate: int) -> np.ndarray:
    """Linearly resample an int16 mono signal from ``src_rate`` to ``dst_rate``."""
    if src_rate <= 0 or dst_rate <= 0:
        raise ValueError("sample rates must be positive")
    if src_rate == dst_rate or pcm.size == 0:
        return pcm.astype(np.int16, copy=True)
    n_out = max(1, round(pcm.size * dst_rate / src_rate))
    src_pos = np.arange(n_out, dtype=np.float64) * (src_rate / dst_rate)
    out = np.interp(src_pos, np.arange(pcm.size, dtype=np.float64), pcm.astype(np.float64))
    return _to_int16(out)


def _decode_wav(wf: wave.Wave_read) -> tuple[np.ndarray, int]:
    channels = wf.getnchannels()
    width = wf.getsampwidth()
    rate = wf.getframerate()
    raw = wf.readframes(wf.getnframes())
    if width == 2:
        data = np.frombuffer(raw, dtype="<i2").astype(np.float64)
    elif width == 1:  # unsigned 8-bit
        data = (np.frombuffer(raw, dtype=np.uint8).astype(np.float64) - 128.0) * 256.0
    elif width == 4:
        data = np.frombuffer(raw, dtype="<i4").astype(np.float64) / 65536.0
    elif width == 3:
        b = np.frombuffer(raw, dtype=np.uint8).reshape(-1, 3)
        v = (
            b[:, 0].astype(np.int32)
            | (b[:, 1].astype(np.int32) << 8)
            | (b[:, 2].astype(np.int32) << 16)
        )
        v = np.where(v & 0x800000, v - 0x1000000, v)
        data = v.astype(np.float64) / 256.0
    else:
        raise ValueError(f"unsupported WAV sample width: {width} bytes")
    if channels > 1:
        data = data[: (data.size // channels) * channels].reshape(-1, channels).mean(axis=1)
    return _to_int16(data), rate


def read_wav(
    source: str | Path | bytes | io.BufferedIOBase, target_rate: int | None = None
) -> tuple[np.ndarray, int]:
    """Read a WAV file as int16 mono, optionally resampled to ``target_rate``.

    Returns ``(pcm, sample_rate)`` where ``sample_rate`` is the rate of ``pcm``.
    """
    if isinstance(source, bytes):
        source = io.BytesIO(source)
    with wave.open(source if not isinstance(source, Path) else str(source), "rb") as wf:
        pcm, rate = _decode_wav(wf)
    if target_rate is not None and target_rate != rate:
        pcm = resample_linear(pcm, rate, target_rate)
        rate = target_rate
    return pcm, rate


def wav_bytes(pcm: np.ndarray, sample_rate: int) -> bytes:
    """Encode int16 mono PCM as WAV bytes."""
    buf = io.BytesIO()
    with wave.open(buf, "wb") as wf:
        wf.setnchannels(1)
        wf.setsampwidth(2)
        wf.setframerate(sample_rate)
        wf.writeframes(pcm.astype("<i2").tobytes())
    return buf.getvalue()


def write_wav(
    path: str | Path, pcm: np.ndarray, sample_rate: int, *, rate: int | None = None
) -> None:
    """Write int16 mono PCM to ``path``; ``rate`` resamples to that rate first."""
    if rate is not None and rate != sample_rate:
        pcm = resample_linear(pcm, sample_rate, rate)
        sample_rate = rate
    Path(path).write_bytes(wav_bytes(pcm, sample_rate))


def frames_from_pcm(
    pcm: np.ndarray, sample_rate: int, frame_ms: int = 20
) -> Iterator[rtc.AudioFrame]:
    """Split int16 mono PCM into fixed-size ``rtc.AudioFrame``s (last one zero-padded)."""
    spf = max(1, sample_rate * frame_ms // 1000)
    pcm = np.ascontiguousarray(pcm, dtype="<i2")
    for start in range(0, pcm.size, spf):
        chunk = pcm[start : start + spf]
        if chunk.size < spf:
            chunk = np.concatenate([chunk, np.zeros(spf - chunk.size, dtype="<i2")])
        yield rtc.AudioFrame(chunk.tobytes(), sample_rate, 1, spf)


def pcm_from_frames(frames: Iterable[rtc.AudioFrame], *, mono: bool = True) -> np.ndarray:
    """Concatenate frames into one int16 array (multi-channel frames are averaged to mono)."""
    parts: list[np.ndarray] = []
    for f in frames:
        arr = np.frombuffer(bytes(f.data), dtype="<i2")
        if f.num_channels > 1 and mono:
            arr = _to_int16(arr.reshape(-1, f.num_channels).astype(np.float64).mean(axis=1))
        parts.append(arr.astype(np.int16))
    if not parts:
        return np.zeros(0, dtype=np.int16)
    return np.concatenate(parts)


def combine_frames(
    frames: Iterable[rtc.AudioFrame], sample_rate: int | None = None
) -> rtc.AudioFrame:
    """Merge frames into a single mono ``AudioFrame`` (what STT.recognize expects)."""
    frame_list = list(frames)
    rate = sample_rate or (frame_list[0].sample_rate if frame_list else 16000)
    pcm = pcm_from_frames(frame_list)
    if frame_list and frame_list[0].sample_rate != rate:
        pcm = resample_linear(pcm, frame_list[0].sample_rate, rate)
    return pcm_to_frame(pcm, rate)


def pcm_to_frame(pcm: np.ndarray, sample_rate: int) -> rtc.AudioFrame:
    data = np.ascontiguousarray(pcm, dtype="<i2")
    return rtc.AudioFrame(data.tobytes(), sample_rate, 1, int(data.size))


def duration_s(pcm: np.ndarray, sample_rate: int) -> float:
    return float(pcm.size) / float(sample_rate) if sample_rate else 0.0


def rms(pcm: np.ndarray) -> float:
    if pcm.size == 0:
        return 0.0
    x = pcm.astype(np.float64) / INT16_MAX
    return float(np.sqrt(np.mean(x * x)))


@dataclass(frozen=True)
class Segment:
    """A detected utterance as sample offsets into the source PCM."""

    start: int
    end: int

    def slice(self, pcm: np.ndarray) -> np.ndarray:
        return pcm[self.start : self.end]

    def start_ms(self, sample_rate: int) -> int:
        return int(self.start * 1000 / sample_rate)

    def end_ms(self, sample_rate: int) -> int:
        return int(self.end * 1000 / sample_rate)


def segment_utterances(
    pcm: np.ndarray,
    sample_rate: int,
    threshold: float = 0.02,
    min_silence_ms: int = 400,
    *,
    frame_ms: int = 20,
    min_speech_ms: int = 100,
    pad_ms: int = 60,
) -> list[Segment]:
    """Very small energy VAD: split ``pcm`` into utterances separated by silence.

    ``threshold`` is a normalized RMS level (0..1) per ``frame_ms`` window. A gap of
    at least ``min_silence_ms`` below threshold ends an utterance; runs shorter than
    ``min_speech_ms`` are dropped as clicks.
    """
    spf = max(1, sample_rate * frame_ms // 1000)
    n_frames = pcm.size // spf
    if n_frames == 0:
        return []
    x = pcm[: n_frames * spf].astype(np.float64).reshape(n_frames, spf) / INT16_MAX
    active = np.sqrt(np.mean(x * x, axis=1)) >= threshold

    gap_frames = max(1, math.ceil(min_silence_ms / frame_ms))
    min_frames = max(1, math.ceil(min_speech_ms / frame_ms))
    pad = pad_ms * sample_rate // 1000

    runs: list[tuple[int, int]] = []  # inclusive frame indices
    start: int | None = None
    last_active = -1
    for i, a in enumerate(active):
        if a:
            if start is None:
                start = i
            last_active = i
        elif start is not None and i - last_active >= gap_frames:
            runs.append((start, last_active))
            start = None
    if start is not None:
        runs.append((start, last_active))

    out: list[Segment] = []
    for s, e in runs:
        if e - s + 1 < min_frames:
            continue
        out.append(Segment(max(0, s * spf - pad), min(pcm.size, (e + 1) * spf + pad)))
    return out


def synth_tone(
    freq_hz: float = 440.0,
    duration_s_: float = 0.5,
    sample_rate: int = 16000,
    amplitude: float = 0.4,
) -> np.ndarray:
    """A plain sine tone as int16 mono."""
    t = np.arange(int(duration_s_ * sample_rate)) / sample_rate
    return _to_int16(np.sin(2 * math.pi * freq_hz * t) * amplitude * INT16_MAX)


def synth_speechlike(
    duration_s_: float = 1.0,
    sample_rate: int = 16000,
    f0: float = 140.0,
    syllable_hz: float = 4.0,
    amplitude: float = 0.35,
) -> np.ndarray:
    """Speech-like burst: harmonic buzz with formant-ish partials and a syllabic envelope."""
    n = int(duration_s_ * sample_rate)
    t = np.arange(n) / sample_rate
    sig = np.zeros(n)
    for k in range(1, 12):
        sig += np.sin(2 * math.pi * f0 * k * t) / k
    env = 0.55 + 0.45 * np.sin(2 * math.pi * syllable_hz * t) ** 2
    fade = np.minimum(1.0, np.minimum(t, duration_s_ - t) / 0.02)  # 20 ms edges
    sig = sig / max(1e-9, np.max(np.abs(sig))) * env * fade * amplitude
    return _to_int16(sig * INT16_MAX)


def synth_utterances(
    n: int = 2,
    sample_rate: int = 16000,
    speech_s: float = 0.8,
    gap_s: float = 0.8,
    lead_s: float = 0.3,
) -> np.ndarray:
    """``n`` speech-like bursts separated by silence, with leading and trailing silence."""
    gap = np.zeros(int(gap_s * sample_rate), dtype=np.int16)
    lead = np.zeros(int(lead_s * sample_rate), dtype=np.int16)
    parts: list[np.ndarray] = [lead]
    for i in range(n):
        parts.append(synth_speechlike(speech_s, sample_rate, f0=120.0 + 25.0 * i))
        parts.append(gap)
    return np.concatenate(parts)


def write_fixture_wav(path: str | Path, n: int = 2, sample_rate: int = 16000) -> Path:
    """Generate a small (<100 KB at 8 kHz) synthetic test WAV and return its path."""
    p = Path(path)
    p.parent.mkdir(parents=True, exist_ok=True)
    write_wav(p, synth_utterances(n, sample_rate), sample_rate)
    return p


if __name__ == "__main__":  # python -m callgo_agent.audio_utils out.wav [rate]
    import sys

    out = Path(sys.argv[1] if len(sys.argv) > 1 else "tests/fixtures/speechlike.wav")
    rate = int(sys.argv[2]) if len(sys.argv) > 2 else 8000
    print(write_fixture_wav(out, 2, rate))
