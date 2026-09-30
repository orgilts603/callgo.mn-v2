from __future__ import annotations

from collections.abc import Callable
from typing import Any
from uuid import UUID

import pytest
from livekit import api as lk_api
from livekit.agents.llm import StopResponse, ToolError, ToolFlag
from livekit.agents.llm.utils import build_legacy_openai_schema

from callgo_agent.schemas import (
    AgentProfile,
    Bootstrap,
    Call,
    CallDirection,
    CallStatus,
    CampaignInfo,
    Contact,
    Organization,
)
from callgo_agent.tools import (
    ALL_TOOLS,
    CallState,
    LiveKitCallControl,
    build_tools,
    contact_summary,
)

ORG = UUID("11111111-1111-1111-1111-111111111111")
CALL = UUID("22222222-2222-2222-2222-222222222222")


def make_bootstrap(
    *,
    tools: list[str] | None = None,
    transfer_number: str = "",
    contact: Contact | None = None,
    campaign: CampaignInfo | None = None,
    direction: CallDirection = CallDirection.INBOUND,
) -> Bootstrap:
    return Bootstrap(
        call=Call(
            id=CALL,
            org_id=ORG,
            direction=direction,
            status=CallStatus.RINGING,
            from_number="+97699112233",
            to_number="+97677001100",
        ),
        org=Organization(id=ORG, name="Demo LLC", slug="demo"),
        profile=AgentProfile(
            id=UUID(int=3),
            org_id=ORG,
            name="Sara",
            tools=tools or [],
            transfer_number=transfer_number,
        ),
        contact=contact,
        campaign=campaign,
    )


class FakeControl:
    def __init__(self, fail: bool = False) -> None:
        self.fail = fail
        self.transfers: list[str] = []
        self.hangups = 0

    async def hangup(self) -> None:
        self.hangups += 1

    async def transfer(self, to: str) -> None:
        self.transfers.append(to)
        if self.fail:
            raise RuntimeError("REFER rejected")


class FakeSpeechHandle:
    def __init__(self) -> None:
        self.callbacks: list[Callable[[Any], None]] = []

    def add_done_callback(self, cb: Callable[[Any], None]) -> None:
        self.callbacks.append(cb)


class FakeSession:
    def __init__(self) -> None:
        self.shutdowns: list[dict[str, Any]] = []

    def shutdown(self, **kw: Any) -> None:
        self.shutdowns.append(kw)


class FakeRunContext:
    def __init__(self) -> None:
        self.session = FakeSession()
        self.speech_handle = FakeSpeechHandle()
        self.playout_waited = False

    async def wait_for_playout(self) -> None:
        self.playout_waited = True


def names(tools: list[Any]) -> list[str]:
    return [t.info.name for t in tools]


def by_name(tools: list[Any], name: str) -> Any:
    return next(t for t in tools if t.info.name == name)


def test_tools_are_gated_by_profile() -> None:
    state = CallState(bootstrap=make_bootstrap(tools=["lookup_contact", "end_call", "bogus"]))
    assert names(build_tools(state, FakeControl())) == ["end_call", "lookup_contact"]


def test_no_tools_when_profile_has_none() -> None:
    assert build_tools(CallState(bootstrap=make_bootstrap()), FakeControl()) == []


def test_transfer_requires_number() -> None:
    state = CallState(bootstrap=make_bootstrap(tools=["transfer_call"]))
    assert build_tools(state, FakeControl()) == []
    state = CallState(bootstrap=make_bootstrap(tools=["transfer_call"], transfer_number="+97611"))
    assert names(build_tools(state, FakeControl())) == ["transfer_call"]


def test_explicit_enabled_overrides_profile_and_schemas_hide_context() -> None:
    state = CallState(bootstrap=make_bootstrap(transfer_number="+97611"))
    tools = build_tools(state, FakeControl(), enabled=ALL_TOOLS)
    assert names(tools) == list(ALL_TOOLS)

    schemas = {t.info.name: build_legacy_openai_schema(t)["function"] for t in tools}
    assert schemas["end_call"]["parameters"].get("properties", {}) == {}
    assert set(schemas["schedule_callback"]["parameters"]["properties"]) == {"when", "note"}
    assert set(schemas["schedule_callback"]["parameters"]["required"]) == {"when", "note"}
    assert set(schemas["transfer_call"]["parameters"]["properties"]) == {"reason"}
    assert "hang up" in schemas["end_call"]["description"]
    assert by_name(tools, "end_call").info.flags & ToolFlag.IGNORE_ON_ENTER


async def test_end_call_sets_reason_and_shuts_down_after_goodbye() -> None:
    state = CallState(bootstrap=make_bootstrap(tools=["end_call"]))
    tool = build_tools(state, FakeControl())[0]
    ctx = FakeRunContext()

    reply = await tool(ctx)

    assert "goodbye" in reply and "Mongolian" in reply
    assert state.end_reason == "hangup_agent"
    assert ctx.session.shutdowns == []  # not before the goodbye is spoken
    assert len(ctx.speech_handle.callbacks) == 1
    ctx.speech_handle.callbacks[0](ctx.speech_handle)
    assert ctx.session.shutdowns == [{}]


