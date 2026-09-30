#!/bin/sh
# =============================================================================
# livekit-server entrypoint: render /etc/livekit/livekit.yaml (template) into
# /tmp/livekit.yaml from environment variables, then exec livekit-server.
# The upstream image is Alpine, so only POSIX sh + busybox sed are available.
# (Тохиргооны загварыг орчны хувьсагчаар бөглөөд серверийг асаана.)
# =============================================================================
set -eu

src="${LIVEKIT_CONFIG_TEMPLATE:-/etc/livekit/livekit.yaml}"
dst="/tmp/livekit.yaml"

cp "$src" "$dst"
for var in LIVEKIT_API_KEY LIVEKIT_API_SECRET LIVEKIT_WEBHOOK_URL LIVEKIT_REDIS_ADDRESS; do
  eval "val=\${$var:-}"
  if [ -z "$val" ]; then
    echo "livekit entrypoint: required environment variable $var is empty" >&2
    exit 1
  fi
  # Escape characters that are special in a sed replacement (\ | &).
  esc=$(printf '%s' "$val" | sed -e 's/[\\|&]/\\&/g')
  sed -i "s|\${$var}|$esc|g" "$dst"
done

if [ "${#LIVEKIT_API_SECRET}" -lt 32 ]; then
  echo "livekit entrypoint: WARNING LIVEKIT_API_SECRET is shorter than 32 characters" >&2
fi

exec /livekit-server --config "$dst" "$@"
