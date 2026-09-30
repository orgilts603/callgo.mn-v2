"""normalize_for_tts: every rule, lexicon at the end, idempotence, properties."""

from __future__ import annotations

import random
import re
import time

import pytest

from callgo_agent.normalizer import normalize_for_tts
from callgo_agent.normalizer.rules import TTS_RULES, apply_rules
from callgo_agent.schemas import LexiconEntry, LexiconScope

CASES = [
    # dates
    ("2026-09-30", "хоёр мянга хорин зургаан оны есдүгээр сарын гуч."),
    ("2026.09.30", "хоёр мянга хорин зургаан оны есдүгээр сарын гуч."),
    ("2026/1/5", "хоёр мянга хорин зургаан оны нэгдүгээр сарын тав."),
    ("30.09.2026", "хоёр мянга хорин зургаан оны есдүгээр сарын гуч."),
    ("9/30", "есдүгээр сарын гуч."),
    ("12/25 болно", "арван хоёрдугаар сарын хорин тав болно."),
    ("Уулзалт 2026-10-11 өдөр", "Уулзалт хоёр мянга хорин зургаан оны аравдугаар сарын арван нэг өдөр."),
    # times
    ("14:30", "арван дөрвөн цаг гучин минут."),
    ("14:00", "арван дөрвөн цаг."),
    ("9:05", "есөн цаг таван минут."),
    ("14:00 цагт уулзъя", "арван дөрвөн цагт уулзъя."),
    ("14:30 цагт уулзъя", "арван дөрвөн цаг гучин минутад уулзъя."),
    ("10:00-11:30", "арван цаг, арван нэг цаг гучин минут."),
    # currency
    ("15000₮", "арван таван мянган төгрөг."),
    ("15,000 төг", "арван таван мянган төгрөг."),
    ("15 000 MNT", "арван таван мянган төгрөг."),
    ("15000 төгрөг", "арван таван мянган төгрөг."),
    ("₮5000", "таван мянган төгрөг."),
    ("1000₮", "мянган төгрөг."),
    ("100$", "зуун доллар."),
    ("$20", "хорин доллар."),
    ("50 USD", "тавин доллар."),
    ("30€", "гучин евро."),
    ("1,500.50₮", "мянга таван зуун төгрөг тавин мөнгө."),
    ("15к төгрөг", "арван таван мянган төгрөг."),
    # percent
    ("25%", "хорин таван хувь."),
    ("25 %", "хорин таван хувь."),
    ("12.5%", "арван хоёр цэг таван хувь."),
    ("100% баталгаа", "зуун хувь баталгаа."),
    # phones
    ("9911 2233", "ес ес нэг нэг, хоёр хоёр гурав гурав."),
    ("99112233", "ес ес нэг нэг, хоёр хоёр гурав гурав."),
    ("9911-2233", "ес ес нэг нэг, хоёр хоёр гурав гурав."),
    ("+976 9911 2233", "нэмэх ес долоо зургаа, ес ес нэг нэг, хоёр хоёр гурав гурав."),
    ("+97699112233", "нэмэх ес долоо зургаа, ес ес нэг нэг, хоёр хоёр гурав гурав."),
    ("Утас 88112233 байна", "Утас найм найм нэг нэг, хоёр хоёр гурав гурав байна."),
    ("1 234 567", "нэг сая хоёр зуун гучин дөрвөн мянга таван зуун жаран долоо."),
    # ordinals
    ("3-р", "гуравдугаар."),
    ("5-р", "тавдугаар."),
    ("9-р сар", "есдүгээр сар."),
    ("1-р", "нэгдүгээр."),
    ("21-р байр", "хорин нэгдүгээр байр."),
    ("2 дугаар", "хоёрдугаар."),
    # decimals / numbers with nouns
    ("3.5", "гурван цэг тав."),
    ("3.14", "гурван цэг арван дөрөв."),
    ("3.5 метр", "гурван цэг таван метр."),
    ("Би 3 хүн", "Би гурван хүн."),
    ("21", "хорин нэг."),
    ("100", "зуу."),
    ("1000", "мянга."),
    ("2500", "хоёр мянга таван зуу."),
    ("1,000,000", "нэг сая."),
    ("0", "тэг."),
    ("10-15 хүн", "арав, арван таван хүн."),
    ("-5 хэм", "хасах таван хэм."),
    ("5-т", "тавт."),
    ("A4 цаас", "A дөрвөн цаас."),
    ("1234567890123", "нэг хоёр гурав дөрөв, тав зургаа долоо найм, ес тэг нэг хоёр, гурав."),
    # abbreviations / units
    ("ХХК", "хязгаарлагдмал хариуцлагатай компани."),
    ("УБ хот", "Улаанбаатар хот."),
    ("УБ-т", "Улаанбаатарт."),
    ("ХХК-ийн", "хязгаарлагдмал хариуцлагатай компанийн."),
    ("шар, т.х", "шар, тэргүүтэй."),
    ("гэх мэт г.м.", "гэх мэт гэх мэт."),
    ("5 км", "таван километр."),
    ("5км", "таван километр."),
    ("70 кг", "далан килограмм."),
    ("5 м²", "таван квадрат метр."),
    ("60 км/ц", "жаран километр цаг."),
    ("25°C", "хорин таван градус цельс."),
    ("-5°C", "хасах таван градус цельс."),
    ("3 л", "гурван литр."),
    ("№5", "дугаар тав."),
    # markdown / emoji / urls / symbols
    ("**Сайн** байна уу", "Сайн байна уу."),
    ("# Гарчиг\nТекст", "Гарчиг. Текст."),
    ("- нэг\n- хоёр\n- гурав", "нэг. хоёр. гурав."),
    ("1. нэг\n2. хоёр", "нэг. хоёр."),
    ("`код` байна", "код байна."),
    ("[холбоос](https://example.com/x)", "холбоос."),
    ("Сайн байна уу 😀🎉", "Сайн байна уу."),
    ("https://www.callgo.mn/a?b=1", "callgo цэг mn."),
    ("Үзнэ үү https://callgo.mn.", "Үзнэ үү callgo цэг mn."),
    ("info@callgo.mn", "info эт callgo цэг mn."),
    ("Тайлбар (жишээ) байна", "Тайлбар, жишээ, байна."),
    ("«Сайн» гэв", "Сайн гэв."),
    ("А & Б", "А ба Б."),
    ("24/7", "хорин дөрөв долоо."),
    ("```py\nx = 1\n```\nдараа", "дараа."),
    # whitespace / punctuation
    ("  Сайн   байна\tуу  ", "Сайн байна уу."),
    ("Сайн байна уу ?", "Сайн байна уу?"),
    ("Сайн байна уу!!", "Сайн байна уу!"),
    ("Сайн байна уу,", "Сайн байна уу."),
    ("Сайн байна уу...", "Сайн байна уу…"),
    ("Сайн байна уу.", "Сайн байна уу."),
    ("", ""),
    ("   ", ""),
    ("😀", ""),
    ("...", ""),
]


