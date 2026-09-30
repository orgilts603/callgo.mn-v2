package audio

import (
	"math"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRMSdBFS(t *testing.T) {
	assert.InDelta(t, 0, RMSdBFS(constPCM(-32768, 100)), 1e-9)
	assert.InDelta(t, -6.02, RMSdBFS(constPCM(16384, 100)), 0.01)
	// Full-scale sine is -3.01 dBFS.
	assert.InDelta(t, -3.01, RMSdBFS(sineWave(8000, 1000, 32768, 8000)), 0.05)
	assert.Equal(t, MinDBFS, RMSdBFS(make([]int16, 10)))
	assert.Equal(t, MinDBFS, RMSdBFS(nil))
}

// frames splits pcm into 20 ms frames.
func frames20(pcm []int16, rate int) [][]int16 {
	n := rate / 50
	var out [][]int16
	for i := 0; i+n <= len(pcm); i += n {
		out = append(out, pcm[i:i+n])
	}
	return out
}

type vadEvent struct {
	ms    int
	state VADState
}

func runVAD(v *EnergyVAD, pcm []int16, rate int) []vadEvent {
	var ev []vadEvent
	for i, f := range frames20(pcm, rate) {
		if st, changed := v.Process(f, rate); changed {
			ev = append(ev, vadEvent{ms: (i + 1) * 20, state: st})
		}
	}
	return ev
}

func TestEnergyVADBurstsInNoise(t *testing.T) {
	const rate = 16000
	rng := rand.New(rand.NewSource(3))
	noise := func(ms int) []int16 { return noiseBurst(rng, rate*ms/1000, 60) } // about -58 dBFS
	tone := func(ms int) []int16 { return sineWave(rate, 400, 6000, rate*ms/1000) }

	var sig []int16
	sig = append(sig, noise(500)...)
	sig = append(sig, tone(600)...) // speech 500..1100
	sig = append(sig, noise(1000)...)
	sig = append(sig, tone(400)...) // speech 2100..2500
	sig = append(sig, noise(800)...)

	vad := NewEnergyVAD(-40, 200, 60)
	ev := runVAD(vad, sig, rate)
	require.Len(t, ev, 4, "events: %+v", ev)
	assert.Equal(t, VADSpeech, ev[0].state)
	assert.InDelta(t, 500+60, ev[0].ms, 20, "start after MinSpeechMs")
	assert.Equal(t, VADSilence, ev[1].state)
	assert.InDelta(t, 1100+200, ev[1].ms, 20, "end after hangover")
	assert.Equal(t, VADSpeech, ev[2].state)
	assert.InDelta(t, 2100+60, ev[2].ms, 20)
	assert.Equal(t, VADSilence, ev[3].state)
	assert.InDelta(t, 2500+200, ev[3].ms, 20)
	assert.Equal(t, VADSilence, vad.State())
}

func TestEnergyVADMinSpeechRejectsClicks(t *testing.T) {
	const rate = 8000
	var sig []int16
	sig = append(sig, make([]int16, rate/2)...)
	sig = append(sig, sineWave(rate, 500, 8000, rate*40/1000)...) // 40 ms < 100 ms
	sig = append(sig, make([]int16, rate)...)
	assert.Empty(t, runVAD(NewEnergyVAD(-40, 200, 100), sig, rate))
}

func TestEnergyVADHangoverBridgesShortPauses(t *testing.T) {
	const rate = 8000
	tone := sineWave(rate, 500, 8000, rate*300/1000)
	pause := make([]int16, rate*100/1000) // 100 ms < 300 ms hangover
	var sig []int16
	for range 3 {
		sig = append(sig, tone...)
		sig = append(sig, pause...)
	}
	sig = append(sig, make([]int16, rate)...)
	ev := runVAD(NewEnergyVAD(-40, 300, 40), sig, rate)
	require.Len(t, ev, 2, "one speech segment, %+v", ev)
	assert.Equal(t, VADSpeech, ev[0].state)
	assert.Equal(t, VADSilence, ev[1].state)
}

func TestEnergyVADEdgeCases(t *testing.T) {
	v := NewEnergyVAD(-40, 100, 20)
	st, changed := v.Process(nil, 8000)
	assert.Equal(t, VADSilence, st)
	assert.False(t, changed)
	_, changed = v.Process([]int16{1, 2}, 0)
	assert.False(t, changed)
	loud := sineWave(8000, 300, 10000, 160)
	st, changed = v.Process(loud, 8000)
	assert.True(t, changed)
	assert.Equal(t, VADSpeech, st)
	assert.Equal(t, "speech", st.String())
	v.Reset()
	assert.Equal(t, VADSilence, v.State())
	assert.Equal(t, "silence", v.State().String())
	// Threshold is inclusive of the level and independent of the sample rate.
	quiet := sineWave(16000, 300, 100*math.Sqrt2, 320) // 100 RMS = -50.3 dBFS
	_, changed = NewEnergyVAD(-40, 100, 20).Process(quiet, 16000)
	assert.False(t, changed)
}

func BenchmarkRMSdBFS(b *testing.B) {
	x := sineWave(16000, 300, 8000, 320)
	for b.Loop() {
		RMSdBFS(x)
	}
}
