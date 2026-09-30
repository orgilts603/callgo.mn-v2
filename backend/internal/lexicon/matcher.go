package lexicon

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Hit records one replacement made by [Matcher.Apply].
type Hit struct {
	// ID of the lexicon entry that fired.
	ID uuid.UUID `json:"id"`
	// Wrong is the text as it appeared in the input (original casing).
	Wrong string `json:"wrong"`
	// Correct is the text that was written to the output (casing applied).
	Correct string `json:"correct"`
	// Pos is the rune offset of the match in the input text.
	Pos int `json:"pos"`
}

// rule is one compiled STT replacement.
type rule struct {
	id      uuid.UUID
	wrong   string // trimmed original, used for the exact-case shortcut
	correct string
	pat     []rune // lowercased, whitespace runs collapsed to a single ' '
	wordHd  bool   // pat[0] is a letter/digit: needs a left word boundary
	wordTl  bool   // last pat rune is a letter/digit: needs a right word boundary
}

// Matcher is an immutable, concurrency-safe whole-word / phrase replacer.
// Build one with [Compile]. A nil *Matcher is valid and matches nothing.
type Matcher struct {
	byFirst  map[string][]*rule // lowercased first word -> rules, longest first
	anyStart []*rule            // rules whose pattern starts with a non-word rune
	hints    []domain.LexiconCorrection
	n        int
}

// Compile builds a Matcher from lexicon entries.
//
// Only entries with scope stt or both take part in [Matcher.Apply]; entries
// with scope tts or both that carry a Phonetic value are exposed through
// [Matcher.TTSHints]. Entries with an empty Wrong are ignored.
func Compile(entries []domain.LexiconCorrection) *Matcher {
	m := &Matcher{byFirst: make(map[string][]*rule)}
	for _, e := range entries {
		if (e.Scope == domain.ScopeTTS || e.Scope == domain.ScopeBoth) && strings.TrimSpace(e.Phonetic) != "" && strings.TrimSpace(e.Wrong) != "" {
			m.hints = append(m.hints, e)
		}
		if e.Scope != domain.ScopeSTT && e.Scope != domain.ScopeBoth {
			continue
		}
		r := newRule(e)
		if r == nil {
			continue
		}
		m.n++
		if !r.wordHd {
			m.anyStart = append(m.anyStart, r)
			continue
		}
		key := firstWordKey(r.pat)
		m.byFirst[key] = append(m.byFirst[key], r)
	}
	longestFirst := func(rs []*rule) {
		sort.SliceStable(rs, func(i, j int) bool { return len(rs[i].pat) > len(rs[j].pat) })
	}
	for _, rs := range m.byFirst {
		longestFirst(rs)
	}
	longestFirst(m.anyStart)
	return m
}

func newRule(e domain.LexiconCorrection) *rule {
	fields := strings.Fields(e.Wrong)
	if len(fields) == 0 {
		return nil
	}
	wrong := strings.Join(fields, " ")
	pat := make([]rune, 0, len(wrong))
	for _, r := range wrong {
		pat = append(pat, unicode.ToLower(r))
	}
	return &rule{
		id:      e.ID,
		wrong:   wrong,
		correct: e.Correct,
		pat:     pat,
		wordHd:  isWord(pat[0]),
		wordTl:  isWord(pat[len(pat)-1]),
	}
}

func firstWordKey(pat []rune) string {
	n := 0
	for n < len(pat) && isWord(pat[n]) {
		n++
	}
	return string(pat[:n])
}

