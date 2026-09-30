# CallGo.mn Backend API contract (v1)

Base URL: `http://localhost:8080`. All JSON. Timestamps are RFC-3339 UTC.
Entity JSON shapes are exactly the `json` tags in `backend/internal/domain/domain.go`
(camelCase). Lists return `{"items": [...], "total": N}`. Errors return
`{"error": {"code": "not_found|invalid|unauthorized|forbidden|conflict|rate_limited|internal", "message": "..."}}`
with matching HTTP status (404/400/401/403/409/429/500).

## Auth
- `POST /api/auth/login` `{email, password}` → `{token, user, org}`.
  JWT (HS256, `CALLGO_JWT_SECRET`), 24h, claims `{sub: userId, org: orgId, role}`.
- `GET /api/auth/me` → `{user, org}`.
- `POST /api/auth/register` `{orgName, email, password, name}` → same as login (creates org + owner). Only enabled when `CALLGO_ALLOW_SIGNUP=true`.
- All `/api/*` routes except login/register/webhook require `Authorization: Bearer <jwt>`.
  Every query is scoped to the token's `org`.
- Dev bootstrap: on start, if no users exist, seed org "CallGo Demo" (slug `demo`) with
  `admin@callgo.mn` / `CALLGO_ADMIN_PASSWORD` (default `admin1234`).
- WebSocket auth: `GET /api/ws?token=<jwt>` (browsers cannot set headers).

## Dashboard
- `GET /api/stats` → `CallStats`.
- `GET /api/stats/daily?days=14` → `{items: DailyCallCount[]}`.

## Calls
- `GET /api/calls?status=active,ringing&direction=inbound&campaignId=&q=&from=&to=&limit=50&offset=0` → `{items: Call[], total}`.
- `GET /api/calls/active` → `{items: Call[]}` (status in queued/ringing/active).
- `GET /api/calls/{id}` → `{call: Call, turns: TranscriptTurn[], contact: Contact|null}`.
- `POST /api/calls/{id}/hangup` → 204. Calls `Telephony.Hangup(roomName)`.
- `POST /api/calls/{id}/transfer` `{toNumber}` → 204.
- `GET /api/calls/{id}/recording` → 302 to `recordingUrl` or 404.
- `POST /api/calls/dial` `{toNumber, sipNumberId, agentProfileId?, contactId?}` → `{call: Call}` (manual outbound test call).

## Transcript correction → lexicon (self-improving loop)
- `PATCH /api/turns/{turnId}` `{text, wrong?, correct?, scope?: "stt"|"tts"|"both", phonetic?}`
  → `{turn: TranscriptTurn, correction: LexiconCorrection|null}`.
  Updates the turn text; when `wrong`+`correct` are given, also inserts a
  `LexiconCorrection` (dedup on org+wrong, case-insensitive) and publishes `lexicon.updated`.

## Lexicon
- `GET /api/lexicon` → `{items: LexiconCorrection[], total}`.
- `POST /api/lexicon` `{wrong, correct, scope, phonetic?}` → `{correction}`.
- `PUT /api/lexicon/{id}` same body → `{correction}`.
- `DELETE /api/lexicon/{id}` → 204.
- `POST /api/lexicon/apply` `{text}` → `{text, hits: [{id, wrong, correct}]}` (preview).

## Contacts
- `GET /api/contacts?q=&limit=&offset=` → `{items, total}`.
- `POST /api/contacts` `{phone, name, tags?, meta?}` → `{contact}` (upsert on org+phone).
- `GET /api/contacts/{id}` → `{contact, calls: Call[]}` (last 20 calls).
- `DELETE /api/contacts/{id}` → 204.
- `POST /api/contacts/import` multipart `file` (CSV) → `{imported, skipped, errors: [{row, message}]}`.

## Campaigns
- `GET /api/campaigns` → `{items: Campaign[]}`.
- `POST /api/campaigns` multipart: `name`, `script`, `sipNumberId`, `agentProfileId`, `concurrency` (1-50, default 2), `maxAttempts` (default 2), `file` (CSV with header; columns `phone` required, `name` optional, other columns → target `vars`) → `{campaign, targets: {imported, skipped, errors}}`.
- `GET /api/campaigns/{id}` → `{campaign, targets: {items: CampaignTarget[], total}}` (`?limit=&offset=`).
- `POST /api/campaigns/{id}/start` → `{campaign}` (status running).
- `POST /api/campaigns/{id}/pause` → `{campaign}`.
- `DELETE /api/campaigns/{id}` → 204 (only draft/completed/paused).

