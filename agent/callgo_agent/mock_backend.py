"""Fake Go backend (internal agent API) for local dev and tests.

Run: ``python -m callgo_agent.mock_backend --port 8080``

Serves ``/internal/agent/{bootstrap,events,lexicon-hit,knowledge/search}`` as documented in
``docs/API.md`` plus ``GET /mock/events`` to inspect what the agent posted.
"""

from __future__ import annotations

import argparse
import logging
import os
import sys
import uuid
from datetime import UTC, datetime
from typing import Any

from aiohttp import web
from pydantic import ValidationError

from .config import settings
from .schemas import (
    AgentProfile,
    Bootstrap,
    Call,
    CallDirection,
    CallStatus,
    Contact,
    EventBatch,
    LexiconEntry,
    LexiconScope,
    LLMConfig,
    LLMProvider,
    Organization,
    SIPNumber,
)

log = logging.getLogger("callgo.mock_backend")

DEMO_ORG_ID = uuid.UUID("00000000-0000-4000-8000-000000000001")
DEMO_PROFILE_ID = uuid.UUID("00000000-0000-4000-8000-000000000002")
DEMO_LLM_ID = uuid.UUID("00000000-0000-4000-8000-000000000003")
DEMO_SIP_ID = uuid.UUID("00000000-0000-4000-8000-000000000004")
DEMO_CONTACT_ID = uuid.UUID("00000000-0000-4000-8000-000000000005")

DEMO_SYSTEM_PROMPT = (
    "Та CallGo Demo компанийн дуут туслах. Монгол хэлээр эелдэг, товч, ойлгомжтой хариулна уу. "
    "Хариулт нэг-хоёр өгүүлбэрээс хэтрэхгүй байг. Тоо, цагийг үгээр бичнэ."
)
DEMO_GREETING = "Сайн байна уу, CallGo Demo-д тавтай морил. Би танд юугаар туслах вэ?"

# (provider, env var holding the key, model, base url)
_PROVIDER_ENV: list[tuple[LLMProvider, str, str, str]] = [
    (LLMProvider.OPENAI, "OPENAI_API_KEY", "gpt-4o-mini", ""),
    (LLMProvider.GOOGLE, "GOOGLE_API_KEY", "gemini-2.5-flash", ""),
    (LLMProvider.ANTHROPIC, "ANTHROPIC_API_KEY", "claude-haiku-4-5", ""),
    (LLMProvider.GROQ, "GROQ_API_KEY", "llama-3.3-70b-versatile", ""),
]


def llm_from_env(env: dict[str, str] | None = None) -> LLMConfig:
    """Pick the LLM config from env.

    ``MOCK_LLM_PROVIDER`` / ``MOCK_LLM_MODEL`` / ``MOCK_LLM_BASE_URL`` / ``MOCK_LLM_API_KEY``
    override everything; otherwise the first provider with an API key in the environment wins;
    with no keys at all we fall back to a local Ollama-style OpenAI-compatible endpoint.
    """
    e = dict(os.environ if env is None else env)
    provider: LLMProvider | None = None
    model = ""
    api_key = ""
    base_url = ""

    forced = e.get("MOCK_LLM_PROVIDER", "").strip()
    if forced:
        provider = LLMProvider(forced)
        for p, key_env, default_model, default_url in _PROVIDER_ENV:
            if p == provider:
                api_key, model, base_url = e.get(key_env, ""), default_model, default_url
        if provider in (LLMProvider.OLLAMA, LLMProvider.OPENAI_COMPATIBLE):
            base_url = "http://localhost:11434/v1"
            model = "qwen2.5"
    else:
        for p, key_env, default_model, default_url in _PROVIDER_ENV:
            if e.get(key_env):
                provider, api_key, model, base_url = p, e[key_env], default_model, default_url
                break
    if provider is None:
        provider = LLMProvider.OPENAI_COMPATIBLE
        model, base_url = "qwen2.5", "http://localhost:11434/v1"
        api_key = "ollama"

    model = e.get("MOCK_LLM_MODEL", model) or model
    base_url = e.get("MOCK_LLM_BASE_URL", base_url)
    api_key = e.get("MOCK_LLM_API_KEY", api_key)
    return LLMConfig(
        id=DEMO_LLM_ID,
        org_id=DEMO_ORG_ID,
        name=f"Mock {provider.value}",
        provider=provider,
        model=model,
        base_url=base_url,
        api_key=api_key,
        temperature=0.4,
        max_tokens=512,
        is_default=True,
    )


def demo_lexicon() -> list[LexiconEntry]:
    ns = uuid.UUID("00000000-0000-4000-8000-0000000000a0")
    rows = [
        ("кол гоу", "CallGo", LexiconScope.STT, ""),
        ("колл го", "CallGo", LexiconScope.STT, ""),
        ("CallGo", "кол гоу", LexiconScope.TTS, "кол гоу"),
        ("Сүхбаатар", "Сүхбаатар", LexiconScope.BOTH, "сүхбаатар"),
    ]
    return [
        LexiconEntry(id=uuid.uuid5(ns, w), wrong=w, correct=c, scope=s, phonetic=p)
        for w, c, s, p in rows
    ]


