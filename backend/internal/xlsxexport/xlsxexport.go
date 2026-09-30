// Package xlsxexport renders campaign results as an Excel workbook: the
// imported target list with the AI call results appended (sheet "Targets")
// and totals per status and outcome (sheet "Summary").
package xlsxexport

import (
	"fmt"
	"io"
	"slices"
	"sort"
	"time"
	_ "time/tzdata" // campaign time zones must resolve on minimal hosts
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Sheet names.
const (
	SheetTargets = "Targets"
	SheetSummary = "Summary"
)

// DefaultTimezone is used when the campaign schedule has none.
const DefaultTimezone = "Asia/Ulaanbaatar"

// ContentType is the MIME type of the produced workbook.
const ContentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

const (
	dateFormat    = "yyyy-mm-dd hh:mm"
	maxCellRunes  = 32767 // Excel's limit per cell
	minColWidth   = 8
	maxColWidth   = 60
	widthPadding  = 2
	widthSampling = 2000 // rows inspected to size columns
)

// Result column headers appended after the imported columns.
const (
	HeaderPhone     = "Утас"
	HeaderName      = "Нэр"
	HeaderStatus    = "Төлөв"
	HeaderOutcome   = "Үр дүн"
	HeaderNote      = "Тайлбар"
	HeaderAttempts  = "Оролдлого"
	HeaderDuration  = "Хугацаа (сек)"
	HeaderSentiment = "Хандлага"
	HeaderSummary   = "Хураангуй"
	HeaderCalledAt  = "Дуудлагын огноо"
	HeaderRecording = "Бичлэг"
	HeaderCallID    = "Дуудлагын ID"
)

// StatusLabels are the Mongolian names of target statuses.
var StatusLabels = map[domain.CampaignTargetStatus]string{
	domain.TargetPending: "Хүлээгдэж буй",
	domain.TargetCalling: "Залгаж байна",
	domain.TargetDone:    "Дууссан",
	domain.TargetFailed:  "Амжилтгүй",
	domain.TargetSkipped: "Алгассан",
}

// statusOrder is the row order of the summary.
var statusOrder = []domain.CampaignTargetStatus{
	domain.TargetPending, domain.TargetCalling, domain.TargetDone, domain.TargetFailed, domain.TargetSkipped,
}

var sentimentLabels = map[domain.Sentiment]string{
	domain.SentimentPositive: "Эерэг",
	domain.SentimentNeutral:  "Төвийг сахисан",
	domain.SentimentNegative: "Сөрөг",
}

// lastErrorLabels explains well-known target errors in the note column.
var lastErrorLabels = map[string]string{
	"do_not_call": "Залгахгүй жагсаалтад байгаа",
}

// Filename returns a safe ASCII file name for the campaign export.
func Filename(c domain.Campaign) string {
	return "campaign-" + c.ID.String() + ".xlsx"
}

// WriteCampaign writes the campaign workbook to w. calls maps Call IDs (the
// targets' CallID) to the latest call of each target; missing calls leave the
// call columns blank. Var columns follow first-seen order across targets
// (keys of one target sorted alphabetically, since Vars is a map).
func WriteCampaign(w io.Writer, c domain.Campaign, targets []domain.CampaignTarget, calls map[uuid.UUID]domain.Call) error {
	loc := location(c.Schedule.Timezone)
	f := excelize.NewFile()
	defer f.Close()

	if err := f.SetSheetName("Sheet1", SheetTargets); err != nil {
		return fmt.Errorf("xlsxexport: rename sheet: %w", err)
	}
	if _, err := f.NewSheet(SheetSummary); err != nil {
		return fmt.Errorf("xlsxexport: add sheet: %w", err)
	}
	st, err := newStyles(f)
	if err != nil {
		return err
	}
	if err := writeTargets(f, st, c, targets, calls, loc); err != nil {
		return err
	}
	if err := writeSummary(f, st, c, targets, loc); err != nil {
		return err
	}
	f.SetActiveSheet(0)
	if _, err := f.WriteTo(w); err != nil {
		return fmt.Errorf("xlsxexport: write: %w", err)
	}
	return nil
}

type styles struct {
	header, date, bold int
}

func newStyles(f *excelize.File) (styles, error) {
	var st styles
	var err error
	st.header, err = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"E7EEF7"}},
		Alignment: &excelize.Alignment{Vertical: "center"},
		Border:    []excelize.Border{{Type: "bottom", Color: "9AA9BC", Style: 1}},
	})
	if err != nil {
		return st, fmt.Errorf("xlsxexport: header style: %w", err)
	}
	df := dateFormat
	if st.date, err = f.NewStyle(&excelize.Style{CustomNumFmt: &df}); err != nil {
		return st, fmt.Errorf("xlsxexport: date style: %w", err)
	}
	if st.bold, err = f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}}); err != nil {
		return st, fmt.Errorf("xlsxexport: bold style: %w", err)
	}
	return st, nil
}

