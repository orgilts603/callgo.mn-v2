"""LLM function tools for a CallGo call, enabled per ``AgentProfile.tools``.

* ``end_call``          say goodbye, then hang up (end reason ``hangup_agent``)
* ``transfer_call``     SIP REFER the caller to ``profile.transfer_number`` (``transferred``)
* ``lookup_contact``    contact record + campaign variables as text
* ``schedule_callback`` record a callback (``CallbackIntent``, due time parsed from Mongolian /
  English / ISO text by :func:`parse_callback_time`); sent as ``call.ended.callbacks``
* ``record_outcome``    commit the campaign outcome mid-call; auto-enabled (not via
  ``profile.tools``) whenever the campaign defines outcomes
* ``lookup_knowledge``  knowledge-base search (:mod:`callgo_agent.knowledge`); enabled by the
  profile's ``knowledgeMode == "tool"``, not via ``profile.tools``
* ``request_operator``  ask a human operator to join (``call.updated {"handoff":"requested"}``);
  auto-enabled whenever ``Bootstrap.handoff.enabled``

Tools only touch :class:`CallState` (per-call mutable data shared with the session) and a
:class:`CallControl` (hang-up / transfer), so they are testable without LiveKit.
"""

from __future__ import annotations

import logging
import re
from collections.abc import Callable, Iterable
from dataclasses import dataclass, field
from datetime import UTC, datetime, timedelta, tzinfo
from typing import Any, Literal, Protocol
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

from livekit import api as lk_api
from livekit.agents.llm import FunctionTool, StopResponse, ToolError, ToolFlag, function_tool
from livekit.agents.voice import RunContext

from .events import utcnow
from .schemas import Bootstrap, CallbackIntent, CampaignOutcome, EndReason

log = logging.getLogger("callgo.tools")

TOOL_END_CALL = "end_call"
TOOL_TRANSFER_CALL = "transfer_call"
TOOL_LOOKUP_CONTACT = "lookup_contact"
TOOL_SCHEDULE_CALLBACK = "schedule_callback"
TOOL_RECORD_OUTCOME = "record_outcome"
TOOL_LOOKUP_KNOWLEDGE = "lookup_knowledge"
TOOL_REQUEST_OPERATOR = "request_operator"
ALL_TOOLS: tuple[str, ...] = (
    TOOL_END_CALL,
    TOOL_TRANSFER_CALL,
    TOOL_LOOKUP_CONTACT,
    TOOL_SCHEDULE_CALLBACK,
)
# Enabled by the campaign (outcomes defined) or the knowledge mode, never by ``profile.tools``.
AUTO_TOOLS: tuple[str, ...] = (TOOL_RECORD_OUTCOME, TOOL_LOOKUP_KNOWLEDGE, TOOL_REQUEST_OPERATOR)

OUTCOME_NOTE_MAX_CHARS = 200

_LANGUAGE_NAMES = {"mn": "Mongolian", "en": "English", "ru": "Russian"}


def language_name(code: str) -> str:
    return _LANGUAGE_NAMES.get(code.lower().split("-")[0], code) if code else "Mongolian"


DEFAULT_TZ = "Asia/Ulaanbaatar"
CALLBACK_DEFAULT_HOUR = 10  # a day without a time ("маргааш") means 10:00 local
CALLBACK_MAX_AHEAD = timedelta(days=180)
CALLBACK_PAST_TOLERANCE = timedelta(minutes=1)


def local_tz(name: str = "") -> tzinfo:
    """``ZoneInfo(name)``, falling back to Asia/Ulaanbaatar (then a fixed UTC+8)."""
    for candidate in (name, DEFAULT_TZ):
        if not candidate:
            continue
        try:
            return ZoneInfo(candidate)
        except (ZoneInfoNotFoundError, ValueError):
            continue
    from datetime import timezone

    return timezone(timedelta(hours=8), "ULAT")


# ---- callback time parsing ------------------------------------------------------------

