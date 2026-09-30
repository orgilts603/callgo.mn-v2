package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xuri/excelize/v2"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func xlsxFile(t *testing.T, rows [][]any) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	for i, row := range rows {
		cell, err := excelize.CoordinatesToCellName(1, i+1)
		require.NoError(t, err)
		require.NoError(t, f.SetSheetRow("Sheet1", cell, &row))
	}
	var buf bytes.Buffer
	_, err := f.WriteTo(&buf)
	require.NoError(t, err)
	return buf.Bytes()
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

type createResp struct {
	Campaign domain.Campaign `json:"campaign"`
	Targets  importResult    `json:"targets"`
}

// seedCampaign creates a campaign through the API and returns it.
func (e *env) createCampaign(fields map[string]string, filename string, file []byte) createResp {
	e.t.Helper()
	w := e.upload("/api/campaigns", e.adminTok, fields, filename, file)
	require.Equal(e.t, http.StatusCreated, w.Code, w.Body.String())
	return decode[createResp](e.t, w)
}

func TestCampaignPreview(t *testing.T) {
	e := newEnv(t)
	w := e.upload("/api/campaigns/preview", e.opTok, nil, "list.csv", []byte("Утас;Нэр;Хот\n99112233;Бат;УБ\n88112233;Сараа\n"))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	p := decode[PreviewResult](t, w)
	assert.Equal(t, "csv", p.Format)
	assert.Equal(t, 2, p.Total)
	assert.Equal(t, []string{"Утас", "Нэр", "Хот"}, p.Columns)
	assert.Equal(t, [][]string{{"99112233", "Бат", "УБ"}, {"88112233", "Сараа", ""}}, p.Rows)
	assert.Equal(t, map[string]string{"Утас": "phone", "Нэр": "name", "Хот": "var"}, p.Mapping)

	rows := [][]any{{"Нэр", "Утасны дугаар", "Огноо"}}
	for i := range 15 {
		rows = append(rows, []any{"Хүн", 99112200 + i, "2026-10-01"})
	}
	// Named .csv on purpose: the ZIP magic wins.
	w = e.upload("/api/campaigns/preview", e.opTok, nil, "list.csv", xlsxFile(t, rows))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	p = decode[PreviewResult](t, w)
	assert.Equal(t, "xlsx", p.Format)
	assert.Equal(t, 15, p.Total)
	require.Len(t, p.Rows, 10)
	assert.Equal(t, []string{"Хүн", "99112200", "2026-10-01"}, p.Rows[0])
	assert.Equal(t, "phone", p.Mapping["Утасны дугаар"])

	requireErr(t, e.upload("/api/campaigns/preview", e.opTok, nil, "x.csv", nil), http.StatusBadRequest, "invalid")
	w = e.upload("/api/campaigns/preview", e.opTok, nil, "old.xls", append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, make([]byte, 64)...))
	requireErr(t, w, http.StatusBadRequest, "invalid")
	assert.Contains(t, w.Body.String(), ".xlsx")
}

