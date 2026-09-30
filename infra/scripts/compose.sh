#!/usr/bin/env bash
# =============================================================================
# CallGo.mn — `docker compose` wrapper: always uses infra/docker-compose.yml
# with the repo-root .env for ${VARIABLE} interpolation (and the GPU override
# when CALLGO_GPU=true). Used by the Makefile compose-* targets.
#   infra/scripts/compose.sh --profile full ps
# =============================================================================
set -euo pipefail
# shellcheck source=infra/scripts/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
load_env
compose "$@"
