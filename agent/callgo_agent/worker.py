"""LiveKit ``AgentServer`` for CallGo: explicit dispatch as ``settings.agent_name``.

Job processes are started with ``forkserver``, so the entrypoint and prewarm function are
module-level (picklable by reference). Plugin modules are imported here, at import time on
the main thread, as LiveKit requires.
"""

from __future__ import annotations

import asyncio
import importlib
import logging
from typing import Any

from livekit.agents import AgentServer, JobContext, JobProcess
from livekit.plugins import silero

from .backend_client import BackendClient
from .config import settings
from .session import load_telephony_vad, run_call, turn_detector_choice

log = logging.getLogger("callgo.worker")

SHUTDOWN_PROCESS_TIMEOUT_SEC = 60.0  # room for post-call analysis + final event flush
OPTIONAL_MODULES = (
    "callgo_agent.llm_router",
    "callgo_agent.pipeline",
    "callgo_agent.normalizer",
)


def preload_modules() -> list[str]:
    """Import pipeline modules (and their LiveKit plugins) on the main thread."""
    loaded: list[str] = []
    for name in OPTIONAL_MODULES:
        try:
            importlib.import_module(name)
            loaded.append(name)
        except ImportError as exc:
            log.warning("optional module %s not importable: %s", name, exc)
    if turn_detector_choice() == "multilingual":
        try:
            importlib.import_module("livekit.plugins.turn_detector.multilingual")
            loaded.append("livekit.plugins.turn_detector.multilingual")
        except ImportError as exc:
            log.warning("turn detector plugin not importable: %s", exc)
    return loaded


preload_modules()


def prewarm(proc: JobProcess) -> None:
    """Runs once per job process before it accepts a job: load the Silero VAD model."""
    proc.userdata["vad"] = load_telephony_vad(silero)


async def entrypoint(ctx: JobContext) -> None:
    await run_call(ctx, BackendClient(), close_client=True)


def _start_http_server(server: AgentServer) -> asyncio.Task[Any] | None:
    """Serve ``/health`` and ``/test-llm`` on ``settings.http_port`` in the main process."""
    try:
        from .http_server import WorkerStatus, start_http_server
    except ImportError as exc:
        log.warning("worker HTTP server unavailable: %s", exc)
        return None

    class _ServerStatus(WorkerStatus):
        """``activeJobs`` read live from the AgentServer's process pool."""

        @property  # type: ignore[override]
        def active_jobs(self) -> int:
            try:
                return len(server.active_jobs)
            except Exception:  # noqa: BLE001 - pool not ready yet
                return 0

        @active_jobs.setter
        def active_jobs(self, value: int) -> None:
            pass

    async def _serve() -> Any:
        try:
            return await start_http_server(_ServerStatus())
        except OSError as exc:
            log.error("cannot bind worker HTTP server on :%d: %s", settings.http_port, exc)
            return None

    return asyncio.get_running_loop().create_task(_serve(), name="callgo-http-server")


def build_server(*, http: bool = True) -> AgentServer:
    server = AgentServer(
        ws_url=settings.livekit_url,
        api_key=settings.livekit_api_key,
        api_secret=settings.livekit_api_secret,
        setup_fnc=prewarm,
        shutdown_process_timeout=SHUTDOWN_PROCESS_TIMEOUT_SEC,
    )
    server.rtc_session(entrypoint, agent_name=settings.agent_name)
    if http:
        tasks: list[asyncio.Task[Any]] = []

        def _on_started() -> None:
            task = _start_http_server(server)
            if task is not None:
                tasks.append(task)  # keep a reference for the process lifetime

        server.on("worker_started", _on_started)
    return server


server = build_server()
