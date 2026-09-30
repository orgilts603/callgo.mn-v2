# Local speech models (faster-whisper STT, Piper TTS)

The agent's default pipeline runs speech recognition and speech synthesis **on
the worker itself**:

| Stage | Engine | Code | Profile value |
|---|---|---|---|
| STT | [faster-whisper](https://github.com/SYSTRAN/faster-whisper) (CTranslate2 Whisper) | `callgo_agent/stt_local.py` → `WhisperSTT` | `sttProvider: "faster_whisper"`, `sttModel: "large-v3"` |
| TTS | [Piper](https://github.com/OHF-Voice/piper1-gpl) (VITS, ONNX Runtime) | `callgo_agent/tts_local.py` → `PiperTTS` | `ttsProvider: "piper"`, `ttsVoice: "<voice>"` |

`callgo_agent/pipeline.py` (`build_stt` / `build_tts`) selects these or a cloud
provider (`openai`, `google`, `groq`) per agent profile.

Model files are **not** in git (`agent/models/` is git-ignored). Download them
with `scripts/download_models.py`.

## Quick start

```bash
cd agent
# both, with defaults (Whisper = $CALLGO_WHISPER_MODEL, Piper = Mongolian or fallback)
.venv/bin/python scripts/download_models.py all

# or individually
.venv/bin/python scripts/download_models.py whisper --model large-v3
.venv/bin/python scripts/download_models.py piper --voice auto --alias mn_MN-default-medium
.venv/bin/python scripts/download_models.py list-voices --lang kk
```

The script exits `0` on success, `1` on a network/download error (with a hint
about `HTTPS_PROXY`, `SSL_CERT_FILE`, `HF_ENDPOINT`, `HF_TOKEN`) and `2` on bad
arguments. Set `HF_TOKEN` to avoid Hugging Face's anonymous rate limits.

## Whisper (STT)

| Model | Disk | Min. RAM/VRAM (int8 / fp16) | Notes |
|---|---|---|---|
| `tiny` | ~75 MB | <1 GB | smoke tests only: Mongolian output is unusable (transliterated to Latin) |
| `base` | ~145 MB | ~1 GB | not usable for Mongolian |
| `small` | ~485 MB | ~1.5 GB | weak for Mongolian |
| `medium` | ~1.5 GB | ~2.5 GB / ~3 GB | usable on CPU for low call volume |
| `large-v3-turbo` | ~1.6 GB | ~2.5 GB / ~4 GB | 4 decoder layers: ~3–6× faster than large-v3 with a small accuracy loss |
| `large-v3` (default) | ~3.1 GB | ~4 GB / ~6 GB | best accuracy; GPU strongly recommended |

* **Where the files go.** By default the model lands in the Hugging Face cache
  (`~/.cache/huggingface/hub`, or `$HF_HOME` / `$HF_HUB_CACHE`), which is exactly
  where `WhisperModel("large-v3")` looks at runtime, so nothing else needs to be
  set. With `--output-dir DIR` the files are written to a plain directory: set
  `CALLGO_WHISPER_MODEL=DIR`. In Docker, mount the cache (or the directory) as
  a volume so it is not re-downloaded on every start.
* **Runtime settings.** `CALLGO_WHISPER_DEVICE` (`cpu` | `cuda` | `auto`) and
  `CALLGO_WHISPER_COMPUTE_TYPE`: use `int8` on CPU, `float16` (or
  `int8_float16`) on NVIDIA GPUs. `auto` lets CTranslate2 pick.
* **CPU vs GPU.** CUDA needs cuBLAS 12 and cuDNN 9 on the host (see the
  faster-whisper README). On CPU, CTranslate2 uses all cores (`cpu_threads=0`).
  One model is shared by every call in a worker process. With the default
  `num_workers=1`, concurrent utterances queue on it. `WhisperSTT(num_workers=N)`
  lets N decode in parallel, at the cost of more memory. Size the host for your
  peak number of simultaneous *utterances*, not calls, since callers rarely all
  speak at once.
* **Mongolian accuracy.** Mongolian is a low-resource language for Whisper, so
  expect a much higher WER than for English even with `large-v3`. The
  lexicon/normalizer stage exists to correct domain terms. A Mongolian
  fine-tune from Hugging Face can be converted with
  `ct2-transformers-converter --model <repo> --output_dir models/whisper-mn --quantization float16`
  and used via `CALLGO_WHISPER_MODEL=models/whisper-mn` (or `sttModel`).
* **Latency guards.** `WhisperSTT` decodes each VAD utterance in one pass
  (`temperature=0`, beam 5, no Whisper VAD, no previous-text conditioning). It
  caps generation at about 20 tokens per second of audio, and it drops
  repetition-loop segments (gzip ratio > 2.4). Without these guards, noise can
  make Whisper generate its full 448-token window, and the temperature fallback
  can repeat that up to 6 times. Pass
  `WhisperSTT(temperature=FALLBACK_TEMPERATURES)` for the classic behaviour.

## Piper (TTS)

Voices are two files in `CALLGO_PIPER_VOICES_DIR` (default `./models/piper`):
`<voice>.onnx` and `<voice>.onnx.json`. `PiperTTS` resolves a voice in this
order:

1. the profile's `ttsVoice`
2. `CALLGO_PIPER_DEFAULT_VOICE` (default `mn_MN-default-medium`)
3. the first voice found in the directory

It raises `PiperVoiceNotFoundError` at construction only when the directory
holds no voice at all.

**There is no published Mongolian Piper voice** (checked against
`rhasspy/piper-voices/voices.json`, 177 voices, 2026-09). `--voice auto` uses a
native `mn_*` voice if one ever appears. Otherwise it falls back in this order:

| Voice | Size | Sample rate | Why |
|---|---|---|---|
| `kk_KZ-issai-high` (auto fallback) | 128 MB, 6 speakers | 22.05 kHz | Kazakh Cyrillic. espeak-ng `kk` pronounces ө/ү/ы properly |
| `kk_KZ-iseke-x_low`, `kk_KZ-raya-x_low` | 28 MB | 16 kHz | fastest; fine for 8 kHz telephony |
| `ru_RU-irina-medium` | 63 MB | 22.05 kHz | **not recommended**: espeak `ru` spells out ө/ү letter names |

Use `--alias mn_MN-default-medium` so that the stock
`CALLGO_PIPER_DEFAULT_VOICE` points at the downloaded voice. The alias is a
symlink, or a copy where symlinks are unavailable.

`PiperTTS(espeak_voice="mn")` phonemizes text with espeak-ng's Mongolian rules
while using a fallback voice's acoustic model. Whether that sounds better than
the voice's native language depends on the voice, so listen before enabling
it. For production-quality Mongolian, train or fine-tune a Piper voice
(`espeak_voice: "mn"`) and install it:

```bash
.venv/bin/python scripts/download_models.py piper \
    --voice-url https://example.com/mn_MN-callgo-medium.onnx   # config: URL + ".json"
```

Tuning options, set per `PiperTTS` or with `update_options()`:

* `length_scale`: >1 slower, <1 faster
* `noise_scale` and `noise_w_scale`
* `sentence_silence`: default 0.2 s
* `speaker_id`: for multi-speaker voices such as `kk_KZ-issai-high`
* `use_cuda=True`: needs the `onnxruntime-gpu` package. Piper is fast enough on CPU.

## Expected latency

The measured rows come from this dev sandbox, a shared 4-vCPU VM running about
20 other jobs, so they are pessimistic. The GPU rows are typical published
figures, not measurements.

| Stage | Setup | Figure |
|---|---|---|
| Piper first audio (TTFB) | `kk_KZ-*-x_low`, CPU | 80–160 ms per sentence (measured) |
| Piper real-time factor | `x_low`/`medium`, CPU | 0.04–0.17 (measured); `high` voices are about 2–3× slower |
| Whisper `tiny` int8 | CPU, 7 s utterance | ~1.1 s (measured) |
| Whisper `large-v3` int8 | 8-core CPU, 3–5 s utterance | roughly 3–8 s. Too slow for live calls; use `medium`/`large-v3-turbo` or a GPU |
| Whisper `large-v3` fp16 | modern NVIDIA GPU, 3–5 s utterance | roughly 0.3–0.8 s |
| Whisper `large-v3-turbo` fp16 | modern NVIDIA GPU, 3–5 s utterance | roughly 0.15–0.4 s |

Whisper is batch-only, so STT latency is added after the VAD's end-of-speech
(Silero `min_silence_duration`, default 0.55 s). Piper's latency is per
sentence: `AgentSession` wraps it in `tts.StreamAdapter`, which sends each LLM
sentence to Piper as soon as it is complete.

The first `recognize()` / `synthesize()` call loads the model, which takes
seconds for Whisper large. Warm up in the job-process prewarm with
`WhisperSTT(...).load_model()` and `PiperTTS(...).load_voice()`. The loaded
models are cached per process. The framework's non-blocking `prewarm()` hooks
also start loading in a background thread when the session starts.

## Tests

```bash
.venv/bin/pytest tests/test_stt_local.py tests/test_tts_local.py tests/test_pipeline.py
# optional, against real models (skipped by default):
CALLGO_TEST_WHISPER_MODEL=tiny CALLGO_TEST_PIPER_VOICES_DIR=models/piper \
    .venv/bin/pytest tests/test_stt_local.py tests/test_tts_local.py -k real
```