_NUMBER_WORDS: dict[str, float] = {}
for _value, _forms in (
    (0, "тэг тэгэн"),
    (1, "нэг нэгэн нэгд нэгэнд нэгийн"),
    (2, "хоёр хоёрт хоёрын хоёрхон"),
    (3, "гурав гурван гуравт гурвын гурвад"),
    (4, "дөрөв дөрвөн дөрөвт дөрвийн дөрвөд"),
    (5, "тав таван тавд тавын"),
    (6, "зургаа зургаан зургаад зургаагийн"),
    (7, "долоо долоон долоод долоогийн"),
    (8, "найм найман наймд наймын наймаад"),
    (9, "ес есөн есд есийн есөөд"),
    (10, "арав арван аравт арвын"),
    (20, "хорь хорин"),
    (30, "гуч гучин"),
    (40, "дөч дөчин"),
    (50, "тавь тавин"),
):
    for _form in _forms.split():
        _NUMBER_WORDS[_form] = _value
_TENS_PREFIX = frozenset({"арван", "хорин", "гучин", "дөчин", "тавин"})
_HALF_WORDS = frozenset({"хагас", "хагаст", "хагасын", "half"})

_WEEKDAYS: dict[str, int] = {
    "даваа": 0,
    "мягмар": 1,
    "лхагва": 2,
    "пүрэв": 3,
    "баасан": 4,
    "бямба": 5,
    "ням": 6,
    "monday": 0,
    "tuesday": 1,
    "wednesday": 2,
    "thursday": 3,
    "friday": 4,
    "saturday": 5,
    "sunday": 6,
}
_DAY_OFFSETS: dict[str, int] = {
    "өнөөдөр": 0,
    "today": 0,
    "маргааш": 1,
    "tomorrow": 1,
    "нөгөөдөр": 2,
}
_MORNING = ("өглөө", "morning")
_AFTERNOON = ("үдээс", "үдийн", "afternoon")
_EVENING = ("орой", "evening", "шөнө", "tonight")
_ISO_RE = re.compile(
    r"\d{4}-\d{2}-\d{2}(?:[t ]\d{1,2}:\d{2}(?::\d{2}(?:\.\d+)?)?)?(?:z|[+-]\d{2}:?\d{2})?"
)
_CLOCK_RE = re.compile(r"^(\d{1,2})[:.](\d{2})")
_AMPM_RE = re.compile(r"^(\d{1,2})(?:[:.](\d{2}))?(am|pm)$")
_TOKEN_RE = re.compile(r"[\w:.]+")


def _tokens(text: str) -> list[str]:
    """Lower-cased tokens with Mongolian number words turned into digits
    (``"арван хоёр"`` -> ``"12"``, ``"долоо хоног"`` -> ``"week"``)."""
    raw = [t.strip(".") or t for t in _TOKEN_RE.findall(text.casefold())]
    out: list[str] = []
    i = 0
    while i < len(raw):
        tok = raw[i]
        nxt = raw[i + 1] if i + 1 < len(raw) else ""
        if tok in ("долоо", "долоон") and nxt.startswith("хоног"):
            out.append("week")
            i += 2
            continue
        if tok in _NUMBER_WORDS:
            value = _NUMBER_WORDS[tok]
            if tok in _TENS_PREFIX and nxt in _NUMBER_WORDS and _NUMBER_WORDS[nxt] < 10:
                value += _NUMBER_WORDS[nxt]
                i += 1
            out.append(str(int(value)))
        elif tok in _HALF_WORDS:
            out.append("0.5")
        else:
            out.append(tok)
        i += 1
    return out


def _num(tok: str) -> float | None:
    m = re.match(r"^(\d+(?:\.5)?)(?:-?[^\d:.]*)$", tok)
    return float(m.group(1)) if m else None


def _has(tokens: list[str], prefixes: Iterable[str]) -> bool:
    return any(t.startswith(p) for t in tokens for p in prefixes)


def _parse_iso(text: str, tz: tzinfo) -> datetime | None:
    m = _ISO_RE.search(text.casefold())
    if not m:
        return None
    value = m.group(0).replace(" ", "t").upper()
    try:
        dt = datetime.fromisoformat(value)
    except ValueError:
        return None
    if "T" not in value:
        dt = dt.replace(hour=CALLBACK_DEFAULT_HOUR)
    return dt.replace(tzinfo=tz) if dt.tzinfo is None else dt


