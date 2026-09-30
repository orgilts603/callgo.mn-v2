"""Worker CLI: ``python -m callgo_agent.main dev|start|console|connect ...``."""

from __future__ import annotations

import logging
import os

from dotenv import load_dotenv


def configure_logging() -> None:
    """Levels only: the LiveKit CLI installs the root handler (colored in dev, JSON in prod)."""
    level = os.environ.get("CALLGO_LOG_LEVEL", "INFO").upper()
    logging.getLogger("callgo").setLevel(level)
    for noisy in ("httpx", "httpcore"):
        logging.getLogger(noisy).setLevel(logging.WARNING)


def main() -> None:
    load_dotenv()
    configure_logging()

    from livekit.agents import cli

    from .worker import server

    cli.run_app(server)


if __name__ == "__main__":
    main()
