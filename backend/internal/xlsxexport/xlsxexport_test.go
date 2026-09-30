package xlsxexport

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xuri/excelize/v2"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func sample() (domain.Campaign, []domain.CampaignTarget, map[uuid.UUID]domain.Call) {
	c := domain.Campaign{
		ID: uuid.New(), Name: "Намрын урамшуулал",
		Outcomes: []domain.CampaignOutcome{
			{Code: "agreed", Label: "Зөвшөөрсөн", Terminal: true},
			{Code: "declined", Label: "Татгалзсан", Terminal: true},
			{Code: "callback", Label: "Дахин залгах"},
		},
	}
	call1 := domain.Call{ID: uuid.New(), DurationSec: 95, Sentiment: domain.SentimentPositive, Summary: "Захиалга өгөхөөр тохирсон",
		StartedAt: time.Date(2026, 9, 30, 10, 30, 0, 0, time.UTC), RecordingURL: "https://rec.example/1.ogg"}
	call2 := domain.Call{ID: uuid.New(), DurationSec: 12, Sentiment: domain.SentimentNegative,
		StartedAt: time.Date(2026, 9, 30, 11, 0, 0, 0, time.UTC)}
	targets := []domain.CampaignTarget{
		{ID: uuid.New(), Phone: "+97699112233", Name: "Бат", Vars: map[string]string{"хот": "УБ", "amount": "12000"},
			Status: domain.TargetDone, Attempts: 1, CallID: &call1.ID, Outcome: "agreed", OutcomeNote: "Худалдан авна гэсэн"},
		{ID: uuid.New(), Phone: "+97688112233", Name: "Сараа", Vars: map[string]string{"amount": "500", "zz": "last"},
			Status: domain.TargetDone, Attempts: 2, CallID: &call2.ID, Outcome: "declined"},
		{ID: uuid.New(), Phone: "+97695112233", Status: domain.TargetSkipped, LastError: "do_not_call"},
		{ID: uuid.New(), Phone: "+97680112233", Status: domain.TargetPending},
		{ID: uuid.New(), Phone: "+97680112299", Status: domain.TargetDone, Outcome: "legacy_code", Attempts: 1},
	}
	return c, targets, map[uuid.UUID]domain.Call{call1.ID: call1, call2.ID: call2}
}

