"""Mongolian text normalization for the CallGo voice agent.

Public API::

    normalize_for_tts(text, lexicon) -> str
    normalize_stt(text, lexicon) -> (str, matched_entries)
    expand_numbers(text) -> str
    number_to_words(n, attributive=False) -> str
    apply_lexicon(text, entries, scope) -> (str, matched_entries)
"""

from callgo_agent.normalizer.lexicon import apply_lexicon
from callgo_agent.normalizer.numbers import (
    expand_numbers,
    number_to_words,
    ordinal_words,
)
from callgo_agent.normalizer.stt import normalize_stt
from callgo_agent.normalizer.tts import normalize_for_tts

__all__ = [
    "apply_lexicon",
    "expand_numbers",
    "normalize_for_tts",
    "normalize_stt",
    "number_to_words",
    "ordinal_words",
]
