package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/xlsxexport"
)

const (
	maxDryRunLimit     = 100000
	maxOutcomes        = 20
	maxOutcomeLabel    = 100
	maxOutcomeDescr    = 1000
	maxPacePerMinute   = 1000
	previewRows        = 10
	defaultScheduleTZ  = "Asia/Ulaanbaatar"
	exportCallPageSize = 500
	exportMaxCallPages = 1000
	exportMaxLookups   = 500
)

var (
	outcomeCodeRe = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	clockRe       = regexp.MustCompile(`^([01]?[0-9]|2[0-3]):([0-5][0-9])$`)
)

// ----------------------------------------------------------------------------
// Validation of schedule / outcomes
// ----------------------------------------------------------------------------

// validateSchedule normalises a calling window: "H:MM" becomes "HH:MM",
// weekdays are de-duplicated and sorted, and a non-empty window without a
// time zone gets Asia/Ulaanbaatar. Overnight windows (end < start) are
// allowed; start == end is an empty window and rejected.
func validateSchedule(sc domain.CampaignSchedule) (domain.CampaignSchedule, error) {
	sc.Timezone = strings.TrimSpace(sc.Timezone)
	var err error
	if sc.StartTime, err = normClock(sc.StartTime, "schedule.startTime"); err != nil {
		return sc, err
	}
	if sc.EndTime, err = normClock(sc.EndTime, "schedule.endTime"); err != nil {
		return sc, err
	}
	if sc.StartTime != "" && sc.StartTime == sc.EndTime {
		return sc, errInvalid("schedule.startTime and schedule.endTime must differ")
	}
	days := make([]time.Weekday, 0, len(sc.Weekdays))
	for _, d := range sc.Weekdays {
		if d < time.Sunday || d > time.Saturday {
			return sc, errInvalid("schedule.weekdays must be 0 (Sunday) to 6 (Saturday)")
		}
		if !slices.Contains(days, d) {
			days = append(days, d)
		}
	}
	slices.Sort(days)
	sc.Weekdays = days
	if sc.PacePerMinute < 0 || sc.PacePerMinute > maxPacePerMinute {
		return sc, errInvalid("schedule.pacePerMinute must be between 0 and %d", maxPacePerMinute)
	}
	if sc.Timezone != "" {
		if _, err := time.LoadLocation(sc.Timezone); err != nil {
			return sc, errInvalid("schedule.timezone %q is not a valid IANA time zone", sc.Timezone)
		}
	} else if !sc.IsZero() {
		sc.Timezone = defaultScheduleTZ
	}
	return sc, nil
}

func normClock(v, field string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	m := clockRe.FindStringSubmatch(v)
	if m == nil {
		return "", errInvalid("%s must be HH:MM", field)
	}
	h, _ := strconv.Atoi(m[1])
	return fmt.Sprintf("%02d:%s", h, m[2]), nil
}

// validateOutcomes checks codes (non-empty, unique, snake_case) and labels
// (non-empty). The result is never nil.
func validateOutcomes(in []domain.CampaignOutcome) ([]domain.CampaignOutcome, error) {
	if len(in) > maxOutcomes {
		return nil, errInvalid("at most %d outcomes are allowed", maxOutcomes)
	}
	out := make([]domain.CampaignOutcome, 0, len(in))
	seen := make(map[string]bool, len(in))
	for i, o := range in {
		o.Code = strings.TrimSpace(o.Code)
		o.Label = strings.TrimSpace(o.Label)
		o.Description = strings.TrimSpace(o.Description)
		switch {
		case o.Code == "":
			return nil, errInvalid("outcomes[%d].code is required", i)
		case len(o.Code) > maxOutcomeCodeLen || !outcomeCodeRe.MatchString(o.Code):
			return nil, errInvalid("outcomes[%d].code %q must be snake_case (a-z, 0-9 and _)", i, o.Code)
		case seen[o.Code]:
			return nil, errInvalid("outcome code %q is used twice", o.Code)
		case o.Label == "":
			return nil, errInvalid("outcomes[%d].label is required", i)
		case len([]rune(o.Label)) > maxOutcomeLabel:
			return nil, errInvalid("outcomes[%d].label is longer than %d characters", i, maxOutcomeLabel)
		case len([]rune(o.Description)) > maxOutcomeDescr:
			return nil, errInvalid("outcomes[%d].description is longer than %d characters", i, maxOutcomeDescr)
		}
		seen[o.Code] = true
		out = append(out, o)
	}
	return out, nil
}

