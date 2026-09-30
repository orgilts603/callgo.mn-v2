package lexicon

import "strings"

// maxDiffCells bounds the LCS table so pathological inputs stay cheap.
const maxDiffCells = 4_000_000

// DiffWord compares a turn's original text with the admin's edited text and,
// when exactly one contiguous region changed, returns that region as a
// (wrong, correct) pair suitable for a lexicon entry.
//
// Texts are tokenised on whitespace and aligned with a longest-common-
// subsequence; ok is false when nothing changed, when more than one separate
// region changed, or when no usable pair can be derived. Pure insertions and
// deletions are widened with the neighbouring original word so that wrong is
// never empty. Leading/trailing punctuation is stripped from both sides.
func DiffWord(original, edited string) (wrong, correct string, ok bool) {
	a := strings.Fields(original)
	b := strings.Fields(edited)

	// Trim the common prefix and suffix; what is left is the changed core.
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ca, cb := a[pre:len(a)-suf], b[pre:len(b)-suf]
	if len(ca) == 0 && len(cb) == 0 {
		return "", "", false
	}
	// Any token shared inside the core splits it into several regions.
	if len(ca) > 0 && len(cb) > 0 {
		if len(ca)*len(cb) > maxDiffCells || lcsLen(ca, cb) > 0 {
			return "", "", false
		}
	}
	// Widen pure insertions / deletions with one neighbouring original word.
	switch {
	case len(ca) == 0 || len(cb) == 0:
		switch {
		case pre > 0:
			ca = a[pre-1 : len(a)-suf]
			cb = append([]string{a[pre-1]}, cb...)
		case suf > 0:
			ca = a[pre : len(a)-suf+1]
			cb = append(append([]string{}, cb...), a[len(a)-suf])
		default:
			return "", "", false
		}
	}
	wrong = trimPunct(strings.Join(ca, " "))
	correct = trimPunct(strings.Join(cb, " "))
	if wrong == "" || correct == "" || wrong == correct {
		return "", "", false
	}
	return wrong, correct, true
}

func trimPunct(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return !isWord(r) })
}

// lcsLen returns the length of the longest common subsequence of a and b.
func lcsLen(a, b []string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			switch {
			case a[i-1] == b[j-1]:
				cur[j] = prev[j-1] + 1
			case prev[j] >= cur[j-1]:
				cur[j] = prev[j]
			default:
				cur[j] = cur[j-1]
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
