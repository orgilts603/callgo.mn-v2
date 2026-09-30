"""normalize_stt and apply_lexicon."""

from __future__ import annotations

import pytest

from callgo_agent.normalizer import apply_lexicon, normalize_stt
from callgo_agent.schemas import LexiconEntry, LexiconScope

CLEAN = [
    ("  сайн   байна уу ", "сайн байна уу"),
    ("сайн байна уу ,", "сайн байна уу"),
    ("сайн байна уу ?", "сайн байна уу?"),
    ("сайн ,байна уу", "сайн, байна уу"),
    ("сайн байна уу!!!", "сайн байна уу!"),
    ("сайн байна уу....", "сайн байна уу..."),
    (",, сайн байна уу", "сайн байна уу"),
    ("сайн *** байна # уу", "сайн байна уу"),
    ("сайн\u200b байна", "сайн байна"),
    ("сайн\u00a0байна", "сайн байна"),
    ("байна.Өнөөдөр", "байна. Өнөөдөр"),
    ("", ""),
    # Latin look-alikes inside Cyrillic words
    ("Cайн бaйна уу", "Сайн байна уу"),
    ("хoрин тaв", "хорин тав"),
    ("xэдэн cар", "хэдэн сар"),
    ("Улаанбаатаp", "Улаанбаатар"),
    ("ok hello", "ok hello"),  # purely Latin stays
    ("Айfон", "Айfон"),  # non look-alike Latin letter: leave alone
    # case fixes
    ("сАйн байна", "сайн байна"),
    ("СайН", "Сайн"),
    ("САЙН БАЙНА УУ", "Сайн байна уу"),
    ("УБ", "УБ"),
    ("ХХК-ийн", "ХХК-ийн"),
]


@pytest.mark.parametrize(("src", "out"), CLEAN)
def test_normalize_stt(src: str, out: str) -> None:
    text, hits = normalize_stt(src, [])
    assert text == out
    assert hits == []


def _e(wrong: str, correct: str, scope: LexiconScope = LexiconScope.BOTH, phonetic: str = "") -> LexiconEntry:
    return LexiconEntry(wrong=wrong, correct=correct, scope=scope, phonetic=phonetic)


def test_stt_lexicon_hits_and_case() -> None:
    lex = [
        _e("улаанбатар", "Улаанбаатар", LexiconScope.STT),
        _e("хаан банк", "Хаан банк", LexiconScope.BOTH),
        _e("гол", "гол", LexiconScope.STT),  # no-op entry never counts as a hit
        _e("тэлэ", "теле", LexiconScope.TTS),  # wrong scope
    ]
    text, hits = normalize_stt("би улаанбатар, ХААН БАНК болон хаан банк гэж", lex)
    assert text == "би Улаанбаатар, ХААН БАНК болон Хаан банк гэж"
    assert [h.wrong for h in hits] == ["улаанбатар", "хаан банк"]


def test_stt_lexicon_capitalised_match() -> None:
    lex = [_e("банк", "банк", LexiconScope.STT), _e("данс", "дансны", LexiconScope.BOTH)]
    text, hits = normalize_stt("Данс нээх", lex)
    assert text == "Дансны нээх"
    assert len(hits) == 1


def test_lexicon_whole_word_only() -> None:
    lex = [_e("тав", "5")]
    text, hits = apply_lexicon("тавь тав таван тав.", lex, LexiconScope.STT)
    assert text == "тавь 5 таван 5."
    assert len(hits) == 1


def test_lexicon_no_chaining_and_longest_first() -> None:
    lex = [_e("а", "б"), _e("б", "в"), _e("сайн уу", "сайн байна уу"), _e("сайн", "муу")]
    text, _ = apply_lexicon("а б сайн уу сайн", lex, LexiconScope.STT)
    assert text == "б в сайн байна уу муу"


def test_lexicon_scopes_tts_prefers_phonetic() -> None:
    lex = [
        _e("Wi-Fi", "вайфай", LexiconScope.TTS, phonetic="вай фай"),
        _e("ХААН", "Хаан", LexiconScope.STT),
        _e("AI", "ай", LexiconScope.BOTH),
    ]
    text, hits = apply_lexicon("wi-fi ХААН AI", lex, LexiconScope.TTS)
    assert text == "вай фай ХААН ай"
    assert [h.wrong for h in hits] == ["Wi-Fi", "AI"]
    text, hits = apply_lexicon("wi-fi ХААН AI", lex, LexiconScope.STT)
    assert text == "wi-fi ХААН АЙ"


def test_lexicon_multiword_across_whitespace_and_empty() -> None:
    lex = [_e("хаан  банк", "Хаан Банк"), _e("", "x")]
    text, hits = apply_lexicon("хаан\nбанк", lex, LexiconScope.STT)
    assert text == "Хаан Банк"
    assert len(hits) == 1
    assert apply_lexicon("", lex, LexiconScope.STT) == ("", [])
    assert apply_lexicon("текст", [], LexiconScope.STT) == ("текст", [])


def test_lexicon_accepts_string_scope() -> None:
    text, _ = apply_lexicon("а", [_e("а", "б")], "tts")
    assert text == "б"
