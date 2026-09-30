#!/usr/bin/env bash
# =============================================================================
# CallGo.mn — apply the backend's SQL migrations (golang-migrate) to
# CALLGO_DATABASE_URL. The backend also runs them automatically on start
# (crm.Migrate); use this for CI, fresh databases or `down` rollbacks.
# Өгөгдлийн сангийн migration ажиллуулах.
#
#   infra/scripts/migrate.sh [up|down 1|version|force N]   (default: up)
# Uses a host `migrate` binary if installed, else the migrate/migrate image.
# =============================================================================
set -euo pipefail
# shellcheck source=infra/scripts/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
load_env

MIGRATIONS="$CALLGO_ROOT/backend/internal/crm/migrations"
MIGRATE_IMAGE="${MIGRATE_IMAGE:-migrate/migrate:v4.20.1}"
DB="${CALLGO_DATABASE_URL:-postgres://callgo:callgo@localhost:5432/callgo?sslmode=disable}"
[[ -d "$MIGRATIONS" ]] || die "no migrations directory at $MIGRATIONS"
[[ $# -gt 0 ]] || set -- up

log "migrate $* (${DB%%@*}@***)"
if command -v migrate >/dev/null 2>&1; then
  migrate -path "$MIGRATIONS" -database "$DB" "$@"
else
  require_cmd docker "install golang-migrate (https://github.com/golang-migrate/migrate) or Docker"
  docker run --rm --network host -v "$MIGRATIONS:/migrations:ro" "$MIGRATE_IMAGE" \
    -path /migrations -database "$DB" "$@"
fi
