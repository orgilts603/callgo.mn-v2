"""One CallGo phone call: agent persona, pipeline hooks, live events and post-call analysis.

* :class:`CallGoAgent` — ``Agent`` whose instructions come from the profile, campaign script
  (``{{var}}`` substitution) and contact; greets on enter; routes STT output through
  ``normalizer.normalize_stt`` and TTS input through ``normalizer.normalize_for_tts``.
* :class:`CallRecorder` — turns ``AgentSession`` events into ``transcript.*`` /
  ``agent.state`` events and keeps the transcript for the post-call analysis.
* :func:`analyze_call` — one LLM completion producing ``{summary, sentiment, intent}``.
* :func:`run_call` — the job entrypoint body used by :mod:`callgo_agent.worker`.
"""

from __future__ import annotations

import asyncio
import contextlib
import dataclasses
import importlib
import json
import logging
import os
import re
import time
from collections import deque
from collections.abc import AsyncGenerator, AsyncIterable, Callable, Iterable, Mapping, Sequence
from dataclasses import dataclass, field
from datetime import datetime
from typing import TYPE_CHECKING, Any, Protocol, cast
from uuid import UUID
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

from livekit import rtc
from livekit.agents import (
    Agent,
    AgentSession,
    AgentStateChangedEvent,
    CloseEvent,
    CloseReason,
    ConversationItemAddedEvent,
    JobContext,
    ModelSettings,
    TurnHandlingOptions,
    UserInputTranscribedEvent,
    UserStateChangedEvent,
    llm,
    room_io,
    stt,
)
from pydantic import ValidationError

from .config import settings
from .events import EventEmitter, utcnow
from .schemas import (
    AgentProfile,
    Bootstrap,
    CallDirection,
    CallEndedPayload,
    CallStatus,
    EndReason,
    JobMetadata,
    LexiconEntry,
    LexiconScope,
    LLMConfig,
    Sentiment,
    Speaker,
    TranscriptTurn,
)
from .tools import (
    CallbackRequest,
    CallControl,
    CallState,
    LiveKitCallControl,
    build_tools,
    contact_summary,
    language_name,
)

if TYPE_CHECKING:
    from livekit.agents import tts, vad
    from livekit.agents.voice.turn import TurnDetectionMode

    from .backend_client import BackendClient

log = logging.getLogger("callgo.session")

SIP_PHONE_NUMBER = "sip.phoneNumber"
SIP_TRUNK_PHONE_NUMBER = "sip.trunkPhoneNumber"
SIP_CALL_ID = "sip.callID"
SIP_CALL_STATUS = "sip.callStatus"

PARTICIPANT_TIMEOUT_SEC = 60.0
ANSWER_TIMEOUT_SEC = 90.0
ANALYSIS_TIMEOUT_SEC = 25.0
LOCAL_TZ = "Asia/Ulaanbaatar"
TURN_DETECTOR_ENV = "CALLGO_TURN_DETECTOR"  # "vad" (default) | "multilingual"

MAX_DURATION_MESSAGES = {
    "mn": "Уучлаарай, ярианы хугацаа дууслаа. Бидэнтэй холбогдсонд баярлалаа. Баяртай.",
    "en": "Sorry, we have reached the maximum call time. Thank you for calling. Goodbye.",
}
AWAY_PROMPT = (
    "The customer has been silent for a while. Politely ask, in one short sentence, "
    "whether they are still on the line."
)


# ---- normalizer adapters (module owned by another agent; imported lazily) ----------------

_warned_missing: set[str] = set()


def _normalizer() -> Any | None:
    try:
        return importlib.import_module("callgo_agent.normalizer")
    except ImportError as exc:
        if "normalizer" not in _warned_missing:
            _warned_missing.add("normalizer")
            log.warning("callgo_agent.normalizer unavailable (%s); text passes through", exc)
        return None


def lexicon_for(lexicon: Iterable[LexiconEntry], scope: LexiconScope) -> list[LexiconEntry]:
    return [e for e in lexicon if e.scope in (scope, LexiconScope.BOTH)]


def normalize_stt_text(text: str, lexicon: list[LexiconEntry]) -> tuple[str, list[LexiconEntry]]:
    """``normalizer.normalize_stt`` with a pass-through fallback. Never raises."""
    mod = _normalizer()
    if mod is None:
        return text, []
    try:
        corrected, hits = mod.normalize_stt(text, lexicon)
        return str(corrected), list(hits)
    except Exception:
        log.exception("normalize_stt failed; using raw STT text")
        return text, []


def normalize_tts_text(text: str, lexicon: list[LexiconEntry]) -> str:
    """``normalizer.normalize_for_tts`` applied to the stripped core; keeps edge whitespace."""
    core = text.strip()
    if not core:
        return text
    mod = _normalizer()
    if mod is None:
        return text
    try:
        out = str(mod.normalize_for_tts(core, lexicon))
    except Exception:
        log.exception("normalize_for_tts failed; speaking the raw text")
        return text
    lead = text[: len(text) - len(text.lstrip())]
    trail = text[len(text.rstrip()) :]
    return f"{lead}{out}{trail}"


