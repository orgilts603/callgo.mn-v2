// Package csvimport parses user-supplied CSV files (campaign target lists and
// contact lists) into domain objects. It is tolerant of what Excel and Google
// Sheets export: UTF-8 BOM, CRLF, ';' '\t' '|' delimiters, ragged rows and
// Mongolian column headers. Input is streamed row by row.
package csvimport

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/phone"
)

const (
	// DefaultMaxRows is the data-row limit when Options.MaxRows is unset.
	DefaultMaxRows = 50000
	// MaxKeptErrors is how many RowErrors a result keeps (all are counted).
	MaxKeptErrors = 200

	peekSize   = 64 * 1024
	sampleSize = 25
)

// Errors returned by the parsers. All wrap domain.ErrInvalid so HTTP layers
// can map them to 400.
var (
	ErrEmptyFile     = fmt.Errorf("%w: file is empty", domain.ErrInvalid)
	ErrNoPhoneColumn = fmt.Errorf("%w: no phone column found", domain.ErrInvalid)
	ErrTooManyRows   = fmt.Errorf("%w: too many rows", domain.ErrInvalid)
	ErrEncoding      = fmt.Errorf("%w: unsupported encoding, save the file as CSV UTF-8", domain.ErrInvalid)
)

// Options tune parsing.
type Options struct {
	// MaxRows caps the number of data rows (default DefaultMaxRows).
	MaxRows int
	// DefaultCountry is used for numbers written without an international
	// prefix: ISO alpha-2 ("MN", "US") or a calling code. Default "MN".
	DefaultCountry string
}

func (o Options) maxRows() int {
	if o.MaxRows <= 0 {
		return DefaultMaxRows
	}
	return o.MaxRows
}

// RowError describes why a row was skipped. Row is the 1-based line number in
// the file (the header is row 1).
type RowError struct {
	Row     int    `json:"row"`
	Message string `json:"message"`
}

// Result is the outcome of ParseTargets. Skipped counts every rejected row;
// Errors keeps at most MaxKeptErrors of them.
type Result struct {
	Targets  []domain.CampaignTarget `json:"-"`
	Total    int                     `json:"total"`
	Imported int                     `json:"imported"`
	Skipped  int                     `json:"skipped"`
	Errors   []RowError              `json:"errors"`
	Columns  []string                `json:"columns"`
}

// ContactsResult is the outcome of ParseContacts.
type ContactsResult struct {
	Contacts []domain.Contact `json:"-"`
	Total    int              `json:"total"`
	Imported int              `json:"imported"`
	Skipped  int              `json:"skipped"`
	Errors   []RowError       `json:"errors"`
	Columns  []string         `json:"columns"`
}

// Field roles reported in Preview.Mapping.
const (
	FieldPhone = "phone"
	FieldName  = "name"
	FieldTags  = "tags"
	FieldExtra = "var" // stored in target Vars / contact Meta
)

// PreviewResult is a look at the top of a file for a mapping UI.
type PreviewResult struct {
	Columns []string `json:"columns"`
	// Rows holds up to n data rows, each padded to the header width.
	Rows [][]string `json:"rows"`
	// Mapping maps each (non-empty) header to the field it will fill:
	// "phone", "name", "tags" or "var".
	Mapping map[string]string `json:"mapping"`
	// Total is the number of non-blank data rows in the whole file.
	Total int `json:"total"`
	// Format is the detected file format ("csv" or "xlsx").
	Format Format `json:"format"`
}

// ParseTargets parses a CSV campaign target list. The tags column, if any, is
// kept as an ordinary variable because targets have no tags.
func ParseTargets(r io.Reader, opts Options) (Result, error) {
	src, err := open(r)
	if err != nil {
		return Result{}, err
	}
	return parseTargets(src, opts)
}

// ParseTargetsFile is ParseTargets for a CSV or Excel (.xlsx) upload; the
// format is detected from filename and the file's first bytes.
func ParseTargetsFile(r io.Reader, filename string, opts Options) (Result, error) {
	src, _, err := openFile(r, filename)
	if err != nil {
		return Result{}, err
	}
	return parseTargets(src, opts)
}

func parseTargets(src *source, opts Options) (Result, error) {
	defer src.Close()
	res := Result{Targets: []domain.CampaignTarget{}}
	sum, err := scan(src, opts, false, func(row parsedRow) {
		res.Targets = append(res.Targets, domain.CampaignTarget{
			Phone:  row.phone,
			Name:   row.name,
			Vars:   row.extras,
			Status: domain.TargetPending,
		})
	})
	if err != nil {
		return Result{}, err
	}
	res.Total, res.Imported, res.Skipped, res.Errors, res.Columns = sum.total, sum.imported, sum.skipped, sum.errors, sum.columns
	return res, nil
}

