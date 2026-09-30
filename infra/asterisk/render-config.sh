#!/bin/bash
# =============================================================================
# CallGo.mn — Asterisk container entrypoint.
# Asterisk контейнерийн эхлэл: тохиргоог .env-ийн утгаар бөглөнө.
#
# 1. Copies every *.conf from /etc/asterisk-src (this directory, mounted
#    read-only) into /etc/asterisk, substituting ONLY the allow-listed
#    variables below with envsubst (dialplan ${EXTEN} etc. stay intact).
# 2. Renders the optional *.conf.in pieces when their feature is enabled,
#    otherwise writes an empty file so `#tryinclude` stays quiet.
# 3. Chains to the image's own entrypoint (PUID/PGID handling, -U asterisk).
#
# Image: andrius/asterisk (Debian), which ships envsubst (gettext-base).
# =============================================================================
set -euo pipefail

SRC=/etc/asterisk-src
DST=/etc/asterisk

log() { echo "[callgo-asterisk] $*" >&2; }
die() { log "ERROR: $*"; exit 1; }

# ---- defaults & validation ---------------------------------------------------
: "${SIP_LOCAL_NET:=172.28.0.0/24}"
: "${LIVEKIT_SIP_HOST:=172.28.0.11}"
: "${LIVEKIT_SIP_PORT:=5060}"
: "${CARRIER_PORT:=5060}"
: "${CARRIER_REGISTER:=no}"
: "${CARRIER_DID_SOURCE:=ruri}"
: "${CARRIER_NUMBER_FORMAT:=national}"
: "${CARRIER_DEFAULT_DID:=}"
: "${CARRIER_HOST:=}"
: "${CARRIER_USERNAME:=}"
: "${CARRIER_PASSWORD:=}"
: "${CARRIER_FROM_USER:=${CARRIER_USERNAME}}"
: "${CARRIER_FROM_DOMAIN:=${CARRIER_HOST}}"
: "${CARRIER_MATCH:=${CARRIER_HOST}}"
: "${ASTERISK_RECORD_CALLS:=no}"
: "${TEST_PHONE_PASSWORD:=}"
: "${SIP_EXTERNAL_IP:=}"
export SIP_LOCAL_NET LIVEKIT_SIP_HOST LIVEKIT_SIP_PORT CARRIER_PORT CARRIER_REGISTER \
  CARRIER_DID_SOURCE CARRIER_NUMBER_FORMAT CARRIER_DEFAULT_DID CARRIER_HOST CARRIER_USERNAME \
  CARRIER_PASSWORD CARRIER_FROM_USER CARRIER_FROM_DOMAIN CARRIER_MATCH ASTERISK_RECORD_CALLS \
  TEST_PHONE_PASSWORD SIP_EXTERNAL_IP

[[ -n "${SIP_AUTH_USERNAME:-}" && -n "${SIP_AUTH_PASSWORD:-}" ]] ||
  die "SIP_AUTH_USERNAME and SIP_AUTH_PASSWORD must be set (shared secret with livekit-sip)"
if [[ -z "$SIP_EXTERNAL_IP" ]]; then
  log "WARNING: SIP_EXTERNAL_IP is empty — carrier-facing SIP/SDP will carry the container IP."
  log "         Set SIP_EXTERNAL_IP to the VPS public IPv4 in .env (full-up.sh auto-detects it)."
fi
case "$CARRIER_NUMBER_FORMAT" in e164|intl|national) ;; *) die "CARRIER_NUMBER_FORMAT must be e164, intl or national" ;; esac
case "$CARRIER_DID_SOURCE" in ruri|to) ;; *) die "CARRIER_DID_SOURCE must be ruri or to" ;; esac

# Allow-list for envsubst: only these ${VARS} are replaced.
VARS='${SIP_EXTERNAL_IP} ${SIP_LOCAL_NET} ${LIVEKIT_SIP_HOST} ${LIVEKIT_SIP_PORT}
${SIP_AUTH_USERNAME} ${SIP_AUTH_PASSWORD}
${CARRIER_HOST} ${CARRIER_PORT} ${CARRIER_USERNAME} ${CARRIER_PASSWORD}
${CARRIER_FROM_USER} ${CARRIER_FROM_DOMAIN} ${CARRIER_MATCH}
${CARRIER_DEFAULT_DID} ${CARRIER_DID_SOURCE} ${CARRIER_NUMBER_FORMAT}
${ASTERISK_RECORD_CALLS} ${TEST_PHONE_PASSWORD}'

render() { # render SRC DST
  envsubst "$VARS" <"$1" >"$2"
  chmod 0640 "$2"
}

mkdir -p "$DST"
for f in "$SRC"/*.conf; do
  render "$f" "$DST/$(basename "$f")"
  log "rendered $(basename "$f")"
done

# ---- optional pieces -----------------------------------------------------------
optional() { # optional NAME ENABLED
  local name=$1 enabled=$2
  if [[ "$enabled" == yes ]]; then
    render "$SRC/$name.in" "$DST/$name"
    log "enabled $name"
  else
    : >"$DST/$name"
  fi
}

carrier=no; [[ -n "$CARRIER_HOST" ]] && carrier=yes
register=no; [[ "$carrier" == yes && "$CARRIER_REGISTER" == yes ]] && register=yes
testphone=no; [[ -n "$TEST_PHONE_PASSWORD" ]] && testphone=yes

optional pjsip_carrier.conf "$carrier"
optional pjsip_carrier_registration.conf "$register"
optional pjsip_testphone.conf "$testphone"

if [[ "$carrier" == no ]]; then
  log "WARNING: CARRIER_HOST is empty — no PSTN trunk; only livekit-sip (and the test phone) are configured."
elif [[ -z "$CARRIER_USERNAME" ]]; then
  # IP-authenticated trunk: drop the auth object and its reference.
  sed -i -e '/^\[carrier-auth\]$/,/^$/d' -e '/^outbound_auth=carrier-auth$/d' "$DST/pjsip_carrier.conf"
  log "carrier trunk without credentials (IP authentication)"
fi
if [[ "$testphone" == yes && ${#TEST_PHONE_PASSWORD} -lt 12 ]]; then
  die "TEST_PHONE_PASSWORD must be at least 12 characters"
fi

mkdir -p /var/spool/asterisk/monitor

# Hand over to the image's entrypoint (fixes ownership, drops to user asterisk).
exec /usr/local/bin/entrypoint.sh "$@"