// scheduleFromForm parses the optional multipart "schedule" JSON field.
func scheduleFromForm(v string) (domain.CampaignSchedule, error) {
	var sc domain.CampaignSchedule
	if v = strings.TrimSpace(v); v == "" || v == "null" {
		return sc, nil
	}
	if err := json.Unmarshal([]byte(v), &sc); err != nil {
		return sc, errInvalid("schedule must be a JSON object: %v", err)
	}
	return validateSchedule(sc)
}

// outcomesFromForm parses the optional multipart "outcomes" JSON field;
// absent → no outcomes.
func outcomesFromForm(v string) ([]domain.CampaignOutcome, error) {
	var list []domain.CampaignOutcome
	if v = strings.TrimSpace(v); v != "" && v != "null" {
		if err := json.Unmarshal([]byte(v), &list); err != nil {
			return nil, errInvalid("outcomes must be a JSON array: %v", err)
		}
	}
	return validateOutcomes(list)
}

// markDoNotCall flags targets whose number is on the org's do-not-call list
// as skipped and returns how many were flagged.
func (s *server) markDoNotCall(ctx context.Context, orgID uuid.UUID, targets []domain.CampaignTarget) (int, error) {
	if s.d.DNC == nil || len(targets) == 0 {
		return 0, nil
	}
	phones := make([]string, len(targets))
	for i, t := range targets {
		phones[i] = t.Phone
	}
	listed, err := s.d.DNC.FilterDoNotCall(ctx, orgID, phones)
	if err != nil {
		return 0, fmt.Errorf("filter do-not-call: %w", err)
	}
	n := 0
	for i := range targets {
		if listed[targets[i].Phone] {
			targets[i].Status, targets[i].LastError = domain.TargetSkipped, dncReason
			n++
		}
	}
	return n, nil
}

// dncReason is the target LastError of numbers skipped for the DNC list.
const dncReason = "do_not_call"

// ----------------------------------------------------------------------------
// POST /api/campaigns/preview
// ----------------------------------------------------------------------------

func (s *server) previewCampaign(w http.ResponseWriter, r *http.Request) {
	if s.d.TargetParser == nil {
		s.writeErr(w, r, errNotConfigured("campaign list parser"))
		return
	}
	f, filename, err := multipartFile(w, r, true)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	defer f.Close()
	p, err := s.d.TargetParser.Preview(f, filename, previewRows)
	if err != nil {
		s.writeErr(w, r, parseErr(err))
		return
	}
	if p.Columns == nil {
		p.Columns = []string{}
	}
	if p.Rows == nil {
		p.Rows = [][]string{}
	}
	if p.Mapping == nil {
		p.Mapping = map[string]string{}
	}
	writeJSON(w, http.StatusOK, p)
}

// ----------------------------------------------------------------------------
// PUT /api/campaigns/{id}
// ----------------------------------------------------------------------------

type campaignUpdate struct {
	Name           *string                   `json:"name"`
	Script         *string                   `json:"script"`
	SIPNumberID    *string                   `json:"sipNumberId"`
	AgentProfileID *string                   `json:"agentProfileId"`
	Concurrency    *int                      `json:"concurrency"`
	MaxAttempts    *int                      `json:"maxAttempts"`
	Schedule       *domain.CampaignSchedule  `json:"schedule"`
	Outcomes       *[]domain.CampaignOutcome `json:"outcomes"`
	DryRunLimit    *int                      `json:"dryRunLimit"`
}

// validated holds the parsed, checked values of a campaignUpdate.
type validatedUpdate struct {
	name, script   *string
	sipNumberID    *uuid.UUID
	agentProfileID *uuid.UUID
	concurrency    *int
	maxAttempts    *int
	schedule       *domain.CampaignSchedule
	outcomes       []domain.CampaignOutcome
	setOutcomes    bool
	dryRunLimit    *int
}

