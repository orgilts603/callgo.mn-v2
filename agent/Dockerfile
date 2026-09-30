# syntax=docker/dockerfile:1
FROM python:3.11-slim

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    UV_LINK_MODE=copy \
    UV_COMPILE_BYTECODE=1 \
    HF_HOME=/models/hf \
    CALLGO_PIPER_VOICES_DIR=/models/piper

# libgomp/libsndfile: runtime deps of ctranslate2 / audio libs; ffmpeg is not needed.
RUN apt-get update \
    && apt-get install -y --no-install-recommends libgomp1 libsndfile1 ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && pip install --no-cache-dir uv

WORKDIR /app
# Dependency layer first (cache friendly), then the source.
COPY pyproject.toml ./
COPY callgo_agent ./callgo_agent
RUN uv pip install --system --no-cache .

RUN useradd --create-home --uid 10001 callgo \
    && mkdir -p /models/piper /models/hf \
    && chown -R callgo:callgo /models /app
USER callgo

# Whisper / Piper / turn-detector weights live here; mount a volume to keep them across restarts.
VOLUME ["/models"]

# Worker HTTP server (/health, /test-llm)
EXPOSE 8090
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 \
    CMD python -c "import os,urllib.request as u; u.urlopen('http://127.0.0.1:%s/health' % os.environ.get('CALLGO_HTTP_PORT','8090'), timeout=3)" || exit 1

CMD ["python", "-m", "callgo_agent.main", "start"]
