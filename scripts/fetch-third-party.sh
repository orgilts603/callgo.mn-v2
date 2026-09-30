#!/usr/bin/env bash
# Clones the LiveKit repositories used as reference into third_party/.
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p third_party && cd third_party
for r in agents sip livekit; do
  [ -d "$r" ] || git clone --depth 1 "https://github.com/livekit/$r.git"
done