async def test_transfer_success() -> None:
    state = CallState(bootstrap=make_bootstrap(tools=["transfer_call"], transfer_number="+97611"))
    control = FakeControl()
    tool = build_tools(state, control)[0]
    ctx = FakeRunContext()

    with pytest.raises(StopResponse):
        await tool(ctx, reason="wants a human")

    assert ctx.playout_waited
    assert control.transfers == ["+97611"]
    assert state.end_reason == "transferred"
    assert state.transferred_to == "+97611"
    assert ctx.session.shutdowns == [{"drain": False}]


async def test_transfer_failure_raises_tool_error_and_keeps_call() -> None:
    state = CallState(bootstrap=make_bootstrap(tools=["transfer_call"], transfer_number="+97611"))
    tool = build_tools(state, FakeControl(fail=True))[0]
    ctx = FakeRunContext()

    with pytest.raises(ToolError):
        await tool(ctx)

    assert state.end_reason is None
    assert ctx.session.shutdowns == []


async def test_lookup_contact_includes_contact_and_campaign() -> None:
    contact = Contact(
        id=UUID(int=9),
        org_id=ORG,
        phone="+97699112233",
        name="Бат",
        tags=["vip"],
        meta={"order": "A-17"},
    )
    campaign = CampaignInfo(id=UUID(int=8), name="Autumn", vars={"discount": "20%"})
    state = CallState(
        bootstrap=make_bootstrap(tools=["lookup_contact"], contact=contact, campaign=campaign)
    )
    text = await build_tools(state, FakeControl())[0](FakeRunContext())
    for expected in (
        "Name: Бат",
        "Phone: +97699112233",
        "Tags: vip",
        "order: A-17",
        "Campaign: Autumn",
        "discount: 20%",
    ):
        assert expected in text


def test_contact_summary_without_contact_uses_caller_number() -> None:
    text = contact_summary(make_bootstrap())
    assert "Phone: +97699112233" in text
    assert "No saved contact" in text
    outbound = contact_summary(make_bootstrap(direction=CallDirection.OUTBOUND))
    assert "Phone: +97677001100" in outbound


async def test_schedule_callback_records_request() -> None:
    state = CallState(bootstrap=make_bootstrap(tools=["schedule_callback"]))
    tool = build_tools(state, FakeControl())[0]
    reply = await tool(FakeRunContext(), when=" маргааш 10:00 ", note="Үнийн санал ")
    assert "маргааш 10:00" in reply
    assert len(state.callbacks) == 1
    assert state.callbacks[0].when == "маргааш 10:00"
    assert state.callbacks[0].describe() == "маргааш 10:00 — Үнийн санал"

    with pytest.raises(ToolError):
        await tool(FakeRunContext(), when="  ", note="x")
    assert len(state.callbacks) == 1


def test_first_end_reason_wins() -> None:
    state = CallState(bootstrap=make_bootstrap())
    assert state.set_end_reason("max_duration")
    assert not state.set_end_reason("hangup_customer")
    assert state.end_reason == "max_duration"


class _FakeSip:
    def __init__(self) -> None:
        self.requests: list[lk_api.TransferSIPParticipantRequest] = []

    async def transfer_sip_participant(self, req: lk_api.TransferSIPParticipantRequest) -> None:
        self.requests.append(req)


class _FakeRoomSvc:
    def __init__(self, error: Exception | None = None) -> None:
        self.deleted: list[str] = []
        self.error = error

    async def delete_room(self, req: lk_api.DeleteRoomRequest) -> None:
        self.deleted.append(req.room)
        if self.error:
            raise self.error


class _FakeAPI:
    def __init__(self, room_error: Exception | None = None) -> None:
        self.sip = _FakeSip()
        self.room = _FakeRoomSvc(room_error)


async def test_livekit_call_control_builds_requests() -> None:
    fake = _FakeAPI()
    control = LiveKitCallControl(fake, "call-1", "sip_+976991")  # type: ignore[arg-type]
    await control.transfer("+97611223344")
    await control.transfer("sip:op@pbx.local")
    await control.hangup()
    first, second = fake.sip.requests
    assert first.room_name == "call-1"
    assert first.participant_identity == "sip_+976991"
    assert first.transfer_to == "tel:+97611223344"
    assert second.transfer_to == "sip:op@pbx.local"
    assert fake.room.deleted == ["call-1"]


async def test_hangup_ignores_missing_room() -> None:
    err = lk_api.TwirpError(lk_api.TwirpErrorCode.NOT_FOUND, "room not found", status=404)
    control = LiveKitCallControl(_FakeAPI(room_error=err), "gone", "x")  # type: ignore[arg-type]
    await control.hangup()
