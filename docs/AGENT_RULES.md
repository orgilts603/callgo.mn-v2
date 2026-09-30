# Rules for parallel development agents

You are one of ~20 agents working **concurrently in the same git working tree**.
Follow these rules strictly or you will break someone else's build.

1. **Own only your directories.** Your task lists the paths you may create or
   edit. Never edit files outside them. If a shared file needs a change, write
   what you need in your final report; the integrator applies it.
2. **Do not touch dependency manifests**: `backend/go.mod`, `backend/go.sum`,
   `frontend/package.json`, `frontend/pnpm-lock.yaml`, `agent/pyproject.toml`,
   `agent/uv.lock`. Every dependency you are likely to need is already
   installed (see the task). If you truly need another, say so in the report
   and write the code so it compiles without it (or vendor a tiny helper).
3. **Never run** `go get`, `go mod tidy`, `pnpm add`, `npm install`, `uv add`,
   `pip install`, `git commit`, `git push`, `git checkout`, `git stash`,
   `git reset`, `git clean`. The integrator commits.
4. **Build and test only your packages.** Go: `go build ./internal/<yours>/... && go test ./internal/<yours>/...`. Other packages may be mid-edit; never "fix" them. Frontend: `pnpm exec tsc --noEmit -p tsconfig.app.json` may show errors in other features; look only at yours. Python: `.venv/bin/pytest tests/<yours>` and `.venv/bin/ruff check callgo_agent/<yours>`.
5. **Contracts are frozen**: `backend/internal/domain/domain.go`, `docs/API.md`,
   `docs/EVENTS.md`, `frontend/src/lib/types.ts`, `agent/callgo_agent/schemas.py`.
   Implement against them. Do not change them; report mismatches.
6. **Production quality.** Idiomatic Go (errors wrapped with `%w`, contexts
   propagated, no globals), typed React (no `any`), typed Python (ruff clean).
   Tests for real logic. No TODO stubs where the task asks for a working piece.
7. **Database for tests**: PostgreSQL is running locally.
   DSN: `postgres://callgo:callgo@localhost:5432/callgo_test?sslmode=disable`
   (env `CALLGO_TEST_DATABASE_URL`). Only `internal/crm` may create/drop
   schema in it; other Go packages must not need a DB.
8. **Reference code**: `third_party/agents` (livekit-agents 1.8.3 source +
   examples), `third_party/sip`, `third_party/livekit`. Read them instead of
   guessing APIs. Installed Go module sources are under `/root/go/pkg/mod`.
9. **Finish with a report**: what you built, how to run its tests, test output
   (pass/fail counts), anything the integrator must wire (constructor
   signatures, env vars, routes to mount), and any contract mismatch.
10. Keep going until your scope is complete and its tests pass. Do not ask
    questions; make the sensible decision and document it.