func TestCampaignCreateV2(t *testing.T) {
	e := newEnv(t)
	p := e.seedProfile(e.org.ID, nil)
	num := e.seedNumber(e.org.ID, "+97677001234", &p.ID)
	ctx := context.Background()
	require.NoError(t, e.dnc.AddDoNotCall(ctx, &domain.DoNotCallEntry{OrgID: e.org.ID, Phone: "+97688112233"}))
	require.NoError(t, e.dnc.AddDoNotCall(ctx, &domain.DoNotCallEntry{OrgID: e.org2.ID, Phone: "+97699112233"}))

	schedule := map[string]any{"timezone": "", "weekdays": []int{5, 1, 1, 2}, "startTime": "9:00", "endTime": "18:00", "pacePerMinute": 10}
	outcomes := []map[string]any{
		{"code": "agreed", "label": " Зөвшөөрсөн ", "description": "yes", "terminal": true},
		{"code": "callback", "label": "Дахин залгах", "terminal": false},
	}
	fields := map[string]string{"name": "Excel", "sipNumberId": num.ID.String(), "dryRunLimit": "3",
		"schedule": mustJSON(t, schedule), "outcomes": mustJSON(t, outcomes)}
	file := xlsxFile(t, [][]any{{"Утас", "Нэр", "Дүн"}, {99112233, "Бат", 5000}, {88112233, "Сараа", 100}, {95112233, "Дорж", 1}, {"bad", "x", 0}})

	res := e.createCampaign(fields, "Жагсаалт.xlsx", file)
	c := res.Campaign
	assert.Equal(t, 3, c.DryRunLimit)
	assert.Equal(t, domain.CampaignSchedule{Timezone: "Asia/Ulaanbaatar", Weekdays: []time.Weekday{1, 2, 5},
		StartTime: "09:00", EndTime: "18:00", PacePerMinute: 10}, c.Schedule)
	require.Len(t, c.Outcomes, 2)
	assert.Equal(t, "Зөвшөөрсөн", c.Outcomes[0].Label)
	assert.True(t, c.Outcomes[0].Terminal)
	assert.Equal(t, 3, c.Total)
	assert.Equal(t, 1, c.Skipped)
	assert.Equal(t, 2, res.Targets.Imported)
	assert.Equal(t, 2, res.Targets.Skipped, "one invalid row + one do-not-call")
	assert.Equal(t, 1, res.Targets.DoNotCall)

	targets, err := e.db.ListAllTargets(ctx, c.ID)
	require.NoError(t, err)
	require.Len(t, targets, 3)
	byPhone := map[string]domain.CampaignTarget{}
	for _, tg := range targets {
		byPhone[tg.Phone] = tg
	}
	assert.Equal(t, domain.TargetSkipped, byPhone["+97688112233"].Status)
	assert.Equal(t, "do_not_call", byPhone["+97688112233"].LastError)
	assert.Equal(t, domain.TargetPending, byPhone["+97699112233"].Status, "other org's list does not apply")
	assert.Equal(t, "5000", byPhone["+97699112233"].Vars["Дүн"])

	// Without outcomes/schedule fields: empty outcomes, zero schedule.
	res = e.createCampaign(map[string]string{"name": "Plain", "sipNumberId": num.ID.String()}, "a.csv", []byte("phone\n99000001\n"))
	assert.NotNil(t, res.Campaign.Outcomes)
	assert.Empty(t, res.Campaign.Outcomes)
	assert.True(t, res.Campaign.Schedule.IsZero())
	assert.Equal(t, 0, res.Campaign.DryRunLimit)

	for name, mut := range map[string]func(map[string]string){
		"outcomes not json":   func(m map[string]string) { m["outcomes"] = "{" },
		"outcome empty code":  func(m map[string]string) { m["outcomes"] = `[{"code":"","label":"x"}]` },
		"outcome camelCase":   func(m map[string]string) { m["outcomes"] = `[{"code":"wrongNumber","label":"x"}]` },
		"outcome dup":         func(m map[string]string) { m["outcomes"] = `[{"code":"a","label":"x"},{"code":"a","label":"y"}]` },
		"outcome no label":    func(m map[string]string) { m["outcomes"] = `[{"code":"a","label":"  "}]` },
		"schedule bad time":   func(m map[string]string) { m["schedule"] = `{"startTime":"25:00"}` },
		"schedule same times": func(m map[string]string) { m["schedule"] = `{"startTime":"09:00","endTime":"9:00"}` },
		"schedule weekday":    func(m map[string]string) { m["schedule"] = `{"weekdays":[7]}` },
		"schedule tz":         func(m map[string]string) { m["schedule"] = `{"timezone":"Mars/Base"}` },
		"schedule pace":       func(m map[string]string) { m["schedule"] = `{"pacePerMinute":-1}` },
		"dry run negative":    func(m map[string]string) { m["dryRunLimit"] = "-1" },
	} {
		t.Run(name, func(t *testing.T) {
			f := map[string]string{"name": "X", "sipNumberId": num.ID.String()}
			mut(f)
			requireErr(t, e.upload("/api/campaigns", e.adminTok, f, "a.csv", []byte("phone\n99000001\n")), http.StatusBadRequest, "invalid")
		})
	}

	// Every number on the list → nothing to dial.
	w := e.upload("/api/campaigns", e.adminTok, map[string]string{"name": "X", "sipNumberId": num.ID.String()}, "a.csv", []byte("phone\n88112233\n"))
	requireErr(t, w, http.StatusBadRequest, "invalid")
}

