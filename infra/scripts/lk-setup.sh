#!/usr/bin/env bash
# =============================================================================
# CallGo.mn — provision LiveKit SIP resources with the lk CLI.
# LiveKit дээр SIP trunk болон dispatch rule үүсгэх скрипт.
#
# For every E.164 number it creates (idempotently, found by name):
#   callgo-in-<number>    inbound trunk   numbers=[number], allowed_addresses=Asterisk IP
#   callgo-rule-<number>  dispatch rule   individual room "call_<caller>_<rand>",
#                                         agent dispatch agent_name=callgo + metadata
#   callgo-out-<number>   outbound trunk  address=Asterisk:5060, auth=SIP_AUTH_*
# from the JSON templates in infra/livekit/sip/*.json (rendered with envsubst).
#
# The names are exactly the ones the backend uses (backend/internal/livekit),
# so the backend adopts these objects when the same number is added in the
# CRM (Settings -> SIP numbers) — no duplicates, no conflicts. Use this script
# to bring the telephony plane up before the CRM is configured, for smoke
# tests, or for numbers you prefer to manage outside the CRM.
#
# Usage:
#   infra/scripts/lk-setup.sh [-n +97677001234]... [--sip-number-id UUID]
#                             [--inbound-only|--outbound-only] [--recreate]
#                             [--delete] [--dry-run] [--list]
# Numbers default to SIP_NUMBERS (comma separated) from .env.
# Requires: lk CLI on PATH, or Docker (falls back to the livekit-cli image).
# =============================================================================
set -euo pipefail
# shellcheck source=infra/scripts/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
load_env

usage() { awk 'NR > 2 && /^#/ { if ($0 ~ /^# =+$/) exit; sub(/^# ?/, ""); print; next } NR > 2 { exit }' "$0"; exit "${1:-0}"; }

NUMBERS=()
MODE=create
DO_IN=1 DO_OUT=1 RECREATE=0 DRY=0
SIP_NUMBER_ID="${SIP_NUMBER_ID:-}"
while [[ $# -gt 0 ]]; do
  case "$1" in
    -n|--number) NUMBERS+=("$2"); shift 2 ;;
    --sip-number-id) SIP_NUMBER_ID="$2"; shift 2 ;;
    --inbound-only) DO_OUT=0; shift ;;
    --outbound-only) DO_IN=0; shift ;;
    --recreate) RECREATE=1; shift ;;
    --delete) MODE=delete; shift ;;
    --list) MODE=list; shift ;;
    --dry-run) DRY=1; shift ;;
    -h|--help) usage 0 ;;
    *) warn "unknown argument: $1"; usage 1 ;;
  esac
done

TEMPLATES="$INFRA_DIR/livekit/sip"
LK_WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/callgo-lk.XXXXXX")"
chmod 0755 "$LK_WORKDIR"
export LK_WORKDIR
trap 'rm -rf "$LK_WORKDIR"' EXIT

if [[ "$MODE" == list ]]; then
  lk sip inbound list
  lk sip outbound list
  lk sip dispatch list
  exit 0
fi

