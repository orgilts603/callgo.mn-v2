"""Multi-provider LLM router.

Maps an :class:`~callgo_agent.schemas.LLMConfig` row (see docs/ARCHITECTURE.md, "Multi-LLM
routing") to a LiveKit Agents plugin LLM and wraps a fallback chain in
:class:`livekit.agents.llm.FallbackAdapter`.

Provider mapping (constructor kwargs verified against livekit-plugins 1.8.3):

* ``openai``            -> ``openai.LLM(model, api_key, base_url?, temperature, max_completion_tokens)``
* ``anthropic``         -> ``anthropic.LLM(model, api_key, base_url?, temperature, max_tokens)``
* ``google``            -> ``google.LLM(model, api_key, vertexai=False, temperature,
  max_output_tokens, http_options?)``
* ``groq``              -> ``groq.LLM(model, api_key, base_url?, temperature, max_completion_tokens)``
* ``ollama``            -> ``openai.LLM.with_ollama(model, base_url, temperature)``
* ``openai_compatible`` -> ``openai.LLM(model, api_key or "not-needed", base_url, temperature,
  max_completion_tokens)``

API keys are never logged. When a config has no API key, the provider's standard environment
variable (``OPENAI_API_KEY``, ...) is used if present; otherwise :class:`LLMConfigError`.

Plugins are imported at module import time because LiveKit requires plugin registration on
the main thread.
"""

from __future__ import annotations

import asyncio
import hashlib
import json
import logging
import math
import os
import time
from collections import OrderedDict
from collections.abc import Callable, Sequence
from dataclasses import dataclass
from typing import Any
from urllib.parse import urlsplit, urlunsplit

from google.genai import types as genai_types
from livekit.agents import llm as lk_llm
from livekit.agents.types import NOT_GIVEN, APIConnectOptions, NotGivenOr
from livekit.plugins import anthropic as lk_anthropic
from livekit.plugins import google as lk_google
from livekit.plugins import groq as lk_groq
from livekit.plugins import openai as lk_openai

from .llm_catalog import OLLAMA_DEFAULT_BASE_URL
from .schemas import CamelModel, LLMConfig, LLMProvider

logger = logging.getLogger("callgo.llm_router")

#: Per-attempt timeout (seconds) used by the FallbackAdapter before moving to the next LLM.
DEFAULT_ATTEMPT_TIMEOUT = 8.0
#: Placeholder key for OpenAI-compatible servers that do not check auth (vLLM, LM Studio, ...).
NO_API_KEY_PLACEHOLDER = "not-needed"

_ENV_API_KEYS: dict[LLMProvider, str] = {
    LLMProvider.OPENAI: "OPENAI_API_KEY",
    LLMProvider.ANTHROPIC: "ANTHROPIC_API_KEY",
    LLMProvider.GOOGLE: "GOOGLE_API_KEY",
    LLMProvider.GROQ: "GROQ_API_KEY",
}

# OpenAI reasoning models reject a non-default temperature.
_OPENAI_NO_TEMPERATURE_PREFIXES = ("o1", "o3", "o4", "gpt-5")


class LLMConfigError(ValueError):
    """Raised when an :class:`LLMConfig` cannot be turned into an LLM instance."""


class TestResult(CamelModel):
    """Result of :func:`test_config`; serialises as ``{ok, reply, latencyMs, error?}``."""

    __test__ = False  # not a pytest test class

    ok: bool
    reply: str = ""
    latency_ms: int = 0
    error: str | None = None


# ---- helpers ----------------------------------------------------------------


def describe(config: LLMConfig) -> str:
    """Human/metrics friendly identifier, e.g. ``"openai/gpt-4.1-mini"``."""
    return f"{config.provider.value}/{config.model}"


def _redact(text: str, *secrets: str) -> str:
    for secret in secrets:
        if secret and len(secret) >= 4:
            text = text.replace(secret, "***")
    return text


def _normalize_base_url(config: LLMConfig, *, required: bool) -> str:
    url = config.base_url.strip()
    if not url:
        if required:
            raise LLMConfigError(f"{describe(config)}: base URL is required")
        return ""
    parts = urlsplit(url)
    if parts.scheme not in ("http", "https") or not parts.netloc:
        raise LLMConfigError(
            f"{describe(config)}: base URL must be an absolute http(s) URL, got {url!r}"
        )
    return url.rstrip("/")


