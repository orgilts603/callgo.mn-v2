package extract

import (
	"bytes"
	"fmt"
	"strings"
	"unicode"

	"github.com/ledongthuc/pdf"
)

// minPDFText is the least amount of non-space text a PDF must yield to be
// considered text-based rather than scanned images.
const minPDFText = 50

// pdfText extracts the text layer page by page.
func pdfText(data []byte) (text string, err error) {
	defer func() {
		// The PDF parser panics on some malformed files.
		if r := recover(); r != nil {
			text, err = "", invalid(fmt.Errorf("pdf: %v", r), "The PDF file could not be read (damaged or unsupported).")
		}
	}()
	rd, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "encrypt") {
			return "", invalid(err, "The PDF is password-protected; upload an unprotected copy.")
		}
		return "", invalid(err, "The PDF file could not be read (damaged or unsupported).")
	}
	var b strings.Builder
	for i := 1; i <= rd.NumPage(); i++ {
		page := rd.Page(i)
		if page.V.IsNull() {
			continue
		}
		// nil fonts: resolve each page's own font resources (names like /F1
		// may denote different fonts on different pages).
		pt, err := page.GetPlainText(nil)
		if err != nil {
			return "", invalid(err, "Page %d of the PDF could not be read.", i)
		}
		b.WriteString(pt)
		b.WriteString("\n\n")
	}
	text = b.String()
	n := 0
	for _, r := range text {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	if n < minPDFText {
		return "", invalid(nil, "PDF has no text layer (scanned?)")
	}
	return text, nil
}