// ParseContacts parses a CSV contact list.
func ParseContacts(r io.Reader, opts Options) (ContactsResult, error) {
	src, err := open(r)
	if err != nil {
		return ContactsResult{}, err
	}
	return parseContacts(src, opts)
}

// ParseContactsFile is ParseContacts for a CSV or Excel (.xlsx) upload.
func ParseContactsFile(r io.Reader, filename string, opts Options) (ContactsResult, error) {
	src, _, err := openFile(r, filename)
	if err != nil {
		return ContactsResult{}, err
	}
	return parseContacts(src, opts)
}

func parseContacts(src *source, opts Options) (ContactsResult, error) {
	defer src.Close()
	res := ContactsResult{Contacts: []domain.Contact{}}
	sum, err := scan(src, opts, true, func(row parsedRow) {
		res.Contacts = append(res.Contacts, domain.Contact{
			Phone: row.phone,
			Name:  row.name,
			Tags:  row.tags,
			Meta:  row.extras,
		})
	})
	if err != nil {
		return ContactsResult{}, err
	}
	res.Total, res.Imported, res.Skipped, res.Errors, res.Columns = sum.total, sum.imported, sum.skipped, sum.errors, sum.columns
	return res, nil
}

// TemplateCSV returns a sample file (UTF-8 with BOM and CRLF so Excel opens
// it correctly) that both parsers accept.
func TemplateCSV() []byte {
	return []byte("\xEF\xBB\xBFphone,name,note\r\n" +
		"99112233,Бат-Эрдэнэ,Анхны дуудлага\r\n" +
		"+97688001122,Сарнай,VIP харилцагч\r\n")
}

// Preview reads a CSV file and reports its header, up to n data rows
// (default 10), the total number of data rows and how columns would be
// mapped. It does not validate phone numbers.
func Preview(r io.Reader, n int) (PreviewResult, error) {
	src, err := open(r)
	if err != nil {
		return PreviewResult{}, err
	}
	return preview(src, FormatCSV, n)
}

// PreviewFile is Preview for a CSV or Excel (.xlsx) upload.
func PreviewFile(r io.Reader, filename string, n int) (PreviewResult, error) {
	src, format, err := openFile(r, filename)
	if err != nil {
		return PreviewResult{}, err
	}
	return preview(src, format, n)
}

func preview(src *source, format Format, n int) (PreviewResult, error) {
	defer src.Close()
	if n <= 0 {
		n = 10
	}
	p := PreviewResult{Columns: src.header, Rows: [][]string{}, Mapping: map[string]string{}, Format: format}
	var sample [][]string
	for {
		rec, err := src.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return PreviewResult{}, err
		}
		if rec.err == nil && blank(rec.fields) {
			continue
		}
		p.Total++
		if rec.err != nil {
			continue
		}
		if len(p.Rows) < n {
			row := rec.fields
			if len(row) < len(src.header) {
				row = append(row, make([]string, len(src.header)-len(row))...)
			}
			p.Rows = append(p.Rows, row)
		}
		if len(sample) < sampleSize {
			sample = append(sample, rec.fields)
		}
	}
	cols := resolve(src.header, sample, "", true)
	for i, h := range src.header {
		if h == "" {
			continue
		}
		switch i {
		case cols.phone:
			p.Mapping[h] = FieldPhone
		case cols.name:
			p.Mapping[h] = FieldName
		case cols.tags:
			p.Mapping[h] = FieldTags
		default:
			p.Mapping[h] = FieldExtra
		}
	}
	return p, nil
}

// SniffDelimiter guesses the field delimiter from the header line by counting
// ',' ';' '\t' and '|' outside double quotes. Ties prefer that order; a line
// without any candidate yields ','.
func SniffDelimiter(headerLine string) rune {
	candidates := []rune{',', ';', '\t', '|'}
	counts := make(map[rune]int, len(candidates))
	inQuotes := false
	for _, r := range headerLine {
		switch {
		case r == '"':
			inQuotes = !inQuotes
		case r == '\n' && !inQuotes:
			goto done
		case !inQuotes:
			counts[r]++
		}
	}
done:
	best, bestN := ',', 0
	for _, c := range candidates {
		if counts[c] > bestN {
			best, bestN = c, counts[c]
		}
	}
	return best
}

// ---------------------------------------------------------------------------
// internals
// ---------------------------------------------------------------------------

type record struct {
	line   int
	fields []string
	err    error // per-row syntax error
}

// rowReader yields raw rows (header included) and io.EOF at the end.
type rowReader interface {
	read() (record, error)
}