def build_bootstrap(query: dict[str, str], env: dict[str, str] | None = None) -> Bootstrap:
    now = datetime.now(UTC)
    room = query.get("room", "call-demo")
    direction = (
        CallDirection.OUTBOUND if query.get("direction") == "outbound" else CallDirection.INBOUND
    )
    call_id = uuid.UUID(query["callId"]) if query.get("callId") else uuid.uuid4()
    from_number = query.get("from", "+97699112233")
    to_number = query.get("to") or query.get("sipNumber", "+97677001122")
    call = Call(
        id=call_id,
        org_id=DEMO_ORG_ID,
        contact_id=DEMO_CONTACT_ID,
        sip_number_id=DEMO_SIP_ID,
        agent_profile_id=DEMO_PROFILE_ID,
        direction=direction,
        status=CallStatus.ACTIVE,
        from_number=from_number,
        to_number=to_number,
        room_name=room,
        started_at=now,
        answered_at=now,
    )
    return Bootstrap(
        call=call,
        org=Organization(id=DEMO_ORG_ID, name="CallGo Demo", slug="demo"),
        sip_number=SIPNumber(
            id=DEMO_SIP_ID,
            org_id=DEMO_ORG_ID,
            number=to_number,
            label="Demo line",
            agent_profile_id=DEMO_PROFILE_ID,
        ),
        profile=AgentProfile(
            id=DEMO_PROFILE_ID,
            org_id=DEMO_ORG_ID,
            name="Demo assistant",
            system_prompt=DEMO_SYSTEM_PROMPT,
            greeting=DEMO_GREETING,
            language="mn",
            llm_config_id=DEMO_LLM_ID,
            tools=["end_call", "transfer_call"],
            transfer_number="+97677001199",
        ),
        llm=llm_from_env(env),
        llm_fallbacks=[],
        lexicon=demo_lexicon(),
        contact=Contact(
            id=DEMO_CONTACT_ID,
            org_id=DEMO_ORG_ID,
            phone=from_number,
            name="Бат",
            tags=["demo"],
            meta={"city": "Улаанбаатар"},
        ),
        campaign=None,
    )


# ---- aiohttp app ----------------------------------------------------------------

TOKEN_KEY = web.AppKey("token", str)
EVENTS_KEY = web.AppKey("events", list)
HITS_KEY = web.AppKey("lexicon_hits", list)
ENV_KEY = web.AppKey("env", dict)
QUIET_KEY = web.AppKey("quiet", bool)


def _error(status: int, code: str, message: str) -> web.Response:
    return web.json_response({"error": {"code": code, "message": message}}, status=status)


@web.middleware
async def _auth(request: web.Request, handler: Any) -> web.StreamResponse:
    if (
        request.path.startswith("/internal/")
        and request.headers.get("X-Agent-Token", "") != request.app[TOKEN_KEY]
    ):
        return _error(401, "unauthorized", "invalid or missing X-Agent-Token")
    return await handler(request)


async def bootstrap(request: web.Request) -> web.Response:
    try:
        b = build_bootstrap(dict(request.query), request.app[ENV_KEY] or None)
    except ValueError as exc:
        return _error(400, "invalid", str(exc))
    if not request.app[QUIET_KEY]:
        log.info(
            "bootstrap room=%s llm=%s/%s",
            b.call.room_name,
            b.llm and b.llm.provider.value,
            b.llm and b.llm.model,
        )
    return web.json_response(b.model_dump(mode="json", by_alias=True))


def _event_line(ev: dict[str, Any]) -> str:
    p = ev.get("payload", {})
    detail = ""
    if ev["type"] == "transcript.final":
        turn = p.get("turn", {})
        detail = f" {turn.get('speaker', '?')}: {turn.get('text', '')!r}"
    elif ev["type"] == "agent.state":
        detail = f" state={p.get('state')} model={p.get('llmModel', '')}"
    elif ev["type"] == "call.ended":
        detail = f" reason={p.get('endReason')} dur={p.get('durationSec')}s"
    elif ev["type"] == "transcript.partial":
        detail = f" {p.get('speaker', '?')}: {p.get('text', '')!r}"
    return f"[{ev['at']}] {ev['type']} call={str(ev.get('callId'))[:8]}{detail}"


async def post_events(request: web.Request) -> web.Response:
    try:
        raw = await request.json()
        batch = EventBatch.model_validate(raw)
    except ValueError as exc:  # JSONDecodeError and pydantic ValidationError are ValueErrors
        msg = str(exc) if not isinstance(exc, ValidationError) else exc.errors()[0]["msg"]
        return _error(400, "invalid", f"bad event batch: {msg}")
    dumped = [e.model_dump(mode="json", by_alias=True) for e in batch.events]
    request.app[EVENTS_KEY].extend(dumped)
    if not request.app[QUIET_KEY]:
        for ev in dumped:
            print(_event_line(ev), flush=True)
    return web.json_response({"accepted": len(dumped)})


