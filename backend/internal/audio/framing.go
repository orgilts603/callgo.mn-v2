package audio

import (
	"math"
	"sync"
)

// Chunker accepts PCM of arbitrary length and yields fixed-size frames, e.g.
// 20 ms frames for an RTP-style pipeline. It is not safe for concurrent use.
type Chunker struct {
	frame int
	buf   []int16
}

// NewChunker returns a Chunker that yields frames of frameSamples samples
// (per channel-interleaved unit; for stereo pass frames*2). frameSamples must
// be positive; otherwise it is treated as 1.
func NewChunker(frameSamples int) *Chunker {
	if frameSamples < 1 {
		frameSamples = 1
	}
	return &Chunker{frame: frameSamples}
}

// NewChunkerMs returns a Chunker for frames of frameMs milliseconds of mono
// audio at the given sample rate (20 ms at 8000 Hz = 160 samples).
func NewChunkerMs(rate, frameMs int) *Chunker {
	return NewChunker(rate * frameMs / 1000)
}

// FrameSize returns the frame length in samples.
func (c *Chunker) FrameSize() int { return c.frame }

// Buffered returns the number of samples held back waiting for a full frame.
func (c *Chunker) Buffered() int { return len(c.buf) }

// Write appends pcm and returns every complete frame now available. Returned
// frames are independent slices that the caller may retain.
func (c *Chunker) Write(pcm []int16) [][]int16 {
	c.buf = append(c.buf, pcm...)
	n := len(c.buf) / c.frame
	if n == 0 {
		return nil
	}
	backing := make([]int16, n*c.frame)
	copy(backing, c.buf)
	frames := make([][]int16, n)
	for i := range frames {
		frames[i] = backing[i*c.frame : (i+1)*c.frame : (i+1)*c.frame]
	}
	c.buf = append(c.buf[:0], c.buf[n*c.frame:]...)
	return frames
}

// Flush returns the buffered remainder, zero-padded to a full frame when pad is
// true (nil if nothing is buffered), and empties the Chunker.
func (c *Chunker) Flush(pad bool) []int16 {
	if len(c.buf) == 0 {
		return nil
	}
	out := make([]int16, len(c.buf))
	copy(out, c.buf)
	if pad {
		out = append(out, make([]int16, c.frame-len(out))...)
	}
	c.buf = c.buf[:0]
	return out
}

// Reset drops any buffered samples.
func (c *Chunker) Reset() { c.buf = c.buf[:0] }

// OverwritePolicy selects what a full RingBuffer does with new samples.
type OverwritePolicy int

const (
	// OverwriteOldest discards the oldest buffered samples to make room. This
	// is the right policy for live audio where latency must stay bounded.
	OverwriteOldest OverwritePolicy = iota
	// DropNewest keeps buffered samples and rejects the overflowing tail of
	// the write.
	DropNewest
)

// RingBuffer is a fixed-capacity, mutex-protected FIFO of PCM samples safe for
// one or more concurrent writers and readers. All operations are non-blocking.
type RingBuffer struct {
	mu      sync.Mutex
	data    []int16
	head    int // index of the oldest sample
	size    int
	policy  OverwritePolicy
	dropped uint64
}

// NewRingBuffer creates a ring buffer holding up to capacity samples
// (minimum 1) using the given overflow policy.
func NewRingBuffer(capacity int, policy OverwritePolicy) *RingBuffer {
	if capacity < 1 {
		capacity = 1
	}
	return &RingBuffer{data: make([]int16, capacity), policy: policy}
}

// Cap returns the capacity in samples.
func (r *RingBuffer) Cap() int { return len(r.data) }

// Len returns the number of buffered samples.
func (r *RingBuffer) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size
}

// Dropped returns the total number of samples discarded by the overflow
// policy since creation.
func (r *RingBuffer) Dropped() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

// Reset empties the buffer (the dropped counter is kept).
func (r *RingBuffer) Reset() {
	r.mu.Lock()
	r.head, r.size = 0, 0
	r.mu.Unlock()
}

