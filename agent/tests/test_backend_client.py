from __future__ import annotations

import json
from datetime import UTC, datetime
from uuid import UUID, uuid4

import httpx
import pytest
import respx

from callgo_agent.backend_client import BackendClient, BackendError
from callgo_agent.schemas import CallDirection, Event, Speaker, TranscriptTurn

BASE = "http://backend.test"
ORG = UUID("11111111-1111-1111-1111-111111111111")
CALL = UUID("22222222-2222-2222-2222-222222222222")
PROFILE = UUID("33333333-3333-3333-3333-333333333333")
LLM_ID = UUID("44444444-4444-4444-4444-444444444444")


async def _no_sleep(_: float) -> None:
    return None


def make_client(**kw: object) -> BackendClient:
    return BackendClient(BASE, "secret-token", sleep=_no_sleep, **kw)  # type: ignore[arg-type]


BOOTSTRAP_JSON = {
    "call": {
        "id": str(CALL),
        "orgId": str(ORG),
        "direction": "inbound",
        "status": "ringing",
        "fromNumber": "+97699112233",
        "toNumber": "+97677001100",
        "roomName": "call-abc",
    },
    "org": {"id": str(ORG), "name": "Demo LLC", "slug": "demo"},
    "sipNumber": {"id": str(uuid4()), "orgId": str(ORG), "number": "+97677001100"},
    "profile": {
        "id": str(PROFILE),
        "orgId": str(ORG),
        "name": "Sara",
        "systemPrompt": "You are Sara.",
        "greeting": "Сайн байна уу",
        "language": "mn",
        "maxDurationSec": 300,
        "tools": ["end_call"],
        "transferNumber": "+97611223344",
    },
    "llm": {
        "id": str(LLM_ID),
        "orgId": str(ORG),
        "name": "Gemini",
        "provider": "google",
        "model": "gemini-2.5-flash",
        "apiKey": "k-123",
    },
    "llmFallbacks": [],
    "lexicon": [{"wrong": "калл го", "correct": "CallGo", "scope": "stt", "phonetic": ""}],
    "contact": None,
    "campaign": None,
}


@respx.mock(base_url=BASE, assert_all_called=True)
async def test_bootstrap_sends_auth_and_query_and_parses_camel_case(
    respx_mock: respx.MockRouter,
) -> None:
    route = respx_mock.get("/internal/agent/bootstrap").mock(
        return_value=httpx.Response(200, json=BOOTSTRAP_JSON)
    )
    client = make_client()
    try:
        boot = await client.bootstrap(
            room="call-abc",
            sip_number="+97677001100",
            from_number="+97699112233",
            to_number="+97677001100",
            direction=CallDirection.INBOUND,
            call_id=CALL,
        )
    finally:
        await client.aclose()

    req = route.calls.last.request
    assert req.headers["X-Agent-Token"] == "secret-token"
    assert dict(req.url.params) == {
        "room": "call-abc",
        "sipNumber": "+97677001100",
        "from": "+97699112233",
        "to": "+97677001100",
        "direction": "inbound",
        "callId": str(CALL),
    }
    assert boot.call.id == CALL
    assert boot.profile.system_prompt == "You are Sara."
    assert boot.profile.max_duration_sec == 300
    assert boot.profile.transfer_number == "+97611223344"
    assert boot.llm is not None and boot.llm.api_key == "k-123"
    assert boot.sip_number is not None and boot.sip_number.number == "+97677001100"
    assert boot.lexicon[0].correct == "CallGo"


@respx.mock(base_url=BASE)
async def test_bootstrap_omits_empty_params(respx_mock: respx.MockRouter) -> None:
    route = respx_mock.get("/internal/agent/bootstrap").mock(
        return_value=httpx.Response(200, json=BOOTSTRAP_JSON)
    )
    async with make_client() as client:
        await client.bootstrap(room="call-x", direction="outbound")
    assert dict(route.calls.last.request.url.params) == {"room": "call-x", "direction": "outbound"}


@respx.mock(base_url=BASE)
async def test_bootstrap_invalid_body_raises_backend_error(respx_mock: respx.MockRouter) -> None:
    respx_mock.get("/internal/agent/bootstrap").mock(
        return_value=httpx.Response(200, json={"call": {}})
    )
    async with make_client() as client:
        with pytest.raises(BackendError):
            await client.bootstrap(room="r")