# ---- prompt building ------------------------------------------------------------------

_VAR_RE = re.compile(r"\{\{\s*([\w.\-]+)\s*\}\}")


def substitute_vars(template: str, variables: Mapping[str, str]) -> str:
    """Replace ``{{name}}`` placeholders; unknown names become empty strings."""

    def repl(m: re.Match[str]) -> str:
        key = m.group(1)
        if key in variables:
            return str(variables[key])
        log.debug("template variable %r not provided", key)
        return ""

    return _VAR_RE.sub(repl, template)


def template_vars(bootstrap: Bootstrap) -> dict[str, str]:
    """Variables for ``{{...}}``: built-ins < campaign vars < contact meta."""
    call = bootstrap.call
    customer_phone = call.from_number if call.direction == CallDirection.INBOUND else call.to_number
    contact = bootstrap.contact
    out: dict[str, str] = {
        "org": bootstrap.org.name,
        "org_name": bootstrap.org.name,
        "agent_name": bootstrap.profile.name,
        "phone": contact.phone if contact else customer_phone,
        "name": contact.name if contact else "",
        "contact_name": contact.name if contact else "",
        "contact_phone": contact.phone if contact else customer_phone,
    }
    if bootstrap.campaign is not None:
        out["campaign"] = bootstrap.campaign.name
        out["campaign_name"] = bootstrap.campaign.name
        out.update(bootstrap.campaign.vars)
    if contact is not None:
        out.update(contact.meta)
    return out


def language_rule(language: str) -> str:
    if not language or language.lower().startswith("mn"):
        return (
            "Always answer in Mongolian (Cyrillic script), even if the customer mixes in "
            "other languages."
        )
    return f"Always answer in {language_name(language)}."


def _today(tz: str = LOCAL_TZ) -> str:
    try:
        now = datetime.now(ZoneInfo(tz))
    except (ZoneInfoNotFoundError, ValueError):
        now = utcnow()
    return now.strftime("%Y-%m-%d %A %H:%M")


def build_instructions(bootstrap: Bootstrap, *, now: str | None = None) -> str:
    profile = bootstrap.profile
    variables = template_vars(bootstrap)
    sections: list[str] = []
    if profile.system_prompt.strip():
        sections.append(substitute_vars(profile.system_prompt, variables).strip())
    else:
        sections.append(
            f"You are {profile.name or 'a helpful assistant'}, a voice assistant answering "
            f"phone calls for {bootstrap.org.name}."
        )

    campaign = bootstrap.campaign
    if campaign is not None and campaign.script.strip():
        sections.append(
            "# Call script\nFollow this script, adapting naturally to the customer:\n"
            + substitute_vars(campaign.script, variables).strip()
        )

    direction = bootstrap.call.direction
    who = (
        "The customer called you."
        if direction == CallDirection.INBOUND
        else ("You are calling the customer (outbound call).")
    )
    sections.append("# Customer\n" + who + "\n" + contact_summary(bootstrap))

    sections.append(
        "# Style\n"
        "This is a live phone call. Keep every reply short (one or two sentences), warm and "
        "natural. Never use markdown, lists, emojis or special symbols. Say numbers, dates "
        "and prices the way people speak them. Ask one question at a time. If you did not "
        "understand the customer, ask them to repeat.\n"
        f"Current local time: {now or _today()}."
    )
    sections.append("# Language\n" + language_rule(profile.language))
    return "\n\n".join(sections)


def render_greeting(bootstrap: Bootstrap) -> str:
    return substitute_vars(bootstrap.profile.greeting, template_vars(bootstrap)).strip()


def outbound_opening_instructions(bootstrap: Bootstrap) -> str:
    greeting = render_greeting(bootstrap)
    text = (
        "The customer has just answered your outbound call. Greet them, introduce yourself "
        "and the reason for the call following the script, then ask your first question."
    )
    if greeting:
        text += f' Start with: "{greeting}"'
    return text


# ---- SIP / job metadata -----------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class SipInfo:
    identity: str = ""
    phone_number: str = ""
    trunk_phone_number: str = ""
    call_id: str = ""
    call_status: str = ""

    @classmethod
    def from_attributes(cls, identity: str, attrs: Mapping[str, str]) -> SipInfo:
        return cls(
            identity=identity,
            phone_number=attrs.get(SIP_PHONE_NUMBER, ""),
            trunk_phone_number=attrs.get(SIP_TRUNK_PHONE_NUMBER, ""),
            call_id=attrs.get(SIP_CALL_ID, ""),
            call_status=attrs.get(SIP_CALL_STATUS, ""),
        )


