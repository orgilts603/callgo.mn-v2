package extract

import (
	"encoding/csv"
	"errors"
	"io"
	"strings"
)

// csvText renders each data row as "col: val; col: val" (the first row is
// the header), one row per paragraph so chunking never splits a row.
func csvText(text string) (string, error) {
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = sniffDelimiter(text)
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.TrimLeadingSpace = true

	var header []string
	var b strings.Builder
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", invalid(err, "The CSV file could not be read: %v", err)
		}
		if header == nil {
			header = make([]string, len(rec))
			for i, h := range rec {
				header[i] = strings.TrimSpace(h)
			}
			continue
		}
		var parts []string
		for i, v := range rec {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			col := ""
			if i < len(header) {
				col = header[i]
			}
			if col == "" {
				parts = append(parts, v)
			} else {
				parts = append(parts, col+": "+v)
			}
		}
		if len(parts) == 0 {
			continue
		}
		b.WriteString(strings.Join(parts, "; "))
		b.WriteString("\n\n")
	}
	if b.Len() == 0 && header != nil {
		// A single-row file is data, not a header.
		return strings.Join(header, "; "), nil
	}
	return b.String(), nil
}

// sniffDelimiter picks ',', ';' or tab by frequency in the first line.
func sniffDelimiter(text string) rune {
	line, _, _ := strings.Cut(text, "\n")
	best, bestN := ',', strings.Count(line, ",")
	for _, d := range []rune{';', '\t'} {
		if n := strings.Count(line, string(d)); n > bestN {
			best, bestN = d, n
		}
	}
	return best
}
