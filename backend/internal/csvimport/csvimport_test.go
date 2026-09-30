package csvimport

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func TestSniffDelimiter(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want rune
	}{
		{"comma", "phone,name,note", ','},
		{"semicolon", "phone;name;note", ';'},
		{"tab", "phone\tname\tnote", '\t'},
		{"pipe", "phone|name|note", '|'},
		{"single column", "phone", ','},
		{"empty", "", ','},
		{"quoted commas ignored", `"a,b,c";name;note`, ';'},
		{"majority wins", "phone;name;a,b", ';'},
		{"tie prefers comma", "a,b;c", ','},
		{"crlf terminated", "phone;name\r\n1,2,3,4", ';'},
		{"mongolian", "утас;нэр", ';'},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, SniffDelimiter(tc.in))
		})
	}
}

func TestParseTargets(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		opts       Options
		wantPhones []string
		wantNames  []string
		wantVars   []map[string]string
		total      int
		skipped    int
		errs       []RowError
		columns    []string
	}{
		{
			name:       "bom crlf comma",
			in:         "\xEF\xBB\xBFphone,name,note\r\n99112233,Bat,hello\r\n+976 8811-2233,Sara,vip\r\n",
			wantPhones: []string{"+97699112233", "+97688112233"},
			wantNames:  []string{"Bat", "Sara"},
			wantVars:   []map[string]string{{"note": "hello"}, {"note": "vip"}},
			total:      2,
			columns:    []string{"phone", "name", "note"},
		},
		{
			name:       "semicolon delimiter",
			in:         "phone;name;debt\n99112233;Bat;12000\n88112233;Sara;500\n",
			wantPhones: []string{"+97699112233", "+97688112233"},
			wantNames:  []string{"Bat", "Sara"},
			wantVars:   []map[string]string{{"debt": "12000"}, {"debt": "500"}},
			total:      2,
		},
		{
			name:       "tab delimiter",
			in:         "phone\tname\n99112233\tBat\n",
			wantPhones: []string{"+97699112233"},
			wantNames:  []string{"Bat"},
			wantVars:   []map[string]string{{}},
			total:      1,
		},
		{
			name:       "pipe delimiter",
			in:         "phone|name\n99112233|Bat\n",
			wantPhones: []string{"+97699112233"},
			wantNames:  []string{"Bat"},
			wantVars:   []map[string]string{{}},
			total:      1,
		},
		{
			name:       "mongolian headers with spaces and caps",
			in:         "Утасны дугаар,Овог нэр,Хаяг\n99112233,Бат Болд,УБ хот\n",
			wantPhones: []string{"+97699112233"},
			wantNames:  []string{"Бат Болд"},
			wantVars:   []map[string]string{{"Хаяг": "УБ хот"}},
			total:      1,
			columns:    []string{"Утасны дугаар", "Овог нэр", "Хаяг"},
		},
		{
			name:       "mongolian gar utas",
			in:         "Гар утас,Харилцагч\n99112233,Дорж\n",
			wantPhones: []string{"+97699112233"},
			wantNames:  []string{"Дорж"},
			wantVars:   []map[string]string{{}},
			total:      1,
		},
		{
			name:       "synonym Phone_Number and Full Name",
			in:         "ID,Phone_Number,Full Name\n7,99112233,Bat\n",
			wantPhones: []string{"+97699112233"},
			wantNames:  []string{"Bat"},
			wantVars:   []map[string]string{{"ID": "7"}},
			total:      1,
		},
		{
			name:       "phone header beats number header regardless of order",
			in:         "Number,Phone\n1234,99112233\n",
			wantPhones: []string{"+97699112233"},
			wantNames:  []string{""},
			wantVars:   []map[string]string{{"Number": "1234"}},
			total:      1,
		},
		{
			name:       "duplicates first wins",
			in:         "phone,name\n99112233,First\n9911 2233,Second\n+97699112233,Third\n88112233,Other\n",
			wantPhones: []string{"+97699112233", "+97688112233"},
			wantNames:  []string{"First", "Other"},
			wantVars:   []map[string]string{{}, {}},
			total:      4,
			skipped:    2,
			errs:       []RowError{{3, "duplicate"}, {4, "duplicate"}},
		},
		{
			name:       "invalid and missing numbers",
			in:         "phone,name\n99112233,ok\nabc123,bad\n,empty\n123,short\n",
			wantPhones: []string{"+97699112233"},
			wantNames:  []string{"ok"},
			wantVars:   []map[string]string{{}},
			total:      4,
			skipped:    3,
		},
		{
			name:       "ragged rows",
			in:         "phone,name,note\n99112233\n88112233,Sara,n1,extra1,extra2\n\n77112233,Dorj\n,onlyname\n",
			wantPhones: []string{"+97699112233", "+97688112233", "+97677112233"},
			wantNames:  []string{"", "Sara", "Dorj"},
			wantVars:   []map[string]string{{}, {"note": "n1"}, {}},
			total:      4,
			skipped:    1,
			errs:       []RowError{{6, "missing phone"}},
		},
		{
			name:       "quoted fields with delimiter and newline keep row numbers",
			in:         "phone,name,note\n99112233,\"Bat, Jr\",\"line1\nline2\"\n88112233,Sara,ok\n",
			wantPhones: []string{"+97699112233", "+97688112233"},
			wantNames:  []string{"Bat, Jr", "Sara"},
			wantVars:   []map[string]string{{"note": "line1\nline2"}, {"note": "ok"}},
			total:      2,
		},
		{
			name:       "error row numbers count physical lines",
			in:         "phone,name\n\"99112233\",A\n\nnope,B\n",
			wantPhones: []string{"+97699112233"},
			wantNames:  []string{"A"},
			wantVars:   []map[string]string{{}},
			total:      2,
			skipped:    1,
			errs:       []RowError{{4, `invalid phone number: "nope"`}},
		},
		{
			name:       "excel float and apostrophe phones",
			in:         "phone\n99112233.0\n'88112233\n",
			wantPhones: []string{"+97699112233", "+97688112233"},
			wantNames:  []string{"", ""},
			wantVars:   []map[string]string{{}, {}},
			total:      2,
		},
		{
			name:       "empty var values dropped and blank header ignored",
			in:         "phone,,note,other\n99112233,zzz,,val\n",
			wantPhones: []string{"+97699112233"},
			wantNames:  []string{""},
			wantVars:   []map[string]string{{"other": "val"}},
			total:      1,
		},
		{
			name:       "tags column is a plain var for targets",
			in:         "phone,tags\n99112233,a;b\n",
			wantPhones: []string{"+97699112233"},
			wantNames:  []string{""},
			wantVars:   []map[string]string{{"tags": "a;b"}},
			total:      1,
		},
		{
			name:       "default country",
			in:         "phone\n010-1234-5678\n",
			opts:       Options{DefaultCountry: "KR"},
			wantPhones: []string{"+821012345678"},
			wantNames:  []string{""},
			wantVars:   []map[string]string{{}},
			total:      1,
		},
		{
			name:       "name whitespace collapsed",
			in:         "phone,name\n99112233,  Бат   Болд \n",
			wantPhones: []string{"+97699112233"},
			wantNames:  []string{"Бат Болд"},
			wantVars:   []map[string]string{{}},
			total:      1,
		},
		{
			name:       "header only",
			in:         "phone,name\n",
			wantPhones: []string{},
			wantNames:  []string{},
			wantVars:   nil,
			total:      0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := ParseTargets(strings.NewReader(tc.in), tc.opts)
			require.NoError(t, err)

			phones := make([]string, 0, len(res.Targets))
			names := make([]string, 0, len(res.Targets))
			for i, tg := range res.Targets {
				phones = append(phones, tg.Phone)
				names = append(names, tg.Name)
				assert.Equal(t, domain.TargetPending, tg.Status)
				require.NotNil(t, tg.Vars)
				if tc.wantVars != nil {
					assert.Equal(t, tc.wantVars[i], tg.Vars, "vars of row %d", i)
				}
			}
			assert.Equal(t, tc.wantPhones, phones)
			assert.Equal(t, tc.wantNames, names)
			assert.Equal(t, tc.total, res.Total)
			assert.Equal(t, len(tc.wantPhones), res.Imported)
			assert.Equal(t, tc.skipped, res.Skipped)
			assert.Equal(t, res.Total, res.Imported+res.Skipped)
			if tc.errs != nil {
				assert.Equal(t, tc.errs, res.Errors)
			} else {
				assert.Len(t, res.Errors, tc.skipped)
			}
			if tc.columns != nil {
				assert.Equal(t, tc.columns, res.Columns)
			}
		})
	}
}

