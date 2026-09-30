package csvimport

import (
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"

	"github.com/xuri/excelize/v2"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// maxUnzipSize caps the decompressed size of an uploaded workbook (zip-bomb
// guard); real campaign lists are far smaller.
const maxUnzipSize = 512 << 20

// openXLSX opens the first worksheet of an .xlsx workbook. The first
// non-blank row is the header. Cells are read as Excel displays them, so
// dates keep their number format and a phone stored as the number 99112233
// reads "99112233".
func openXLSX(r io.Reader) (*source, error) {
	f, err := excelize.OpenReader(r, excelize.Options{UnzipSizeLimit: maxUnzipSize})
	if err != nil {
		return nil, fmt.Errorf("%w: not a valid .xlsx workbook: %v", domain.ErrInvalid, err)
	}
	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		_ = f.Close()
		return nil, ErrEmptyFile
	}
	rows, err := f.Rows(sheets[0])
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%w: read sheet %q: %v", domain.ErrInvalid, sheets[0], err)
	}
	src := &source{
		rows: &xlsxRows{rows: rows},
		close: func() error {
			return errors.Join(rows.Close(), f.Close())
		},
	}
	return withHeader(src)
}

type xlsxRows struct {
	rows *excelize.Rows
	line int
}

func (x *xlsxRows) read() (record, error) {
	if !x.rows.Next() {
		if err := x.rows.Error(); err != nil {
			return record{}, fmt.Errorf("%w: read workbook: %v", domain.ErrInvalid, err)
		}
		return record{}, io.EOF
	}
	x.line++
	cells, err := x.rows.Columns()
	if err != nil {
		return record{line: x.line, err: err}, nil
	}
	for i, c := range cells {
		cells[i] = plainNumber(c)
	}
	return record{line: x.line, fields: cells}, nil
}

// sciNumber matches integers rendered in scientific notation, e.g.
// "9.9112233E+07", which some writers store for long numeric cells.
var sciNumber = regexp.MustCompile(`^[0-9](\.[0-9]+)?[eE]\+?[0-9]{1,2}$`)

// plainNumber rewrites an integral number in scientific notation as plain
// digits so phone numbers typed into numeric cells survive.
func plainNumber(s string) string {
	if !sciNumber.MatchString(s) {
		return s
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v != math.Trunc(v) || v >= 1e16 {
		return s
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}