func TestWriteCampaign(t *testing.T) {
	c, targets, calls := sample()
	var buf bytes.Buffer
	require.NoError(t, WriteCampaign(&buf, c, targets, calls))

	f, err := excelize.OpenReader(bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
	defer f.Close()
	assert.Equal(t, []string{SheetTargets, SheetSummary}, f.GetSheetList())

	rows, err := f.GetRows(SheetTargets)
	require.NoError(t, err)
	require.Len(t, rows, 6)
	assert.Equal(t, []string{"Утас", "Нэр", "amount", "хот", "zz", "Төлөв", "Үр дүн", "Тайлбар", "Оролдлого",
		"Хугацаа (сек)", "Хандлага", "Хураангуй", "Дуудлагын огноо", "Бичлэг", "Дуудлагын ID"}, rows[0])
	assert.Equal(t, []string{"+97699112233", "Бат", "12000", "УБ", "", "Дууссан", "Зөвшөөрсөн", "Худалдан авна гэсэн", "1",
		"95", "Эерэг", "Захиалга өгөхөөр тохирсон", "2026-09-30 18:30", "https://rec.example/1.ogg", calls[*targets[0].CallID].ID.String()}, rows[1])
	assert.Equal(t, "Татгалзсан", rows[2][6])
	assert.Equal(t, "last", rows[2][4])
	assert.Equal(t, "2026-09-30 19:00", rows[2][12])
	assert.Equal(t, []string{"+97695112233", "", "", "", "", "Алгассан", "", "Залгахгүй жагсаалтад байгаа", "0"}, rows[3])
	assert.Equal(t, "Хүлээгдэж буй", rows[4][5])
	assert.Equal(t, "legacy_code", rows[5][6], "unknown outcome codes are shown as-is")

	// Numbers are real numbers; the date is a date serial with our format.
	typ, err := f.GetCellType(SheetTargets, "J2")
	require.NoError(t, err)
	assert.NotEqual(t, excelize.CellTypeSharedString, typ)
	raw, err := f.GetCellValue(SheetTargets, "M2", excelize.Options{RawCellValue: true})
	require.NoError(t, err)
	assert.NotContains(t, raw, "-", "date cell holds a serial number")
	// Phone stays text.
	typ, err = f.GetCellType(SheetTargets, "A2")
	require.NoError(t, err)
	assert.NotEqual(t, excelize.CellTypeNumber, typ)

	panes, err := f.GetPanes(SheetTargets)
	require.NoError(t, err)
	assert.True(t, panes.Freeze)
	assert.Equal(t, 1, panes.YSplit)

	hdrStyle, err := f.GetCellStyle(SheetTargets, "A1")
	require.NoError(t, err)
	st, err := f.GetStyle(hdrStyle)
	require.NoError(t, err)
	require.NotNil(t, st.Font)
	assert.True(t, st.Font.Bold)

	width, err := f.GetColWidth(SheetTargets, "L")
	require.NoError(t, err)
	assert.Greater(t, width, 20.0)

	sheetXML := zipEntry(t, buf.Bytes(), "xl/worksheets/sheet1.xml")
	assert.Contains(t, sheetXML, `<autoFilter ref="$A$1:$O$6"`)
	assert.Less(t, strings.Index(sheetXML, `<col min="1"`), strings.Index(sheetXML, `<col min="2"`), "cols ascending")
	assert.NotContains(t, sheetXML, "<t></t>", "blank cells carry no empty strings")

	sum, err := f.GetRows(SheetSummary)
	require.NoError(t, err)
	got := map[string]string{}
	for _, r := range sum {
		if len(r) >= 2 {
			got[r[0]] = r[1]
		}
	}
	assert.Equal(t, c.Name, got["Кампанит ажил"])
	assert.Equal(t, "5", got["Нийт"])
	assert.Equal(t, "3", got["Дууссан"])
	assert.Equal(t, "1", got["Алгассан"])
	assert.Equal(t, "1", got["Хүлээгдэж буй"])
	assert.Equal(t, "0", got["Амжилтгүй"])
	assert.Equal(t, "1", got["Зөвшөөрсөн"])
	assert.Equal(t, "1", got["Татгалзсан"])
	assert.Equal(t, "0", got["Дахин залгах"])
	assert.Equal(t, "1", got["legacy_code"])
	assert.Equal(t, "2", got["Үр дүнгүй"])
}

func TestWriteCampaignEmpty(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, WriteCampaign(&buf, domain.Campaign{ID: uuid.New(), Name: "x", Schedule: domain.CampaignSchedule{Timezone: "Bad/Zone"}}, nil, nil))
	f, err := excelize.OpenReader(&buf)
	require.NoError(t, err)
	defer f.Close()
	rows, err := f.GetRows(SheetTargets)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, HeaderPhone, rows[0][0])
}

func TestVarColumnsAndFilename(t *testing.T) {
	cols := VarColumns([]domain.CampaignTarget{
		{Vars: map[string]string{"b": "1", "a": "2"}},
		{Vars: map[string]string{"c": "1", "a": "2"}},
	})
	assert.Equal(t, []string{"a", "b", "c"}, cols)
	id := uuid.New()
	assert.Equal(t, "campaign-"+id.String()+".xlsx", Filename(domain.Campaign{ID: id}))
}

func zipEntry(t *testing.T, data []byte, name string) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			require.NoError(t, err)
			defer rc.Close()
			b, err := io.ReadAll(rc)
			require.NoError(t, err)
			return string(b)
		}
	}
	t.Fatalf("zip entry %s not found", name)
	return ""
}
