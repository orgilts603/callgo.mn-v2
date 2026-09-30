package audio

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	wavHeaderSize   = 44
	wavUnknownSize  = 0xFFFFFFFF
	wavMaxDataBytes = 0xFFFFFFFF - (wavHeaderSize - 8) - 1
)

var (
	// ErrInvalidWAV is returned for malformed WAV data.
	ErrInvalidWAV = errors.New("audio: invalid WAV")
	// ErrUnsupportedWAV is returned for valid WAV files that are not 16-bit PCM.
	ErrUnsupportedWAV = errors.New("audio: unsupported WAV format (need 16-bit PCM)")
	// ErrPartialFrame is returned when interleaved samples are not a whole
	// number of channel frames.
	ErrPartialFrame = errors.New("audio: sample count is not a multiple of channels")
	// ErrWAVTooLarge is returned when data would exceed the 4 GiB RIFF limit.
	ErrWAVTooLarge = errors.New("audio: WAV data exceeds 4 GiB")
	// ErrClosed is returned by writers used after Close.
	ErrClosed = errors.New("audio: closed")
)

func validateFormat(rate, channels int) error {
	if rate <= 0 {
		return fmt.Errorf("%w: %d", ErrInvalidRate, rate)
	}
	if channels < 1 || channels > 65535 {
		return fmt.Errorf("audio: invalid channel count %d", channels)
	}
	return nil
}

func putWAVHeader(rate, channels int, dataBytes uint32) []byte {
	h := make([]byte, wavHeaderSize)
	le := binary.LittleEndian
	copy(h[0:], "RIFF")
	riff := uint32(wavUnknownSize)
	if dataBytes != wavUnknownSize {
		riff = dataBytes + wavHeaderSize - 8
	}
	le.PutUint32(h[4:], riff)
	copy(h[8:], "WAVEfmt ")
	le.PutUint32(h[16:], 16)
	le.PutUint16(h[20:], 1) // PCM
	le.PutUint16(h[22:], uint16(channels))
	le.PutUint32(h[24:], uint32(rate))
	le.PutUint32(h[28:], uint32(rate*channels*2))
	le.PutUint16(h[32:], uint16(channels*2))
	le.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	le.PutUint32(h[40:], dataBytes)
	return h
}

func appendPCM(dst []byte, pcm []int16) []byte {
	for _, s := range pcm {
		dst = append(dst, byte(s), byte(uint16(s)>>8))
	}
	return dst
}

// WriteWAV writes pcm (interleaved when channels > 1) as a 16-bit PCM WAV file.
func WriteWAV(w io.Writer, pcm []int16, rate, channels int) error {
	if err := validateFormat(rate, channels); err != nil {
		return err
	}
	if len(pcm)%channels != 0 {
		return ErrPartialFrame
	}
	if int64(len(pcm))*2 > wavMaxDataBytes {
		return ErrWAVTooLarge
	}
	buf := make([]byte, 0, wavHeaderSize+len(pcm)*2)
	buf = append(buf, putWAVHeader(rate, channels, uint32(len(pcm)*2))...)
	buf = appendPCM(buf, pcm)
	if _, err := w.Write(buf); err != nil {
		return fmt.Errorf("audio: write WAV: %w", err)
	}
	return nil
}

// ReadWAV parses a 16-bit PCM WAV stream and returns interleaved samples.
// Unknown chunks are skipped. A data chunk with an unpatched size
// (0xFFFFFFFF) or one truncated by a crashed writer yields the complete frames
// that are present rather than an error.
func ReadWAV(r io.Reader) (pcm []int16, rate, channels int, err error) {
	br := bufio.NewReader(r)
	var hdr [12]byte
	if _, err = io.ReadFull(br, hdr[:]); err != nil {
		return nil, 0, 0, fmt.Errorf("%w: reading RIFF header: %v", ErrInvalidWAV, err)
	}
	if string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return nil, 0, 0, fmt.Errorf("%w: missing RIFF/WAVE signature", ErrInvalidWAV)
	}
	le := binary.LittleEndian
	haveFmt := false
	for {
		var ch [8]byte
		if _, err = io.ReadFull(br, ch[:]); err != nil {
			return nil, 0, 0, fmt.Errorf("%w: no data chunk: %v", ErrInvalidWAV, err)
		}
		id, size := string(ch[0:4]), le.Uint32(ch[4:])
		switch id {
		case "fmt ":
			if size < 16 || size > 1<<16 {
				return nil, 0, 0, fmt.Errorf("%w: bad fmt chunk size %d", ErrInvalidWAV, size)
			}
			body := make([]byte, size)
			if _, err = io.ReadFull(br, body); err != nil {
				return nil, 0, 0, fmt.Errorf("%w: reading fmt chunk: %v", ErrInvalidWAV, err)
			}
			tag := le.Uint16(body[0:])
			channels = int(le.Uint16(body[2:]))
			rate = int(le.Uint32(body[4:]))
			bits := le.Uint16(body[14:])
			if tag == 0xFFFE && size >= 26 { // WAVE_FORMAT_EXTENSIBLE
				tag = le.Uint16(body[24:]) // first two bytes of the sub-format GUID
			}
			if tag != 1 || bits != 16 {
				return nil, 0, 0, fmt.Errorf("%w: format tag %d, %d bits", ErrUnsupportedWAV, tag, bits)
			}
			if channels < 1 || rate < 1 {
				return nil, 0, 0, fmt.Errorf("%w: %d channels at %d Hz", ErrInvalidWAV, channels, rate)
			}
			haveFmt = true
			if size%2 == 1 {
				_, _ = br.Discard(1)
			}
		case "data":
			if !haveFmt {
				return nil, 0, 0, fmt.Errorf("%w: data chunk before fmt chunk", ErrInvalidWAV)
			}
			var raw []byte
			if size == wavUnknownSize {
				raw, err = io.ReadAll(br)
			} else {
				raw, err = io.ReadAll(io.LimitReader(br, int64(size)))
			}
			if err != nil {
				return nil, 0, 0, fmt.Errorf("audio: read WAV data: %w", err)
			}
			n := len(raw) / 2
			n -= n % channels
			pcm = make([]int16, n)
			for i := range pcm {
				pcm[i] = int16(le.Uint16(raw[2*i:]))
			}
			return pcm, rate, channels, nil
		default:
			skip := int64(size) + int64(size%2)
			if _, err = io.CopyN(io.Discard, br, skip); err != nil {
				return nil, 0, 0, fmt.Errorf("%w: skipping %q chunk: %v", ErrInvalidWAV, id, err)
			}
		}
	}
}