func TestCampaignUpdate(t *testing.T) {
	e := newEnv(t)
	p := e.seedProfile(e.org.ID, nil)
	p2 := e.seedProfile(e.org.ID, nil)
	num := e.seedNumber(e.org.ID, "+97677001234", &p.ID)
	otherNum := e.seedNumber(e.org2.ID, "+97677009999", nil)
	c := e.createCampaign(map[string]string{"name": "Old", "sipNumberId": num.ID.String()}, "a.csv", []byte("phone\n99000001\n")).Campaign
	path := "/api/campaigns/" + c.ID.String()

	// Draft: everything may change.
	body := map[string]any{"name": " New ", "script": "Hi", "agentProfileId": p2.ID.String(), "concurrency": 7, "maxAttempts": 3,
		"dryRunLimit": 5, "schedule": map[string]any{"startTime": "22:00", "endTime": "06:00"},
		"outcomes": []map[string]any{{"code": "agreed", "label": "Тийм", "terminal": true}}}
	w := e.do(http.MethodPut, path, e.adminTok, body)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	got := decode[struct {
		Campaign domain.Campaign `json:"campaign"`
	}](t, w).Campaign
	assert.Equal(t, "New", got.Name)
	assert.Equal(t, "Hi", got.Script)
	assert.Equal(t, p2.ID, *got.AgentProfileID)
	assert.Equal(t, 7, got.Concurrency)
	assert.Equal(t, 3, got.MaxAttempts)
	assert.Equal(t, 5, got.DryRunLimit)
	assert.Equal(t, "22:00", got.Schedule.StartTime)
	assert.Equal(t, "Asia/Ulaanbaatar", got.Schedule.Timezone)
	require.Len(t, got.Outcomes, 1)
	stored, _ := e.db.GetCampaign(context.Background(), c.ID)
	assert.Equal(t, "New", stored.Name)
	assert.NotEmpty(t, e.bus.ofType(domain.EventCampaignProgress))

	// Partial update keeps the rest.
	w = e.do(http.MethodPut, path, e.adminTok, map[string]any{"concurrency": 2})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	stored, _ = e.db.GetCampaign(context.Background(), c.ID)
	assert.Equal(t, 2, stored.Concurrency)
	assert.Equal(t, "Hi", stored.Script)

	requireErr(t, e.do(http.MethodPut, path, e.adminTok, map[string]any{"concurrency": 0}), http.StatusBadRequest, "invalid")
	requireErr(t, e.do(http.MethodPut, path, e.adminTok, map[string]any{"name": ""}), http.StatusBadRequest, "invalid")
	requireErr(t, e.do(http.MethodPut, path, e.adminTok, map[string]any{"sipNumberId": otherNum.ID.String()}), http.StatusBadRequest, "invalid")
	requireErr(t, e.do(http.MethodPut, path, e.adminTok, map[string]any{"outcomes": []map[string]any{{"code": "Bad Code", "label": "x"}}}), http.StatusBadRequest, "invalid")
	requireErr(t, e.do(http.MethodPut, path, e.opTok, map[string]any{"concurrency": 3}), http.StatusForbidden, "forbidden")
	requireErr(t, e.do(http.MethodPut, "/api/campaigns/"+c.ID.String(), e.othTok, map[string]any{"concurrency": 3}), http.StatusNotFound, "not_found")

	// Running: only schedule / concurrency / outcomes; unchanged values are fine.
	require.Equal(t, http.StatusOK, e.do(http.MethodPost, path+"/start", e.adminTok, nil).Code)
	w = e.do(http.MethodPut, path, e.adminTok, map[string]any{"name": "New", "concurrency": 4,
		"schedule": map[string]any{}, "outcomes": []map[string]any{}})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	stored, _ = e.db.GetCampaign(context.Background(), c.ID)
	assert.Equal(t, 4, stored.Concurrency)
	assert.True(t, stored.Schedule.IsZero())
	assert.Empty(t, stored.Outcomes)
	assert.Equal(t, domain.CampaignRunning, stored.Status)
	for _, b := range []map[string]any{{"name": "Other"}, {"script": "x"}, {"maxAttempts": 1}, {"dryRunLimit": 1}, {"agentProfileId": p.ID.String()}} {
		requireErr(t, e.do(http.MethodPut, path, e.adminTok, b), http.StatusConflict, "conflict")
	}

	// Completed: nothing.
	stored.Status = domain.CampaignCompleted
	require.NoError(t, e.db.UpdateCampaign(context.Background(), stored))
	requireErr(t, e.do(http.MethodPut, path, e.adminTok, map[string]any{"concurrency": 3}), http.StatusConflict, "conflict")
}

