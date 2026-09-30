#!/usr/bin/env bash
# =============================================================================
# CallGo.mn — start the FULL stack (real telephony): dev services +
# livekit-sip + Asterisk, with CALLGO_MOCK_TELEPHONY=false.
# Жинхэнэ утасны холболттой бүрэн горимоор асаана.
#
#   infra/scripts/full-up.sh [extra docker compose up args]
# Honours CALLGO_RECORDING=true (adds the egress profile) and CALLGO_GPU=true
# (adds infra/docker-compose.gpu.yml). Runs pre-flight checks first.
# Next steps after it succeeds: docs/DEPLOY.md ("Анхны тохиргоо").
# =============================================================================
set -euo pipefail
# shellcheck source=infra/scripts/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
load_env
require_cmd docker "https://docs.docker.com/engine/install/"
docker compose version >/dev/null 2>&1 || die "docker compose v2 plugin is required"
[[ -f "$ENV_FILE" ]] || die "no .env — run: make env   (or infra/scripts/init-env.sh --prod)"

# ---- pre-flight / урьдчилсан шалгалт -------------------------------------------
[[ -n "${SIP_AUTH_USERNAME:-}" && -n "${SIP_AUTH_PASSWORD:-}" ]] ||
  die "SIP_AUTH_USERNAME / SIP_AUTH_PASSWORD must be set in .env"
[[ "${LIVEKIT_API_SECRET:-}" == devsecret-* ]] && warn "LIVEKIT_API_SECRET is the development default — run make env for production"
[[ ${#LIVEKIT_API_SECRET} -ge 32 ]] || warn "LIVEKIT_API_SECRET shorter than 32 characters"
if [[ -z "${SIP_EXTERNAL_IP:-}" ]]; then
  for url in https://api.ipify.org https://ifconfig.me/ip https://icanhazip.com; do
    SIP_EXTERNAL_IP="$(curl -4 -fsS --max-time 5 "$url" 2>/dev/null | tr -d '[:space:]')" || true
    [[ "$SIP_EXTERNAL_IP" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] && break
    SIP_EXTERNAL_IP=""
  done
  [[ -n "$SIP_EXTERNAL_IP" ]] || die "SIP_EXTERNAL_IP is empty and auto-detection failed — set it in .env"
  export SIP_EXTERNAL_IP
  log "detected public IP $SIP_EXTERNAL_IP (set SIP_EXTERNAL_IP in .env to pin it)"
fi
[[ -n "${CARRIER_HOST:-}" ]] || warn "CARRIER_HOST is empty: no PSTN trunk (echo tests / test softphone only)"
if [[ "${CARRIER_REGISTER:-no}" == yes && -z "${CARRIER_USERNAME:-}" ]]; then
  die "CARRIER_REGISTER=yes needs CARRIER_USERNAME / CARRIER_PASSWORD"
fi
[[ -n "${SIP_NUMBERS:-}" ]] || warn "SIP_NUMBERS is empty (used by lk-setup.sh / sip-test-call.sh)"

export CALLGO_MOCK_TELEPHONY=false
ensure_models_dir

profiles=(--profile full)
[[ "${CALLGO_RECORDING:-false}" == "true" ]] && profiles+=(--profile recording)

log "starting ${profiles[*]} (public SIP IP $SIP_EXTERNAL_IP, mock telephony off)"
compose "${profiles[@]}" up -d --build "$@"

"$INFRA_DIR/scripts/wait-for.sh" -t 240 \
  "http://127.0.0.1:$(port_of LIVEKIT_HTTP_PUBLISH 127.0.0.1:7880)/" \
  "http://127.0.0.1:$(port_of BACKEND_PUBLISH 127.0.0.1:8080)/healthz" ||
  die "stack did not become healthy — docker compose -f infra/docker-compose.yml --profile full ps"

port="$(port_of FRONTEND_PUBLISH 8088)"
ok "CallGo full stack is up"
cat >&2 <<INFO
  UI            http://localhost:$port (behind Caddy: https://<your domain>)
  Asterisk CLI  infra/scripts/compose.sh --profile full exec asterisk asterisk -rvvv
  Next steps    1) log in and add your SIP number(s) in Settings -> SIP numbers
                   (or: infra/scripts/lk-setup.sh)
                2) echo test:  infra/scripts/sip-test-call.sh 9000
                3) real call:  infra/scripts/sip-test-call.sh +976XXXXXXXX
INFO
