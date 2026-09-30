package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func (s *server) loadCampaign(ctx context.Context, orgID, id uuid.UUID) (*domain.Campaign, error) {
	c, err := s.d.Campaign.GetCampaign(ctx, id)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && (c == nil || c.OrgID != orgID)) {
		return nil, errNotFound("campaign")
	}
	if err != nil {
		return nil, fmt.Errorf("get campaign: %w", err)
	}
	return c, nil
}

func (s *server) listCampaigns(w http.ResponseWriter, r *http.Request) {
	items, err := s.d.Campaign.ListCampaigns(r.Context(), claimsOf(r).OrgID)
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("list campaigns: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, newList(items, len(items)))
}

func formInt(r *http.Request, name string, def, lo, hi int) (int, error) {
	v := strings.TrimSpace(r.FormValue(name))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return 0, errInvalid("%s must be between %d and %d", name, lo, hi)
	}
	return n, nil
}

func (s *server) createCampaign(w http.ResponseWriter, r *http.Request) {
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
	ctx := r.Context()
	orgID := claimsOf(r).OrgID

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.writeErr(w, r, errInvalid("name is required"))
		return
	}
	concurrency, err := formInt(r, "concurrency", 2, 1, 50)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	maxAttempts, err := formInt(r, "maxAttempts", 2, 1, 10)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	dryRunLimit, err := formInt(r, "dryRunLimit", 0, 0, maxDryRunLimit)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	schedule, err := scheduleFromForm(r.FormValue("schedule"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	outcomes, err := outcomesFromForm(r.FormValue("outcomes"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	numID, err := parseOptUUID(r.FormValue("sipNumberId"), "sipNumberId")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if numID == nil {
		s.writeErr(w, r, errInvalid("sipNumberId is required"))
		return
	}
	num, err := s.loadSIPNumber(ctx, orgID, *numID)
	if err != nil {
		s.writeErr(w, r, asInvalidRef(err, "sipNumberId"))
		return
	}
	if !num.AllowOutbound {
		s.writeErr(w, r, errInvalid("SIP number %s does not allow outbound calls", num.Number))
		return
	}
	profID, err := parseOptUUID(r.FormValue("agentProfileId"), "agentProfileId")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if profID == nil {
		profID = num.AgentProfileID
	}
	if profID == nil {
		s.writeErr(w, r, errInvalid("agentProfileId is required"))
		return
	}
	if _, err := s.loadProfile(ctx, orgID, *profID); err != nil {
		s.writeErr(w, r, asInvalidRef(err, "agentProfileId"))
		return
	}

	parsed, err := s.d.TargetParser.ParseTargets(f, filename)
	if err != nil {
		s.writeErr(w, r, parseErr(err))
		return
	}
	res := importResult{Skipped: parsed.Skipped, Errors: append([]RowError{}, parsed.Errors...)}
	now := s.now()
	camp := &domain.Campaign{
		ID: uuid.New(), OrgID: orgID, Name: name, SIPNumberID: &num.ID, AgentProfileID: profID,
		Script: strings.TrimSpace(r.FormValue("script")), Status: domain.CampaignDraft,
		Concurrency: concurrency, MaxAttempts: maxAttempts, Schedule: schedule, Outcomes: outcomes,
		DryRunLimit: dryRunLimit, CreatedAt: now, UpdatedAt: now,
	}
	targets := make([]domain.CampaignTarget, 0, len(parsed.Targets))
	seen := make(map[string]bool, len(parsed.Targets))
	for i, t := range parsed.Targets {
		phone, ok := normalizePhone(t.Phone)
		if !ok {
			res.Skipped++
			res.Errors = append(res.Errors, RowError{Row: i + 1, Message: fmt.Sprintf("invalid phone %q", t.Phone)})
			continue
		}
		if seen[phone] {
			res.Skipped++
			continue
		}
		seen[phone] = true
		if t.ID == uuid.Nil {
			t.ID = uuid.New()
		}
		t.CampaignID, t.Phone, t.Status, t.Attempts, t.UpdatedAt = camp.ID, phone, domain.TargetPending, 0, now
		t.CallID, t.NextTryAt, t.LastError = nil, nil, ""
		if t.ContactID == nil {
			if ct, err := s.d.Contact.GetContactByPhone(ctx, orgID, phone); err == nil && ct != nil {
				t.ContactID = &ct.ID
			}
		}
		targets = append(targets, t)
	}
	if len(targets) == 0 {
		s.writeErr(w, r, errInvalid("the file contains no valid targets (skipped %d)", res.Skipped))
		return
	}
	dnc, err := s.markDoNotCall(ctx, orgID, targets)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if dnc == len(targets) {
		s.writeErr(w, r, errInvalid("all %d numbers are on the do-not-call list", dnc))
		return
	}
	camp.Total, camp.Skipped = len(targets), dnc
	res.Imported = len(targets) - dnc
	res.Skipped += dnc
	res.DoNotCall = dnc
	if err := s.d.Campaign.CreateCampaign(ctx, camp, targets); err != nil {
		s.writeErr(w, r, fmt.Errorf("create campaign: %w", err))
		return
	}
	s.publish(ctx, orgID, nil, domain.EventCampaignProgress, map[string]any{"campaign": camp, "target": nil})
	writeJSON(w, http.StatusCreated, map[string]any{"campaign": camp, "targets": res})
}

func (s *server) getCampaign(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	limit, err := queryInt(r, "limit", 50, 1, 1000)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	offset, err := queryInt(r, "offset", 0, 0, 1<<30)
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
	targets, total, err := s.d.Campaign.ListTargets(ctx, c.ID, limit, offset)
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("list targets: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"campaign": c, "targets": newList(targets, total)})
}

func (s *server) startCampaign(w http.ResponseWriter, r *http.Request) {
	s.controlCampaign(w, r, true)
}

func (s *server) pauseCampaign(w http.ResponseWriter, r *http.Request) {
	s.controlCampaign(w, r, false)
}

func (s *server) controlCampaign(w http.ResponseWriter, r *http.Request, start bool) {
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	// Optional start body {dryRunLimit: N}; absent = the stored limit.
	var body struct {
		DryRunLimit *int `json:"dryRunLimit"`
	}
	if start {
		if err := decodeOptionalJSON(w, r, &body); err != nil {
			s.writeErr(w, r, err)
			return
		}
		if body.DryRunLimit != nil && (*body.DryRunLimit < 0 || *body.DryRunLimit > maxDryRunLimit) {
			s.writeErr(w, r, errInvalid("dryRunLimit must be between 0 and %d", maxDryRunLimit))
			return
		}
	}
	if s.d.Campaigns == nil {
		s.writeErr(w, r, errNotConfigured("campaign engine"))
		return
	}
	ctx := r.Context()
	orgID := claimsOf(r).OrgID
	c, err := s.loadCampaign(ctx, orgID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if start {
		if c.Status == domain.CampaignCompleted {
			s.writeErr(w, r, errConflict("campaign is already completed"))
			return
		}
		limit := c.DryRunLimit
		if body.DryRunLimit != nil {
			limit = *body.DryRunLimit
		}
		err = s.d.Campaigns.Start(ctx, c.ID, limit)
	} else {
		if c.Status != domain.CampaignRunning {
			s.writeErr(w, r, errConflict("campaign is not running"))
			return
		}
		err = s.d.Campaigns.Pause(ctx, c.ID)
	}
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("control campaign: %w", err))
		return
	}
	if c, err = s.loadCampaign(ctx, orgID, id); err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"campaign": c})
}

func (s *server) deleteCampaign(w http.ResponseWriter, r *http.Request) {
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
	switch c.Status {
	case domain.CampaignDraft, domain.CampaignCompleted, domain.CampaignPaused:
	default:
		s.writeErr(w, r, errConflict("pause the campaign before deleting it"))
		return
	}
	del := s.d.CampaignDeleter
	if del == nil {
		del, _ = s.d.Campaign.(CampaignDeleter)
	}
	if del == nil {
		s.writeErr(w, r, errNotConfigured("campaign deletion"))
		return
	}
	if err := del.DeleteCampaign(ctx, c.ID); err != nil {
		s.writeErr(w, r, fmt.Errorf("delete campaign: %w", err))
		return
	}
	noContent(w)
}
