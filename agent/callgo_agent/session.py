"""One CallGo phone call: agent persona, pipeline hooks, live events and post-call analysis.

* :class:`CallGoAgent` — ``Agent`` whose instructions come from the profile, campaign script
  (``{{var}}`` substitution) and contact; greets on enter; routes STT output through
  ``normalizer.normalize_stt`` and TTS input through ``normalizer.normalize_for_tts``.
* :class:`CallRecorder` — turns ``AgentSession`` events into ``transcript.*`` /
  ``agent.state`` events and keeps the transcript for the post-call analysis.
* :func:`analyze_call` — one LLM completion producing ``{summary, sentiment, intent}`` plus,
  for campaigns with outcomes, ``{outcome, outcomeNote}``.
* :func:`run_call` — the job entrypoint body used by :mod:`callgo_agent.worker`.

Knowledge base (``Bootstrap.knowledge``): ``context`` mode puts the base text in the
instructions; ``tool`` mode adds ``lookup_knowledge`` (see :mod:`callgo_agent.knowledge`).

SaaS additions: ``Bootstrap.entitlements.canStart == false`` and inbound ``after_hours``
routes without a profile are refused with a short message (:mod:`callgo_agent.routing`);
``menu`` routes run the IVR menu first and switch the agent with ``session.update_agent``;
operator handoff (:mod:`callgo_agent.handoff`) makes the agent passive; usage is metered
(:mod:`callgo_agent.usage`) and sent in ``call.ended`` together with callback intents.
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
    NOT_GIVEN,
    Agent,
    AgentSession,
    AgentStateChangedEvent,
    CloseEvent,
    CloseReason,
    ConversationItemAddedEvent,
    JobContext,
    ModelSettings,
    NotGivenOr,
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
from .handoff import HandoffController, OperatorTranscriber
from .knowledge import (
    KnowledgeRetriever,
    KnowledgeSearcher,
    active_knowledge,
    build_lookup_tool,
    knowledge_instructions,
)
from .routing import (
    CallGate,
    KeyQueue,
    MenuFlow,
    MenuResult,
    SessionPromptPlayer,
    attach_dtmf,
    attach_spoken_keys,
    gate_for,
    menu_route,
)
from .schemas import (
    AgentProfile,
    AgentState,
    Bootstrap,
    CallDirection,
    CallEndedPayload,
    CallStatus,
    CallUsage,
    CampaignOutcome,
    EndReason,
    JobMetadata,
    LexiconEntry,
    LexiconScope,
    LLMConfig,
    ResolvedRoute,
    Sentiment,
    Speaker,
    TranscriptTurn,
)
from .tools import (
    ALL_TOOLS,
    TOOL_REQUEST_OPERATOR,
    TOOL_TRANSFER_CALL,
    CallbackRequest,
    CallControl,
    CallState,
    LiveKitCallControl,
    build_tools,
    campaign_outcomes,
    clip_note,
    contact_summary,
    language_name,
    outcome_table,
    resolve_outcome,
)
from .usage import UsageTracker

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
REFUSAL_PLAYOUT_TIMEOUT_SEC = 30.0
MENU_INSTRUCTIONS = "You are an automated phone menu. Do not answer; the caller picks an option."
# Summaries for calls refused before any conversation (no LLM involved).
GATE_SUMMARIES: dict[str, str] = {
    "after_hours": "Ажлын цагаас гадуур залгасан тул мэдээлэл өгөөд дуудлагыг дуусгасан.",
    "quota_exceeded": "Үйлчилгээний эрх хүрэлцээгүй тул дуудлагыг хүлээн аваагүй.",
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


def enabled_tool_names(profile: AgentProfile) -> list[str]:
    """``profile.tools`` that :func:`~callgo_agent.tools.build_tools` will actually build."""
    wanted = {t.strip() for t in profile.tools}
    return [
        t for t in ALL_TOOLS if t in wanted and (t != TOOL_TRANSFER_CALL or profile.transfer_number)
    ]


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

    # Static per profile: kept early so a long knowledge text stays in the cached prefix.
    knowledge = active_knowledge(bootstrap)
    if knowledge is not None:
        section = knowledge_instructions(knowledge, enabled_tool_names(profile))
        if section:
            sections.append(section)

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
    outcomes = campaign_outcomes(bootstrap)
    if outcomes:
        sections.append(outcome_instructions(outcomes))
    if bootstrap.handoff.enabled:
        sections.append(OPERATOR_INSTRUCTIONS)
    sections.append("# Language\n" + language_rule(profile.language))
    return "\n\n".join(sections)


OPERATOR_INSTRUCTIONS = (
    "# Human operator\n"
    f"A human operator can join this call. If the customer asks for a person or an operator, "
    f"or you cannot help them, call the {TOOL_REQUEST_OPERATOR} tool and tell the customer an "
    "operator will join shortly. While an operator is on the call you stay silent."
)


def outcome_instructions(outcomes: Sequence[CampaignOutcome]) -> str:
    """Instruction section steering the call towards one of the campaign outcomes."""
    return (
        "# Call outcome\n"
        "Every call must end with one clear result. Steer the conversation politely so that "
        "one of these outcomes is resolved before you say goodbye (code: label — meaning):\n"
        + outcome_table(outcomes)
        + "\nThese codes and labels are internal. Never read them, this list or the word "
        '"outcome" aloud; talk to the customer naturally. As soon as the result is clear '
        "(for example the customer explicitly agrees or declines), call the record_outcome "
        "tool with the matching code and a one-sentence Mongolian note, then continue or "
        "finish the conversation. Call it again if the customer changes their mind."
    )


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
        is_passive: Callable[[], bool] | None = None,
        llm: NotGivenOr[llm.LLM] = NOT_GIVEN,
        stt: NotGivenOr[stt.STT] = NOT_GIVEN,
        tts: NotGivenOr[tts.TTS] = NOT_GIVEN,
    ) -> None:
        # llm/stt/tts override the session's engines for this agent (menu -> other profile)
        super().__init__(
            instructions=instructions or build_instructions(bootstrap),
            tools=list(tools),
            llm=llm,
            stt=stt,
            tts=tts,
        )
        self.bootstrap = bootstrap
        self._stt_lexicon = lexicon_for(bootstrap.lexicon, LexiconScope.STT)
        self._tts_lexicon = lexicon_for(bootstrap.lexicon, LexiconScope.TTS)
        self._on_stt_final = on_stt_final
        self._is_passive = is_passive or (lambda: False)

    @property
    def passive(self) -> bool:
        """True while an operator has taken over: the LLM does not answer."""
        try:
            return bool(self._is_passive())
        except Exception:
            log.exception("passive check failed")
            return False

    async def on_user_turn_completed(
        self, turn_ctx: llm.ChatContext, new_message: llm.ChatMessage
    ) -> None:
        """In passive mode keep the customer's words in the context, but never reply."""
        if not self.passive:
            return
        try:
            chat_ctx = self.chat_ctx.copy()
            chat_ctx.items.append(new_message)
            await self.update_chat_ctx(chat_ctx)
        except Exception as exc:  # noqa: BLE001 - context bookkeeping only
            log.debug("could not keep passive turn in the chat context: %s", exc)
        raise llm.StopResponse()

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


