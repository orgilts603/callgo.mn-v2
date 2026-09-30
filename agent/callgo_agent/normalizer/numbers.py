"""Mongolian number reading (cardinals, ordinals, decimals, digit strings).

Everything here is pure and deterministic.

Grammar tables
--------------
Mongolian numerals have two surface forms. The *standalone* form is used when
the numeral is the last word of a phrase ("Би тав") and the *attributive* form
when it modifies a following noun or is followed by another numeral word
("таван хүн", "арван нэг", "хорин тав"):

=====  ==========  ===========    =====  ==========  ===========
digit  standalone  attributive    tens   standalone  attributive
=====  ==========  ===========    =====  ==========  ===========
1      нэг         нэг            10     арав        арван
2      хоёр        хоёр           20     хорь        хорин
3      гурав       гурван         30     гуч         гучин
4      дөрөв       дөрвөн         40     дөч         дөчин
5      тав         таван          50     тавь        тавин
6      зургаа      зургаан        60     жар         жаран
7      долоо       долоон         70     дал         далан
8      найм        найман         80     ная         наян
9      ес          есөн           90     ер          ерэн
=====  ==========  ===========    =====  ==========  ===========

Scale words: 100 зуу / зуун, 1 000 мянга / мянган, 10^6 сая, 10^9 тэрбум
(сая and тэрбум are invariant). Every word followed by another word of the
same number takes its attributive form ("зуун тав", "хорин нэг"), except
мянга which only becomes мянган as the last word in front of a noun:
2500 "хоёр мянга таван зуу", 15 000 "арван таван мянга", and
"арван таван мянган төгрөг".

Coefficient 1: a *leading* 1 is dropped before зуу and мянга (100 "зуу",
1000 "мянга", 1100 "мянга нэг зуу") but never before сая / тэрбум
("нэг сая", "нэг тэрбум"), and it is kept when it is not the first group
("нэг сая нэг мянга").

Ordinals: the suffix is ``-дугаар`` after a back-vowel stem and ``-дүгээр``
after a front-vowel stem. Harmony is decided by the last non-neutral vowel of
the (possibly truncated) stem: back = а о у я ё ю ы, front = э ө ү е,
neutral = и. Stems: зургаа→зурга, долоо→долд, all others unchanged
(3 гуравдугаар, 5 тавдугаар, 9 есдүгээр, 1 нэгдүгээр, 6 зургадугаар,
7 долдугаар, 20 хорьдугаар).

Numbers >= 10^12 are read digit by digit so that no digit ever survives.
"""

from __future__ import annotations

import re

ZERO = "тэг"

_UNITS = ("тэг", "нэг", "хоёр", "гурав", "дөрөв", "тав", "зургаа", "долоо", "найм", "ес")
_UNITS_ATTR = ("тэг", "нэг", "хоёр", "гурван", "дөрвөн", "таван", "зургаан", "долоон", "найман", "есөн")
_TENS = (None, "арав", "хорь", "гуч", "дөч", "тавь", "жар", "дал", "ная", "ер")
_TENS_ATTR = (None, "арван", "хорин", "гучин", "дөчин", "тавин", "жаран", "далан", "наян", "ерэн")

# (standalone, attributive)
_Tok = tuple[str, str]
_HUNDRED: _Tok = ("зуу", "зуун")
_THOUSAND: _Tok = ("мянга", "мянган")
_MILLION: _Tok = ("сая", "сая")
_BILLION: _Tok = ("тэрбум", "тэрбум")

MAX_WORDED = 10**12 - 1

_BACK = set("аоуяёюы")
_FRONT = set("эөүе")

_ORDINAL_STEM = {"зургаа": "зурга", "долоо": "долд"}


def _unit(u: int) -> _Tok:
    return (_UNITS[u], _UNITS_ATTR[u])


def _group_tokens(g: int, *, leading: bool) -> list[_Tok]:
    """Tokens for a group value 1..999."""
    toks: list[_Tok] = []
    h, r = divmod(g, 100)
    if h:
        if not (h == 1 and leading):
            toks.append(_unit(h))
        toks.append(_HUNDRED)
    t, u = divmod(r, 10)
    if t:
        tens = _TENS[t]
        tens_attr = _TENS_ATTR[t]
        assert tens is not None and tens_attr is not None
        toks.append((tens, tens_attr))
    if u:
        toks.append(_unit(u))
    return toks


def digits_to_words(digits: str, sep: str = " ") -> str:
    """Read a digit string digit by digit ("9911" -> "ес ес нэг нэг")."""
    return sep.join(_UNITS[int(c)] for c in digits if c in "0123456789")


def _tokens(n: int) -> list[_Tok]:
    if n == 0:
        return [_unit(0)]
    toks: list[_Tok] = []
    rest = n
    leading = True
    for scale, tok in ((10**9, _BILLION), (10**6, _MILLION), (10**3, _THOUSAND)):
        g, rest = divmod(rest, scale)
        if not g:
            continue
        if tok is _THOUSAND and g == 1 and leading:
            toks.append(tok)
        else:
            toks.extend(_group_tokens(g, leading=leading))
            toks.append(tok)
        leading = False
    if rest:
        toks.extend(_group_tokens(rest, leading=leading))
    return toks


