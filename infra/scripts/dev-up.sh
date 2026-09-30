#!/usr/bin/env bash
# =============================================================================
# CallGo.mn — start the DEV profile in Docker: postgres, redis, livekit,
# backend (mock telephony), agent, frontend. No Asterisk, no livekit-sip.
# Хөгжүүлэлтийн горимоор (жинхэнэ утасгүй) бүх үйлчилгээг асаана.
#
#   infra/scripts/dev-up.sh [extra docker compose up args, e.g. --build]
# UI: http://localhost:8088  (FRONTEND_PUBLISH)   API: http://127.0.0.1:8080
# =============================================================================
set -euo pipefail
# shellcheck source=infra/scripts/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
load_env
require_cmd docker "https://docs.docker.com/engine/install/"
docker compose version >/dev/null 2>&1 || die "docker compose v2 plugin is required"

[[ -f "$ENV_FILE" ]] || { log "no .env yet — generating one"; "$INFRA_DIR/scripts/init-env.sh"; load_env; }

export CALLGO_MOCK_TELEPHONY="${CALLGO_MOCK_TELEPHONY:-true}"
ensure_models_dir

log "starting profile dev (mock telephony=$CALLGO_MOCK_TELEPHONY)"
compose --profile dev up -d --build "$@"

"$INFRA_DIR/scripts/wait-for.sh" -t 180 "http://127.0.0.1:$(port_of BACKEND_PUBLISH 127.0.0.1:8080)/healthz" ||
  die "backend did not become healthy — check: docker compose -f infra/docker-compose.yml logs backend"

port="$(port_of FRONTEND_PUBLISH 8088)"
ok "CallGo dev stack is up"
log "UI:       http://localhost:$port   (login ${CALLGO_ADMIN_EMAIL:-admin@callgo.mn})"
log "API:      http://127.0.0.1:$(port_of BACKEND_PUBLISH 127.0.0.1:8080)/healthz"
log "LiveKit:  ws://127.0.0.1:$(port_of LIVEKIT_HTTP_PUBLISH 127.0.0.1:7880)"
log "Logs:     make compose-logs    Stop: make compose-down"
