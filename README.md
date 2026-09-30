# MISSION: Autonomous Research, Architecture & Full-Stack Development of CallGo.mn Voice AI CRM & SIP Integration

You are an autonomous Principal Systems Architect and Staff Full-Stack Engineer powered by Claude Fable. You have full permission to inspect the environment, execute shell commands, run tests, diagnose issues, and autonomously implement features.

Your objective is to design, research, and fully build the production-ready **CallGo.mn Voice AI Call Center CRM** and **SIP Telephony Gateway**.

---

### PHASE 1: ENVIRONMENT & CODEBASE DISCOVERY (Do this first)
Before writing any new code:
1. **Inspect the current environment:**
   - Scan the working directory, existing repos, active ports, and running processes (`ps aux`, `netstat`/`ss`, docker containers).
   - Check if FreeSWITCH, Asterisk, or any SIP PBX is running locally or configured.
   - Check existing Python environments, Piper TTS paths, and Faster-Whisper models.
2. **Identify available tools & constraints:**
   - Verify installed Go toolchain (`go version`), Node/NPM/Bun, PostgreSQL, and audio utilities (`ffmpeg`, `sox`, etc.).
3. **Formulate an action plan:**
   - Summarize your findings in a clear, concise step-by-step roadmap before proceeding to Phase 2.

---

### PHASE 2: ARCHITECTURE & RESEARCH
Autonomous decisions to validate and implement:
1. **Backend (Go 1.22+):**
   - Design a high-concurrency Go service with clean hexagonal architecture:
     - `/internal/telephony`: Bidirectional WebSocket audio bridge for SIP (handling 8kHz/16kHz PCM audio streaming, VAD events, barge-in cancellation).
     - `/internal/ai`: Orchestrator connecting Faster-Whisper, LLM reasoning, Mongolian Text Normalizer, and Piper TTS streaming.
     - `/internal/crm`: PostgreSQL data access for call logs, contacts, campaigns, and audio recordings.
     - `/internal/live`: SSE / WebSocket hub for live call streaming to the frontend.
2. **Frontend (React + Tailwind CSS):**
   - Fast, modern, dark-mode SaaS UI (Linear/Vapi style) matching CallGo.mn branding:
     - Real-time Live Calls Desk (TanStack Table v8 updating live over WebSocket).
     - Call drawer with interactive waveform audio player, transcript turns, sentiment badge, and LLM summary.
     - Outbound Campaign manager (CSV upload -> multi-line automated calling).
3. **Self-Improving Lexicon / Feedback Loop:**
   - Build a mechanism where an admin can click any misrecognized word in a transcript, correct it, and automatically append it to the pronunciation lexicon / normalizer dictionary so the AI never repeats the mistake.

---

### PHASE 3: AUTONOMOUS IMPLEMENTATION
Execute systematically without waiting for micro-instructions:
1. **Database:** Initialize PostgreSQL schemas and migrations for `organizations`, `contacts`, `call_logs`, `call_transcripts`, `campaigns`, and `lexicon_corrections`.
2. **Backend Engine:**
   - Scaffold the Go backend, dependencies, and database pool.
   - Implement the WebSocket audio gateway capable of streaming raw PCM chunks with minimal latency (<700ms end-to-end target).
   - Build mock telephony event generators so the system can be tested even without an active telecom trunk.
3. **Frontend Application:**
   - Scaffold the React + Vite + Tailwind frontend.
   - Implement the Live Desk with real-time status pulses, audio player, and call inspection.
4. **Self-Healing & Testing:**
   - Write unit and integration tests for audio chunking, normalizer rules, and API endpoints.
   - Execute the tests, read compiler/runtime errors, and fix them autonomously.

---

### EXECUTION DIRECTIVES:
- **Do not take easy shortcuts:** Build real, idiomatic Go and modern React code, not dummy placeholders.
- **Root-cause debugging:** If any library, build, or test fails, inspect the error output, diagnose the underlying cause, and fix it directly.
- **Progress reporting:** Provide brief one-line status updates before initiating major phases so I can follow along.

Begin Phase 1 now: inspect the filesystem and environment, and report your discovery.

---

## Project status & quick start

The mission above is implemented as a monorepo. Architecture, contracts and
deployment live in `docs/`:

| Doc | What it covers |
|---|---|
| `docs/ARCHITECTURE.md` | Asterisk → LiveKit SIP → LiveKit Agents → Go CRM → React; call flows; multi-LLM routing |
| `docs/API.md` | Every REST / WebSocket / internal endpoint (frozen contract) |
| `docs/EVENTS.md` | Live event envelope and payloads |
| `docs/API.md` → "Campaign v2" and "Knowledge base / RAG" | Excel campaigns with schedules/outcomes/dry-run/do-not-call; pgvector knowledge bases with `tool` / `context` modes |
| `docs/DEPLOY.md`, `docs/ASTERISK.md`, `docs/SIP_FLOW.md` | Production deployment, carrier trunk setup, SIP sequence diagrams |

### Run locally (no SIP trunk needed)

```bash
# 1. PostgreSQL 16 with a callgo/callgo user (see backend/.env.example)
# 2. Backend — mock telephony + fake-call simulator
cd backend && CALLGO_SIMULATOR=true go run ./cmd/server          # :8080
# 3. Frontend
cd frontend && pnpm install && pnpm dev                           # :5173 → login admin@callgo.mn / admin1234
# 4. Voice agent worker (needs a LiveKit server; see docs/DEPLOY.md)
cd agent && uv venv && uv pip install -e ".[dev]" && python -m callgo_agent.main dev
```

### Tests

```bash
cd backend  && go test ./...                 # Go: 18 packages incl. Postgres+pgvector integration (CALLGO_TEST_DATABASE_URL)
cd frontend && pnpm exec vitest run          # React: 132 tests
cd agent    && .venv/bin/pytest              # Python: 600 tests
```

### Layout

```
backend/   Go 1.26 control plane  (internal/{crm,httpapi,auth,livekit,campaign,live,lexicon,knowledge,embed,csvimport,xlsxexport,phone,audio,config,middleware,llmtest})
agent/     Python LiveKit Agents worker (session, tools, knowledge (RAG), llm_router, stt_local=faster-whisper, tts_local=piper, normalizer)
frontend/  React 19 + Vite + Tailwind v4 dashboard (live desk, call drawer, campaigns, contacts, lexicon, settings)
infra/     docker-compose, livekit-server, livekit-sip, redis, Asterisk PJSIP configs, lk CLI scripts
docs/      contracts and runbooks
```