def _relative_delta(tokens: list[str]) -> timedelta | None:
    """``N цаг(ийн) [M минут(ын)] дараа`` / ``in N hours`` -> duration."""
    later = "дараа" in tokens or "later" in tokens or "in" in tokens
    if not later:
        return None
    delta = timedelta()
    found = False
    for i, tok in enumerate(tokens):
        unit = tokens[i + 1] if i + 1 < len(tokens) else ""
        n = _num(tok)
        if n is None:
            if tok.startswith("цаг") and (i == 0 or _num(tokens[i - 1]) is None):
                n, unit = 1.0, tok  # "цагийн дараа" = in an hour
            else:
                continue
        if unit.startswith(("цаг", "hour")):
            delta += timedelta(hours=n)
        elif unit.startswith(("минут", "min")):
            delta += timedelta(minutes=n)
        elif unit.startswith(("хоног", "өдр", "өдөр", "day")):
            delta += timedelta(days=n)
        elif unit.startswith("week"):
            delta += timedelta(weeks=n)
        else:
            continue
        found = True
    if not found and _has(tokens, ("week",)):
        return timedelta(weeks=1)
    return delta if found and delta > timedelta() else None


def _clock_time(tokens: list[str]) -> tuple[int, int] | None:
    """``10:30`` / ``10 цагт`` / ``10 цаг 30 минутад`` / ``10 цаг хагаст`` / ``3pm``."""
    for tok in tokens:
        m = _CLOCK_RE.match(tok)
        if m and int(m.group(1)) < 24 and int(m.group(2)) < 60:
            return int(m.group(1)), int(m.group(2))
        m = _AMPM_RE.match(tok)
        if m:
            hour = int(m.group(1)) % 12 + (12 if m.group(3) == "pm" else 0)
            return hour, int(m.group(2) or 0)
    for i, tok in enumerate(tokens[:-1]):
        n = _num(tok)
        if n is None or n != int(n) or not tokens[i + 1].startswith(("цаг", "hour", "o'clock")):
            continue
        hour, minute = int(n), 0
        rest = tokens[i + 2 : i + 4]
        if rest and rest[0] == "0.5":
            minute = 30
        elif (
            len(rest) == 2
            and (m_val := _num(rest[0])) is not None
            and rest[1].startswith("мин")
            and 0 <= m_val < 60
        ):
            minute = int(m_val)
        if 0 <= hour < 24:
            return hour, minute
    return None


def parse_callback_time(text: str, now: datetime, tz: tzinfo | None = None) -> datetime | None:
    """Resolve a callback time said in Mongolian, English or ISO 8601; ``None`` if unclear.

    ``now`` is timezone-aware; the result is in ``tz`` (default Asia/Ulaanbaatar).
    Examples: ``"2026-10-01T10:00"``, ``"маргааш 10 цагт"``, ``"2 цагийн дараа"``,
    ``"хагас цагийн дараа"``, ``"баасан гарагт орой 7 цагт"``, ``"tomorrow 3pm"``.
    Hours 1-7 without ``өглөө`` are read as afternoon (13-19), as usual in speech.
    A day without a time means 10:00; a bare time already past today means tomorrow.
    """
    zone = tz or local_tz()
    local_now = now.astimezone(zone)
    iso = _parse_iso(text, zone)
    if iso is not None:
        return iso.astimezone(zone)
    tokens = _tokens(text)
    if not tokens:
        return None

    delta = _relative_delta(tokens)
    if delta is not None:
        return local_now + delta

    day_offset: int | None = None
    for tok in tokens:
        for word, offset in _DAY_OFFSETS.items():
            if tok.startswith(word):
                day_offset = offset
                break
        if day_offset is not None:
            break
    if day_offset is None:
        for tok in tokens:
            wd = next((v for k, v in _WEEKDAYS.items() if tok.startswith(k)), None)
            if wd is not None:
                day_offset = (wd - local_now.weekday()) % 7 or 7
                break

    morning = _has(tokens, _MORNING)
    afternoon = _has(tokens, _AFTERNOON)
    evening = _has(tokens, _EVENING)
    clock = _clock_time(tokens)
    if clock is not None:
        hour, minute = clock
        pm = afternoon or evening
        if (pm and hour < 12) or (not morning and 1 <= hour <= 7):
            hour += 12
    elif morning:
        hour, minute = CALLBACK_DEFAULT_HOUR, 0
    elif afternoon:
        hour, minute = 14, 0
    elif evening:
        hour, minute = 18, 0
    elif day_offset is not None:
        hour, minute = CALLBACK_DEFAULT_HOUR, 0
    else:
        return None

    base = local_now.replace(hour=hour, minute=minute, second=0, microsecond=0)
    if day_offset is not None:
        return base + timedelta(days=day_offset)
    if base <= local_now:
        base += timedelta(days=1)
    return base


