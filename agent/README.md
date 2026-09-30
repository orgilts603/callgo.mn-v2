# CallGo.mn agent worker

Python voice-AI worker built on [LiveKit Agents](https://docs.livekit.io/agents/) 1.8.
It joins LiveKit rooms created by the SIP bridge, runs the STT -> LLM -> TTS pipeline and
reports everything to the Go backend. See `docs/ARCHITECTURE.md` for the whole system.

## Worker architecture

```
LiveKit room (SIP caller)
   |  audio
   v
VAD (silero) -> STT (faster-whisper | cloud) -> normalize_stt + lexicon
   -> LLM router (openai / anthropic / google / groq / ollama / openai_compatible,
                  FallbackAdapter over the configured fallback chain)
   -> normalize_for_tts + lexicon -> TTS (piper | cloud) -> room
   tools: end_call, transfer_call, ...

per job:  GET  {backend}/internal/agent/bootstrap   profile, LLM config (+ key), lexicon, contact
          POST {backend}/internal/agent/events      transcript / state / call.ended (batched)
          POST {backend}/internal/agent/lexicon-hit
worker:   GET  :8090/health      {ok, workerId, activeJobs}
          POST :8090/test-llm    {config, prompt} -> {ok, reply, latencyMs, error}
```

| Module | Role |
|---|---|
| `main.py` | CLI entrypoint (`dev` / `start`), registers the worker with agent name `callgo` |
| `session.py` | `CallGoAgent`: prompt, tools, lifecycle of one call |
| `pipeline.py` | `build_stt(profile)`, `build_tts(profile)` |
| `llm_router.py` | `build_llm(config, fallbacks)`, `test_config(config, prompt)` |
| `normalizer.py` | Mongolian text normalization + lexicon (`normalize_stt`, `normalize_for_tts`) |
| `schemas.py`, `config.py` | Frozen wire models and settings |
| `http_server.py` | Worker HTTP server + `WorkerStatus` |
| `mock_backend.py` | Fake Go backend for local dev/tests |
| `harness.py`, `audio_utils.py` | Offline pipeline runner and audio helpers |

## Setup

```bash
cd agent
uv venv .venv && uv pip install -e ".[dev]"     # or: uv pip install --system .
cp .env.example .env                             # then edit
```

### Environment variables

Settings use the `CALLGO_` prefix and are read from the environment or `agent/.env`.

| Variable | Default | Meaning |
|---|---|---|
| `CALLGO_BACKEND_URL` | `http://localhost:8080` | Go backend base URL |
| `CALLGO_AGENT_TOKEN` | `dev-agent-token` | Sent as `X-Agent-Token` |
| `CALLGO_AGENT_NAME` | `callgo` | LiveKit explicit-dispatch agent name |
| `CALLGO_HTTP_PORT` | `8090` | Worker HTTP server port |
| `LIVEKIT_URL` / `LIVEKIT_API_KEY` / `LIVEKIT_API_SECRET` | `ws://localhost:7880` / `devkey` / `secret` | LiveKit server |
| `CALLGO_WHISPER_MODEL` | `large-v3` | faster-whisper model id or path |
| `CALLGO_WHISPER_DEVICE` / `CALLGO_WHISPER_COMPUTE_TYPE` | `auto` / `auto` | `cpu`/`cuda`, `int8`/`float16` |
| `CALLGO_PIPER_VOICES_DIR` | `./models/piper` | Piper `.onnx` + `.onnx.json` voices |
| `CALLGO_PIPER_DEFAULT_VOICE` | `mn_MN-default-medium` | Voice when the profile sets none |
| `CALLGO_DEFAULT_LANGUAGE` | `mn` | Fallback language |
| `CALLGO_MAX_CALL_DURATION_SEC` | `600` | Hard cap per call |
| `CALLGO_EVENT_FLUSH_INTERVAL_MS` | `250` | Event batching interval |
| `HF_HOME` | `~/.cache/huggingface` | Model cache (`/models/hf` in Docker) |

## Run modes

```bash
python -m callgo_agent.main dev     # auto-reload, verbose logs, for development
python -m callgo_agent.main start   # production mode (what the Docker image runs)
```

Both connect to `LIVEKIT_URL` and wait for dispatches to agent `callgo`. The worker HTTP
server on `CALLGO_HTTP_PORT` starts alongside.

### Local development without a phone trunk

1. Start the fake backend (or the real Go backend):
   ```bash
   python -m callgo_agent.mock_backend --port 8080
   ```
   It serves `GET /internal/agent/bootstrap`, `POST /internal/agent/events`,
   `POST /internal/agent/lexicon-hit` (all need `X-Agent-Token`), and `GET /mock/events`
   (`?type=transcript.final` to filter; `DELETE` clears). Events are printed as one line each.
   The demo LLM is picked from the environment: first of `OPENAI_API_KEY`, `GOOGLE_API_KEY`,
   `ANTHROPIC_API_KEY`, `GROQ_API_KEY`; with none it points at Ollama
   (`http://localhost:11434/v1`, model `qwen2.5`). Override with `MOCK_LLM_PROVIDER`,
   `MOCK_LLM_MODEL`, `MOCK_LLM_BASE_URL`, `MOCK_LLM_API_KEY`.
2. `python -m callgo_agent.main dev` and join a room from the LiveKit playground/console.

Check the worker:

```bash
curl localhost:8090/health
curl -X POST localhost:8090/test-llm -H 'content-type: application/json' \
  -d '{"config": {"id": "...", "orgId": "...", "name": "t", "provider": "openai",
       "model": "gpt-4o-mini", "apiKey": "sk-..."}, "prompt": "Сайн уу"}'
```

## Offline harness (no LiveKit, no phone)

Runs one turn through the real pipeline and prints a latency report:

```bash
python -m callgo_agent.harness --wav tests/fixtures/speechlike.wav --out out.wav
python -m callgo_agent.harness --wav in.wav --profile bootstrap.json --out reply.wav
python -m callgo_agent.harness --text "Танай ажлын цаг хэд вэ?"      # skip STT
python -m callgo_agent.harness --fake --wav in.wav --text "Сайн уу"   # no models, no network
```

* `--wav` any WAV (resampled to 16 kHz mono); utterances are split with an energy VAD.
* `--text` replaces the STT stage (with `--fake`, the fake STT returns it).
* `--profile` an AgentProfile JSON or a saved bootstrap response (uses its `llm`, `lexicon`);
  default is the mock backend's demo profile and LLM.
* `--fake` fake STT + echo LLM + beep TTS, so it runs anywhere; use it in CI.
* `--json` prints `{reply, timings}` only.

Report fields: `stt_ms`, `llm_first_token_ms`, `llm_total_ms`, `tts_first_audio_ms`,
`tts_total_ms`, `total_ms`. Stage functions `run_stt`, `run_llm`, `run_tts` and
`run_pipeline` are importable for custom benchmarks.
Generate the tiny sample input with `python -m callgo_agent.audio_utils out.wav 8000`.

## Models

* **Silero VAD** and the **turn detector** ship with / are fetched by livekit-agents:
  `python -m callgo_agent.main download-files`.
* **faster-whisper**: downloaded from Hugging Face on first use into `HF_HOME`. Pre-fetch:
  `python -c "from faster_whisper import WhisperModel; WhisperModel('large-v3')"`.
  Use `small`/`medium` with `CALLGO_WHISPER_DEVICE=cpu CALLGO_WHISPER_COMPUTE_TYPE=int8`
  on machines without a GPU.
* **Piper voices**: put `<voice>.onnx` and `<voice>.onnx.json` in `CALLGO_PIPER_VOICES_DIR`
  (`python -m piper.download_voices <voice> --data-dir models/piper`).

## Docker

```bash
docker build -t callgo-agent agent/
docker run --rm -v callgo-models:/models --env-file agent/.env \
  -e CALLGO_BACKEND_URL=http://host.docker.internal:8080 -p 8090:8090 callgo-agent
```

The image runs as a non-root user (uid 10001), keeps all weights in the `/models` volume
(`HF_HOME=/models/hf`, `CALLGO_PIPER_VOICES_DIR=/models/piper`) and has a `/health` healthcheck.

## Tests

```bash
.venv/bin/pytest -q
.venv/bin/ruff check callgo_agent
```

## Troubleshooting

| Symptom | Fix |
|---|---|
| Worker never gets jobs | `CALLGO_AGENT_NAME` must match the dispatch rule's agent name; check `LIVEKIT_URL`/keys. |
| Bootstrap `401` | `CALLGO_AGENT_TOKEN` must equal the backend's `CALLGO_AGENT_TOKEN`. |
| First call is slow / times out | Models download or load lazily; pre-fetch them (see Models) and mount `/models`. |
| `CUDA` / `cublas` errors | Set `CALLGO_WHISPER_DEVICE=cpu` and `CALLGO_WHISPER_COMPUTE_TYPE=int8`. |
| Piper voice not found | Both `.onnx` and `.onnx.json` must be in `CALLGO_PIPER_VOICES_DIR`. |
| `/test-llm` returns `ok:false` | The `error` field carries the provider message (bad key, wrong base URL, model name). |
| Harness `no audio produced` | STT heard nothing (VAD threshold) or the LLM replied empty; try `--text`. |
| Port 8090 in use | Set `CALLGO_HTTP_PORT`. |
