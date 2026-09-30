from __future__ import annotations

import asyncio
import types
from collections import defaultdict
from collections.abc import Callable
from typing import Any
from uuid import UUID

import httpx
import pytest
from livekit import rtc
from livekit.agents import UserInputTranscribedEvent

from callgo_agent import routing
from callgo_agent.backend_client import BackendClient
from callgo_agent.routing import (
    AFTER_HOURS_MESSAGE,
    MENU_INVALID_MESSAGE,
    QUOTA_MESSAGE,
    KeyQueue,
    MenuFlow,
    SessionPromptPlayer,
    attach_dtmf,
    attach_spoken_keys,
    gate_for,
    menu_route,
    normalize_key,
    spoken_key,
)
from callgo_agent.schemas import (
    AgentProfile,
    Bootstrap,
    Call,
    CallDirection,
    CallStatus,
    EntitlementInfo,
    MenuOption,
    Organization,
    ResolvedRoute,
)

ORG = UUID("11111111-1111-1111-1111-111111111111")
CALL = UUID("22222222-2222-2222-2222-222222222222")
SALES = UUID(int=41)
SUPPORT = UUID(int=42)
OPTIONS = [
    MenuOption(key="1", label="Борлуулалт", agent_profile_id=SALES),
    MenuOption(key="2", label="Техникийн тусламж", agent_profile_id=SUPPORT),
]


def make_bootstrap(
    *,
    route: ResolvedRoute | None = None,
    can_start: bool = True,
    direction: CallDirection = CallDirection.INBOUND,
) -> Bootstrap:
    return Bootstrap(
        call=Call(id=CALL, org_id=ORG, direction=direction, status=CallStatus.RINGING),
        org=Organization(id=ORG, name="Demo", slug="demo"),
        profile=AgentProfile(id=UUID(int=3), org_id=ORG, name="Default"),
        route=route,
        entitlements=EntitlementInfo(can_start=can_start, reason="" if can_start else "quota"),
    )


def menu(**kw: Any) -> ResolvedRoute:
    base: dict[str, Any] = {
        "mode": "menu",
        "menu_prompt": "Борлуулалт бол 1, тусламж бол 2.",
        "menu": OPTIONS,
        "menu_timeout_sec": 0,
        "menu_repeat": 1,
    }
    base.update(kw)
    return ResolvedRoute(**base)


