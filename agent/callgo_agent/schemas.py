"""Pydantic models mirroring backend/internal/domain/domain.go and docs/EVENTS.md.

FROZEN CONTRACT — feature agents import from here and must not edit it.
Field names are camelCase on the wire (aliases); use ``model_dump(by_alias=True)``.
"""

from __future__ import annotations

from datetime import datetime
from enum import Enum
from typing import Any, Literal
from uuid import UUID

from pydantic import BaseModel, ConfigDict, Field


class _Base(BaseModel):
    model_config = ConfigDict(populate_by_name=True, extra="ignore", alias_generator=None)


def _alias(name: str) -> str:
    parts = name.split("_")
    return parts[0] + "".join(p.capitalize() for p in parts[1:])


class CamelModel(_Base):
    model_config = ConfigDict(
        populate_by_name=True, extra="ignore", alias_generator=_alias, serialize_by_alias=True
    )


class LLMProvider(str, Enum):
    OPENAI = "openai"
    ANTHROPIC = "anthropic"
    GOOGLE = "google"
    GROQ = "groq"
    OLLAMA = "ollama"
    OPENAI_COMPATIBLE = "openai_compatible"


class CallDirection(str, Enum):
    INBOUND = "inbound"
    OUTBOUND = "outbound"


class CallStatus(str, Enum):
    QUEUED = "queued"
    RINGING = "ringing"
    ACTIVE = "active"
    COMPLETED = "completed"
    FAILED = "failed"
    NO_ANSWER = "no_answer"
    BUSY = "busy"
    VOICEMAIL = "voicemail"


class Sentiment(str, Enum):
    POSITIVE = "positive"
    NEUTRAL = "neutral"
    NEGATIVE = "negative"


class Speaker(str, Enum):
    CUSTOMER = "customer"
    AGENT = "agent"
    HUMAN = "human"


class LexiconScope(str, Enum):
    STT = "stt"
    TTS = "tts"
    BOTH = "both"


AgentState = Literal["initializing", "listening", "thinking", "speaking", "idle"]
EndReason = Literal[
    "hangup_customer",
    "hangup_agent",
    "no_answer",
    "busy",
    "failed",
    "max_duration",
    "transferred",
    "voicemail",
]


class Organization(CamelModel):
    id: UUID
    name: str
    slug: str


class LLMConfig(CamelModel):
    id: UUID
    org_id: UUID
    name: str
    provider: LLMProvider
    model: str
    base_url: str = ""
    api_key: str = ""
    temperature: float = 0.4
    max_tokens: int = 512
    is_default: bool = False
    fallback_id: UUID | None = None


class AgentProfile(CamelModel):
    id: UUID
    org_id: UUID
    name: str
    system_prompt: str = ""
    greeting: str = ""
    language: str = "mn"
    llm_config_id: UUID | None = None
    stt_provider: str = "faster_whisper"
    stt_model: str = "large-v3"
    tts_provider: str = "piper"
    tts_voice: str = ""
    max_duration_sec: int = 600
    tools: list[str] = Field(default_factory=list)
    transfer_number: str = ""


class SIPNumber(CamelModel):
    id: UUID
    org_id: UUID
    number: str
    label: str = ""
    agent_profile_id: UUID | None = None
    allow_inbound: bool = True
    allow_outbound: bool = True
    active: bool = True


class Contact(CamelModel):
    id: UUID
    org_id: UUID
    phone: str
    name: str = ""
    tags: list[str] = Field(default_factory=list)
    meta: dict[str, str] = Field(default_factory=dict)


class Call(CamelModel):
    id: UUID
    org_id: UUID
    contact_id: UUID | None = None
    campaign_id: UUID | None = None
    sip_number_id: UUID | None = None
    agent_profile_id: UUID | None = None
    direction: CallDirection
    status: CallStatus
    from_number: str = ""
    to_number: str = ""
    room_name: str = ""
    started_at: datetime | None = None
    answered_at: datetime | None = None
    ended_at: datetime | None = None
    duration_sec: int = 0
    summary: str = ""
    sentiment: str = ""
    intent: str = ""
    end_reason: str = ""
    llm_model_used: str = ""
    metadata: dict[str, Any] = Field(default_factory=dict)


class TranscriptTurn(CamelModel):
    id: UUID | None = None
    call_id: UUID
    seq: int = 0
    speaker: Speaker
    text: str
    raw_text: str = ""
    confidence: float = 0.0
    start_ms: int = 0
    end_ms: int = 0
    is_final: bool = True


class LexiconEntry(CamelModel):
    wrong: str
    correct: str
    scope: LexiconScope = LexiconScope.BOTH
    phonetic: str = ""
    id: UUID | None = None


class CampaignInfo(CamelModel):
    id: UUID
    name: str
    script: str = ""
    vars: dict[str, str] = Field(default_factory=dict)


class Bootstrap(CamelModel):
    """Response of GET /internal/agent/bootstrap."""

    call: Call
    org: Organization
    sip_number: SIPNumber | None = None
    profile: AgentProfile
    llm: LLMConfig | None = None
    llm_fallbacks: list[LLMConfig] = Field(default_factory=list)
    lexicon: list[LexiconEntry] = Field(default_factory=list)
    contact: Contact | None = None
    campaign: CampaignInfo | None = None


# ---- live events ------------------------------------------------------------

EventType = Literal[
    "call.started",
    "call.ringing",
    "call.answered",
    "call.ended",
    "call.updated",
    "transcript.partial",
    "transcript.final",
    "agent.state",
    "campaign.progress",
    "lexicon.updated",
    "system",
]


class Event(CamelModel):
    id: str
    type: EventType
    org_id: UUID
    call_id: UUID | None = None
    at: datetime
    payload: dict[str, Any] = Field(default_factory=dict)


class EventBatch(CamelModel):
    events: list[Event]


class CallEndedPayload(CamelModel):
    end_reason: EndReason = "hangup_customer"
    summary: str = ""
    sentiment: Sentiment = Sentiment.NEUTRAL
    intent: str = ""
    duration_sec: int = 0
    llm_model_used: str = ""


class JobMetadata(CamelModel):
    """JSON placed in the LiveKit job / dispatch metadata by the backend."""

    call_id: UUID | None = None
    campaign_id: UUID | None = None
    sip_number_id: UUID | None = None
    direction: CallDirection = CallDirection.INBOUND
    to_number: str = ""
    from_number: str = ""