func location(tz string) *time.Location {
	if tz == "" {
		tz = DefaultTimezone
	}
	if loc, err := time.LoadLocation(tz); err == nil {
		return loc
	}
	return time.UTC
}

// wallClock re-expresses t in loc as a zone-less time: Excel has no time
// zones, so the cell must hold the local wall-clock value.
func wallClock(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), 0, time.UTC)
}

// VarColumns lists the var keys of targets in first-seen order.
func VarColumns(targets []domain.CampaignTarget) []string {
	seen := map[string]bool{}
	var cols []string
	for _, t := range targets {
		keys := make([]string, 0, len(t.Vars))
		for k := range t.Vars {
			if !seen[k] {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			seen[k] = true
			cols = append(cols, k)
		}
	}
	return cols
}

func outcomeLabels(c domain.Campaign) map[string]string {
	m := make(map[string]string, len(c.Outcomes))
	for _, o := range c.Outcomes {
		if o.Label != "" {
			m[o.Code] = o.Label
		} else {
			m[o.Code] = o.Code
		}
	}
	return m
}

func clip(s string) string {
	if utf8.RuneCountInString(s) <= maxCellRunes {
		return s
	}
	r := []rune(s)
	return string(r[:maxCellRunes])
}

func writeTargets(f *excelize.File, st styles, c domain.Campaign, targets []domain.CampaignTarget,
	calls map[uuid.UUID]domain.Call, loc *time.Location) error {
	vars := VarColumns(targets)
	header := append([]string{HeaderPhone, HeaderName}, vars...)
	header = append(header, HeaderStatus, HeaderOutcome, HeaderNote, HeaderAttempts, HeaderDuration,
		HeaderSentiment, HeaderSummary, HeaderCalledAt, HeaderRecording, HeaderCallID)
	labels := outcomeLabels(c)

	rows := make([][]any, 0, len(targets))
	widths := make([]int, len(header))
	for i, h := range header {
		widths[i] = utf8.RuneCountInString(h)
	}
	for ti, t := range targets {
		row := make([]any, 0, len(header))
		row = append(row, t.Phone, clip(t.Name))
		for _, k := range vars {
			row = append(row, clip(t.Vars[k]))
		}
		var call *domain.Call
		if t.CallID != nil {
			if cl, ok := calls[*t.CallID]; ok {
				call = &cl
			}
		}
		status := StatusLabels[t.Status]
		if status == "" {
			status = string(t.Status)
		}
		outcome := t.Outcome
		if l, ok := labels[outcome]; ok {
			outcome = l
		}
		note := t.OutcomeNote
		if note == "" && t.LastError != "" {
			note = t.LastError
			if l, ok := lastErrorLabels[note]; ok {
				note = l
			}
		}
		row = append(row, status, outcome, clip(note), t.Attempts)
		if call != nil {
			var calledAt any = ""
			if !call.StartedAt.IsZero() {
				calledAt = excelize.Cell{StyleID: st.date, Value: wallClock(call.StartedAt, loc)}
			}
			row = append(row, call.DurationSec, sentimentLabels[call.Sentiment], clip(call.Summary),
				calledAt, call.RecordingURL, call.ID.String())
		} else {
			row = append(row, "", "", "", "", "", "")
		}
		rows = append(rows, row)
		if ti < widthSampling {
			for i, v := range row {
				widths[i] = max(widths[i], cellWidth(v))
			}
		}
	}

	sw, err := f.NewStreamWriter(SheetTargets)
	if err != nil {
		return fmt.Errorf("xlsxexport: stream writer: %w", err)
	}
	// Columns are set last to first: the stream writer prepends them and
	// Excel expects <col> elements in ascending order.
	for i := len(widths) - 1; i >= 0; i-- {
		wd := min(max(widths[i]+widthPadding, minColWidth), maxColWidth)
		if err := sw.SetColWidth(i+1, i+1, float64(wd)); err != nil {
			return fmt.Errorf("xlsxexport: column width: %w", err)
		}
	}
	if err := sw.SetPanes(&excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft",
		Selection: []excelize.Selection{{SQRef: "A2", ActiveCell: "A2", Pane: "bottomLeft"}}}); err != nil {
		return fmt.Errorf("xlsxexport: freeze header: %w", err)
	}
	hdr := make([]any, len(header))
	for i, h := range header {
		hdr[i] = excelize.Cell{StyleID: st.header, Value: h}
	}
	if err := sw.SetRow("A1", hdr, excelize.RowOpts{Height: 20}); err != nil {
		return fmt.Errorf("xlsxexport: header row: %w", err)
	}
	for i, row := range rows {
		for j, v := range row {
			if v == "" {
				row[j] = nil // a truly blank cell, not an empty string
			}
		}
		cell, _ := excelize.CoordinatesToCellName(1, i+2)
		if err := sw.SetRow(cell, row); err != nil {
			return fmt.Errorf("xlsxexport: row %d: %w", i+2, err)
		}
	}
	last, _ := excelize.CoordinatesToCellName(len(header), len(rows)+1)
	// The stream writer serialises the worksheet's autoFilter on Flush.
	if err := f.AutoFilter(SheetTargets, "A1:"+last, nil); err != nil {
		return fmt.Errorf("xlsxexport: autofilter: %w", err)
	}
	if err := sw.Flush(); err != nil {
		return fmt.Errorf("xlsxexport: flush: %w", err)
	}
	return nil
}