@pytest.fixture(autouse=True)
def fast_timeouts(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(routing, "MENU_TIMEOUT_MIN_SEC", 0.02)


# ---- gates ------------------------------------------------------------------------------


def test_gate_quota_exceeded_wins() -> None:
    gate = gate_for(make_bootstrap(can_start=False, route=ResolvedRoute(mode="after_hours")))
    assert gate is not None
    assert (gate.end_reason, gate.message) == ("quota_exceeded", QUOTA_MESSAGE)
    assert QUOTA_MESSAGE == "Уучлаарай, одоогоор үйлчилгээ авах боломжгүй байна."
    # outbound calls are gated by entitlements too
    assert gate_for(make_bootstrap(can_start=False, direction=CallDirection.OUTBOUND))


def test_gate_after_hours() -> None:
    gate = gate_for(make_bootstrap(route=ResolvedRoute(mode="after_hours", message=" Хаалттай. ")))
    assert gate is not None and (gate.end_reason, gate.message) == ("after_hours", "Хаалттай.")
    default = gate_for(make_bootstrap(route=ResolvedRoute(mode="after_hours")))
    assert default is not None and default.message == AFTER_HOURS_MESSAGE
    # an after-hours profile continues the call normally
    with_profile = ResolvedRoute(mode="after_hours", agent_profile_id=SALES, message="x")
    assert gate_for(make_bootstrap(route=with_profile)) is None


def test_no_gate_for_direct_menu_outbound_or_missing_route() -> None:
    assert gate_for(make_bootstrap()) is None
    assert gate_for(make_bootstrap(route=ResolvedRoute(mode="direct"))) is None
    assert gate_for(make_bootstrap(route=menu())) is None
    outbound = make_bootstrap(
        route=ResolvedRoute(mode="after_hours"), direction=CallDirection.OUTBOUND
    )
    assert gate_for(outbound) is None


def test_menu_route() -> None:
    assert menu_route(make_bootstrap(route=menu())) is not None
    assert menu_route(make_bootstrap(route=menu(menu=[]))) is None
    assert menu_route(make_bootstrap(route=ResolvedRoute(mode="direct"))) is None
    assert menu_route(make_bootstrap()) is None
    assert menu_route(make_bootstrap(route=menu(), direction=CallDirection.OUTBOUND)) is None


# ---- keys ------------------------------------------------------------------------------


def test_normalize_key() -> None:
    assert [normalize_key(k) for k in ("1", " 9 ", "*", "#", "0")] == ["1", "9", "*", "#", "0"]
    assert [normalize_key(k) for k in ("", "12", "a", "A")] == ["", "", "", ""]


@pytest.mark.parametrize(
    ("text", "key"),
    [
        ("1", "1"),
        ("Хоёр.", "2"),
        ("хоёрыг дарна", "2"),
        ("гурав", "3"),
        ("нэгдүгээр", "1"),
        ("тэг", "0"),
        ("есөн", "9"),
        ("од", "*"),
        ("Борлуулалт", "1"),
        ("техникийн тусламж хэрэгтэй", "2"),
        ("", ""),
        ("сайн байна уу", ""),
        ("би нэг юм асуух гэсэн юм аа", ""),  # a long sentence is not a menu answer
    ],
)
def test_spoken_key(text: str, key: str) -> None:
    assert spoken_key(text, OPTIONS) == key


async def test_key_queue() -> None:
    q = KeyQueue()
    assert q.push("5") and q.push("#", "speech")
    assert not q.push("x")
    assert await q.get() == ("5", "dtmf")
    q.clear()
    assert q._queue.empty()


class FakeEmitter:
    def __init__(self) -> None:
        self.handlers: dict[str, list[Callable[..., None]]] = defaultdict(list)

    def on(self, name: str, cb: Callable[..., None]) -> None:
        self.handlers[name].append(cb)

    def off(self, name: str, cb: Callable[..., None]) -> None:
        self.handlers[name].remove(cb)

    def emit(self, name: str, *args: Any) -> None:
        for cb in list(self.handlers[name]):
            cb(*args)


async def test_attach_dtmf_filters_caller_and_detaches() -> None:
    room, q = FakeEmitter(), KeyQueue()
    detach = attach_dtmf(room, q, "sip_caller")
    caller = types.SimpleNamespace(identity="sip_caller")
    other = types.SimpleNamespace(identity="op-1")
    room.emit("sip_dtmf_received", rtc.SipDTMF(code=3, digit="3", participant=other))  # type: ignore[arg-type]
    room.emit("sip_dtmf_received", rtc.SipDTMF(code=4, digit="4", participant=caller))  # type: ignore[arg-type]
    room.emit("sip_dtmf_received", rtc.SipDTMF(code=5, digit="5", participant=None))
    assert await q.get() == ("4", "dtmf")
    assert await q.get() == ("5", "dtmf")  # server-sent digits (no participant) count
    detach()
    detach()
    assert room.handlers["sip_dtmf_received"] == []


async def test_attach_spoken_keys_uses_final_transcripts() -> None:
    session, q = FakeEmitter(), KeyQueue()
    detach = attach_spoken_keys(session, q, OPTIONS)
    session.emit(
        "user_input_transcribed", UserInputTranscribedEvent(transcript="хоёр", is_final=False)
    )
    session.emit(
        "user_input_transcribed", UserInputTranscribedEvent(transcript="юу?", is_final=True)
    )
    session.emit(
        "user_input_transcribed", UserInputTranscribedEvent(transcript="хоёр", is_final=True)
    )
    assert await q.get() == ("2", "speech")
    assert q._queue.empty()
    detach()
    assert session.handlers["user_input_transcribed"] == []


# ---- menu flow ---------------------------------------------------------------------------


class Player:
    """Records prompts; ``hold`` keeps a prompt 'playing' until cancelled or released."""

    def __init__(self, *, hold: bool = False, fail: bool = False) -> None:
        self.said: list[str] = []
        self.cancelled = 0
        self.hold = hold
        self.fail = fail
        self.on_say: Callable[[str], None] | None = None

    async def __call__(self, text: str) -> None:
        self.said.append(text)
        if self.on_say is not None:
            self.on_say(text)
        if self.fail:
            raise RuntimeError("tts down")
        if self.hold:
            try:
                await asyncio.Event().wait()
            except asyncio.CancelledError:
                self.cancelled += 1
                raise


async def test_menu_key_after_prompt_selects_option() -> None:
    q, player = KeyQueue(), Player()
    route = menu(menu_timeout_sec=5)
    player.on_say = lambda _t: asyncio.get_running_loop().call_later(0.01, q.push, "2")
    result = await MenuFlow(route, player, q).run()
    assert result.option is not None and result.option.agent_profile_id == SUPPORT
    assert (result.reason, result.key, result.source, result.attempts) == (
        "selected",
        "2",
        "dtmf",
        1,
    )
    assert player.said == [route.menu_prompt]


async def test_menu_key_during_prompt_interrupts_it() -> None:
    q, player = KeyQueue(), Player(hold=True)
    player.on_say = lambda _t: q.push("1", "speech")
    result = await asyncio.wait_for(MenuFlow(menu(), player, q).run(), 1)
    assert result.option is not None and result.option.key == "1" and result.source == "speech"
    assert player.cancelled == 1


async def test_menu_timeout_repeats_then_falls_back() -> None:
    q, player = KeyQueue(), Player()
    result = await MenuFlow(menu(menu_repeat=2), player, q).run()
    assert result.option is None and result.reason == "timeout" and result.attempts == 3
    assert player.said == [menu().menu_prompt] * 3


async def test_menu_unknown_key_repeats_prompt() -> None:
    q, player = KeyQueue(), Player()
    keys = iter(["9", "1"])
    player.on_say = lambda t: q.push(next(keys)) if t != MENU_INVALID_MESSAGE else None
    result = await MenuFlow(menu(menu_timeout_sec=5), player, q).run()
    assert result.option is not None and result.option.key == "1" and result.attempts == 2
    assert player.said == [menu().menu_prompt, MENU_INVALID_MESSAGE, menu().menu_prompt]


async def test_menu_unknown_key_on_last_attempt_falls_back() -> None:
    q, player = KeyQueue(), Player()
    player.on_say = lambda _t: q.push("7")
    result = await MenuFlow(menu(menu_repeat=0, menu_timeout_sec=5), player, q).run()
    assert result.option is None and player.said == [menu().menu_prompt]


async def test_menu_keeps_listening_when_speech_fails() -> None:
    q, player = KeyQueue(), Player(fail=True)
    player.on_say = lambda _t: asyncio.get_running_loop().call_later(0.005, q.push, "1")
    result = await MenuFlow(menu(menu_timeout_sec=5), player, q).run()
    assert result.option is not None and result.option.key == "1"


def test_menu_prompt_generated_from_labels() -> None:
    flow = MenuFlow(menu(menu_prompt=""), Player(), KeyQueue())
    text = flow.prompt_text()
    assert "Борлуулалт бол 1" in text and "Техникийн тусламж бол 2" in text
    assert flow.attempts == 2
    assert MenuFlow(menu(menu_repeat=-3), Player(), KeyQueue()).attempts == 1
    assert MenuFlow(menu(menu_timeout_sec=8), Player(), KeyQueue()).timeout == 8


class FakeHandle:
    def __init__(self, delay: float) -> None:
        self.delay = delay
        self.interrupts: list[bool] = []

    async def wait_for_playout(self) -> None:
        await asyncio.sleep(self.delay)

    def interrupt(self, *, force: bool = False) -> None:
        self.interrupts.append(force)


class FakeSession:
    def __init__(self, delay: float = 0.0) -> None:
        self.delay = delay
        self.handles: list[FakeHandle] = []
        self.said: list[tuple[str, dict[str, Any]]] = []

    def say(self, text: str, **kw: Any) -> FakeHandle:
        self.said.append((text, kw))
        self.handles.append(FakeHandle(self.delay))
        return self.handles[-1]


async def test_session_prompt_player_interrupts_on_cancel() -> None:
    session = FakeSession(delay=10)
    player = SessionPromptPlayer(session)
    task = asyncio.create_task(player("Цэс"))
    await asyncio.sleep(0.01)
    task.cancel()
    with pytest.raises(asyncio.CancelledError):
        await task
    assert session.said == [("Цэс", {"allow_interruptions": True})]
    assert session.handles[0].interrupts == [True]

    quick = FakeSession()
    await SessionPromptPlayer(quick)("Сайн уу")
    assert quick.handles[0].interrupts == []


async def test_backend_bootstrap_sends_profile_id() -> None:
    seen: list[httpx.QueryParams] = []
    body = make_bootstrap().model_dump(mode="json")

    def handler(request: httpx.Request) -> httpx.Response:
        seen.append(request.url.params)
        return httpx.Response(200, json=body)

    async with BackendClient(
        "http://backend", "tok", transport=httpx.MockTransport(handler)
    ) as client:
        await client.bootstrap(room="call-1", call_id=CALL, profile_id=SALES)
        await client.bootstrap(room="call-1")
    assert seen[0]["profileId"] == str(SALES) and seen[0]["callId"] == str(CALL)
    assert "profileId" not in seen[1]
