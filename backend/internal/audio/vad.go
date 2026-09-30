package audio

import "math"

// MinDBFS is the floor returned by RMSdBFS for silence and empty input.
const MinDBFS = -120.0

// RMSdBFS returns the RMS level of pcm in dB relative to digital full scale
// (a full-scale square wave is 0 dBFS, a full-scale sine is about -3 dBFS).
// Silence and empty input return MinDBFS.
func RMSdBFS(pcm []int16) float64 {
	if len(pcm) == 0 {
		return MinDBFS
	}
	var sum float64
	for _, s := range pcm {
		f := float64(s)
		sum += f * f
	}
	rms := math.Sqrt(sum / float64(len(pcm)))
	if rms < 1e-9 {
		return MinDBFS
	}
	return math.Max(MinDBFS, 20*math.Log10(rms/32768))
}

// VADState is the output state of a voice activity detector.
type VADState int

const (
	// VADSilence means no speech is active.
	VADSilence VADState = iota
	// VADSpeech means speech is active.
	VADSpeech
)

func (s VADState) String() string {
	if s == VADSpeech {
		return "speech"
	}
	return "silence"
}

// EnergyVAD is a simple energy based voice activity detector with debouncing.
//
// A frame is "active" when its RMS level is at least ThresholdDBFS. Silence
// becomes speech once active frames have accumulated MinSpeechMs
// consecutively; speech becomes silence once inactive frames have accumulated
// HangoverMs consecutively. Fill the exported fields (or use NewEnergyVAD) and
// call Process for each frame. It is not safe for concurrent use.
type EnergyVAD struct {
	// ThresholdDBFS is the activity threshold (e.g. -40).
	ThresholdDBFS float64
	// HangoverMs is how long the level must stay below the threshold before
	// speech ends.
	HangoverMs int
	// MinSpeechMs is how long the level must stay above the threshold before
	// speech starts (rejects clicks).
	MinSpeechMs int

	state    VADState
	activeMs float64 // consecutive active time while in silence
	quietMs  float64 // consecutive inactive time while in speech
}

// NewEnergyVAD returns a detector. Typical telephony values are -40 dBFS,
// 300 ms hangover and 60 ms minimum speech.
func NewEnergyVAD(thresholdDBFS float64, hangoverMs, minSpeechMs int) *EnergyVAD {
	return &EnergyVAD{ThresholdDBFS: thresholdDBFS, HangoverMs: hangoverMs, MinSpeechMs: minSpeechMs}
}

// State returns the current state.
func (v *EnergyVAD) State() VADState { return v.state }

// Reset returns the detector to silence and clears its timers.
func (v *EnergyVAD) Reset() { v.state, v.activeMs, v.quietMs = VADSilence, 0, 0 }

// Process feeds one frame of mono PCM sampled at rate Hz and returns the state
// after the frame and whether it changed (a speech-start or speech-end
// transition). Empty frames or a non-positive rate are ignored.
func (v *EnergyVAD) Process(frame []int16, rate int) (state VADState, changed bool) {
	if len(frame) == 0 || rate <= 0 {
		return v.state, false
	}
	ms := float64(len(frame)) * 1000 / float64(rate)
	active := RMSdBFS(frame) >= v.ThresholdDBFS
	switch v.state {
	case VADSilence:
		if !active {
			v.activeMs = 0
			break
		}
		v.activeMs += ms
		if v.activeMs >= float64(v.MinSpeechMs) {
			v.state, v.activeMs, v.quietMs = VADSpeech, 0, 0
			return v.state, true
		}
	case VADSpeech:
		if active {
			v.quietMs = 0
			break
		}
		v.quietMs += ms
		if v.quietMs >= float64(v.HangoverMs) {
			v.state, v.activeMs, v.quietMs = VADSilence, 0, 0
			return v.state, true
		}
	}
	return v.state, false
}
