package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memFile is an in-memory io.WriteSeeker.
type memFile struct {
	data []byte
	pos  int64
}

func (m *memFile) Write(p []byte) (int, error) {
	if end := m.pos + int64(len(p)); end > int64(len(m.data)) {
		m.data = append(m.data, make([]byte, end-int64(len(m.data)))...)
	}
	copy(m.data[m.pos:], p)
	m.pos += int64(len(p))
	return len(p), nil
}

func (m *memFile) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		m.pos = off
	case io.SeekCurrent:
		m.pos += off
	case io.SeekEnd:
		m.pos = int64(len(m.data)) + off
	}
	if m.pos < 0 {
		return 0, errors.New("negative position")
	}
	return m.pos, nil
}

func TestWriteReadWAVRoundTrip(t *testing.T) {
	for _, ch := range []int{1, 2, 3} {
		pcm := make([]int16, 300*ch)
		for i := range pcm {
			pcm[i] = int16(i*97 - 15000)
		}
		pcm[0], pcm[1] = math.MinInt16, math.MaxInt16
		var buf bytes.Buffer
		require.NoError(t, WriteWAV(&buf, pcm, 16000, ch))
		require.Equal(t, 44+len(pcm)*2, buf.Len())

		got, rate, channels, err := ReadWAV(&buf)
		require.NoError(t, err)
		assert.Equal(t, 16000, rate)
		assert.Equal(t, ch, channels)
		assert.Equal(t, pcm, got)
	}
}

func TestWriteWAVHeaderBytes(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, WriteWAV(&buf, []int16{1, -2}, 8000, 1))
	b := buf.Bytes()
	assert.Equal(t, "RIFF", string(b[0:4]))
	assert.Equal(t, uint32(36+4), binary.LittleEndian.Uint32(b[4:]))
	assert.Equal(t, "WAVEfmt ", string(b[8:16]))
	assert.Equal(t, uint16(1), binary.LittleEndian.Uint16(b[20:]))
	assert.Equal(t, uint32(16000), binary.LittleEndian.Uint32(b[28:]), "byte rate")
	assert.Equal(t, "data", string(b[36:40]))
	assert.Equal(t, uint32(4), binary.LittleEndian.Uint32(b[40:]))
	assert.Equal(t, []byte{1, 0, 0xFE, 0xFF}, b[44:])
}