def parse_job_metadata(raw: str | None) -> JobMetadata:
    """Parse the dispatch metadata JSON; empty or invalid metadata yields defaults."""
    if not raw or not raw.strip():
        return JobMetadata()
    try:
        data = json.loads(raw)
    except ValueError as exc:
        log.warning("ignoring non-JSON job metadata %r: %s", raw[:200], exc)
        return JobMetadata()
    if not isinstance(data, dict):
        log.warning("ignoring job metadata that is not a JSON object: %r", raw[:200])
        return JobMetadata()
    try:
        return JobMetadata.model_validate(data)
    except ValidationError as exc:
        log.warning("ignoring invalid job metadata %r: %s", raw[:200], exc)
        return JobMetadata()


def resolve_numbers(meta: JobMetadata, sip: SipInfo) -> tuple[str, str]:
    """(from, to) in E.164. Inbound: caller -> our DID; outbound: our DID -> callee."""
    if meta.direction == CallDirection.INBOUND:
        return meta.from_number or sip.phone_number, meta.to_number or sip.trunk_phone_number
    return meta.from_number or sip.trunk_phone_number, meta.to_number or sip.phone_number


# ---- the agent --------------------------------------------------------------------------

SttFinalCallback = Callable[[str, str, float, list[LexiconEntry]], None]

_SENTENCE_END = re.compile(r"[.!?…;:\n]+[\"'»”)\]]*\s+")
_TRANSCRIPT_TYPES = frozenset(
    {
        stt.SpeechEventType.INTERIM_TRANSCRIPT,
        stt.SpeechEventType.PREFLIGHT_TRANSCRIPT,
        stt.SpeechEventType.FINAL_TRANSCRIPT,
    }
)


def _find_cut(buf: str, max_chars: int) -> int:
    last = 0
    for m in _SENTENCE_END.finditer(buf):
        last = m.end()
    if last:
        return last
    if len(buf) > max_chars:
        ws = buf.rfind(" ", 0, max_chars)
        return ws + 1 if ws > 0 else len(buf)
    return 0


async def sentence_chunks(
    text: AsyncIterable[Any], transform: Callable[[str], str], *, max_chars: int = 240
) -> AsyncGenerator[Any, None]:
    """Re-chunk an LLM token stream into sentences and apply ``transform`` to each.

    Non-string items (e.g. flush sentinels) flush the buffer and pass through unchanged.
    """
    buf = ""
    async for chunk in text:
        if not isinstance(chunk, str):
            if buf:
                yield transform(buf)
                buf = ""
            yield chunk
            continue
        buf += chunk
        while (cut := _find_cut(buf, max_chars)) > 0:
            yield transform(buf[:cut])
            buf = buf[cut:]
    if buf:
        yield transform(buf)


class CallGoAgent(Agent):
    """The voice persona for one call."""

    def __init__(
        self,
        *,
        bootstrap: Bootstrap,
        tools: Sequence[llm.Tool] = (),
        on_stt_final: SttFinalCallback | None = None,
        instructions: str | None = None,
    ) -> None:
        super().__init__(
            instructions=instructions or build_instructions(bootstrap),
            tools=list(tools),
        )
        self.bootstrap = bootstrap
        self._stt_lexicon = lexicon_for(bootstrap.lexicon, LexiconScope.STT)
        self._tts_lexicon = lexicon_for(bootstrap.lexicon, LexiconScope.TTS)
        self._on_stt_final = on_stt_final

    @property
    def is_outbound(self) -> bool:
        return self.bootstrap.call.direction == CallDirection.OUTBOUND

    async def on_enter(self) -> None:
        if self.is_outbound:
            self.session.generate_reply(instructions=outbound_opening_instructions(self.bootstrap))
            return
        greeting = render_greeting(self.bootstrap)
        if greeting:
            self.session.say(greeting)
        else:
            self.session.generate_reply(
                instructions="Greet the caller briefly and ask how you can help."
            )

    # -- STT hook --

    def process_speech_event(self, ev: stt.SpeechEvent | str) -> stt.SpeechEvent | str:
        """Apply ``normalize_stt`` to transcript events; report finals (raw text + hits)."""
        if (
            not isinstance(ev, stt.SpeechEvent)
            or ev.type not in _TRANSCRIPT_TYPES
            or not ev.alternatives
        ):
            return ev
        alt = ev.alternatives[0]
        raw = alt.text
        if not raw.strip():
            return ev
        corrected, hits = normalize_stt_text(raw, self._stt_lexicon)
        if ev.type == stt.SpeechEventType.FINAL_TRANSCRIPT and self._on_stt_final is not None:
            try:
                self._on_stt_final(corrected, raw, alt.confidence, hits)
            except Exception:
                log.exception("on_stt_final callback failed")
        if corrected == raw:
            return ev
        new_alt = dataclasses.replace(alt, text=corrected)
        return dataclasses.replace(ev, alternatives=[new_alt, *ev.alternatives[1:]])

    async def stt_node(  # type: ignore[override]
        self, audio: AsyncIterable[rtc.AudioFrame], model_settings: ModelSettings
    ) -> AsyncGenerator[stt.SpeechEvent | str, None]:
        async for ev in Agent.default.stt_node(self, audio, model_settings):
            yield self.process_speech_event(ev)

    # -- TTS hook --

    def normalize_for_tts(self, text: str) -> str:
        return normalize_tts_text(text, self._tts_lexicon)

    async def tts_node(  # type: ignore[override]
        self, text: AsyncIterable[str], model_settings: ModelSettings
    ) -> AsyncGenerator[rtc.AudioFrame, None]:
        normalized = sentence_chunks(text, self.normalize_for_tts)
        async for frame in Agent.default.tts_node(self, normalized, model_settings):
            yield frame