class SilentAgent(CallGoAgent):
    """Speaks only through ``session.say`` (IVR menu, refusal messages); never replies.

    Keeps :class:`CallGoAgent`'s STT/TTS normalization so digits in a menu prompt are read
    out as Mongolian words.
    """

    def __init__(
        self,
        *,
        bootstrap: Bootstrap,
        on_stt_final: SttFinalCallback | None = None,
        instructions: str = MENU_INSTRUCTIONS,
    ) -> None:
        super().__init__(
            bootstrap=bootstrap,
            on_stt_final=on_stt_final,
            instructions=instructions,
            is_passive=lambda: True,
        )

    async def on_enter(self) -> None:
        return None

    async def on_user_turn_completed(
        self, turn_ctx: llm.ChatContext, new_message: llm.ChatMessage
    ) -> None:
        raise llm.StopResponse()


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
        # While it returns True (operator handoff) session agent states are not published
        # and silent customers are not prompted.
        self.passive: Callable[[], bool] = lambda: False

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
        if ev.new_state == self._last_state or self.passive():
            return
        self._last_state = ev.new_state
        self.emitter.agent_state(ev.new_state, self.llm_label)

    def publish_state(self, state: AgentState) -> None:
        if state != self._last_state:
            self._last_state = state
            self.emitter.agent_state(state, self.llm_label)

    # -- operator handoff (HandoffObserver) --

    def handoff_started(self) -> None:
        self.publish_state("handoff")

    def handoff_ended(self) -> None:
        self._last_state = None  # the next session state is published again

    def add_operator_turn(
        self, text: str, raw: str = "", confidence: float = 0.0, duration_sec: float = 0.0
    ) -> None:
        """A final transcript of the human operator (``speaker = "human"``)."""
        text = text.strip()
        if not text:
            return
        end = self.ms()
        self._add_turn(
            TranscriptTurn(
                call_id=self.bootstrap.call.id,
                seq=self._next_seq(),
                speaker=Speaker.HUMAN,
                text=text,
                raw_text=raw.strip() or text,
                confidence=confidence,
                start_ms=max(0, end - int(duration_sec * 1000)),
                end_ms=end,
                is_final=True,
            )
        )

    def on_user_state_changed(self, ev: UserStateChangedEvent) -> None:
        if ev.new_state == "speaking":
            if self._user_start_ms is None:
                self._user_start_ms = self.ms(ev.created_at)
            self._away_prompted = False
        elif (
            ev.new_state == "away"
            and not self._away_prompted
            and self._session is not None
            and not self.passive()
        ):
            self._away_prompted = True
            try:
                self._session.generate_reply(instructions=AWAY_PROMPT)
            except RuntimeError as exc:
                log.debug("cannot prompt away user: %s", exc)

    def transcript_text(self) -> str:
        label = {Speaker.CUSTOMER: "Customer", Speaker.AGENT: "Agent", Speaker.HUMAN: "Operator"}
        return "\n".join(f"{label[t.speaker]}: {t.text}" for t in self.turns)