def _ollama_base_url(config: LLMConfig) -> str:
    url = _normalize_base_url(config, required=False) or OLLAMA_DEFAULT_BASE_URL
    parts = urlsplit(url)
    if parts.path in ("", "/"):
        # Users often paste the bare Ollama address; the OpenAI API lives under /v1.
        url = urlunsplit((parts.scheme, parts.netloc, "/v1", parts.query, parts.fragment))
    return url


def _api_key(config: LLMConfig) -> NotGivenOr[str]:
    """Explicit key from the config, or NOT_GIVEN to let the plugin read its env var."""
    key = config.api_key.strip()
    if key:
        return key
    env = _ENV_API_KEYS.get(config.provider)
    if env and os.environ.get(env):
        return NOT_GIVEN
    hint = f" (or set {env})" if env else ""
    raise LLMConfigError(f"{describe(config)}: API key is required{hint}")


def _validate_common(config: LLMConfig) -> None:
    if not config.model.strip():
        raise LLMConfigError(f"{config.provider.value}: model is required")
    if not math.isfinite(config.temperature) or not 0.0 <= config.temperature <= 2.0:
        raise LLMConfigError(
            f"{describe(config)}: temperature must be between 0 and 2, got {config.temperature}"
        )
    if config.max_tokens < 1:
        raise LLMConfigError(
            f"{describe(config)}: maxTokens must be a positive integer, got {config.max_tokens}"
        )


def _openai_temperature(model: str, temperature: float) -> NotGivenOr[float]:
    if model.startswith(_OPENAI_NO_TEMPERATURE_PREFIXES):
        return NOT_GIVEN
    return temperature


# ---- per-provider builders ---------------------------------------------------


def _build_openai(config: LLMConfig) -> lk_llm.LLM:
    model = config.model.strip()
    base_url = _normalize_base_url(config, required=False)
    return lk_openai.LLM(
        model=model,
        api_key=_api_key(config),
        base_url=base_url or NOT_GIVEN,
        temperature=_openai_temperature(model, config.temperature),
        max_completion_tokens=config.max_tokens,
    )


def _build_anthropic(config: LLMConfig) -> lk_llm.LLM:
    base_url = _normalize_base_url(config, required=False)
    # Anthropic accepts temperature in [0, 1].
    return lk_anthropic.LLM(
        model=config.model.strip(),
        api_key=_api_key(config),
        base_url=base_url or NOT_GIVEN,
        temperature=min(config.temperature, 1.0),
        max_tokens=config.max_tokens,
    )


def _build_google(config: LLMConfig) -> lk_llm.LLM:
    base_url = _normalize_base_url(config, required=False)
    http_options: NotGivenOr[genai_types.HttpOptions] = (
        genai_types.HttpOptions(base_url=base_url) if base_url else NOT_GIVEN
    )
    return lk_google.LLM(
        model=config.model.strip(),
        api_key=_api_key(config),
        # Per-org configs carry a Gemini API key; never let GOOGLE_GENAI_USE_VERTEXAI in the
        # worker environment silently switch this to Vertex AI credentials.
        vertexai=False,
        temperature=config.temperature,
        max_output_tokens=config.max_tokens,
        http_options=http_options,
    )


def _build_groq(config: LLMConfig) -> lk_llm.LLM:
    base_url = _normalize_base_url(config, required=False)
    kwargs: dict[str, Any] = {}
    if base_url:
        kwargs["base_url"] = base_url
    return lk_groq.LLM(
        model=config.model.strip(),
        api_key=_api_key(config),
        temperature=config.temperature,
        max_completion_tokens=config.max_tokens,
        **kwargs,
    )


def _build_ollama(config: LLMConfig) -> lk_llm.LLM:
    return lk_openai.LLM.with_ollama(
        model=config.model.strip(),
        base_url=_ollama_base_url(config),
        temperature=config.temperature,
    )


def _build_openai_compatible(config: LLMConfig) -> lk_llm.LLM:
    return lk_openai.LLM(
        model=config.model.strip(),
        api_key=config.api_key.strip() or NO_API_KEY_PLACEHOLDER,
        base_url=_normalize_base_url(config, required=True),
        temperature=config.temperature,
        max_completion_tokens=config.max_tokens,
    )