func TestParseTargetsFallbackColumn(t *testing.T) {
	in := "customer,contact_info,notes\nBat,99112233,x\nSara,88112233,y\nDorj,n/a,z\n"
	res, err := ParseTargets(strings.NewReader(in), Options{})
	require.NoError(t, err)
	require.Len(t, res.Targets, 2)
	assert.Equal(t, "+97699112233", res.Targets[0].Phone)
	assert.Equal(t, map[string]string{"customer": "Bat", "notes": "x"}, res.Targets[0].Vars)
	require.Len(t, res.Errors, 1)
	assert.Equal(t, 4, res.Errors[0].Row)
}

func TestNoPhoneColumn(t *testing.T) {
	_, err := ParseTargets(strings.NewReader("name,city\nBat,UB\nSara,Darkhan\n"), Options{})
	require.ErrorIs(t, err, ErrNoPhoneColumn)
	assert.ErrorIs(t, err, domain.ErrInvalid)
}

func TestEmptyAndBadEncoding(t *testing.T) {
	for _, in := range []string{"", "\xEF\xBB\xBF", "\r\n\r\n  \r\n"} {
		_, err := ParseContacts(strings.NewReader(in), Options{})
		assert.ErrorIs(t, err, ErrEmptyFile, "%q", in)
	}
	_, err := ParseContacts(strings.NewReader("\xFF\xFEp\x00h\x00"), Options{})
	assert.ErrorIs(t, err, ErrEncoding)
}

