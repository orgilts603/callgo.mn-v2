"""Worker settings (env / .env). Shared by all agent modules."""

from __future__ import annotations

from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_prefix="CALLGO_", env_file=".env", extra="ignore")

    backend_url: str = "http://localhost:8080"
    agent_token: str = "dev-agent-token"
    agent_name: str = "callgo"  # LiveKit explicit dispatch agent name
    http_port: int = 8090

    # Local models
    whisper_model: str = "large-v3"  # faster-whisper model id or path
    whisper_device: str = "auto"  # cpu | cuda | auto
    whisper_compute_type: str = "auto"  # int8 | float16 | auto
    piper_voices_dir: str = "./models/piper"
    piper_default_voice: str = "mn_MN-default-medium"

    # Behaviour
    default_language: str = "mn"
    max_call_duration_sec: int = 600
    event_flush_interval_ms: int = 250
    # "vad" (default) or "multilingual" (LiveKit turn-detector model; falls back
    # to VAD for languages it does not cover, including Mongolian).
    turn_detector: str = "vad"
    log_level: str = "INFO"

    livekit_url: str = Field(default="ws://localhost:7880", alias="LIVEKIT_URL")
    livekit_api_key: str = Field(default="devkey", alias="LIVEKIT_API_KEY")
    livekit_api_secret: str = Field(default="secret", alias="LIVEKIT_API_SECRET")


settings = Settings()