func TestWriteWAVErrors(t *testing.T) {
	var buf bytes.Buffer
	require.ErrorIs(t, WriteWAV(&buf, []int16{1}, 8000, 2), ErrPartialFrame)
	require.ErrorIs(t, WriteWAV(&buf, nil, 0, 1), ErrInvalidRate)
	require.Error(t, WriteWAV(&buf, nil, 8000, 0))
	require.Error(t, WriteWAV(failWriter{}, []int16{1}, 8000, 1))
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestReadWAVSkipsUnknownChunksAndPadding(t *testing.T) {
	le := binary.LittleEndian
	var buf bytes.Buffer
	buf.WriteString("RIFF\x00\x00\x00\x00WAVE")
	// odd-sized LIST chunk (needs a pad byte) before fmt
	buf.WriteString("LIST")
	_ = binary.Write(&buf, le, uint32(3))
	buf.Write([]byte{'a', 'b', 'c', 0})
	hdr := putWAVHeader(44100, 1, 4)
	buf.Write(hdr[12:]) // fmt + data headers
	buf.Write([]byte{5, 0, 6, 0})
	pcm, rate, ch, err := ReadWAV(&buf)
	require.NoError(t, err)
	assert.Equal(t, []int16{5, 6}, pcm)
	assert.Equal(t, 44100, rate)
	assert.Equal(t, 1, ch)
}

func TestReadWAVTruncatedAndUnpatched(t *testing.T) {
	var full bytes.Buffer
	require.NoError(t, WriteWAV(&full, []int16{1, 2, 3, 4, 5, 6}, 8000, 2))
	// Truncated mid-frame: complete frames are returned.
	pcm, _, _, err := ReadWAV(bytes.NewReader(full.Bytes()[:44+2*5]))
	require.NoError(t, err)
	assert.Equal(t, []int16{1, 2, 3, 4}, pcm)
	// Unknown data size (streaming writer never closed).
	raw := append([]byte{}, full.Bytes()...)
	binary.LittleEndian.PutUint32(raw[40:], wavUnknownSize)
	pcm, _, _, err = ReadWAV(bytes.NewReader(raw))
	require.NoError(t, err)
	assert.Equal(t, []int16{1, 2, 3, 4, 5, 6}, pcm)
}

func TestReadWAVErrors(t *testing.T) {
	_, _, _, err := ReadWAV(bytes.NewReader(nil))
	require.ErrorIs(t, err, ErrInvalidWAV)
	_, _, _, err = ReadWAV(bytes.NewReader([]byte("RIFX\x00\x00\x00\x00WAVEfmt ")))
	require.ErrorIs(t, err, ErrInvalidWAV)

	var buf bytes.Buffer
	require.NoError(t, WriteWAV(&buf, []int16{1, 2}, 8000, 1))
	// 8-bit is unsupported.
	bad := append([]byte{}, buf.Bytes()...)
	binary.LittleEndian.PutUint16(bad[34:], 8)
	_, _, _, err = ReadWAV(bytes.NewReader(bad))
	require.ErrorIs(t, err, ErrUnsupportedWAV)
	// IEEE float (tag 3) is unsupported.
	bad = append([]byte{}, buf.Bytes()...)
	binary.LittleEndian.PutUint16(bad[20:], 3)
	_, _, _, err = ReadWAV(bytes.NewReader(bad))
	require.ErrorIs(t, err, ErrUnsupportedWAV)
	// No data chunk.
	_, _, _, err = ReadWAV(bytes.NewReader(buf.Bytes()[:36]))
	require.ErrorIs(t, err, ErrInvalidWAV)
	// data before fmt.
	_, _, _, err = ReadWAV(bytes.NewReader(append([]byte("RIFF\x00\x00\x00\x00WAVEdata\x02\x00\x00\x00ab"), 0)))
	require.ErrorIs(t, err, ErrInvalidWAV)
}

func TestWAVWriterSeekablePatchesHeader(t *testing.T) {
	f := &memFile{}
	w, err := NewWAVWriter(f, 16000, 2)
	require.NoError(t, err)
	assert.True(t, w.Seekable())
	require.NoError(t, w.Write([]int16{1, 2, 3, 4}))

	// Before Close the file is still readable (sizes are "unknown").
	pcm, _, _, err := ReadWAV(bytes.NewReader(f.data))
	require.NoError(t, err)
	assert.Equal(t, []int16{1, 2, 3, 4}, pcm)

	require.NoError(t, w.Write([]int16{5, 6}))
	require.ErrorIs(t, w.Write([]int16{7}), ErrPartialFrame)
	assert.Equal(t, int64(3), w.Frames())
	require.NoError(t, w.Close())
	require.NoError(t, w.Close(), "idempotent")
	require.ErrorIs(t, w.Write([]int16{1, 2}), ErrClosed)

	assert.Equal(t, uint32(12), binary.LittleEndian.Uint32(f.data[40:]))
	assert.Equal(t, uint32(36+12), binary.LittleEndian.Uint32(f.data[4:]))
	assert.Equal(t, int64(len(f.data)), f.pos, "position restored to end")
	pcm, rate, ch, err := ReadWAV(bytes.NewReader(f.data))
	require.NoError(t, err)
	assert.Equal(t, []int16{1, 2, 3, 4, 5, 6}, pcm)
	assert.Equal(t, 16000, rate)
	assert.Equal(t, 2, ch)
}

func TestWAVWriterNonSeekableBuffersUntilClose(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWAVWriter(&buf, 8000, 1)
	require.NoError(t, err)
	assert.False(t, w.Seekable())
	require.NoError(t, w.Write([]int16{9, 8, 7}))
	assert.Zero(t, buf.Len(), "nothing written before Close")
	require.NoError(t, w.Close())
	pcm, rate, ch, err := ReadWAV(&buf)
	require.NoError(t, err)
	assert.Equal(t, []int16{9, 8, 7}, pcm)
	assert.Equal(t, 8000, rate)
	assert.Equal(t, 1, ch)
}

func TestWAVWriterStartsAtOffset(t *testing.T) {
	f := &memFile{}
	_, _ = f.Write([]byte("PREFIX"))
	w, err := NewWAVWriter(f, 8000, 1)
	require.NoError(t, err)
	require.NoError(t, w.Write([]int16{1, 2}))
	require.NoError(t, w.Close())
	assert.Equal(t, "PREFIX", string(f.data[:6]))
	pcm, _, _, err := ReadWAV(bytes.NewReader(f.data[6:]))
	require.NoError(t, err)
	assert.Equal(t, []int16{1, 2}, pcm)
}

func TestNewWAVWriterInvalid(t *testing.T) {
	_, err := NewWAVWriter(&bytes.Buffer{}, 0, 1)
	require.ErrorIs(t, err, ErrInvalidRate)
	_, err = NewWAVWriter(&bytes.Buffer{}, 8000, 0)
	require.Error(t, err)
}

func BenchmarkWriteWAV(b *testing.B) {
	pcm := sineWave(16000, 440, 9000, 32000)
	b.SetBytes(int64(len(pcm)) * 2)
	for b.Loop() {
		_ = WriteWAV(io.Discard, pcm, 16000, 1)
	}
}