# ---- live events --------------------------------------------------------------------------


class LexiconHitSink(Protocol):
    async def lexicon_hit(self, ids: Iterable[UUID | str]) -> None: ...


class CallRecorder:
    """Maps AgentSession events to live events and records the final transcript."""

    def __init__(
        self,
        emitter: EventEmitter,
        bootstrap: Bootstrap,
        *,
        llm_label: str = "",
        hit_sink: LexiconHitSink | None = None,
        clock: Callable[[], float] = time.time,
    ) -> None:
        self.emitter = emitter
        self.bootstrap = bootstrap
        self.llm_label = llm_label
        self._hit_sink = hit_sink
        self._clock = clock
        self.answered_at: float | None = None
        self.turns: list[TranscriptTurn] = []
        self._seq = 0
        self._user_start_ms: int | None = None
        self._raw: deque[tuple[str, str, float]] = deque(maxlen=64)
        self._last_state: str | None = None
        self._away_prompted = False
        self._session: AgentSession[Any] | None = None
        self._tasks: set[asyncio.Task[None]] = set()

    # -- timing --

    def mark_answered(self, at: float | None = None) -> None:
        if self.answered_at is None:
            self.answered_at = self._clock() if at is None else at

    def ms(self, at: float | None = None) -> int:
        if self.answered_at is None:
            return 0
        t = self._clock() if at is None else at
        return max(0, int((t - self.answered_at) * 1000))

    def duration_sec(self) -> int:
        if self.answered_at is None:
            return 0
        return max(0, round(self._clock() - self.answered_at))

    # -- wiring --

    def attach(self, session: AgentSession[Any]) -> None:
        self._session = session
        session.on("user_input_transcribed", self.on_user_input_transcribed)
        session.on("conversation_item_added", self.on_conversation_item_added)
        session.on("agent_state_changed", self.on_agent_state_changed)
        session.on("user_state_changed", self.on_user_state_changed)

    def _next_seq(self) -> int:
        self._seq += 1
        return self._seq

    def _add_turn(self, turn: TranscriptTurn) -> None:
        self.turns.append(turn)
        self.emitter.transcript_final(turn)

    # -- STT side-channel (from CallGoAgent.stt_node) --

    def on_stt_final(
        self, text: str, raw: str, confidence: float, hits: list[LexiconEntry]
    ) -> None:
        self._raw.append((text, raw, confidence))
        ids = [str(h.id) for h in hits if h.id is not None]
        if ids and self._hit_sink is not None:
            self._spawn(self._post_hits(ids))

    async def _post_hits(self, ids: list[str]) -> None:
        assert self._hit_sink is not None
        try:
            await self._hit_sink.lexicon_hit(ids)
        except Exception as exc:  # noqa: BLE001 - best effort counter
            log.warning("lexicon-hit report failed: %s", exc)

    def _spawn(self, coro: Any) -> None:
        try:
            task = asyncio.get_running_loop().create_task(coro)
        except RuntimeError:
            coro.close()
            return
        self._tasks.add(task)
        task.add_done_callback(self._tasks.discard)

    async def drain(self, timeout: float = 5.0) -> None:
        if self._tasks:
            await asyncio.wait(set(self._tasks), timeout=timeout)

    def _pop_raw(self, text: str) -> tuple[str, float]:
        for i, (norm, raw, conf) in enumerate(self._raw):
            if norm.strip() == text.strip():
                del self._raw[i]
                return raw, conf
        return "", 0.0

    # -- session events --

    def on_user_input_transcribed(self, ev: UserInputTranscribedEvent) -> None:
        text = ev.transcript.strip()
        if not text:
            return
        if self._user_start_ms is None:
            self._user_start_ms = self.ms(ev.created_at)
        if not ev.is_final:
            self.emitter.transcript_partial(Speaker.CUSTOMER, text, self._user_start_ms)
            return
        raw, confidence = self._pop_raw(text)
        turn = TranscriptTurn(
            call_id=self.bootstrap.call.id,
            seq=self._next_seq(),
            speaker=Speaker.CUSTOMER,
            text=text,
            raw_text=raw or text,
            confidence=confidence,
            start_ms=self._user_start_ms,
            end_ms=self.ms(ev.created_at),
            is_final=True,
        )
        self._user_start_ms = None
        self._add_turn(turn)

    def on_conversation_item_added(self, ev: ConversationItemAddedEvent) -> None:
        item = ev.item
        if not isinstance(item, llm.ChatMessage) or item.role != "assistant":
            return
        text = (item.text_content or "").strip()
        if not text:
            return
        started = item.metrics.get("started_speaking_at") or item.created_at
        stopped = item.metrics.get("stopped_speaking_at") or ev.created_at
        self._add_turn(
            TranscriptTurn(
                call_id=self.bootstrap.call.id,
                seq=self._next_seq(),
                speaker=Speaker.AGENT,
                text=text,
                raw_text=text,
                confidence=1.0,
                start_ms=self.ms(started),
                end_ms=self.ms(stopped),
                is_final=True,
            )
        )

    def on_agent_state_changed(self, ev: AgentStateChangedEvent) -> None:
        if ev.new_state == self._last_state:
            return
        self._last_state = ev.new_state
        self.emitter.agent_state(ev.new_state, self.llm_label)

    def on_user_state_changed(self, ev: UserStateChangedEvent) -> None:
        if ev.new_state == "speaking":
            if self._user_start_ms is None:
                self._user_start_ms = self.ms(ev.created_at)
            self._away_prompted = False
        elif ev.new_state == "away" and not self._away_prompted and self._session is not None:
            self._away_prompted = True
            try:
                self._session.generate_reply(instructions=AWAY_PROMPT)
            except RuntimeError as exc:
                log.debug("cannot prompt away user: %s", exc)

    def transcript_text(self) -> str:
        label = {Speaker.CUSTOMER: "Customer", Speaker.AGENT: "Agent", Speaker.HUMAN: "Operator"}
        return "\n".join(f"{label[t.speaker]}: {t.text}" for t in self.turns)