# ---- post-call analysis -----------------------------------------------------------------

_ANALYSIS_HEAD = (
    "You analyse a finished phone call between a customer and a voice agent.\n"
    "Reply with ONLY one JSON object and nothing else:\n"
)
_ANALYSIS_FIELDS = """- summary: 2-3 sentences in Mongolian (Cyrillic) saying what the customer wanted and the outcome.
- sentiment: the customer's overall sentiment: positive, neutral or negative.
- intent: the customer's main intent as a short English snake_case label, for example
  "order_status", "product_inquiry", "complaint", "callback_request", "not_interested"."""

ANALYSIS_PROMPT = (
    _ANALYSIS_HEAD
    + '{"summary": "...", "sentiment": "positive|neutral|negative", "intent": "..."}\n'
    + _ANALYSIS_FIELDS
)

_ANALYSIS_OUTCOME_FIELDS = """- outcome: exactly one outcome code from the table below (the code, not the label), or "".
- outcomeNote: one short sentence in Mongolian (Cyrillic), at most 200 characters.

Дуудлагын үр дүнгийн хүснэгт (код: нэр — хэзээ сонгох):
{table}

Үр дүн сонгох заавар:
- Ярианы төгсгөл дэх харилцагчийн эцсийн байр сууринд хамгийн сайн тохирох ГАНЦ кодыг сонго.
- outcome талбарт зөвхөн хүснэгтэд байгаа кодыг яг тэр хэлбэрээр нь бич (нэрийг нь биш).
- Аль нь ч тохирохгүй эсвэл үр дүн тодорхойгүй бол outcome-д "" гэж бич.
- outcomeNote-д яагаад энэ үр дүнг сонгосноо монгол хэлээр, 200 тэмдэгтээс хэтрүүлэлгүй нэг
  богино өгүүлбэрээр бич. Жишээ: "Харилцагч маргааш 10 цагт дахин залгахыг хүссэн."
- Дуудлагын явцад бүртгэгдсэн үр дүн өгөгдсөн бол яриатай зөрчилдөхгүй л бол түүнийг сонго."""

OUTCOME_NO_CONTACT = "no_contact"
_NO_CONTACT_NOTES: dict[str, str] = {
    "no_answer": "Дуудлагад хариу өгөөгүй.",
    "busy": "Харилцагчийн шугам завгүй байсан.",
    "failed": "Дуудлага холбогдож чадсангүй.",
    "voicemail": "Дуут шуудан руу шилжсэн.",
}
_NO_CONTACT_DEFAULT_NOTE = "Харилцагч ярианд оролцоогүй."


