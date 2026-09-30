from __future__ import annotations

import sys
import types
import uuid
from collections.abc import AsyncIterator
from dataclasses import dataclass

import pytest
from aiohttp.test_utils import TestClient, TestServer

from callgo_agent import http_server as hs


@dataclass
class _Result:
    ok: bool
    reply: str
    latency_ms: int
    error: str | None = None


def _config(**over):
    cfg = {
        "id": str(uuid.uuid4()),
        "orgId": str(uuid.uuid4()),
        "name": "t",
        "provider": "openai",
        "model": "gpt-4o-mini",
        "apiKey": "sk-test",
    }
    cfg.update(over)
    return cfg


@pytest.fixture
def status() -> hs.WorkerStatus:
    return hs.WorkerStatus(worker_id="w-1")


@pytest.fixture
async def client(status: hs.WorkerStatus) -> AsyncIterator[TestClient]:
    async with TestClient(TestServer(hs.create_app(status))) as c:
        yield c


@pytest.fixture
def fake_router(monkeypatch: pytest.MonkeyPatch) -> types.ModuleType:
    mod = types.ModuleType("callgo_agent.llm_router")
    monkeypatch.setitem(sys.modules, "callgo_agent.llm_router", mod)
    import callgo_agent

    monkeypatch.setattr(callgo_agent, "llm_router", mod, raising=False)
    return mod


def test_worker_status_counters():
    s = hs.WorkerStatus("x")
    assert s.job_started() == 1
    assert s.job_started() == 2
    assert s.job_finished() == 1
    with s.job():
        assert s.active_jobs == 2
    assert s.active_jobs == 1
    s.job_finished()
    s.job_finished()  # never negative
    assert s.active_jobs == 0 and s.total_jobs == 3


async def test_health_reflects_active_jobs(client: TestClient, status: hs.WorkerStatus):
    assert await (await client.get("/health")).json() == {
        "ok": True,
        "workerId": "w-1",
        "activeJobs": 0,
    }
    status.job_started()
    assert (await (await client.get("/health")).json())["activeJobs"] == 1


async def test_test_llm_ok_async(client: TestClient, fake_router: types.ModuleType):
    seen = {}

    async def test_config(config, prompt):
        seen["model"], seen["prompt"], seen["key"] = config.model, prompt, config.api_key
        return _Result(True, "сайн", 42)

    fake_router.test_config = test_config
    r = await client.post("/test-llm", json={"config": _config(), "prompt": "hi"})
    assert r.status == 200
    assert await r.json() == {"ok": True, "reply": "сайн", "latencyMs": 42, "error": ""}
    assert seen == {"model": "gpt-4o-mini", "prompt": "hi", "key": "sk-test"}


async def test_test_llm_failure_result_and_sync_impl(
    client: TestClient, fake_router: types.ModuleType
):
    fake_router.test_config = lambda config, prompt: _Result(False, "", 5, "401 unauthorized")
    body = await (await client.post("/test-llm", json={"config": _config(), "prompt": "x"})).json()
    assert body["ok"] is False and body["error"] == "401 unauthorized" and body["latencyMs"] == 5


async def test_test_llm_router_exception_is_reported(
    client: TestClient, fake_router: types.ModuleType
):
    async def boom(config, prompt):
        raise RuntimeError("provider down")

    fake_router.test_config = boom
    r = await client.post("/test-llm", json={"config": _config()})
    assert r.status == 200
    body = await r.json()
    assert body["ok"] is False and "provider down" in body["error"]


async def test_test_llm_bad_requests(client: TestClient):
    r = await client.post("/test-llm", json={"prompt": "x"})
    assert r.status == 400 and (await r.json())["ok"] is False
    r = await client.post("/test-llm", json={"config": _config(provider="nope")})
    assert r.status == 400
    r = await client.post("/test-llm", data="{oops")
    assert r.status == 400
    r = await client.post("/test-llm", json=[1])
    assert r.status == 400


async def test_start_http_server_runner(status: hs.WorkerStatus):
    import aiohttp

    runner = await hs.start_http_server(status, host="127.0.0.1", port=0)
    try:
        port = runner.addresses[0][1]
        async with aiohttp.ClientSession() as s, s.get(f"http://127.0.0.1:{port}/health") as r:
            assert (await r.json())["workerId"] == "w-1"
    finally:
        await runner.cleanup()
