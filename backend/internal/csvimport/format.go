package csvimport

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Format is the detected layout of an uploaded file.
type Format string

// Supported (and recognised but unsupported) formats.
const (
	FormatCSV  Format = "csv"
	FormatXLSX Format = "xlsx"
	// FormatXLS is the legacy Excel 97-2003 binary format. It is recognised
	// only to give a helpful error: callers get ErrUnsupportedFormat.
	FormatXLS Format = "xls"
)

// ErrUnsupportedFormat is returned for legacy .xls workbooks.
var ErrUnsupportedFormat = fmt.Errorf("%w: Excel 97-2003 (.xls) files are not supported, save the file as Excel Workbook (.xlsx) or CSV UTF-8", domain.ErrInvalid)

var (
	zipMagic = []byte("PK\x03\x04")
	oleMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}
)

// DetectFormat decides how to read an upload. The file's leading bytes win
// over its name: a ZIP container is an .xlsx workbook and an OLE2 compound
// file is a legacy .xls workbook, whatever the extension says. Otherwise the
// extension decides (.xlsx/.xlsm → xlsx) and anything else is read as CSV —
// including text files misleadingly named .xls, which some systems export.
func DetectFormat(filename string, head []byte) Format {
	switch {
	case bytes.HasPrefix(head, zipMagic):
		return FormatXLSX
	case bytes.HasPrefix(head, oleMagic):
		return FormatXLS
	}
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".xlsx", ".xlsm":
		return FormatXLSX
	}
	return FormatCSV
}

// openFile detects the format of r and opens the matching row source.
func openFile(r io.Reader, filename string) (*source, Format, error) {
	br := bufio.NewReaderSize(r, peekSize)
	head, err := br.Peek(len(oleMagic))
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, "", fmt.Errorf("csvimport: read: %w", err)
	}
	switch format := DetectFormat(filename, head); format {
	case FormatXLSX:
		src, err := openXLSX(br)
		return src, format, err
	case FormatXLS:
		return nil, format, ErrUnsupportedFormat
	default:
		src, err := open(br)
		return src, FormatCSV, err
	}
}
