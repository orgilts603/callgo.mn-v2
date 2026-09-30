// Package phone normalises and formats telephone numbers, with first-class
// support for Mongolian (+976) numbers as they are typed by humans or found
// in spreadsheets.
package phone

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

const (
	// CountryCodeMN is the Mongolian calling code (without '+').
	CountryCodeMN = "976"

	mnNationalLen = 8
	minIntlDigits = 8
	maxIntlDigits = 15
	maxRawLen     = 64
)

// ErrInvalid is returned (wrapped) for every number that cannot be normalised.
var ErrInvalid = errors.New("invalid phone number")

// ErrEmpty is returned when the input has no digits at all.
var ErrEmpty = fmt.Errorf("%w: empty", ErrInvalid)

// countryCodes maps the ISO-3166 alpha-2 codes accepted as a default country
// to their calling codes. Mongolia is the default.
var countryCodes = map[string]string{
	"MN": "976", "US": "1", "CA": "1", "RU": "7", "KZ": "7", "CN": "86",
	"KR": "82", "JP": "81", "GB": "44", "DE": "49", "FR": "33", "TR": "90",
	"IN": "91", "SG": "65", "AU": "61", "HK": "852", "TW": "886",
}

// Normalize converts a human-typed number to E.164.
//
// Mongolian numbers are accepted in any of these forms: "99112233",
// "9911 2233", "+976 9911-2233", "976-99112233", "(976) 9911 2233",
// "0099112233" and "00976 99112233". A Mongolian national number has 8
// digits and starts with 5, 6, 7, 8 or 9 (mobile, landline and VoIP).
// Other international numbers must be written with a leading "+" or "00"
// and have 8-15 digits including the country code. Spaces, dashes,
// parentheses, dots and non-ASCII (e.g. full-width) digits are handled;
// anything else (letters, symbols) is rejected.
func Normalize(raw string) (string, error) {
	return NormalizeFor(raw, "")
}

// NormalizeFor is Normalize with a configurable default country for numbers
// written without any international prefix. defaultCountry is an ISO alpha-2
// code ("MN", "US", ...), a calling code ("+82", "82") or empty for "MN".
func NormalizeFor(raw, defaultCountry string) (string, error) {
	cc, err := callingCode(defaultCountry)
	if err != nil {
		return "", err
	}
	digits, plus, err := clean(raw)
	if err != nil {
		return "", err
	}
	switch {
	case plus:
		return international(digits, raw)
	case strings.HasPrefix(digits, "00"):
		rest := digits[2:]
		// "0099112233": the 00 exit prefix followed by an 8-digit Mongolian
		// national number (a common local habit) is Mongolian.
		if len(rest) == mnNationalLen {
			if e164, err := mongolian(rest, raw); err == nil {
				return e164, nil
			}
		}
		return international(rest, raw)
	case cc == CountryCodeMN:
		return mongolianLocal(digits, raw)
	default:
		return otherLocal(cc, digits, raw)
	}
}

func invalid(raw string) error {
	if len(raw) > 32 {
		raw = raw[:32] + "..."
	}
	return fmt.Errorf("%w: %q", ErrInvalid, raw)
}

func callingCode(country string) (string, error) {
	c := strings.TrimSpace(country)
	if c == "" {
		return CountryCodeMN, nil
	}
	if cc, ok := countryCodes[strings.ToUpper(c)]; ok {
		return cc, nil
	}
	c = strings.TrimPrefix(c, "+")
	if c != "" && len(c) <= 3 && strings.Trim(c, "0123456789") == "" && c[0] != '0' {
		return c, nil
	}
	return "", fmt.Errorf("%w: unsupported default country %q", ErrInvalid, country)
}