// source is a header plus a pull-based row stream with a small pushback queue
// so a sample can be inspected before rows are processed.
type source struct {
	rows   rowReader
	header []string
	queue  []record
	close  func() error
}

func (s *source) Close() error {
	if s.close == nil {
		return nil
	}
	return s.close()
}

// open reads a CSV stream.
func open(r io.Reader) (*source, error) {
	br := bufio.NewReaderSize(r, peekSize)
	if bom, _ := br.Peek(3); bytes.Equal(bom, []byte{0xEF, 0xBB, 0xBF}) {
		_, _ = br.Discard(3)
	} else if len(bom) >= 2 && (bom[0] == 0xFF && bom[1] == 0xFE || bom[0] == 0xFE && bom[1] == 0xFF) {
		return nil, ErrEncoding
	}
	peek, err := br.Peek(peekSize)
	if len(peek) == 0 {
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("csvimport: read: %w", err)
		}
		return nil, ErrEmptyFile
	}
	firstLine := peek
	if i := bytes.IndexByte(peek, '\n'); i >= 0 {
		firstLine = peek[:i]
	}
	cr := csv.NewReader(br)
	cr.Comma = SniffDelimiter(string(firstLine))
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.TrimLeadingSpace = true
	return withHeader(&source{rows: csvRows{cr: cr}})
}

// withHeader consumes rows up to and including the first non-blank one,
// which becomes the header.
func withHeader(s *source) (*source, error) {
	for {
		rec, err := s.rows.read()
		if errors.Is(err, io.EOF) {
			_ = s.Close()
			return nil, ErrEmptyFile
		}
		if err != nil {
			_ = s.Close()
			return nil, err
		}
		if rec.err != nil {
			_ = s.Close()
			return nil, fmt.Errorf("%w: header: %v", domain.ErrInvalid, rec.err)
		}
		if blank(rec.fields) {
			continue
		}
		s.header = make([]string, len(rec.fields))
		for i, f := range rec.fields {
			s.header[i] = cleanCell(f)
		}
		return s, nil
	}
}

type csvRows struct{ cr *csv.Reader }

func (c csvRows) read() (record, error) {
	fields, err := c.cr.Read()
	if err != nil {
		var pe *csv.ParseError
		if errors.As(err, &pe) {
			return record{line: pe.StartLine, err: pe.Err}, nil
		}
		if errors.Is(err, io.EOF) {
			return record{}, io.EOF
		}
		return record{}, fmt.Errorf("csvimport: read: %w", err)
	}
	line, _ := c.cr.FieldPos(0)
	return record{line: line, fields: fields}, nil
}

func (s *source) read() (record, error) { return s.rows.read() }

func (s *source) next() (record, error) {
	if len(s.queue) > 0 {
		rec := s.queue[0]
		s.queue = s.queue[1:]
		return rec, nil
	}
	return s.read()
}

// peekRows buffers up to n non-blank rows without consuming them.
func (s *source) peekRows(n int) ([][]string, error) {
	var sample [][]string
	for len(sample) < n {
		rec, err := s.read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		s.queue = append(s.queue, rec)
		if rec.err == nil && !blank(rec.fields) {
			sample = append(sample, rec.fields)
		}
	}
	return sample, nil
}

func blank(fields []string) bool {
	for _, f := range fields {
		if strings.TrimSpace(f) != "" {
			return false
		}
	}
	return true
}

// cleanCell trims whitespace, stray BOMs, and the apostrophe Excel prepends
// to text cells.
func cleanCell(s string) string {
	s = strings.TrimSpace(strings.Trim(s, "\uFEFF\u200B"))
	if len(s) > 1 && s[0] == '\'' {
		s = strings.TrimSpace(s[1:])
	}
	return s
}

// columns is the resolved header mapping; -1 means absent.
type columns struct {
	phone, name, tags int
	extras            []extraCol
}

type extraCol struct {
	idx int
	key string
}

// Header synonyms in priority order, compared after normHeader.
var (
	phoneHeaders = []string{"phone", "phonenumber", "mobile", "tel", "number", "утас", "утасныдугаар", "дугаар", "гарутас", "telephone", "msisdn"}
	nameHeaders  = []string{"name", "fullname", "contact", "нэр", "овогнэр", "харилцагч"}
	tagsHeaders  = []string{"tags", "таг", "tag"}
)