@pytest.mark.parametrize(("src", "out"), CASES, ids=[c[0][:30] or "empty" for c in CASES])
def test_tts_rules(src: str, out: str) -> None:
    assert normalize_for_tts(src, []) == out


def test_case_count() -> None:
    assert len(CASES) >= 80


@pytest.mark.parametrize(("src", "_out"), CASES)
def test_idempotent(src: str, _out: str) -> None:
    once = normalize_for_tts(src, [])
    assert normalize_for_tts(once, []) == once


def test_rule_names_unique() -> None:
    names = [r.name for r in TTS_RULES]
    assert len(names) == len(set(names))
    assert {"date_ymd", "time", "percent", "phone_digits", "ordinal", "abbr"} <= set(names)


def test_apply_rules_individually() -> None:
    rule = next(r for r in TTS_RULES if r.name == "percent")
    assert rule.apply("7%").strip() == "долоон хувь"
    assert apply_rules("7%", [rule]).strip() == "долоон хувь"


def test_lexicon_after_rules() -> None:
    lex = [
        LexiconEntry(wrong="CallGo", correct="Колл Го", scope=LexiconScope.TTS),
        LexiconEntry(wrong="callgo", correct="колго", phonetic="колл го", scope=LexiconScope.TTS),
    ]
    assert normalize_for_tts("Сайн байна уу, CallGo байна", lex[:1]) == "Сайн байна уу, Колл Го байна."
    assert normalize_for_tts("Сайн, callgo", lex[1:]) == "Сайн, колл го."


def test_lexicon_scope_and_cyrillic() -> None:
    lex = [
        LexiconEntry(wrong="дэлгүүр", correct="дэлгүүр", phonetic="дэлгүүр", scope=LexiconScope.STT),
        LexiconEntry(wrong="Хаан банк", correct="Хаан банк", phonetic="хаан банк", scope=LexiconScope.BOTH),
    ]
    out = normalize_for_tts("ХААН БАНКны дэлгүүр. Хаан  Банк", lex)
    assert out == "ХААН БАНКны дэлгүүр. хаан банк."


def test_digits_removed_property() -> None:
    rng = random.Random(1234)
    pieces = [
        "{n}", "{n}-р", "{n}%", "{n}₮", "{n} төг", "+976 {n}", "{a}:{b}", "{y}-{m}-{d}",
        "{a}.{b}", "{a}/{b}", "{n} км", "{n} м²", "-{n}", "{n},{n}", "{n} {n}", "{n}-т",
        "№{n}", "{n}k", "{n}°C", "{n}.{n}.{n}", "1{n}2", "x{n}y",
    ]
    for _ in range(3000):
        n = rng.choice([0, 1, 7, 10, 25, 100, 999, 2026, 15000, 10**6, 10**9, 10**12, 10**15])
        n = n if rng.random() < 0.5 else rng.randrange(0, 10 ** rng.randrange(1, 16))
        ctx = {
            "n": n,
            "a": rng.randrange(0, 40),
            "b": rng.randrange(0, 70),
            "y": rng.randrange(1000, 3000),
            "m": rng.randrange(1, 15),
            "d": rng.randrange(1, 35),
        }
        text = " ".join(rng.choice(pieces).format(**ctx) for _ in range(rng.randrange(1, 4)))
        out = normalize_for_tts(text, [])
        assert not re.search(r"[0-9]", out), (text, out)
        assert normalize_for_tts(out, []) == out, (text, out)


def test_non_ascii_digits_removed() -> None:
    assert not re.search(r"\d", normalize_for_tts("٣ хүн ５ өдөр", []))


def test_performance_200_chars() -> None:
    text = "Сайн байна уу, таны захиалга 15,000₮ бөгөөд 2026-09-30 өдрийн 14:30 цагт 3-р давхарт 9911 2233 руу залгана."
    text = (text * 2)[:200]
    normalize_for_tts(text, [])  # warm caches
    start = time.perf_counter()
    for _ in range(50):
        normalize_for_tts(text, [])
    per_call_ms = (time.perf_counter() - start) / 50 * 1000
    assert per_call_ms < 5, per_call_ms  # generous CI bound; ~1ms locally
