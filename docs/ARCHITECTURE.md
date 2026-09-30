# CallGo.mn v2 — Architecture

CallGo.mn is a Voice-AI call-center CRM. Phone calls arrive through **Asterisk**
(PBX / carrier trunk), are bridged by **LiveKit SIP** into a LiveKit room, and
are answered by a **LiveKit Agents** worker (Python) running an STT → LLM → TTS
pipeline. The **Go backend** is the CRM control plane (tenants, SIP numbers,
agent profiles, LLM configs, contacts, calls, transcripts, campaigns, lexicon)
and pushes live events to the **React** dashboard.

```
 PSTN / Carrier
      │ SIP trunk
      ▼
 ┌──────────┐  SIP/RTP   ┌──────────────┐  WebRTC   ┌──────────────────┐
 │ Asterisk │──────────▶│ livekit-sip  │─────────▶│ livekit-server   │
 │  (PBX)   │◀──────────│ (bridge)     │◀─────────│ (rooms, redis)   │
 └──────────┘            └──────────────┘           └────────┬─────────┘
                                                             │ room join (agent dispatch)
                                                             ▼
                    ┌──────────────────────────────────────────────────────┐
                    │ agent/  (Python, livekit-agents 1.8)                 │
                    │  VAD(silero) → STT(faster-whisper|cloud)             │
                    │  → normalizer + lexicon → LLM router (multi-provider)│
                    │  → TTS(piper|cloud) ; tools: end_call, transfer, …   │
                    └───────────────┬──────────────────────────────────────┘
                                    │ HTTP (internal API, X-Agent-Token)
                                    ▼
 ┌──────────────┐  REST/WS  ┌───────────────────────────────────────────────┐
 │ frontend/    │◀─────────▶│ backend/ (Go 1.26, chi, pgx, LiveKit SDK)    │
 │ React+Vite   │           │  internal/httpapi  REST + WS                  │
 │ Tailwind v4  │           │  internal/live     event hub (fan-out)        │
 └──────────────┘           │  internal/crm      PostgreSQL repos + migrations│
                            │  internal/livekit  Telephony port (SIP/rooms) │
                            │  internal/campaign outbound dialer engine     │
                            │  internal/lexicon  correction engine          │
                            │  internal/csvimport, internal/config, …      │
                            └───────────────────────┬───────────────────────┘
                                                    ▼
                                              PostgreSQL 16
```

## Repos and directories

| Path | Language | Purpose |
|---|---|---|
| `backend/` | Go | CRM control plane, REST + WebSocket API, dialer, LiveKit control |
| `agent/` | Python | Voice AI worker built on `livekit-agents` |
| `frontend/` | TypeScript | Dark-mode SaaS dashboard |
| `infra/` | YAML/conf | docker-compose, livekit / livekit-sip / redis / asterisk configs |
| `third_party/` | — | Read-only clones of `livekit/agents`, `livekit/sip`, `livekit/livekit` for reference |
| `docs/` | — | This file, `API.md`, `EVENTS.md`, `AGENT_RULES.md` |

## Call flows

### Inbound
1. Carrier → Asterisk (PJSIP endpoint) → dialplan forwards the DID to LiveKit SIP (`infra/asterisk`).
2. livekit-sip matches an **inbound trunk** + **dispatch rule** (created by the backend when a `SIPNumber` is provisioned). Rule type: *individual* room per call, room name prefix `call-` (inbound rooms become `call-in_<caller>_<random>`; outbound rooms are `call-<callId>`), with an **agent dispatch** to agent name `callgo` and metadata `{"sipNumberId": "...", "direction": "inbound"}`.
3. livekit-server fires webhooks (`room_started`, `participant_joined`, `participant_left`, `room_finished`) → `POST /api/livekit/webhook` → backend creates/updates a `Call` row and publishes live events.
4. The agent worker receives the job, calls `GET /internal/agent/bootstrap?room=…` to fetch the resolved `AgentProfile`, `LLMConfig` (decrypted), lexicon and contact; runs the session; streams transcript turns / state to `POST /internal/agent/events`.
5. On hangup the agent posts `call.ended` with the summary/sentiment/intent it computed (post-call LLM analysis), the backend closes the call.

### Outbound (campaign)
1. Admin uploads CSV → `Campaign` + `CampaignTarget`s.
2. `internal/campaign` engine claims targets (`FOR UPDATE SKIP LOCKED`), creates a `Call` (status `queued`), and calls `Telephony.Dial` → LiveKit `CreateSIPParticipant` on the number's **outbound trunk** with `wait_until_answered` + agent dispatch metadata `{"callId": …, "campaignId": …, "direction": "outbound"}`.
3. Same agent flow as inbound from step 4; the engine updates target status from the call outcome and retries per `maxAttempts`.

## Multi-LLM routing
`LLMConfig` rows per org: provider (`openai`, `anthropic`, `google`, `groq`, `ollama`, `openai_compatible`), model, base URL, encrypted API key, temperature, fallback chain. The Python `llm_router` maps a config to a livekit plugin LLM (`openai.LLM`, `anthropic.LLM`, `google.LLM`, `groq.LLM`, `openai.LLM.with_ollama` / `base_url`) and wraps it in a `FallbackAdapter`.

## Local dev without a trunk
- `Telephony` has a **mock** implementation (`internal/livekit/mock.go`) that simulates ringing/answer/hangup timings.
- `internal/live/simulator.go` generates fake calls + transcript events so the Live Desk can be demoed.
- `agent/callgo_agent/harness.py` runs the pipeline against WAV files with no LiveKit server.
