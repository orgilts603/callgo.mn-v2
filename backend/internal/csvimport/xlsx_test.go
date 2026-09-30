package csvimport

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xuri/excelize/v2"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// buildXLSX writes rows to the first sheet; values keep their Go types so
// numbers and dates are stored as Excel numbers.
func buildXLSX(t *testing.T, sheet string, rows [][]any, mutate ...func(f *excelize.File, sheet string)) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	if sheet != "Sheet1" {
		require.NoError(t, f.SetSheetName("Sheet1", sheet))
	}
	for i, row := range rows {
		cell, err := excelize.CoordinatesToCellName(1, i+1)
		require.NoError(t, err)
		require.NoError(t, f.SetSheetRow(sheet, cell, &row))
	}
	for _, m := range mutate {
		m(f, sheet)
	}
	var buf bytes.Buffer
	_, err := f.WriteTo(&buf)
	require.NoError(t, err)
	return buf.Bytes()
}

func sampleWorkbook(t *testing.T) []byte {
	day := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	return buildXLSX(t, "Жагсаалт", [][]any{
		{"Утас", "Нэр", "Огноо", "Дүн"},
		{99112233, "Бат-Эрдэнэ", day, 12500.5},
		{float64(88112233), "Сарнай", day.AddDate(0, 0, 1), 3000},
		{"+976 9911-0000", "Дорж", nil, nil},
		{97695112233.0, "Олон улс", nil, nil},
	}, func(f *excelize.File, sheet string) {
		fmtDate := "yyyy-mm-dd"
		style, err := f.NewStyle(&excelize.Style{CustomNumFmt: &fmtDate})
		require.NoError(t, err)
		require.NoError(t, f.SetCellStyle(sheet, "C2", "C3", style))
		// A formatted but empty trailing row (Excel keeps such rows).
		require.NoError(t, f.SetCellStyle(sheet, "A7", "D7", style))
		require.NoError(t, f.SetCellValue(sheet, "A8", ""))
		// A second sheet must be ignored.
		_, err = f.NewSheet("Other")
		require.NoError(t, err)
		require.NoError(t, f.SetCellValue("Other", "A1", "ignored"))
	})
}

func TestParseTargetsFileXLSX(t *testing.T) {
	data := sampleWorkbook(t)
	res, err := ParseTargetsFile(bytes.NewReader(data), "list.xlsx", Options{})
	require.NoError(t, err)
	assert.Equal(t, []string{"Утас", "Нэр", "Огноо", "Дүн"}, res.Columns)
	assert.Equal(t, 4, res.Total)
	assert.Equal(t, 4, res.Imported)
	assert.Equal(t, 0, res.Skipped, res.Errors)
	require.Len(t, res.Targets, 4)

	assert.Equal(t, "+97699112233", res.Targets[0].Phone)
	assert.Equal(t, "Бат-Эрдэнэ", res.Targets[0].Name)
	assert.Equal(t, "2026-09-30", res.Targets[0].Vars["Огноо"])
	assert.Equal(t, "12500.5", res.Targets[0].Vars["Дүн"])
	assert.Equal(t, domain.TargetPending, res.Targets[0].Status)

	assert.Equal(t, "+97688112233", res.Targets[1].Phone)
	assert.Equal(t, "2026-10-01", res.Targets[1].Vars["Огноо"])
	assert.Equal(t, "3000", res.Targets[1].Vars["Дүн"])

	assert.Equal(t, "+97699110000", res.Targets[2].Phone)
	assert.Empty(t, res.Targets[2].Vars)
	assert.Equal(t, "+97695112233", res.Targets[3].Phone)
}

func TestParseTargetsFileXLSXRowErrors(t *testing.T) {
	data := buildXLSX(t, "Sheet1", [][]any{
		{},
		{"phone", "name"},
		{99112233, "A"},
		{"abc", "B"},
		{99112233, "dup"},
	})
	res, err := ParseTargetsFile(bytes.NewReader(data), "x.xlsx", Options{})
	require.NoError(t, err)
	assert.Equal(t, 3, res.Total)
	assert.Equal(t, 1, res.Imported)
	assert.Equal(t, 2, res.Skipped)
	require.Len(t, res.Errors, 2)
	assert.Equal(t, 4, res.Errors[0].Row, "row numbers are sheet rows")
	assert.Equal(t, RowError{Row: 5, Message: "duplicate"}, res.Errors[1])
}

