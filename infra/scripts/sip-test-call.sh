#!/usr/bin/env bash
# =============================================================================
# CallGo.mn — place a test OUTBOUND call through LiveKit SIP -> Asterisk.
# LiveKit SIP-ээр туршилтын гарах дуудлага хийх.
#
#   infra/scripts/sip-test-call.sh 9000            # Asterisk echo test (no carrier needed)
#   infra/scripts/sip-test-call.sh 9001            # 1004 Hz tone
#   infra/scripts/sip-test-call.sh +97699112233    # real PSTN call via the carrier
#
# Options:
#   --from +976...   caller ID / CallGo number (default: first of SIP_NUMBERS);
#                    its outbound trunk callgo-out-<number> must exist
#                    (lk-setup.sh, or the number added in the CRM)
#   --room NAME      LiveKit room (default call-test-<timestamp>)
#   --no-agent       do not dispatch the `callgo` AI agent into the room
#   --timeout 60s    ring timeout
# Uses `lk sip participant create` (wait_until_answered). The call bypasses
# the CRM (no Call row until the agent bootstraps); use the CRM's "Test call"
# button (POST /api/calls/dial) to exercise the full backend path.
# =============================================================================
set -euo pipefail
# shellcheck source=infra/scripts/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
load_env

usage() { awk 'NR > 2 && /^#/ { if ($0 ~ /^# =+$/) exit; sub(/^# ?/, ""); print; next } NR > 2 { exit }' "$0"; exit "${1:-0}"; }

TO="" FROM="" ROOM="" AGENT=1 TIMEOUT=60s
while [[ $# -gt 0 ]]; do
  case "$1" in
    --from) FROM="$2"; shift 2 ;;
    --room) ROOM="$2"; shift 2 ;;
    --no-agent) AGENT=0; shift ;;
    --timeout) TIMEOUT="$2"; shift 2 ;;
    -h|--help) usage 0 ;;
    -*) warn "unknown option $1"; usage 1 ;;
    *) TO="$1"; shift ;;
  esac
done
[[ -n "$TO" ]] || usage 1
[[ "$TO" =~ ^\+?[0-9*#]+$ ]] || die "invalid number '$TO'"
if [[ -z "$FROM" ]]; then
  IFS=',' read -r FROM _ <<<"${SIP_NUMBERS:-}"
  FROM="${FROM// /}"
fi
is_e164 "$FROM" || die "no valid --from number (set SIP_NUMBERS in .env or pass --from +976...)"
ROOM="${ROOM:-call-test-$(date +%s)}"
AGENT_NAME="${LIVEKIT_AGENT_NAME:-${CALLGO_AGENT_NAME:-callgo}}"

LK_WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/callgo-lk.XXXXXX")"
chmod 0755 "$LK_WORKDIR"
export LK_WORKDIR
trap 'rm -rf "$LK_WORKDIR"' EXIT

lk sip outbound list --json >"$LK_WORKDIR/out.json"
TRUNK_ID="$(json_find_id "$LK_WORKDIR/out.json" "callgo-out-$FROM")"
[[ -n "$TRUNK_ID" ]] || die "outbound trunk callgo-out-$FROM not found — run: infra/scripts/lk-setup.sh -n $FROM"

if [[ $AGENT -eq 1 ]]; then
  md="{\"direction\":\"outbound\",\"fromNumber\":\"$FROM\",\"toNumber\":\"$(json_escape "$TO")\",\"test\":true}"
  log "dispatching agent '$AGENT_NAME' into room $ROOM"
  lk dispatch create --room "$ROOM" --agent-name "$AGENT_NAME" --metadata "$md"
fi

export LK_OUTBOUND_TRUNK_ID="$TRUNK_ID" LK_CALL_TO="$TO" LK_NUMBER="$FROM" LK_ROOM="$ROOM"
export LK_CALL_TO_ID="${TO//[^0-9]/}"
req="$LK_WORKDIR/participant.json"
render_template "$INFRA_DIR/livekit/sip/sip-participant.json" "$req" \
  LK_OUTBOUND_TRUNK_ID LK_CALL_TO LK_NUMBER LK_ROOM LK_CALL_TO_ID

log "calling $TO from $FROM via trunk $TRUNK_ID (room $ROOM, waiting up to $TIMEOUT for answer)"
if lk sip participant create --timeout "$TIMEOUT" "$req"; then
  ok "answered — the call is live in room $ROOM"
  log "hang up with: lk room delete $ROOM   (or: infra/scripts/compose.sh --profile full exec asterisk asterisk -rx 'channel request hangup all')"
else
  die "call failed (see SIPStatusCode above; 486=busy, 480/408=no answer, 404=unknown number, 401/403=auth)"
fi
