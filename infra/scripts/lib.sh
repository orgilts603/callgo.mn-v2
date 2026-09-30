# shellcheck shell=bash
# =============================================================================
# CallGo.mn — shared helpers for infra/scripts/*.sh (source it, don't run it).
# Туслах функцууд: .env ачаалах, docker compose, загвар бөглөх, lk CLI.
# =============================================================================

CALLGO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
INFRA_DIR="$CALLGO_ROOT/infra"
COMPOSE_FILE="$INFRA_DIR/docker-compose.yml"
ENV_FILE="${CALLGO_ENV_FILE:-$CALLGO_ROOT/.env}"
# Docker network created by infra/docker-compose.yml (used by the lk fallback).
CALLGO_DOCKER_NETWORK="${CALLGO_DOCKER_NETWORK:-callgo-net}"
LIVEKIT_CLI_IMAGE="${LIVEKIT_CLI_IMAGE:-livekit/livekit-cli:v2.18}"

if [[ -t 2 ]]; then
  _c_blue=$'\033[1;34m' _c_yellow=$'\033[1;33m' _c_red=$'\033[1;31m' _c_green=$'\033[1;32m' _c_off=$'\033[0m'
else
  _c_blue='' _c_yellow='' _c_red='' _c_green='' _c_off=''
fi
log()  { printf '%s[callgo]%s %s\n' "$_c_blue" "$_c_off" "$*" >&2; }
ok()   { printf '%s[callgo]%s %s\n' "$_c_green" "$_c_off" "$*" >&2; }
warn() { printf '%s[callgo] WARN:%s %s\n' "$_c_yellow" "$_c_off" "$*" >&2; }
die()  { printf '%s[callgo] ERROR:%s %s\n' "$_c_red" "$_c_off" "$*" >&2; exit 1; }

# load_env: export KEY=VALUE lines from $ENV_FILE. Variables already set in the
# shell win (same precedence as docker compose). Supports optional `export `,
# single/double quotes, blank lines and full-line # comments.
load_env() {
  if [[ ! -f "$ENV_FILE" ]]; then
    warn "$ENV_FILE not found — using defaults (run: make env)"
    return 0
  fi
  local line key val
  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%$'\r'}"
    [[ "$line" =~ ^[[:space:]]*(#|$) ]] && continue
    [[ "$line" =~ ^[[:space:]]*(export[[:space:]]+)?([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]] || continue
    key="${BASH_REMATCH[2]}"
    val="${BASH_REMATCH[3]}"
    if [[ "$val" =~ ^\"(.*)\"$ || "$val" =~ ^\'(.*)\'$ ]]; then
      val="${BASH_REMATCH[1]}"
    fi
    [[ -n "${!key+x}" ]] && continue
    export "$key=$val"
  done <"$ENV_FILE"
}

# require_cmd CMD [HINT]
require_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "'$1' is required${2:+ — $2}"
}

# compose ARGS...: docker compose with the repo's compose file and root .env.
compose() {
  local args=(-f "$COMPOSE_FILE")
  [[ -f "$ENV_FILE" ]] && args+=(--env-file "$ENV_FILE")
  if [[ "${CALLGO_GPU:-false}" == "true" ]]; then
    args+=(-f "$INFRA_DIR/docker-compose.gpu.yml")
  fi
  docker compose "${args[@]}" "$@"
}

# render_template SRC DST VAR...: replace ${VAR} for the listed variables only
# (envsubst when installed, python3 otherwise). Unset variables become "".
render_template() {
  local src=$1 dst=$2
  shift 2
  if command -v envsubst >/dev/null 2>&1; then
    local list="" v
    for v in "$@"; do list+="\${$v} "; done
    envsubst "$list" <"$src" >"$dst"
  else
    require_cmd python3 "install python3 or gettext-base (envsubst)"
    python3 - "$src" "$dst" "$@" <<'PY'
import os, sys
src, dst, names = sys.argv[1], sys.argv[2], sys.argv[3:]
text = open(src, encoding="utf-8").read()
for name in names:
    text = text.replace("${" + name + "}", os.environ.get(name, ""))
open(dst, "w", encoding="utf-8").write(text)
PY
  fi
}

# json_escape STRING: escape a value for embedding inside a JSON string.
json_escape() {
  local s=$1
  s=${s//\\/\\\\}
  s=${s//\"/\\\"}
  printf '%s' "$s"
}

# lk ARGS...: run the LiveKit CLI. Uses a host `lk` binary when installed
# (talking to LIVEKIT_URL, default http://127.0.0.1:7880); otherwise runs the
# livekit/livekit-cli image on the compose network against http://livekit:7880.
# Request files must live under $LK_WORKDIR (mounted into the container).
lk() {
  : "${LIVEKIT_API_KEY:?LIVEKIT_API_KEY is not set}"
  : "${LIVEKIT_API_SECRET:?LIVEKIT_API_SECRET is not set}"
  if type -P lk >/dev/null 2>&1 && [[ "${LK_USE_DOCKER:-false}" != "true" ]]; then
    LIVEKIT_URL="${LK_URL:-http://127.0.0.1:$(port_of LIVEKIT_HTTP_PUBLISH 127.0.0.1:7880)}" command lk "$@"
  else
    require_cmd docker "install the lk CLI (https://github.com/livekit/livekit-cli) or Docker"
    local mount=()
    [[ -n "${LK_WORKDIR:-}" ]] && mount=(-v "$LK_WORKDIR:$LK_WORKDIR:ro" -w "$LK_WORKDIR")
    docker run --rm -i --network "$CALLGO_DOCKER_NETWORK" \
      -e LIVEKIT_URL="${LK_DOCKER_URL:-http://livekit:7880}" \
      -e LIVEKIT_API_KEY -e LIVEKIT_API_SECRET \
      "${mount[@]}" "$LIVEKIT_CLI_IMAGE" "$@"
  fi
}

# json_find_id FILE NAME: print the id of the SIP trunk / dispatch rule called
# NAME in an `lk sip ... list --json` document (empty when absent).
json_find_id() {
  python3 - "$1" "$2" <<'PY'
import json, sys
path, name = sys.argv[1], sys.argv[2]
try:
    doc = json.load(open(path, encoding="utf-8"))
except (OSError, ValueError):
    sys.exit(0)
for item in doc.get("items", []) or []:
    if item.get("name") == name:
        print(item.get("sipTrunkId") or item.get("sip_trunk_id")
              or item.get("sipDispatchRuleId") or item.get("sip_dispatch_rule_id") or "")
        break
PY
}

# port_of VAR DEFAULT: port part of a "[host:]port" publish binding.
port_of() { local v="${!1:-$2}"; printf '%s' "${v##*:}"; }

# is_e164 NUMBER
is_e164() { [[ "$1" =~ ^\+[1-9][0-9]{6,14}$ ]]; }

# ensure_models_dir: agent/models must be writable by the agent container
# user (uid 10001) so missing models can still be downloaded at runtime.
ensure_models_dir() {
  local dir="${CALLGO_MODELS_DIR:-$CALLGO_ROOT/agent/models}"
  [[ "$dir" = /* ]] || dir="$INFRA_DIR/$dir"
  mkdir -p "$dir/hf" "$dir/piper"
  chmod -R a+rwX "$dir" 2>/dev/null || warn "could not chmod $dir (agent may fail to cache models)"
}
