# CI and end-to-end tests

GitHub Actions workflows live in `.github/workflows/`. Every workflow is
path-filtered (its own tree, its workflow file and `docs/**`), cancels older
runs of the same branch or PR (`concurrency` + `cancel-in-progress`) and needs
**no secrets**: tests use mock telephony, fake embeddings and a throwaway
PostgreSQL service container.

| Workflow | Runs when | What it does |
|---|---|---|
| `backend.yml` | `backend/**` | Go 1.26 (module cache). Postgres `pgvector/pgvector:pg16` service (`callgo`/`callgo`), a step creates `callgo_test` + the `vector` extension. `gofmt`, `go vet ./...`, `go test -race ./...` with `CALLGO_TEST_DATABASE_URL`. |
| `frontend.yml` | `frontend/**` (not `frontend/e2e/**`) | pnpm 10 + Node 22 (pnpm store cache), `pnpm install --frozen-lockfile`, `pnpm lint`, `tsc --noEmit -p tsconfig.app.json`, `vitest run`, `pnpm build`. |
| `agent.yml` | `agent/**` | uv (cached) + Python 3.11, `uv pip install -e ".[dev]"`, `ruff check callgo_agent tests`, `pytest`. |
| `infra.yml` | `infra/**`, `Makefile`, `.env.example` | `make infra-check` (YAML/JSON/shell syntax, shellcheck, `docker compose config` for every profile; the Docker Compose plugin is preinstalled on `ubuntu-latest`). |
| `e2e.yml` | `backend/**`, `frontend/**` | Builds the backend, starts it against the Postgres service with `CALLGO_MOCK_TELEPHONY=true CALLGO_SIMULATOR=true CALLGO_EMBED_FAKE=true`, builds the frontend and serves it with `vite preview` on `:5173` (its `server.proxy` sends `/api` and the WebSocket to `:8080`), installs Chromium with `npx playwright install --with-deps chromium` and runs the smoke tests. The HTML report and traces are uploaded as the `playwright-report` artifact; service logs as `e2e-service-logs` on failure. |

Run the same checks locally with `make ci` (vet + race tests, tsc + vitest,
ruff + pytest) and `make infra-check`.

## Playwright smoke tests

The tests are in `frontend/e2e/` and are an **isolated npm project**
(`package.json` + `package-lock.json`); the frontend's pnpm manifest is not
touched. `frontend/playwright.config.ts` is the configuration (base URL from
`E2E_BASE_URL`, 1 retry, trace on first retry, screenshot on failure, one
worker because the specs share one backend). Spec files are named `*.e2e.ts`
so Vitest (`**/*.spec.ts`) never picks them up.

Covered flows (`smoke.e2e.ts`): login (wrong password, then success) and the
redirect for anonymous visitors, dashboard stat cards, Live Desk showing
simulator calls within 20 s and opening/closing the call drawer, creating a
campaign through the wizard from a generated CSV and finding it in the list,
and creating a knowledge base under Settings.

### Run locally

```bash
# 1. PostgreSQL 16 + pgvector with the callgo/callgo user, then the backend
#    (mock telephony + simulator; seeds admin@callgo.mn / admin1234):
cd backend
CALLGO_SIMULATOR=true CALLGO_SIMULATOR_INTERVAL=2s CALLGO_EMBED_FAKE=true go run ./cmd/server   # :8080

# 2. The dashboard. `pnpm dev` works; `vite preview` of a build is faster and
#    steadier on a loaded machine (it reuses the /api proxy to :8080):
cd frontend && pnpm dev                                              # :5173
#   or: pnpm exec vite build && pnpm exec vite preview --port 5173

# 3. Tests (first run installs the npm deps):
make e2e                                   # E2E_BASE_URL defaults to http://127.0.0.1:5173
make e2e E2E_BASE_URL=http://127.0.0.1:5174 ARGS="-g campaign"
```

Manually: `cd frontend/e2e && npm ci && npx playwright install --with-deps
chromium && npm test`.

Environment variables read by the tests:

| Variable | Default | Meaning |
|---|---|---|
| `E2E_BASE_URL` | `http://127.0.0.1:5173` | Where the dashboard is served. |
| `E2E_EMAIL` / `E2E_PASSWORD` | `admin@callgo.mn` / `admin1234` | Seeded admin (`CALLGO_ADMIN_EMAIL` / `CALLGO_ADMIN_PASSWORD`). |
| `CALLGO_E2E_CHROME` | unset | Path of a Chromium binary to launch instead of Playwright's download, e.g. `/opt/pw-browsers/chromium-1194/chrome-linux/chrome` together with `PLAYWRIGHT_BROWSERS_PATH=/opt/pw-browsers`. |

The specs create data (campaigns, knowledge bases) with a per-run suffix, so
they can be re-run against a persistent database; use a scratch database
(`CALLGO_DATABASE_URL=.../callgo_e2e`) to keep your dev data clean.

### Adding a test

Put new flows in `frontend/e2e/*.e2e.ts`, sign in with `login(page)` from
`helpers.ts`, prefer roles, labels and `data-testid` over CSS, and keep them
independent of each other. Live-desk rows re-sort every few seconds, so
dispatch clicks on them (`locator.dispatchEvent('click')`) rather than
hit-testing a moving target.

## Platform admin API

`backend/internal/httpapi/admin.go` implements the "Platform admin" section of
`docs/API.md` (`mountAdmin`, `AdminDeps`); it has no CI-specific wiring.