@dataclass(slots=True)
class CallbackRequest:
    when: str
    note: str
    requested_at: datetime = field(default_factory=utcnow)
    due_at: datetime | None = None

    def describe(self) -> str:
        return f"{self.when} — {self.note}" if self.note else self.when

    def intent(self) -> CallbackIntent | None:
        """The ``call.ended.callbacks[]`` entry (UTC ``dueAt``); ``None`` without a due time."""
        if self.due_at is None:
            return None
        return CallbackIntent(due_at=self.due_at.astimezone(UTC), note=self.note)


def campaign_outcomes(bootstrap: Bootstrap) -> list[CampaignOutcome]:
    """The campaign's outcome options with a usable code (empty for non-campaign calls)."""
    campaign = bootstrap.campaign
    if campaign is None:
        return []
    return [o for o in campaign.outcomes if o.code.strip()]


def resolve_outcome(value: str, outcomes: Iterable[CampaignOutcome]) -> str:
    """Map an LLM-provided outcome to one of the campaign codes; ``""`` if none matches.

    Tries the exact code, then the code case-insensitively, then the label
    case-insensitively (models sometimes answer with the human label).
    """
    options = [o for o in outcomes if o.code.strip()]
    wanted = value.strip()
    if not wanted or not options:
        return ""
    for o in options:
        if o.code == wanted:
            return o.code
    folded = wanted.casefold()
    for o in options:
        if o.code.strip().casefold() == folded:
            return o.code
    for o in options:
        if o.label.strip() and o.label.strip().casefold() == folded:
            return o.code
    return ""


def clip_note(note: str, limit: int = OUTCOME_NOTE_MAX_CHARS) -> str:
    """Collapse whitespace and cut to ``limit`` characters (ellipsis included)."""
    text = " ".join(note.split())
    if len(text) <= limit:
        return text
    return text[: limit - 1].rstrip() + "…"


HandoffState = Literal["", "requested", "active", "ended"]


@dataclass
class CallState:
    """Mutable per-call context shared by the tools and the session runner."""

    bootstrap: Bootstrap
    end_reason: EndReason | None = None
    callbacks: list[CallbackRequest] = field(default_factory=list)
    transferred_to: str = ""
    # Campaign outcome committed mid-call by ``record_outcome`` (code, or "" if none yet).
    # It takes precedence over the post-call analysis.
    outcome: str = ""
    outcome_note: str = ""
    # ``lookup_knowledge`` metrics: searches that completed, and how many found nothing.
    knowledge_lookups: int = 0
    knowledge_misses: int = 0
    # Operator handoff: "" | "requested" | "active" | "ended". While "active" the agent is
    # passive (no LLM replies). ``on_handoff_request`` publishes ``call.updated``.
    handoff: HandoffState = ""
    on_handoff_request: Callable[[str], None] | None = field(default=None, repr=False)
    clock: Callable[[], datetime] = field(default=utcnow, repr=False)

    @property
    def passive(self) -> bool:
        """True while a human operator has taken over the call."""
        return self.handoff == "active"

    @property
    def tz(self) -> tzinfo:
        return local_tz(self.bootstrap.org.timezone)

    def callback_intents(self) -> list[CallbackIntent]:
        return [i for c in self.callbacks if (i := c.intent()) is not None]

    def request_handoff(self, reason: str = "") -> bool:
        """Mark an operator as requested (once). Returns True if this call changed it."""
        if self.handoff in ("requested", "active"):
            return False
        self.handoff = "requested"
        if self.on_handoff_request is not None:
            try:
                self.on_handoff_request(reason)
            except Exception:
                log.exception("handoff request notification failed")
        return True

    def record_knowledge_lookup(self, hits: int) -> None:
        """Count one completed knowledge search that returned ``hits`` usable passages."""
        self.knowledge_lookups += 1
        if hits <= 0:
            self.knowledge_misses += 1

    def set_end_reason(self, reason: EndReason) -> bool:
        """Record why the call ends; the first reason wins. Returns True if it was set."""
        if self.end_reason is None:
            self.end_reason = reason
            return True
        return False

    @property
    def outcomes(self) -> list[CampaignOutcome]:
        return campaign_outcomes(self.bootstrap)

    def record_outcome(self, value: str, note: str = "") -> str:
        """Validate and store an outcome (latest call wins). Returns the code, ``""`` if unknown."""
        code = resolve_outcome(value, self.outcomes)
        if code:
            self.outcome = code
            self.outcome_note = clip_note(note)
        return code


