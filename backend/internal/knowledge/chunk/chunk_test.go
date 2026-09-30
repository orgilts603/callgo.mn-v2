package chunk

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestSplitBasics(t *testing.T) {
	text := "Intro line one.\nIntro line two.\n\n# Хүргэлт\n\nХүргэлт   24 цагт.\n\n## Үнэ ##\nҮнэ 5000₮.\n\n#hashtag is text\n"
	got := Split(text, 1200, 200)
	require.Equal(t, []Chunk{
		{Heading: "", Content: "Intro line one. Intro line two.", Seq: 0},
		{Heading: "Хүргэлт", Content: "Хүргэлт 24 цагт.", Seq: 1},
		{Heading: "Үнэ", Content: "Үнэ 5000₮.\n#hashtag is text", Seq: 2},
	}, got)
	require.Empty(t, Split(" \n\n# Only heading\n\n", 100, 10))
	require.Empty(t, Split("", 100, 10))
}

func TestSplitPacksParagraphsWithOverlap(t *testing.T) {
	var paras []string
	for i := 0; i < 12; i++ {
		paras = append(paras, fmt.Sprintf("Параграф %d: энэ бол туршилтын өгүүлбэр юм.", i))
	}
	text := strings.Join(paras, "\n\n")
	chunks := Split(text, 200, 40)
	require.Greater(t, len(chunks), 2)
	for i, c := range chunks {
		require.Equal(t, i, c.Seq)
		require.LessOrEqual(t, utf8.RuneCountInString(c.Content), 240)
		if i == 0 {
			continue
		}
		// The chunk starts with a suffix of its predecessor, then a space.
		prev := chunks[i-1].Content
		idx := strings.Index(c.Content, " ")
		found := false
		for idx > 0 {
			if strings.HasSuffix(prev, c.Content[:idx]) {
				found = true
				break
			}
			next := strings.Index(c.Content[idx+1:], " ")
			if next < 0 || idx+1+next > 50*4 {
				break
			}
			idx += 1 + next
		}
		require.Truef(t, found, "chunk %d does not start with an overlap: %q", i, c.Content)
	}
	// Paragraph boundaries are respected: no paragraph is cut.
	for _, p := range paras {
		inOne := false
		for _, c := range chunks {
			if strings.Contains(c.Content, p) {
				inOne = true
			}
		}
		require.Truef(t, inOne, "paragraph %q split", p)
	}
}

func TestSplitLongParagraphAtSentences(t *testing.T) {
	s1 := strings.Repeat("а", 50) + "."
	s2 := strings.Repeat("б", 50) + "!"
	s3 := strings.Repeat("в", 50) + "…»"
	s4 := strings.Repeat("г", 50) + "?"
	text := strings.Join([]string{s1, s2, s3, s4}, " ")
	chunks := Split(text, 110, 0)
	require.Equal(t, []string{s1 + " " + s2, s3 + " " + s4}, contents(chunks))

	// Decimal points do not end sentences; an overlong word is hard-split.
	require.Equal(t, []string{"Үнэ 3.5 сая.", "Дараагийн өгүүлбэр."}, sentencesOrPieces("Үнэ 3.5 сая. Дараагийн өгүүлбэр.", 22))
	long := strings.Repeat("ж", 250)
	chunks = Split(long, 100, 0)
	require.Equal(t, []string{long[:200], long[200:400], long[400:]}, contents(chunks))
}

func sentencesOrPieces(text string, size int) []string { return contents(Split(text, size, 0)) }

func contents(cs []Chunk) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Content
	}
	return out
}

func TestSplitArgs(t *testing.T) {
	text := strings.Repeat("үг ", 2000)
	def := Split(text, 0, -5)
	for _, c := range def {
		require.LessOrEqual(t, utf8.RuneCountInString(c.Content), DefaultSize)
	}
	// Overlap is capped at half the size.
	for _, c := range Split(text, 100, 1000) {
		require.LessOrEqual(t, utf8.RuneCountInString(c.Content), 150)
	}
}

var vocab = []string{"захиалга", "хүргэлт", "үнэ", "төгрөг", "QPay", "delivery", "24", "цаг", "Улаанбаатар",
	"буцаалт", "хоног", "оператор", "a", "3.5", "“quoted”", "(хаалт)", "ё", "ү", "ө", strings.Repeat("х", 70)}

var enders = []string{"", "", "", ".", "!", "?", "…", ",", ";"}

// randomDoc builds a markdown-ish document from a seed.
func randomDoc(r *rand.Rand) string {
	var b strings.Builder
	paras := 1 + r.IntN(25)
	for p := 0; p < paras; p++ {
		switch r.IntN(6) {
		case 0:
			fmt.Fprintf(&b, "%s %s\n", strings.Repeat("#", 1+r.IntN(3)), vocab[r.IntN(len(vocab))])
		case 1:
			b.WriteString("\n\n  \t\n")
		}
		words := 1 + r.IntN(120)
		for w := 0; w < words; w++ {
			b.WriteString(vocab[r.IntN(len(vocab))])
			b.WriteString(enders[r.IntN(len(enders))])
			switch r.IntN(12) {
			case 0:
				b.WriteString("\n")
			case 1:
				b.WriteString("   ")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString("\n\n")
	}
	return b.String()
}

// bodyRunes counts non-space runes of text outside heading lines.
func bodyRunes(text string) map[rune]int {
	m := map[rune]int{}
	for _, line := range strings.Split(text, "\n") {
		if _, ok := headingLine(line); ok {
			continue
		}
		for _, r := range line {
			if !unicode.IsSpace(r) {
				m[r]++
			}
		}
	}
	return m
}

func TestSplitProperties(t *testing.T) {
	for seed := uint64(0); seed < 300; seed++ {
		r := rand.New(rand.NewPCG(seed, seed*7+1))
		text := randomDoc(r)
		size := 40 + r.IntN(400)
		overlap := r.IntN(size/2 + 1)
		chunks := Split(text, size, overlap)

		// Deterministic.
		require.Equal(t, chunks, Split(text, size, overlap), "seed %d", seed)

		got := map[rune]int{}
		for i, c := range chunks {
			require.Equal(t, i, c.Seq)
			n := utf8.RuneCountInString(c.Content)
			require.LessOrEqualf(t, n, size+overlap, "seed %d chunk %d", seed, i)
			require.NotEmpty(t, strings.TrimSpace(c.Content))
			require.Equal(t, c.Content, strings.TrimSpace(c.Content))
			require.NotContains(t, c.Content, "  ")
			for _, r := range c.Content {
				if !unicode.IsSpace(r) {
					got[r]++
				}
			}
		}
		// Every non-space body rune appears in the chunks at least as often
		// as in the input (overlap only adds copies).
		for r, want := range bodyRunes(text) {
			require.GreaterOrEqualf(t, got[r], want, "seed %d rune %q", seed, r)
		}
		// Without overlap the chunks are exactly the normalised body.
		if overlap == 0 {
			var all []string
			for _, c := range chunks {
				all = append(all, strings.Fields(c.Content)...)
			}
			var want []string
			for _, p := range paragraphs(text) {
				want = append(want, strings.Fields(p.text)...)
			}
			require.Equal(t, strings.Join(want, ""), strings.Join(all, ""), "seed %d", seed)
		}
	}
}
