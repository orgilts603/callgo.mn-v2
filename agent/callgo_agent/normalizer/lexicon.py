"""Lexicon (word correction) application for STT and TTS text."""

from __future__ import annotations

import re
from collections.abc import Iterable
from functools import lru_cache

from callgo_agent.schemas import LexiconEntry, LexiconScope

_WS = re.compile(r"\s+")


def _key(s: str) -> str:
    return _WS.sub(" ", s.strip()).lower()


@lru_cache(maxsize=64)
def _compile(keys: tuple[str, ...]) -> re.Pattern[str]:
    """One alternation, longest ``wrong`` first, whole-word, case-insensitive."""
    ordered = sorted(keys, key=lambda k: (-len(k), k))
    alts = "|".join(r"\s+".join(re.escape(part) for part in k.split(" ")) for k in ordered)
    return re.compile(rf"(?<!\w)(?:{alts})(?!\w)", re.IGNORECASE)


def _applies(entry: LexiconEntry, scope: LexiconScope) -> bool:
    return entry.scope == LexiconScope.BOTH or entry.scope == scope


def _preserve_case(matched: str, replacement: str) -> str:
    letters = [c for c in matched if c.isalpha()]
    if len(letters) > 1 and all(c.isupper() for c in letters):
        return replacement.upper()
    if letters and letters[0].isupper() and replacement[:1].islower():
        return replacement[:1].upper() + replacement[1:]
    return replacement


def apply_lexicon(
    text: str,
    entries: Iterable[LexiconEntry],
    scope: LexiconScope | str,
) -> tuple[str, list[LexiconEntry]]:
    """Apply the entries relevant for ``scope`` to ``text``.

    * ``scope == STT``: ``wrong`` -> ``correct``, preserving the matched case
      (ALL CAPS stays upper, a capitalised match capitalises a lower-case
      correction).
    * ``scope == TTS``: ``wrong`` -> ``phonetic`` if set else ``correct``,
      verbatim.

    Entries with scope ``both`` apply to either. Matching is whole-word,
    Unicode/Cyrillic aware and case-insensitive; multi-word ``wrong`` values
    match across any whitespace. All replacements happen in a single pass, so
    an output is never re-matched by another entry. If several entries share
    the same ``wrong``, the first wins.

    Returns ``(new_text, hits)`` where ``hits`` holds each entry that changed
    the text at least once, in order of first occurrence (no duplicates).
    """
    scope = LexiconScope(scope)
    by_key: dict[str, tuple[LexiconEntry, str]] = {}
    for e in entries:
        k = _key(e.wrong)
        if not k or k in by_key or not _applies(e, scope):
            continue
        repl = (e.phonetic or e.correct) if scope == LexiconScope.TTS else e.correct
        by_key[k] = (e, repl)
    if not by_key or not text:
        return text, []

    hits: list[LexiconEntry] = []
    seen: set[int] = set()

    def sub(m: re.Match[str]) -> str:
        matched = m.group(0)
        entry, repl = by_key[_key(matched)]
        out = repl if scope == LexiconScope.TTS else _preserve_case(matched, repl)
        if out != matched and id(entry) not in seen:
            seen.add(id(entry))
            hits.append(entry)
        return out

    return _compile(tuple(by_key)).sub(sub, text), hits
