package audio

import (
	"bytes"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chunked pushes pcm in 20 ms chunks starting at startMs using push.
func chunked(t *testing.T, push func(int64, []int16, int) error, startMs int64, pcm []int16, rate int) {
	t.Helper()
	n := rate / 50
	for off := 0; off < len(pcm); off += n {
		end := min(off+n, len(pcm))
		ts := startMs + int64(off)*1000/int64(rate)
		require.NoError(t, push(ts, pcm[off:end], rate))
	}
}

func readStereo(t *testing.T, f *memFile) (left, right []int16, rate int) {
	t.Helper()
	pcm, rate, ch, err := ReadWAV(bytes.NewReader(f.data))
	require.NoError(t, err)
	require.Equal(t, 2, ch)
	for i := 0; i+1 < len(pcm); i += 2 {
		left = append(left, pcm[i])
		right = append(right, pcm[i+1])
	}
	return left, right, rate
}

func rmsWindow(pcm []int16, rate int, fromMs, toMs int) float64 {
	seg := pcm[rate*fromMs/1000 : rate*toMs/1000]
	return RMSdBFS(seg)
}

func TestRecorderBurstsAndGaps(t *testing.T) {
	const target = 16000
	f := &memFile{}
	rec, err := NewRecorder(f, target)
	require.NoError(t, err)

	// Customer (8 kHz): 0-200 ms and 1000-1300 ms. Agent (24 kHz): 400-800 ms and 1500-1700 ms.
	chunked(t, rec.PushCustomer, 0, sineWave(8000, 500, 9000, 8000*200/1000), 8000)
	chunked(t, rec.PushAgent, 400, sineWave(24000, 900, 9000, 24000*400/1000), 24000)
	chunked(t, rec.PushCustomer, 1000, sineWave(8000, 500, 9000, 8000*300/1000), 8000)
	chunked(t, rec.PushAgent, 1500, sineWave(24000, 900, 9000, 24000*200/1000), 24000)
	require.NoError(t, rec.Close())
	require.NoError(t, rec.Close(), "idempotent")

	left, right, rate := readStereo(t, f)
	assert.Equal(t, target, rate)
	// Expected length: the agent's last burst ends at 1700 ms.
	require.Len(t, left, target*1700/1000)
	require.Len(t, right, target*1700/1000)
	assert.Equal(t, int64(target*1700/1000), rec.Frames())

	// Customer left: active in bursts, digital silence in gaps.
	assert.Greater(t, rmsWindow(left, rate, 10, 190), -25.0)
	assert.Equal(t, MinDBFS, rmsWindow(left, rate, 210, 990), "gap after first burst")
	assert.Greater(t, rmsWindow(left, rate, 1010, 1290), -25.0)
	assert.Equal(t, MinDBFS, rmsWindow(left, rate, 1310, 1700), "after last customer burst")
	// Agent right.
	assert.Equal(t, MinDBFS, rmsWindow(right, rate, 0, 395))
	assert.Greater(t, rmsWindow(right, rate, 410, 790), -25.0)
	assert.Equal(t, MinDBFS, rmsWindow(right, rate, 810, 1490))
	assert.Greater(t, rmsWindow(right, rate, 1510, 1690), -25.0)

	// Tones land on the right frequencies and channels.
	assert.Greater(t, goertzelPower(left[:target/5], target, 500), 1000*goertzelPower(left[:target/5], target, 900))
	seg := right[target*400/1000 : target*800/1000]
	assert.Greater(t, goertzelPower(seg, target, 900), 1000*goertzelPower(seg, target, 500))

	// Alignment: the customer's second burst begins at 1000 ms (within 1 ms).
	first := -1
	for i := target * 900 / 1000; i < len(left); i++ {
		if left[i] != 0 {
			first = i
			break
		}
	}
	require.GreaterOrEqual(t, first, 0)
	assert.InDelta(t, target*1000/1000, first, float64(target)/1000+2)
}

func TestRecorderContinuousStreamHasNoClicks(t *testing.T) {
	f := &memFile{}
	rec, err := NewRecorder(f, 16000)
	require.NoError(t, err)
	chunked(t, rec.PushCustomer, 0, sineWave(8000, 300, 12000, 16000), 8000) // 2 s
	require.NoError(t, rec.Close())
	left, right, _ := readStereo(t, f)
	require.Len(t, left, 32000)
	require.Len(t, right, 32000, "silent agent is padded to equal length")
	assert.Equal(t, MinDBFS, RMSdBFS(right))
	// Compare to the ideal upsampled sine.
	assert.Greater(t, snrDB(sineWave(16000, 300, 12000, 32000), left, 400), 40.0)
}

func TestRecorderTimestampJitterAndOverlapAreAbsorbed(t *testing.T) {
	f := &memFile{}
	rec, err := NewRecorder(f, 8000)
	require.NoError(t, err)
	chunk := sineWave(8000, 400, 8000, 160)
	for i := range 50 { // 1 s, timestamps jitter +-2 ms
		ts := int64(i*20 + (i%3 - 1)) // -1,0,+1 ms
		if i == 0 {
			ts = 0
		}
		require.NoError(t, rec.PushCustomer(ts, chunk, 8000))
	}
	require.NoError(t, rec.PushCustomer(0, chunk, 8000)) // stale/overlapping: appended contiguously
	require.NoError(t, rec.Close())
	left, _, _ := readStereo(t, f)
	assert.Len(t, left, 8000+160)
}

func TestRecorderEarlierTimestampShiftsOrigin(t *testing.T) {
	f := &memFile{}
	rec, err := NewRecorder(f, 8000)
	require.NoError(t, err)
	// Timestamps are absolute (Unix-style) ms. Agent speaks first at t=1_000_100.
	require.NoError(t, rec.PushAgent(1_000_100, sineWave(8000, 400, 8000, 800), 8000))
	require.NoError(t, rec.PushCustomer(1_000_000, sineWave(8000, 400, 8000, 800), 8000)) // 100 ms earlier
	require.NoError(t, rec.Close())
	left, right, _ := readStereo(t, f)
	// Customer 1_000_000..1_000_100, agent 1_000_100..1_000_200 => 200 ms.
	require.Len(t, left, 1600)
	assert.Equal(t, MinDBFS, rmsWindow(right, 8000, 0, 99))
	assert.Greater(t, rmsWindow(right, 8000, 105, 195), -25.0)
	assert.Greater(t, rmsWindow(left, 8000, 5, 95), -25.0)
}

func TestRecorderMixedRateChangeMidStream(t *testing.T) {
	f := &memFile{}
	rec, err := NewRecorder(f, 16000)
	require.NoError(t, err)
	chunked(t, rec.PushAgent, 0, sineWave(8000, 700, 9000, 4000), 8000)      // 0-500 ms
	chunked(t, rec.PushAgent, 500, sineWave(48000, 700, 9000, 24000), 48000) // 500-1000 ms
	require.NoError(t, rec.Close())
	_, right, _ := readStereo(t, f)
	require.Len(t, right, 16000)
	assert.Greater(t, snrDB(sineWave(16000, 700, 9000, 16000), right, 500), 35.0)
}

func TestRecorderBoundedMemoryWhenOneLegIsSilent(t *testing.T) {
	f := &memFile{}
	rec, err := NewRecorder(f, 8000)
	require.NoError(t, err)
	rec.MaxSkewMs = 500
	chunked(t, rec.PushCustomer, 0, sineWave(8000, 400, 8000, 8000*10), 8000) // 10 s, agent never speaks
	// Almost everything must already be on disk before Close.
	assert.GreaterOrEqual(t, rec.Frames(), int64(8000*9))
	require.NoError(t, rec.Close())
	left, right, _ := readStereo(t, f)
	assert.Len(t, left, 80000)
	assert.Len(t, right, 80000)
}

func TestRecorderErrors(t *testing.T) {
	_, err := NewRecorder(&memFile{}, 0)
	require.ErrorIs(t, err, ErrInvalidRate)

	f := &memFile{}
	rec, err := NewRecorder(f, 8000)
	require.NoError(t, err)
	require.ErrorIs(t, rec.PushCustomer(0, []int16{1}, 0), ErrInvalidRate)
	require.NoError(t, rec.PushCustomer(0, nil, 8000))
	require.NoError(t, rec.Close())
	require.ErrorIs(t, rec.PushAgent(0, []int16{1}, 8000), ErrClosed)

	// Empty recording is a valid empty WAV.
	pcm, rate, ch, err := ReadWAV(bytes.NewReader(f.data))
	require.NoError(t, err)
	assert.Empty(t, pcm)
	assert.Equal(t, 8000, rate)
	assert.Equal(t, 2, ch)
}

func TestRecorderConcurrentLegs(t *testing.T) {
	f := &memFile{}
	rec, err := NewRecorder(f, 16000)
	require.NoError(t, err)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		chunked(t, rec.PushCustomer, 0, sineWave(8000, 400, 8000, 16000), 8000)
	}()
	go func() {
		defer wg.Done()
		chunked(t, rec.PushAgent, 0, sineWave(16000, 600, 8000, 32000), 16000)
	}()
	wg.Wait()
	require.NoError(t, rec.Close())
	left, right, _ := readStereo(t, f)
	assert.Len(t, left, 32000)
	assert.Len(t, right, 32000)
}

func BenchmarkRecorder1sStereo(b *testing.B) {
	c := sineWave(8000, 400, 8000, 160)
	a := sineWave(16000, 600, 8000, 320)
	for b.Loop() {
		rec, _ := NewRecorder(&memFile{}, 16000)
		for i := range 50 {
			_ = rec.PushCustomer(int64(i*20), c, 8000)
			_ = rec.PushAgent(int64(i*20), a, 16000)
		}
		_ = rec.Close()
	}
}
