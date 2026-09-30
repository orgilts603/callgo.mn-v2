package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/phone"
)

const maxDNCReason = 500

// dncNumber normalises a number for the do-not-call list: E.164 via
// internal/phone, falling back to the generic cleanup for numbers it does
// not know (e.g. short internal extensions).
func dncNumber(raw, field string) (string, error) {
	if e164, err := phone.Normalize(raw); err == nil {
		return e164, nil
	}
	if p, ok := normalizePhone(raw); ok {
		return p, nil
	}
	return "", errInvalid("%s must be a phone number", field)
}

func (s *server) requireDNC(w http.ResponseWriter, r *http.Request) bool {
	if s.d.DNC == nil {
		s.writeErr(w, r, errNotConfigured("do-not-call list"))
		return false
	}
	return true
}

// addDNC stores an entry and reports whether it was newly created.
func (s *server) addDNC(ctx context.Context, e *domain.DoNotCallEntry) (bool, error) {
	if ins, ok := s.d.DNC.(DNCInserter); ok {
		created, err := ins.InsertDoNotCall(ctx, e)
		if err != nil {
			return false, fmt.Errorf("add do-not-call: %w", err)
		}
		return created, nil
	}
	listed, err := s.d.DNC.IsDoNotCall(ctx, e.OrgID, e.Phone)
	if err != nil {
		return false, fmt.Errorf("check do-not-call: %w", err)
	}
	if err := s.d.DNC.AddDoNotCall(ctx, e); err != nil {
		return false, fmt.Errorf("add do-not-call: %w", err)
	}
	return !listed, nil
}

func (s *server) listDNC(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNC(w, r) {
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
	items, total, err := s.d.DNC.ListDoNotCall(r.Context(), claimsOf(r).OrgID, strings.TrimSpace(r.URL.Query().Get("q")), limit, offset)
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("list do-not-call: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, newList(items, total))
}

func (s *server) createDNC(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNC(w, r) {
		return
	}
	var req struct {
		Phone  string `json:"phone"`
		Reason string `json:"reason"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	p, err := dncNumber(req.Phone, "phone")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	cl := claimsOf(r)
	e := &domain.DoNotCallEntry{OrgID: cl.OrgID, Phone: p, Reason: truncRunes(strings.TrimSpace(req.Reason), maxDNCReason),
		CreatedBy: userRef(cl.UserID), CreatedAt: s.now()}
	created, err := s.addDNC(r.Context(), e)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"entry": e})
}

func (s *server) deleteDNC(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNC(w, r) {
		return
	}
	raw, err := url.PathUnescape(chi.URLParam(r, "phone"))
	if err != nil {
		s.writeErr(w, r, errInvalid("invalid phone"))
		return
	}
	p, err := dncNumber(raw, "phone")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.d.DNC.RemoveDoNotCall(r.Context(), claimsOf(r).OrgID, p); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.writeErr(w, r, errNotFound("do-not-call entry"))
			return
		}
		s.writeErr(w, r, fmt.Errorf("remove do-not-call: %w", err))
		return
	}
	noContent(w)
}

func (s *server) importDNC(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNC(w, r) {
		return
	}
	if s.d.ContactParser == nil {
		s.writeErr(w, r, errNotConfigured("contact list parser"))
		return
	}
	f, filename, err := multipartFile(w, r, true)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	defer f.Close()
	parsed, err := s.d.ContactParser.ParseContacts(f, filename)
	if err != nil {
		s.writeErr(w, r, parseErr(err))
		return
	}
	ctx := r.Context()
	cl := claimsOf(r)
	reason := truncRunes(strings.TrimSpace(r.FormValue("reason")), maxDNCReason)
	res := importResult{Skipped: parsed.Skipped, Errors: append([]RowError{}, parsed.Errors...)}

	phones := make([]string, 0, len(parsed.Contacts))
	seen := make(map[string]bool, len(parsed.Contacts))
	for i, c := range parsed.Contacts {
		p, err := dncNumber(c.Phone, "phone")
		if err != nil {
			res.Skipped++
			res.Errors = append(res.Errors, RowError{Row: i + 1, Message: fmt.Sprintf("invalid phone %q", c.Phone)})
			continue
		}
		if seen[p] {
			res.Skipped++
			continue
		}
		seen[p] = true
		phones = append(phones, p)
	}
	listed, err := s.d.DNC.FilterDoNotCall(ctx, cl.OrgID, phones)
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("filter do-not-call: %w", err))
		return
	}
	now := s.now()
	for _, p := range phones {
		if listed[p] {
			res.Skipped++ // already on the list
			continue
		}
		e := &domain.DoNotCallEntry{OrgID: cl.OrgID, Phone: p, Reason: reason, CreatedBy: userRef(cl.UserID), CreatedAt: now}
		if err := s.d.DNC.AddDoNotCall(ctx, e); err != nil {
			s.log.Warn().Err(err).Msg("import do-not-call entry")
			res.Skipped++
			res.Errors = append(res.Errors, RowError{Message: fmt.Sprintf("could not save %s", p)})
			continue
		}
		res.Imported++
	}
	writeJSON(w, http.StatusOK, res)
}

// callToDNC puts the customer side of a call on the do-not-call list.
func (s *server) callToDNC(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNC(w, r) {
		return
	}
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := decodeOptionalJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	cl := claimsOf(r)
	c, err := s.loadCall(ctx, cl.OrgID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	customer := c.FromNumber
	if c.Direction == domain.DirectionOutbound {
		customer = c.ToNumber
	}
	p, err := dncNumber(customer, "the call's customer number")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	e := &domain.DoNotCallEntry{OrgID: cl.OrgID, Phone: p, Reason: truncRunes(strings.TrimSpace(req.Reason), maxDNCReason),
		CreatedBy: userRef(cl.UserID), CreatedAt: s.now()}
	if _, err := s.addDNC(ctx, e); err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"entry": e})
}

// checkDialable refuses numbers on the do-not-call list.
func (s *server) checkDialable(ctx context.Context, orgID uuid.UUID, number string) error {
	if s.d.DNC == nil {
		return nil
	}
	p, err := dncNumber(number, "toNumber")
	if err != nil {
		return err
	}
	listed, err := s.d.DNC.IsDoNotCall(ctx, orgID, p)
	if err != nil {
		return fmt.Errorf("check do-not-call: %w", err)
	}
	if listed {
		return errConflict("%s is on the do-not-call list", p)
	}
	return nil
}

func userRef(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}
