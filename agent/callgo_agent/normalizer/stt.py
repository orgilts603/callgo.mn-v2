"""Cleanup of raw STT (Whisper) output before it reaches the LLM."""

from __future__ import annotations

import re

from callgo_agent.normalizer.lexicon import apply_lexicon
from callgo_agent.normalizer.rules import Rule, apply_rules
from callgo_agent.schemas import LexiconEntry, LexiconScope

# Latin letters that render like a Cyrillic letter (a e o p c x y + capitals).
LATIN_TO_CYRILLIC = {
    "a": "а", "e": "е", "o": "о", "p": "р", "c": "с", "x": "х", "y": "у",
    "A": "А", "B": "В", "C": "С", "E": "Е", "H": "Н", "K": "К", "M": "М",
    "O": "О", "P": "Р", "T": "Т", "X": "Х", "Y": "У",
}
_TRANSLATE = str.maketrans(LATIN_TO_CYRILLIC)
_LATIN = re.compile(r"[A-Za-z]")
_CYRILLIC = re.compile(r"[Ѐ-ӿ]")
_TOKEN = re.compile(r"[^\W\d_]+")


def _fix_lookalikes(m: re.Match[str]) -> str:
    tok = m.group(0)
    if not _CYRILLIC.search(tok) or not _LATIN.search(tok):
        return tok
    if any(c not in LATIN_TO_CYRILLIC for c in _LATIN.findall(tok)):
        return tok
    return tok.translate(_TRANSLATE)


def _fix_case(m: re.Match[str]) -> str:
    tok = m.group(0)
    if len(tok) < 3 or not _CYRILLIC.search(tok):
        return tok
    if tok.islower() or tok.isupper() or (tok[0].isupper() and tok[1:].islower()):
        return tok
    return tok[0] + tok[1:].lower() if tok[0].isupper() else tok.lower()


def _shouting(m: re.Match[str]) -> str:
    s = m.group(0)
    letters = [c for c in s if c.isalpha()]
    if len(letters) < 4 or len(s.split()) < 2 or not all(c.isupper() for c in letters):
        return s
    s = s.lower()
    return s[:1].upper() + s[1:]


STT_RULES: list[Rule] = [
    Rule("unicode", re.compile(r"[​-‍⁠﻿]"), lambda _m: ""),
    Rule("nbsp", re.compile(r"[  ]"), lambda _m: " "),
    Rule("stray_symbols", re.compile(r"[*#~^_|\\<>{}\[\]=`\"“”«»„]"), lambda _m: " "),
    Rule("collapse_ws", re.compile(r"\s+"), lambda _m: " "),
    Rule("lookalikes", _TOKEN, _fix_lookalikes),
    Rule("shouting", re.compile(r"(?s)\A.*\Z"), _shouting),
    Rule("mixed_case", _TOKEN, _fix_case),
    Rule("space_before_punct", re.compile(r" +([,.;:!?…])"), lambda m: m.group(1)),
    Rule("dup_pause", re.compile(r"([,;:])(?: *[,;:])+"), lambda m: m.group(1)),
    Rule("pause_before_stop", re.compile(r"[,;:] *([.!?…])"), lambda m: m.group(1)),
    Rule("dup_stop", re.compile(r"\.{2,}"), lambda _m: "..."),
    Rule("dup_mark", re.compile(r"([!?])\1+"), lambda m: m.group(1)),
    Rule("mixed_stop", re.compile(r"([!?])[.!?]+"), lambda m: m.group(1)),
    Rule("space_after_pause", re.compile(r"([,;!?])(?=[^\s,;:!?.…])"), lambda m: m.group(1) + " "),
    Rule(
        "space_after_stop",
        re.compile(r"(?<=[а-яөүё]{2})\.(?=[А-ЯӨҮЁ])"),
        lambda _m: ". ",
    ),
    Rule("leading_punct", re.compile(r"\A[\s,;:.\-]+"), lambda _m: ""),
    Rule("trim", re.compile(r"(?s)\A\s+|\s+\Z"), lambda _m: ""),
]


def normalize_stt(
    text: str, lexicon: list[LexiconEntry] | None = None
) -> tuple[str, list[LexiconEntry]]:
    """Clean STT output and apply lexicon entries (scope stt/both).

    Steps: strip zero-width/stray symbols, collapse whitespace, map Latin
    look-alike letters to Cyrillic inside otherwise-Cyrillic words, repair
    odd capitalisation ("сАйн" -> "сайн", all-caps utterances), tidy
    punctuation, then apply the lexicon (wrong -> correct, whole word,
    case-preserving). Returns ``(text, matched_entries)``.
    """
    out = apply_rules(text, STT_RULES)
    hits: list[LexiconEntry] = []
    if lexicon:
        out, hits = apply_lexicon(out, lexicon, LexiconScope.STT)
    return out, hits
