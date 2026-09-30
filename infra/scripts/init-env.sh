#!/usr/bin/env bash
# =============================================================================
# CallGo.mn — create .env from .env.example with freshly generated secrets.
# .env файлыг санамсаргүй нууц түлхүүрүүдтэй үүсгэнэ.
#
#   infra/scripts/init-env.sh           # dev: random secrets, CALLGO_ENV=dev
#   infra/scripts/init-env.sh --prod    # prod: CALLGO_ENV=prod, mock telephony off
#   infra/scripts/init-env.sh --force   # overwrite an existing .env (backup kept)
# =============================================================================
set -euo pipefail
# shellcheck source=infra/scripts/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

PROD=0 FORCE=0
for a in "$@"; do
  case "$a" in
    --prod) PROD=1 ;;
    --force) FORCE=1 ;;
    -h|--help) sed -n '3,9p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown argument $a" ;;
  esac
done

src="$CALLGO_ROOT/.env.example"
[[ -f "$src" ]] || die "$src missing"
if [[ -f "$ENV_FILE" && $FORCE -eq 0 ]]; then
  die "$ENV_FILE already exists (use --force to regenerate; a backup is kept)"
fi
[[ -f "$ENV_FILE" ]] && cp "$ENV_FILE" "$ENV_FILE.bak.$(date +%Y%m%d%H%M%S)"

rand_hex() { # rand_hex BYTES
  if command -v openssl >/dev/null 2>&1; then openssl rand -hex "$1"
  else python3 -c "import secrets,sys; print(secrets.token_hex(int(sys.argv[1])))" "$1"; fi
}
rand_b64() {
  if command -v openssl >/dev/null 2>&1; then openssl rand -base64 "$1"
  else python3 -c "import base64,secrets,sys; print(base64.b64encode(secrets.token_bytes(int(sys.argv[1]))).decode())" "$1"; fi
}

declare -A SET=(
  [CALLGO_JWT_SECRET]="$(rand_hex 32)"
  [CALLGO_AGENT_TOKEN]="$(rand_hex 24)"
  [CALLGO_ENCRYPTION_KEY]="$(rand_b64 32)"
  [CALLGO_ADMIN_PASSWORD]="$(rand_hex 8)"
  [LIVEKIT_API_KEY]="API$(rand_hex 6)"
  [LIVEKIT_API_SECRET]="$(rand_hex 32)"
  [SIP_AUTH_USERNAME]="livekit"
  [SIP_AUTH_PASSWORD]="$(rand_hex 16)"
  [POSTGRES_PASSWORD]="$(rand_hex 16)"
)
if [[ $PROD -eq 1 ]]; then
  SET[CALLGO_ENV]=prod
  SET[CALLGO_LOG_PRETTY]=false
  SET[CALLGO_MOCK_TELEPHONY]=false
  SET[FRONTEND_PUBLISH]=127.0.0.1:8088
fi
# The host-side DSN must carry the generated postgres password too.
SET[CALLGO_DATABASE_URL]="postgres://callgo:${SET[POSTGRES_PASSWORD]}@localhost:5432/callgo?sslmode=disable"

tmp="$(mktemp)"
while IFS= read -r line || [[ -n "$line" ]]; do
  if [[ "$line" =~ ^([A-Z_][A-Z0-9_]*)= ]] && [[ -n "${SET[${BASH_REMATCH[1]}]+x}" ]]; then
    key="${BASH_REMATCH[1]}"
    printf '%s=%s\n' "$key" "${SET[$key]}"
  else
    printf '%s\n' "$line"
  fi
done <"$src" >"$tmp"
install -m 0600 "$tmp" "$ENV_FILE"
rm -f "$tmp"

ok "wrote $ENV_FILE (mode 600)"
log "admin login: $(grep -E '^CALLGO_ADMIN_EMAIL=' "$ENV_FILE" | cut -d= -f2) / ${SET[CALLGO_ADMIN_PASSWORD]}"
log "next: edit carrier settings (CARRIER_*, SIP_NUMBERS) if you use real telephony"
