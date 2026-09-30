"""Tests for callgo_agent.llm_router and llm_catalog (no network)."""

from __future__ import annotations

import asyncio
import logging
import uuid
from typing import Any

import pytest
from livekit.agents import llm as lk_llm
from livekit.agents import APIConnectionError
from livekit.agents.types import (
    DEFAULT_API_CONNECT_OPTIONS,
    NOT_GIVEN,
    APIConnectOptions,
    NotGivenOr,
)
from livekit.plugins import anthropic as lk_anthropic
from livekit.plugins import google as lk_google
from livekit.plugins import groq as lk_groq
from livekit.plugins import openai as lk_openai

from callgo_agent import llm_catalog, llm_router
from callgo_agent.llm_router import LLMConfigError, LLMRouter
from callgo_agent.schemas import LLMConfig, LLMProvider

ORG = uuid.uuid4()


def make_cfg(provider: str, model: str = "m", **kw: Any) -> LLMConfig:
    kw.setdefault("id", uuid.uuid4())
    return LLMConfig(org_id=ORG, name="test", provider=provider, model=model, **kw)


@pytest.fixture(autouse=True)
def _clean_env(monkeypatch: pytest.MonkeyPatch) -> None:
    for var in (
        "OPENAI_API_KEY",
        "ANTHROPIC_API_KEY",
        "GOOGLE_API_KEY",
        "GROQ_API_KEY",
        "GOOGLE_GENAI_USE_VERTEXAI",
    ):
        monkeypatch.delenv(var, raising=False)


# ---- fakes ------------------------------------------------------------------


class _FakeStream(lk_llm.LLMStream):
    def __init__(
        self,
        llm: FakeLLM,
        *,
        chat_ctx: lk_llm.ChatContext,
        conn_options: APIConnectOptions,
    ) -> None:
        super().__init__(llm, chat_ctx=chat_ctx, tools=[], conn_options=conn_options)
        self._fake = llm

    async def _run(self) -> None:
        self._fake.prompts.append(self.chat_ctx.items[-1].text_content or "")
        if self._fake.delay:
            await asyncio.sleep(self._fake.delay)
        if self._fake.error is not None:
            raise self._fake.error
        for piece in self._fake.pieces:
            self._event_ch.send_nowait(
                lk_llm.ChatChunk(
                    id="fake", delta=lk_llm.ChoiceDelta(role="assistant", content=piece)
                )
            )


class FakeLLM(lk_llm.LLM):
    def __init__(
        self,
        pieces: list[str] | None = None,
        *,
        error: Exception | None = None,
        delay: float = 0.0,
    ) -> None:
        super().__init__()
        self.pieces = pieces or []
        self.error = error
        self.delay = delay
        self.prompts: list[str] = []
        self.closed = False

    @property
    def model(self) -> str:
        return "fake"

    def chat(
        self,
        *,
        chat_ctx: lk_llm.ChatContext,
        tools: list[lk_llm.Tool] | None = None,
        conn_options: APIConnectOptions = DEFAULT_API_CONNECT_OPTIONS,
        parallel_tool_calls: NotGivenOr[bool] = NOT_GIVEN,
        tool_choice: NotGivenOr[lk_llm.ToolChoice] = NOT_GIVEN,
        extra_kwargs: NotGivenOr[dict[str, Any]] = NOT_GIVEN,
    ) -> lk_llm.LLMStream:
        return _FakeStream(self, chat_ctx=chat_ctx, conn_options=conn_options)

    async def aclose(self) -> None:
        self.closed = True
        await super().aclose()


class Recorder:
    """Stand-in for a plugin LLM class that records constructor kwargs."""

    def __init__(self) -> None:
        self.calls: list[dict[str, Any]] = []

    def __call__(self, **kwargs: Any) -> FakeLLM:
        self.calls.append(kwargs)
        return FakeLLM(["ok"])

    @property
    def kwargs(self) -> dict[str, Any]:
        assert len(self.calls) == 1
        return self.calls[0]


# ---- real construction -------------------------------------------------------


def _base_url(llm: lk_llm.LLM) -> str:
    return str(llm._client.base_url)  # type: ignore[attr-defined]