_BUILDERS: dict[LLMProvider, Callable[[LLMConfig], lk_llm.LLM]] = {
    LLMProvider.OPENAI: _build_openai,
    LLMProvider.ANTHROPIC: _build_anthropic,
    LLMProvider.GOOGLE: _build_google,
    LLMProvider.GROQ: _build_groq,
    LLMProvider.OLLAMA: _build_ollama,
    LLMProvider.OPENAI_COMPATIBLE: _build_openai_compatible,
}


# ---- public API -------------------------------------------------------------


def build_single(config: LLMConfig) -> lk_llm.LLM:
    """Build one plugin LLM for ``config``.

    Raises :class:`LLMConfigError` for invalid/missing fields or when the plugin rejects the
    configuration.
    """
    builder = _BUILDERS.get(config.provider)
    if builder is None:  # pragma: no cover - enum is exhaustive
        raise LLMConfigError(f"unsupported LLM provider: {config.provider!r}")
    _validate_common(config)
    try:
        return builder(config)
    except LLMConfigError:
        raise
    except Exception as exc:  # plugin-level validation (ValueError etc.)
        msg = _redact(str(exc), config.api_key)
        raise LLMConfigError(f"{describe(config)}: {msg}") from exc


def _dedupe_chain(config: LLMConfig, fallbacks: Sequence[LLMConfig]) -> list[LLMConfig]:
    seen = {config.id}
    out: list[LLMConfig] = []
    for fb in fallbacks:
        if fb.id in seen:
            logger.debug("skipping duplicate fallback %s (%s)", fb.id, describe(fb))
            continue
        seen.add(fb.id)
        out.append(fb)
    return out


@dataclass(slots=True)
class BuiltLLM:
    """An LLM plus the underlying plugin instances it owns (for closing)."""

    llm: lk_llm.LLM
    members: list[lk_llm.LLM]
    labels: list[str]

    async def aclose(self) -> None:
        if self.llm not in self.members:
            await self.llm.aclose()
        for member in self.members:
            try:
                await member.aclose()
            except Exception:  # pragma: no cover - best effort
                logger.warning("failed to close LLM instance", exc_info=True)


def build_chain(
    config: LLMConfig,
    fallbacks: Sequence[LLMConfig] = (),
    *,
    attempt_timeout: float = DEFAULT_ATTEMPT_TIMEOUT,
) -> BuiltLLM:
    """Like :func:`build_llm` but also returns the member instances and their labels."""
    primary = build_single(config)
    members = [primary]
    labels = [describe(config)]
    for fb in _dedupe_chain(config, fallbacks):
        try:
            members.append(build_single(fb))
            labels.append(describe(fb))
        except LLMConfigError as exc:
            logger.warning("skipping LLM fallback %s: %s", fb.id, exc)
    if len(members) == 1:
        return BuiltLLM(llm=primary, members=members, labels=labels)
    adapter = lk_llm.FallbackAdapter(
        members,
        attempt_timeout=attempt_timeout,
        max_retry_per_llm=0,
        retry_on_chunk_sent=False,
    )
    logger.info("built LLM fallback chain: %s", " -> ".join(labels))
    return BuiltLLM(llm=adapter, members=members, labels=labels)


def build_llm(
    config: LLMConfig,
    fallbacks: Sequence[LLMConfig] = (),
    *,
    attempt_timeout: float = DEFAULT_ATTEMPT_TIMEOUT,
) -> lk_llm.LLM:
    """Primary LLM, wrapped in a ``FallbackAdapter`` when fallbacks are given.

    The primary must build (else :class:`LLMConfigError`); fallbacks that fail to build are
    skipped with a warning. If none survive, the bare primary is returned.
    """
    return build_chain(config, fallbacks, attempt_timeout=attempt_timeout).llm