// Write stores p and returns the number of samples of p that were retained
// (accepted). With OverwriteOldest all of p is accepted (if len(p) exceeds the
// capacity only the newest Cap() samples survive, but the return value still
// counts them as written) and older data is dropped; with DropNewest the tail
// that does not fit is dropped.
func (r *RingBuffer) Write(p []int16) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	capacity := len(r.data)
	switch r.policy {
	case DropNewest:
		free := capacity - r.size
		if len(p) > free {
			r.dropped += uint64(len(p) - free)
			p = p[:free]
		}
		r.put(p)
		return len(p)
	default:
		total := len(p)
		if len(p) > capacity {
			r.dropped += uint64(len(p) - capacity)
			p = p[len(p)-capacity:]
			r.head, r.size = 0, 0
		}
		if over := r.size + len(p) - capacity; over > 0 {
			r.head = (r.head + over) % capacity
			r.size -= over
			r.dropped += uint64(over)
		}
		r.put(p)
		return total
	}
}

// put appends p, which must fit. Caller holds the lock.
func (r *RingBuffer) put(p []int16) {
	capacity := len(r.data)
	tail := (r.head + r.size) % capacity
	n := copy(r.data[tail:], p)
	copy(r.data, p[n:])
	r.size += len(p)
}

// Read removes up to len(p) samples from the buffer into p and returns how
// many were read. It never blocks.
func (r *RingBuffer) Read(p []int16) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := min(len(p), r.size)
	first := copy(p[:n], r.data[r.head:])
	copy(p[first:n], r.data)
	r.head = (r.head + n) % len(r.data)
	r.size -= n
	return n
}

// Mixer combines PCM streams with clipping protection. The zero value hard
// saturates at the int16 limits; with SoftClip set, peaks above the knee are
// smoothly compressed towards full scale instead of being flat-topped.
type Mixer struct {
	// SoftClip enables a tanh soft limiter above SoftKnee.
	SoftClip bool
	// SoftKnee is the linear-region limit in (0, 32767]; default 24576
	// (about -2.5 dBFS) when zero.
	SoftKnee int
}

// MixMono sums any number of mono streams sample by sample. Shorter streams are
// treated as silence past their end, so the result has the length of the
// longest input. The sum never wraps: it is saturated (or soft-limited).
func (m Mixer) MixMono(streams ...[]int16) []int16 {
	n := 0
	for _, s := range streams {
		n = max(n, len(s))
	}
	out := make([]int16, n)
	if len(streams) == 0 {
		return out
	}
	acc := make([]int32, n)
	for _, s := range streams {
		for i, v := range s {
			acc[i] += int32(v)
		}
	}
	for i, v := range acc {
		out[i] = m.limit(v)
	}
	return out
}

// MixStereo interleaves left and right into L,R,L,R,... The result has
// max(len(left), len(right)) frames; the shorter channel is padded with
// silence.
func (m Mixer) MixStereo(left, right []int16) []int16 {
	n := max(len(left), len(right))
	out := make([]int16, 2*n)
	for i := range n {
		if i < len(left) {
			out[2*i] = left[i]
		}
		if i < len(right) {
			out[2*i+1] = right[i]
		}
	}
	return out
}

func (m Mixer) limit(v int32) int16 {
	if !m.SoftClip {
		return saturate(v)
	}
	knee := float64(m.SoftKnee)
	if knee <= 0 || knee > math.MaxInt16 {
		knee = 24576
	}
	f := float64(v)
	a := math.Abs(f)
	if a <= knee {
		return int16(v)
	}
	headroom := math.MaxInt16 - knee
	g := knee + headroom*math.Tanh((a-knee)/headroom)
	if f < 0 {
		g = -g
	}
	return saturate(int32(math.Round(g)))
}

func saturate(v int32) int16 {
	switch {
	case v > math.MaxInt16:
		return math.MaxInt16
	case v < math.MinInt16:
		return math.MinInt16
	}
	return int16(v)
}