async def test_build_openai_real() -> None:
    llm = llm_router.build_single(make_cfg("openai", "gpt-4.1-mini", api_key="sk-test"))
    try:
        assert type(llm) is lk_openai.LLM
        assert llm.model == "gpt-4.1-mini"
        assert _base_url(llm).startswith("https://api.openai.com/v1")
        assert llm._opts.max_completion_tokens == 512  # type: ignore[attr-defined]
        assert llm._opts.temperature == pytest.approx(0.4)  # type: ignore[attr-defined]
    finally:
        await llm.aclose()


async def test_build_anthropic_real() -> None:
    cfg = make_cfg("anthropic", "claude-haiku-4-5", api_key="k", max_tokens=300)
    llm = llm_router.build_single(cfg)
    try:
        assert type(llm) is lk_anthropic.LLM
        assert llm.model == "claude-haiku-4-5"
        assert llm._opts.max_tokens == 300  # type: ignore[attr-defined]
    finally:
        await llm.aclose()


async def test_build_google_real() -> None:
    cfg = make_cfg("google", "gemini-2.5-flash", api_key="k", max_tokens=256)
    llm = llm_router.build_single(cfg)
    try:
        assert type(llm) is lk_google.LLM
        assert llm.model == "gemini-2.5-flash"
        assert llm.provider == "Gemini"
        assert llm._opts.max_output_tokens == 256  # type: ignore[attr-defined]
    finally:
        await llm.aclose()


async def test_build_groq_real() -> None:
    llm = llm_router.build_single(make_cfg("groq", "llama-3.3-70b-versatile", api_key="gk"))
    try:
        assert type(llm) is lk_groq.LLM
        assert llm.model == "llama-3.3-70b-versatile"
        assert _base_url(llm).startswith("https://api.groq.com/openai/v1")
    finally:
        await llm.aclose()


@pytest.mark.parametrize(
    ("base_url", "expected"),
    [
        ("", "http://localhost:11434/v1"),
        ("http://gpu-box:11434", "http://gpu-box:11434/v1"),
        ("http://gpu-box:11434/v1/", "http://gpu-box:11434/v1"),
    ],
)
async def test_build_ollama_real(base_url: str, expected: str) -> None:
    llm = llm_router.build_single(make_cfg("ollama", "llama3.1", base_url=base_url))
    try:
        assert type(llm) is lk_openai.LLM
        assert llm.model == "llama3.1"
        assert _base_url(llm).rstrip("/") == expected
    finally:
        await llm.aclose()


async def test_build_openai_compatible_real() -> None:
    cfg = make_cfg("openai_compatible", "qwen2.5-7b", base_url="http://vllm:8000/v1")
    llm = llm_router.build_single(cfg)
    try:
        assert type(llm) is lk_openai.LLM
        assert llm.model == "qwen2.5-7b"
        assert _base_url(llm).rstrip("/") == "http://vllm:8000/v1"
        assert llm._client.api_key == "not-needed"  # type: ignore[attr-defined]
    finally:
        await llm.aclose()


# ---- kwargs passed to plugin constructors ------------------------------------------------


def test_openai_kwargs(monkeypatch: pytest.MonkeyPatch) -> None:
    rec = Recorder()
    monkeypatch.setattr(lk_openai, "LLM", rec)
    llm_router.build_single(
        make_cfg(
            "openai",
            "gpt-4.1",
            api_key="sk-1",
            temperature=0.7,
            max_tokens=200,
            base_url="https://gw.example/v1/",
        )
    )
    assert rec.kwargs == {
        "model": "gpt-4.1",
        "api_key": "sk-1",
        "base_url": "https://gw.example/v1",
        "temperature": 0.7,
        "max_completion_tokens": 200,
    }


def test_openai_reasoning_model_omits_temperature(monkeypatch: pytest.MonkeyPatch) -> None:
    rec = Recorder()
    monkeypatch.setattr(lk_openai, "LLM", rec)
    llm_router.build_single(make_cfg("openai", "gpt-5-mini", api_key="sk-1"))
    assert rec.kwargs["temperature"] is NOT_GIVEN
    assert rec.kwargs["base_url"] is NOT_GIVEN


