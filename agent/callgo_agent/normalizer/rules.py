"""Ordered rule pipeline that turns LLM text into something a Mongolian TTS can speak.

Each :class:`Rule` is ``(name, compiled regex, replacement function)``; the
pipeline applies ``regex.sub(fn, text)`` in order. Order matters and is
documented next to :data:`TTS_RULES`.
"""

from __future__ import annotations

import re
import unicodedata
from collections.abc import Callable
from dataclasses import dataclass

from callgo_agent.normalizer.numbers import (
    DECIMAL_RE,
    INT_RE,
    NUM,
    ORDINAL_RE,
    decimal_sub,
    digits_to_words,
    int_sub,
    number_to_words,
    ordinal_sub,
    ordinal_words,
    read_number,
    word_follows,
)

Repl = Callable[[re.Match[str]], str]


@dataclass(frozen=True)
class Rule:
    name: str
    pattern: re.Pattern[str]
    fn: Repl

    def apply(self, text: str) -> str:
        return self.pattern.sub(self.fn, text)


def _r(name: str, pattern: str, fn: Repl | str, flags: int = 0) -> Rule:
    if isinstance(fn, str):
        repl = fn
        return Rule(name, re.compile(pattern, flags), lambda _m, _r=repl: _r)
    return Rule(name, re.compile(pattern, flags), fn)


def apply_rules(text: str, rules: list[Rule]) -> str:
    for rule in rules:
        text = rule.apply(text)
    return text


def _clean(s: str) -> str:
    return re.sub(r"[,  ' ]", "", s)


# ---------------------------------------------------------------------------
# unicode / markdown / emoji

_ZERO_WIDTH = "​‌‍⁠﻿️︎"


def _unicode(m: re.Match[str]) -> str:
    s = unicodedata.normalize("NFC", m.group(0))
    out = []
    for ch in s:
        if ch in _ZERO_WIDTH:
            continue
        if ch.isspace():
            out.append(" " if ch not in "\n\r" else ch)
        elif ch.isdecimal() and not ch.isascii():
            out.append(str(unicodedata.decimal(ch)))
        else:
            out.append(ch)
    return "".join(out)


def _strip_url_tail(url: str) -> tuple[str, str]:
    core = url.rstrip(".,;:!?")
    return core, url[len(core) :]


def _domain_words(host: str) -> str:
    return re.sub(r"\.", " цэг ", host)


def _url(m: re.Match[str]) -> str:
    core, tail = _strip_url_tail(m.group(0))
    core = re.sub(r"(?i)^(?:https?://)?(?:www\.)?", "", core)
    host = re.split(r"[/?#]", core, maxsplit=1)[0]
    host = re.sub(r":[0-9]+$", "", host)
    return f" {_domain_words(host)} {tail}"


def _email(m: re.Match[str]) -> str:
    local, domain = m.group(0).split("@", 1)
    return f" {_domain_words(local)} эт {_domain_words(domain)} "


def _tilde(m: re.Match[str]) -> str:
    return " ойролцоогоор "


_EMOJI = (
    "[\U0001f000-\U0001faff←-⇿⌀-⏿①-⓿■-➿"
    "⤀-⥿⬀-⯿〰〽㊗㊙\U000e0020-\U000e007f]"
)


def _newline(m: re.Match[str]) -> str:
    s = m.string
    if m.start() == 0 or m.end() == len(s):
        return " "
    prev = s[m.start() - 1]
    return " " if prev in ".!?…,;:" else ". "


# ---------------------------------------------------------------------------
# abbreviations and units

ABBREVIATIONS: dict[str, str] = {
    "ХХК": "хязгаарлагдмал хариуцлагатай компани",
    "ХК": "хувьцаат компани",
    "ББСБ": "банк бус санхүүгийн байгууллага",
    "ААН": "аж ахуйн нэгж",
    "НӨАТ": "нэмэгдсэн өртгийн албан татвар",
    "УИХ": "Улсын Их Хурал",
    "ЗГ": "Засгийн газар",
    "УБ": "Улаанбаатар",
    "АНУ": "Америкийн Нэгдсэн Улс",
    "ТУЗ": "Төлөөлөн удирдах зөвлөл",
}
DOTTED_ABBREVIATIONS: dict[str, str] = {
    "т.х": "тэргүүтэй",
    "г.м": "гэх мэт",
    "ж.нь": "жишээ нь",
}
_ABBR_RE = "|".join(sorted(map(re.escape, ABBREVIATIONS), key=len, reverse=True))
_DOTTED_RE = "|".join(sorted(map(re.escape, DOTTED_ABBREVIATIONS), key=len, reverse=True))
_SUFFIX = r"(?:-([а-яөүё]{1,4}))?"