func TestCampaignStartDryRun(t *testing.T) {
	e := newEnv(t)
	p := e.seedProfile(e.org.ID, nil)
	num := e.seedNumber(e.org.ID, "+97677001234", &p.ID)
	c := e.createCampaign(map[string]string{"name": "D", "sipNumberId": num.ID.String(), "dryRunLimit": "5"}, "a.csv", []byte("phone\n99000001\n")).Campaign
	path := "/api/campaigns/" + c.ID.String()

	require.Equal(t, http.StatusOK, e.do(http.MethodPost, path+"/start", e.adminTok, nil).Code, "no body → stored limit")
	require.Equal(t, http.StatusOK, e.do(http.MethodPost, path+"/start", e.adminTok, map[string]any{"dryRunLimit": 2}).Code)
	require.Equal(t, http.StatusOK, e.do(http.MethodPost, path+"/start", e.adminTok, map[string]any{"dryRunLimit": 0}).Code)
	require.Equal(t, http.StatusOK, e.do(http.MethodPost, path+"/start", e.adminTok, map[string]any{}).Code)
	assert.Equal(t, []int{5, 2, 0, 5}, e.ctl.starts)
	requireErr(t, e.do(http.MethodPost, path+"/start", e.adminTok, map[string]any{"dryRunLimit": -1}), http.StatusBadRequest, "invalid")
	requireErr(t, e.do(http.MethodPost, path+"/start", e.adminTok, "{"), http.StatusBadRequest, "invalid")
}

func seedResults(t *testing.T, e *env) (domain.Campaign, domain.Call) {
	t.Helper()
	ctx := context.Background()
	camp := domain.Campaign{ID: uuid.New(), OrgID: e.org.ID, Name: "Намар/2026: \"VIP\"", Status: domain.CampaignRunning,
		Outcomes: []domain.CampaignOutcome{{Code: "agreed", Label: "Зөвшөөрсөн", Terminal: true}, {Code: "declined", Label: "Татгалзсан", Terminal: true}}}
	started := time.Date(2026, 9, 30, 2, 0, 0, 0, time.UTC)
	call := e.seedCall(e.org.ID, domain.StatusCompleted, func(c *domain.Call) {
		c.CampaignID, c.Direction, c.ToNumber, c.DurationSec, c.Summary, c.Sentiment = &camp.ID, domain.DirectionOutbound, "+97699000001", 61, "Тохирсон", domain.SentimentPositive
		c.StartedAt = started
	})
	targets := []domain.CampaignTarget{
		{ID: uuid.New(), CampaignID: camp.ID, Phone: "+97699000001", Name: "Бат", Vars: map[string]string{"Дүн": "5000"}, Status: domain.TargetDone, Attempts: 1, CallID: &call.ID, Outcome: "agreed", OutcomeNote: "Авна"},
		{ID: uuid.New(), CampaignID: camp.ID, Phone: "+97699000002", Status: domain.TargetDone, Attempts: 2, Outcome: "old_code"},
		{ID: uuid.New(), CampaignID: camp.ID, Phone: "+97699000003", Status: domain.TargetSkipped, LastError: "do_not_call"},
		{ID: uuid.New(), CampaignID: camp.ID, Phone: "+97699000004", Status: domain.TargetPending},
	}
	require.NoError(t, e.db.CreateCampaign(ctx, &camp, targets))
	return camp, call
}