func TestMaxRows(t *testing.T) {
	in := "phone\n99112230\n99112231\n99112232\n"
	res, err := ParseTargets(strings.NewReader(in), Options{MaxRows: 3})
	require.NoError(t, err)
	assert.Equal(t, 3, res.Imported)

	_, err = ParseTargets(strings.NewReader(in), Options{MaxRows: 2})
	require.ErrorIs(t, err, ErrTooManyRows)
	assert.ErrorIs(t, err, domain.ErrInvalid)

	// duplicates and invalid rows count towards the limit too
	_, err = ParseContacts(strings.NewReader("phone\n99112230\n99112230\nbad\n"), Options{MaxRows: 2})
	assert.ErrorIs(t, err, ErrTooManyRows)

	// blank lines do not
	res, err = ParseTargets(strings.NewReader("phone\n\n99112230\n\n\n99112231\n"), Options{MaxRows: 2})
	require.NoError(t, err)
	assert.Equal(t, 2, res.Imported)
}

func TestDefaultMaxRows(t *testing.T) {
	assert.Equal(t, 50000, Options{}.maxRows())
	assert.Equal(t, 50000, Options{MaxRows: -1}.maxRows())
	assert.Equal(t, 7, Options{MaxRows: 7}.maxRows())
}

func TestErrorCap(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("phone\n")
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&sb, "bad%d\n", i)
	}
	sb.WriteString("99112233\n")
	res, err := ParseTargets(strings.NewReader(sb.String()), Options{})
	require.NoError(t, err)
	assert.Equal(t, 501, res.Total)
	assert.Equal(t, 500, res.Skipped)
	assert.Equal(t, 1, res.Imported)
	require.Len(t, res.Errors, MaxKeptErrors)
	assert.Equal(t, 2, res.Errors[0].Row)
	assert.Equal(t, 1+MaxKeptErrors, res.Errors[MaxKeptErrors-1].Row)
}

func TestParseContacts(t *testing.T) {
	in := "\xEF\xBB\xBFутас;нэр;таг;хот;Тайлбар\r\n" +
		"99112233;Бат;vip;УБ;a\r\n" +
		"8811 2233;Сараа;vip | new,hot;Дархан;\r\n" +
		"99112233;Давхардсан;x;;\r\n" +
		"77112233;Дорж;;;\r\n" +
		"oops;Буруу;;;\r\n"
	res, err := ParseContacts(strings.NewReader(in), Options{})
	require.NoError(t, err)
	assert.Equal(t, 5, res.Total)
	assert.Equal(t, 3, res.Imported)
	assert.Equal(t, 2, res.Skipped)
	assert.Equal(t, []string{"утас", "нэр", "таг", "хот", "Тайлбар"}, res.Columns)
	require.Len(t, res.Contacts, 3)

	assert.Equal(t, "+97699112233", res.Contacts[0].Phone)
	assert.Equal(t, "Бат", res.Contacts[0].Name)
	assert.Equal(t, []string{"vip"}, res.Contacts[0].Tags)
	assert.Equal(t, map[string]string{"хот": "УБ", "Тайлбар": "a"}, res.Contacts[0].Meta)

	assert.Equal(t, []string{"vip", "new", "hot"}, res.Contacts[1].Tags)
	assert.Equal(t, map[string]string{"хот": "Дархан"}, res.Contacts[1].Meta)

	assert.Equal(t, []string{}, res.Contacts[2].Tags)
	assert.NotNil(t, res.Contacts[2].Meta)

	assert.Equal(t, []RowError{
		{4, "duplicate"},
		{6, `invalid phone number: "oops"`},
	}, res.Errors)
}

