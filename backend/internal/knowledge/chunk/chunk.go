// Package chunk splits document text into overlapping passages for
// embedding. It is markdown-heading aware and packs whole paragraphs first,
// splitting long paragraphs at sentence boundaries (Latin and Cyrillic).
package chunk

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Defaults used for non-positive arguments.
const (
	DefaultSize    = 1200
	DefaultOverlap = 200
)

// Chunk is one passage. Heading is the nearest preceding markdown heading;
// Content is at most size+overlap runes; Seq counts from 0.
type Chunk struct {
	Heading string
	Content string
	Seq     int
}

// paragraph is a whitespace-normalised block of text under a heading.
type paragraph struct {
	heading string
	text    string
}

// Split cuts text into chunks of at most size runes of new text, each
// prefixed (within the same heading section) by up to overlap runes carried
// from the end of the previous chunk. A line "# …" (1–6 '#') sets the current
// heading and is not part of any chunk's content. Whitespace runs collapse to
// one space; paragraphs (blank-line separated) are joined with "\n". Empty
// chunks are dropped. Split is deterministic.
func Split(text string, size, overlap int) []Chunk {
	if size <= 0 {
		size = DefaultSize
	}
	if overlap < 0 {
		overlap = 0
	}
	if overlap > size/2 {
		overlap = size / 2
	}
	b := builder{size: size, overlap: overlap}
	for _, p := range paragraphs(text) {
		b.add(p)
	}
	b.flush()
	return b.out
}

// paragraphs splits text into heading-tagged, whitespace-normalised
// paragraphs.
func paragraphs(text string) []paragraph {
	var (
		out     []paragraph
		heading string
		cur     []string
	)
	end := func() {
		if len(cur) == 0 {
			return
		}
		if t := normalize(strings.Join(cur, " ")); t != "" {
			out = append(out, paragraph{heading: heading, text: t})
		}
		cur = cur[:0]
	}
	for _, line := range strings.Split(text, "\n") {
		if h, ok := headingLine(line); ok {
			end()
			heading = h
			continue
		}
		if strings.TrimSpace(line) == "" {
			end()
			continue
		}
		cur = append(cur, line)
	}
	end()
	return out
}

// headingLine recognises "# Title" … "###### Title" (a lone "#" run with no
// text is not a heading).
func headingLine(line string) (string, bool) {
	t := strings.TrimSpace(line)
	n := 0
	for n < len(t) && t[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || n == len(t) || (t[n] != ' ' && t[n] != '\t') {
		return "", false
	}
	h := normalize(strings.TrimRight(strings.TrimSpace(t[n:]), "#"))
	if h == "" {
		return "", false
	}
	return h, true
}

// normalize collapses whitespace runs to single spaces and trims.
func normalize(s string) string {
	return strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// builder packs paragraphs into chunks.
type builder struct {
	size, overlap int
	out           []Chunk

	heading string
	parts   []string // paragraphs/pieces of the chunk being built
	n       int      // rune length of strings.Join(parts, "\n")
	carry   string   // overlap prefix for the chunk being built
}

func (b *builder) add(p paragraph) {
	if p.heading != b.heading {
		b.flush()
		b.carry = "" // no overlap across sections
		b.heading = p.heading
	}
	if runeLen(p.text) <= b.size {
		b.addPiece(p.text, "\n")
		return
	}
	// Long paragraph: start it on a fresh chunk and pack its sentences.
	b.flush()
	for _, s := range sentences(p.text) {
		for _, piece := range splitLong(s, b.size) {
			b.addPiece(piece, " ")
		}
	}
	b.flush()
}

// addPiece appends piece (≤ size runes) joined by sep, flushing first when it
// would not fit.
func (b *builder) addPiece(piece, sep string) {
	pl := runeLen(piece)
	if len(b.parts) > 0 && b.n+1+pl > b.size {
		b.flush()
	}
	if len(b.parts) > 0 {
		b.parts = append(b.parts, sep, piece)
		b.n += 1 + pl
	} else {
		b.parts = append(b.parts, piece)
		b.n = pl
	}
}

// flush emits the chunk being built and prepares the overlap for the next.
func (b *builder) flush() {
	if len(b.parts) == 0 {
		return
	}
	body := strings.Join(b.parts, "")
	content := body
	if b.carry != "" {
		content = b.carry + " " + body
	}
	b.out = append(b.out, Chunk{Heading: b.heading, Content: content, Seq: len(b.out)})
	b.carry = tail(content, b.overlap-1)
	b.parts, b.n = b.parts[:0], 0
}

// tail returns at most n runes from the end of s, starting at a word
// boundary when possible (so the prefix is a clean suffix of s).
func tail(s string, n int) string {
	if n <= 0 || s == "" {
		return ""
	}
	total := runeLen(s)
	if total <= n {
		return strings.TrimSpace(s)
	}
	// Byte offset of the first rune to keep.
	skip := total - n
	start := 0
	for i := range s {
		if skip == 0 {
			start = i
			break
		}
		skip--
	}
	t := s[start:]
	// Drop a partial leading word unless that would leave nothing.
	if prev, _ := utf8.DecodeLastRuneInString(s[:start]); !unicode.IsSpace(prev) {
		if i := strings.IndexAny(t, " \n"); i >= 0 && i < len(t)-1 {
			t = t[i+1:]
		}
	}
	return strings.TrimSpace(t)
}

// sentences splits a normalised paragraph after sentence enders
// (. ! ? … and their runs, followed by closing quotes/brackets) that are
// followed by a space.
func sentences(text string) []string {
	var out []string
	rs := []rune(text)
	start := 0
	for i := 0; i < len(rs); i++ {
		if !isEnder(rs[i]) {
			continue
		}
		j := i + 1
		for j < len(rs) && (isEnder(rs[j]) || isCloser(rs[j])) {
			j++
		}
		if j < len(rs) && rs[j] == ' ' {
			if s := strings.TrimSpace(string(rs[start:j])); s != "" {
				out = append(out, s)
			}
			start = j + 1
		}
		i = j - 1
	}
	if s := strings.TrimSpace(string(rs[start:])); s != "" {
		out = append(out, s)
	}
	return out
}

func isEnder(r rune) bool {
	switch r {
	case '.', '!', '?', '…', '。', '！', '？':
		return true
	}
	return false
}

func isCloser(r rune) bool {
	switch r {
	case '"', '\'', ')', ']', '»', '”', '’':
		return true
	}
	return false
}

// splitLong cuts s into pieces of at most size runes, at spaces when
// possible, otherwise mid-word.
func splitLong(s string, size int) []string {
	if runeLen(s) <= size {
		return []string{s}
	}
	var out []string
	var cur strings.Builder
	curN := 0
	emit := func() {
		if curN > 0 {
			out = append(out, cur.String())
			cur.Reset()
			curN = 0
		}
	}
	for _, w := range strings.Split(s, " ") {
		wr := []rune(w)
		for len(wr) > size { // a single word longer than a chunk
			emit()
			out = append(out, string(wr[:size]))
			wr = wr[size:]
		}
		wn := len(wr)
		if wn == 0 {
			continue
		}
		if curN > 0 && curN+1+wn > size {
			emit()
		}
		if curN > 0 {
			cur.WriteByte(' ')
			curN++
		}
		cur.WriteString(string(wr))
		curN += wn
	}
	emit()
	return out
}
