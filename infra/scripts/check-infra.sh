#!/usr/bin/env bash
# =============================================================================
# CallGo.mn — static checks for infra/ (no running Docker daemon needed):
#   * every YAML file parses (python3 + PyYAML)
#   * lk request templates are valid JSON
#   * every shell script passes `bash -n` / `sh -n` (+ shellcheck if installed)
#   * `docker compose config` accepts every profile (if the docker CLI exists)
# Инфра тохиргооны синтакс шалгалт.
# =============================================================================
set -euo pipefail
# shellcheck source=infra/scripts/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

fail=0
check() { # check DESCRIPTION CMD...
  local d=$1; shift
  if "$@" >/dev/null 2>&1; then ok "ok    $d"; else warn "FAIL  $d"; "$@" || true; fail=1; fi
}

require_cmd python3
for f in "$INFRA_DIR"/docker-compose*.yml "$INFRA_DIR"/livekit/livekit.yaml \
  "$INFRA_DIR"/livekit-sip/config.yaml "$INFRA_DIR"/egress/egress.yaml; do
  check "yaml  ${f#"$CALLGO_ROOT"/}" python3 -c 'import sys, yaml; yaml.safe_load(open(sys.argv[1]))' "$f"
done
for f in "$INFRA_DIR"/livekit/sip/*.json; do
  check "json  ${f#"$CALLGO_ROOT"/}" python3 -m json.tool "$f"
done
for f in "$INFRA_DIR"/scripts/*.sh "$INFRA_DIR"/asterisk/render-config.sh; do
  check "bash  ${f#"$CALLGO_ROOT"/}" bash -n "$f"
done
check "sh    infra/livekit/entrypoint.sh" sh -n "$INFRA_DIR/livekit/entrypoint.sh"
if command -v shellcheck >/dev/null 2>&1; then
  check "shellcheck infra/scripts" shellcheck -x -S warning "$INFRA_DIR"/scripts/*.sh "$INFRA_DIR"/asterisk/render-config.sh
fi
if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  envf="$ENV_FILE"; [[ -f "$envf" ]] || envf="$CALLGO_ROOT/.env.example"
  for p in dev full "full recording"; do
    args=(); for x in $p; do args+=(--profile "$x"); done
    check "compose config --profile ${p// / --profile }" \
      docker compose -f "$COMPOSE_FILE" -f "$INFRA_DIR/docker-compose.gpu.yml" --env-file "$envf" "${args[@]}" config -q
  done
fi
if [[ $fail -eq 0 ]]; then ok "infra checks passed"; else die "infra checks failed"; fi