func TestParseContactsFileXLSX(t *testing.T) {
	data := buildXLSX(t, "Sheet1", [][]any{
		{"Нэр", "Утасны дугаар", "Таг"},
		{"Бат", 99112233, "vip; шинэ"},
	})
	res, err := ParseContactsFile(bytes.NewReader(data), "contacts.XLSX", Options{})
	require.NoError(t, err)
	require.Len(t, res.Contacts, 1)
	assert.Equal(t, "+97699112233", res.Contacts[0].Phone)
	assert.Equal(t, "Бат", res.Contacts[0].Name)
	assert.Equal(t, []string{"vip", "шинэ"}, res.Contacts[0].Tags)
}

func TestPreviewFileXLSX(t *testing.T) {
	p, err := PreviewFile(bytes.NewReader(sampleWorkbook(t)), "list.xlsx", 2)
	require.NoError(t, err)
	assert.Equal(t, FormatXLSX, p.Format)
	assert.Equal(t, 4, p.Total)
	assert.Equal(t, []string{"Утас", "Нэр", "Огноо", "Дүн"}, p.Columns)
	assert.Equal(t, [][]string{
		{"99112233", "Бат-Эрдэнэ", "2026-09-30", "12500.5"},
		{"88112233", "Сарнай", "2026-10-01", "3000"},
	}, p.Rows)
	assert.Equal(t, map[string]string{"Утас": FieldPhone, "Нэр": FieldName, "Огноо": FieldExtra, "Дүн": FieldExtra}, p.Mapping)

	// Rows shorter than the header are padded.
	p, err = PreviewFile(bytes.NewReader(sampleWorkbook(t)), "list.xlsx", 10)
	require.NoError(t, err)
	require.Len(t, p.Rows, 4)
	assert.Equal(t, []string{"+976 9911-0000", "Дорж", "", ""}, p.Rows[2])
}

func TestPreviewFileCSV(t *testing.T) {
	p, err := PreviewFile(strings.NewReader("phone,name\n99112233,A\n\n88112233,B\n"), "a.csv", 1)
	require.NoError(t, err)
	assert.Equal(t, FormatCSV, p.Format)
	assert.Equal(t, 2, p.Total)
	assert.Len(t, p.Rows, 1)
}

func TestXLSXErrors(t *testing.T) {
	// Legacy .xls (OLE2 compound file) is refused with a helpful message.
	ole := append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, make([]byte, 512)...)
	_, err := ParseTargetsFile(bytes.NewReader(ole), "old.xls", Options{})
	require.ErrorIs(t, err, ErrUnsupportedFormat)
	require.ErrorIs(t, err, domain.ErrInvalid)
	assert.Contains(t, err.Error(), ".xlsx")

	// Named .xlsx but not a zip.
	_, err = ParseTargetsFile(strings.NewReader("garbage"), "list.xlsx", Options{})
	require.ErrorIs(t, err, domain.ErrInvalid)

	// A workbook whose first sheet is empty.
	empty := buildXLSX(t, "Sheet1", nil)
	_, err = PreviewFile(bytes.NewReader(empty), "e.xlsx", 5)
	require.ErrorIs(t, err, ErrEmptyFile)

	// No phone column.
	nophone := buildXLSX(t, "Sheet1", [][]any{{"name"}, {"Bat"}})
	_, err = ParseTargetsFile(bytes.NewReader(nophone), "n.xlsx", Options{})
	require.ErrorIs(t, err, ErrNoPhoneColumn)

	// Text file misnamed .xls is read as CSV.
	res, err := ParseTargetsFile(strings.NewReader("phone\n99112233\n"), "export.xls", Options{})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Imported)
}

func TestDetectFormat(t *testing.T) {
	zip := []byte("PK\x03\x04rest")
	ole := []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}
	tests := []struct {
		name string
		file string
		head []byte
		want Format
	}{
		{"csv", "a.csv", []byte("phone,name"), FormatCSV},
		{"xlsx by magic", "a.csv", zip, FormatXLSX},
		{"xlsx by magic no name", "", zip, FormatXLSX},
		{"xlsx by extension", "A.XLSX", nil, FormatXLSX},
		{"xlsm", "a.xlsm", nil, FormatXLSX},
		{"xls by magic", "a.xls", ole, FormatXLS},
		{"xls text", "a.xls", []byte("phone\t"), FormatCSV},
		{"no name", "", []byte("phone"), FormatCSV},
		{"txt", "a.txt", []byte("phone"), FormatCSV},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, DetectFormat(tc.file, tc.head))
		})
	}
}

func TestPlainNumber(t *testing.T) {
	assert.Equal(t, "99112233", plainNumber("9.9112233E+07"))
	assert.Equal(t, "99112233", plainNumber("9.9112233e7"))
	assert.Equal(t, "1.5E+00", plainNumber("1.5E+00"), "non-integral values are kept")
	assert.Equal(t, "12.5", plainNumber("12.5"))
	assert.Equal(t, "abc", plainNumber("abc"))
}