# ---- post-call analysis -----------------------------------------------------------------

ANALYSIS_PROMPT = """You analyse a finished phone call between a customer and a voice agent.
Reply with ONLY one JSON object and nothing else:
{"summary": "...", "sentiment": "positive|neutral|negative", "intent": "..."}
- summary: 2-3 sentences in Mongolian (Cyrillic) saying what the customer wanted and the outcome.
- sentiment: the customer's overall sentiment: positive, neutral or negative.
- intent: the customer's main intent as a short English snake_case label, for example
  "order_status", "product_inquiry", "complaint", "callback_request", "not_interested"."""


@dataclass(slots=True)
class CallAnalysis:
    summary: str = ""
    sentiment: Sentiment = Sentiment.NEUTRAL
    intent: str = ""
    from_llm: bool = False


_FENCE_RE = re.compile(r"^```[a-zA-Z]*\s*|\s*```$")


def parse_analysis(text: str) -> CallAnalysis | None:
    """Extract ``{summary, sentiment, intent}`` from an LLM reply; ``None`` if unusable."""
    cleaned = _FENCE_RE.sub("", text.strip())
    start, end = cleaned.find("{"), cleaned.rfind("}")
    if start < 0 or end <= start:
        return None
    try:
        data = json.loads(cleaned[start : end + 1])
    except ValueError:
        return None
    if not isinstance(data, dict):
        return None
    summary = str(data.get("summary") or "").strip()
    if not summary:
        return None
    raw_sentiment = str(data.get("sentiment") or "").strip().lower()
    try:
        sentiment = Sentiment(raw_sentiment)
    except ValueError:
        sentiment = Sentiment.NEUTRAL
    intent = re.sub(r"\s+", "_", str(data.get("intent") or "").strip().lower())[:64]
    return CallAnalysis(summary=summary, sentiment=sentiment, intent=intent, from_llm=True)


def fallback_analysis(turns: Sequence[TranscriptTurn]) -> CallAnalysis:
    customer = [t.text for t in turns if t.speaker == Speaker.CUSTOMER]
    if not turns:
        return CallAnalysis(summary="Харилцан яриа бүртгэгдээгүй.")
    if not customer:
        return CallAnalysis(summary="Харилцагч ярианд оролцоогүй.")
    last = customer[-1]
    if len(last) > 160:
        last = last[:157] + "..."
    return CallAnalysis(
        summary=(
            f"Автомат хураангуй гаргах боломжгүй байлаа. Харилцагч {len(customer)} удаа "
            f"ярьсан; сүүлд: «{last}»."
        )
    )


def callbacks_note(callbacks: Sequence[CallbackRequest]) -> str:
    if not callbacks:
        return ""
    return "Буцаж залгах хүсэлт: " + "; ".join(c.describe() for c in callbacks) + "."


async def _complete(model: llm.LLM, chat_ctx: llm.ChatContext) -> str:
    parts: list[str] = []
    async with model.chat(chat_ctx=chat_ctx) as stream:
        async for chunk in stream:
            if chunk.delta is not None and chunk.delta.content:
                parts.append(chunk.delta.content)
    return "".join(parts)


