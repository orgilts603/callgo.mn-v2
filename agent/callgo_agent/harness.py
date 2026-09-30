"""Offline pipeline harness: WAV -> STT -> LLM -> TTS -> WAV, with a latency report.

    python -m callgo_agent.harness --wav in.wav [--text "..."] [--profile profile.json] \
        [--out out.wav] [--fake]

* Without ``--fake`` the real factories are used (``pipeline.build_stt/build_tts``,
  ``llm_router.build_llm``); the LLM config comes from the ``llm`` key of ``--profile``
  (a Bootstrap JSON) or from the mock-backend env defaults.
* ``--fake`` needs no models or network: fake STT returns ``--text``, an echo LLM and a
  beep TTS stand in for the real stages.
* ``--text`` replaces the STT stage (fake STT returns it; in real mode STT is skipped).

Each stage is a plain coroutine (``run_stt``, ``run_llm``, ``run_tts``) so it can be
tested in isolation; :func:`run_pipeline` chains them.
"""

from __future__ import annotations

import argparse
import asyncio
import inspect
import json
import logging
import sys
import time
from collections.abc import Callable
from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Any

import numpy as np
from livekit import rtc
from livekit.agents import llm as lk_llm
from livekit.agents import stt as lk_stt
from livekit.agents import tts as lk_tts
from livekit.agents.types import (
    DEFAULT_API_CONNECT_OPTIONS,
    NOT_GIVEN,
    APIConnectOptions,
    NotGivenOr,
)
from livekit.agents.utils.audio import AudioBuffer

from .audio_utils import (
    duration_s,
    frames_from_pcm,
    pcm_from_frames,
    pcm_to_frame,
    read_wav,
    segment_utterances,
    synth_tone,
    synth_utterances,
    write_wav,
)
from .schemas import AgentProfile, LexiconEntry, LLMConfig

log = logging.getLogger("callgo.harness")

STT_RATE = 16000
DEFAULT_FAKE_TEXT = "Сайн байна уу"


# ---- results ---------------------------------------------------------------------


@dataclass
class Timings:
    """Per-stage latency in milliseconds (0.0 when a stage was skipped)."""

    stt_ms: float = 0.0
    llm_first_token_ms: float = 0.0
    llm_total_ms: float = 0.0
    tts_first_audio_ms: float = 0.0
    tts_total_ms: float = 0.0
    total_ms: float = 0.0

    def as_dict(self) -> dict[str, float]:
        return {k: round(v, 1) for k, v in asdict(self).items()}


@dataclass
class HarnessResult:
    transcript: str = ""  # after normalize_stt
    raw_transcript: str = ""  # STT output
    reply: str = ""  # LLM output
    tts_text: str = ""  # after normalize_for_tts
    audio: np.ndarray = field(default_factory=lambda: np.zeros(0, dtype=np.int16))
    sample_rate: int = 0
    utterances: int = 0
    timings: Timings = field(default_factory=Timings)

    def report(self) -> str:
        t = self.timings
        rows = [
            ("STT", t.stt_ms),
            ("LLM first token", t.llm_first_token_ms),
            ("LLM total", t.llm_total_ms),
            ("TTS first audio", t.tts_first_audio_ms),
            ("TTS total", t.tts_total_ms),
            ("TOTAL", t.total_ms),
        ]
        lines = [
            f"utterances : {self.utterances}",
            f"heard      : {self.raw_transcript!r}",
            f"normalized : {self.transcript!r}",
            f"reply      : {self.reply!r}",
            f"spoken     : {self.tts_text!r}",
            f"audio      : {duration_s(self.audio, self.sample_rate):.2f}s @ {self.sample_rate} Hz",
            "latency:",
        ]
        lines += [f"  {name:<16}{ms:>9.1f} ms" for name, ms in rows]
        return "\n".join(lines)


# ---- fakes (no models needed) ----------------------------------------------------


class FakeSTT(lk_stt.STT):
    """Returns ``text`` for the first utterance and ``""`` for any later one."""

    def __init__(self, text: str = DEFAULT_FAKE_TEXT, delay_s: float = 0.0) -> None:
        super().__init__(
            capabilities=lk_stt.STTCapabilities(streaming=False, interim_results=False)
        )
        self._pending: list[str] = [text]
        self._delay = delay_s

    @property
    def model(self) -> str:
        return "fake"

    @property
    def provider(self) -> str:
        return "callgo-harness"

    async def _recognize_impl(
        self,
        buffer: AudioBuffer,
        *,
        language: NotGivenOr[str] = NOT_GIVEN,
        conn_options: APIConnectOptions,
    ) -> lk_stt.SpeechEvent:
        if self._delay:
            await asyncio.sleep(self._delay)
        text = self._pending.pop(0) if self._pending else ""
        lang = language if isinstance(language, str) else "mn"
        return lk_stt.SpeechEvent(
            type=lk_stt.SpeechEventType.FINAL_TRANSCRIPT,
            alternatives=[lk_stt.SpeechData(language=lang, text=text, confidence=1.0)],
        )


