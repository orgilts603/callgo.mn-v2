package audio

import (
	"fmt"
	"math"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testRates = []int{8000, 16000, 24000, 48000}

func TestResampleSineRoundTrip16k8k16k(t *testing.T) {
	const freq = 1000.0
	x := sineWave(16000, freq, 16000, 16000)
	down := Resample(x, 16000, 8000)
	require.Len(t, down, 8000)

	// Frequency survives the downsample: zero crossings and Goertzel peak.
	assert.InDelta(t, 2*freq, float64(zeroCrossings(down)), 4)
	peak := goertzelPower(down, 8000, freq)
	for _, other := range []float64{500, 800, 1200, 1500, 2000, 3000} {
		assert.Greater(t, peak, 1000*goertzelPower(down, 8000, other), "1 kHz vs %v Hz", other)
	}
	// Amplitude preserved within 0.1 dB.
	ratio := math.Sqrt(4 * peak / goertzelPower(x, 16000, freq)) // half as many samples
	assert.InDelta(t, 1.0, ratio, 0.012)

	up := Resample(down, 8000, 16000)
	require.Len(t, up, len(x))
	assert.InDelta(t, 2*freq, float64(zeroCrossings(up)), 4)
	snr := snrDB(x, up, 300)
	t.Logf("16k->8k->16k SNR: %.1f dB", snr)
	assert.Greater(t, snr, 30.0)
}

func TestResampleAllRatePairs(t *testing.T) {
	const freq = 1000.0
	for _, from := range testRates {
		for _, to := range testRates {
			t.Run(fmt.Sprintf("%d_%d", from, to), func(t *testing.T) {
				x := sineWave(from, freq, 12000, from/2)
				y := Resample(x, from, to)
				require.Len(t, y, to/2, "output length")
				want := sineWave(to, freq, 12000, to/2)
				snr := snrDB(want, y, to/40)
				assert.Greater(t, snr, 40.0, "SNR %.1f dB", snr)
			})
		}
	}
}

func TestResampleAntiAliasing(t *testing.T) {
	// 6 kHz at 16 kHz is above the 4 kHz Nyquist of an 8 kHz output and would
	// alias to 2 kHz without the low-pass.
	x := sineWave(16000, 6000, 16000, 16000)
	y := Resample(x, 16000, 8000)
	var rms float64
	for _, s := range y[100 : len(y)-100] {
		rms += float64(s) * float64(s)
	}
	rms = math.Sqrt(rms / float64(len(y)-200))
	assert.Less(t, 20*math.Log10(rms/(16000/math.Sqrt2)), -40.0, "alias energy must be >40 dB down")
}

func TestResampleDCAndSilence(t *testing.T) {
	for _, pair := range [][2]int{{16000, 8000}, {8000, 16000}, {8000, 48000}, {48000, 8000}, {24000, 16000}} {
		silence := Resample(make([]int16, 4000), pair[0], pair[1])
		for i, s := range silence {
			require.Zerof(t, s, "silence %v sample %d", pair, i)
		}
		dc := Resample(constPCM(10000, 4000), pair[0], pair[1])
		for _, s := range dc[100 : len(dc)-100] {
			require.InDeltaf(t, 10000, float64(s), 1, "DC %v", pair)
		}
	}
}

func TestResampleEdgeCases(t *testing.T) {
	assert.Nil(t, Resample([]int16{1, 2}, 0, 8000))
	assert.Nil(t, Resample([]int16{1, 2}, 8000, -1))
	assert.Empty(t, Resample(nil, 8000, 16000))
	same := []int16{1, 2, 3}
	out := Resample(same, 8000, 8000)
	assert.Equal(t, same, out)
	out[0] = 99
	assert.Equal(t, int16(1), same[0], "passthrough must copy")
	// Very short input still yields ceil(n*to/from) samples.
	assert.Len(t, Resample([]int16{1000}, 8000, 48000), 6)
	assert.Len(t, Resample([]int16{1000, 1000, 1000}, 16000, 8000), 2)
	_, err := NewResampler(44101, 8000)
	require.ErrorIs(t, err, ErrInvalidRate)
	// Arbitrary rational ratio.
	assert.Len(t, Resample(make([]int16, 44100), 44100, 16000), 16000)
}

func TestResampleClipping(t *testing.T) {
	x := sineWave(8000, 200, 32767, 8000)
	y := Resample(x, 8000, 48000) // overshoot must clamp, not wrap
	assert.Greater(t, snrDB(sineWave(48000, 200, 32767, 48000), y, 1000), 25.0)
}

func TestResamplerStreamingMatchesOneShot(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, pair := range [][2]int{{16000, 8000}, {8000, 16000}, {8000, 24000}, {48000, 16000}, {24000, 48000}} {
		x := make([]int16, 20011)
		for i := range x {
			x[i] = int16(8000*math.Sin(float64(i)*0.05) + rng.Float64()*2000)
		}
		want := Resample(x, pair[0], pair[1])

		r, err := NewResampler(pair[0], pair[1])
		require.NoError(t, err)
		var got []int16
		for pos := 0; pos < len(x); {
			n := min(1+rng.Intn(700), len(x)-pos) // includes 1-sample chunks
			got = append(got, r.Process(x[pos:pos+n])...)
			pos += n
		}
		got = append(got, r.Flush()...)
		require.Equalf(t, want, got, "streaming != one-shot for %v", pair)

		// The resampler is reusable after Flush.
		again := append(r.Process(x), r.Flush()...)
		require.Equal(t, want, again)
	}
}

func TestResamplerNoBoundaryClicks(t *testing.T) {
	x := sineWave(16000, 440, 16000, 16000)
	r, err := NewResampler(16000, 8000)
	require.NoError(t, err)
	var y []int16
	for pos := 0; pos < len(x); pos += 320 { // 20 ms chunks
		y = append(y, r.Process(x[pos:pos+320])...)
	}
	y = append(y, r.Flush()...)
	// A 440 Hz sine at 16000 amplitude changes by at most ~2*pi*440/8000*16000.
	maxStep := 2 * math.Pi * 440 / 8000 * 16000 * 1.05
	for i := 1; i < len(y); i++ {
		require.LessOrEqualf(t, math.Abs(float64(y[i])-float64(y[i-1])), maxStep, "click at %d", i)
	}
}

func BenchmarkResample(b *testing.B) {
	for _, pair := range [][2]int{{16000, 8000}, {8000, 16000}, {8000, 48000}, {48000, 8000}, {24000, 16000}} {
		x := sineWave(pair[0], 1000, 12000, pair[0]) // one second
		b.Run(fmt.Sprintf("%dto%d_1s", pair[0], pair[1]), func(b *testing.B) {
			b.SetBytes(int64(len(x)) * 2)
			for b.Loop() {
				Resample(x, pair[0], pair[1])
			}
		})
	}
}

func BenchmarkResamplerStream20ms(b *testing.B) {
	r, _ := NewResampler(16000, 8000)
	chunk := sineWave(16000, 1000, 12000, 320)
	b.SetBytes(int64(len(chunk)) * 2)
	for b.Loop() {
		r.Process(chunk)
	}
}