if [[ ${#NUMBERS[@]} -eq 0 ]]; then
  IFS=',' read -r -a NUMBERS <<<"${SIP_NUMBERS:-}"
fi
[[ ${#NUMBERS[@]} -gt 0 && -n "${NUMBERS[0]// /}" ]] ||
  die "no numbers: pass -n +976XXXXXXXX or set SIP_NUMBERS in .env"

# ---- template variables ------------------------------------------------------
export SIP_ASTERISK_HOST="${SIP_ASTERISK_HOST:-172.28.0.10}"
export SIP_ASTERISK_PORT="${SIP_ASTERISK_PORT:-5060}"
export SIP_AUTH_USERNAME="${SIP_AUTH_USERNAME:-}"
export SIP_AUTH_PASSWORD="${SIP_AUTH_PASSWORD:-}"
export LK_AGENT_NAME="${LIVEKIT_AGENT_NAME:-${CALLGO_AGENT_NAME:-callgo}}"
# LiveKit appends "_<caller>_<random>" -> rooms "call_+976..._abcd" (same as the backend).
export LK_ROOM_PREFIX="${LK_ROOM_PREFIX:-call}"
export LK_SIP_NUMBER_ID="$SIP_NUMBER_ID"
case "${SIP_TRANSPORT:-udp}" in
  udp) LK_TRANSPORT=SIP_TRANSPORT_UDP ;;
  tcp) LK_TRANSPORT=SIP_TRANSPORT_TCP ;;
  tls) LK_TRANSPORT=SIP_TRANSPORT_TLS ;;
  auto|"") LK_TRANSPORT=SIP_TRANSPORT_AUTO ;;
  *) die "SIP_TRANSPORT must be udp, tcp, tls or auto" ;;
esac
export LK_TRANSPORT
# JSON array body for the template's ["${LK_ALLOWED_ADDRESSES}"]: a,b -> a", "b
allowed=()
IFS=',' read -r -a allowed <<<"${SIP_ALLOWED_ADDRESSES:-172.28.0.10/32}"
joined=""
for a in "${allowed[@]}"; do
  a="${a// /}"; [[ -z "$a" ]] && continue
  joined+="${joined:+\", \"}$(json_escape "$a")"
done
export LK_ALLOWED_ADDRESSES="$joined"
SIP_AUTH_USERNAME="$(json_escape "$SIP_AUTH_USERNAME")"
SIP_AUTH_PASSWORD="$(json_escape "$SIP_AUTH_PASSWORD")"

VARS=(LK_NUMBER LK_SIP_NUMBER_ID LK_ALLOWED_ADDRESSES LK_INBOUND_TRUNK_ID LK_ROOM_PREFIX
  LK_AGENT_NAME LK_TRANSPORT SIP_ASTERISK_HOST SIP_ASTERISK_PORT SIP_AUTH_USERNAME SIP_AUTH_PASSWORD)

if [[ $DO_OUT -eq 1 && ( -z "$SIP_AUTH_USERNAME" || -z "$SIP_AUTH_PASSWORD" ) ]]; then
  warn "SIP_AUTH_USERNAME/SIP_AUTH_PASSWORD empty: Asterisk challenges livekit-sip, outbound calls will get 401"
fi

# render NAME -> path of the rendered request file
render() {
  local out="$LK_WORKDIR/$1-${LK_NUMBER//+/}.json"
  render_template "$TEMPLATES/$1.json" "$out" "${VARS[@]}"
  python3 -m json.tool "$out" >/dev/null || die "rendered $1.json is not valid JSON"
  if [[ "$1" == dispatch-rule && "${CALLGO_RECORDING:-false}" == "true" ]]; then
    # Auto-start an audio-only room-composite egress for every inbound call.
    python3 - "$out" <<'PY'
import json, sys
p = sys.argv[1]
doc = json.load(open(p))
doc["dispatch_rule"]["room_config"]["egress"] = {"room": {
    "audio_only": True,
    "file_outputs": [{"file_type": "OGG", "filepath": "/out/recordings/{room_name}-{time}.ogg"}],
}}
json.dump(doc, open(p, "w"), indent=2)
PY
  fi
  printf '%s' "$out"
}

# ---- current state -------------------------------------------------------------
refresh_lists() {
  [[ $DRY -eq 1 ]] && { echo '{}' >"$LK_WORKDIR/in.json"; echo '{}' >"$LK_WORKDIR/out.json"; echo '{}' >"$LK_WORKDIR/rules.json"; return; }
  lk sip inbound list --json >"$LK_WORKDIR/in.json"
  lk sip outbound list --json >"$LK_WORKDIR/out.json"
  lk sip dispatch list --json >"$LK_WORKDIR/rules.json"
}

run_create() { # run_create KIND FILE ID_REGEX -> prints created id
  local kind=$1 file=$2 re=$3 output
  if [[ $DRY -eq 1 ]]; then
    log "dry-run: lk sip $kind create $(basename "$file")"
    cat "$file" >&2
    printf 'DRY_RUN_ID'
    return
  fi
  output="$(lk sip "$kind" create "$file")" || die "lk sip $kind create failed: $output"
  grep -oE "$re" <<<"$output" | head -n1
}

delete_trunk() { [[ $DRY -eq 1 ]] && { log "dry-run: delete trunk $1"; return; }; lk sip inbound delete "$1" >/dev/null; }
delete_rule() { [[ $DRY -eq 1 ]] && { log "dry-run: delete rule $1"; return; }; lk sip dispatch delete "$1" >/dev/null; }

refresh_lists
for LK_NUMBER in "${NUMBERS[@]}"; do
  LK_NUMBER="${LK_NUMBER// /}"
  is_e164 "$LK_NUMBER" || die "'$LK_NUMBER' is not E.164 (expected e.g. +97677001234)"
  export LK_NUMBER
  in_name="callgo-in-$LK_NUMBER" out_name="callgo-out-$LK_NUMBER" rule_name="callgo-rule-$LK_NUMBER"
  in_id="$(json_find_id "$LK_WORKDIR/in.json" "$in_name")"
  out_id="$(json_find_id "$LK_WORKDIR/out.json" "$out_name")"
  rule_id="$(json_find_id "$LK_WORKDIR/rules.json" "$rule_name")"

  if [[ "$MODE" == delete || $RECREATE -eq 1 ]]; then
    [[ -n "$rule_id" && $DO_IN -eq 1 ]] && { delete_rule "$rule_id"; ok "$LK_NUMBER: deleted dispatch rule $rule_id"; rule_id=""; }
    [[ -n "$in_id" && $DO_IN -eq 1 ]] && { delete_trunk "$in_id"; ok "$LK_NUMBER: deleted inbound trunk $in_id"; in_id=""; }
    [[ -n "$out_id" && $DO_OUT -eq 1 ]] && { delete_trunk "$out_id"; ok "$LK_NUMBER: deleted outbound trunk $out_id"; out_id=""; }
    [[ "$MODE" == delete ]] && continue
  fi

  if [[ $DO_IN -eq 1 ]]; then
    if [[ -n "$in_id" ]]; then
      log "$LK_NUMBER: inbound trunk exists ($in_id)"
    else
      req="$(render inbound-trunk)"
      in_id="$(run_create inbound "$req" 'ST_[A-Za-z0-9]+')"
      [[ -n "$in_id" ]] || die "$LK_NUMBER: could not parse inbound trunk id"
      ok "$LK_NUMBER: created inbound trunk $in_id"
    fi
    if [[ -n "$rule_id" ]]; then
      log "$LK_NUMBER: dispatch rule exists ($rule_id)"
    else
      export LK_INBOUND_TRUNK_ID="$in_id"
      req="$(render dispatch-rule)"
      rule_id="$(run_create dispatch "$req" 'SDR_[A-Za-z0-9]+')"
      [[ -n "$rule_id" ]] || die "$LK_NUMBER: could not parse dispatch rule id"
      ok "$LK_NUMBER: created dispatch rule $rule_id (agent '$LK_AGENT_NAME', rooms ${LK_ROOM_PREFIX}_<caller>_<rand>)"
    fi
  fi
  if [[ $DO_OUT -eq 1 ]]; then
    if [[ -n "$out_id" ]]; then
      log "$LK_NUMBER: outbound trunk exists ($out_id)"
    else
      req="$(render outbound-trunk)"
      out_id="$(run_create outbound "$req" 'ST_[A-Za-z0-9]+')"
      [[ -n "$out_id" ]] || die "$LK_NUMBER: could not parse outbound trunk id"
      ok "$LK_NUMBER: created outbound trunk $out_id -> $SIP_ASTERISK_HOST:$SIP_ASTERISK_PORT"
    fi
  fi
  printf '%s\tinbound=%s\tdispatch=%s\toutbound=%s\n' "$LK_NUMBER" "${in_id:--}" "${rule_id:--}" "${out_id:--}"
done