async def post_lexicon_hit(request: web.Request) -> web.Response:
    try:
        body = await request.json()
        ids = [str(uuid.UUID(str(i))) for i in body["ids"]]
    except (ValueError, KeyError, TypeError):
        return _error(400, "invalid", "expected {ids: [uuid]}")
    request.app[HITS_KEY].extend(ids)
    return web.Response(status=204)


# A tiny in-memory knowledge base so tool-mode lookups work against the mock.
MOCK_KNOWLEDGE: list[dict[str, str]] = [
    {
        "filename": "price-list.md",
        "heading": "Интернэтийн багц",
        "content": "100 Мбит багц сарын 49,900 төгрөг. 300 Мбит багц сарын 79,900 төгрөг. "
        "Суурилуулалт үнэгүй, гэрээ 12 сар.",
    },
    {
        "filename": "policy.md",
        "heading": "Буцаалтын нөхцөл",
        "content": "Төхөөрөмжийг 14 хоногийн дотор бүрэн бүтэн буцаавал төлбөрийг 100% буцаана. "
        "14 хоногоос хойш буцаалт хийгдэхгүй.",
    },
    {
        "filename": "hours.md",
        "heading": "Ажлын цаг",
        "content": "Салбар Даваа-Баасан 09:00-18:00, Бямба 10:00-15:00 ажиллана. Ням амарна. "
        "Дуудлагын төв 24 цаг ажиллана.",
    },
]


async def post_knowledge_search(request: web.Request) -> web.Response:
    try:
        body = await request.json()
        query = str(body["query"]).strip()
        k = int(body.get("k", 5) or 5)
    except (ValueError, KeyError, TypeError):
        return _error(400, "invalid", "expected {knowledgeBaseId, query, k}")
    if not query:
        return _error(400, "invalid", "query is required")
    words = [w for w in query.casefold().split() if len(w) > 2]
    hits: list[dict[str, Any]] = []
    for i, doc in enumerate(MOCK_KNOWLEDGE):
        text = (doc["heading"] + " " + doc["content"]).casefold()
        score = sum(1 for w in words if w[:4] in text) / max(len(words), 1)
        if score > 0:
            hits.append(
                {
                    "chunkId": str(uuid.uuid5(uuid.NAMESPACE_URL, f"chunk-{i}")),
                    "documentId": str(uuid.uuid5(uuid.NAMESPACE_URL, f"doc-{i}")),
                    "filename": doc["filename"],
                    "heading": doc["heading"],
                    "content": doc["content"],
                    "score": round(min(score, 1.0), 3),
                }
            )
    hits.sort(key=lambda h: -h["score"])
    return web.json_response({"hits": hits[:k]})


async def dump_events(request: web.Request) -> web.Response:
    events: list[dict[str, Any]] = request.app[EVENTS_KEY]
    if t := request.query.get("type"):
        events = [e for e in events if e["type"] == t]
    return web.json_response(
        {"items": events, "total": len(events), "lexiconHits": request.app[HITS_KEY]}
    )


async def clear_events(request: web.Request) -> web.Response:
    request.app[EVENTS_KEY].clear()
    request.app[HITS_KEY].clear()
    return web.Response(status=204)


def create_app(
    token: str | None = None, env: dict[str, str] | None = None, quiet: bool = False
) -> web.Application:
    app = web.Application(middlewares=[_auth])
    app[TOKEN_KEY] = settings.agent_token if token is None else token
    app[EVENTS_KEY] = []
    app[HITS_KEY] = []
    app[ENV_KEY] = env or {}
    app[QUIET_KEY] = quiet
    app.router.add_get("/internal/agent/bootstrap", bootstrap)
    app.router.add_post("/internal/agent/events", post_events)
    app.router.add_post("/internal/agent/lexicon-hit", post_lexicon_hit)
    app.router.add_post("/internal/agent/knowledge/search", post_knowledge_search)
    app.router.add_get("/mock/events", dump_events)
    app.router.add_delete("/mock/events", clear_events)
    return app


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="CallGo mock backend (internal agent API)")
    ap.add_argument("--host", default="0.0.0.0")
    ap.add_argument("--port", type=int, default=8080)
    ap.add_argument("--token", default=None, help="X-Agent-Token (default CALLGO_AGENT_TOKEN)")
    args = ap.parse_args(argv)
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(name)s %(message)s")
    llm = llm_from_env()
    log.info(
        "mock backend on http://%s:%d  llm=%s/%s base=%s",
        args.host,
        args.port,
        llm.provider.value,
        llm.model,
        llm.base_url or "-",
    )
    web.run_app(create_app(args.token), host=args.host, port=args.port, print=None)
    return 0


if __name__ == "__main__":
    sys.exit(main())