func (s *server) validateCampaignUpdate(ctx context.Context, orgID uuid.UUID, req campaignUpdate) (validatedUpdate, error) {
	var v validatedUpdate
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if n == "" {
			return v, errInvalid("name must not be empty")
		}
		v.name = &n
	}
	if req.Script != nil {
		sc := strings.TrimSpace(*req.Script)
		v.script = &sc
	}
	if req.SIPNumberID != nil {
		id, err := parseOptUUID(*req.SIPNumberID, "sipNumberId")
		if err != nil {
			return v, err
		}
		if id == nil {
			return v, errInvalid("sipNumberId must not be empty")
		}
		num, err := s.loadSIPNumber(ctx, orgID, *id)
		if err != nil {
			return v, asInvalidRef(err, "sipNumberId")
		}
		if !num.AllowOutbound {
			return v, errInvalid("SIP number %s does not allow outbound calls", num.Number)
		}
		v.sipNumberID = id
	}
	if req.AgentProfileID != nil {
		id, err := parseOptUUID(*req.AgentProfileID, "agentProfileId")
		if err != nil {
			return v, err
		}
		if id == nil {
			return v, errInvalid("agentProfileId must not be empty")
		}
		if _, err := s.loadProfile(ctx, orgID, *id); err != nil {
			return v, asInvalidRef(err, "agentProfileId")
		}
		v.agentProfileID = id
	}
	if req.Concurrency != nil {
		if *req.Concurrency < 1 || *req.Concurrency > 50 {
			return v, errInvalid("concurrency must be between 1 and 50")
		}
		v.concurrency = req.Concurrency
	}
	if req.MaxAttempts != nil {
		if *req.MaxAttempts < 1 || *req.MaxAttempts > 10 {
			return v, errInvalid("maxAttempts must be between 1 and 10")
		}
		v.maxAttempts = req.MaxAttempts
	}
	if req.DryRunLimit != nil {
		if *req.DryRunLimit < 0 || *req.DryRunLimit > maxDryRunLimit {
			return v, errInvalid("dryRunLimit must be between 0 and %d", maxDryRunLimit)
		}
		v.dryRunLimit = req.DryRunLimit
	}
	if req.Schedule != nil {
		sc, err := validateSchedule(*req.Schedule)
		if err != nil {
			return v, err
		}
		v.schedule = &sc
	}
	if req.Outcomes != nil {
		list, err := validateOutcomes(*req.Outcomes)
		if err != nil {
			return v, err
		}
		v.outcomes, v.setOutcomes = list, true
	}
	return v, nil
}

// lockedWhileRunning names the first field of v that would change c but may
// not change while the campaign runs ("" = none).
func (v validatedUpdate) lockedWhileRunning(c *domain.Campaign) string {
	switch {
	case v.name != nil && *v.name != c.Name:
		return "name"
	case v.script != nil && *v.script != c.Script:
		return "script"
	case v.sipNumberID != nil && (c.SIPNumberID == nil || *c.SIPNumberID != *v.sipNumberID):
		return "sipNumberId"
	case v.agentProfileID != nil && (c.AgentProfileID == nil || *c.AgentProfileID != *v.agentProfileID):
		return "agentProfileId"
	case v.maxAttempts != nil && *v.maxAttempts != c.MaxAttempts:
		return "maxAttempts"
	case v.dryRunLimit != nil && *v.dryRunLimit != c.DryRunLimit:
		return "dryRunLimit"
	}
	return ""
}

func (v validatedUpdate) apply(c *domain.Campaign) {
	if v.name != nil {
		c.Name = *v.name
	}
	if v.script != nil {
		c.Script = *v.script
	}
	if v.sipNumberID != nil {
		c.SIPNumberID = v.sipNumberID
	}
	if v.agentProfileID != nil {
		c.AgentProfileID = v.agentProfileID
	}
	if v.concurrency != nil {
		c.Concurrency = *v.concurrency
	}
	if v.maxAttempts != nil {
		c.MaxAttempts = *v.maxAttempts
	}
	if v.dryRunLimit != nil {
		c.DryRunLimit = *v.dryRunLimit
	}
	if v.schedule != nil {
		c.Schedule = *v.schedule
	}
	if v.setOutcomes {
		c.Outcomes = v.outcomes
	}
}