async def analyze_call(
    model: llm.LLM | None,
    turns: Sequence[TranscriptTurn],
    *,
    end_reason: str = "",
    callbacks: Sequence[CallbackRequest] = (),
    timeout: float = ANALYSIS_TIMEOUT_SEC,
) -> CallAnalysis:
    """One chat completion over the transcript; falls back safely on any failure."""
    if model is None or not any(t.speaker == Speaker.CUSTOMER for t in turns):
        return fallback_analysis(turns)
    label = {Speaker.CUSTOMER: "Customer", Speaker.AGENT: "Agent", Speaker.HUMAN: "Operator"}
    transcript = "\n".join(f"{label[t.speaker]}: {t.text}" for t in turns)
    extra = []
    if end_reason:
        extra.append(f"Call end reason: {end_reason}")
    if callbacks:
        extra.append("Callback requested: " + "; ".join(c.describe() for c in callbacks))
    chat_ctx = llm.ChatContext.empty()
    chat_ctx.add_message(role="system", content=ANALYSIS_PROMPT)
    chat_ctx.add_message(
        role="user",
        content="Transcript:\n" + transcript + ("\n\n" + "\n".join(extra) if extra else ""),
    )
    try:
        reply = await asyncio.wait_for(_complete(model, chat_ctx), timeout=timeout)
    except Exception as exc:  # noqa: BLE001 - analysis must never break call teardown
        log.warning("post-call analysis failed: %r", exc)
        return fallback_analysis(turns)
    parsed = parse_analysis(reply)
    if parsed is None:
        log.warning("post-call analysis returned unparsable output: %r", reply[:300])
        return fallback_analysis(turns)
    return parsed


# ---- pipeline factories (modules owned by other agents; imported lazily) ------------------


def turn_detector_choice() -> str:
    return os.environ.get(TURN_DETECTOR_ENV, "vad").strip().lower() or "vad"


def _default_turn_detection() -> TurnDetectionMode:
    if turn_detector_choice() == "multilingual":
        try:
            from livekit.plugins.turn_detector.multilingual import MultilingualModel

            return MultilingualModel()
        except Exception as exc:  # noqa: BLE001 - fall back to VAD endpointing
            log.warning("multilingual turn detector unavailable (%s); using VAD", exc)
    return "vad"


def _default_build_stt(profile: AgentProfile) -> stt.STT:
    from . import pipeline  # type: ignore[attr-defined]

    return cast("stt.STT", pipeline.build_stt(profile))


def _default_build_tts(profile: AgentProfile) -> tts.TTS:
    from . import pipeline  # type: ignore[attr-defined]

    return cast("tts.TTS", pipeline.build_tts(profile))


def _default_build_llm(config: LLMConfig, fallbacks: list[LLMConfig]) -> llm.LLM:
    from . import llm_router

    return llm_router.build_llm(config, fallbacks)


def _default_describe_llm(config: LLMConfig) -> str:
    try:
        from . import llm_router

        return str(llm_router.describe(config))
    except Exception:  # noqa: BLE001
        return f"{config.provider.value}/{config.model}"


def _default_load_vad() -> vad.VAD:
    from livekit.plugins import silero

    return load_telephony_vad(silero)


def load_telephony_vad(silero_mod: Any) -> vad.VAD:
    return cast(
        "vad.VAD",
        silero_mod.VAD.load(min_silence_duration=0.45, prefix_padding_duration=0.4),
    )


@dataclass
class PipelineFactories:
    build_stt: Callable[[AgentProfile], stt.STT] = _default_build_stt
    build_tts: Callable[[AgentProfile], tts.TTS] = _default_build_tts
    build_llm: Callable[[LLMConfig, list[LLMConfig]], llm.LLM] = _default_build_llm
    describe_llm: Callable[[LLMConfig], str] = _default_describe_llm
    turn_detection: Callable[[], TurnDetectionMode] = _default_turn_detection
    load_vad: Callable[[], vad.VAD] = _default_load_vad


def build_turn_handling(turn_detection: TurnDetectionMode) -> TurnHandlingOptions:
    """Telephony tuning: a bit more patience at end of turn, VAD-based barge-in."""
    return {
        "turn_detection": turn_detection,
        "endpointing": {"min_delay": 0.6, "max_delay": 3.0},
        "interruption": {
            "enabled": True,
            "mode": "vad",
            "min_duration": 0.6,
            "resume_false_interruption": True,
            "false_interruption_timeout": 1.5,
        },
    }


def select_llm_chain(bootstrap: Bootstrap) -> tuple[LLMConfig, list[LLMConfig]] | None:
    if bootstrap.llm is not None:
        return bootstrap.llm, list(bootstrap.llm_fallbacks)
    if bootstrap.llm_fallbacks:
        return bootstrap.llm_fallbacks[0], list(bootstrap.llm_fallbacks[1:])
    return None


