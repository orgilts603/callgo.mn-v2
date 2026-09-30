package audio

import (
	"fmt"
	"io"
	"sync"
)

const (
	// DefaultRecorderMaxSkewMs is the default for Recorder.MaxSkewMs.
	DefaultRecorderMaxSkewMs = 5000
	// recorderGapToleranceMs is how far a chunk timestamp may run ahead of the
	// end of the previous chunk before the difference is treated as a real
	// silence gap (smaller differences are timestamp jitter).
	recorderGapToleranceMs = 5
)

// Recorder mixes the two legs of a call into a stereo WAV, customer on the
// left channel and agent on the right, aligned by capture timestamp.
//
// Each leg is mono PCM pushed in chunks with a millisecond timestamp for the
// first sample of the chunk (any common clock, e.g. Unix ms or ms since call
// start) and its own sample rate. Legs are resampled to the recorder's rate.
// Consecutive chunks whose timestamps agree with the chunk durations are
// resampled as one continuous stream (no clicks at chunk boundaries); a
// timestamp that jumps ahead by more than a few milliseconds inserts silence
// for the gap; a timestamp that is behind (overlap or reorder) is ignored and
// the chunk is appended contiguously.
//
// Time zero is the earliest timestamp seen before the first samples were
// written; later data older than that is trimmed. Output is written
// incrementally, so memory use is bounded by the skew between the legs: if one
// leg is more than MaxSkewMs ahead of the other, the lagging leg is padded
// with silence up to that point. Recorder is safe for concurrent use.
type Recorder struct {
	// MaxSkewMs bounds buffering between the legs. It defaults to
	// DefaultRecorderMaxSkewMs; set it before the first Push.
	MaxSkewMs int64

	mu      sync.Mutex
	w       *WAVWriter
	rate    int
	started bool
	origin  int64
	legs    [2]recLeg // 0 customer, 1 agent
	closed  bool
	scratch []int16
}

// recLeg is one mono leg of the recording at the target rate.
type recLeg struct {
	target  int
	flushed int64   // absolute position of pending[0]
	pending []int16 // resampled, not yet written samples

	rs       *Resampler // active continuous segment, nil between segments
	srcRate  int
	segStart int64 // absolute output position where the segment began
	segIn    int64 // input samples consumed by the segment
}

func (l *recLeg) end() int64 { return l.flushed + int64(len(l.pending)) }

func (l *recLeg) padTo(pos int64) {
	if n := pos - l.end(); n > 0 {
		l.pending = append(l.pending, make([]int16, n)...)
	}
}

// closeSegment flushes the resampler tail and ends the continuous segment.
func (l *recLeg) closeSegment() {
	if l.rs == nil {
		return
	}
	l.pending = append(l.pending, l.rs.Flush()...)
	l.rs = nil
}

func (l *recLeg) push(start int64, pcm []int16, rate int) error {
	if l.rs != nil {
		expected := l.segStart + l.segIn*int64(l.target)/int64(l.srcRate)
		tol := int64(recorderGapToleranceMs) * int64(l.target) / 1000
		if rate != l.srcRate || start-expected > tol {
			l.closeSegment()
		}
	}
	if l.rs == nil {
		rs, err := NewResampler(rate, l.target)
		if err != nil {
			return err
		}
		l.padTo(start)
		l.rs, l.srcRate, l.segStart, l.segIn = rs, rate, l.end(), 0
	}
	l.pending = append(l.pending, l.rs.Process(pcm)...)
	l.segIn += int64(len(pcm))
	return nil
}

// NewRecorder starts a stereo WAV recording at the given sample rate on w.
func NewRecorder(w io.WriteSeeker, rate int) (*Recorder, error) {
	ww, err := NewWAVWriter(w, rate, 2)
	if err != nil {
		return nil, err
	}
	r := &Recorder{MaxSkewMs: DefaultRecorderMaxSkewMs, w: ww, rate: rate}
	r.legs[0].target, r.legs[1].target = rate, rate
	return r, nil
}

// PushCustomer adds mono PCM (sampled at rate Hz) captured at ts ms to the
// left channel.
func (r *Recorder) PushCustomer(ts int64, pcm []int16, rate int) error {
	return r.push(0, ts, pcm, rate)
}

// PushAgent adds mono PCM (sampled at rate Hz) captured at ts ms to the right
// channel.
func (r *Recorder) PushAgent(ts int64, pcm []int16, rate int) error {
	return r.push(1, ts, pcm, rate)
}

// Frames returns the number of stereo frames written to the WAV so far.
func (r *Recorder) Frames() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.w.Frames()
}

func (r *Recorder) push(leg int, ts int64, pcm []int16, rate int) error {
	if rate <= 0 {
		return fmt.Errorf("%w: %d", ErrInvalidRate, rate)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrClosed
	}
	if len(pcm) == 0 {
		return nil
	}
	if !r.started {
		r.started, r.origin = true, ts
	}
	if ts < r.origin {
		if r.w.Frames() == 0 && r.legs[0].flushed == 0 {
			r.shiftOrigin(ts)
		} else {
			// Already written past this point: trim the head that is too old.
			trim := ceilDiv((r.origin-ts)*int64(rate), 1000)
			if trim >= int64(len(pcm)) {
				return nil
			}
			pcm, ts = pcm[trim:], r.origin
		}
	}
	start := (ts - r.origin) * int64(r.rate) / 1000
	if err := r.legs[leg].push(start, pcm, rate); err != nil {
		return err
	}
	return r.drain()
}

// shiftOrigin moves time zero earlier to ts, prepending silence to both legs.
func (r *Recorder) shiftOrigin(ts int64) {
	n := int((r.origin - ts) * int64(r.rate) / 1000)
	for i := range r.legs {
		l := &r.legs[i]
		l.pending = append(make([]int16, n, n+len(l.pending)), l.pending...)
		l.segStart += int64(n)
	}
	r.origin = ts
}

// drain writes every sample position both legs have produced.
func (r *Recorder) drain() error {
	c, a := &r.legs[0], &r.legs[1]
	skew := r.MaxSkewMs * int64(r.rate) / 1000
	switch {
	case c.end()-a.end() > skew:
		a.closeSegment()
		a.padTo(c.end() - skew)
	case a.end()-c.end() > skew:
		c.closeSegment()
		c.padTo(a.end() - skew)
	}
	return r.writeUpTo(min(c.end(), a.end()))
}

func (r *Recorder) writeUpTo(pos int64) error {
	c, a := &r.legs[0], &r.legs[1]
	n := int(pos - c.flushed)
	if n <= 0 {
		return nil
	}
	const block = 4096
	for off := 0; off < n; off += block {
		m := min(block, n-off)
		r.scratch = r.scratch[:0]
		for i := range m {
			r.scratch = append(r.scratch, c.pending[off+i], a.pending[off+i])
		}
		if err := r.w.Write(r.scratch); err != nil {
			return err
		}
	}
	c.pending = append(c.pending[:0], c.pending[n:]...)
	a.pending = append(a.pending[:0], a.pending[n:]...)
	c.flushed += int64(n)
	a.flushed += int64(n)
	return nil
}

// Close flushes both legs, pads the shorter one with silence so both channels
// have equal length, and finalises the WAV header. The underlying writer is
// not closed. Close is idempotent.
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	c, a := &r.legs[0], &r.legs[1]
	c.closeSegment()
	a.closeSegment()
	total := max(c.end(), a.end())
	c.padTo(total)
	a.padTo(total)
	werr := r.writeUpTo(total)
	cerr := r.w.Close()
	if werr != nil {
		return werr
	}
	return cerr
}