func (s *server) updateCampaign(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	var req campaignUpdate
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	orgID := claimsOf(r).OrgID
	c, err := s.loadCampaign(ctx, orgID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	v, err := s.validateCampaignUpdate(ctx, orgID, req)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	// Re-read right before writing so engine counters stay as fresh as
	// possible (UpdateCampaign writes the whole row).
	if c, err = s.loadCampaign(ctx, orgID, id); err != nil {
		s.writeErr(w, r, err)
		return
	}
	switch c.Status {
	case domain.CampaignDraft, domain.CampaignPaused:
	case domain.CampaignRunning:
		if f := v.lockedWhileRunning(c); f != "" {
			s.writeErr(w, r, errConflict("%s cannot change while the campaign is running; pause it first", f))
			return
		}
	default:
		s.writeErr(w, r, errConflict("a %s campaign cannot be changed", c.Status))
		return
	}
	v.apply(c)
	if c.Outcomes == nil {
		c.Outcomes = []domain.CampaignOutcome{}
	}
	c.UpdatedAt = s.now()
	if err := s.d.Campaign.UpdateCampaign(ctx, c); err != nil {
		s.writeErr(w, r, fmt.Errorf("update campaign: %w", err))
		return
	}
	s.publish(ctx, orgID, nil, domain.EventCampaignProgress, map[string]any{"campaign": c, "target": nil})
	writeJSON(w, http.StatusOK, map[string]any{"campaign": c})
}

// ----------------------------------------------------------------------------
// GET /api/campaigns/{id}/stats
// ----------------------------------------------------------------------------

type outcomeCount struct {
	Code  string `json:"code"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

type campaignStatsResponse struct {
	ByStatus  map[domain.CampaignTargetStatus]int `json:"byStatus"`
	ByOutcome []outcomeCount                      `json:"byOutcome"`
}

var targetStatuses = []domain.CampaignTargetStatus{
	domain.TargetPending, domain.TargetCalling, domain.TargetDone, domain.TargetFailed, domain.TargetSkipped,
}

func (s *server) campaignStatsCounter() CampaignStats {
	if s.d.CampaignStats != nil {
		return s.d.CampaignStats
	}
	cs, _ := s.d.Campaign.(CampaignStats)
	return cs
}

func (s *server) countTargets(ctx context.Context, id uuid.UUID) (map[domain.CampaignTargetStatus]int, map[string]int, error) {
	if cs := s.campaignStatsCounter(); cs != nil {
		byStatus, err := cs.CountTargetsByStatus(ctx, id)
		if err != nil {
			return nil, nil, fmt.Errorf("count targets by status: %w", err)
		}
		byOutcome, err := cs.CountTargetsByOutcome(ctx, id)
		if err != nil {
			return nil, nil, fmt.Errorf("count targets by outcome: %w", err)
		}
		return byStatus, byOutcome, nil
	}
	targets, err := s.d.Campaign.ListAllTargets(ctx, id)
	if err != nil {
		return nil, nil, fmt.Errorf("list targets: %w", err)
	}
	byStatus := map[domain.CampaignTargetStatus]int{}
	byOutcome := map[string]int{}
	for _, t := range targets {
		byStatus[t.Status]++
		if t.Outcome != "" {
			byOutcome[t.Outcome]++
		}
	}
	return byStatus, byOutcome, nil
}

func (s *server) campaignStats(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	c, err := s.loadCampaign(ctx, claimsOf(r).OrgID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	byStatus, byOutcome, err := s.countTargets(ctx, c.ID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	resp := campaignStatsResponse{ByStatus: make(map[domain.CampaignTargetStatus]int, len(targetStatuses)), ByOutcome: []outcomeCount{}}
	for _, st := range targetStatuses {
		resp.ByStatus[st] = byStatus[st]
	}
	known := make(map[string]bool, len(c.Outcomes))
	for _, o := range c.Outcomes {
		known[o.Code] = true
		resp.ByOutcome = append(resp.ByOutcome, outcomeCount{Code: o.Code, Label: o.Label, Count: byOutcome[o.Code]})
	}
	var extra []string
	for code, n := range byOutcome {
		if code != "" && n > 0 && !known[code] {
			extra = append(extra, code)
		}
	}
	sort.Strings(extra)
	for _, code := range extra {
		resp.ByOutcome = append(resp.ByOutcome, outcomeCount{Code: code, Label: code, Count: byOutcome[code]})
	}
	writeJSON(w, http.StatusOK, resp)
}

// ----------------------------------------------------------------------------
// GET /api/campaigns/{id}/export.xlsx
// ----------------------------------------------------------------------------

func (s *server) exportCampaign(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	c, err := s.loadCampaign(ctx, claimsOf(r).OrgID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	targets, err := s.d.Campaign.ListAllTargets(ctx, c.ID)
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("list targets: %w", err))
		return
	}
	calls, err := s.targetCalls(ctx, c, targets)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	var buf bytes.Buffer
	if err := xlsxexport.WriteCampaign(&buf, *c, targets, calls); err != nil {
		s.writeErr(w, r, fmt.Errorf("export campaign: %w", err))
		return
	}
	h := w.Header()
	h.Set("Content-Type", xlsxexport.ContentType)
	h.Set("Content-Disposition", attachmentDisposition(xlsxexport.Filename(*c), c.Name+".xlsx"))
	h.Set("Content-Length", strconv.Itoa(buf.Len()))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = buf.WriteTo(w)
}

// targetCalls loads the calls referenced by targets: the campaign's calls in
// pages, then any stragglers one by one.
func (s *server) targetCalls(ctx context.Context, c *domain.Campaign, targets []domain.CampaignTarget) (map[uuid.UUID]domain.Call, error) {
	want := make(map[uuid.UUID]bool)
	for _, t := range targets {
		if t.CallID != nil {
			want[*t.CallID] = true
		}
	}
	out := make(map[uuid.UUID]domain.Call, len(want))
	if len(want) == 0 {
		return out, nil
	}
	campID := c.ID
	for page := 0; page < exportMaxCallPages && len(out) < len(want); page++ {
		calls, total, err := s.d.Call.ListCalls(ctx, domain.CallFilter{
			OrgID: c.OrgID, CampaignID: &campID, Limit: exportCallPageSize, Offset: page * exportCallPageSize,
		})
		if err != nil {
			return nil, fmt.Errorf("list campaign calls: %w", err)
		}
		for _, call := range calls {
			if want[call.ID] {
				out[call.ID] = call
			}
		}
		if len(calls) < exportCallPageSize || (page+1)*exportCallPageSize >= total {
			break
		}
	}
	lookups := 0
	for id := range want {
		if _, ok := out[id]; ok {
			continue
		}
		if lookups++; lookups > exportMaxLookups {
			break
		}
		call, err := s.d.Call.GetCall(ctx, id)
		if errors.Is(err, domain.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("get call: %w", err)
		}
		if call != nil && call.OrgID == c.OrgID {
			out[id] = *call
		}
	}
	return out, nil
}

// attachmentDisposition builds a Content-Disposition header with an ASCII
// fallback name and an RFC 5987 UTF-8 name (omitted when it cannot be made
// safe).
func attachmentDisposition(asciiName, utf8Name string) string {
	v := `attachment; filename="` + asciiName + `"`
	if name := safeFilename(utf8Name); name != "" && name != asciiName {
		v += "; filename*=UTF-8''" + rfc5987Escape(name)
	}
	return v
}

// safeFilename drops control and path characters and bounds the length.
func safeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsControl(r), strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteRune('_')
		case unicode.IsSpace(r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if out == "" || strings.Trim(out, "._ ") == "" || out == ".xlsx" {
		return ""
	}
	return truncRunes(out, 150)
}

// rfc5987Escape percent-encodes everything except RFC 5987 attr-chars.
func rfc5987Escape(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for _, c := range []byte(s) {
		if c < 0x80 && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.IndexByte("!#$&+-.^_`|~", c) >= 0) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}
