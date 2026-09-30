"""LLM function tools for a CallGo call, enabled per ``AgentProfile.tools``.

* ``end_call``          say goodbye, then hang up (end reason ``hangup_agent``)
* ``transfer_call``     SIP REFER the caller to ``profile.transfer_number`` (``transferred``)
* ``lookup_contact``    contact record + campaign variables as text
* ``schedule_callback`` remember a requested callback; reported in the call summary
* ``record_outcome``    commit the campaign outcome mid-call; auto-enabled (not via
  ``profile.tools``) whenever the campaign defines outcomes

Tools only touch :class:`CallState` (per-call mutable data shared with the session) and a
:class:`CallControl` (hang-up / transfer), so they are testable without LiveKit.
"""

from __future__ import annotations

import logging
from collections.abc import Iterable
from dataclasses import dataclass, field
from datetime import datetime
from typing import Any, Protocol

from livekit import api as lk_api
from livekit.agents.llm import FunctionTool, StopResponse, ToolError, ToolFlag, function_tool
from livekit.agents.voice import RunContext

from .events import utcnow
from .schemas import Bootstrap, CampaignOutcome, EndReason

log = logging.getLogger("callgo.tools")

TOOL_END_CALL = "end_call"
TOOL_TRANSFER_CALL = "transfer_call"
TOOL_LOOKUP_CONTACT = "lookup_contact"
TOOL_SCHEDULE_CALLBACK = "schedule_callback"
TOOL_RECORD_OUTCOME = "record_outcome"
ALL_TOOLS: tuple[str, ...] = (
    TOOL_END_CALL,
    TOOL_TRANSFER_CALL,
    TOOL_LOOKUP_CONTACT,
    TOOL_SCHEDULE_CALLBACK,
)
# Enabled by the campaign (outcomes defined), never by ``profile.tools``.
AUTO_TOOLS: tuple[str, ...] = (TOOL_RECORD_OUTCOME,)

OUTCOME_NOTE_MAX_CHARS = 200

_LANGUAGE_NAMES = {"mn": "Mongolian", "en": "English", "ru": "Russian"}


def language_name(code: str) -> str:
    return _LANGUAGE_NAMES.get(code.lower().split("-")[0], code) if code else "Mongolian"


@dataclass(slots=True)
class CallbackRequest:
    when: str
    note: str
    requested_at: datetime = field(default_factory=utcnow)

    def describe(self) -> str:
        return f"{self.when} — {self.note}" if self.note else self.when


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
            when: When to call back, as the customer said it (e.g. "tomorrow 10:00").
            note: What the callback is about, one short sentence.
        """
        when = when.strip()
        if not when:
            raise ToolError("Ask the customer when they would like to be called back.")
        state.callbacks.append(CallbackRequest(when=when, note=note.strip()))
        log.info("callback scheduled: %s", state.callbacks[-1].describe())
        return f"Callback scheduled for {when}. Confirm it to the customer briefly."

    return schedule_callback


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
    plus ``record_outcome`` whenever the campaign defines outcomes.

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
    return tools