def end_reason_for_close(reason: CloseReason | str | None) -> EndReason:
    if reason is not None and str(getattr(reason, "value", reason)) == CloseReason.ERROR.value:
        return "failed"
    return "hangup_customer"


# ---- call runner --------------------------------------------------------------------------


@dataclass
class CallFinalizer:
    """Idempotent end-of-call work: hang up, analyse, emit ``call.ended``, flush."""

    state: CallState
    recorder: CallRecorder
    emitter: EventEmitter
    control: CallControl | None = None
    model: llm.LLM | None = None
    llm_label: str = ""
    analysis_timeout: float = ANALYSIS_TIMEOUT_SEC
    payload: CallEndedPayload | None = None
    _lock: asyncio.Lock = field(default_factory=asyncio.Lock)

    async def finalize(self, close_reason: CloseReason | str | None = None) -> CallEndedPayload:
        async with self._lock:
            if self.payload is not None:
                return self.payload
            end_reason = self.state.end_reason or end_reason_for_close(close_reason)
            self.state.set_end_reason(end_reason)
            duration = self.recorder.duration_sec()
            if self.control is not None:
                try:
                    await self.control.hangup()
                except Exception as exc:  # noqa: BLE001
                    log.warning("hangup failed: %s", exc)
            analysis = await analyze_call(
                self.model,
                self.recorder.turns,
                end_reason=end_reason,
                callbacks=self.state.callbacks,
                timeout=self.analysis_timeout,
            )
            summary = " ".join(
                s for s in (analysis.summary, callbacks_note(self.state.callbacks)) if s
            )
            self.payload = CallEndedPayload(
                end_reason=end_reason,
                summary=summary,
                sentiment=analysis.sentiment,
                intent=analysis.intent,
                duration_sec=duration,
                llm_model_used=self.llm_label,
            )
            self.emitter.call_ended(self.payload)
            await self.recorder.drain()
            await self.emitter.aclose()
            if self.model is not None:
                with contextlib.suppress(Exception):
                    await self.model.aclose()
            log.info(
                "call %s ended: reason=%s duration=%ss sentiment=%s intent=%s",
                self.state.bootstrap.call.id,
                end_reason,
                duration,
                analysis.sentiment.value,
                analysis.intent or "-",
            )
            return self.payload


async def wait_for_answer(
    room: rtc.Room, participant: rtc.RemoteParticipant, timeout: float = ANSWER_TIMEOUT_SEC
) -> bool:
    """For outbound calls: wait until ``sip.callStatus`` becomes ``active``."""
    if participant.attributes.get(SIP_CALL_STATUS, "active") == "active":
        return True
    loop = asyncio.get_running_loop()
    fut: asyncio.Future[bool] = loop.create_future()

    def on_attrs(changed: dict[str, str], p: rtc.Participant) -> None:
        if p.identity != participant.identity or fut.done():
            return
        status = p.attributes.get(SIP_CALL_STATUS, "")
        if status == "active":
            fut.set_result(True)
        elif status == "hangup":
            fut.set_result(False)

    def on_left(p: rtc.RemoteParticipant) -> None:
        if p.identity == participant.identity and not fut.done():
            fut.set_result(False)

    room.on("participant_attributes_changed", on_attrs)
    room.on("participant_disconnected", on_left)
    try:
        return await asyncio.wait_for(fut, timeout=timeout)
    except TimeoutError:
        return False
    finally:
        room.off("participant_attributes_changed", on_attrs)
        room.off("participant_disconnected", on_left)


async def enforce_max_duration(
    session: AgentSession[Any], state: CallState, max_sec: float, language: str
) -> None:
    await asyncio.sleep(max_sec)
    if not state.set_end_reason("max_duration"):
        return
    log.info("max call duration (%ss) reached", max_sec)
    message = MAX_DURATION_MESSAGES.get(language[:2].lower(), MAX_DURATION_MESSAGES["mn"])
    try:
        handle = session.say(message, allow_interruptions=False)
        await asyncio.wait_for(handle.wait_for_playout(), timeout=15)
    except Exception as exc:  # noqa: BLE001
        log.debug("max-duration goodbye not played: %s", exc)
    session.shutdown(drain=False)