def test_openai_uses_env_key_when_config_empty(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("OPENAI_API_KEY", "sk-env")
    rec = Recorder()
    monkeypatch.setattr(lk_openai, "LLM", rec)
    llm_router.build_single(make_cfg("openai", "gpt-4.1"))
    assert rec.kwargs["api_key"] is NOT_GIVEN


def test_anthropic_kwargs_clamps_temperature(monkeypatch: pytest.MonkeyPatch) -> None:
    rec = Recorder()
    monkeypatch.setattr(lk_anthropic, "LLM", rec)
    llm_router.build_single(
        make_cfg("anthropic", "claude-sonnet-4-5", api_key="ak", temperature=1.5, max_tokens=64)
    )
    assert rec.kwargs == {
        "model": "claude-sonnet-4-5",
        "api_key": "ak",
        "base_url": NOT_GIVEN,
        "temperature": 1.0,
        "max_tokens": 64,
    }


def test_google_kwargs(monkeypatch: pytest.MonkeyPatch) -> None:
    rec = Recorder()
    monkeypatch.setattr(lk_google, "LLM", rec)
    llm_router.build_single(
        make_cfg("google", "gemini-2.5-pro", api_key="gk", temperature=0.2, max_tokens=99)
    )
    kw = rec.kwargs
    assert kw["model"] == "gemini-2.5-pro"
    assert kw["api_key"] == "gk"
    assert kw["vertexai"] is False
    assert kw["temperature"] == 0.2
    assert kw["max_output_tokens"] == 99
    assert kw["http_options"] is NOT_GIVEN


def test_google_base_url_goes_to_http_options(monkeypatch: pytest.MonkeyPatch) -> None:
    rec = Recorder()
    monkeypatch.setattr(lk_google, "LLM", rec)
    llm_router.build_single(
        make_cfg("google", "gemini-2.5-flash", api_key="gk", base_url="https://gproxy.example")
    )
    assert rec.kwargs["http_options"].base_url == "https://gproxy.example"


def test_groq_kwargs(monkeypatch: pytest.MonkeyPatch) -> None:
    rec = Recorder()
    monkeypatch.setattr(lk_groq, "LLM", rec)
    llm_router.build_single(make_cfg("groq", "qwen-qwq-32b", api_key="gq", temperature=0.1))
    assert rec.kwargs == {
        "model": "qwen-qwq-32b",
        "api_key": "gq",
        "temperature": 0.1,
        "max_completion_tokens": 512,
    }


def test_ollama_kwargs(monkeypatch: pytest.MonkeyPatch) -> None:
    rec = Recorder()
    monkeypatch.setattr(lk_openai.LLM, "with_ollama", rec)
    llm_router.build_single(make_cfg("ollama", "qwen2.5", temperature=0.3))
    assert rec.kwargs == {
        "model": "qwen2.5",
        "base_url": "http://localhost:11434/v1",
        "temperature": 0.3,
    }


def test_openai_compatible_kwargs(monkeypatch: pytest.MonkeyPatch) -> None:
    rec = Recorder()
    monkeypatch.setattr(lk_openai, "LLM", rec)
    llm_router.build_single(
        make_cfg("openai_compatible", "mistral", api_key="secret", base_url="https://x.example/v1")
    )
    assert rec.kwargs == {
        "model": "mistral",
        "api_key": "secret",
        "base_url": "https://x.example/v1",
        "temperature": 0.4,
        "max_completion_tokens": 512,
    }


# ---- validation --------------------------------------------------------------------


@pytest.mark.parametrize("provider", ["openai", "anthropic", "google", "groq"])
def test_missing_api_key(provider: str) -> None:
    with pytest.raises(LLMConfigError, match="API key is required"):
        llm_router.build_single(make_cfg(provider, "some-model"))


def test_openai_compatible_requires_base_url() -> None:
    with pytest.raises(LLMConfigError, match="base URL is required"):
        llm_router.build_single(make_cfg("openai_compatible", "m"))


@pytest.mark.parametrize("url", ["localhost:8000", "ftp://x/v1", "http://"])
def test_invalid_base_url(url: str) -> None:
    with pytest.raises(LLMConfigError, match="absolute http"):
        llm_router.build_single(make_cfg("openai_compatible", "m", base_url=url))


def test_model_required() -> None:
    with pytest.raises(LLMConfigError, match="model is required"):
        llm_router.build_single(make_cfg("ollama", "  "))


@pytest.mark.parametrize("temp", [-0.1, 2.5, float("nan")])
def test_temperature_range(temp: float) -> None:
    with pytest.raises(LLMConfigError, match="temperature"):
        llm_router.build_single(make_cfg("ollama", "llama3.1", temperature=temp))


def test_max_tokens_positive() -> None:
    with pytest.raises(LLMConfigError, match="maxTokens"):
        llm_router.build_single(make_cfg("ollama", "llama3.1", max_tokens=0))


def test_plugin_error_is_wrapped_and_redacted(monkeypatch: pytest.MonkeyPatch) -> None:
    def boom(**kwargs: Any) -> None:
        raise ValueError(f"bad key {kwargs['api_key']}")

    monkeypatch.setattr(lk_openai, "LLM", boom)
    with pytest.raises(LLMConfigError) as ei:
        llm_router.build_single(make_cfg("openai", "gpt-4.1", api_key="sk-supersecret"))
    assert "sk-supersecret" not in str(ei.value)
    assert "openai/gpt-4.1" in str(ei.value)


def test_api_key_never_logged(caplog: pytest.LogCaptureFixture) -> None:
    caplog.set_level(logging.DEBUG)
    primary = make_cfg("openai_compatible", "m", api_key="sk-topsecret", base_url="http://a/v1")
    bad = make_cfg("openai", "gpt-4.1", api_key="")  # fails -> warning logged
    good = make_cfg("groq", "llama-3.3-70b-versatile", api_key="gsk-topsecret")
    llm_router.build_llm(primary, [bad, good])
    assert "topsecret" not in caplog.text


# ---- describe / fallback chain --------------------------------------------------------


def test_describe() -> None:
    assert llm_router.describe(make_cfg("anthropic", "claude-haiku-4-5")) == (
        "anthropic/claude-haiku-4-5"
    )
    assert llm_router.describe(make_cfg("openai_compatible", "x")) == "openai_compatible/x"


def test_build_llm_single_without_fallbacks() -> None:
    llm = llm_router.build_llm(make_cfg("ollama", "llama3.1"), [])
    assert type(llm) is lk_openai.LLM


def test_build_llm_fallback_adapter() -> None:
    primary = make_cfg("openai", "gpt-4.1-mini", api_key="sk")
    fb1 = make_cfg("anthropic", "claude-haiku-4-5", api_key="ak")
    fb2 = make_cfg("ollama", "llama3.1")
    built = llm_router.build_chain(primary, [fb1, fb2], attempt_timeout=3.0)
    assert isinstance(built.llm, lk_llm.FallbackAdapter)
    assert [type(m) for m in built.members] == [lk_openai.LLM, lk_anthropic.LLM, lk_openai.LLM]
    assert built.labels == ["openai/gpt-4.1-mini", "anthropic/claude-haiku-4-5", "ollama/llama3.1"]
    assert built.llm._llm_instances == built.members  # type: ignore[attr-defined]
    assert built.llm._attempt_timeout == 3.0  # type: ignore[attr-defined]
    assert built.llm.model == "gpt-4.1-mini"  # primary serves first


def test_build_llm_skips_broken_fallbacks(caplog: pytest.LogCaptureFixture) -> None:
    primary = make_cfg("ollama", "llama3.1")
    broken = make_cfg("openai_compatible", "m")  # no base URL
    good = make_cfg("groq", "llama-3.3-70b-versatile", api_key="gk")
    with caplog.at_level(logging.WARNING, logger="callgo.llm_router"):
        built = llm_router.build_chain(primary, [broken, good])
    assert built.labels == ["ollama/llama3.1", "groq/llama-3.3-70b-versatile"]
    assert isinstance(built.llm, lk_llm.FallbackAdapter)
    assert "skipping LLM fallback" in caplog.text


def test_build_llm_all_fallbacks_broken_returns_primary() -> None:
    primary = make_cfg("ollama", "llama3.1")
    llm = llm_router.build_llm(primary, [make_cfg("openai", "gpt-4.1")])  # no key
    assert type(llm) is lk_openai.LLM


def test_build_llm_primary_error_propagates() -> None:
    with pytest.raises(LLMConfigError):
        llm_router.build_llm(make_cfg("openai", "gpt-4.1"), [make_cfg("ollama", "llama3.1")])


def test_build_llm_dedupes_fallbacks() -> None:
    primary = make_cfg("ollama", "llama3.1")
    fb = make_cfg("ollama", "qwen2.5")
    built = llm_router.build_chain(primary, [primary, fb, fb])
    assert built.labels == ["ollama/llama3.1", "ollama/qwen2.5"]


async def test_fallback_adapter_uses_next_llm_on_failure() -> None:
    bad = FakeLLM(error=APIConnectionError("down", retryable=False))
    good = FakeLLM(["from ", "fallback"])
    adapter = lk_llm.FallbackAdapter([bad, good], attempt_timeout=2.0)
    ctx = lk_llm.ChatContext.empty()
    ctx.add_message(role="user", content="hi")
    text = ""
    async with adapter.chat(chat_ctx=ctx) as stream:
        async for chunk in stream:
            if chunk.delta and chunk.delta.content:
                text += chunk.delta.content
    assert text == "from fallback"
    await adapter.aclose()


# ---- test_config ----------------------------------------------------------------------


async def test_test_config_ok(monkeypatch: pytest.MonkeyPatch) -> None:
    fake = FakeLLM(["O", "K", "!"])
    monkeypatch.setattr(llm_router, "build_single", lambda cfg: fake)
    res = await llm_router.test_config(make_cfg("ollama", "llama3.1"), prompt="Say OK please")
    assert res.ok is True
    assert res.reply == "OK!"
    assert res.error is None
    assert res.latency_ms >= 0
    assert fake.prompts == ["Say OK please"]
    assert fake.closed
    assert set(res.model_dump()) == {"ok", "reply", "latencyMs", "error"}


async def test_test_config_error(monkeypatch: pytest.MonkeyPatch) -> None:
    fake = FakeLLM(error=RuntimeError("401 invalid key sk-leaky"))
    monkeypatch.setattr(llm_router, "build_single", lambda cfg: fake)
    res = await llm_router.test_config(make_cfg("openai", "gpt-4.1", api_key="sk-leaky"))
    assert res.ok is False
    assert res.error is not None and "RuntimeError" in res.error
    assert "sk-leaky" not in res.error
    assert fake.closed


async def test_test_config_api_error(monkeypatch: pytest.MonkeyPatch) -> None:
    fake = FakeLLM(error=APIConnectionError("connection refused", retryable=True))
    monkeypatch.setattr(llm_router, "build_single", lambda cfg: fake)
    res = await llm_router.test_config(make_cfg("ollama", "llama3.1"))
    assert res.ok is False
    assert res.error is not None and "connection refused" in res.error


async def test_test_config_timeout(monkeypatch: pytest.MonkeyPatch) -> None:
    fake = FakeLLM(["late"], delay=5)
    monkeypatch.setattr(llm_router, "build_single", lambda cfg: fake)
    res = await llm_router.test_config(make_cfg("ollama", "llama3.1"), timeout=0.2)
    assert res.ok is False
    assert res.error is not None and "timed out" in res.error


async def test_test_config_empty_reply(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(llm_router, "build_single", lambda cfg: FakeLLM([]))
    res = await llm_router.test_config(make_cfg("ollama", "llama3.1"))
    assert res.ok is False
    assert res.error == "empty response"


async def test_test_config_invalid_config() -> None:
    res = await llm_router.test_config(make_cfg("openai_compatible", "m"))
    assert res.ok is False
    assert res.error is not None and "base URL is required" in res.error


# ---- router cache --------------------------------------------------------------------


class CountingBuilder:
    def __init__(self) -> None:
        self.calls = 0
        self.built: list[llm_router.BuiltLLM] = []

    def __call__(
        self, config: LLMConfig, fallbacks: list[LLMConfig], *, attempt_timeout: float
    ) -> llm_router.BuiltLLM:
        self.calls += 1
        fake = FakeLLM(["ok"])
        built = llm_router.BuiltLLM(llm=fake, members=[fake], labels=[llm_router.describe(config)])
        self.built.append(built)
        return built


async def test_router_reuses_instances() -> None:
    builder = CountingBuilder()
    router = LLMRouter(builder=builder)
    cfg = make_cfg("ollama", "llama3.1")
    fb = make_cfg("ollama", "qwen2.5")
    a = router.get(cfg, [fb])
    b = router.get(cfg.model_copy(), [fb.model_copy()])
    assert a is b
    assert builder.calls == 1
    # different fallback chain -> different entry
    c = router.get(cfg, [])
    assert c is not a
    assert builder.calls == 2
    await router.aclose()
    assert all(isinstance(x.llm, FakeLLM) and x.llm.closed for x in builder.built)
    assert len(router) == 0


def test_router_rebuilds_when_config_changes() -> None:
    builder = CountingBuilder()
    router = LLMRouter(builder=builder)
    cfg = make_cfg("openai", "gpt-4.1", api_key="k1")
    first = router.get(cfg)
    changed = cfg.model_copy(update={"api_key": "k2"})
    second = router.get(changed)
    assert first is not second
    renamed = cfg.model_copy(update={"name": "renamed"})  # name doesn't affect the LLM
    assert router.get(renamed) is first
    assert builder.calls == 2


def test_router_lru_eviction() -> None:
    builder = CountingBuilder()
    router = LLMRouter(max_size=2, builder=builder)
    c1, c2, c3 = (make_cfg("ollama", f"m{i}") for i in range(3))
    l1 = router.get(c1)
    router.get(c2)
    assert router.get(c1) is l1  # c1 is now most recently used
    router.get(c3)  # evicts c2
    assert len(router) == 2
    assert router.get(c1) is l1
    router.get(c2)
    assert builder.calls == 4


def test_router_invalidate() -> None:
    builder = CountingBuilder()
    router = LLMRouter(builder=builder)
    cfg = make_cfg("ollama", "llama3.1")
    fb = make_cfg("ollama", "qwen2.5")
    router.get(cfg, [fb])
    router.get(cfg)
    assert router.invalidate(fb.id) == 1
    assert len(router) == 1


async def test_router_real_builder() -> None:
    router = LLMRouter()
    cfg = make_cfg("ollama", "llama3.1")
    llm = router.get(cfg, [make_cfg("openai_compatible", "m", base_url="http://h/v1")])
    assert isinstance(llm, lk_llm.FallbackAdapter)
    assert router.get(cfg, [make_cfg("openai_compatible", "m", base_url="http://h/v1")]) is not llm
    await router.aclose()


def test_router_invalid_size() -> None:
    with pytest.raises(ValueError):
        LLMRouter(max_size=0)


# ---- catalog ---------------------------------------------------------------------------


def test_catalog_covers_all_providers() -> None:
    assert {p.provider for p in llm_catalog.CATALOG} == set(LLMProvider)


def test_catalog_payload_shape() -> None:
    payload = llm_catalog.catalog_payload()
    by = {p["provider"]: p for p in payload["providers"]}
    assert by["openai"]["models"] == ["gpt-4.1", "gpt-4.1-mini", "gpt-4o-mini"]
    assert by["anthropic"]["models"] == ["claude-sonnet-4-5", "claude-haiku-4-5"]
    assert by["google"]["models"] == ["gemini-2.5-flash", "gemini-2.5-pro", "gemini-2.0-flash"]
    assert by["groq"]["models"] == ["llama-3.3-70b-versatile", "qwen-qwq-32b"]
    assert by["ollama"] == {
        "provider": "ollama",
        "label": "Ollama (local)",
        "models": ["llama3.1", "qwen2.5"],
        "needsApiKey": False,
        "needsBaseUrl": True,
    }
    assert by["openai_compatible"]["needsBaseUrl"] is True
    assert by["openai_compatible"]["models"] == []
    assert by["openai"]["needsApiKey"] is True and by["openai"]["needsBaseUrl"] is False


def test_default_model() -> None:
    assert llm_catalog.default_model("openai") == "gpt-4.1"
    assert llm_catalog.default_model(LLMProvider.GOOGLE) == "gemini-2.5-flash"
    assert llm_catalog.default_model("ollama") == "llama3.1"
    assert llm_catalog.default_model("openai_compatible") == ""
    assert llm_catalog.default_model("nope") == ""
    with pytest.raises(KeyError):
        llm_catalog.get_provider("nope")
