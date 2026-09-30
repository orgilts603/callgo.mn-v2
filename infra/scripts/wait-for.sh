#!/usr/bin/env bash
# =============================================================================
# CallGo.mn — wait until TCP endpoints or HTTP URLs are up.
# Үйлчилгээ бэлэн болтол хүлээх.
#
#   wait-for.sh [-t SECONDS] TARGET... [-- COMMAND ARGS...]
#   TARGET is host:port (TCP connect) or http(s)://... (expects HTTP 2xx/3xx).
# Example:
#   wait-for.sh -t 120 127.0.0.1:5432 http://127.0.0.1:8080/healthz -- echo ready
# =============================================================================
set -euo pipefail

TIMEOUT=60
TARGETS=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    -t|--timeout) TIMEOUT="$2"; shift 2 ;;
    --) shift; break ;;
    -h|--help) sed -n '3,11p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) TARGETS+=("$1"); shift ;;
  esac
done
[[ ${#TARGETS[@]} -gt 0 ]] || { echo "wait-for: no targets" >&2; exit 2; }

check() {
  local t=$1
  if [[ "$t" == http://* || "$t" == https://* ]]; then
    if command -v curl >/dev/null 2>&1; then
      curl -fsS -o /dev/null --max-time 3 "$t"
    else
      wget -q -O /dev/null -T 3 "$t"
    fi
  else
    local host=${t%:*} port=${t##*:}
    [[ "$host" != "$port" && -n "$port" ]] || { echo "wait-for: bad target $t" >&2; return 1; }
    (exec 3<>"/dev/tcp/$host/$port") 2>/dev/null
  fi
}

deadline=$((SECONDS + TIMEOUT))
for t in "${TARGETS[@]}"; do
  until check "$t" >/dev/null 2>&1; do
    if ((SECONDS >= deadline)); then
      echo "wait-for: timed out after ${TIMEOUT}s waiting for $t" >&2
      exit 1
    fi
    sleep 1
  done
  echo "wait-for: $t is up" >&2
done

if [[ $# -gt 0 ]]; then
  exec "$@"
fi