async def run_call(
    ctx: JobContext,
    client: BackendClient,
    *,
    factories: PipelineFactories | None = None,
    close_client: bool = False,
) -> None:
    """Run one phone call end to end inside a LiveKit job.

    With ``close_client`` the backend client is closed from the job's shutdown callback,
    after ``call.ended`` has been delivered.
    """
    f = factories or PipelineFactories()
    meta = parse_job_metadata(ctx.job.metadata)
    finalizers: list[CallFinalizer] = []

    async def _on_job_shutdown(reason: str) -> None:
        # Job shutdown callbacks run concurrently, so finalize and close in one callback.
        try:
            for fin in finalizers:
                await fin.finalize(None)
        finally:
            if close_client:
                await client.aclose()

    ctx.add_shutdown_callback(_on_job_shutdown)
    await ctx.connect()
    room_name = ctx.room.name
    ctx.log_context_fields = {"room": room_name, "direction": meta.direction.value}

    try:
        participant = await asyncio.wait_for(
            ctx.wait_for_participant(kind=rtc.ParticipantKind.PARTICIPANT_KIND_SIP),
            timeout=PARTICIPANT_TIMEOUT_SEC,
        )
    except TimeoutError:
        log.error("no SIP participant joined %s within %ss", room_name, PARTICIPANT_TIMEOUT_SEC)
        ctx.shutdown("no sip participant")
        return

    sip = SipInfo.from_attributes(participant.identity, participant.attributes)
    from_number, to_number = resolve_numbers(meta, sip)
    control = LiveKitCallControl(ctx.api, room_name, participant.identity)
    try:
        boot = await client.bootstrap(
            room=room_name,
            sip_number=sip.trunk_phone_number,
            from_number=from_number,
            to_number=to_number,
            direction=meta.direction,
            call_id=meta.call_id,
        )
    except Exception:
        log.exception("bootstrap failed for room %s; hanging up", room_name)
        await control.hangup()
        ctx.shutdown("bootstrap failed")
        return

    ctx.log_context_fields = {**ctx.log_context_fields, "call_id": str(boot.call.id)}
    emitter = EventEmitter(client, org_id=boot.org.id, call_id=boot.call.id)
    emitter.start()
    state = CallState(bootstrap=boot)
    recorder = CallRecorder(emitter, boot, hit_sink=client)
    finalizer = CallFinalizer(state=state, recorder=recorder, emitter=emitter, control=control)
    finalizers.append(finalizer)

    if meta.direction == CallDirection.OUTBOUND and not await wait_for_answer(
        ctx.room, participant
    ):
        state.set_end_reason("no_answer")
        await finalizer.finalize()
        ctx.shutdown("no answer")
        return

    chain = select_llm_chain(boot)
    try:
        if chain is None:
            raise RuntimeError("bootstrap returned no LLM configuration")
        model = f.build_llm(chain[0], chain[1])
        finalizer.model = model  # closed by the finalizer even if STT/TTS fail below
        llm_label = f.describe_llm(chain[0])
        finalizer.llm_label = recorder.llm_label = llm_label
        stt_engine = f.build_stt(boot.profile)
        tts_engine = f.build_tts(boot.profile)
    except Exception:
        log.exception("could not build the voice pipeline for call %s", boot.call.id)
        state.set_end_reason("failed")
        await finalizer.finalize()
        ctx.shutdown("pipeline build failed")
        return

    vad_model = ctx.proc.userdata.get("vad") or f.load_vad()
    session: AgentSession[CallState] = AgentSession(
        vad=vad_model,
        stt=stt_engine,
        llm=model,
        tts=tts_engine,
        turn_handling=build_turn_handling(f.turn_detection()),
        userdata=state,
    )
    recorder.attach(session)

    closed: asyncio.Future[CloseReason | None] = asyncio.get_running_loop().create_future()

    def _on_close(ev: CloseEvent) -> None:
        if ev.error is not None:
            log.warning("session closed with error: %s", ev.error)
        if not closed.done():
            closed.set_result(ev.reason)

    def _on_participant_left(p: rtc.RemoteParticipant) -> None:
        if p.identity == participant.identity:
            state.set_end_reason("hangup_customer")
            session.shutdown(drain=False)

    session.on("close", _on_close)
    ctx.room.on("participant_disconnected", _on_participant_left)

    agent = CallGoAgent(
        bootstrap=boot,
        tools=build_tools(state, control),
        on_stt_final=recorder.on_stt_final,
    )
    recorder.mark_answered()
    emitter.call_answered(
        boot.call.model_copy(
            update={
                "status": CallStatus.ACTIVE,
                "answered_at": utcnow(),
                "room_name": boot.call.room_name or room_name,
                "llm_model_used": llm_label,
            }
        )
    )
    try:
        await session.start(
            agent=agent,
            room=ctx.room,
            room_options=room_io.RoomOptions(
                participant_identity=participant.identity,
                participant_kinds=[rtc.ParticipantKind.PARTICIPANT_KIND_SIP],
                close_on_disconnect=True,
            ),
        )
    except Exception:
        log.exception("AgentSession failed to start for call %s", boot.call.id)
        ctx.room.off("participant_disconnected", _on_participant_left)
        state.set_end_reason("failed")
        await finalizer.finalize(CloseReason.ERROR)
        ctx.shutdown("session start failed")
        return

    max_sec = boot.profile.max_duration_sec or settings.max_call_duration_sec
    timer = asyncio.create_task(
        enforce_max_duration(session, state, max_sec, boot.profile.language)
    )
    try:
        close_reason = await closed
    finally:
        timer.cancel()
        ctx.room.off("participant_disconnected", _on_participant_left)
    await finalizer.finalize(close_reason)
    ctx.shutdown(state.end_reason or "call ended")
