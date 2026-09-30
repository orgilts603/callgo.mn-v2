package identity

import "strings"

// maxSlugLen bounds generated organisation slugs.
const maxSlugLen = 40

// mnTranslit maps Mongolian Cyrillic to Latin (MNS 5217-style, simplified).
var mnTranslit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "yo", 'ж': "j",
	'з': "z", 'и': "i", 'й': "i", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o",
	'ө': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ү': "u", 'ф': "f",
	'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sh", 'ъ': "", 'ы': "y", 'ь': "i",
	'э': "e", 'ю': "yu", 'я': "ya",
}

// Slugify turns an organisation name (Latin or Mongolian Cyrillic) into a
// URL-safe slug of lowercase ASCII letters, digits and single dashes.
func Slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if b.Len() >= maxSlugLen {
			break
		}
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dash = false
		case mnTranslit[r] != "":
			b.WriteString(mnTranslit[r])
			dash = false
		case r == 'ъ':
		default:
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	s := b.String()
	if len(s) > maxSlugLen {
		s = s[:maxSlugLen]
	}
	return strings.Trim(s, "-")
}
