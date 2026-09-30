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

## Campaign v2 additions (Excel in → AI calls → Excel out)

### Campaign fields (in every Campaign JSON)
- `schedule`: `{timezone: "Asia/Ulaanbaatar", weekdays: [1,2,3,4,5], startTime: "09:00", endTime: "18:00", pacePerMinute: 10}`. Empty object = dial anytime. Weekdays use 0=Sunday..6=Saturday.
- `outcomes`: `[{code, label, description, terminal}]`. When non-empty the AI must choose one at the end of each call. Non-terminal outcomes (e.g. `callback`) re-queue the target while attempts remain.
- `dryRunLimit` (int), `dryRunDialed` (int), `skipped` (int).
- Default outcomes offered by the UI (editable): `agreed` Зөвшөөрсөн (terminal), `declined` Татгалзсан (terminal), `callback` Дахин залгах (non-terminal), `wrong_number` Буруу дугаар (terminal), `no_contact` Холбогдоогүй (terminal).

### Target fields
- `status` gains `skipped` (do-not-call at import or claim time; `lastError` says why).
- `outcome` (code), `outcomeNote` (LLM one-liner).

### Call fields
- `outcome`, `outcomeNote` — filled from the agent's `call.ended` payload (`payload.outcome`, `payload.outcomeNote`).

### Endpoints
- `POST /api/campaigns` (multipart) accepts `file` as **.csv or .xlsx/.xls** (first sheet, first row = header; same column detection as CSV). New optional fields: `schedule` (JSON string), `outcomes` (JSON string), `dryRunLimit` (int). Targets on the org's do-not-call list are imported with status `skipped`, counted in `campaign.skipped` and returned in `targets.skipped`.
- `POST /api/campaigns/preview` multipart `file` → `{columns: string[], rows: string[][] (first 10), mapping: {column: "phone"|"name"|"tags"|"var"}, total: N, format: "csv"|"xlsx"}`. Used by the UI for both CSV and Excel.
- `PUT /api/campaigns/{id}` `{name?, script?, sipNumberId?, agentProfileId?, concurrency?, maxAttempts?, schedule?, outcomes?, dryRunLimit?}` → `{campaign}`. Allowed while draft/paused; while running only schedule/concurrency/outcomes may change.
- `POST /api/campaigns/{id}/start` optional body `{dryRunLimit: N}` (overrides the stored value for this run; 0 = full run). When the dry-run limit is reached the engine sets status `paused` and publishes `campaign.progress`; a later `start` with `{dryRunLimit: 0}` continues with the remaining targets.
- `GET /api/campaigns/{id}/export.xlsx` → `application/vnd.openxmlformats-officedocument.spreadsheetml.sheet`. Sheet "Targets": original imported columns (phone, name, every var) followed by `Төлөв`, `Үр дүн` (outcome label), `Тайлбар` (outcomeNote), `Оролдлого`, `Хугацаа (сек)`, `Хандлага` (sentiment), `Хураангуй` (summary), `Дуудлагын огноо`, `Бичлэг` (recordingUrl), `Дуудлагын ID`. Sheet "Summary": totals per status and per outcome. Header row bold + frozen; filters on.
- `GET /api/campaigns/{id}/stats` → `{byStatus: {pending, calling, done, failed, skipped}, byOutcome: [{code, label, count}]}`.

### Do-not-call list
- `GET /api/dnc?q=&limit=&offset=` → `{items: DoNotCallEntry[], total}`.
- `POST /api/dnc` `{phone, reason?}` → `{entry}` (201; idempotent → 200 with existing).
- `DELETE /api/dnc/{phone}` (URL-encoded E.164) → 204.
- `POST /api/dnc/import` multipart `file` (.csv/.xlsx, phone column detected) → `{imported, skipped, errors}`.
- `POST /api/calls/{id}/dnc` `{reason?}` → adds the call's customer number; 201.
- Manual `POST /api/calls/dial` refuses (409 `conflict`) a number on the list.

### Agent bootstrap (internal) additions
- `campaign` gains `outcomes: [{code, label, description, terminal}]` and `schedule` is NOT sent (engine-side only).
- `call.ended` payload may carry `outcome` (one of the codes, or "") and `outcomeNote`.