def _last_user_text(chat_ctx: lk_llm.ChatContext) -> str:
    for item in reversed(chat_ctx.items):
        if item.type == "message" and item.role == "user":
            return item.text_content or ""
    return ""


class EchoLLM(lk_llm.LLM):
    """Answers ``Та хэлсэн: <user text>.`` in word-sized chunks."""

    def __init__(self, delay_s: float = 0.0) -> None:
        super().__init__()
        self._delay = delay_s

    @property
    def model(self) -> str:
        return "echo"

    @property
    def provider(self) -> str:
        return "callgo-harness"

    def chat(
        self,
        *,
        chat_ctx: lk_llm.ChatContext,
        tools: list[lk_llm.Tool] | None = None,
        conn_options: APIConnectOptions = DEFAULT_API_CONNECT_OPTIONS,
        parallel_tool_calls: NotGivenOr[bool] = NOT_GIVEN,
        tool_choice: NotGivenOr[lk_llm.ToolChoice] = NOT_GIVEN,
        extra_kwargs: NotGivenOr[dict[str, Any]] = NOT_GIVEN,
    ) -> lk_llm.LLMStream:
        return _EchoStream(self, chat_ctx=chat_ctx, tools=tools or [], conn_options=conn_options)


class _EchoStream(lk_llm.LLMStream):
    async def _run(self) -> None:
        assert isinstance(self._llm, EchoLLM)
        if self._llm._delay:
            await asyncio.sleep(self._llm._delay)
        reply = f"Та хэлсэн: {_last_user_text(self.chat_ctx)}."
        words = reply.split(" ")
        for i, w in enumerate(words):
            self._event_ch.send_nowait(
                lk_llm.ChatChunk(
                    id="echo",
                    delta=lk_llm.ChoiceDelta(
                        role="assistant", content=w + (" " if i < len(words) - 1 else "")
                    ),
                )
            )


class BeepTTS(lk_tts.TTS):
    """Emits a sine beep whose length scales with the text (60 ms/char, 0.3-4 s)."""

    def __init__(self, sample_rate: int = 22050, freq_hz: float = 440.0) -> None:
        super().__init__(
            capabilities=lk_tts.TTSCapabilities(streaming=False),
            sample_rate=sample_rate,
            num_channels=1,
        )
        self._freq = freq_hz

    @property
    def model(self) -> str:
        return "beep"

    @property
    def provider(self) -> str:
        return "callgo-harness"

    def synthesize(
        self, text: str, *, conn_options: APIConnectOptions = DEFAULT_API_CONNECT_OPTIONS
    ) -> lk_tts.ChunkedStream:
        return _BeepStream(tts=self, input_text=text, conn_options=conn_options)


class _BeepStream(lk_tts.ChunkedStream):
    async def _run(self, output_emitter: lk_tts.AudioEmitter) -> None:
        tts = self._tts
        assert isinstance(tts, BeepTTS)
        output_emitter.initialize(
            request_id="beep",
            sample_rate=tts.sample_rate,
            num_channels=1,
            mime_type="audio/pcm",
        )
        secs = min(4.0, max(0.3, 0.06 * len(self._input_text)))
        pcm = synth_tone(tts._freq, secs, tts.sample_rate)
        step = tts.sample_rate // 50
        for i in range(0, pcm.size, step):
            output_emitter.push(pcm[i : i + step].astype("<i2").tobytes())
            await asyncio.sleep(0)
        output_emitter.flush()


# ---- normalizer glue --------------------------------------------------------------


def _normalize(fn_name: str, text: str, lexicon: list[LexiconEntry] | None) -> str:
    """Call ``callgo_agent.normalizer.<fn_name>`` if present; identity otherwise."""
    try:
        from . import normalizer

        fn: Callable[..., Any] = getattr(normalizer, fn_name)
    except (ImportError, AttributeError):
        return text
    try:
        params = inspect.signature(fn).parameters
    except (TypeError, ValueError):
        params = {}  # type: ignore[assignment]
    if "lexicon" in params:
        out = fn(text, lexicon or [])
    else:
        out = fn(text)
    # normalize_stt returns (text, hits); normalize_for_tts returns text.
    if isinstance(out, tuple):
        out = out[0]
    return out if isinstance(out, str) else getattr(out, "text", str(out))


def normalize_stt_text(text: str, lexicon: list[LexiconEntry] | None = None) -> str:
    return _normalize("normalize_stt", text, lexicon)