class CallControl(Protocol):
    async def hangup(self) -> None: ...

    async def transfer(self, to: str) -> None: ...


class LiveKitCallControl:
    """Hang up / cold-transfer the SIP leg through the LiveKit server API."""

    def __init__(self, lkapi: lk_api.LiveKitAPI, room_name: str, participant_identity: str) -> None:
        self._api = lkapi
        self._room = room_name
        self._identity = participant_identity

    async def hangup(self) -> None:
        """Delete the room: disconnects every participant, including the SIP caller."""
        try:
            await self._api.room.delete_room(lk_api.DeleteRoomRequest(room=self._room))
        except lk_api.TwirpError as exc:
            if exc.code != lk_api.TwirpErrorCode.NOT_FOUND:
                log.warning("delete_room(%s) failed: %s", self._room, exc)

    async def transfer(self, to: str) -> None:
        """SIP REFER the caller to ``to`` (E.164 number or ``sip:`` URI)."""
        target = to if to.startswith(("sip:", "sips:", "tel:")) else f"tel:{to}"
        await self._api.sip.transfer_sip_participant(
            lk_api.TransferSIPParticipantRequest(
                room_name=self._room,
                participant_identity=self._identity,
                transfer_to=target,
                play_dialtone=False,
            )
        )


def contact_summary(bootstrap: Bootstrap) -> str:
    """Human-readable contact + campaign context (used by ``lookup_contact`` and prompts)."""
    lines: list[str] = []
    contact = bootstrap.contact
    if contact is not None:
        if contact.name:
            lines.append(f"Name: {contact.name}")
        lines.append(f"Phone: {contact.phone}")
        if contact.tags:
            lines.append(f"Tags: {', '.join(contact.tags)}")
        for key, value in sorted(contact.meta.items()):
            lines.append(f"{key}: {value}")
    else:
        call = bootstrap.call
        phone = call.from_number if call.direction.value == "inbound" else call.to_number
        if phone:
            lines.append(f"Phone: {phone}")
        lines.append("No saved contact record for this number.")
    campaign = bootstrap.campaign
    if campaign is not None:
        lines.append(f"Campaign: {campaign.name}")
        for key, value in sorted(campaign.vars.items()):
            lines.append(f"{key}: {value}")
    return "\n".join(lines)


def _build_end_call(state: CallState) -> FunctionTool[Any, Any]:
    language = language_name(state.bootstrap.profile.language)

    @function_tool(name=TOOL_END_CALL, flags=ToolFlag.IGNORE_ON_ENTER)
    async def end_call(ctx: RunContext) -> str:
        """End the phone call and hang up.

        Call this only when the conversation is clearly finished: the customer said goodbye,
        has no more questions, or asked to end the call. Do not call it when the customer
        asks to hold, to be transferred, or when their intent is unclear.
        """
        state.set_end_reason("hangup_agent")
        log.info("end_call requested by the LLM")

        def _on_speech_done(_: object) -> None:
            # The goodbye (tool reply) reuses this speech handle: shut down once it played.
            ctx.session.shutdown()

        ctx.speech_handle.add_done_callback(_on_speech_done)
        return (
            f"The call will end right after your next message. Say one short, polite goodbye "
            f"in {language} and nothing else."
        )

    return end_call