@respx.mock(base_url=BASE)
async def test_post_events_serializes_camel_case(respx_mock: respx.MockRouter) -> None:
    route = respx_mock.post("/internal/agent/events").mock(
        return_value=httpx.Response(200, json={"accepted": 2})
    )
    turn = TranscriptTurn(
        call_id=CALL, speaker=Speaker.CUSTOMER, text="сайн", raw_text="сайн уу", start_ms=10
    )
    at = datetime(2026, 9, 30, 10, 0, tzinfo=UTC)
    events = [
        Event(
            id="e1",
            type="transcript.final",
            org_id=ORG,
            call_id=CALL,
            at=at,
            payload={"turn": turn.model_dump(mode="json", by_alias=True, exclude_none=True)},
        ),
        Event(id="e2", type="agent.state", org_id=ORG, call_id=CALL, at=at, payload={}),
    ]
    async with make_client() as client:
        accepted = await client.post_events(events)

    assert accepted == 2
    req = route.calls.last.request
    assert req.headers["X-Agent-Token"] == "secret-token"
    body = json.loads(req.content)
    first = body["events"][0]
    assert set(first) == {"id", "type", "orgId", "callId", "at", "payload"}
    assert first["orgId"] == str(ORG)
    assert first["callId"] == str(CALL)
    assert first["at"].startswith("2026-09-30T10:00:00")
    assert first["payload"]["turn"]["rawText"] == "сайн уу"
    assert first["payload"]["turn"]["startMs"] == 10
    assert first["payload"]["turn"]["isFinal"] is True
    assert "id" not in first["payload"]["turn"]


async def test_post_events_empty_is_noop() -> None:
    with respx.mock(base_url=BASE, assert_all_called=False) as router:
        route = router.post("/internal/agent/events")
        async with make_client() as client:
            assert await client.post_events([]) == 0
        assert not route.called


@respx.mock(base_url=BASE)
async def test_retries_5xx_then_succeeds(respx_mock: respx.MockRouter) -> None:
    route = respx_mock.post("/internal/agent/events").mock(
        side_effect=[
            httpx.Response(503),
            httpx.Response(502),
            httpx.Response(200, json={"accepted": 1}),
        ]
    )
    ev = Event(id="e", type="system", org_id=ORG, at=datetime.now(UTC))
    async with make_client(max_retries=3) as client:
        assert await client.post_events([ev]) == 1
    assert route.call_count == 3


@respx.mock(base_url=BASE)
async def test_retries_connection_errors_then_gives_up(respx_mock: respx.MockRouter) -> None:
    route = respx_mock.post("/internal/agent/lexicon-hit").mock(
        side_effect=httpx.ConnectError("refused")
    )
    delays: list[float] = []

    async def record(d: float) -> None:
        delays.append(d)

    client = BackendClient(BASE, "t", max_retries=2, backoff_base=0.1, sleep=record)
    try:
        with pytest.raises(BackendError):
            await client.lexicon_hit([uuid4()])
    finally:
        await client.aclose()
    assert route.call_count == 3
    assert len(delays) == 2
    assert 0.05 <= delays[0] <= 0.1 <= delays[1] <= 0.2  # exponential backoff with jitter


@respx.mock(base_url=BASE)
async def test_4xx_is_not_retried(respx_mock: respx.MockRouter) -> None:
    route = respx_mock.get("/internal/agent/bootstrap").mock(
        return_value=httpx.Response(401, json={"error": "bad token"})
    )
    async with make_client() as client:
        with pytest.raises(BackendError) as info:
            await client.bootstrap(room="r")
    assert info.value.status_code == 401
    assert route.call_count == 1


@respx.mock(base_url=BASE)
async def test_lexicon_hit_posts_ids(respx_mock: respx.MockRouter) -> None:
    route = respx_mock.post("/internal/agent/lexicon-hit").mock(return_value=httpx.Response(204))
    a, b = uuid4(), uuid4()
    async with make_client() as client:
        await client.lexicon_hit([a, str(b)])
        await client.lexicon_hit([])
    assert route.call_count == 1
    assert json.loads(route.calls.last.request.content) == {"ids": [str(a), str(b)]}


async def test_defaults_come_from_settings(monkeypatch: pytest.MonkeyPatch) -> None:
    from callgo_agent import backend_client

    monkeypatch.setattr(backend_client.settings, "backend_url", "http://x.test:9/")
    monkeypatch.setattr(backend_client.settings, "agent_token", "tok")
    with respx.mock(base_url="http://x.test:9") as router:
        route = router.post("/internal/agent/lexicon-hit").mock(return_value=httpx.Response(204))
        async with BackendClient() as client:
            assert client.base_url == "http://x.test:9"
            await client.lexicon_hit(["a"])
    assert route.calls.last.request.headers["X-Agent-Token"] == "tok"
