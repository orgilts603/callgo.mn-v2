from __future__ import annotations

import uuid
from collections.abc import AsyncIterator
from datetime import UTC, datetime

import pytest
from aiohttp.test_utils import TestClient, TestServer

from callgo_agent import mock_backend as mb
from callgo_agent.schemas import Bootstrap

TOKEN = "t0ken"
H = {"X-Agent-Token": TOKEN}


@pytest.fixture
async def client() -> AsyncIterator[TestClient]:
    app = mb.create_app(token=TOKEN, env={}, quiet=True)
    async with TestClient(TestServer(app)) as c:
        yield c


def _event(call_id: uuid.UUID, type_: str = "transcript.final", payload: dict | None = None):
    return {
        "id": str(uuid.uuid4()),
        "type": type_,
        "orgId": str(mb.DEMO_ORG_ID),
        "callId": str(call_id),
        "at": datetime.now(UTC).isoformat(),
        "payload": payload or {},
    }


async def test_auth_required(client: TestClient):
    for method, path in [
        ("GET", "/internal/agent/bootstrap"),
        ("POST", "/internal/agent/events"),
        ("POST", "/internal/agent/lexicon-hit"),
    ]:
        r = await client.request(method, path)
        assert r.status == 401
        assert (await r.json())["error"]["code"] == "unauthorized"
        r = await client.request(method, path, headers={"X-Agent-Token": "wrong"})
        assert r.status == 401


async def test_bootstrap_camelcase_and_parses(client: TestClient):
    r = await client.get(
        "/internal/agent/bootstrap",
        params={"room": "call-xyz", "direction": "outbound", "from": "+97699000000"},
        headers=H,
    )
    assert r.status == 200
    data = await r.json()
    assert {"call", "org", "sipNumber", "profile", "llm", "llmFallbacks", "lexicon"} <= set(data)
    assert data["call"]["roomName"] == "call-xyz"
    assert data["call"]["direction"] == "outbound"
    assert "systemPrompt" in data["profile"] and "system_prompt" not in data["profile"]
    assert data["profile"]["language"] == "mn"
    assert "apiKey" in data["llm"] and "baseUrl" in data["llm"]
    assert data["contact"]["phone"] == "+97699000000"
    assert data["lexicon"][0]["wrong"]
    b = Bootstrap.model_validate(data)
    assert b.profile.greeting


def test_llm_from_env_defaults_and_keys():
    d = mb.llm_from_env({})
    assert d.provider.value == "openai_compatible"
    assert d.base_url == "http://localhost:11434/v1" and d.model == "qwen2.5"
    o = mb.llm_from_env({"OPENAI_API_KEY": "sk-x"})
    assert o.provider.value == "openai" and o.api_key == "sk-x"
    g = mb.llm_from_env({"GOOGLE_API_KEY": "g-x"})
    assert g.provider.value == "google" and g.model.startswith("gemini")
    f = mb.llm_from_env({"OPENAI_API_KEY": "k", "MOCK_LLM_PROVIDER": "google", "GOOGLE_API_KEY": "g"})
    assert f.provider.value == "google"


async def test_events_accepted_stored_and_dumped(client: TestClient):
    cid = uuid.uuid4()
    batch = {
        "events": [
            _event(cid, "agent.state", {"state": "listening", "llmModel": "x/y"}),
            _event(cid, "transcript.final", {"turn": {"speaker": "customer", "text": "сайн уу"}}),
        ]
    }
    r = await client.post("/internal/agent/events", json=batch, headers=H)
    assert r.status == 200
    assert await r.json() == {"accepted": 2}

    dump = await (await client.get("/mock/events")).json()
    assert dump["total"] == 2
    ev = dump["items"][0]
    assert set(ev) >= {"id", "type", "orgId", "callId", "at", "payload"}
    assert ev["callId"] == str(cid)
    only = await (await client.get("/mock/events", params={"type": "agent.state"})).json()
    assert only["total"] == 1


async def test_events_validation_error(client: TestClient):
    r = await client.post("/internal/agent/events", json={"events": [{"type": "nope"}]}, headers=H)
    assert r.status == 400
    r = await client.post("/internal/agent/events", data="not json", headers=H)
    assert r.status == 400


async def test_lexicon_hit(client: TestClient):
    i = str(uuid.uuid4())
    r = await client.post("/internal/agent/lexicon-hit", json={"ids": [i]}, headers=H)
    assert r.status == 204
    assert (await (await client.get("/mock/events")).json())["lexiconHits"] == [i]
    r = await client.post("/internal/agent/lexicon-hit", json={"ids": ["bad"]}, headers=H)
    assert r.status == 400
