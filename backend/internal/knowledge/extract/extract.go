// Package extract turns uploaded knowledge documents (txt, md, csv, docx,
// pdf) into plain text for chunking. Headings are kept as markdown "# "
// lines so the chunker can attach them to passages.
package extract

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"mime"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// MaxBytes is the largest accepted upload (20 MB).
const MaxBytes = 20 << 20

// Format is a supported document format.
type Format string

// Supported formats.
const (
	FormatText     Format = "txt"
	FormatMarkdown Format = "md"
	FormatCSV      Format = "csv"
	FormatDOCX     Format = "docx"
	FormatPDF      Format = "pdf"
)

// MIME types by format (used when the upload carries none).
var mimeTypes = map[Format]string{
	FormatText:     "text/plain",
	FormatMarkdown: "text/markdown",
	FormatCSV:      "text/csv",
	FormatDOCX:     "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	FormatPDF:      "application/pdf",
}

// Error is an extraction failure with a message fit for end users. It
// unwraps to domain.ErrInvalid.
type Error struct {
	Msg string
	Err error
}

func (e *Error) Error() string { return e.Msg }

// Unwrap returns domain.ErrInvalid (and the underlying cause, if any).
func (e *Error) Unwrap() []error {
	if e.Err != nil {
		return []error{domain.ErrInvalid, e.Err}
	}
	return []error{domain.ErrInvalid}
}

func invalid(cause error, format string, args ...any) error {
	return &Error{Msg: fmt.Sprintf(format, args...), Err: cause}
}

// Detect returns the document format from the file extension, falling back
// to the MIME type; ok is false for unsupported documents.
func Detect(filename, mimeType string) (Format, bool) {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), ".")) {
	case "txt", "text":
		return FormatText, true
	case "md", "markdown":
		return FormatMarkdown, true
	case "csv":
		return FormatCSV, true
	case "docx":
		return FormatDOCX, true
	case "pdf":
		return FormatPDF, true
	case "":
	default:
		return "", false
	}
	mt, _, _ := mime.ParseMediaType(mimeType)
	for f, m := range mimeTypes {
		if strings.EqualFold(mt, m) {
			return f, true
		}
	}
	return "", false
}

// MIMEType returns the canonical MIME type of a format.
func MIMEType(f Format) string { return mimeTypes[f] }

// CheckSupported returns an *Error (wrapping domain.ErrInvalid) when the
// document type is not supported.
func CheckSupported(filename, mimeType string) error {
	if _, ok := Detect(filename, mimeType); ok {
		return nil
	}
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == ".doc" {
		return invalid(nil, "Legacy .doc files are not supported; save the file as .docx and upload it again.")
	}
	if ext == "" {
		ext = mimeType
	}
	return invalid(nil, "Unsupported file type %q; upload a PDF, DOCX, TXT, MD or CSV file.", ext)
}

// Text extracts plain text from r. The format is chosen by filename
// extension (falling back to mime). Inputs over MaxBytes, unsupported types,
// unreadable files and PDFs without a text layer return an *Error that wraps
// domain.ErrInvalid.
func Text(filename, mimeType string, r io.Reader) (string, error) {
	if err := CheckSupported(filename, mimeType); err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("extract: read %s: %w", filename, err)
	}
	return Bytes(filename, mimeType, data)
}

// Bytes is Text for an in-memory document.
func Bytes(filename, mimeType string, data []byte) (string, error) {
	format, ok := Detect(filename, mimeType)
	if !ok {
		return "", CheckSupported(filename, mimeType)
	}
	if len(data) > MaxBytes {
		return "", invalid(nil, "The file is larger than %d MB.", MaxBytes>>20)
	}
	var (
		text string
		err  error
	)
	switch format {
	case FormatText, FormatMarkdown:
		text = decodeText(data)
	case FormatCSV:
		text, err = csvText(decodeText(data))
	case FormatDOCX:
		text, err = docxText(data)
	case FormatPDF:
		text, err = pdfText(data)
	}
	if err != nil {
		return "", err
	}
	text = Clean(text)
	if strings.TrimSpace(text) == "" {
		return "", invalid(nil, "The document contains no text.")
	}
	return text, nil
}

// Clean makes text safe to store: valid UTF-8, no NUL bytes, "\n" line ends.
func Clean(text string) string {
	text = strings.ToValidUTF8(text, "�")
	text = strings.ReplaceAll(text, "\x00", "")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.TrimSpace(text)
}

// decodeText decodes UTF-8 (with or without BOM) or BOM-marked UTF-16.
func decodeText(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		return string(data[3:])
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		return decodeUTF16(data[2:], binary.LittleEndian)
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		return decodeUTF16(data[2:], binary.BigEndian)
	}
	if !utf8.Valid(data) {
		return strings.ToValidUTF8(string(data), "�")
	}
	return string(data)
}

func decodeUTF16(data []byte, order binary.ByteOrder) string {
	u := make([]uint16, len(data)/2)
	for i := range u {
		u[i] = order.Uint16(data[2*i:])
	}
	return string(utf16.Decode(u))
}
