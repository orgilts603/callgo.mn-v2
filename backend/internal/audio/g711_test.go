package audio

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMuLawKnownVectors(t *testing.T) {
	enc := map[int16]byte{
		0: 0xFF, 32767: 0x80, -32768: 0x00, -1: 0x7F, 1: 0xFF, 8: 0xFE, 132: 0xEF,
	}
	for pcm, want := range enc {
		assert.Equalf(t, []byte{want}, MuLawEncode([]int16{pcm}), "encode %d", pcm)
	}
	dec := map[byte]int16{
		0xFF: 0, 0x7F: 0, 0x80: 32124, 0x00: -32124, 0xFE: 8, 0xEF: 132, 0x7E: -8,
	}
	for b, want := range dec {
		assert.Equalf(t, []int16{want}, MuLawDecode([]byte{b}), "decode %#02x", b)
	}
}

func TestALawKnownVectors(t *testing.T) {
	enc := map[int16]byte{
		0: 0xD5, -1: 0x55, 32767: 0xAA, -32768: 0x2A, 8: 0xD4, 264: 0xC5,
	}
	for pcm, want := range enc {
		assert.Equalf(t, []byte{want}, ALawEncode([]int16{pcm}), "encode %d", pcm)
	}
	dec := map[byte]int16{
		0xD5: 8, 0x55: -8, 0xAA: 32256, 0x2A: -32256, 0xD4: 24, 0xC5: 264,
	}
	for b, want := range dec {
		assert.Equalf(t, []int16{want}, ALawDecode([]byte{b}), "decode %#02x", b)
	}
}

func TestG711CodeRoundTrip(t *testing.T) {
	all := make([]byte, 256)
	for i := range all {
		all[i] = byte(i)
	}
	// Every A-law code is canonical.
	assert.Equal(t, all, ALawEncode(ALawDecode(all)))
	// Mu-law has two zero codes (0x7F = -0, 0xFF = +0); everything else is exact.
	got := MuLawEncode(MuLawDecode(all))
	for i := range all {
		if i == 0x7F {
			assert.Equal(t, byte(0xFF), got[i])
			continue
		}
		assert.Equalf(t, all[i], got[i], "code %#02x", i)
	}
}

func TestG711ErrorBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		rt   func(int16) int16
	}{
		{"mulaw", func(s int16) int16 { return MuLawDecode(MuLawEncode([]int16{s}))[0] }},
		{"alaw", func(s int16) int16 { return ALawDecode(ALawEncode([]int16{s}))[0] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for s := math.MinInt16; s <= math.MaxInt16; s++ {
				got := tc.rt(int16(s))
				bound := math.Abs(float64(s))/16 + 8
				require.LessOrEqualf(t, math.Abs(float64(got)-float64(s)), bound, "sample %d -> %d", s, got)
				// Sign is preserved for anything above the smallest step.
				if s > 16 {
					require.Greater(t, got, int16(0))
				}
				if s < -16 {
					require.Less(t, got, int16(0))
				}
			}
		})
	}
}

func TestG711MonotonicDecode(t *testing.T) {
	// Encoding a rising ramp must give a non-decreasing decoded ramp.
	prevM, prevA := int16(math.MinInt16), int16(math.MinInt16)
	for s := math.MinInt16; s <= math.MaxInt16; s++ {
		m := MuLawDecode(MuLawEncode([]int16{int16(s)}))[0]
		a := ALawDecode(ALawEncode([]int16{int16(s)}))[0]
		require.GreaterOrEqual(t, m, prevM, "mulaw at %d", s)
		require.GreaterOrEqual(t, a, prevA, "alaw at %d", s)
		prevM, prevA = m, a
	}
}

func TestG711SineSNR(t *testing.T) {
	x := sineWave(8000, 1000, 20000, 8000)
	assert.Greater(t, snrDB(x, MuLawDecode(MuLawEncode(x)), 0), 30.0)
	assert.Greater(t, snrDB(x, ALawDecode(ALawEncode(x)), 0), 30.0)
}

func TestG711Empty(t *testing.T) {
	assert.Empty(t, MuLawEncode(nil))
	assert.Empty(t, ALawDecode(nil))
}

func BenchmarkMuLawEncode(b *testing.B) {
	x := sineWave(8000, 440, 12000, 8000)
	b.SetBytes(int64(len(x)) * 2)
	for b.Loop() {
		MuLawEncode(x)
	}
}

func BenchmarkMuLawDecode(b *testing.B) {
	x := MuLawEncode(sineWave(8000, 440, 12000, 8000))
	b.SetBytes(int64(len(x)))
	for b.Loop() {
		MuLawDecode(x)
	}
}

func BenchmarkALawEncode(b *testing.B) {
	x := sineWave(8000, 440, 12000, 8000)
	b.SetBytes(int64(len(x)) * 2)
	for b.Loop() {
		ALawEncode(x)
	}
}

func BenchmarkALawDecode(b *testing.B) {
	x := ALawEncode(sineWave(8000, 440, 12000, 8000))
	b.SetBytes(int64(len(x)))
	for b.Loop() {
		ALawDecode(x)
	}
}
