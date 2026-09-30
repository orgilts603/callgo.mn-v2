# CallGo.mn backend

Go 1.26 control plane for CallGo.mn: REST + WebSocket API, PostgreSQL
persistence, LiveKit SIP control, the outbound campaign dialer and the live
event hub. See `../docs/ARCHITECTURE.md` for the system overview and
`../docs/API.md` for the HTTP contract.

## Package map

| Path | Purpose |
|---|---|
| `cmd/server` | Process entry point: wires config, DB, services, HTTP server, graceful shutdown |
| `internal/domain` | Entities and port interfaces (repositories, `Telephony`, `EventBus`); no infrastructure imports |
| `internal/config` | Environment-based configuration, validation, zerolog logger |
| `internal/middleware` | chi-compatible middleware: request ID, access log, recoverer, rate limit, body limit, security headers, timeout |
| `internal/httpapi` | REST + WebSocket handlers, auth, routing |
| `internal/crm` | PostgreSQL repositories and embedded migrations |
| `internal/live` | In-process event hub (fan-out to browsers) and the call simulator |
| `internal/livekit` | `Telephony` adapters: LiveKit SIP client and a mock for local development |
| `internal/campaign` | Outbound dialer engine (`FOR UPDATE SKIP LOCKED` claiming, retries) |
| `internal/lexicon` | Correction engine for STT/TTS lexicon |
| `internal/csvimport` | CSV parsing for contacts and campaign targets |
| `internal/llmtest` | LLM config tester: proxies to the agent worker, falls back to a direct OpenAI-compatible call |
| `internal/ai` | AI helpers (post-call analysis, catalog) |

## Running

```sh
cp .env.example .env        # optional; real env vars always win over .env
make run                    # go run ./cmd/server
```

Dev defaults expect PostgreSQL at
`postgres://callgo:callgo@localhost:5432/callgo?sslmode=disable` and run with
mock telephony, so no SIP trunk is needed. Set `CALLGO_SIMULATOR=true` to fill
the Live Desk with fake calls.

Container image (build context is this directory):

```sh
make docker                 # docker build -t callgo-backend .
docker run --rm -p 8080:8080 --env-file .env callgo-backend
```

The image is a static `CGO_ENABLED=0` binary on `distroless/static` (includes CA
certificates and tzdata, runs as non-root). It has no shell; probe
`GET /healthz` from the orchestrator.

## Tests

```sh
make test          # go test ./...
make test-race     # with the race detector
make lint          # go vet ./...
make fmt           # gofmt -s -w .
```

Only `internal/crm` tests need PostgreSQL
(`CALLGO_TEST_DATABASE_URL`, default
`postgres://callgo:callgo@localhost:5432/callgo_test?sslmode=disable`).

## Configuration

All configuration comes from environment variables. If a `.env` file exists in
the working directory it is loaded first but never overrides real variables.
Durations use Go syntax (`30s`, `5m`); booleans accept `true/false/1/0`.
With `CALLGO_ENV=prod`, `config.Load` fails on insecure development defaults
(see "Prod requirements" below).

