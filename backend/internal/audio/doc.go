// Package audio provides small, dependency-free audio DSP utilities used by
// recording post-processing, mock media generation and future direct-media
// paths.
//
// All PCM in this package is signed 16-bit linear (int16) unless noted. Stereo
// buffers are interleaved (L, R, L, R, ...).
//
//   - G.711: MuLawEncode/MuLawDecode, ALawEncode/ALawDecode (ITU-T G.711).
//   - Resampling: Resample (one shot) and Resampler (streaming, click free),
//     windowed-sinc polyphase FIR for any rational rate pair.
//   - Framing: Chunker, RingBuffer, Mixer.
//   - VAD: RMSdBFS and EnergyVAD (speech start/end with hangover).
//   - WAV: WriteWAV, ReadWAV, WAVWriter (streaming, header patched on Close).
//   - Recorder: two mono call legs -> timestamp aligned stereo WAV.
package audio
