#!/usr/bin/env bash
# =============================================================================
# CallGo.mn — local development runner (`make dev`).
# Локал хөгжүүлэлт: backend, frontend, agent-ийг нэг дор ажиллуулна.
#
# Starts, with prefixed/coloured output and clean shutdown on Ctrl-C:
#   postgres   reuses one on 127.0.0.1:5432, else `docker compose up postgres`
#   livekit    reuses one on :7880, else docker (redis+livekit) or a native
#              `livekit-server --dev`; skipped when neither is available
#   backend    go run ./cmd/server                   (http://localhost:8080)
#   frontend   pnpm dev                              (http://localhost:5173)
#   agent      python -m callgo_agent.main dev       (worker, :8090)
#
# Environment knobs:
#   DEV_SERVICES="backend frontend agent"   subset to run
#   DEV_LIVEKIT=auto|docker|native|off      how to provide LiveKit (default auto)
# Telephony is mocked unless CALLGO_MOCK_TELEPHONY=false in .env.
# =============================================================================
set -euo pipefail
# shellcheck source=infra/scripts/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
load_env

DEV_SERVICES="${DEV_SERVICES:-backend frontend agent}"
DEV_LIVEKIT="${DEV_LIVEKIT:-auto}"
WAIT="$INFRA_DIR/scripts/wait-for.sh"
have_docker() { command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; }
wants() { [[ " $DEV_SERVICES " == *" $1 "* ]]; }

# Host-side addresses (the .env defaults already are, but be explicit).
export CALLGO_ENV="${CALLGO_ENV:-dev}"
export CALLGO_MOCK_TELEPHONY="${CALLGO_MOCK_TELEPHONY:-true}"
export CALLGO_BACKEND_URL="http://localhost:8080"
export CALLGO_AGENT_WORKER_URL="http://localhost:${CALLGO_HTTP_PORT:-8090}"
export LIVEKIT_URL="${LIVEKIT_URL:-ws://localhost:7880}"
export HF_HOME="${HF_HOME:-$CALLGO_ROOT/agent/models/hf}"
export CALLGO_PIPER_VOICES_DIR="${CALLGO_PIPER_VOICES_DIR:-./models/piper}"
# LiveKit in Docker must reach the backend running on the host.
export LIVEKIT_WEBHOOK_URL="http://host.docker.internal:8080/api/livekit/webhook"
mkdir -p "$HF_HOME"

# ---- process management --------------------------------------------------------
set -m # every background job gets its own process group -> clean group kill
PGIDS=() NAMES=()
colors=(36 35 33 32 34 31)