def _abbr(m: re.Match[str]) -> str:
    return ABBREVIATIONS[m.group(1)] + (m.group(2) or "")


def _dotted(m: re.Match[str]) -> str:
    return DOTTED_ABBREVIATIONS[m.group(1).lower()]


UNITS: dict[str, str] = {
    "км²": "квадрат километр", "км2": "квадрат километр",
    "м²": "квадрат метр", "м2": "квадрат метр", "㎡": "квадрат метр",
    "см²": "квадрат сантиметр", "см2": "квадрат сантиметр",
    "м³": "шоо метр", "м3": "шоо метр",
    "км/ц": "километр цаг", "км/цаг": "километр цаг", "km/h": "километр цаг",
    "км": "километр", "km": "километр",
    "кг": "килограмм", "kg": "килограмм",
    "мг": "миллиграмм", "mg": "миллиграмм",
    "гр": "грамм", "г": "грамм", "g": "грамм",
    "тн": "тонн",
    "мм": "миллиметр", "mm": "миллиметр",
    "см": "сантиметр", "cm": "сантиметр",
    "м": "метр", "m": "метр",
    "мл": "миллилитр", "ml": "миллилитр",
    "л": "литр", "l": "литр",
    "га": "гектар",
    "кВт": "киловатт", "kW": "киловатт", "МВт": "мегаватт", "Вт": "ватт", "W": "ватт",
    "ГБ": "гигабайт", "GB": "гигабайт", "МБ": "мегабайт", "MB": "мегабайт",
    "КБ": "килобайт", "KB": "килобайт",
    "°C": "градус цельс", "°С": "градус цельс", "°F": "градус фаренгейт", "°": "градус",
    "ш": "ширхэг",
    "мин": "минут",
    "сек": "секунд",
}
_UNIT_RE = "|".join(sorted(map(re.escape, UNITS), key=len, reverse=True))


def _unit(m: re.Match[str]) -> str:
    return " " + UNITS[m.group(1)]


# ---------------------------------------------------------------------------
# dates and times


def _valid_md(month: int, day: int) -> bool:
    return 1 <= month <= 12 and 1 <= day <= 31


def _date_words(year: int | None, month: int, day: int) -> str:
    parts = []
    if year is not None:
        parts.append(number_to_words(year, True) + " оны")
    parts.append(ordinal_words(month) + " сарын")
    parts.append(number_to_words(day))
    return f" {' '.join(parts)} "


def _date_ymd(m: re.Match[str]) -> str:
    y, mo, d = int(m.group(1)), int(m.group(2)), int(m.group(3))
    return _date_words(y, mo, d) if _valid_md(mo, d) else m.group(0)


def _date_dmy(m: re.Match[str]) -> str:
    d, mo, y = int(m.group(1)), int(m.group(2)), int(m.group(3))
    return _date_words(y, mo, d) if _valid_md(mo, d) else m.group(0)


def _date_md(m: re.Match[str]) -> str:
    mo, d = int(m.group(1)), int(m.group(2))
    return _date_words(None, mo, d) if _valid_md(mo, d) else m.group(0)


def _time(m: re.Match[str]) -> str:
    h, mi, se = int(m.group(1)), int(m.group(2)), m.group(3)
    parts = [number_to_words(h, True) + " цаг"]
    if mi or se:
        if mi:
            parts.append(number_to_words(mi, True) + " минут")
        if se and int(se):
            parts.append(number_to_words(int(se), True) + " секунд")
    return f" {' '.join(parts)} "


# ---------------------------------------------------------------------------
# money, percent, phone

_CUR_MNT = r"(?:₮|төг(?![\wа-яөүё])|МНТ|MNT|mnt|мнт)"
_CUR_USD = r"(?:\$|USD|usd)"
_CUR_EUR = r"(?:€|EUR|eur)"
_CUR_TAIL = rf"(?:{_CUR_MNT}|{_CUR_USD}|{_CUR_EUR})"
_CUR_NAME = {"mnt": "төгрөг", "usd": "доллар", "eur": "евро"}