def number_to_words(n: int, attributive: bool = False) -> str:
    """Spell ``n`` in Mongolian Cyrillic.

    ``attributive=True`` puts the last word in its attributive form (use it
    when a noun follows: 3 -> "гурван", 15000 -> "арван таван мянган").
    Negative numbers are prefixed with "хасах". Values >= 10^12 are read digit
    by digit.
    """
    if n < 0:
        return "хасах " + number_to_words(-n, attributive)
    if n > MAX_WORDED:
        return digits_to_words(str(n))
    toks = _tokens(n)
    # мянга keeps its plain form unless it is the last word before a noun
    words = [t[0] if t is _THOUSAND else t[1] for t in toks[:-1]]
    words.append(toks[-1][1] if attributive else toks[-1][0])
    return " ".join(words)


def harmony(word: str) -> str:
    """Return "back" or "front" by the last non-neutral vowel of ``word``."""
    for ch in reversed(word.lower()):
        if ch in _BACK:
            return "back"
        if ch in _FRONT:
            return "front"
    return "back"


def ordinal_words(n: int) -> str:
    """Ordinal of ``n``: 3 -> "гуравдугаар", 9 -> "есдүгээр", 21 -> "хорин нэгдүгээр"."""
    words = number_to_words(n).split(" ")
    last = words[-1]
    stem = _ORDINAL_STEM.get(last, last)
    suffix = "дугаар" if harmony(stem) == "back" else "дүгээр"
    words[-1] = stem + suffix
    return " ".join(words)


def _fraction_words(frac: str, attributive: bool) -> str:
    zeros = len(frac) - len(frac.lstrip("0"))
    rest = frac[zeros:]
    parts = [ZERO] * zeros
    if not rest:
        return " ".join(parts)
    if len(rest) <= 3:
        parts.append(number_to_words(int(rest), attributive))
    else:
        parts.append(digits_to_words(rest))
    return " ".join(parts)


def decimal_to_words(integer: str, frac: str, attributive: bool = False) -> str:
    """3.5 -> "гурван цэг тав" (integer part is attributive before "цэг")."""
    head = _read_int(integer, True)
    return f"{head} цэг {_fraction_words(frac, attributive)}"


def _clean_int(s: str) -> str:
    return re.sub(r"[,  ' ]", "", s)


def _read_int(s: str, attributive: bool) -> str:
    digits = _clean_int(s)
    if len(digits) > 1 and digits.startswith("0"):
        return digits_to_words(digits)  # codes such as "007" are read digit by digit
    return number_to_words(int(digits), attributive)


def read_number(token: str, attributive: bool = False) -> str:
    """Read a numeric token: "15,000", "15 000", "3.5", "007"."""
    if "." in token:
        integer, frac = token.split(".", 1)
        return decimal_to_words(integer, frac, attributive)
    return _read_int(token, attributive)


# ---------------------------------------------------------------------------
# text-level expansion

_GROUPED_COMMA = r"[0-9]{1,3}(?:[,  '][0-9]{3})+"
_GROUPED_SPACE = r"[0-9]{1,3}(?: [0-9]{3})+"
NUM = rf"(?:{_GROUPED_COMMA}|{_GROUPED_SPACE}|[0-9]+)"

_WORD_AHEAD = re.compile(r"\s*[^\W\d_]")


def word_follows(text: str, pos: int) -> bool:
    """True when the next non-space character at ``pos`` starts a word."""
    return _WORD_AHEAD.match(text, pos) is not None


ORDINAL_RE = re.compile(
    rf"(?<![0-9])({NUM})\s*-\s*р(?![\w])|(?<![0-9])({NUM})\s*-?\s*(?:дугаар|дүгээр)(?![\w])"
)
DECIMAL_RE = re.compile(rf"(?<![0-9.,])({NUM})\.([0-9]+)(?![0-9])(?!\.[0-9])")
_CASE_SUFFIX = (
    "ын|ийн|ний|ны|ийг|ыг|д|т|аас|ээс|оос|өөс|аар|ээр|оор|өөр|тай|тэй|той|руу|рүү|н|ад|эд|од|өд|нд|ийх|ынх"
)
INT_RE = re.compile(rf"(?<![0-9]){NUM}(?![0-9])(?:-({_CASE_SUFFIX})(?![\w]))?")


def ordinal_sub(m: re.Match[str]) -> str:
    raw = m.group(1) or m.group(2)
    return f" {ordinal_words(int(_clean_int(raw)))} "


def decimal_sub(m: re.Match[str]) -> str:
    attr = word_follows(m.string, m.end())
    return f" {decimal_to_words(m.group(1), m.group(2), attr)} "


def int_sub(m: re.Match[str]) -> str:
    suffix = m.group(1)
    core = m.group(0)
    if suffix:
        core = core[: -(len(suffix) + 1)]
        return f" {_read_int(core, False)}{suffix} "
    attr = word_follows(m.string, m.end())
    return f" {_read_int(core, attr)} "


def expand_numbers(text: str) -> str:
    """Expand ordinals ("3-р"), decimals ("3.5") and integers in ``text``.

    A number directly followed by a word takes the attributive form
    ("3 хүн" -> "гурван хүн"); otherwise the standalone form is used.
    Whitespace is collapsed.
    """
    text = ORDINAL_RE.sub(ordinal_sub, text)
    text = DECIMAL_RE.sub(decimal_sub, text)
    text = INT_RE.sub(int_sub, text)
    return re.sub(r"\s+", " ", text).strip()
