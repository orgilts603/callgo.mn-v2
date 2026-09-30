"""Static catalog of supported LLM providers and suggested models.

Mirrors the semantics of the backend's ``GET /api/llm-configs/catalog`` response
(``{providers: [{provider, label, models, needsApiKey, needsBaseUrl}]}``, see docs/API.md).
The model lists are suggestions for the UI; any model id the provider accepts can be used.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any

from .schemas import LLMProvider


@dataclass(frozen=True, slots=True)
class ProviderInfo:
    provider: LLMProvider
    label: str
    models: tuple[str, ...]
    needs_api_key: bool
    needs_base_url: bool
    default_base_url: str = ""

    def to_wire(self) -> dict[str, Any]:
        """camelCase dict matching the backend catalog entry."""
        return {
            "provider": self.provider.value,
            "label": self.label,
            "models": list(self.models),
            "needsApiKey": self.needs_api_key,
            "needsBaseUrl": self.needs_base_url,
        }


OLLAMA_DEFAULT_BASE_URL = "http://localhost:11434/v1"

CATALOG: tuple[ProviderInfo, ...] = (
    ProviderInfo(
        provider=LLMProvider.OPENAI,
        label="OpenAI",
        models=("gpt-4.1", "gpt-4.1-mini", "gpt-4o-mini"),
        needs_api_key=True,
        needs_base_url=False,
    ),
    ProviderInfo(
        provider=LLMProvider.ANTHROPIC,
        label="Anthropic",
        models=("claude-sonnet-4-5", "claude-haiku-4-5"),
        needs_api_key=True,
        needs_base_url=False,
    ),
    ProviderInfo(
        provider=LLMProvider.GOOGLE,
        label="Google Gemini",
        models=("gemini-2.5-flash", "gemini-2.5-pro", "gemini-2.0-flash"),
        needs_api_key=True,
        needs_base_url=False,
    ),
    ProviderInfo(
        provider=LLMProvider.GROQ,
        label="Groq",
        models=("llama-3.3-70b-versatile", "qwen-qwq-32b"),
        needs_api_key=True,
        needs_base_url=False,
    ),
    ProviderInfo(
        provider=LLMProvider.OLLAMA,
        label="Ollama (local)",
        models=("llama3.1", "qwen2.5"),
        needs_api_key=False,
        needs_base_url=True,
        default_base_url=OLLAMA_DEFAULT_BASE_URL,
    ),
    ProviderInfo(
        provider=LLMProvider.OPENAI_COMPATIBLE,
        label="OpenAI-compatible",
        models=(),
        needs_api_key=False,
        needs_base_url=True,
    ),
)

_BY_PROVIDER: dict[LLMProvider, ProviderInfo] = {p.provider: p for p in CATALOG}


def get_provider(provider: LLMProvider | str) -> ProviderInfo:
    """Return the catalog entry for ``provider``; raises ``KeyError`` for unknown providers."""
    try:
        key = LLMProvider(provider)
    except ValueError as exc:
        raise KeyError(f"unknown LLM provider: {provider!r}") from exc
    return _BY_PROVIDER[key]


def default_model(provider: LLMProvider | str) -> str:
    """First suggested model for ``provider``, or ``""`` when the provider has no suggestions
    (``openai_compatible``) or is unknown."""
    try:
        info = get_provider(provider)
    except KeyError:
        return ""
    return info.models[0] if info.models else ""


def catalog_payload() -> dict[str, list[dict[str, Any]]]:
    """The full catalog in the wire shape of ``GET /api/llm-configs/catalog``."""
    return {"providers": [p.to_wire() for p in CATALOG]}


__all__ = [
    "CATALOG",
    "OLLAMA_DEFAULT_BASE_URL",
    "ProviderInfo",
    "catalog_payload",
    "default_model",
    "get_provider",
]