// normHeader lowercases and removes spaces, '_', '-' and '.' so "Phone
// Number", "phone_number" and "PHONENUMBER" compare equal.
func normHeader(h string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(cleanCell(h)) {
		if unicode.IsSpace(r) || r == '_' || r == '-' || r == '.' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func findHeader(norm []string, synonyms []string, taken ...int) int {
	for _, syn := range synonyms {
		for i, n := range norm {
			if n != syn {
				continue
			}
			used := false
			for _, t := range taken {
				used = used || t == i
			}
			if !used {
				return i
			}
		}
	}
	return -1
}

// resolve maps header cells to roles. Without a phone header it picks the
// first free column whose sampled values mostly look like phone numbers.
func resolve(header []string, sample [][]string, country string, tagsRole bool) columns {
	norm := make([]string, len(header))
	for i, h := range header {
		norm[i] = normHeader(h)
	}
	c := columns{phone: findHeader(norm, phoneHeaders), name: -1, tags: -1}
	c.name = findHeader(norm, nameHeaders, c.phone)
	if tagsRole {
		c.tags = findHeader(norm, tagsHeaders, c.phone, c.name)
	}
	if c.phone < 0 {
		c.phone = sniffPhoneColumn(len(header), sample, country, c.name, c.tags)
	}
	for i, h := range header {
		if i == c.phone || i == c.name || i == c.tags || h == "" {
			continue
		}
		c.extras = append(c.extras, extraCol{idx: i, key: h})
	}
	return c
}

func sniffPhoneColumn(ncols int, sample [][]string, country string, skip ...int) int {
	for col := 0; col < ncols; col++ {
		skipped := false
		for _, s := range skip {
			skipped = skipped || s == col
		}
		if skipped {
			continue
		}
		seen, ok := 0, 0
		for _, row := range sample {
			v := cell(row, col)
			if v == "" {
				continue
			}
			seen++
			if _, err := phone.NormalizeFor(v, country); err == nil {
				ok++
			}
		}
		if seen > 0 && ok*2 >= seen {
			return col
		}
	}
	return -1
}

func cell(row []string, i int) string {
	if i < 0 || i >= len(row) {
		return ""
	}
	return cleanCell(row[i])
}

type parsedRow struct {
	phone  string
	name   string
	tags   []string
	extras map[string]string
}

type summary struct {
	total, imported, skipped int
	errors                   []RowError
	columns                  []string
}

func (s *summary) fail(line int, msg string) {
	s.skipped++
	if len(s.errors) < MaxKeptErrors {
		s.errors = append(s.errors, RowError{Row: line, Message: msg})
	}
}

// scan streams the file and calls accept for every valid, unique row.
func scan(src *source, opts Options, tagsRole bool, accept func(parsedRow)) (summary, error) {
	sample, err := src.peekRows(sampleSize)
	if err != nil {
		return summary{}, err
	}
	cols := resolve(src.header, sample, opts.DefaultCountry, tagsRole)
	if cols.phone < 0 {
		return summary{}, ErrNoPhoneColumn
	}
	sum := summary{columns: src.header, errors: []RowError{}}
	maxRows := opts.maxRows()
	seen := make(map[string]struct{})

	for {
		rec, err := src.next()
		if errors.Is(err, io.EOF) {
			return sum, nil
		}
		if err != nil {
			return summary{}, err
		}
		if rec.err == nil && blank(rec.fields) {
			continue
		}
		if sum.total >= maxRows {
			return summary{}, fmt.Errorf("%w: limit is %d rows", ErrTooManyRows, maxRows)
		}
		sum.total++
		if rec.err != nil {
			sum.fail(rec.line, "malformed row: "+rec.err.Error())
			continue
		}
		raw := cell(rec.fields, cols.phone)
		if raw == "" {
			sum.fail(rec.line, "missing phone")
			continue
		}
		e164, err := phone.NormalizeFor(raw, opts.DefaultCountry)
		if err != nil {
			sum.fail(rec.line, err.Error())
			continue
		}
		if _, dup := seen[e164]; dup {
			sum.fail(rec.line, "duplicate")
			continue
		}
		seen[e164] = struct{}{}

		row := parsedRow{
			phone:  e164,
			name:   strings.Join(strings.Fields(cell(rec.fields, cols.name)), " "),
			tags:   splitTags(cell(rec.fields, cols.tags)),
			extras: make(map[string]string, len(cols.extras)),
		}
		for _, ex := range cols.extras {
			if v := cell(rec.fields, ex.idx); v != "" {
				if _, exists := row.extras[ex.key]; !exists {
					row.extras[ex.key] = v
				}
			}
		}
		sum.imported++
		accept(row)
	}
}

// splitTags splits on ';' '|' ',' trimming and de-duplicating; never nil.
func splitTags(s string) []string {
	tags := []string{}
	if s == "" {
		return tags
	}
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == '|' || r == ',' })
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		dup := false
		for _, t := range tags {
			dup = dup || t == p
		}
		if !dup {
			tags = append(tags, p)
		}
	}
	return tags
}
