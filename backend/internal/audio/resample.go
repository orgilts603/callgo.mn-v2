package audio

import (
	"errors"
	"fmt"
	"math"
)

const (
	// resampleZeroCrossings is the number of sinc zero crossings on each side
	// of the filter centre (at the lower of the two Nyquist rates).
	resampleZeroCrossings = 16
	// resampleKaiserBeta gives roughly 80 dB of stop-band attenuation.
	resampleKaiserBeta = 8.0
	// resampleRolloff keeps the cutoff slightly under the lower Nyquist
	// frequency so the transition band stays inside the output band.
	resampleRolloff = 0.95
	// maxPolyphaseUp bounds the number of filter phases (and table memory).
	maxPolyphaseUp = 4096
)

// ErrInvalidRate is returned for non-positive or absurd sample rates.
var ErrInvalidRate = errors.New("audio: invalid sample rate")

// Resampler converts a PCM stream from one sample rate to another using a
// windowed-sinc (Kaiser) polyphase FIR. It is stateful, so a stream can be fed
// in arbitrary chunks without discontinuities at chunk boundaries: feeding
// chunks to Process and then calling Flush yields exactly the same samples as
// a single Resample call on the concatenated input.
//
// The filter is symmetric and zero-delay: output sample n corresponds to input
// time n*from/to, with the stream assumed to be silent before its start. The
// last few output samples are only available after more input arrives or after
// Flush. A Resampler is not safe for concurrent use.
type Resampler struct {
	from, to    int
	up, down    int // to/gcd, from/gcd
	half        int // taps per phase = 2*half
	table       [][]float32
	passthrough bool

	buf     []float32 // input samples with absolute index base .. base+len-1
	base    int64
	n       int64 // next output index
	totalIn int64
}

// NewResampler creates a streaming resampler from fromRate to toRate (Hz).
// Any positive rate pair is accepted; 8000, 16000, 24000 and 48000 (in any
// combination) are the intended set.
func NewResampler(fromRate, toRate int) (*Resampler, error) {
	if fromRate <= 0 || toRate <= 0 {
		return nil, fmt.Errorf("%w: %d -> %d", ErrInvalidRate, fromRate, toRate)
	}
	r := &Resampler{from: fromRate, to: toRate}
	if fromRate == toRate {
		r.passthrough = true
		return r, nil
	}
	g := gcd(fromRate, toRate)
	r.up, r.down = toRate/g, fromRate/g
	if r.up > maxPolyphaseUp {
		return nil, fmt.Errorf("%w: %d -> %d needs %d filter phases (max %d)",
			ErrInvalidRate, fromRate, toRate, r.up, maxPolyphaseUp)
	}
	r.buildTable()
	r.Reset()
	return r, nil
}

// Reset discards all streaming state (history and pending tail).
func (r *Resampler) Reset() {
	r.n, r.totalIn = 0, 0
	if r.passthrough {
		return
	}
	r.buf = make([]float32, r.half-1)
	r.base = -int64(r.half - 1)
}

// Process consumes input samples and returns every output sample that can be
// computed so far. The returned slice is newly allocated.
func (r *Resampler) Process(in []int16) []int16 {
	if r.passthrough {
		out := make([]int16, len(in))
		copy(out, in)
		return out
	}
	for _, s := range in {
		r.buf = append(r.buf, float32(s))
	}
	r.totalIn += int64(len(in))
	return r.run(-1)
}

// Flush emits the remaining tail of the stream (as if followed by silence) and
// resets the resampler for reuse. After a stream of N input samples the total
// output length across Process and Flush is ceil(N*to/from).
func (r *Resampler) Flush() []int16 {
	if r.passthrough {
		return nil
	}
	limit := ceilDiv(r.totalIn*int64(r.up), int64(r.down))
	r.buf = append(r.buf, make([]float32, r.half)...)
	out := r.run(limit)
	r.Reset()
	return out
}

// run computes outputs while all taps are available and (if limit >= 0) the
// output index is below limit.
func (r *Resampler) run(limit int64) []int16 {
	up, down, half := int64(r.up), int64(r.down), int64(r.half)
	taps := 2 * r.half
	end := r.base + int64(len(r.buf))
	var out []int16
	if limit < 0 {
		// Estimate the number of outputs to avoid regrowth.
		avail := (end - half) * up / down
		if c := avail - r.n; c > 0 {
			out = make([]int16, 0, c)
		}
	}
	for limit < 0 || r.n < limit {
		num := r.n * down
		i0 := num / up
		if i0+half >= end {
			break
		}
		coef := r.table[num%up]
		src := r.buf[i0-half+1-r.base:][:taps]
		var acc float32
		for k, c := range coef {
			acc += c * src[k]
		}
		out = append(out, clampRound(acc))
		r.n++
	}
	// Drop history that no future output needs.
	if drop := (r.n*down/up - half + 1) - r.base; drop > 0 {
		if drop > int64(len(r.buf)) {
			drop = int64(len(r.buf))
		}
		r.buf = append(r.buf[:0], r.buf[drop:]...)
		r.base += drop
	}
	return out
}

func (r *Resampler) buildTable() {
	scale := math.Min(1, float64(r.to)/float64(r.from))
	fc := scale * resampleRolloff // cutoff in cycles per input sample is fc/2
	if r.to > r.from {
		fc = 1 * 0.98 // interpolation: pass (almost) the whole input band
	}
	halfWidth := resampleZeroCrossings / fc
	r.half = int(math.Ceil(halfWidth)) + 1
	taps := 2 * r.half
	i0Beta := besselI0(resampleKaiserBeta)
	r.table = make([][]float32, r.up)
	tmp := make([]float64, taps)
	for p := range r.table {
		frac := float64(p) / float64(r.up)
		var sum float64
		for k := range tmp {
			t := float64(k-r.half+1) - frac
			v := 0.0
			if x := t / halfWidth; math.Abs(x) < 1 {
				w := besselI0(resampleKaiserBeta*math.Sqrt(1-x*x)) / i0Beta
				v = fc * sinc(fc*t) * w
			}
			tmp[k] = v
			sum += v
		}
		row := make([]float32, taps)
		for k, v := range tmp {
			row[k] = float32(v / sum) // unity DC gain in every phase
		}
		r.table[p] = row
	}
}

// Resample converts pcm from fromRate to toRate in one call. The output has
// ceil(len(in)*toRate/fromRate) samples. Equal rates return a copy. Invalid
// rates return nil.
func Resample(in []int16, fromRate, toRate int) []int16 {
	r, err := NewResampler(fromRate, toRate)
	if err != nil {
		return nil
	}
	out := r.Process(in)
	return append(out, r.Flush()...)
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	px := math.Pi * x
	return math.Sin(px) / px
}

// besselI0 is the zeroth-order modified Bessel function of the first kind.
func besselI0(x float64) float64 {
	sum, term := 1.0, 1.0
	q := x * x / 4
	for k := 1; k < 64; k++ {
		term *= q / float64(k*k)
		sum += term
		if term < sum*1e-16 {
			break
		}
	}
	return sum
}

func clampRound(v float32) int16 {
	f := math.Floor(float64(v) + 0.5)
	switch {
	case f > math.MaxInt16:
		return math.MaxInt16
	case f < math.MinInt16:
		return math.MinInt16
	}
	return int16(f)
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func ceilDiv(a, b int64) int64 { return (a + b - 1) / b }
