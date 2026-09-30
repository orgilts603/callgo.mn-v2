from __future__ import annotations

import json

import numpy as np

from callgo_agent import audio_utils as au
from callgo_agent import harness as h


def _wav(tmp_path, n=2):
    p = tmp_path / "in.wav"
    au.write_wav(p, au.synth_utterances(n, 16000), 16000)
    return p


async def test_run_stt_stage():
    pcm = au.synth_utterances(2, 16000)
    text, n, ms = await h.run_stt(h.FakeSTT("сайн уу"), pcm, 16000, "mn")
    assert text == "сайн уу"  # fake returns text once, "" for later utterances
    assert n == 2 and ms >= 0


async def test_run_stt_resamples_input():
    pcm = au.synth_utterances(1, 8000)
    text, n, _ = await h.run_stt(h.FakeSTT("x"), pcm, 8000)
    assert (text, n) == ("x", 1)


async def test_run_llm_stage_streams():
    reply, first, total = await h.run_llm(h.EchoLLM(delay_s=0.01), "sys", "hello")
    assert reply == "Та хэлсэн: hello."
    assert 0 < first <= total


async def test_run_tts_stage():
    pcm, rate, first, total = await h.run_tts(h.BeepTTS(sample_rate=16000), "hello there")
    assert rate == 16000
    assert pcm.size > 16000 * 0.3 * 0.9
    assert au.rms(pcm) > 0.05
    assert 0 < first <= total


async def test_pipeline_text_only_skips_stt():
    res = await h.run_pipeline(
        stt=None, llm=h.EchoLLM(), tts=h.BeepTTS(), pcm=None, sample_rate=16000, text="сайн"
    )
    assert res.timings.stt_ms == 0.0
    assert res.reply.endswith("сайн.")
    assert res.audio.size > 0


async def test_pipeline_empty_transcript_stops_early():
    res = await h.run_pipeline(
        stt=h.FakeSTT(""),
        llm=h.EchoLLM(),
        tts=h.BeepTTS(),
        pcm=np.zeros(16000, dtype=np.int16),
        sample_rate=16000,
    )
    assert res.reply == "" and res.audio.size == 0


def test_fake_end_to_end_writes_wav_and_timings(tmp_path, capsys):
    out = tmp_path / "out.wav"
    args = h.parse_args(
        ["--fake", "--wav", str(_wav(tmp_path)), "--text", "Сайн байна уу", "--out", str(out)]
    )
    import asyncio

    res = asyncio.run(h.run(args))
    assert out.exists() and out.stat().st_size > 1000
    pcm, rate = au.read_wav(out)
    assert rate == res.sample_rate and pcm.size == res.audio.size
    t = res.timings.as_dict()
    assert set(t) == {
        "stt_ms",
        "llm_first_token_ms",
        "llm_total_ms",
        "tts_first_audio_ms",
        "tts_total_ms",
        "total_ms",
    }
    assert t["total_ms"] >= t["llm_total_ms"] > 0
    assert res.raw_transcript == "Сайн байна уу"
    assert "TOTAL" in res.report()


def test_cli_main_fake_without_wav(tmp_path, capsys):
    out = tmp_path / "o.wav"
    assert h.main(["--fake", "--json", "--out", str(out)]) == 0
    payload = json.loads(capsys.readouterr().out)
    assert payload["timings"]["total_ms"] > 0 and payload["reply"]
    assert out.exists()


def test_load_profile_variants(tmp_path):
    from callgo_agent.mock_backend import build_bootstrap

    profile, llm, fallbacks, lexicon = h.load_profile(None)
    assert profile.language == "mn" and llm is not None and lexicon

    b = build_bootstrap({})
    p = tmp_path / "b.json"
    p.write_text(json.dumps(b.model_dump(mode="json", by_alias=True)), encoding="utf-8")
    profile2, llm2, fb2, lex2 = h.load_profile(str(p))
    assert profile2.id == b.profile.id and llm2 is not None and fb2 == [] and lex2

    p2 = tmp_path / "p.json"
    p2.write_text(json.dumps(b.profile.model_dump(mode="json", by_alias=True)), encoding="utf-8")
    assert h.load_profile(str(p2))[0].name == b.profile.name