## Knowledge base / RAG

### Agent profile fields
- `knowledgeBaseId` (uuid|null), `knowledgeMode`: `off` | `tool` | `context`.
  - `tool`: the agent gets a `lookup_knowledge(question)` function tool; it searches the base and answers from the passages (or says it does not know / offers transfer).
  - `context`: the whole base (≤ 60 000 chars) is appended to the system prompt under "# Мэдлэгийн сан"; best for small manuals, cheap with provider context caching.

### Knowledge bases
- `GET /api/knowledge-bases` → `{items: KnowledgeBase[]}`.
- `POST /api/knowledge-bases` `{name, description?, embeddingLlmConfigId?, embeddingModel?, chunkSize?, chunkOverlap?}` → `{knowledgeBase}` (201). When `embeddingLlmConfigId` is empty the org's default LLM config is used; `embeddingModel` defaults per provider (openai `text-embedding-3-small`, google `text-embedding-004`, ollama `nomic-embed-text`, openai_compatible required).
- `PUT /api/knowledge-bases/{id}` same body (embedding settings are locked once `chunkCount > 0`; 409 otherwise) → `{knowledgeBase}`.
- `DELETE /api/knowledge-bases/{id}` → 204 (cascades). 409 if an agent profile still references it? No: profiles are unlinked (ON DELETE SET NULL).
- `GET /api/knowledge-bases/{id}` → `{knowledgeBase, documents: KnowledgeDocument[]}`.

### Documents
- `POST /api/knowledge-bases/{id}/documents` multipart `file` (pdf, docx, txt, md, csv; ≤ 20 MB) **or** JSON `{filename, text}` (pasted text) → `{document}` (202; status `processing`). Ingestion runs in the background: extract text → chunk → embed → store; status becomes `ready` or `failed` with `error`.
- `GET /api/knowledge-bases/{id}/documents` → `{items}`; `GET /api/knowledge-documents/{docId}` → `{document, chunks: [{id, seq, heading, content}]}` (first 50 chunks, `?offset=`).
- `DELETE /api/knowledge-documents/{docId}` → 204.
- `POST /api/knowledge-documents/{docId}/reprocess` → 202.

### Search (admin test panel)
- `POST /api/knowledge-bases/{id}/search` `{query, k?: 5}` → `{hits: KnowledgeHit[], latencyMs, mode: "hybrid"|"text"}` (`text` when the base has no embeddings yet / embedder unavailable).

### Internal (agent)
- `POST /internal/agent/knowledge/search` `{knowledgeBaseId, query, k?: 5}` → `{hits: KnowledgeHit[]}`.
- Bootstrap gains `knowledge: {id, name, mode, contextText?: string}|null` (`contextText` only in `context` mode, ≤ 60 000 chars, with a `truncated: true` flag when cut).

Hybrid search = RRF fusion of pgvector cosine top-20 and `to_tsvector('simple')` top-20, returning k. Scores are normalised to [0,1].

# SaaS API contract (identity, billing, recordings, handoff, integrations, routing, analytics, admin)

Conventions unchanged (JSON, `{items,total}`, error envelope). New auth methods:
- **Refresh tokens**: `POST /api/auth/login` now returns `{token, refreshToken, user, org, subscription}`; access JWT TTL 15 min, refresh 30 days (rotated on every use). `POST /api/auth/refresh` `{refreshToken}` → `{token, refreshToken}`; `POST /api/auth/logout` `{refreshToken}` → 204. Old 24h tokens keep working until expiry.
- **API keys**: header `Authorization: Bearer cg_live_…` authenticates as the org (role = admin, scopes enforced). `Claims.APIKeyID` set.
- **Roles**: owner > admin > operator. Platform staff (`user.isPlatformAdmin`) may call `/api/admin/*`.
- **Suspended org** (`org.status = suspended`): every route except auth, billing and `GET` reads returns 402 `{"error":{"code":"payment_required"}}`.
- New error codes: `payment_required` (402), `quota_exceeded` (429 with `{"error":{"code":"quota_exceeded","message":..., "details":{"limit":..,"used":..}}}`), `feature_unavailable` (403).