start() { # start NAME DIR CMD...
  local name=$1 dir=$2 color=${colors[${#NAMES[@]} % ${#colors[@]}]}
  shift 2
  (cd "$dir" && exec "$@") 2>&1 |
    awk -v p="$(printf '\033[%sm%-8s\033[0m' "$color" "$name")" '{ print p " | " $0; fflush() }' &
  PGIDS+=("$(jobs -p %%)")
  NAMES+=("$name")
  log "started $name (pgid ${PGIDS[-1]})"
}

cleanup() {
  trap - EXIT INT TERM
  set +m # no "Terminated" job notices while tearing down
  log "stopping ${NAMES[*]:-nothing}"
  local g
  for g in "${PGIDS[@]}"; do kill -TERM -- "-$g" 2>/dev/null || true; done
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    local alive=0
    for g in "${PGIDS[@]}"; do kill -0 -- "-$g" 2>/dev/null && alive=1; done
    [[ $alive -eq 0 ]] && break
    sleep 0.5
  done
  for g in "${PGIDS[@]}"; do kill -KILL -- "-$g" 2>/dev/null || true; done
  if [[ -n "${NATIVE_LK_PGID:-}" ]]; then kill -TERM -- "-$NATIVE_LK_PGID" 2>/dev/null || true; fi
  log "stopped (docker services, if any, keep running: make compose-down)"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

# ---- postgres ------------------------------------------------------------------------
if "$WAIT" -t 1 127.0.0.1:5432 >/dev/null 2>&1; then
  log "postgres: using the server on 127.0.0.1:5432"
elif have_docker; then
  log "postgres: starting the compose postgres container"
  compose --profile dev up -d postgres
  "$WAIT" -t 60 127.0.0.1:5432 || die "postgres did not come up"
else
  die "no PostgreSQL on 127.0.0.1:5432 and no Docker to start one"
fi

# ---- livekit -------------------------------------------------------------------------
if wants agent || [[ "$CALLGO_MOCK_TELEPHONY" != "true" ]]; then
  if [[ "$DEV_LIVEKIT" != off ]] && "$WAIT" -t 1 127.0.0.1:7880 >/dev/null 2>&1; then
    log "livekit: using the server on 127.0.0.1:7880"
  elif [[ "$DEV_LIVEKIT" =~ ^(auto|docker)$ ]] && have_docker; then
    log "livekit: starting redis + livekit containers (webhooks -> host backend)"
    compose --profile dev up -d redis livekit
    "$WAIT" -t 60 http://127.0.0.1:7880/ || die "livekit did not come up"
  elif [[ "$DEV_LIVEKIT" =~ ^(auto|native)$ ]] && command -v livekit-server >/dev/null 2>&1; then
    log "livekit: running livekit-server --dev natively (no webhooks)"
    livekit-server --dev --bind 0.0.0.0 --keys "${LIVEKIT_API_KEY:-devkey}: ${LIVEKIT_API_SECRET:-secret}" 2>&1 |
      awk '{ print "\033[90mlivekit \033[0m | " $0; fflush() }' &
    NATIVE_LK_PGID="$(jobs -p %%)"
    PGIDS+=("$NATIVE_LK_PGID"); NAMES+=(livekit)
    "$WAIT" -t 30 http://127.0.0.1:7880/ || die "livekit-server did not come up"
  else
    warn "livekit: not available (install Docker or livekit-server); the agent will keep retrying"
  fi
fi

# ---- application processes --------------------------------------------------------------
if wants backend; then
  require_cmd go "https://go.dev/dl/"
  start backend "$CALLGO_ROOT/backend" go run ./cmd/server
fi

if wants frontend; then
  require_cmd pnpm "corepack enable && corepack prepare pnpm@latest --activate"
  if [[ ! -d "$CALLGO_ROOT/frontend/node_modules" ]]; then
    log "frontend: installing dependencies (pnpm install --frozen-lockfile)"
    (cd "$CALLGO_ROOT/frontend" && pnpm install --frozen-lockfile)
  fi
  start frontend "$CALLGO_ROOT/frontend" pnpm dev
fi

if wants agent; then
  PY="$CALLGO_ROOT/agent/.venv/bin/python"
  if [[ ! -x "$PY" ]]; then
    require_cmd uv "https://docs.astral.sh/uv/"
    log "agent: creating the virtualenv (uv sync --extra dev)"
    (cd "$CALLGO_ROOT/agent" && uv sync --extra dev)
  fi
  if wants backend; then "$WAIT" -t 120 http://127.0.0.1:8080/healthz >/dev/null || warn "backend not healthy yet"; fi
  start agent "$CALLGO_ROOT/agent" "$PY" -m callgo_agent.main dev
fi

[[ ${#PGIDS[@]} -gt 0 ]] || die "nothing to run (DEV_SERVICES='$DEV_SERVICES')"
ok "dev environment running — UI http://localhost:5173 (admin ${CALLGO_ADMIN_EMAIL:-admin@callgo.mn}); Ctrl-C to stop"

# Exit (and stop everything) as soon as any process dies.
while true; do
  for i in "${!PGIDS[@]}"; do
    if ! kill -0 -- "-${PGIDS[$i]}" 2>/dev/null; then
      warn "${NAMES[$i]} exited — shutting down the rest"
      exit 1
    fi
  done
  sleep 1
done