// clean strips formatting and returns ASCII digits plus whether the number
// started with '+'.
func clean(raw string) (digits string, plus bool, err error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false, ErrEmpty
	}
	if len(s) > maxRawLen {
		return "", false, invalid(raw)
	}
	// Spreadsheets often turn 99112233 into 99112233.0.
	if strings.HasSuffix(s, ".0") && strings.Count(s, ".") == 1 {
		s = s[:len(s)-2]
	}
	var b strings.Builder
	started := false
	for _, r := range s {
		switch {
		case r == '+' || r == '＋':
			if started {
				return "", false, invalid(raw)
			}
			plus, started = true, true
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			started = true
		case unicode.IsSpace(r), isDash(r), r == '(', r == ')', r == '.':
		case unicode.Is(unicode.Nd, r):
			b.WriteByte(byte('0' + digitValue(r)))
			started = true
		default:
			return "", false, invalid(raw)
		}
	}
	if b.Len() == 0 {
		return "", false, ErrEmpty
	}
	return b.String(), plus, nil
}

func isDash(r rune) bool {
	switch r {
	case '-', '‐', '‑', '‒', '–', '—', '−':
		return true
	}
	return false
}

// digitValue returns the numeric value of a Unicode decimal digit. Digit
// blocks are contiguous runs of ten starting at zero.
func digitValue(r rune) int {
	for _, rg := range unicode.Nd.R16 {
		if r >= rune(rg.Lo) && r <= rune(rg.Hi) {
			return int(r-rune(rg.Lo)) % 10
		}
	}
	for _, rg := range unicode.Nd.R32 {
		if r >= rune(rg.Lo) && r <= rune(rg.Hi) {
			return int(r-rune(rg.Lo)) % 10
		}
	}
	return 0
}

// mongolian validates an 8-digit national number.
func mongolian(national, raw string) (string, error) {
	if len(national) != mnNationalLen || national[0] < '5' || national[0] > '9' {
		return "", invalid(raw)
	}
	return "+" + CountryCodeMN + national, nil
}

func mongolianLocal(digits, raw string) (string, error) {
	switch {
	case len(digits) == mnNationalLen:
		return mongolian(digits, raw)
	case len(digits) == mnNationalLen+len(CountryCodeMN) && strings.HasPrefix(digits, CountryCodeMN):
		return mongolian(digits[len(CountryCodeMN):], raw)
	}
	return "", invalid(raw)
}

func otherLocal(cc, digits, raw string) (string, error) {
	digits = strings.TrimPrefix(digits, "0") // national trunk prefix
	total := len(cc) + len(digits)
	if digits == "" || total < minIntlDigits || total > maxIntlDigits {
		return "", invalid(raw)
	}
	return "+" + cc + digits, nil
}

// international handles digits that follow a "+" or "00".
func international(digits, raw string) (string, error) {
	if strings.HasPrefix(digits, CountryCodeMN) {
		if len(digits) != len(CountryCodeMN)+mnNationalLen {
			return "", invalid(raw)
		}
		return mongolian(digits[len(CountryCodeMN):], raw)
	}
	if digits == "" || digits[0] == '0' || len(digits) < minIntlDigits || len(digits) > maxIntlDigits {
		return "", invalid(raw)
	}
	return "+" + digits, nil
}

// IsMongolian reports whether e164 is a valid Mongolian E.164 number.
func IsMongolian(e164 string) bool {
	if len(e164) != 1+len(CountryCodeMN)+mnNationalLen || !strings.HasPrefix(e164, "+"+CountryCodeMN) {
		return false
	}
	national := e164[1+len(CountryCodeMN):]
	if national[0] < '5' || national[0] > '9' {
		return false
	}
	return strings.Trim(national, "0123456789") == ""
}

// Pretty formats an E.164 number for display: "+976 9911 2233". Numbers that
// are not Mongolian are returned unchanged.
func Pretty(e164 string) string {
	if !IsMongolian(e164) {
		return e164
	}
	n := e164[4:]
	return "+976 " + n[:4] + " " + n[4:]
}

// Mask hides the middle of a number for logs and UIs: "+976 99** **33".
// Other numbers keep only the first three characters and the last two.
func Mask(e164 string) string {
	if IsMongolian(e164) {
		n := e164[4:]
		return "+976 " + n[:2] + "** **" + n[6:]
	}
	if len(e164) <= 5 {
		return strings.Repeat("*", len(e164))
	}
	return e164[:3] + strings.Repeat("*", len(e164)-5) + e164[len(e164)-2:]
}