def _cur_kind(sym: str) -> str:
    s = sym.lower()
    if s in {"₮", "төг", "мнт", "mnt"}:
        return "mnt"
    if s in {"$", "usd"}:
        return "usd"
    return "eur"


def _money(amount: str, kind: str) -> str:
    integer, _, frac = amount.partition(".")
    name = _CUR_NAME[kind]
    words = f"{read_number(integer, True)} {name}"
    if frac and int(frac):
        sub = "мөнгө" if kind == "mnt" else "цент"
        words += f" {read_number(frac[:2].ljust(2, '0'), True)} {sub}"
    return f" {words} "


def _money_suffix(m: re.Match[str]) -> str:
    return _money(m.group(1), _cur_kind(m.group(2)))


def _money_prefix(m: re.Match[str]) -> str:
    return _money(m.group(2), _cur_kind(m.group(1)))


def _percent(m: re.Match[str]) -> str:
    return f" {read_number(m.group(1), True)} хувь "


def _thousands_k(m: re.Match[str]) -> str:
    attr = word_follows(m.string, m.end())
    return f" {number_to_words(int(m.group(1)) * 1000, attr)} "


_PHONE_GUARD = rf"(?!\s*(?:{_CUR_TAIL}|%))(?![0-9])(?!\.[0-9])"


def _phone_groups(digits: str) -> str:
    groups = [digits[i : i + 4] for i in range(0, len(digits), 4)]
    return ", ".join(digits_to_words(g) for g in groups)


def _phone(m: re.Match[str]) -> str:
    raw = m.group(0)
    digits = re.sub(r"[^0-9]", "", raw)
    prefix = ""
    if raw.lstrip().startswith("+"):
        prefix = "нэмэх "
        if digits.startswith("976") and len(digits) > 8:
            prefix += digits_to_words("976") + ", "
            digits = digits[3:]
    return f" {prefix}{_phone_groups(digits)}, "


# ---------------------------------------------------------------------------
# signs, symbols, punctuation


def _final_punct(m: re.Match[str]) -> str:
    s = m.group(0).strip()
    s = re.sub(r"[\s,;:\-]+$", "", s)
    if not re.search(r"[^\W_]", s):
        return ""
    if s[-1] not in ".!?…":
        s += "."
    return s


