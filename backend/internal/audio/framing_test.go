package audio

import (
	"math"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ramp(from, n int) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(from + i)
	}
	return out
}

func TestChunkerYieldsFixedFrames(t *testing.T) {
	c := NewChunkerMs(8000, 20)
	require.Equal(t, 160, c.FrameSize())
	assert.Empty(t, c.Write(ramp(0, 100)))
	assert.Equal(t, 100, c.Buffered())

	frames := c.Write(ramp(100, 500)) // 600 total -> 3 frames + 120
	require.Len(t, frames, 3)
	for i, f := range frames {
		require.Len(t, f, 160)
		for j, s := range f {
			require.Equal(t, int16(i*160+j), s)
		}
	}
	assert.Equal(t, 120, c.Buffered())
	// Frames are independent of later writes.
	frames[0][0] = 7
	more := c.Write(ramp(600, 40))
	require.Len(t, more, 1)
	assert.Equal(t, int16(480), more[0][0])

	c.Write(ramp(0, 10))
	padded := c.Flush(true)
	require.Len(t, padded, 160)
	assert.Equal(t, int16(0), padded[159])
	assert.Nil(t, c.Flush(true))
	c.Write(ramp(0, 5))
	assert.Len(t, c.Flush(false), 5)
	c.Write(ramp(0, 5))
	c.Reset()
	assert.Zero(t, c.Buffered())
}

func TestChunkerOneSampleWrites(t *testing.T) {
	c := NewChunker(4)
	var total int
	for i := range 10 {
		total += len(c.Write([]int16{int16(i)}))
	}
	assert.Equal(t, 2, total)
	assert.Equal(t, 2, c.Buffered())
	assert.Equal(t, 1, NewChunker(0).FrameSize())
}

func TestRingBufferOverwriteOldest(t *testing.T) {
	r := NewRingBuffer(5, OverwriteOldest)
	assert.Equal(t, 5, r.Cap())
	assert.Equal(t, 3, r.Write([]int16{1, 2, 3}))
	assert.Equal(t, 3, r.Len())
	assert.Equal(t, 3, r.Write([]int16{4, 5, 6})) // drops 1
	assert.Equal(t, 5, r.Len())
	assert.Equal(t, uint64(1), r.Dropped())

	buf := make([]int16, 2)
	assert.Equal(t, 2, r.Read(buf))
	assert.Equal(t, []int16{2, 3}, buf)

	r.Write([]int16{7, 8, 9, 10}) // wraps; buffer had 4,5,6 -> drops 4,5? size 3+4=7 > 5 -> drop 2
	out := make([]int16, 10)
	n := r.Read(out)
	assert.Equal(t, []int16{6, 7, 8, 9, 10}, out[:n])
	assert.Equal(t, uint64(3), r.Dropped())
	assert.Zero(t, r.Read(out))

	// A write larger than the capacity keeps only the newest samples.
	r.Write(ramp(0, 12))
	n = r.Read(out)
	assert.Equal(t, ramp(7, 5), out[:n])
}

func TestRingBufferDropNewest(t *testing.T) {
	r := NewRingBuffer(4, DropNewest)
	assert.Equal(t, 4, r.Write([]int16{1, 2, 3, 4}))
	assert.Equal(t, 0, r.Write([]int16{5}))
	buf := make([]int16, 2)
	r.Read(buf)
	assert.Equal(t, 2, r.Write([]int16{5, 6, 7})) // only two fit
	assert.Equal(t, uint64(2), r.Dropped())
	out := make([]int16, 8)
	n := r.Read(out)
	assert.Equal(t, []int16{3, 4, 5, 6}, out[:n])
	r.Write([]int16{1})
	r.Reset()
	assert.Zero(t, r.Len())
}

func TestRingBufferConcurrent(t *testing.T) {
	r := NewRingBuffer(1024, OverwriteOldest)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 2000 {
			r.Write(ramp(0, 100))
		}
	}()
	go func() {
		defer wg.Done()
		buf := make([]int16, 64)
		for range 2000 {
			r.Read(buf)
			_ = r.Len()
		}
	}()
	wg.Wait()
	assert.LessOrEqual(t, r.Len(), 1024)
}

func TestMixStereo(t *testing.T) {
	var m Mixer
	assert.Equal(t, []int16{1, 10, 2, 20, 3, 0}, m.MixStereo([]int16{1, 2, 3}, []int16{10, 20}))
	assert.Equal(t, []int16{0, 5}, m.MixStereo(nil, []int16{5}))
	assert.Empty(t, m.MixStereo(nil, nil))
}

func TestMixMonoClipping(t *testing.T) {
	var m Mixer
	got := m.MixMono([]int16{30000, -30000, 100, 5}, []int16{30000, -30000, -50})
	assert.Equal(t, []int16{math.MaxInt16, math.MinInt16, 50, 5}, got)
	assert.Empty(t, m.MixMono())
	assert.Equal(t, []int16{1, 2}, m.MixMono([]int16{1, 2}))
}

func TestMixMonoSoftClip(t *testing.T) {
	m := Mixer{SoftClip: true}
	got := m.MixMono([]int16{1000, 20000, 32000, -32000}, []int16{0, 0, 32000, -32000})
	assert.Equal(t, int16(1000), got[0], "below the knee is untouched")
	assert.Equal(t, int16(20000), got[1])
	assert.Less(t, got[2], int16(math.MaxInt16), "soft clip stays below full scale")
	assert.Greater(t, got[2], int16(24576))
	assert.Equal(t, -got[2], got[3], "symmetric")
	// Monotonic through the knee.
	prev := int16(0)
	for v := 0; v <= 65000; v += 250 {
		g := m.MixMono([]int16{int16(min(v, 32767))}, []int16{int16(max(0, v-32767))})[0]
		require.GreaterOrEqual(t, g, prev)
		prev = g
	}
}

func BenchmarkMixStereo(b *testing.B) {
	l, r := sineWave(16000, 300, 9000, 16000), sineWave(16000, 500, 9000, 16000)
	var m Mixer
	for b.Loop() {
		m.MixStereo(l, r)
	}
}