func cellWidth(v any) int {
	switch x := v.(type) {
	case string:
		return utf8.RuneCountInString(x)
	case excelize.Cell:
		if _, ok := x.Value.(time.Time); ok {
			return len(dateFormat)
		}
		return cellWidth(x.Value)
	case int:
		return len(fmt.Sprint(x))
	}
	return 0
}

func writeSummary(f *excelize.File, st styles, c domain.Campaign, targets []domain.CampaignTarget, loc *time.Location) error {
	byStatus := map[domain.CampaignTargetStatus]int{}
	byOutcome := map[string]int{}
	var unknown []string
	known := outcomeLabels(c)
	for _, t := range targets {
		byStatus[t.Status]++
		byOutcome[t.Outcome]++
		if _, ok := known[t.Outcome]; !ok && t.Outcome != "" && byOutcome[t.Outcome] == 1 {
			unknown = append(unknown, t.Outcome)
		}
	}
	sort.Strings(unknown)

	type line struct {
		label string
		value any
		bold  bool
	}
	lines := []line{
		{"Кампанит ажил", c.Name, true},
		{"Экспортолсон", excelize.Cell{StyleID: st.date, Value: wallClock(time.Now(), loc)}, false},
		{"Нийт", len(targets), true},
		{},
		{HeaderStatus, "Тоо", true},
	}
	statuses := slices.Clone(statusOrder)
	for s := range byStatus {
		if !slices.Contains(statuses, s) {
			statuses = append(statuses, s)
		}
	}
	for _, s := range statuses {
		label := StatusLabels[s]
		if label == "" {
			label = string(s)
		}
		lines = append(lines, line{label: label, value: byStatus[s]})
	}
	lines = append(lines, line{}, line{HeaderOutcome, "Тоо", true})
	for _, o := range c.Outcomes {
		lines = append(lines, line{label: known[o.Code], value: byOutcome[o.Code]})
	}
	for _, code := range unknown {
		lines = append(lines, line{label: code, value: byOutcome[code]})
	}
	lines = append(lines, line{label: "Үр дүнгүй", value: byOutcome[""]})

	for i, l := range lines {
		r := i + 1
		if l.label == "" && l.value == nil {
			continue
		}
		a, _ := excelize.CoordinatesToCellName(1, r)
		b, _ := excelize.CoordinatesToCellName(2, r)
		if err := f.SetCellValue(SheetSummary, a, l.label); err != nil {
			return fmt.Errorf("xlsxexport: summary: %w", err)
		}
		switch v := l.value.(type) {
		case excelize.Cell:
			if err := f.SetCellValue(SheetSummary, b, v.Value); err != nil {
				return fmt.Errorf("xlsxexport: summary: %w", err)
			}
			if err := f.SetCellStyle(SheetSummary, b, b, v.StyleID); err != nil {
				return fmt.Errorf("xlsxexport: summary: %w", err)
			}
		default:
			if err := f.SetCellValue(SheetSummary, b, v); err != nil {
				return fmt.Errorf("xlsxexport: summary: %w", err)
			}
		}
		if l.bold {
			style := st.bold
			if l.value == "Тоо" {
				style = st.header
			}
			if err := f.SetCellStyle(SheetSummary, a, b, style); err != nil {
				return fmt.Errorf("xlsxexport: summary: %w", err)
			}
		}
	}
	if err := f.SetColWidth(SheetSummary, "A", "A", 28); err != nil {
		return fmt.Errorf("xlsxexport: summary: %w", err)
	}
	if err := f.SetColWidth(SheetSummary, "B", "B", 24); err != nil {
		return fmt.Errorf("xlsxexport: summary: %w", err)
	}
	return nil
}
