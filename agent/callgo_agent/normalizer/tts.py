"""Text normalization for Mongolian TTS (Piper)."""

from __future__ import annotations

from callgo_agent.normalizer.lexicon import apply_lexicon
from callgo_agent.normalizer.rules import TTS_RULES, apply_rules
from callgo_agent.schemas import LexiconEntry, LexiconScope


def normalize_for_tts(text: str, lexicon: list[LexiconEntry] | None = None) -> str:
    """Make LLM output speakable: markdown/emoji/URLs removed, numbers, dates,
    times, money, phones, units and abbreviations spelled out in Mongolian,
    sentence-final punctuation ensured, then lexicon entries (scope tts/both)
    applied (``phonetic`` if set else ``correct``)."""
    out = apply_rules(text, TTS_RULES)
    if lexicon:
        out, _ = apply_lexicon(out, lexicon, LexiconScope.TTS)
    return out