## Identity
- `POST /api/auth/signup` `{orgName, email, password, name, phone?}` → 201 `{token, refreshToken, user, org, subscription}`. Creates org (slug from name, unique), owner user (status active, emailVerifiedAt null), trial subscription (plan `trial`, 14 days), sends verification email. Rate limited 5/min/IP. Disabled when `CALLGO_ALLOW_SIGNUP=false` (403).
- `POST /api/auth/verify-email` `{token}` → `{user}`. `POST /api/auth/resend-verification` → 204.
- `POST /api/auth/forgot-password` `{email}` → 204 always (no enumeration). Email contains `https://<app>/reset-password?token=…` (token valid 1h, single use).
- `POST /api/auth/reset-password` `{token, password}` → 204 (revokes all sessions).
- `POST /api/auth/change-password` `{currentPassword, newPassword}` → 204.
- `GET /api/auth/sessions` → `{items: RefreshSession[]}`; `DELETE /api/auth/sessions/{id}` → 204.
- Members (owner/admin): `GET /api/org/members` → `{items: User[], invitations: Invitation[]}`; `POST /api/org/invitations` `{email, role}` → 201 `{invitation}` (email with accept link; 409 if member exists; 429 quota_exceeded when plan MaxUsers reached); `DELETE /api/org/invitations/{id}` → 204; `POST /api/auth/accept-invitation` `{token, name, password}` → `{token, refreshToken, user, org}` (public); `PUT /api/org/members/{userId}` `{role?, status?}` → `{user}` (cannot demote/disable the last owner or yourself); `DELETE /api/org/members/{userId}` → 204.
- Org: `GET /api/org` → `{org, subscription, plan}`; `PUT /api/org` `{name?, timezone?, settings?}` → `{org}` (owner/admin).
- API keys (owner/admin, feature `api`): `GET /api/org/api-keys` → `{items: APIKey[]}`; `POST /api/org/api-keys` `{name, scopes[]}` → 201 `{apiKey, plaintext}` (plaintext shown once); `DELETE /api/org/api-keys/{id}` → 204.
- Audit (owner/admin): `GET /api/org/audit?actorId=&action=&from=&to=&limit=&offset=` → `{items: AuditEntry[], total}`. Every mutating handler appends an entry (action `<resource>.<verb>`).