func TestTagSplitting(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", []string{}},
		{"a", []string{"a"}},
		{"a;b|c,d", []string{"a", "b", "c", "d"}},
		{" a ; ; b ;a", []string{"a", "b"}},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, splitTags(tc.in), tc.in)
	}
}

func TestLargeInputSpeed(t *testing.T) {
	const n = 10000
	var sb strings.Builder
	sb.WriteString("phone,name,city,note\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, "%d,Person %d,Ulaanbaatar,row %d\n", 90000000+i, i, i)
	}
	in := sb.String()

	start := time.Now()
	res, err := ParseTargets(strings.NewReader(in), Options{})
	elapsed := time.Since(start)
	require.NoError(t, err)
	assert.Equal(t, n, res.Imported)
	assert.Zero(t, res.Skipped)
	assert.Less(t, elapsed, 200*time.Millisecond)

	start = time.Now()
	cres, err := ParseContacts(strings.NewReader(in), Options{})
	elapsed = time.Since(start)
	require.NoError(t, err)
	assert.Equal(t, n, cres.Imported)
	assert.Less(t, elapsed, 200*time.Millisecond)
}

func TestTemplateRoundTrip(t *testing.T) {
	tpl := TemplateCSV()
	require.True(t, bytes.HasPrefix(tpl, []byte{0xEF, 0xBB, 0xBF}), "template must carry a UTF-8 BOM")
	assert.Contains(t, string(tpl), "phone,name,note\r\n")

	cres, err := ParseContacts(bytes.NewReader(tpl), Options{})
	require.NoError(t, err)
	assert.Equal(t, 0, cres.Skipped)
	require.Len(t, cres.Contacts, 2)
	assert.Equal(t, "+97699112233", cres.Contacts[0].Phone)
	assert.Equal(t, "Бат-Эрдэнэ", cres.Contacts[0].Name)
	assert.Equal(t, "Анхны дуудлага", cres.Contacts[0].Meta["note"])
	assert.Equal(t, "+97688001122", cres.Contacts[1].Phone)
	assert.Equal(t, []string{"phone", "name", "note"}, cres.Columns)

	tres, err := ParseTargets(bytes.NewReader(tpl), Options{})
	require.NoError(t, err)
	require.Len(t, tres.Targets, 2)
	assert.Equal(t, "VIP харилцагч", tres.Targets[1].Vars["note"])
}

func TestPreview(t *testing.T) {
	in := "\xEF\xBB\xBFУтас;Нэр;Таг;Хот\n99112233;Бат;vip;УБ\n88112233;Сараа\n77112233;Дорж;x;Дархан\n"
	p, err := Preview(strings.NewReader(in), 2)
	require.NoError(t, err)
	assert.Equal(t, []string{"Утас", "Нэр", "Таг", "Хот"}, p.Columns)
	assert.Equal(t, [][]string{
		{"99112233", "Бат", "vip", "УБ"},
		{"88112233", "Сараа"},
	}, p.Rows)
	assert.Equal(t, map[string]string{
		"Утас": FieldPhone, "Нэр": FieldName, "Таг": FieldTags, "Хот": FieldExtra,
	}, p.Mapping)

	// fallback detection shows up in the mapping
	p, err = Preview(strings.NewReader("who,digits\nBat,99112233\nSara,88112233\n"), 0)
	require.NoError(t, err)
	assert.Equal(t, FieldPhone, p.Mapping["digits"])
	assert.Equal(t, FieldExtra, p.Mapping["who"])
	assert.Len(t, p.Rows, 2)

	_, err = Preview(strings.NewReader(""), 5)
	assert.ErrorIs(t, err, ErrEmptyFile)
}

func TestReadErrorPropagates(t *testing.T) {
	r := io.MultiReader(strings.NewReader("phone\n99112233\n"), errReader{})
	_, err := ParseTargets(r, Options{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, errBoom))
}

var errBoom = errors.New("boom")

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errBoom }