def normalize_tts_text(text: str, lexicon: list[LexiconEntry] | None = None) -> str:
    return _normalize("normalize_for_tts", text, lexicon)


# ---- stages -----------------------------------------------------------------------


async def _recognize_via_stream(stt: lk_stt.STT, pcm: np.ndarray, language: str) -> str:
    texts: list[str] = []
    async with stt.stream(language=language) as stream:
        for frame in frames_from_pcm(pcm, STT_RATE):
            stream.push_frame(frame)
        stream.end_input()
        async for ev in stream:
            if ev.type == lk_stt.SpeechEventType.FINAL_TRANSCRIPT and ev.alternatives:
                texts.append(ev.alternatives[0].text)
    return " ".join(t.strip() for t in texts if t.strip())


async def run_stt(
    stt: lk_stt.STT,
    pcm: np.ndarray,
    sample_rate: int,
    language: str = "mn",
    *,
    threshold: float = 0.02,
    min_silence_ms: int = 400,
) -> tuple[str, int, float]:
    """Transcribe ``pcm`` utterance by utterance. Returns ``(text, n_utterances, ms)``."""
    if sample_rate != STT_RATE:
        from .audio_utils import resample_linear

        pcm = resample_linear(pcm, sample_rate, STT_RATE)
    segments = segment_utterances(pcm, STT_RATE, threshold, min_silence_ms)
    chunks = [s.slice(pcm) for s in segments] or ([pcm] if pcm.size else [])

    t0 = time.perf_counter()
    texts: list[str] = []
    for chunk in chunks:
        if stt.capabilities.offline_recognize:
            try:
                ev = await stt.recognize(pcm_to_frame(chunk, STT_RATE), language=language)
                text = ev.alternatives[0].text if ev.alternatives else ""
            except NotImplementedError:
                text = await _recognize_via_stream(stt, chunk, language)
        else:
            text = await _recognize_via_stream(stt, chunk, language)
        if text.strip():
            texts.append(text.strip())
    ms = (time.perf_counter() - t0) * 1000
    return " ".join(texts), len(chunks), ms


async def run_llm(llm: lk_llm.LLM, system_prompt: str, user_text: str) -> tuple[str, float, float]:
    """One chat turn via ``llm.chat`` streaming. Returns ``(reply, first_token_ms, total_ms)``."""
    ctx = lk_llm.ChatContext.empty()
    if system_prompt:
        ctx.add_message(role="system", content=system_prompt)
    ctx.add_message(role="user", content=user_text)

    t0 = time.perf_counter()
    first_ms = 0.0
    parts: list[str] = []
    async with llm.chat(chat_ctx=ctx) as stream:
        async for chunk in stream:
            if chunk.delta and chunk.delta.content:
                if not parts:
                    first_ms = (time.perf_counter() - t0) * 1000
                parts.append(chunk.delta.content)
    return "".join(parts).strip(), first_ms, (time.perf_counter() - t0) * 1000


async def run_tts(tts: lk_tts.TTS, text: str) -> tuple[np.ndarray, int, float, float]:
    """Synthesize ``text``. Returns ``(pcm, sample_rate, first_audio_ms, total_ms)``."""
    t0 = time.perf_counter()
    first_ms = 0.0
    frames: list[rtc.AudioFrame] = []
    async with tts.synthesize(text) as stream:
        async for ev in stream:
            if not frames:
                first_ms = (time.perf_counter() - t0) * 1000
            frames.append(ev.frame)
    total = (time.perf_counter() - t0) * 1000
    rate = frames[0].sample_rate if frames else tts.sample_rate
    return pcm_from_frames(frames), rate, first_ms, total


async def run_pipeline(
    *,
    stt: lk_stt.STT | None,
    llm: lk_llm.LLM,
    tts: lk_tts.TTS,
    pcm: np.ndarray | None,
    sample_rate: int,
    text: str | None = None,
    system_prompt: str = "",
    language: str = "mn",
    lexicon: list[LexiconEntry] | None = None,
) -> HarnessResult:
    """STT (or ``text``) -> normalize_stt -> LLM -> normalize_for_tts -> TTS."""
    res = HarnessResult()
    t_start = time.perf_counter()

    if text is not None:
        res.raw_transcript = text
        res.utterances = 1
    else:
        if stt is None or pcm is None:
            raise ValueError("need either `text` or both `stt` and `pcm`")
        res.raw_transcript, res.utterances, res.timings.stt_ms = await run_stt(
            stt, pcm, sample_rate, language
        )

    res.transcript = normalize_stt_text(res.raw_transcript, lexicon)
    if not res.transcript.strip():
        res.timings.total_ms = (time.perf_counter() - t_start) * 1000
        return res

    res.reply, res.timings.llm_first_token_ms, res.timings.llm_total_ms = await run_llm(
        llm, system_prompt, res.transcript
    )
    res.tts_text = normalize_tts_text(res.reply, lexicon)
    if res.tts_text.strip():
        (
            res.audio,
            res.sample_rate,
            res.timings.tts_first_audio_ms,
            res.timings.tts_total_ms,
        ) = await run_tts(tts, res.tts_text)
    res.timings.total_ms = (time.perf_counter() - t_start) * 1000
    return res