## Billing
- `GET /api/billing/plans` → `{items: Plan[]}` (public plans; no auth needed).
- `GET /api/billing/subscription` → `{subscription, plan, usage: UsageSummary, limits: Plan}`.
- `POST /api/billing/subscription` `{planCode}` → `{subscription, invoice?}` — switching to a paid plan creates an `open` invoice for the first period (prorated not required; full month); plan becomes effective when paid (status `past_due` until then if upgrading from trial with expired trial; `active` immediately when downgrading/paid).
- `POST /api/billing/subscription/cancel` → `{subscription}` (cancels at period end).
- `GET /api/billing/usage?from=&to=` → `{summary: UsageSummary, daily: [{day, minutes, calls, costMnt}]}`.
- `GET /api/billing/invoices` → `{items: Invoice[]}`; `GET /api/billing/invoices/{id}` → `{invoice, payments: Payment[]}`; `GET /api/billing/invoices/{id}/pdf` → a print-ready HTML invoice (`text/html`, Mongolian labels; the browser's print dialog produces the PDF).
- `POST /api/billing/invoices/{id}/pay` `{provider: "qpay"|"mock"}` → 201 `{payment}` with `qrText`, `qrImage`, `deepLinks`, `expiresAt`.
- `GET /api/billing/payments/{id}` → `{payment}` (UI polls every 3 s until paid/expired; also calls provider Check when pending and > 10 s old).
- `POST /api/billing/webhooks/qpay?payment_id=<uuid>` (public, provider callback) → 200 `SUCCESS`; verifies via provider Check, marks payment + invoice paid, activates subscription, audit entry.
- Enforcement: `POST /api/calls/dial`, campaign start and the dialer engine consult `Entitlements.CanStartCall` → 429 `quota_exceeded` / 402 `payment_required`. Agent profiles / SIP numbers / users / KB uploads check plan caps → 429 `quota_exceeded`. Feature-gated routes (`recordings`, `webhooks`, `sms`, `api`, `analytics`, `handoff`) → 403 `feature_unavailable`.
- Monthly job: on period end create invoice (plan fee + overage lines + VAT 10%), start next period; unpaid 7 days after due → org `suspended`; payment → `active`.

## Recordings
- Recording starts automatically on `call.answered` when the org has feature `recordings` and profile/org setting `recordCalls` (org.settings.recordCalls default true). Backend starts LiveKit room-composite audio egress (`audio_only`, OGG/MP4) to the object store (S3/MinIO; `local` driver for dev writes under `CALLGO_RECORDINGS_DIR`).
- `Call.recording` is filled from `egress_ended`; `Call.recordingUrl` = `/api/calls/{id}/recording`.
- `GET /api/calls/{id}/recording` → 302 to a signed URL (TTL 10 min) or 404. `GET /api/calls/{id}/recording/url` → `{url, expiresAt}` (for the player). `DELETE /api/calls/{id}/recording` → 204 (owner/admin).
- Retention: `org.settings.recordingRetentionDays` (default 90); nightly job deletes older objects and marks status `deleted`.

## Operator handoff
- `POST /api/calls/{id}/handoff` → `{token, url, roomName, identity}` — a LiveKit access token (identity `op-<userId>`, name = user name, attributes `callgo.role=operator`, `callgo.userId`) valid 1 h; sets `call.handoff=requested`, `operatorId`; publishes `call.updated`. Feature `handoff`; only for active calls.
- `POST /api/calls/{id}/handoff/end` → 204 (operator left; agent resumes).
- Agent worker: on a participant with `callgo.role=operator` joining, it says "Оператор холбогдлоо" once, stops generating replies (passive mode: STT continues, agent does not speak, `agent.state=idle`), publishes `call.updated` with `handoff=active`; when the operator leaves it resumes ("Би үргэлжлүүлье"). Operator speech is transcribed as `speaker=human` when the agent subscribes to the operator track (best effort).
- `GET /api/livekit/config` → `{url}` (public LiveKit WS URL for the browser SDK).

## Integrations
- Webhooks (feature `webhooks`, owner/admin): `GET /api/webhooks` → `{items}`; `POST /api/webhooks` `{url, events[], description?}` → 201 `{webhook, secret}` (secret shown once); `PUT /api/webhooks/{id}` `{url?, events?, active?, description?}`; `DELETE`; `POST /api/webhooks/{id}/test` → `{delivery}` (sends `system` event); `POST /api/webhooks/{id}/rotate-secret` → `{secret}`; `GET /api/webhooks/{id}/deliveries?limit=&offset=` → `{items, total}`; `POST /api/webhook-deliveries/{id}/retry` → `{delivery}`.
- Delivery: POST JSON `Event` with headers `X-CallGo-Event`, `X-CallGo-Delivery`, `X-CallGo-Timestamp`, `X-CallGo-Signature: sha256=<hmac(secret, timestamp + "." + body)>`; 10 s timeout; retries 1m, 5m, 30m, 2h, 12h; after 5 failures → failed; 20 consecutive failures → webhook `active=false`.
- SMS (feature `sms`): `GET /api/sms/config` → `{provider, from, configured}`; `PUT /api/sms/config` `{provider: "mock"|"http", url?, apiKey?, from?, bodyTemplate?}` (stored in org.settings.sms, apiKey encrypted); `POST /api/sms/send` `{to, body, callId?}` → 201 `{message}` (metered `UsageSMS`); `GET /api/sms?limit=&offset=` → `{items, total}`.
- Post-call actions: `AgentProfile.postCallActions[]` (validated: sms needs template + feature, webhook needs webhookId, callback delayMin 0..10080). Runner executes on `call.ended` (after outcome is known); SMS templates render `{{name}}`, `{{phone}}`, `{{summary}}`, `{{outcome}}`, `{{campaign}}`, `{{vars.X}}`.
- Callbacks: `GET /api/callbacks?status=&limit=&offset=` → `{items, total}`; `POST /api/callbacks` `{phone, name?, note?, dueAt, sipNumberId?, agentProfileId?, contactId?}` → 201; `PUT /api/callbacks/{id}` `{dueAt?, note?, status?: "canceled"}`; `DELETE`. The scheduler dials due callbacks (respecting business hours of the SIP number and entitlements) as outbound calls with `metadata.callbackId`; `call.ended` marks the callback done/failed (retry once after 30 min on no_answer/busy). The agent tool `schedule_callback(when, note)` now creates a CallbackRequest through the events ingest (`call.ended` payload `callbacks: [{dueAt, note}]`).

## Inbound routing
- `SIPNumber.routing: RoutingConfig` accepted on `POST/PUT /api/sip-numbers` (validated: menu keys unique in 0-9,*,#; profiles belong to org; timeout 3..30; repeat 0..3). `PUT /api/sip-numbers/{id}/routing` `RoutingConfig` → `{sipNumber}` (admin; same validation). `POST /api/sip-numbers/{id}/routing/resolve?at=<rfc3339>` → `{route: ResolvedRoute}` (preview).
- Bootstrap gains `route: ResolvedRoute` (inbound only) and the resolved `profile`. For `mode=after_hours` with no profile the agent speaks `message` and hangs up (`endReason: after_hours`). For `mode=menu` the agent speaks `menuPrompt`, waits for DTMF (`MenuTimeoutSec`, repeats `MenuRepeat` times), then re-bootstraps with `?profileId=` (`GET /internal/agent/bootstrap` accepts `profileId` to override) and continues; unknown key → repeats prompt.

## Analytics (feature `analytics`)
- `GET /api/analytics/overview?from=&to=` → `{calls, answered, answerRate, avgDurationSec, totalMinutes, costMnt, costPerCallMnt, sentiment: {positive, neutral, negative}, outcomes: [{code,label,count}], byDirection: {inbound, outbound}}`.
- `GET /api/analytics/timeseries?from=&to=&bucket=hour|day` → `{items: [{ts, calls, answered, minutes, costMnt}]}`.
- `GET /api/analytics/heatmap?from=&to=` → `{cells: [{weekday, hour, calls, answerRate}]}` (org timezone).
- `GET /api/analytics/profiles?from=&to=` → `{items: [{profileId, name, calls, answerRate, avgDurationSec, positiveRate, costMnt, outcomes: {...}}]}`.
- `GET /api/analytics/campaigns?from=&to=` → `{items: [{campaignId, name, total, done, failed, skipped, outcomes: {...}, minutes, costMnt}]}`.
- `GET /api/analytics/export.csv?from=&to=` → CSV of calls with outcome/usage columns.

## Platform admin (`user.isPlatformAdmin`)
- `GET /api/admin/orgs?q=&limit=&offset=` → `{items: [{org, subscription, usage: UsageSummary, users}]}`.
- `GET /api/admin/orgs/{id}` → `{org, subscription, plan, usage, users, invoices}`.
- `PUT /api/admin/orgs/{id}/subscription` `{planCode, status?, customLimits?, currentPeriodEnd?}` → `{subscription}`.
- `PUT /api/admin/orgs/{id}` `{status}` → `{org}` (suspend/close/reactivate).
- `POST /api/admin/invoices/{id}/mark-paid` `{note}` → `{invoice}` (bank transfer).
- `GET /api/admin/stats` → `{orgs, activeSubscriptions, mrrMnt, callsToday, minutesToday}`.

## Agent worker additions
- Bootstrap: `org.settings.recordCalls`, `route`, `entitlements: {canStart: bool, reason}` (agent hangs up politely when false), `handoff: {enabled: bool}`.
- `call.ended` payload gains `usage: CallUsage` and optional `callbacks: [{dueAt, note}]`; `call.updated` from the agent may carry `handoff: "active"|"ended"`.
- `POST /internal/agent/events` accepts `agent.state` with `state: "handoff"`.
