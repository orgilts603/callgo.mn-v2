package extract

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func requireInvalid(t *testing.T, err error, msg string) {
	t.Helper()
	require.Error(t, err)
	require.True(t, errors.Is(err, domain.ErrInvalid), "want ErrInvalid, got %v", err)
	var ee *Error
	require.ErrorAs(t, err, &ee)
	if msg != "" {
		require.Contains(t, err.Error(), msg)
	}
}

func TestPlainAndMarkdown(t *testing.T) {
	got, err := Text("notes.txt", "", strings.NewReader("\xEF\xBB\xBFСайн байна уу?\r\nМөр 2\x00"))
	require.NoError(t, err)
	require.Equal(t, "Сайн байна уу?\nМөр 2", got)

	md := "# Гарчиг\n\nТекст **тод**.\n"
	got, err = Text("README.MD", "application/octet-stream", strings.NewReader(md))
	require.NoError(t, err)
	require.Equal(t, strings.TrimSpace(md), got)

	// UTF-16LE with BOM.
	utf16 := []byte{0xFF, 0xFE, 'H', 0, 'i', 0}
	got, err = Text("w.txt", "", bytes.NewReader(utf16))
	require.NoError(t, err)
	require.Equal(t, "Hi", got)

	// No extension: MIME decides.
	got, err = Text("pasted", "text/plain; charset=utf-8", strings.NewReader("abc"))
	require.NoError(t, err)
	require.Equal(t, "abc", got)

	// Invalid UTF-8 is repaired, not rejected.
	got, err = Text("bad.txt", "", bytes.NewReader([]byte{'a', 0xff, 'b'}))
	require.NoError(t, err)
	require.Equal(t, "a�b", got)
}

func TestErrors(t *testing.T) {
	_, err := Text("slides.pptx", "", strings.NewReader("x"))
	requireInvalid(t, err, "Unsupported file type")
	_, err = Text("old.doc", "", strings.NewReader("x"))
	requireInvalid(t, err, ".docx")
	_, err = Text("x", "image/png", strings.NewReader("x"))
	requireInvalid(t, err, "image/png")
	_, err = Text("empty.txt", "", strings.NewReader("  \n "))
	requireInvalid(t, err, "no text")
	_, err = Text("big.txt", "", bytes.NewReader(make([]byte, MaxBytes+1)))
	requireInvalid(t, err, "20 MB")
	_, err = Text("broken.docx", "", strings.NewReader("not a zip"))
	requireInvalid(t, err, "DOCX")
	_, err = Text("broken.pdf", "", strings.NewReader("%PDF-1.4 garbage"))
	requireInvalid(t, err, "PDF")
	require.NoError(t, CheckSupported("a.PDF", ""))
	f, ok := Detect("", MIMEType(FormatDOCX))
	require.True(t, ok)
	require.Equal(t, FormatDOCX, f)
}

func TestCSV(t *testing.T) {
	in := "Бараа;Үнэ;Тайлбар\nЦай;5000;\n\"Кофе; хар\";7000;халуун\n;;\n"
	got, err := Text("prices.csv", "", strings.NewReader(in))
	require.NoError(t, err)
	require.Equal(t, "Бараа: Цай; Үнэ: 5000\n\nБараа: Кофе; хар; Үнэ: 7000; Тайлбар: халуун", got)

	got, err = Text("x.csv", "", strings.NewReader("a,b,c\n1,2,3,4\n"))
	require.NoError(t, err)
	require.Equal(t, "a: 1; b: 2; c: 3; 4", got, "extra columns kept without a name")

	got, err = Text("one.csv", "", strings.NewReader("only,a,header"))
	require.NoError(t, err)
	require.Equal(t, "only; a; header", got)
}

// buildDOCX zips a minimal Word document around body XML.
func buildDOCX(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("[Content_Types].xml")
	require.NoError(t, err)
	_, _ = w.Write([]byte(`<?xml version="1.0"?><Types/>`))
	w, err = zw.Create("word/document.xml")
	require.NoError(t, err)
	_, err = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>%s</w:body></w:document>`, body)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func wp(style, text string) string {
	ppr := ""
	if style != "" {
		ppr = `<w:pPr><w:pStyle w:val="` + style + `"/></w:pPr>`
	}
	return `<w:p>` + ppr + `<w:r><w:t xml:space="preserve">` + text + `</w:t></w:r></w:p>`
}

func TestDOCX(t *testing.T) {
	body := wp("Title", "Гарын авлага") +
		wp("Heading2", "Хүргэлт") +
		`<w:p><w:r><w:t xml:space="preserve">Хүргэлт </w:t></w:r><w:r><w:t>24 цагт</w:t></w:r><w:r><w:tab/><w:t>хийгдэнэ.</w:t></w:r></w:p>` +
		`<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/></w:numPr></w:pPr><w:r><w:t>Улаанбаатар</w:t></w:r></w:p>` +
		`<w:p><w:pPr><w:outlineLvl w:val="2"/></w:pPr><w:r><w:t>Үнэ</w:t></w:r></w:p>` +
		`<w:tbl><w:tr><w:tc>` + wp("", "Бараа") + `</w:tc><w:tc>` + wp("", "Үнэ") + `</w:tc></w:tr>` +
		`<w:tr><w:tc>` + wp("", "Цай") + wp("", "ногоон") + `</w:tc><w:tc>` + wp("", "5000₮") + `</w:tc></w:tr></w:tbl>` +
		wp("", "") + wp("", "Төгсгөл &amp; баяртай")
	got, err := Text("guide.docx", "", bytes.NewReader(buildDOCX(t, body)))
	require.NoError(t, err)
	require.Equal(t, strings.Join([]string{
		"# Гарын авлага",
		"## Хүргэлт",
		"Хүргэлт 24 цагт хийгдэнэ.",
		"- Улаанбаатар",
		"### Үнэ",
		"Бараа | Үнэ",
		"Цай ногоон | 5000₮",
		"Төгсгөл & баяртай",
	}, "\n\n"), got)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	_, err = zw.Create("word/other.xml")
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	_, err = Text("nobody.docx", "", &buf)
	requireInvalid(t, err, "no document body")
}

// buildPDF writes a one-page PDF with a correct xref table whose content
// stream is content.
func buildPDF(content string) []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}

func TestPDF(t *testing.T) {
	content := "BT /F1 12 Tf 72 720 Td (Delivery takes 24 hours inside Ulaanbaatar city.) Tj ET\n" +
		"BT /F1 12 Tf 72 700 Td (Returns are accepted within seven days.) Tj ET"
	got, err := Text("manual.pdf", "application/pdf", bytes.NewReader(buildPDF(content)))
	require.NoError(t, err)
	require.Contains(t, got, "Delivery takes 24 hours inside Ulaanbaatar city.")
	require.Contains(t, got, "Returns are accepted within seven days.")

	_, err = Text("scan.pdf", "", bytes.NewReader(buildPDF("BT /F1 12 Tf 72 720 Td (p. 1) Tj ET")))
	requireInvalid(t, err, "PDF has no text layer (scanned?)")
}
