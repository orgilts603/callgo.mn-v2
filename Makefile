# =============================================================================
# CallGo.mn v2 — developer & operator entry points
# Хөгжүүлэгч ба администраторын үндсэн командууд.   `make help`
# =============================================================================
SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help
MAKEFLAGS += --no-print-directory

ROOT      := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
SCRIPTS   := $(ROOT)/infra/scripts
COMPOSE   := $(SCRIPTS)/compose.sh
# docker compose profile(s) for compose-* targets: dev | full | "full recording"
PROFILE   ?= dev
PROFILES  := $(foreach p,$(PROFILE),--profile $(p))
SERVICE   ?=

AGENT_PY  := $(ROOT)/agent/.venv/bin/python
# Test database (docs/AGENT_RULES.md); created by infra/postgres/init in compose.
CALLGO_TEST_DATABASE_URL ?= postgres://callgo:callgo@localhost:5432/callgo_test?sslmode=disable
export CALLGO_TEST_DATABASE_URL

.PHONY: help env dev dev-up full-up \
        compose-up compose-down compose-logs compose-ps compose-build compose-config \
        backend-test agent-test frontend-test test lint infra-check ci e2e \
        migrate models third-party lk-setup sip-test-call

help: ## Show this help
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ---- setup ----------------------------------------------------------------------
env: ## Create .env from .env.example with random secrets (ENV_ARGS=--prod|--force)
	$(SCRIPTS)/init-env.sh $(ENV_ARGS)

models: ## Download faster-whisper + Piper models into agent/models
	$(SCRIPTS)/download-models.sh

third-party: ## Clone livekit reference repos into third_party/
	$(ROOT)/scripts/fetch-third-party.sh

# ---- local development ------------------------------------------------------------
dev: ## Run postgres(+livekit) + backend (go run) + frontend (pnpm dev) + agent (dev) locally
	$(SCRIPTS)/dev.sh

# ---- docker compose ------------------------------------------------------------------
dev-up: ## docker compose profile dev (mock telephony, no Asterisk)
	$(SCRIPTS)/dev-up.sh

full-up: ## docker compose profile full (Asterisk + livekit-sip, real calls)
	$(SCRIPTS)/full-up.sh

compose-up: ## docker compose up -d --build for PROFILE (default dev)
	$(COMPOSE) $(PROFILES) up -d --build $(SERVICE)

compose-down: ## Stop and remove all CallGo containers (volumes are kept)
	$(COMPOSE) --profile dev --profile full --profile recording down --remove-orphans

compose-logs: ## Follow logs (SERVICE=backend to filter)
	$(COMPOSE) --profile dev --profile full --profile recording logs -f --tail=200 $(SERVICE)

compose-ps: ## Show container status
	$(COMPOSE) --profile dev --profile full --profile recording ps

compose-build: ## Build the backend / agent / frontend images
	$(COMPOSE) --profile dev build $(SERVICE)

compose-config: ## Render the effective compose config for PROFILE
	$(COMPOSE) $(PROFILES) config

# ---- database ----------------------------------------------------------------------------
migrate: ## Apply backend SQL migrations to CALLGO_DATABASE_URL (the backend also migrates on start)
	$(SCRIPTS)/migrate.sh

# ---- tests & lint --------------------------------------------------------------------------
backend-test: ## go test ./... (uses CALLGO_TEST_DATABASE_URL)
	cd $(ROOT)/backend && go test ./...

agent-test: ## pytest for the Python agent
	cd $(ROOT)/agent && if [ -x .venv/bin/pytest ]; then .venv/bin/pytest -q; else uv run --extra dev pytest -q; fi

frontend-test: ## vitest run for the frontend
	cd $(ROOT)/frontend && pnpm exec vitest run

test: backend-test agent-test frontend-test ## Run every test suite

ci: ## What CI runs: go vet + go test -race, frontend tsc + vitest, agent ruff + pytest
	cd $(ROOT)/backend && go vet ./... && go test -race -count=1 ./...
	cd $(ROOT)/frontend && pnpm exec tsc --noEmit -p tsconfig.app.json && pnpm exec vitest run
	cd $(ROOT)/agent && if [ -x .venv/bin/pytest ]; then .venv/bin/ruff check callgo_agent tests && .venv/bin/pytest -q; else uv run --extra dev ruff check callgo_agent tests && uv run --extra dev pytest -q; fi

# E2E_BASE_URL is where the dashboard is served (default: `make dev` on :5173); the
# backend behind it must run with CALLGO_SIMULATOR=true. See docs/CI.md.
E2E_BASE_URL ?= http://127.0.0.1:5173
e2e: ## Playwright smoke tests against a running stack (E2E_BASE_URL, CALLGO_E2E_CHROME optional)
	cd $(ROOT)/frontend/e2e && if [ ! -d node_modules ]; then npm ci; fi
	cd $(ROOT)/frontend/e2e && E2E_BASE_URL=$(E2E_BASE_URL) npx playwright test -c ../playwright.config.ts $(ARGS)

lint: infra-check ## go vet + gofmt, ruff, oxlint + tsc, infra config checks
	cd $(ROOT)/backend && go vet ./... && test -z "$$(gofmt -l . | tee /dev/stderr)"
	cd $(ROOT)/agent && if [ -x .venv/bin/ruff ]; then .venv/bin/ruff check callgo_agent tests; else uv run --extra dev ruff check callgo_agent tests; fi
	cd $(ROOT)/frontend && pnpm lint && pnpm exec tsc --noEmit -p tsconfig.app.json

infra-check: ## Validate infra YAML/JSON/shell syntax (+ compose config when docker is installed)
	$(SCRIPTS)/check-infra.sh

# ---- telephony helpers ------------------------------------------------------------------------
lk-setup: ## Create LiveKit SIP trunks + dispatch rules for SIP_NUMBERS (ARGS=--dry-run|--delete|...)
	$(SCRIPTS)/lk-setup.sh $(ARGS)

sip-test-call: ## Outbound test call: make sip-test-call TO=9000 (echo) or TO=+976...
	$(SCRIPTS)/sip-test-call.sh $(TO) $(ARGS)
