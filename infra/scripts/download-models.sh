#!/usr/bin/env bash
# =============================================================================
# CallGo.mn — pre-download the agent's local models into agent/models
# (bind-mounted at /models in the agent container).
# Агентын загваруудыг (Whisper, Piper) урьдчилан татах.
#
#   agent/models/hf/     Hugging Face cache (HF_HOME): faster-whisper model
#                        CALLGO_WHISPER_MODEL (+ livekit turn-detector files)
#   agent/models/piper/  Piper voices: CALLGO_PIPER_DEFAULT_VOICE and
#                        PIPER_EXTRA_VOICES from rhasspy/piper-voices
#
# There is no public Mongolian (mn_MN) Piper voice: the script reports it as
# missing and you copy your own <voice>.onnx + <voice>.onnx.json there.
# Uses the agent's virtualenv (agent/.venv) when present, else python3 with
# faster-whisper installed (e.g. `uv sync` in agent/ first).
# =============================================================================
set -euo pipefail
# shellcheck source=infra/scripts/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
load_env

MODELS_DIR="${CALLGO_MODELS_DIR:-../agent/models}"
[[ "$MODELS_DIR" = /* ]] || MODELS_DIR="$(cd "$INFRA_DIR" && mkdir -p "$MODELS_DIR" && cd "$MODELS_DIR" && pwd)"
export HF_HOME="$MODELS_DIR/hf"
PIPER_DIR="$MODELS_DIR/piper"
mkdir -p "$HF_HOME" "$PIPER_DIR"

PY="$CALLGO_ROOT/agent/.venv/bin/python"
[[ -x "$PY" ]] || PY="$(command -v python3 || true)"
[[ -n "$PY" ]] || die "python3 not found"

WHISPER="${CALLGO_WHISPER_MODEL:-large-v3}"
log "faster-whisper '$WHISPER' -> $HF_HOME"
if ! "$PY" - "$WHISPER" <<'PY'
import os, sys
name = sys.argv[1]
if os.path.isdir(name):
    print(f"  {name} is a local directory, nothing to download")
    sys.exit(0)
try:
    from faster_whisper.utils import download_model
except ImportError:
    sys.exit(3)
path = download_model(name)
print(f"  ready: {path}")
PY
then
  warn "faster-whisper is not importable with $PY — run 'uv sync' (or pip install faster-whisper) in agent/ and retry"
fi

# livekit-agents plugin files (silero VAD, turn-detector) into the same HF cache.
if [[ -x "$CALLGO_ROOT/agent/.venv/bin/python" ]]; then
  log "livekit-agents plugin files (download-files)"
  (cd "$CALLGO_ROOT/agent" && "$PY" -m callgo_agent.main download-files) ||
    warn "download-files failed (the agent downloads them on first start instead)"
fi

# Piper voices: rhasspy/piper-voices layout <lang>/<lang_REGION>/<speaker>/<quality>/<voice>.onnx
BASE="https://huggingface.co/rhasspy/piper-voices/resolve/main"
voices=("${CALLGO_PIPER_DEFAULT_VOICE:-mn_MN-default-medium}")
read -r -a extra <<<"${PIPER_EXTRA_VOICES:-}"
voices+=("${extra[@]}")
for v in "${voices[@]}"; do
  [[ -z "$v" ]] && continue
  if [[ -s "$PIPER_DIR/$v.onnx" && -s "$PIPER_DIR/$v.onnx.json" ]]; then
    log "piper voice $v already present"
    continue
  fi
  if [[ ! "$v" =~ ^([a-z]{2,3})_([A-Z]{2})-(.+)-(x_low|low|medium|high)$ ]]; then
    warn "piper voice '$v' does not follow <lang>_<REGION>-<speaker>-<quality>; copy it into $PIPER_DIR manually"
    continue
  fi
  lang="${BASH_REMATCH[1]}" region="${BASH_REMATCH[1]}_${BASH_REMATCH[2]}" speaker="${BASH_REMATCH[3]}" quality="${BASH_REMATCH[4]}"
  url="$BASE/$lang/$region/$speaker/$quality/$v"
  log "piper voice $v"
  if curl -fsSL --retry 3 -o "$PIPER_DIR/$v.onnx.part" "$url.onnx?download=true" &&
     curl -fsSL --retry 3 -o "$PIPER_DIR/$v.onnx.json" "$url.onnx.json?download=true"; then
    mv "$PIPER_DIR/$v.onnx.part" "$PIPER_DIR/$v.onnx"
    ok "  saved $PIPER_DIR/$v.onnx"
  else
    rm -f "$PIPER_DIR/$v.onnx.part" "$PIPER_DIR/$v.onnx.json"
    warn "  $v is not in rhasspy/piper-voices — put $v.onnx and $v.onnx.json into $PIPER_DIR yourself"
  fi
done

# The agent container runs as uid 10001 and may add files at runtime.
chmod -R a+rwX "$MODELS_DIR" 2>/dev/null || true
ok "models ready in $MODELS_DIR"