func TestCampaignExport(t *testing.T) {
	e := newEnv(t)
	camp, call := seedResults(t, e)
	w := e.do(http.MethodGet, "/api/campaigns/"+camp.ID.String()+"/export.xlsx", e.opTok, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", w.Header().Get("Content-Type"))
	cd := w.Header().Get("Content-Disposition")
	assert.True(t, strings.HasPrefix(cd, `attachment; filename="campaign-`+camp.ID.String()+`.xlsx"; filename*=UTF-8''`), cd)
	encoded := cd[strings.Index(cd, "UTF-8''")+len("UTF-8''"):]
	decoded, err := url.PathUnescape(encoded)
	require.NoError(t, err)
	assert.Equal(t, `Намар_2026_ _VIP_.xlsx`, decoded)
	for _, r := range cd {
		require.Less(t, r, rune(0x80), "header must be ASCII")
	}

	f, err := excelize.OpenReader(bytes.NewReader(w.Body.Bytes()))
	require.NoError(t, err)
	defer f.Close()
	assert.Equal(t, []string{"Targets", "Summary"}, f.GetSheetList())
	rows, err := f.GetRows("Targets")
	require.NoError(t, err)
	require.Len(t, rows, 5)
	assert.Equal(t, []string{"Утас", "Нэр", "Дүн", "Төлөв", "Үр дүн", "Тайлбар", "Оролдлого", "Хугацаа (сек)", "Хандлага",
		"Хураангуй", "Дуудлагын огноо", "Бичлэг", "Дуудлагын ID"}, rows[0])
	assert.Equal(t, []string{"+97699000001", "Бат", "5000", "Дууссан", "Зөвшөөрсөн", "Авна", "1", "61", "Эерэг", "Тохирсон",
		"2026-09-30 10:00", "", call.ID.String()}, rows[1])

	requireErr(t, e.do(http.MethodGet, "/api/campaigns/"+camp.ID.String()+"/export.xlsx", e.othTok, nil), http.StatusNotFound, "not_found")
}

type statsBody struct {
	ByStatus  map[string]int `json:"byStatus"`
	ByOutcome []outcomeCount `json:"byOutcome"`
}

func TestCampaignStatsEndpoint(t *testing.T) {
	e := newEnv(t)
	camp, _ := seedResults(t, e)
	path := "/api/campaigns/" + camp.ID.String() + "/stats"
	w := e.do(http.MethodGet, path, e.opTok, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	got := decode[statsBody](t, w)
	assert.Equal(t, map[string]int{"pending": 1, "calling": 0, "done": 2, "failed": 0, "skipped": 1}, got.ByStatus)
	assert.Equal(t, []outcomeCount{{"agreed", "Зөвшөөрсөн", 1}, {"declined", "Татгалзсан", 0}, {"old_code", "old_code", 1}}, got.ByOutcome)
	requireErr(t, e.do(http.MethodGet, path, e.othTok, nil), http.StatusNotFound, "not_found")

	// A dedicated counter is preferred over scanning targets.
	e2 := newEnv(t, func(d *Deps, _ *Config) {
		d.CampaignStats = fakeStats{byStatus: map[domain.CampaignTargetStatus]int{domain.TargetDone: 9}, byOutcome: map[string]int{"declined": 4}}
	})
	camp2, _ := seedResults(t, e2)
	got = decode[statsBody](t, e2.do(http.MethodGet, "/api/campaigns/"+camp2.ID.String()+"/stats", e2.opTok, nil))
	assert.Equal(t, 9, got.ByStatus["done"])
	assert.Equal(t, 0, got.ByStatus["pending"])
	assert.Equal(t, []outcomeCount{{"agreed", "Зөвшөөрсөн", 0}, {"declined", "Татгалзсан", 4}}, got.ByOutcome)
}

type dncEntryBody struct {
	Entry domain.DoNotCallEntry `json:"entry"`
}

func TestDNC(t *testing.T) {
	for _, inserter := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain repo", true: "inserter"}[inserter], func(t *testing.T) {
			e := newEnv(t, func(d *Deps, _ *Config) {
				if inserter {
					d.DNC = fakeDNCInserter{d.DNC.(*fakeDNC)}
				}
			})
			w := e.do(http.MethodPost, "/api/dnc", e.opTok, map[string]any{"phone": "9911-2233", "reason": " asked "})
			require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
			entry := decode[dncEntryBody](t, w).Entry
			assert.Equal(t, "+97699112233", entry.Phone)
			assert.Equal(t, "asked", entry.Reason)
			require.NotNil(t, entry.CreatedBy)
			assert.Equal(t, e.operator.ID, *entry.CreatedBy)

			w = e.do(http.MethodPost, "/api/dnc", e.opTok, map[string]any{"phone": "+976 99112233", "reason": "again"})
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.Equal(t, entry.ID, decode[dncEntryBody](t, w).Entry.ID)
			requireErr(t, e.do(http.MethodPost, "/api/dnc", e.opTok, map[string]any{"phone": "abc"}), http.StatusBadRequest, "invalid")

			l := decode[list[domain.DoNotCallEntry]](t, e.do(http.MethodGet, "/api/dnc?q=9911", e.opTok, nil))
			assert.Equal(t, 1, l.Total)
			assert.Empty(t, decode[list[domain.DoNotCallEntry]](t, e.do(http.MethodGet, "/api/dnc", e.othTok, nil)).Items)
			requireErr(t, e.do(http.MethodGet, "/api/dnc?limit=0", e.opTok, nil), http.StatusBadRequest, "invalid")

			// Import (xlsx, phones only).
			file := xlsxFile(t, [][]any{{"Утас", "Нэр"}, {99112233, "dup of existing"}, {88112233, "A"}, {88112233, "dup"}, {"zz", "bad"}, {95112233, "B"}})
			w = e.upload("/api/dnc/import", e.adminTok, map[string]string{"reason": "bulk"}, "dnc.xlsx", file)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			imp := decode[importResult](t, w)
			assert.Equal(t, 2, imp.Imported)
			assert.Equal(t, 3, imp.Skipped)
			assert.Len(t, imp.Errors, 2) // in-file duplicate + invalid phone
			l = decode[list[domain.DoNotCallEntry]](t, e.do(http.MethodGet, "/api/dnc", e.opTok, nil))
			assert.Equal(t, 3, l.Total)
			requireErr(t, e.upload("/api/dnc/import", e.opTok, nil, "dnc.xlsx", file), http.StatusForbidden, "forbidden")

			// Delete: URL-encoded E.164, also accepts other spellings.
			require.Equal(t, http.StatusNoContent, e.do(http.MethodDelete, "/api/dnc/%2B97699112233", e.adminTok, nil).Code)
			require.Equal(t, http.StatusNoContent, e.do(http.MethodDelete, "/api/dnc/88112233", e.adminTok, nil).Code)
			require.Equal(t, http.StatusNoContent, e.do(http.MethodDelete, "/api/dnc/+97695112233", e.adminTok, nil).Code)
			requireErr(t, e.do(http.MethodDelete, "/api/dnc/%2B97695112233", e.adminTok, nil), http.StatusNotFound, "not_found")
			requireErr(t, e.do(http.MethodDelete, "/api/dnc/abc", e.adminTok, nil), http.StatusBadRequest, "invalid")
			requireErr(t, e.do(http.MethodDelete, "/api/dnc/%2B97699112233", e.opTok, nil), http.StatusForbidden, "forbidden")
			assert.Equal(t, 0, decode[list[domain.DoNotCallEntry]](t, e.do(http.MethodGet, "/api/dnc", e.opTok, nil)).Total)
		})
	}

	e := newEnv(t, func(d *Deps, _ *Config) { d.DNC = nil })
	requireErr(t, e.do(http.MethodGet, "/api/dnc", e.opTok, nil), http.StatusInternalServerError, "internal")
}

