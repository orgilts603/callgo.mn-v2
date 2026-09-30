"""Download the local STT / TTS models used by the CallGo agent.

    python scripts/download_models.py whisper [--model large-v3] [--cache-dir DIR | --output-dir DIR]
    python scripts/download_models.py piper   [--voice auto|<name>] [--voice-url URL] [--dest DIR]
    python scripts/download_models.py all     # both, with defaults
    python scripts/download_models.py list-voices [--lang mn]

* faster-whisper models come from the Hugging Face Hub (``Systran/faster-whisper-*``).
  By default they land in the regular HF cache (``HF_HOME`` / ``HF_HUB_CACHE``), which is
  where ``WhisperModel("large-v3")`` looks at runtime. With ``--output-dir`` the files are
  written to a plain directory; set ``CALLGO_WHISPER_MODEL`` to that path.
* Piper voices come from ``rhasspy/piper-voices`` and are written to
  ``CALLGO_PIPER_VOICES_DIR`` (default ``./models/piper``) as ``<voice>.onnx`` +
  ``<voice>.onnx.json``. ``--voice auto`` picks a Mongolian (``mn_*``) voice if one is
  published, otherwise the closest fallback (Kazakh ``kk_KZ-issai-high``; its espeak-ng
  phonemizer handles Mongolian Cyrillic letters ө/ү, unlike the Russian voices). A custom
  voice can be fetched with ``--voice-url https://…/voice.onnx`` (config: ``URL + .json``
  unless ``--config-url`` is given).

Exit code is 0 on success, 1 on a download/network error, 2 on bad arguments.
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import re
import shutil
import sys
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any

AGENT_DIR = Path(__file__).resolve().parent.parent
if str(AGENT_DIR) not in sys.path:
    sys.path.insert(0, str(AGENT_DIR))

from callgo_agent.config import settings

log = logging.getLogger("download_models")

PIPER_REPO = "rhasspy/piper-voices"
MONGOLIAN_PREFIX = "mn_"
FALLBACK_VOICES = (
    "kk_KZ-issai-high",  # Kazakh, 6 speakers, 22.05 kHz, ~128 MB — best Cyrillic fallback
    "kk_KZ-iseke-x_low",  # Kazakh, 16 kHz, ~28 MB — fastest
    "ru_RU-irina-medium",  # Russian: reads ө/ү badly (espeak ru spells them out)
    "en_US-lessac-medium",
)
QUALITY_ORDER = {"medium": 0, "high": 1, "low": 2, "x_low": 3}
VOICE_PATTERN = re.compile(r"^(?P<lang>[a-z]{2,3})_(?P<region>[A-Z]{2})-(?P<name>[^-]+)-(?P<q>.+)$")

WHISPER_FILES = [
    "config.json",
    "preprocessor_config.json",
    "model.bin",
    "tokenizer.json",
    "vocabulary.*",
]


class DownloadError(RuntimeError):
    pass


def _network_hint(e: BaseException) -> str:
    return (
        f"{type(e).__name__}: {e}\n"
        "  Network / Hugging Face Hub unreachable? Check connectivity, HTTPS_PROXY and "
        "SSL_CERT_FILE, or set HF_ENDPOINT to a mirror. Gated/private repos need HF_TOKEN."
    )


# ---- faster-whisper ----------------------------------------------------------


def whisper_repo_id(model: str) -> str:
    if "/" in model:
        return model
    from faster_whisper import utils as fw_utils

    models: dict[str, str] = getattr(fw_utils, "_MODELS", {})
    repo = models.get(model)
    if repo is None:
        raise ValueError(
            f"unknown faster-whisper model {model!r}; expected one of "
            f"{', '.join(fw_utils.available_models())} or a HF repo id"
        )
    return repo


def download_whisper(
    model: str, *, cache_dir: str | None = None, output_dir: str | None = None
) -> Path:
    """Download a CTranslate2 Whisper model; returns the local model directory."""
    if Path(model).is_dir():
        log.info("whisper model %s is already a local directory", model)
        return Path(model)
    repo_id = whisper_repo_id(model)
    try:
        from huggingface_hub import snapshot_download

        kwargs: dict[str, Any] = {"allow_patterns": WHISPER_FILES}
        if output_dir:
            # faster_whisper.download_model(output_dir=…) passes `local_dir_use_symlinks`,
            # which huggingface_hub>=1.0 removed — call snapshot_download directly.
            kwargs["local_dir"] = output_dir
        elif cache_dir:
            kwargs["cache_dir"] = cache_dir
        log.info("downloading %s (%s) …", model, repo_id)
        path = Path(snapshot_download(repo_id, **kwargs))
    except ValueError:
        raise
    except Exception as e:
        raise DownloadError(
            f"failed to download whisper model {model!r}: {_network_hint(e)}"
        ) from e

    if not (path / "model.bin").is_file():
        raise DownloadError(f"download of {repo_id} finished but {path}/model.bin is missing")
    return path


# ---- piper -----------------------------------------------------------------


def _hf_file(repo: str, filename: str) -> Path:
    from huggingface_hub import hf_hub_download

    try:
        return Path(hf_hub_download(repo, filename))
    except Exception as e:
        raise DownloadError(f"failed to fetch {repo}/{filename}: {_network_hint(e)}") from e


def fetch_voice_catalog() -> dict[str, Any]:
    path = _hf_file(PIPER_REPO, "voices.json")
    with path.open(encoding="utf-8") as f:
        data: dict[str, Any] = json.load(f)
    return data


def pick_voice(catalog: dict[str, Any], lang_prefix: str = MONGOLIAN_PREFIX) -> tuple[str, bool]:
    """Return ``(voice, is_native)`` — a native ``lang_prefix`` voice if published."""
    native = [k for k in catalog if k.startswith(lang_prefix)]
    if native:
        native.sort(key=lambda k: (QUALITY_ORDER.get(k.rsplit("-", 1)[-1], 9), k))
        return native[0], True
    for fallback in FALLBACK_VOICES:
        if fallback in catalog:
            return fallback, False
    raise DownloadError("piper voices.json contains neither a Mongolian nor a fallback voice")


def _voice_repo_paths(voice: str, catalog: dict[str, Any] | None) -> tuple[str, str]:
    if catalog and voice in catalog:
        files = [f for f in catalog[voice].get("files", {}) if f.endswith((".onnx", ".onnx.json"))]
        onnx = next((f for f in files if f.endswith(".onnx")), None)
        cfg = next((f for f in files if f.endswith(".onnx.json")), None)
        if onnx and cfg:
            return onnx, cfg
    m = VOICE_PATTERN.match(voice)
    if not m:
        raise ValueError(
            f"voice {voice!r} does not look like <lang>_<REGION>-<name>-<quality> "
            "(e.g. kk_KZ-issai-high); use --voice-url for custom voices"
        )
    base = f"{m['lang']}/{m['lang']}_{m['region']}/{m['name']}/{m['q']}/{voice}"
    return f"{base}.onnx", f"{base}.onnx.json"


def _install(src: Path, dst: Path, force: bool) -> None:
    if dst.is_file() and dst.stat().st_size > 0 and not force:
        log.info("exists, skipping: %s", dst)
        return
    dst.parent.mkdir(parents=True, exist_ok=True)
    tmp = dst.with_name(dst.name + ".part")
    shutil.copyfile(src, tmp)
    os.replace(tmp, dst)


def download_piper_voice(
    voice: str, dest: Path, *, catalog: dict[str, Any] | None = None, force: bool = False
) -> Path:
    onnx_path, cfg_path = _voice_repo_paths(voice, catalog)
    log.info("downloading piper voice %s …", voice)
    model_src = _hf_file(PIPER_REPO, onnx_path)
    cfg_src = _hf_file(PIPER_REPO, cfg_path)
    model_dst = dest / f"{voice}.onnx"
    _install(model_src, model_dst, force)
    _install(cfg_src, dest / f"{voice}.onnx.json", force)
    return model_dst


def _download_url(url: str, dst: Path, force: bool) -> None:
    if dst.is_file() and dst.stat().st_size > 0 and not force:
        log.info("exists, skipping: %s", dst)
        return
    dst.parent.mkdir(parents=True, exist_ok=True)
    tmp = dst.with_name(dst.name + ".part")
    try:
        with urllib.request.urlopen(url, timeout=60) as resp, tmp.open("wb") as out:
            shutil.copyfileobj(resp, out)
        os.replace(tmp, dst)
    except (urllib.error.URLError, OSError, TimeoutError) as e:
        tmp.unlink(missing_ok=True)
        raise DownloadError(f"failed to download {url}: {_network_hint(e)}") from e


def download_voice_url(
    url: str,
    dest: Path,
    *,
    name: str | None = None,
    config_url: str | None = None,
    force: bool = False,
) -> Path:
    stem = name or Path(url.split("?", 1)[0]).name.removesuffix(".onnx")
    if not stem:
        raise ValueError(f"cannot derive a voice name from {url!r}; pass --name")
    cfg_url = config_url or (url.split("?", 1)[0] + ".json")
    model_dst = dest / f"{stem}.onnx"
    _download_url(url, model_dst, force)
    _download_url(cfg_url, dest / f"{stem}.onnx.json", force)
    return model_dst


def _alias(model_path: Path, alias: str) -> None:
    """Expose a voice under another name (e.g. the configured default voice)."""
    for suffix in (".onnx", ".onnx.json"):
        src = model_path.with_name(model_path.name.removesuffix(".onnx") + suffix)
        dst = model_path.with_name(alias + suffix)
        if dst.exists() or dst.is_symlink():
            dst.unlink()
        try:
            dst.symlink_to(src.name)
        except OSError:
            shutil.copyfile(src, dst)
    log.info("aliased %s -> %s", model_path.name, alias)


# ---- CLI -----------------------------------------------------------------------


def cmd_whisper(args: argparse.Namespace) -> None:
    path = download_whisper(args.model, cache_dir=args.cache_dir, output_dir=args.output_dir)
    print(f"whisper model {args.model!r} ready at {path}")
    if args.output_dir:
        print(f"  set CALLGO_WHISPER_MODEL={path}")
    elif args.cache_dir:
        print(f"  set HF_HUB_CACHE={args.cache_dir} (or CALLGO_WHISPER_MODEL={path})")


def cmd_piper(args: argparse.Namespace) -> None:
    dest = Path(args.dest)
    if args.voice_url:
        model = download_voice_url(
            args.voice_url, dest, name=args.name, config_url=args.config_url, force=args.force
        )
    else:
        catalog = fetch_voice_catalog()
        voice = args.voice
        if voice == "auto":
            voice, native = pick_voice(catalog)
            if not native:
                print(
                    f"NOTE: rhasspy/piper-voices has no Mongolian (mn_*) voice; using fallback "
                    f"{voice!r}. For native Mongolian speech train/fine-tune a Piper voice "
                    "(espeak-ng has an 'mn' phonemizer) and install it with --voice-url, or try "
                    "PiperTTS(espeak_voice='mn') with the fallback voice."
                )
        elif voice not in catalog:
            log.warning("voice %r is not in voices.json; trying the conventional path", voice)
        model = download_piper_voice(voice, dest, catalog=catalog, force=args.force)
    if args.alias:
        _alias(model, args.alias)
    name = args.alias or model.name.removesuffix(".onnx")
    print(f"piper voice ready: {model}")
    if name != settings.piper_default_voice:
        print(f"  set CALLGO_PIPER_DEFAULT_VOICE={name} (or use it as a profile tts_voice)")


def cmd_list_voices(args: argparse.Namespace) -> None:
    catalog = fetch_voice_catalog()
    for key in sorted(catalog):
        if args.lang and not key.startswith(args.lang):
            continue
        files = catalog[key].get("files", {})
        size = sum(v.get("size_bytes", 0) for f, v in files.items() if f.endswith(".onnx"))
        print(f"{key:40s} {size / 1e6:7.1f} MB  speakers={catalog[key].get('num_speakers', 1)}")


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawTextHelpFormatter)
    p.add_argument("-v", "--verbose", action="store_true")
    sub = p.add_subparsers(dest="cmd", required=True)

    def add_whisper_args(sp: argparse.ArgumentParser) -> None:
        sp.add_argument("--model", default=settings.whisper_model, help="size or HF repo id")
        g = sp.add_mutually_exclusive_group()
        g.add_argument("--cache-dir", help="HF cache dir (default: HF_HOME/HF_HUB_CACHE)")
        g.add_argument("--output-dir", help="plain directory for the model files")

    def add_piper_args(sp: argparse.ArgumentParser) -> None:
        sp.add_argument("--voice", default="auto", help="voice key, or 'auto' (Mongolian/fallback)")
        sp.add_argument("--voice-url", help="direct URL of a custom .onnx voice")
        sp.add_argument("--config-url", help="URL of the .onnx.json (default: voice URL + .json)")
        sp.add_argument("--name", help="local name for --voice-url voices")
        sp.add_argument("--dest", default=settings.piper_voices_dir, help="voices directory")
        sp.add_argument(
            "--alias",
            help=f"also expose the voice under this name (e.g. {settings.piper_default_voice})",
        )
        sp.add_argument("--force", action="store_true", help="re-download existing files")

    add_whisper_args(sub.add_parser("whisper", help="download a faster-whisper model"))
    add_piper_args(sub.add_parser("piper", help="download a Piper voice"))
    sp_all = sub.add_parser("all", help="download both (defaults)")
    add_whisper_args(sp_all)
    add_piper_args(sp_all)
    lv = sub.add_parser("list-voices", help="list published Piper voices")
    lv.add_argument("--lang", default="", help="key prefix filter, e.g. mn, kk, ru")
    return p


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    logging.basicConfig(
        level=logging.DEBUG if args.verbose else logging.INFO, format="%(levelname)s %(message)s"
    )
    if not args.verbose:
        for noisy in ("httpx", "huggingface_hub"):
            logging.getLogger(noisy).setLevel(logging.WARNING)
    try:
        if args.cmd in ("whisper", "all"):
            cmd_whisper(args)
        if args.cmd in ("piper", "all"):
            cmd_piper(args)
        if args.cmd == "list-voices":
            cmd_list_voices(args)
    except ValueError as e:
        print(f"error: {e}", file=sys.stderr)
        return 2
    except DownloadError as e:
        print(f"error: {e}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