def _build_transfer_call(state: CallState, control: CallControl) -> FunctionTool[Any, Any]:
    number = state.bootstrap.profile.transfer_number

    @function_tool(name=TOOL_TRANSFER_CALL, flags=ToolFlag.IGNORE_ON_ENTER)
    async def transfer_call(ctx: RunContext, reason: str = "") -> None:
        """Transfer the caller to a human operator.

        Use it when the customer asks for a human, or when you cannot help them. Before
        calling it, tell the customer in one short sentence that you are connecting them.

        Args:
            reason: Short reason for the transfer, for the operator.
        """
        if not number:
            raise ToolError("Transfer is not available. Offer to help or schedule a callback.")
        await ctx.wait_for_playout()
        log.info("transferring call to %s (reason: %s)", number, reason or "-")
        # Mark before the REFER: the SIP participant may leave the room before it returns.
        claimed = state.set_end_reason("transferred")
        try:
            await control.transfer(number)
        except Exception as exc:
            if claimed and state.end_reason == "transferred":
                state.end_reason = None
            log.warning("SIP transfer to %s failed: %s", number, exc)
            raise ToolError(
                "The transfer failed. Apologise and offer to schedule a callback instead."
            ) from exc
        state.transferred_to = number
        ctx.session.shutdown(drain=False)
        raise StopResponse()

    return transfer_call


def _build_lookup_contact(state: CallState) -> FunctionTool[Any, Any]:
    @function_tool(name=TOOL_LOOKUP_CONTACT)
    async def lookup_contact(ctx: RunContext) -> str:
        """Look up what we know about the person on the call: name, phone, tags, notes and
        campaign variables."""
        return contact_summary(state.bootstrap)

    return lookup_contact


def _build_schedule_callback(state: CallState) -> FunctionTool[Any, Any]:
    @function_tool(name=TOOL_SCHEDULE_CALLBACK)
    async def schedule_callback(ctx: RunContext, when: str, note: str) -> str:
        """Schedule a callback when the customer asks to be called back later.

        Args:
            when: When to call back. Prefer an exact local date and time as
                "YYYY-MM-DDTHH:MM" computed from the current local time; otherwise the
                customer's words (e.g. "маргааш 10 цагт", "2 цагийн дараа").
            note: What the callback is about, one short sentence.
        """
        when = when.strip()
        if not when:
            raise ToolError("Ask the customer when they would like to be called back.")
        tz = state.tz
        now = state.clock()
        due = parse_callback_time(when, now, tz)
        if due is None:
            raise ToolError(
                f"Could not understand the time {when!r}. Ask the customer for a specific "
                "day and time (for example tomorrow at 10), then call this tool again."
            )
        if due < now - CALLBACK_PAST_TOLERANCE:
            raise ToolError("That time is in the past. Ask the customer for a future time.")
        if due > now + CALLBACK_MAX_AHEAD:
            raise ToolError("That is too far ahead. Ask for a time within the next few months.")
        state.callbacks.append(CallbackRequest(when=when, note=note.strip(), due_at=due))
        local = due.astimezone(tz).strftime("%Y-%m-%d %H:%M")
        log.info("callback scheduled: %s (due %s)", state.callbacks[-1].describe(), local)
        return (
            f"Callback scheduled for {local} local time ({when}). "
            "Confirm the day and time to the customer briefly."
        )

    return schedule_callback


