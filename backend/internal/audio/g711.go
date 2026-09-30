package audio

const (
	muLawBias = 0x84
	muLawClip = 32635
)

// muLawDecodeTable maps every mu-law byte to its linear 16-bit value. It is
// built once at package initialisation and never modified.
var muLawDecodeTable = buildMuLawDecodeTable()

// aLawDecodeTable maps every A-law byte to its linear 16-bit value.
var aLawDecodeTable = buildALawDecodeTable()

// muLawEncodeTable maps sample+32768 to its mu-law byte, making encoding a
// single lookup.
var muLawEncodeTable = buildMuLawEncodeTable()

// aLawEncodeTable maps sample+32768 to its A-law byte.
var aLawEncodeTable = buildALawEncodeTable()

func muLawDecodeByte(b byte) int16 {
	u := ^b
	t := (int32(u&0x0F) << 3) + muLawBias
	t <<= (u & 0x70) >> 4
	if u&0x80 != 0 {
		return int16(muLawBias - t)
	}
	return int16(t - muLawBias)
}

func muLawEncodeSample(s int16) byte {
	v := int32(s)
	var sign int32
	if v < 0 {
		v = -v
		sign = 0x80
	}
	if v > muLawClip {
		v = muLawClip
	}
	v += muLawBias
	exp := int32(7)
	for mask := int32(0x4000); v&mask == 0 && exp > 0; mask >>= 1 {
		exp--
	}
	mant := (v >> (uint(exp) + 3)) & 0x0F
	return byte(^(sign | exp<<4 | mant))
}

func aLawDecodeByte(b byte) int16 {
	a := b ^ 0x55
	t := int32(a&0x0F) << 4
	seg := int32(a&0x70) >> 4
	switch seg {
	case 0:
		t += 8
	case 1:
		t += 0x108
	default:
		t += 0x108
		t <<= uint(seg - 1)
	}
	if a&0x80 != 0 {
		return int16(t)
	}
	return int16(-t)
}

var aLawSegEnd = [8]int32{0x1F, 0x3F, 0x7F, 0xFF, 0x1FF, 0x3FF, 0x7FF, 0xFFF}

func aLawEncodeSample(s int16) byte {
	v := int32(s) >> 3
	var mask byte
	if v >= 0 {
		mask = 0xD5
	} else {
		mask = 0x55
		v = -v - 1
	}
	seg := 8
	for i, end := range aLawSegEnd {
		if v <= end {
			seg = i
			break
		}
	}
	if seg >= 8 {
		return 0x7F ^ mask
	}
	aval := byte(seg << 4)
	if seg < 2 {
		aval |= byte(v>>1) & 0x0F
	} else {
		aval |= byte(v>>uint(seg)) & 0x0F
	}
	return aval ^ mask
}

func buildMuLawDecodeTable() [256]int16 {
	var t [256]int16
	for i := range t {
		t[i] = muLawDecodeByte(byte(i))
	}
	return t
}

func buildALawDecodeTable() [256]int16 {
	var t [256]int16
	for i := range t {
		t[i] = aLawDecodeByte(byte(i))
	}
	return t
}

func buildMuLawEncodeTable() []byte {
	t := make([]byte, 1<<16)
	for i := range t {
		t[i] = muLawEncodeSample(int16(i - 32768))
	}
	return t
}

func buildALawEncodeTable() []byte {
	t := make([]byte, 1<<16)
	for i := range t {
		t[i] = aLawEncodeSample(int16(i - 32768))
	}
	return t
}

// MuLawDecode decodes G.711 mu-law bytes to 16-bit linear PCM.
func MuLawDecode(in []byte) []int16 {
	out := make([]int16, len(in))
	for i, b := range in {
		out[i] = muLawDecodeTable[b]
	}
	return out
}

// MuLawEncode encodes 16-bit linear PCM to G.711 mu-law bytes. Input is
// clipped at +-32635 as specified by the standard.
func MuLawEncode(in []int16) []byte {
	out := make([]byte, len(in))
	for i, s := range in {
		out[i] = muLawEncodeTable[int(s)+32768]
	}
	return out
}

// ALawDecode decodes G.711 A-law bytes to 16-bit linear PCM.
func ALawDecode(in []byte) []int16 {
	out := make([]int16, len(in))
	for i, b := range in {
		out[i] = aLawDecodeTable[b]
	}
	return out
}

// ALawEncode encodes 16-bit linear PCM to G.711 A-law bytes.
func ALawEncode(in []int16) []byte {
	out := make([]byte, len(in))
	for i, s := range in {
		out[i] = aLawEncodeTable[int(s)+32768]
	}
	return out
}