def analysis_prompt(outcomes: Sequence[CampaignOutcome] = ()) -> str:
    """System prompt for the post-call analysis; asks for an outcome when there are options."""
    if not outcomes:
        return ANALYSIS_PROMPT
    return (
        _ANALYSIS_HEAD
        + '{"summary": "...", "sentiment": "positive|neutral|negative", "intent": "...", '
        '"outcome": "<code>", "outcomeNote": "..."}\n'
        + _ANALYSIS_FIELDS
        + "\n"
        + _ANALYSIS_OUTCOME_FIELDS.format(table=outcome_table(outcomes))
    )


@dataclass(slots=True)
class CallAnalysis:
    summary: str = ""
    sentiment: Sentiment = Sentiment.NEUTRAL
    intent: str = ""
    from_llm: bool = False
    outcome: str = ""
    outcome_note: str = ""


_FENCE_RE = re.compile(r"^```[a-zA-Z]*\s*|\s*```$")


def parse_analysis(text: str, outcomes: Sequence[CampaignOutcome] = ()) -> CallAnalysis | None:
    """Extract ``{summary, sentiment, intent[, outcome, outcomeNote]}`` from an LLM reply.

    ``None`` if unusable. With ``outcomes`` the outcome is validated against the codes
    (falling back to a case-insensitive label match); anything else becomes ``""``.
    """
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
    outcome, note = "", ""
    if outcomes:
        raw_outcome = str(data.get("outcome") or "").strip()
        outcome = resolve_outcome(raw_outcome, outcomes)
        if raw_outcome and not outcome:
            log.warning("analysis returned unknown outcome %r; leaving it empty", raw_outcome)
        note = clip_note(str(data.get("outcomeNote") or data.get("outcome_note") or ""))
    return CallAnalysis(
        summary=summary,
        sentiment=sentiment,
        intent=intent,
        from_llm=True,
        outcome=outcome,
        outcome_note=note,
    )


def fallback_analysis(
    turns: Sequence[TranscriptTurn],
    outcomes: Sequence[CampaignOutcome] = (),
    end_reason: str = "",
) -> CallAnalysis:
    """Analysis without an LLM. The outcome is ``no_contact`` (when the campaign defines it)
    if the customer never spoke, otherwise ``""``."""
    customer = [t.text for t in turns if t.speaker == Speaker.CUSTOMER]
    outcome, note = "", ""
    if not customer:
        outcome = resolve_outcome(OUTCOME_NO_CONTACT, outcomes)
        if outcome:
            note = _NO_CONTACT_NOTES.get(end_reason, _NO_CONTACT_DEFAULT_NOTE)
    if not customer and end_reason in GATE_SUMMARIES:
        return CallAnalysis(summary=GATE_SUMMARIES[end_reason], outcome=outcome, outcome_note=note)
    if not turns:
        return CallAnalysis(
            summary="Харилцан яриа бүртгэгдээгүй.", outcome=outcome, outcome_note=note
        )
    if not customer:
        return CallAnalysis(
            summary="Харилцагч ярианд оролцоогүй.", outcome=outcome, outcome_note=note
        )
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