// WAVWriter writes a 16-bit PCM WAV incrementally.
//
// If the destination implements io.WriteSeeker the header is written up front
// with placeholder sizes and patched on Close; the file stays readable by
// ReadWAV even if Close is never reached. Otherwise samples are buffered in
// memory and the complete file is written on Close. WAVWriter does not close
// the underlying writer. It is not safe for concurrent use.
type WAVWriter struct {
	w        io.Writer
	seek     io.Seeker // non-nil when streaming
	start    int64     // offset of the RIFF header within seek
	rate     int
	channels int
	data     int64 // data bytes written
	buf      []byte
	mem      bytes.Buffer // used only when not seekable
	closed   bool
}

// NewWAVWriter starts a WAV stream on w.
func NewWAVWriter(w io.Writer, rate, channels int) (*WAVWriter, error) {
	if err := validateFormat(rate, channels); err != nil {
		return nil, err
	}
	ww := &WAVWriter{w: w, rate: rate, channels: channels}
	if ws, ok := w.(io.WriteSeeker); ok {
		start, err := ws.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, fmt.Errorf("audio: locate WAV start: %w", err)
		}
		ww.seek, ww.start = ws, start
		if _, err := w.Write(putWAVHeader(rate, channels, wavUnknownSize)); err != nil {
			return nil, fmt.Errorf("audio: write WAV header: %w", err)
		}
	}
	return ww, nil
}

// Seekable reports whether the header is written up front and patched on Close
// (true) or the whole file is buffered until Close (false).
func (ww *WAVWriter) Seekable() bool { return ww.seek != nil }

// Rate returns the sample rate.
func (ww *WAVWriter) Rate() int { return ww.rate }

// Channels returns the channel count.
func (ww *WAVWriter) Channels() int { return ww.channels }

// Frames returns the number of sample frames (samples per channel) written.
func (ww *WAVWriter) Frames() int64 { return ww.data / int64(2*ww.channels) }

// Write appends interleaved samples; len(pcm) must be a multiple of the
// channel count.
func (ww *WAVWriter) Write(pcm []int16) error {
	if ww.closed {
		return ErrClosed
	}
	if len(pcm)%ww.channels != 0 {
		return ErrPartialFrame
	}
	if ww.data+int64(len(pcm))*2 > wavMaxDataBytes {
		return ErrWAVTooLarge
	}
	ww.buf = appendPCM(ww.buf[:0], pcm)
	dst := io.Writer(&ww.mem)
	if ww.seek != nil {
		dst = ww.w
	}
	if _, err := dst.Write(ww.buf); err != nil {
		return fmt.Errorf("audio: write WAV data: %w", err)
	}
	ww.data += int64(len(ww.buf))
	return nil
}

// Close finalises the file: it patches the header sizes (seekable) or writes
// the header and buffered data (non-seekable). It is idempotent.
func (ww *WAVWriter) Close() error {
	if ww.closed {
		return nil
	}
	ww.closed = true
	if ww.seek == nil {
		if _, err := ww.w.Write(putWAVHeader(ww.rate, ww.channels, uint32(ww.data))); err != nil {
			return fmt.Errorf("audio: write WAV header: %w", err)
		}
		if _, err := ww.mem.WriteTo(ww.w); err != nil {
			return fmt.Errorf("audio: write WAV data: %w", err)
		}
		return nil
	}
	le := binary.LittleEndian
	var sz [4]byte
	le.PutUint32(sz[:], uint32(ww.data)+wavHeaderSize-8)
	if err := ww.patch(ww.start+4, sz[:]); err != nil {
		return err
	}
	le.PutUint32(sz[:], uint32(ww.data))
	if err := ww.patch(ww.start+40, sz[:]); err != nil {
		return err
	}
	if _, err := ww.seek.Seek(ww.start+wavHeaderSize+ww.data, io.SeekStart); err != nil {
		return fmt.Errorf("audio: restore WAV position: %w", err)
	}
	return nil
}

func (ww *WAVWriter) patch(off int64, b []byte) error {
	if _, err := ww.seek.Seek(off, io.SeekStart); err != nil {
		return fmt.Errorf("audio: seek to patch WAV header: %w", err)
	}
	if _, err := ww.w.Write(b); err != nil {
		return fmt.Errorf("audio: patch WAV header: %w", err)
	}
	return nil
}
