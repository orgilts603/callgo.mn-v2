package audio

import (
	"math"
	"math/rand"
)

func sineWave(rate int, freq float64, amp float64, n int) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(math.Round(amp * math.Sin(2*math.Pi*freq*float64(i)/float64(rate))))
	}
	return out
}

// goertzelPower returns the power of pcm at freq (normalised so a sine of
// amplitude A gives roughly A^2/4 * n).
func goertzelPower(pcm []int16, rate int, freq float64) float64 {
	w := 2 * math.Pi * freq / float64(rate)
	coeff := 2 * math.Cos(w)
	var s1, s2 float64
	for _, x := range pcm {
		s0 := float64(x) + coeff*s1 - s2
		s2, s1 = s1, s0
	}
	return s1*s1 + s2*s2 - coeff*s1*s2
}

func zeroCrossings(pcm []int16) int {
	n := 0
	for i := 1; i < len(pcm); i++ {
		if (pcm[i-1] < 0) != (pcm[i] < 0) {
			n++
		}
	}
	return n
}

// snrDB compares got against want over [skip, len-skip).
func snrDB(want, got []int16, skip int) float64 {
	var sig, noise float64
	for i := skip; i < len(want)-skip && i < len(got); i++ {
		w := float64(want[i])
		d := float64(got[i]) - w
		sig += w * w
		noise += d * d
	}
	if noise == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(sig/noise)
}

func noiseBurst(rng *rand.Rand, n int, amp float64) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16((rng.Float64()*2 - 1) * amp)
	}
	return out
}

func constPCM(v int16, n int) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = v
	}
	return out
}
