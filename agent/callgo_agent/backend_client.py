"""Async HTTP client for the Go backend's internal agent API (docs/API.md).

Endpoints (all under ``/internal/agent``, header ``X-Agent-Token``):

* ``GET  /bootstrap``   -> :class:`~callgo_agent.schemas.Bootstrap`
* ``POST /events``      ``{"events": [Event...]}`` -> ``{"accepted": N}``
* ``POST /lexicon-hit`` ``{"ids": [uuid]}`` -> 204
* ``POST /knowledge/search`` ``{"knowledgeBaseId", "query", "k"}`` -> ``{"hits": [KnowledgeHit]}``

Transient failures (connection errors, timeouts, HTTP 429 and 5xx) are retried with
exponential backoff and jitter; other 4xx responses fail fast with :class:`BackendError`.
"""

from __future__ import annotations

import asyncio
import logging
import random
from collections.abc import Awaitable, Callable, Iterable, Mapping, Sequence
from types import TracebackType
from typing import Any, Self
from uuid import UUID

import httpx
from pydantic import ValidationError

from .config import settings
from .schemas import Bootstrap, CallDirection, Event, EventBatch, KnowledgeHit

log = logging.getLogger("callgo.backend_client")

TOKEN_HEADER = "X-Agent-Token"
BOOTSTRAP_PATH = "/internal/agent/bootstrap"
EVENTS_PATH = "/internal/agent/events"
LEXICON_HIT_PATH = "/internal/agent/lexicon-hit"
KNOWLEDGE_SEARCH_PATH = "/internal/agent/knowledge/search"

DEFAULT_TIMEOUT = httpx.Timeout(10.0, connect=3.0)
# Knowledge search runs while the caller waits on the line: fail fast, per attempt.
KNOWLEDGE_SEARCH_TIMEOUT = httpx.Timeout(2.5, connect=1.0)
RETRY_STATUS = frozenset({408, 425, 429, 500, 502, 503, 504})


class BackendError(RuntimeError):
    """The backend rejected a request or stayed unreachable after all retries."""

    def __init__(self, message: str, *, status_code: int | None = None) -> None:
        super().__init__(message)
        self.status_code = status_code


def _is_retryable_status(status: int) -> bool:
    return status in RETRY_STATUS or status >= 500