| Variable | Default | Description |
|---|---|---|
| `CALLGO_ENV` | `dev` | `dev` or `prod` |
| `CALLGO_HTTP_ADDR` | `:8080` | HTTP listen address |
| `CALLGO_DATABASE_URL` | `postgres://callgo:callgo@localhost:5432/callgo?sslmode=disable` | PostgreSQL DSN |
| `CALLGO_JWT_SECRET` | `dev-secret-change-me` (dev only, warns) | HS256 signing secret for user tokens |
| `CALLGO_AGENT_TOKEN` | `dev-agent-token` | Shared secret for the Python agent (`X-Agent-Token`) |
| `CALLGO_ENCRYPTION_KEY` | derived from JWT secret (dev only, warns) | Base64 of 32 bytes; encrypts LLM API keys at rest |
| `CALLGO_ADMIN_EMAIL` | `admin@callgo.mn` | Bootstrap admin user |
| `CALLGO_ADMIN_PASSWORD` | `admin1234` | Bootstrap admin password |
| `CALLGO_ALLOW_SIGNUP` | `false` | Enable `POST /api/auth/register` |
| `CALLGO_CORS_ORIGINS` | `http://localhost:5173` | Comma-separated allowed origins |
| `CALLGO_LOG_LEVEL` | `info` | `trace`, `debug`, `info`, `warn`, `error` |
| `CALLGO_LOG_PRETTY` | `true` in dev, `false` in prod | Console log format instead of JSON |
| `CALLGO_MOCK_TELEPHONY` | `true` | Use the mock `Telephony` (no trunk needed) |
| `CALLGO_SIMULATOR` | `false` | Generate fake calls and transcripts |
| `CALLGO_SIMULATOR_INTERVAL` | `4s` | Simulator tick |
| `CALLGO_SHUTDOWN_TIMEOUT` | `15s` | Graceful shutdown budget |
| `CALLGO_AGENT_WORKER_URL` | `http://localhost:8090` | Agent worker HTTP API (LLM test proxy) |
| `CALLGO_CAMPAIGN_POLL_INTERVAL` | `2s` | Dialer polling interval |
| `CALLGO_CAMPAIGN_RETRY_BACKOFF` | `5m` | Delay before retrying a failed target |
| `CALLGO_CAMPAIGN_MAX_CONCURRENCY` | `20` | Upper bound of concurrent campaign calls |
| `LIVEKIT_URL` | `ws://localhost:7880` | LiveKit server URL |
| `LIVEKIT_API_KEY` | `devkey` | LiveKit API key |
| `LIVEKIT_API_SECRET` | `secret` | LiveKit API secret |
| `LIVEKIT_AGENT_NAME` | `callgo` | Agent name used for explicit dispatch |
| `LIVEKIT_WEBHOOK_API_KEY` | `LIVEKIT_API_KEY` | Key used to verify LiveKit webhooks |
| `LIVEKIT_WEBHOOK_API_SECRET` | `LIVEKIT_API_SECRET` | Secret used to verify LiveKit webhooks |
| `SIP_ASTERISK_HOST` | `localhost` | Asterisk host used as trunk address |
| `SIP_ASTERISK_PORT` | `5060` | Asterisk SIP port |
| `SIP_ALLOWED_ADDRESSES` | empty | Comma-separated IPs/CIDRs allowed on inbound trunks |
| `SIP_TRANSPORT` | `udp` | `udp`, `tcp` or `tls` |
| `SIP_AUTH_USERNAME` / `SIP_AUTH_PASSWORD` | empty | Trunk digest credentials |
| `SIP_RING_TIMEOUT` | `30s` | Outbound ring timeout |
| `SIP_MAX_CALL_DURATION` | `20m` | Hard cap per call |

### Generating secrets

```sh
openssl rand -base64 32    # CALLGO_ENCRYPTION_KEY (exactly 32 bytes, base64)
openssl rand -base64 32    # CALLGO_JWT_SECRET
openssl rand -hex 24       # CALLGO_AGENT_TOKEN (set the same value in the agent's env)
```

The agent token is a shared secret: the Python worker sends it as
`X-Agent-Token` on every `/internal/agent/*` call. The encryption key protects
stored LLM API keys (AES-GCM); losing or rotating it makes stored keys
unreadable, so back it up with the database.

### Prod requirements

`CALLGO_ENV=prod` makes `config.Load` return an error unless:

- `CALLGO_JWT_SECRET` is set and at least 32 characters
- `CALLGO_ENCRYPTION_KEY` is set explicitly
- `CALLGO_AGENT_TOKEN` is not the dev default and at least 16 characters
- `CALLGO_ADMIN_PASSWORD` is not `admin1234`
- `LIVEKIT_API_KEY` / `LIVEKIT_API_SECRET` are not the dev defaults
- `CALLGO_CORS_ORIGINS` contains no `*`

Mock telephony or the simulator in prod only produce startup warnings.

## Middleware

`internal/middleware` exposes plain `func(http.Handler) http.Handler` values:

| Middleware | Behaviour |
|---|---|
| `RequestID` | Passes through a sane `X-Request-ID` or generates a UUID; stored in context, echoed in the response |
| `Logger(log)` | One zerolog line per request (method, path, status, bytes, duration, request_id, remote_ip); skips `/healthz`; keeps WebSocket hijack working |
| `Recoverer(log)` | Panic to `500 {"error":{"code":"internal","message":"internal error"}}` plus stack log |
| `RateLimit(rps, burst)` | Per-client-IP token bucket, `429` + `Retry-After`; idle buckets evicted by a goroutine |
| `MaxBody(n)` | Caps request bodies, `413` when exceeded |
| `SecureHeaders` | `nosniff`, frame deny, referrer policy, HSTS over TLS |
| `Timeout(d)` | Request context deadline; `503` if the handler produced nothing before the deadline |

Suggested order: `RequestID`, `Logger`, `Recoverer`, `SecureHeaders`, then
`RateLimit` / `MaxBody` / `Timeout` on the API group only (not on `/api/ws`).
Behind a reverse proxy, mount chi's `RealIP` first so the rate limiter and
access log see the real client address.
