from __future__ import annotations

import io

import numpy as np

from callgo_agent import audio_utils as au


def test_wav_roundtrip_bytes_and_file(tmp_path):
    pcm = au.synth_tone(300, 0.25, 16000)
    p = tmp_path / "t.wav"
    au.write_wav(p, pcm, 16000)
    back, rate = au.read_wav(p)
    assert rate == 16000
    assert np.array_equal(back, pcm)
    back2, _ = au.read_wav(au.wav_bytes(pcm, 16000))
    assert np.array_equal(back2, pcm)
    back3, _ = au.read_wav(io.BytesIO(au.wav_bytes(pcm, 16000)))
    assert np.array_equal(back3, pcm)


def test_read_wav_resamples_and_downmixes(tmp_path):
    import wave

    mono = au.synth_tone(440, 0.5, 8000)
    stereo = np.stack([mono, mono], axis=1).reshape(-1)
    p = tmp_path / "st.wav"
    with wave.open(str(p), "wb") as wf:
        wf.setnchannels(2)
        wf.setsampwidth(2)
        wf.setframerate(8000)
        wf.writeframes(stereo.astype("<i2").tobytes())
    pcm, rate = au.read_wav(p, target_rate=16000)
    assert rate == 16000
    assert abs(pcm.size - 8000) <= 1
    assert abs(au.rms(pcm) - au.rms(mono)) < 0.01


def test_resample_preserves_duration_and_tone_energy():
    pcm = au.synth_tone(440, 1.0, 16000)
    down = au.resample_linear(pcm, 16000, 8000)
    up = au.resample_linear(down, 8000, 24000)
    assert down.size == 8000
    assert up.size == 24000
    assert down.dtype == np.int16
    assert abs(au.rms(down) - au.rms(pcm)) < 0.02
    assert np.array_equal(au.resample_linear(pcm, 16000, 16000), pcm)


def test_frames_roundtrip():
    pcm = au.synth_tone(500, 0.105, 16000)  # not a multiple of 20 ms
    frames = list(au.frames_from_pcm(pcm, 16000, 20))
    assert all(f.samples_per_channel == 320 and f.sample_rate == 16000 for f in frames)
    assert len(frames) == 6
    back = au.pcm_from_frames(frames)
    assert np.array_equal(back[: pcm.size], pcm)
    assert not back[pcm.size :].any()  # zero padding only
    merged = au.combine_frames(frames)
    assert merged.samples_per_channel == back.size


def test_segment_utterances_finds_bursts():
    sr = 16000
    pcm = au.synth_utterances(3, sr, speech_s=0.6, gap_s=0.7, lead_s=0.3)
    segs = au.segment_utterances(pcm, sr, threshold=0.02, min_silence_ms=400)
    assert len(segs) == 3
    for s in segs:
        assert 0.5 < (s.end - s.start) / sr < 0.8
        assert au.rms(s.slice(pcm)) > 0.05
    # gaps shorter than min_silence merge everything into one utterance
    assert len(au.segment_utterances(pcm, sr, 0.02, min_silence_ms=1000)) == 1


def test_segment_utterances_edge_cases():
    sr = 16000
    assert au.segment_utterances(np.zeros(0, dtype=np.int16), sr) == []
    assert au.segment_utterances(np.zeros(sr, dtype=np.int16), sr) == []
    click = np.zeros(sr, dtype=np.int16)
    click[8000:8100] = 20000
    assert au.segment_utterances(click, sr, min_speech_ms=100) == []


def test_fixture_is_small(tmp_path):
    p = au.write_fixture_wav(tmp_path / "f.wav", 2, 8000)
    assert p.stat().st_size < 100_000