async def test_config(config: LLMConfig, prompt: str = "Say OK", timeout: float = 20) -> TestResult:
    """Run one chat completion against ``config`` and report the outcome.

    Never raises: construction and runtime errors are returned in ``TestResult.error``.
    """
    start = time.perf_counter()

    def elapsed_ms() -> int:
        return int((time.perf_counter() - start) * 1000)

    try:
        llm = build_single(config)
    except LLMConfigError as exc:
        return TestResult(ok=False, latency_ms=elapsed_ms(), error=str(exc))

    chat_ctx = lk_llm.ChatContext.empty()
    chat_ctx.add_message(role="user", content=prompt or "Say OK")
    parts: list[str] = []

    async def _collect() -> None:
        conn = APIConnectOptions(max_retry=0, timeout=timeout)
        async with llm.chat(chat_ctx=chat_ctx, conn_options=conn) as stream:
            async for chunk in stream:
                if chunk.delta and chunk.delta.content:
                    parts.append(chunk.delta.content)

    try:
        await asyncio.wait_for(_collect(), timeout=timeout)
    except TimeoutError:
        return TestResult(
            ok=False,
            reply="".join(parts),
            latency_ms=elapsed_ms(),
            error=f"timed out after {timeout:g}s",
        )
    except Exception as exc:  # noqa: BLE001 - contract: report every failure, never raise
        msg = _redact(f"{type(exc).__name__}: {exc}", config.api_key)
        logger.info("LLM test failed for %s: %s", describe(config), msg)
        return TestResult(ok=False, reply="".join(parts), latency_ms=elapsed_ms(), error=msg)
    finally:
        try:
            await llm.aclose()
        except Exception:  # pragma: no cover - best effort
            logger.debug("failed to close test LLM", exc_info=True)

    reply = "".join(parts).strip()
    if not reply:
        return TestResult(ok=False, latency_ms=elapsed_ms(), error="empty response")
    return TestResult(ok=True, reply=reply, latency_ms=elapsed_ms())


test_config.__test__ = False  # type: ignore[attr-defined]  # not a pytest test


def config_fingerprint(config: LLMConfig) -> str:
    """Stable hash of every field that affects construction (API key included, hashed)."""
    payload = config.model_dump(mode="json", by_alias=False, exclude={"name", "is_default"})
    raw = json.dumps(payload, sort_keys=True, separators=(",", ":"))
    return hashlib.sha256(raw.encode()).hexdigest()[:16]


CacheKey = tuple[tuple[str, str], ...]


class LLMRouter:
    """Builds LLMs and caches them (LRU) so repeated calls with the same configs reuse instances.

    The cache key is ``(config id, fingerprint)`` for the primary and each fallback, so editing
    a config (model, key, temperature, ...) yields a fresh instance. Evicted instances are
    dropped, not closed, because a running call session may still hold them; :meth:`aclose`
    closes everything still cached (call it on worker shutdown).
    """

    def __init__(
        self,
        *,
        max_size: int = 32,
        attempt_timeout: float = DEFAULT_ATTEMPT_TIMEOUT,
        builder: Callable[..., BuiltLLM] = build_chain,
    ) -> None:
        if max_size < 1:
            raise ValueError("max_size must be >= 1")
        self._max_size = max_size
        self._attempt_timeout = attempt_timeout
        self._builder = builder
        self._cache: OrderedDict[CacheKey, BuiltLLM] = OrderedDict()

    @staticmethod
    def cache_key(config: LLMConfig, fallbacks: Sequence[LLMConfig] = ()) -> CacheKey:
        return tuple((str(c.id), config_fingerprint(c)) for c in (config, *fallbacks))

    def get(self, config: LLMConfig, fallbacks: Sequence[LLMConfig] = ()) -> lk_llm.LLM:
        key = self.cache_key(config, fallbacks)
        hit = self._cache.get(key)
        if hit is not None:
            self._cache.move_to_end(key)
            return hit.llm
        built = self._builder(config, list(fallbacks), attempt_timeout=self._attempt_timeout)
        self._cache[key] = built
        while len(self._cache) > self._max_size:
            _, evicted = self._cache.popitem(last=False)
            logger.debug("evicted LLM %s from router cache", " -> ".join(evicted.labels))
        return built.llm

    def invalidate(self, config_id: object) -> int:
        """Drop every cached chain that includes ``config_id``; returns how many were dropped."""
        cid = str(config_id)
        stale = [k for k in self._cache if any(part[0] == cid for part in k)]
        for k in stale:
            del self._cache[k]
        return len(stale)

    def __len__(self) -> int:
        return len(self._cache)

    async def aclose(self) -> None:
        entries = list(self._cache.values())
        self._cache.clear()
        for entry in entries:
            await entry.aclose()


__all__ = [
    "DEFAULT_ATTEMPT_TIMEOUT",
    "BuiltLLM",
    "LLMConfigError",
    "LLMRouter",
    "TestResult",
    "build_chain",
    "build_llm",
    "build_single",
    "config_fingerprint",
    "describe",
    "test_config",
]
