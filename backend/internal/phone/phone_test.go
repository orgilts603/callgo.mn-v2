package phone

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain 8 digits", "99112233", "+97699112233"},
		{"spaced", "9911 2233", "+97699112233"},
		{"plus with dash", "+976 9911-2233", "+97699112233"},
		{"country code dash", "976-99112233", "+97699112233"},
		{"country code space", "976 99112233", "+97699112233"},
		{"already e164", "+97699112233", "+97699112233"},
		{"00 + 8 digits", "0099112233", "+97699112233"},
		{"00976", "00976 9911 2233", "+97699112233"},
		{"parentheses", "(976) 9911 2233", "+97699112233"},
		{"dots", "9911.2233", "+97699112233"},
		{"surrounding space", "  99112233\t", "+97699112233"},
		{"nbsp", "9911 2233", "+97699112233"},
		{"unicode dash", "9911–2233", "+97699112233"},
		{"fullwidth digits", "９９１１２２３３", "+97699112233"},
		{"arabic-indic digits", "٩٩١١٢٢٣٣", "+97699112233"},
		{"fullwidth plus", "＋976 99112233", "+97699112233"},
		{"excel float", "99112233.0", "+97699112233"},
		{"landline 7", "7011 1234", "+97670111234"},
		{"mobile 5", "50112233", "+97650112233"},
		{"mobile 6", "60112233", "+97660112233"},
		{"mobile 8", "88112233", "+97688112233"},
		{"international US", "+1 (415) 555-2671", "+14155552671"},
		{"international 00 UK", "0044 20 7946 0958", "+442079460958"},
		{"international KR", "+82-10-1234-5678", "+821012345678"},
		{"min intl length", "+1234567 8", "+12345678"},
		{"max intl length", "+123456789012345", "+123456789012345"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Normalize(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestNormalizeInvalid(t *testing.T) {
	tests := []struct{ name, in string }{
		{"empty", ""},
		{"blank", "   "},
		{"letters", "9911abcd"},
		{"alpha only", "call me"},
		{"too short", "9911223"},
		{"too long", "991122334"},
		{"starts with 4", "49112233"},
		{"starts with 1", "11112233"},
		{"starts with 0", "09112233"},
		{"976 short", "976 9911223"},
		{"+976 short", "+976 9911223"},
		{"+976 long", "+976 991122334"},
		{"+976 bad first", "+976 41122334"},
		{"976 bad first digit", "97641122334"},
		{"no prefix foreign", "4155552671"},
		{"intl too short", "+1234567"},
		{"intl too long", "+1234567890123456"},
		{"intl leading zero", "+0123456789"},
		{"plus in middle", "9911+2233"},
		{"double plus", "++97699112233"},
		{"symbols", "9911#2233"},
		{"scientific", "9.9112233E+7"},
		{"00 only", "00"},
		{"very long", "99999999999999999999999999999999999999999999999999999999999999999999"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Normalize(tc.in)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrInvalid))
			assert.Empty(t, got)
		})
	}
}

func TestNormalizeFor(t *testing.T) {
	tests := []struct {
		name, in, country, want string
		wantErr                 bool
	}{
		{"default mn", "99112233", "", "+97699112233", false},
		{"explicit MN lower", "99112233", "mn", "+97699112233", false},
		{"US national", "415 555 2671", "US", "+14155552671", false},
		{"KR trunk zero", "010-1234-5678", "KR", "+821012345678", false},
		{"calling code plus", "10 1234 5678", "+82", "+821012345678", false},
		{"calling code bare", "415 555 2671", "1", "+14155552671", false},
		{"prefix wins over default", "+97699112233", "US", "+97699112233", false},
		{"unknown country", "99112233", "ZZ", "", true},
		{"too short for country", "1234", "US", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeFor(tc.in, tc.country)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestEmptyError(t *testing.T) {
	_, err := Normalize("")
	assert.ErrorIs(t, err, ErrEmpty)
	assert.ErrorIs(t, err, ErrInvalid)
}

func TestIsMongolian(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"+97699112233", true},
		{"+97670111234", true},
		{"+97650112233", true},
		{"+14155552671", false},
		{"+9769911223", false},
		{"+976991122334", false},
		{"+97641122334", false},
		{"97699112233", false},
		{"+9769911223a", false},
		{"", false},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, IsMongolian(tc.in), tc.in)
	}
}

func TestPretty(t *testing.T) {
	tests := []struct{ in, want string }{
		{"+97699112233", "+976 9911 2233"},
		{"+97670111234", "+976 7011 1234"},
		{"+14155552671", "+14155552671"},
		{"garbage", "garbage"},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, Pretty(tc.in))
	}
}

func TestMask(t *testing.T) {
	tests := []struct{ in, want string }{
		{"+97699112233", "+976 99** **33"},
		{"+97688001122", "+976 88** **22"},
		{"+14155552671", "+14*******71"},
		{"+1234", "*****"},
		{"", ""},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, Mask(tc.in))
	}
}

func FuzzNormalize(f *testing.F) {
	for _, s := range []string{"99112233", "+976 9911-2233", "0099112233", "+1 415 555 2671", "", "٩٩١١٢٢٣٣"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, err := Normalize(s)
		if err != nil {
			return
		}
		again, err := Normalize(got)
		require.NoError(t, err)
		require.Equal(t, got, again, "normalisation must be idempotent")
	})
}