def final_outcome(state: CallState, analysis: CallAnalysis) -> tuple[str, str]:
    """(outcome, note) for ``call.ended``: a ``record_outcome`` value wins over the analysis."""
    if state.outcome:
        note = state.outcome_note
        if not note and analysis.outcome == state.outcome:
            note = analysis.outcome_note
        return state.outcome, note
    return analysis.outcome, analysis.outcome_note if analysis.outcome else ""


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
    outcomes: Sequence[CampaignOutcome] = (),
    recorded_outcome: str = "",
) -> CallAnalysis:
    """One chat completion over the transcript; falls back safely on any failure.

    Without customer speech (unanswered, busy, failed before any conversation) no LLM is
    called and :func:`fallback_analysis` decides (``no_contact`` when defined).
    """
    if model is None or not any(t.speaker == Speaker.CUSTOMER for t in turns):
        return fallback_analysis(turns, outcomes, end_reason)
    label = {Speaker.CUSTOMER: "Customer", Speaker.AGENT: "Agent", Speaker.HUMAN: "Operator"}
    transcript = "\n".join(f"{label[t.speaker]}: {t.text}" for t in turns)
    extra = []
    if end_reason:
        extra.append(f"Call end reason: {end_reason}")
    if callbacks:
        extra.append("Callback requested: " + "; ".join(c.describe() for c in callbacks))
    if outcomes and recorded_outcome:
        extra.append(f"Outcome recorded during the call: {recorded_outcome}")
    chat_ctx = llm.ChatContext.empty()
    chat_ctx.add_message(role="system", content=analysis_prompt(outcomes))
    chat_ctx.add_message(
        role="user",
        content="Transcript:\n" + transcript + ("\n\n" + "\n".join(extra) if extra else ""),
    )
    try:
        reply = await asyncio.wait_for(_complete(model, chat_ctx), timeout=timeout)
    except Exception as exc:  # noqa: BLE001 - analysis must never break call teardown
        log.warning("post-call analysis failed: %r", exc)
        return fallback_analysis(turns, outcomes, end_reason)
    parsed = parse_analysis(reply, outcomes)
    if parsed is None:
        log.warning("post-call analysis returned unparsable output: %r", reply[:300])
        return fallback_analysis(turns, outcomes, end_reason)
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
    usage: UsageTracker | None = None
    # LLMs replaced mid-call (menu switched profile): closed with ``model`` at the end.
    retired_models: list[llm.LLM] = field(default_factory=list)
    payload: CallEndedPayload | None = None
    _lock: asyncio.Lock = field(default_factory=asyncio.Lock)

    def usage_snapshot(self) -> CallUsage:
        if self.usage is None:
            return CallUsage(llm_model=self.llm_label)
        if not self.usage.llm_model:
            self.usage.llm_model = self.llm_label
        return self.usage.snapshot()

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
            metering = self.usage.watch(self.model) if self.usage else contextlib.nullcontext()
            with metering:
                analysis = await analyze_call(
                    self.model,
                    self.recorder.turns,
                    end_reason=end_reason,
                    callbacks=self.state.callbacks,
                    timeout=self.analysis_timeout,
                    outcomes=campaign_outcomes(self.state.bootstrap),
                    recorded_outcome=self.state.outcome,
                )
            outcome, outcome_note = final_outcome(self.state, analysis)
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
                outcome=outcome,
                outcome_note=outcome_note,
            )
            usage = self.usage_snapshot()
            self.emitter.call_ended(
                self.payload, usage=usage, callbacks=self.state.callback_intents()
            )
            await self.recorder.drain()
            await self.emitter.aclose()
            for model in (*self.retired_models, self.model):
                if model is not None:
                    with contextlib.suppress(Exception):
                        await model.aclose()
            log.info(
                "call %s ended: reason=%s duration=%ss sentiment=%s intent=%s outcome=%s",
                self.state.bootstrap.call.id,
                end_reason,
                duration,
                analysis.sentiment.value,
                analysis.intent or "-",
                outcome or "-",
            )
            log.info(
                "call %s usage: llm %d/%d tokens, stt %.1fs, tts %d chars, cost %d MNT",
                self.state.bootstrap.call.id,
                usage.llm_tokens_in,
                usage.llm_tokens_out,
                usage.stt_seconds,
                usage.tts_chars,
                usage.cost_mnt,
            )
            if self.state.knowledge_lookups:
                # Metrics only: the summary is customer-facing and stays untouched.
                log.info(
                    "call %s knowledge lookups: %d (%d without results)",
                    self.state.bootstrap.call.id,
                    self.state.knowledge_lookups,
                    self.state.knowledge_misses,
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


def call_tools(
    state: CallState, control: CallControl, searcher: KnowledgeSearcher
) -> list[llm.Tool]:
    """Profile-gated tools plus ``lookup_knowledge`` when the knowledge mode is ``tool``."""
    tools: list[llm.Tool] = list(build_tools(state, control))
    knowledge = active_knowledge(state.bootstrap)
    if knowledge is not None and knowledge.mode == "tool":
        retriever = KnowledgeRetriever(searcher, knowledge.id)
        tools.append(build_lookup_tool(retriever, on_lookup=state.record_knowledge_lookup))
        log.info("knowledge base %s (%s) enabled as a tool", knowledge.id, knowledge.name)
    elif knowledge is not None:
        log.info(
            "knowledge base %s (%s) in context mode: %d chars%s",
            knowledge.id,
            knowledge.name,
            len(knowledge.context_text),
            " (truncated)" if knowledge.truncated else "",
        )
    return tools


def _profile_needs(old: AgentProfile, new: AgentProfile) -> tuple[bool, bool]:
    """(new STT needed, new TTS needed) when switching from ``old`` to ``new``."""
    stt_changed = (old.stt_provider, old.stt_model, old.language) != (
        new.stt_provider,
        new.stt_model,
        new.language,
    )
    tts_changed = (old.tts_provider, old.tts_voice, old.language) != (
        new.tts_provider,
        new.tts_voice,
        new.language,
    )
    return stt_changed, tts_changed


def _same_llm(a: LLMConfig, b: LLMConfig) -> bool:
    return (a.id, a.provider, a.model, a.base_url) == (b.id, b.provider, b.model, b.base_url)


async def refuse_call(
    ctx: JobContext,
    f: PipelineFactories,
    boot: Bootstrap,
    participant: rtc.RemoteParticipant,
    gate: CallGate,
    *,
    state: CallState,
    recorder: CallRecorder,
    usage: UsageTracker,
) -> None:
    """Say ``gate.message`` with a TTS-only session (no STT, no LLM) and return.

    The caller is not reported as answered (no ``call.answered``: no recording, no
    conversation); the finalizer then emits ``call.ended`` with ``gate.end_reason``.
    """
    state.set_end_reason(gate.end_reason)
    recorder.mark_answered()
    log.info("refusing call %s: %s", boot.call.id, gate.end_reason)
    try:
        tts_engine = f.build_tts(boot.profile)
    except Exception:
        log.exception("could not build TTS for the %s message; hanging up", gate.end_reason)
        return
    session: AgentSession[CallState] = AgentSession(tts=tts_engine, userdata=state)
    recorder.attach(session)
    usage.attach(session)
    try:
        await session.start(
            agent=SilentAgent(bootstrap=boot, instructions="Say the given message only."),
            room=ctx.room,
            room_options=room_io.RoomOptions(
                participant_identity=participant.identity,
                participant_kinds=[rtc.ParticipantKind.PARTICIPANT_KIND_SIP],
                audio_input=False,
                text_input=False,
                close_on_disconnect=True,
            ),
        )
        handle = session.say(gate.message, allow_interruptions=False)
        await asyncio.wait_for(handle.wait_for_playout(), timeout=REFUSAL_PLAYOUT_TIMEOUT_SEC)
    except Exception as exc:  # noqa: BLE001 - the call ends either way
        log.warning("%s message not played: %r", gate.end_reason, exc)
    finally:
        with contextlib.suppress(Exception):
            await session.aclose()


async def run_menu(
    route: ResolvedRoute,
    session: Any,
    room: Any,
    caller_identity: str,
    *,
    keys: KeyQueue | None = None,
) -> MenuResult:
    """Run the IVR menu on a started session: DTMF from the caller plus spoken keys."""
    queue = keys or KeyQueue()
    detach = [
        attach_dtmf(room, queue, caller_identity),
        attach_spoken_keys(session, queue, route.menu),
    ]
    try:
        return await MenuFlow(route, SessionPromptPlayer(session), queue).run()
    finally:
        for d in detach:
            d()


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
    state.on_handoff_request = lambda _reason: emitter.call_handoff("requested")
    recorder = CallRecorder(emitter, boot, hit_sink=client)
    recorder.passive = lambda: state.passive
    usage = UsageTracker()
    finalizer = CallFinalizer(
        state=state, recorder=recorder, emitter=emitter, control=control, usage=usage
    )
    finalizers.append(finalizer)

    if meta.direction == CallDirection.OUTBOUND and not await wait_for_answer(
        ctx.room, participant
    ):
        state.set_end_reason("no_answer")
        await finalizer.finalize()
        ctx.shutdown("no answer")
        return

    gate = gate_for(boot)
    if gate is not None:
        await refuse_call(
            ctx, f, boot, participant, gate, state=state, recorder=recorder, usage=usage
        )
        await finalizer.finalize()
        ctx.shutdown(gate.end_reason)
        return

    chain = select_llm_chain(boot)
    try:
        if chain is None:
            raise RuntimeError("bootstrap returned no LLM configuration")
        model = f.build_llm(chain[0], chain[1])
        finalizer.model = model  # closed by the finalizer even if STT/TTS fail below
        llm_label = f.describe_llm(chain[0])
        finalizer.llm_label = recorder.llm_label = usage.llm_model = llm_label
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
    usage.attach(session)

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

    def make_agent(b: Bootstrap, **engines: Any) -> CallGoAgent:
        return CallGoAgent(
            bootstrap=b,
            tools=call_tools(state, control, client),
            on_stt_final=recorder.on_stt_final,
            is_passive=lambda: state.passive,
            **engines,
        )

    async def continue_with(chosen: Bootstrap) -> None:
        """Switch from the menu to the conversational agent for ``chosen``'s profile."""
        engines: dict[str, Any] = {}
        if chosen is not boot:
            state.bootstrap = recorder.bootstrap = chosen
            new_chain = select_llm_chain(chosen)
            try:
                if (
                    new_chain is not None
                    and chain is not None
                    and not _same_llm(new_chain[0], chain[0])
                ):
                    new_model = f.build_llm(new_chain[0], new_chain[1])
                    engines["llm"] = new_model
                    if finalizer.model is not None:
                        finalizer.retired_models.append(finalizer.model)
                    finalizer.model = new_model
                    label = f.describe_llm(new_chain[0])
                    finalizer.llm_label = recorder.llm_label = usage.llm_model = label
                need_stt, need_tts = _profile_needs(boot.profile, chosen.profile)
                if need_stt:
                    engines["stt"] = f.build_stt(chosen.profile)
                if need_tts:
                    engines["tts"] = f.build_tts(chosen.profile)
            except Exception:
                log.exception("could not build engines for profile %s", chosen.profile.id)
        session.update_agent(make_agent(chosen, **engines))

    async def menu_then_agent(route: ResolvedRoute) -> None:
        chosen = boot
        try:
            result = await run_menu(route, session, ctx.room, participant.identity)
            option = result.option
            if option is not None and option.agent_profile_id != boot.profile.id:
                chosen = await client.bootstrap(
                    room=room_name,
                    sip_number=sip.trunk_phone_number,
                    from_number=from_number,
                    to_number=to_number,
                    direction=meta.direction,
                    call_id=boot.call.id,
                    profile_id=option.agent_profile_id,
                )
            elif option is None:
                log.info("menu timed out; continuing with profile %s", boot.profile.id)
        except asyncio.CancelledError:
            raise
        except Exception:
            log.exception("menu flow failed; continuing with profile %s", boot.profile.id)
            chosen = boot
        await continue_with(chosen)

    route = menu_route(boot)
    agent: CallGoAgent = (
        SilentAgent(bootstrap=boot, on_stt_final=recorder.on_stt_final)
        if route is not None
        else make_agent(boot)
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

    handoff: HandoffController | None = None
    if boot.handoff.enabled:
        stt_lexicon = lexicon_for(boot.lexicon, LexiconScope.STT)

        def _operator_final(text: str, confidence: float, duration: float) -> None:
            corrected, _ = normalize_stt_text(text, stt_lexicon)
            recorder.add_operator_turn(corrected, text, confidence, duration)

        handoff = HandoffController(
            ctx.room,
            session,
            state,
            emitter,
            customer_identity=participant.identity,
            observer=recorder,
            transcribe=OperatorTranscriber(stt_engine, vad_model, _operator_final),
        )
        handoff.start()

    background: list[asyncio.Task[None]] = []
    max_sec = boot.profile.max_duration_sec or settings.max_call_duration_sec
    background.append(
        asyncio.create_task(enforce_max_duration(session, state, max_sec, boot.profile.language))
    )
    if route is not None:
        background.append(asyncio.create_task(menu_then_agent(route), name="callgo-menu"))
    try:
        close_reason = await closed
    finally:
        for task in background:
            task.cancel()
        await asyncio.gather(*background, return_exceptions=True)
        ctx.room.off("participant_disconnected", _on_participant_left)
        if handoff is not None:
            await handoff.aclose()
    await finalizer.finalize(close_reason)
    ctx.shutdown(state.end_reason or "call ended")