# ---- CLI --------------------------------------------------------------------------


@dataclass
class Setup:
    stt: lk_stt.STT | None
    llm: lk_llm.LLM
    tts: lk_tts.TTS
    profile: AgentProfile
    lexicon: list[LexiconEntry]


def load_profile(
    path: str | None,
) -> tuple[AgentProfile, LLMConfig | None, list[LLMConfig], list[LexiconEntry]]:
    """Load an AgentProfile or Bootstrap JSON; without a path use the mock backend's demo."""
    from .mock_backend import build_bootstrap, llm_from_env

    if path is None:
        b = build_bootstrap({})
        return b.profile, llm_from_env(), [], b.lexicon
    data = json.loads(Path(path).read_text(encoding="utf-8"))
    if "profile" in data:
        llm = LLMConfig.model_validate(data["llm"]) if data.get("llm") else llm_from_env()
        fallbacks = [LLMConfig.model_validate(x) for x in data.get("llmFallbacks", [])]
        lexicon = [LexiconEntry.model_validate(x) for x in data.get("lexicon", [])]
        return AgentProfile.model_validate(data["profile"]), llm, fallbacks, lexicon
    return AgentProfile.model_validate(data), llm_from_env(), [], []


def build_setup(args: argparse.Namespace) -> Setup:
    profile, llm_cfg, fallbacks, lexicon = load_profile(args.profile)
    if args.fake:
        return Setup(
            stt=FakeSTT(args.text or DEFAULT_FAKE_TEXT),
            llm=EchoLLM(),
            tts=BeepTTS(),
            profile=profile,
            lexicon=lexicon,
        )
    from . import llm_router, pipeline  # lazy: heavy, owned by other modules

    stt = None if args.text is not None else pipeline.build_stt(profile)
    return Setup(
        stt=stt,
        llm=llm_router.build_llm(llm_cfg, fallbacks),
        tts=pipeline.build_tts(profile),
        profile=profile,
        lexicon=lexicon,
    )


async def run(args: argparse.Namespace) -> HarnessResult:
    setup = build_setup(args)
    pcm: np.ndarray | None = None
    sr = STT_RATE
    if args.wav:
        pcm, sr = read_wav(args.wav, STT_RATE)
        sr = STT_RATE
    elif args.fake:
        pcm = synth_utterances(1, STT_RATE)  # something for the fake STT to "hear"
    elif args.text is None:
        raise SystemExit("error: provide --wav and/or --text")
    text = args.text if (args.text is not None and not args.fake) else None
    try:
        res = await run_pipeline(
            stt=setup.stt,
            llm=setup.llm,
            tts=setup.tts,
            pcm=pcm,
            sample_rate=sr,
            text=text,
            system_prompt=setup.profile.system_prompt,
            language=setup.profile.language,
            lexicon=setup.lexicon,
        )
    finally:
        for comp in (setup.stt, setup.llm, setup.tts):
            if comp is not None:
                await comp.aclose()
    if res.audio.size:
        write_wav(args.out, res.audio, res.sample_rate)
    return res


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    ap = argparse.ArgumentParser(
        prog="python -m callgo_agent.harness", description="Offline STT->LLM->TTS latency harness"
    )
    ap.add_argument("--wav", help="input WAV (any rate/channels; resampled to 16 kHz mono)")
    ap.add_argument("--text", help="skip STT and use this text (fake STT returns it with --fake)")
    ap.add_argument("--profile", help="AgentProfile or Bootstrap JSON (default: mock demo profile)")
    ap.add_argument("--out", default="out.wav", help="output WAV path (default out.wav)")
    ap.add_argument("--fake", action="store_true", help="fake STT/LLM/TTS; no models or network")
    ap.add_argument("--json", action="store_true", help="print the timings as JSON only")
    return ap.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    logging.basicConfig(level=logging.WARNING)
    res = asyncio.run(run(args))
    if args.json:
        print(
            json.dumps({"reply": res.reply, "timings": res.timings.as_dict()}, ensure_ascii=False)
        )
    else:
        print(res.report())
        if res.audio.size:
            print(f"wrote {args.out}")
        else:
            print("no audio produced (empty transcript or reply)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