## SIP numbers
- `GET /api/sip-numbers` → `{items: SIPNumber[]}`.
- `POST /api/sip-numbers` `{number, label, agentProfileId?, allowInbound, allowOutbound, asteriskEndpoint?}` → `{sipNumber}`; also calls `Telephony.EnsureNumberProvisioned`.
- `PUT /api/sip-numbers/{id}` same body + `active` → `{sipNumber}`.
- `DELETE /api/sip-numbers/{id}` → 204 (deprovisions).
- `POST /api/sip-numbers/{id}/provision` → `{sipNumber}` (re-run provisioning).

## Agent profiles
- `GET /api/agent-profiles` → `{items}`; `POST /api/agent-profiles` `{name, systemPrompt, greeting, language, llmConfigId?, sttProvider, sttModel, ttsProvider, ttsVoice, maxDurationSec, tools[], transferNumber?}` → `{profile}`; `PUT /api/agent-profiles/{id}`; `DELETE`.

## LLM configs
- `GET /api/llm-configs` → `{items: LLMConfig[]}` (apiKeyHint only).
- `POST /api/llm-configs` `{name, provider, model, baseUrl?, apiKey, temperature, maxTokens, isDefault, fallbackId?}` → `{config}`.
- `PUT /api/llm-configs/{id}` same (omit/empty `apiKey` keeps the stored key).
- `DELETE /api/llm-configs/{id}` → 204.
- `POST /api/llm-configs/{id}/test` `{prompt?}` → `{ok, reply, latencyMs, error?}` — backend proxies to the agent worker's `POST /test-llm` (see agent internal API) or performs a minimal chat completion itself for openai-compatible providers.
- `GET /api/llm-configs/catalog` → `{providers: [{provider, label, models: string[], needsApiKey, needsBaseUrl}]}` (static list).

## Live WebSocket
- `GET /api/ws?token=…` — server → client messages are `Event` JSON (see `docs/EVENTS.md`).
  On connect the server sends `{"type":"system","payload":{"hello":true,"activeCalls":[Call...]}}`.
  Client → server: `{"type":"ping"}` → `{"type":"pong"}`; `{"type":"subscribe","callId":"…"}` is optional (server sends all org events anyway).

## LiveKit webhook
- `POST /api/livekit/webhook` — raw body verified with `webhook.NewReceiver(auth.NewSimpleKeyProvider(key, secret))` from `github.com/livekit/protocol/webhook`. Handles `room_started`, `participant_joined`, `participant_left`, `room_finished`, `egress_ended` (recording URL).

## Internal API for the Python agent worker
All under `/internal/agent/*`, protected by header `X-Agent-Token: $CALLGO_AGENT_TOKEN`.
- `GET /internal/agent/bootstrap?room=<roomName>&sipNumber=<E.164>&from=<E.164>&to=<E.164>&direction=inbound|outbound&callId=<uuid?>`
  → `{ "call": Call, "org": Organization, "sipNumber": SIPNumber, "profile": AgentProfile,
       "llm": LLMConfig (WITH apiKey), "llmFallbacks": LLMConfig[] (with apiKey, in order),
       "lexicon": [{wrong, correct, scope, phonetic}], "contact": Contact|null,
       "campaign": {id, name, script, vars: {...}}|null }`.
  If no Call row exists for the room yet (inbound before the webhook), the backend creates it.
- `POST /internal/agent/events` body `{ "events": [Event...] }` where each `Event` is
  from `docs/EVENTS.md` with `callId` set (batching allowed). The backend persists
  transcript turns (`transcript.final` → `TranscriptTurn`), call state changes
  (`call.answered`, `call.ended` with `payload.{endReason, summary, sentiment, intent, durationSec, llmModelUsed}`),
  and re-broadcasts everything to browsers. Returns `{accepted: N}`.
- `POST /internal/agent/lexicon-hit` `{ids: [uuid]}` → 204 (increments hit counts).

## Agent worker HTTP (Python, port 8090)
- `GET /health` → `{ok: true, workerId, activeJobs}`.
- `POST /test-llm` `{config: LLMConfig-with-apiKey, prompt}` → `{ok, reply, latencyMs, error?}`.