func TestDialRefusesDNC(t *testing.T) {
	e := newEnv(t)
	p := e.seedProfile(e.org.ID, nil)
	num := e.seedNumber(e.org.ID, "+97677001234", &p.ID)
	require.NoError(t, e.dnc.AddDoNotCall(context.Background(), &domain.DoNotCallEntry{OrgID: e.org.ID, Phone: "+97699112233"}))

	w := e.do(http.MethodPost, "/api/calls/dial", e.opTok, map[string]any{"toNumber": "99112233", "sipNumberId": num.ID.String()})
	requireErr(t, w, http.StatusConflict, "conflict")
	assert.Contains(t, w.Body.String(), "do-not-call")
	w = e.do(http.MethodPost, "/api/calls/dial", e.opTok, map[string]any{"toNumber": "+97699112233", "sipNumberId": num.ID.String()})
	requireErr(t, w, http.StatusConflict, "conflict")

	w = e.do(http.MethodPost, "/api/calls/dial", e.opTok, map[string]any{"toNumber": "+97688112233", "sipNumberId": num.ID.String()})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	e.srv.bg.Wait()
}

func TestCallToDNC(t *testing.T) {
	e := newEnv(t)
	in := e.seedCall(e.org.ID, domain.StatusCompleted) // inbound from +97699110000
	out := e.seedCall(e.org.ID, domain.StatusCompleted, func(c *domain.Call) {
		c.Direction, c.FromNumber, c.ToNumber = domain.DirectionOutbound, "+97677001234", "+97688110000"
	})

	w := e.do(http.MethodPost, "/api/calls/"+in.ID.String()+"/dnc", e.opTok, map[string]any{"reason": "rude"})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	entry := decode[dncEntryBody](t, w).Entry
	assert.Equal(t, "+97699110000", entry.Phone)
	assert.Equal(t, "rude", entry.Reason)

	w = e.do(http.MethodPost, "/api/calls/"+out.ID.String()+"/dnc", e.opTok, nil)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	assert.Equal(t, "+97688110000", decode[dncEntryBody](t, w).Entry.Phone)

	listed, _ := e.dnc.IsDoNotCall(context.Background(), e.org.ID, "+97688110000")
	assert.True(t, listed)
	requireErr(t, e.do(http.MethodPost, "/api/calls/"+out.ID.String()+"/dnc", e.othTok, nil), http.StatusNotFound, "not_found")
}