def _build_request_operator(state: CallState) -> FunctionTool[Any, Any]:
    language = language_name(state.bootstrap.profile.language)

    @function_tool(name=TOOL_REQUEST_OPERATOR, flags=ToolFlag.IGNORE_ON_ENTER)
    async def request_operator(ctx: RunContext, reason: str = "") -> str:
        """Ask a human operator to join this call.

        Use it when the customer asks to talk to a person or an operator, or when you
        cannot help them. The operator joins this same call; stay with the customer until
        then. After the operator joins you will stay silent.

        Args:
            reason: Short reason for the operator, one sentence.
        """
        if state.handoff == "active":
            return "An operator is already on the call. Do not say anything more."
        if not state.request_handoff(reason.strip()):
            return (
                f"An operator was already requested. Reassure the customer briefly in "
                f"{language} that the operator will join soon."
            )
        log.info("operator requested by the LLM (reason: %s)", reason.strip() or "-")
        return (
            f"An operator has been notified and will join this call shortly. Tell the "
            f"customer in one short sentence in {language} that an operator will join soon "
            "and ask them to stay on the line; keep helping them meanwhile."
        )

    return request_operator


def outcome_table(outcomes: Iterable[CampaignOutcome]) -> str:
    """One ``- code: label — description`` line per outcome."""
    lines = []
    for o in outcomes:
        line = f"- {o.code}: {o.label or o.code}"
        if o.description.strip():
            line += f" — {' '.join(o.description.split())}"
        lines.append(line)
    return "\n".join(lines)


def _build_record_outcome(state: CallState) -> FunctionTool[Any, Any]:
    outcomes = state.outcomes
    codes = ", ".join(o.code for o in outcomes)
    description = (
        "Record the result of this call as soon as it is clear, for example when the "
        "customer explicitly agrees, declines, asks to be called back later or says it is "
        "the wrong number. You may call it again if the customer changes their mind; the "
        "last value counts. This is internal bookkeeping: never read the codes or labels "
        "aloud and do not tell the customer that you recorded anything.\n"
        "Possible outcomes (code: label — when to use it):\n" + outcome_table(outcomes)
    )

    async def record_outcome(ctx: RunContext, outcome_code: str, note: str) -> str:
        """Record the call outcome.

        Args:
            outcome_code: Exactly one of the outcome codes listed in the description.
            note: One short sentence in Mongolian (max 200 characters) explaining the outcome.
        """
        code = state.record_outcome(outcome_code, note)
        if not code:
            log.warning("record_outcome with unknown code %r", outcome_code)
            raise ToolError(f"Unknown outcome code {outcome_code!r}. Use one of: {codes}.")
        log.info("outcome recorded by the LLM: %s (%s)", code, state.outcome_note or "-")
        return (
            f"Outcome '{code}' recorded. Do not mention it to the customer; continue the "
            "conversation naturally, or say goodbye if it is finished."
        )

    return function_tool(record_outcome, name=TOOL_RECORD_OUTCOME, description=description)


def build_tools(
    state: CallState,
    control: CallControl,
    enabled: Iterable[str] | None = None,
) -> list[FunctionTool[Any, Any]]:
    """Tools listed in ``enabled`` (default: ``profile.tools``), in :data:`ALL_TOOLS` order,
    plus ``record_outcome`` whenever the campaign defines outcomes and ``request_operator``
    whenever operator handoff is enabled.

    Unknown names are ignored; ``transfer_call`` is skipped when no transfer number is set.
    """
    profile = state.bootstrap.profile
    wanted = {t.strip() for t in (profile.tools if enabled is None else enabled) if t.strip()}
    for unknown in sorted(wanted - set(ALL_TOOLS) - set(AUTO_TOOLS)):
        log.warning("profile %s enables unknown tool %r; ignoring", profile.id, unknown)

    tools: list[FunctionTool[Any, Any]] = []
    if TOOL_END_CALL in wanted:
        tools.append(_build_end_call(state))
    if TOOL_TRANSFER_CALL in wanted:
        if profile.transfer_number:
            tools.append(_build_transfer_call(state, control))
        else:
            log.warning("transfer_call enabled but profile %s has no transferNumber", profile.id)
    if TOOL_LOOKUP_CONTACT in wanted:
        tools.append(_build_lookup_contact(state))
    if TOOL_SCHEDULE_CALLBACK in wanted:
        tools.append(_build_schedule_callback(state))
    if state.outcomes:
        tools.append(_build_record_outcome(state))
    if state.bootstrap.handoff.enabled:
        tools.append(_build_request_operator(state))
    return tools