TTS_RULES: list[Rule] = [
    _r("unicode", r"(?s).*", _unicode),
    # -- markdown / noise ---------------------------------------------------
    _r("code_fence", r"(?s)```.*?```", " "),
    _r("md_image", r"!\[([^\]]*)\]\([^)\s]*\)", lambda m: f" {m.group(1)} "),
    _r("md_link", r"\[([^\]]+)\]\([^)\s]*\)", lambda m: f" {m.group(1)} "),
    _r("url", r"(?i)\b(?:https?://|www\.)[^\s<>()\[\]\"']+", _url),
    _r("email", r"[\w.+-]+@[\w-]+(?:\.[\w-]+)+", _email),
    _r("hr", r"(?m)^[ \t]*(?:[-*_][ \t]*){3,}$", ""),
    _r("quote_marker", r"(?m)^[ \t]*>+[ \t]?", ""),
    _r("heading", r"(?m)^[ \t]{0,3}#{1,6}[ \t]*", ""),
    _r("bullet", r"(?m)^[ \t]*(?:[-*+•▪◦‣●○■□➤►▶–—]|[0-9]+[.)])[ \t]+", ""),
    _r("emphasis", r"(?s)(\*\*\*|\*\*|__|\*|~~)(?=\S)(.+?)(?<=\S)\1", lambda m: m.group(2)),
    _r("tilde", r"~(?=[ ]?[0-9])", _tilde),
    _r("md_chars", r"[*_#~`^|\\]", " "),
    _r("emoji", _EMOJI, " "),
    _r("newline", r"[ \t]*\r?\n[ \t\r\n]*", _newline),
    # -- abbreviations ------------------------------------------------------
    _r("abbr", rf"(?<!\w)({_ABBR_RE}){_SUFFIX}(?!\w)", _abbr),
    _r("abbr_dotted", rf"(?<!\w)({_DOTTED_RE})(?!\w)", _dotted, re.IGNORECASE),
    _r("numero", r"№\s*", " дугаар "),
    # -- dates, phones, ranges, times --------------------------------------
    _r("date_ymd", r"(?<![0-9.\-/])([0-9]{4})[-./]([0-9]{1,2})[-./]([0-9]{1,2})(?![0-9])", _date_ymd),
    _r("date_dmy", r"(?<![0-9.\-/])([0-9]{1,2})[-./]([0-9]{1,2})[-./]([0-9]{4})(?![0-9])", _date_dmy),
    _r("date_md", r"(?<![0-9.\-/])([0-9]{1,2})/([0-9]{1,2})(?![0-9./\-])", _date_md),
    _r("phone_intl", rf"(?<![\w+])\+[ ]?976[ -]?[0-9]{{4}}[ -]?[0-9]{{4}}{_PHONE_GUARD}", _phone),
    _r("phone_plus", rf"(?<![\w+])\+[0-9]{{1,3}}(?:[ -]?[0-9]{{2,4}}){{2,4}}{_PHONE_GUARD}", _phone),
    _r("phone_group", rf"(?<![0-9.,\-])(?:[6-9]|11)[0-9]{{2}}[ -][0-9]{{4}}{_PHONE_GUARD}", _phone),
    _r("phone_digits", rf"(?<![\w.,\-])[0-9]{{8,}}{_PHONE_GUARD}", _phone),
    _r("range", r"(?<=[0-9])(?:-|\s*[–—]\s*)(?=[0-9])", ", "),
    _r(
        "time",
        r"(?<![0-9:])([01]?[0-9]|2[0-3]):([0-5][0-9])(?::([0-5][0-9]))?(?![0-9:])",
        _time,
    ),
    # -- money / percent / shorthand ---------------------------------------
    _r("money_suffix", rf"(?<![0-9.,])({NUM}(?:\.[0-9]+)?)\s*({_CUR_TAIL})(?!\w)", _money_suffix),
    _r("money_prefix", rf"({_CUR_USD}|{_CUR_EUR}|₮)\s*({NUM}(?:\.[0-9]+)?)", _money_prefix),
    _r("percent", rf"(?<![0-9.,])({NUM}(?:\.[0-9]+)?)\s*%", _percent),
    _r("percent_alone", r"%", " хувь "),
    _r("thousands_k", r"(?<![\w.,])([0-9]{1,3})[кКkK](?![\w])", _thousands_k),
    _r("tugrik_alone", r"₮", " төгрөг "),
    # -- units, ordinals, decimals, integers -------------------------------
    _r("unit", rf"(?<=[0-9])\s*({_UNIT_RE})(?![\w])", _unit),
    _r("ordinal", ORDINAL_RE.pattern, ordinal_sub),
    _r("decimal", DECIMAL_RE.pattern, decimal_sub),
    _r("sign_minus", r"(?<![\w)\]])-(?=[0-9])", " хасах "),
    _r("sign_plus", r"(?<![\w)\]])\+(?=[0-9])", " нэмэх "),
    _r("integer", INT_RE.pattern, int_sub),
    # -- symbols and punctuation -------------------------------------------
    _r("symbols", r"[&=×°]|\+", lambda m: {
        "&": " ба ", "=": " тэнцүү ", "×": " үржих ", "°": " градус ", "+": " нэмэх ",
    }[m.group(0)]),
    _r("slash", r"[/<>]", " "),
    _r("dash", r"(?:\s[-–—]+\s|[–—])", ", "),
    _r("quotes", r"[\"“”«»„‟]|(?<!\w)['‘’]|['‘’](?!\w)", ""),
    _r("brackets", r"[()\[\]{}]", ", "),
    _r("superscripts", r"[¹²³⁰-⁹]", " "),
    # -- cleanup ------------------------------------------------------------
    _r("collapse_ws", r"\s+", " "),
    _r("space_before_punct", r" +([,.;:!?…])", lambda m: m.group(1)),
    _r("dup_pause", r"([,;:])(?: *[,;:])+", lambda m: m.group(1)),
    _r("pause_before_stop", r"[,;:] *([.!?…])", lambda m: m.group(1)),
    _r("stop_then_pause", r"([.!?…]) *[,;:]+", lambda m: m.group(1)),
    _r("dup_stop", r"\.{2,}", "…"),
    _r("dup_mark", r"([!?…])\1+", lambda m: m.group(1)),
    _r("mixed_stop", r"([!?])[.!?]+", lambda m: m.group(1)),
    _r("space_after_pause", r"([,;:!?])(?=[^\s,;:!?.…])", lambda m: m.group(1) + " "),
    _r("leading_punct", r"\A[\s,;:.\-]+", ""),
    _r("final_punct", r"(?s)\A.*\Z", _final_punct),
]