func TestBootstrapOutcomesAndEndedOutcome(t *testing.T) {
	e := newEnv(t)
	p := e.seedProfile(e.org.ID, nil)
	num := e.seedNumber(e.org.ID, "+97677001234", nil)
	outcomes := []domain.CampaignOutcome{{Code: "agreed", Label: "Зөвшөөрсөн", Description: "yes", Terminal: true}, {Code: "callback", Label: "Дахин", Terminal: false}}
	camp := &domain.Campaign{ID: uuid.New(), OrgID: e.org.ID, Name: "O", AgentProfileID: &p.ID, SIPNumberID: &num.ID, Status: domain.CampaignRunning,
		Outcomes: outcomes, Schedule: domain.CampaignSchedule{StartTime: "09:00", EndTime: "18:00"}}
	callID := uuid.New()
	require.NoError(t, e.db.CreateCampaign(context.Background(), camp, []domain.CampaignTarget{
		{ID: uuid.New(), CampaignID: camp.ID, Phone: "+97699000001", CallID: &callID},
	}))
	e.seedCall(e.org.ID, domain.StatusActive, func(c *domain.Call) {
		c.ID, c.RoomName, c.Direction, c.CampaignID, c.SIPNumberID = callID, "call-"+callID.String(), domain.DirectionOutbound, &camp.ID, &num.ID
	})

	w := e.agent(http.MethodGet, "/internal/agent/bootstrap?callId="+callID.String(), nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	b := decode[bootstrapBody](t, w)
	require.NotNil(t, b.Campaign)
	assert.Equal(t, outcomes, b.Campaign.Outcomes)
	assert.NotContains(t, w.Body.String(), `"schedule"`, "schedule is engine-side only")

	body := map[string]any{"events": []map[string]any{{"type": "call.ended", "callId": callID.String(), "payload": map[string]any{
		"endReason": "hangup_agent", "outcome": " Agreed ", "outcomeNote": "  Худалдан\n авна  ",
	}}}}
	require.Equal(t, http.StatusOK, e.agent(http.MethodPost, "/internal/agent/events", body).Code)
	got := e.call(callID)
	assert.Equal(t, "agreed", got.Outcome)
	assert.Equal(t, "Худалдан авна", got.OutcomeNote)
	require.Len(t, e.ctl.endedCalls, 1)
	assert.Equal(t, "agreed", e.ctl.endedCalls[0].Outcome, "engine sees the outcome")
	ended := e.bus.ofType(domain.EventCallEnded)
	require.Len(t, ended, 1)
	pl := ended[0].Payload.(map[string]any)
	assert.Equal(t, "agreed", pl["outcome"])
	assert.Equal(t, "Худалдан авна", pl["outcomeNote"])

	// A later call.ended without outcome keeps it.
	body = map[string]any{"events": []map[string]any{{"type": "call.ended", "callId": callID.String(), "payload": map[string]any{"summary": "s"}}}}
	require.Equal(t, http.StatusOK, e.agent(http.MethodPost, "/internal/agent/events", body).Code)
	assert.Equal(t, "agreed", e.call(callID).Outcome)
}