func isWord(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// Len reports how many STT rules the matcher holds.
func (m *Matcher) Len() int {
	if m == nil {
		return 0
	}
	return m.n
}

// TTSHints returns the corrections (scope tts or both) that carry a phonetic
// spelling, for the agent's pronunciation lexicon. The slice is a copy.
func (m *Matcher) TTSHints() []domain.LexiconCorrection {
	if m == nil || len(m.hints) == 0 {
		return nil
	}
	out := make([]domain.LexiconCorrection, len(m.hints))
	copy(out, m.hints)
	return out
}

// Apply replaces every whole-word / whole-phrase occurrence of a lexicon entry
// in text. Matching is case-insensitive; the replacement follows the casing of
// the matched text (Title-case and ALL CAPS are preserved). Longer phrases win
// over shorter ones and replaced regions never overlap. When nothing matches
// the input is returned unchanged with nil hits.
func (m *Matcher) Apply(text string) (string, []Hit) {
	if m == nil || m.n == 0 || text == "" {
		return text, nil
	}
	rs := []rune(text)
	var (
		sb       strings.Builder
		hits     []Hit
		copyFrom int
		key      = make([]byte, 0, 32)
	)
	i := 0
	for i < len(rs) {
		var best *rule
		bestEnd := -1
		atWordStart := isWord(rs[i]) && (i == 0 || !isWord(rs[i-1]))
		if atWordStart {
			key = key[:0]
			for j := i; j < len(rs) && isWord(rs[j]); j++ {
				key = utf8.AppendRune(key, unicode.ToLower(rs[j]))
			}
			for _, r := range m.byFirst[string(key)] {
				if end := matchAt(rs, i, r); end >= 0 {
					best, bestEnd = r, end
					break
				}
			}
		}
		if best == nil {
			for _, r := range m.anyStart {
				if end := matchAt(rs, i, r); end >= 0 {
					best, bestEnd = r, end
					break
				}
			}
		}
		if best == nil {
			i++
			if atWordStart && len(m.anyStart) == 0 {
				for i < len(rs) && isWord(rs[i]) {
					i++
				}
			}
			continue
		}
		if hits == nil {
			sb.Grow(len(text) + 16)
		}
		sb.WriteString(string(rs[copyFrom:i]))
		matched := string(rs[i:bestEnd])
		repl := applyCase(matched, best)
		sb.WriteString(repl)
		hits = append(hits, Hit{ID: best.id, Wrong: matched, Correct: repl, Pos: i})
		i = bestEnd
		copyFrom = bestEnd
	}
	if hits == nil {
		return text, nil
	}
	sb.WriteString(string(rs[copyFrom:]))
	return sb.String(), hits
}

// matchAt tests rule r at text[i:] and returns the end index of the match or -1.
func matchAt(text []rune, i int, r *rule) int {
	if r.wordHd && i > 0 && isWord(text[i-1]) {
		return -1
	}
	j := i
	for _, p := range r.pat {
		if j >= len(text) {
			return -1
		}
		if p == ' ' {
			if !unicode.IsSpace(text[j]) {
				return -1
			}
			for j < len(text) && unicode.IsSpace(text[j]) {
				j++
			}
			continue
		}
		if unicode.ToLower(text[j]) != p {
			return -1
		}
		j++
	}
	if r.wordTl && j < len(text) && isWord(text[j]) {
		return -1
	}
	return j
}

// applyCase maps the casing of the matched text onto the rule's replacement.
func applyCase(matched string, r *rule) string {
	if matched == r.wrong {
		return r.correct // exact spelling the admin typed: use verbatim
	}
	var upper, lower int
	firstUpper := false
	seenFirst := false
	for _, c := range matched {
		if !unicode.IsLetter(c) {
			continue
		}
		if !seenFirst {
			seenFirst = true
			firstUpper = unicode.IsUpper(c)
		}
		if unicode.IsUpper(c) {
			upper++
		} else if unicode.IsLower(c) {
			lower++
		}
	}
	switch {
	case !seenFirst:
		return r.correct
	case upper >= 2 && lower == 0:
		return strings.ToUpper(r.correct)
	case firstUpper:
		return capitalize(r.correct)
	}
	return r.correct
}

func capitalize(s string) string {
	c, size := utf8.DecodeRuneInString(s)
	if c == utf8.RuneError || unicode.IsUpper(c) {
		return s
	}
	return string(unicode.ToUpper(c)) + s[size:]
}
