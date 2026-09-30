"""Worker HTTP server (default port ``settings.http_port`` = 8090).

* ``GET /health``    -> ``{ok, workerId, activeJobs}``
* ``POST /test-llm`` -> body ``{config: LLMConfig, prompt}`` -> ``{ok, reply, latencyMs, error}``

The worker process creates one :class:`WorkerStatus`, increments it around each job and
calls :func:`start_http_server`. Standalone: ``python -m callgo_agent.http_server``.
"""

from __future__ import annotations

import argparse
import asyncio
import contextlib
import inspect
import logging
import os
import socket
import sys
from collections.abc import Iterator
from typing import Any

from aiohttp import web
from pydantic import ValidationError

from .config import settings
from .schemas import LLMConfig

log = logging.getLogger("callgo.http_server")

DEFAULT_TEST_PROMPT = "Say hello in one short sentence."


def _default_worker_id() -> str:
    return f"{socket.gethostname()}-{os.getpid()}"


class WorkerStatus:
    """Shared, process-local worker state read by ``/health``.

    The core agent (entrypoint) does::

        status = WorkerStatus(worker_id=...)     # once, in main
        await start_http_server(status)
        ...
        with status.job():                       # around each job's lifetime
            await run_session(...)
        # or status.job_started() / status.job_finished() explicitly

    Everything runs on the worker's single event loop, so no locking is needed.
    """

    def __init__(self, worker_id: str | None = None) -> None:
        self.worker_id: str = worker_id or _default_worker_id()
        self.active_jobs: int = 0
        self.total_jobs: int = 0

    def job_started(self) -> int:
        self.active_jobs += 1
        self.total_jobs += 1
        return self.active_jobs

    def job_finished(self) -> int:
        self.active_jobs = max(0, self.active_jobs - 1)
        return self.active_jobs

    @contextlib.contextmanager
    def job(self) -> Iterator[None]:
        self.job_started()
        try:
            yield
        finally:
            self.job_finished()

    def as_dict(self) -> dict[str, Any]:
        return {"ok": True, "workerId": self.worker_id, "activeJobs": self.active_jobs}


STATUS_KEY = web.AppKey("status", WorkerStatus)


async def health(request: web.Request) -> web.Response:
    return web.json_response(request.app[STATUS_KEY].as_dict())


def _bad(status: int, message: str) -> web.Response:
    return web.json_response(
        {"ok": False, "reply": "", "latencyMs": 0, "error": message}, status=status
    )


async def handle_test_llm(request: web.Request) -> web.Response:
    try:
        body = await request.json()
        if not isinstance(body, dict):
            raise TypeError("body must be a JSON object")
        config = LLMConfig.model_validate(body.get("config"))
    except ValidationError as exc:
        first = exc.errors()[0]
        loc = ".".join(str(p) for p in first["loc"])
        return _bad(400, f"invalid config: {loc}: {first['msg']}")
    except (ValueError, TypeError) as exc:
        return _bad(400, f"invalid request: {exc}")
    prompt = str(body.get("prompt") or DEFAULT_TEST_PROMPT)

    try:
        from . import llm_router  # lazy: heavy plugins, built by another module

        result = llm_router.test_config(config, prompt)
        if inspect.isawaitable(result):
            result = await result
    except Exception as exc:  # noqa: BLE001 - provider/SDK failures must not 500 the worker
        log.warning("test-llm failed: %s", exc)
        return web.json_response(
            {"ok": False, "reply": "", "latencyMs": 0, "error": f"{type(exc).__name__}: {exc}"}
        )

    return web.json_response(
        {
            "ok": bool(getattr(result, "ok", False)),
            "reply": getattr(result, "reply", "") or "",
            "latencyMs": int(getattr(result, "latency_ms", 0) or 0),
            "error": getattr(result, "error", None) or "",
        }
    )


def create_app(status: WorkerStatus) -> web.Application:
    app = web.Application()
    app[STATUS_KEY] = status
    app.router.add_get("/health", health)
    app.router.add_post("/test-llm", handle_test_llm)
    return app


async def start_http_server(
    status: WorkerStatus, host: str = "0.0.0.0", port: int | None = None
) -> web.AppRunner:
    """Start serving and return the runner (call ``await runner.cleanup()`` to stop)."""
    runner = web.AppRunner(create_app(status), access_log=None)
    await runner.setup()
    p = settings.http_port if port is None else port
    site = web.TCPSite(runner, host, p)
    await site.start()
    log.info("worker http server listening on %s:%d", host, p)
    return runner


async def _serve(host: str, port: int) -> None:
    runner = await start_http_server(WorkerStatus(), host, port)
    try:
        await asyncio.Event().wait()
    finally:
        await runner.cleanup()


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="CallGo worker HTTP server (standalone)")
    ap.add_argument("--host", default="0.0.0.0")
    ap.add_argument("--port", type=int, default=settings.http_port)
    args = ap.parse_args(argv)
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(name)s %(message)s")
    with contextlib.suppress(KeyboardInterrupt):
        asyncio.run(_serve(args.host, args.port))
    return 0


if __name__ == "__main__":
    sys.exit(main())