class BackendClient:
    """Thin typed wrapper around the backend's ``/internal/agent/*`` endpoints."""

    def __init__(
        self,
        base_url: str | None = None,
        token: str | None = None,
        *,
        timeout: float | httpx.Timeout = DEFAULT_TIMEOUT,
        max_retries: int = 3,
        backoff_base: float = 0.25,
        backoff_max: float = 4.0,
        transport: httpx.AsyncBaseTransport | None = None,
        sleep: Callable[[float], Awaitable[None]] = asyncio.sleep,
    ) -> None:
        self._base_url = (base_url or settings.backend_url).rstrip("/")
        self._max_retries = max(0, max_retries)
        self._backoff_base = backoff_base
        self._backoff_max = backoff_max
        self._sleep = sleep
        self._http = httpx.AsyncClient(
            base_url=self._base_url,
            headers={
                TOKEN_HEADER: token if token is not None else settings.agent_token,
                "Accept": "application/json",
                "User-Agent": "callgo-agent/0.1",
            },
            timeout=timeout,
            transport=transport,
        )

    @property
    def base_url(self) -> str:
        return self._base_url

    async def __aenter__(self) -> Self:
        return self

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc: BaseException | None,
        tb: TracebackType | None,
    ) -> None:
        await self.aclose()

    async def aclose(self) -> None:
        await self._http.aclose()

    # ---- endpoints -----------------------------------------------------------------

    async def bootstrap(
        self,
        room: str,
        sip_number: str = "",
        from_number: str = "",
        to_number: str = "",
        direction: CallDirection | str = CallDirection.INBOUND,
        call_id: UUID | str | None = None,
    ) -> Bootstrap:
        """Resolve the call's org, profile, LLM chain, lexicon, contact and campaign."""
        params: dict[str, str] = {"room": room}
        if sip_number:
            params["sipNumber"] = sip_number
        if from_number:
            params["from"] = from_number
        if to_number:
            params["to"] = to_number
        params["direction"] = CallDirection(direction).value
        if call_id:
            params["callId"] = str(call_id)

        resp = await self._request("GET", BOOTSTRAP_PATH, params=params)
        try:
            return Bootstrap.model_validate(resp.json())
        except (ValueError, ValidationError) as exc:
            raise BackendError(f"invalid bootstrap response: {exc}") from exc

    async def post_events(self, events: Sequence[Event]) -> int:
        """Post a batch of live events; returns the number the backend accepted."""
        if not events:
            return 0
        body = EventBatch(events=list(events)).model_dump(mode="json", by_alias=True)
        resp = await self._request("POST", EVENTS_PATH, json=body)
        accepted: Any = None
        try:
            data = resp.json() if resp.content else {}
            if isinstance(data, Mapping):
                accepted = data.get("accepted")
        except ValueError:
            pass
        return int(accepted) if isinstance(accepted, int) else len(events)

    async def lexicon_hit(self, ids: Iterable[UUID | str]) -> None:
        """Increment hit counters for lexicon corrections applied to STT output."""
        id_list = [str(i) for i in ids if i]
        if not id_list:
            return
        await self._request("POST", LEXICON_HIT_PATH, json={"ids": id_list})

    async def knowledge_search(
        self, knowledge_base_id: UUID, query: str, k: int = 5
    ) -> list[KnowledgeHit]:
        """Hybrid search over a knowledge base; best hits first (scores in ``[0, 1]``)."""
        body = {"knowledgeBaseId": str(knowledge_base_id), "query": query, "k": k}
        resp = await self._request(
            "POST", KNOWLEDGE_SEARCH_PATH, json=body, timeout=KNOWLEDGE_SEARCH_TIMEOUT
        )
        try:
            data = resp.json()
        except ValueError as exc:
            raise BackendError(f"invalid knowledge search response: {exc}") from exc
        raw_hits = data.get("hits") if isinstance(data, Mapping) else None
        if raw_hits is None:
            return []
        if not isinstance(raw_hits, list):
            raise BackendError("invalid knowledge search response: 'hits' is not a list")
        try:
            return [KnowledgeHit.model_validate(h) for h in raw_hits]
        except ValidationError as exc:
            raise BackendError(f"invalid knowledge search response: {exc}") from exc

    # ---- transport -----------------------------------------------------------------

    def _backoff(self, attempt: int) -> float:
        delay = min(self._backoff_max, self._backoff_base * (2**attempt))
        return delay * (0.5 + random.random() / 2)

    async def _request(
        self,
        method: str,
        path: str,
        *,
        params: Mapping[str, str] | None = None,
        json: Any = None,
        timeout: float | httpx.Timeout | None = None,
    ) -> httpx.Response:
        extra: dict[str, Any] = {} if timeout is None else {"timeout": timeout}
        attempt = 0
        while True:
            try:
                resp = await self._http.request(method, path, params=params, json=json, **extra)
            except httpx.TransportError as exc:
                if attempt >= self._max_retries:
                    raise BackendError(
                        f"{method} {path} failed after {attempt + 1} attempts: {exc!r}"
                    ) from exc
                delay = self._backoff(attempt)
                log.warning(
                    "backend %s %s transport error (%r), retrying in %.2fs",
                    method,
                    path,
                    exc,
                    delay,
                )
            else:
                if resp.is_success:
                    return resp
                if not _is_retryable_status(resp.status_code) or attempt >= self._max_retries:
                    raise BackendError(
                        f"{method} {path} -> HTTP {resp.status_code}: {resp.text[:300]}",
                        status_code=resp.status_code,
                    )
                delay = self._backoff(attempt)
                log.warning(
                    "backend %s %s -> HTTP %d, retrying in %.2fs",
                    method,
                    path,
                    resp.status_code,
                    delay,
                )
            attempt += 1
            await self._sleep(delay)
