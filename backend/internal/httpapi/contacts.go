package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func (s *server) loadContact(ctx context.Context, orgID, id uuid.UUID) (*domain.Contact, error) {
	c, err := s.d.Contact.GetContact(ctx, id)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && (c == nil || c.OrgID != orgID)) {
		return nil, errNotFound("contact")
	}
	if err != nil {
		return nil, fmt.Errorf("get contact: %w", err)
	}
	return c, nil
}

func (s *server) listContacts(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 50, 1, 500)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	offset, err := queryInt(r, "offset", 0, 0, 1<<30)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	items, total, err := s.d.Contact.ListContacts(r.Context(), claimsOf(r).OrgID, strings.TrimSpace(r.URL.Query().Get("q")), limit, offset)
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("list contacts: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, newList(items, total))
}

func (s *server) upsertContact(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Phone string            `json:"phone"`
		Name  string            `json:"name"`
		Tags  []string          `json:"tags"`
		Meta  map[string]string `json:"meta"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	phone, err := requirePhone(req.Phone, "phone")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	tags := make([]string, 0, len(req.Tags))
	for _, t := range req.Tags {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	if req.Meta == nil {
		req.Meta = map[string]string{}
	}
	now := s.now()
	c := &domain.Contact{
		ID: uuid.New(), OrgID: claimsOf(r).OrgID, Phone: phone, Name: strings.TrimSpace(req.Name),
		Tags: tags, Meta: req.Meta, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.d.Contact.UpsertContact(r.Context(), c); err != nil {
		s.writeErr(w, r, fmt.Errorf("upsert contact: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"contact": c})
}

func (s *server) getContact(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	orgID := claimsOf(r).OrgID
	c, err := s.loadContact(ctx, orgID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	// The call filter has no contact field: search by phone, then keep the
	// calls linked to this contact (or to its number).
	found, _, err := s.d.Call.ListCalls(ctx, domain.CallFilter{OrgID: orgID, Search: c.Phone, Limit: 100})
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("list contact calls: %w", err))
		return
	}
	calls := make([]domain.Call, 0, 20)
	for _, call := range found {
		linked := call.ContactID != nil && *call.ContactID == c.ID
		byPhone := call.ContactID == nil && (call.FromNumber == c.Phone || call.ToNumber == c.Phone)
		if linked || byPhone {
			calls = append(calls, call)
		}
		if len(calls) == 20 {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"contact": c, "calls": calls})
}

func (s *server) deleteContact(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	if _, err := s.loadContact(ctx, claimsOf(r).OrgID, id); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.d.Contact.DeleteContact(ctx, id); err != nil {
		s.writeErr(w, r, fmt.Errorf("delete contact: %w", err))
		return
	}
	noContent(w)
}

// multipartFile parses the multipart form and opens the "file" part.
func multipartFile(w http.ResponseWriter, r *http.Request, required bool) (multipart.File, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxMultipartBody)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		return nil, errInvalid("expected multipart/form-data: %v", err)
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		if required || !errors.Is(err, http.ErrMissingFile) {
			return nil, errInvalid("file is required")
		}
		return nil, nil
	}
	return f, nil
}

type importResult struct {
	Imported int        `json:"imported"`
	Skipped  int        `json:"skipped"`
	Errors   []RowError `json:"errors"`
}

func (s *server) importContacts(w http.ResponseWriter, r *http.Request) {
	if s.d.ContactParser == nil {
		s.writeErr(w, r, errNotConfigured("contact CSV parser"))
		return
	}
	f, err := multipartFile(w, r, true)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	defer f.Close()
	parsed, err := s.d.ContactParser.ParseContacts(io.Reader(f))
	if err != nil {
		s.writeErr(w, r, errInvalid("could not parse CSV: %v", err))
		return
	}
	ctx := r.Context()
	orgID := claimsOf(r).OrgID
	res := importResult{Skipped: parsed.Skipped, Errors: append([]RowError{}, parsed.Errors...)}
	now := s.now()
	for i := range parsed.Contacts {
		c := parsed.Contacts[i]
		phone, ok := normalizePhone(c.Phone)
		if !ok {
			res.Skipped++
			res.Errors = append(res.Errors, RowError{Row: i + 1, Message: fmt.Sprintf("invalid phone %q", c.Phone)})
			continue
		}
		if c.ID == uuid.Nil {
			c.ID = uuid.New()
		}
		c.OrgID, c.Phone, c.CreatedAt, c.UpdatedAt = orgID, phone, now, now
		if c.Tags == nil {
			c.Tags = []string{}
		}
		if c.Meta == nil {
			c.Meta = map[string]string{}
		}
		if err := s.d.Contact.UpsertContact(ctx, &c); err != nil {
			s.log.Warn().Err(err).Str("phone", phone).Msg("import contact")
			res.Skipped++
			res.Errors = append(res.Errors, RowError{Row: i + 1, Message: "could not save contact"})
			continue
		}
		res.Imported++
	}
	writeJSON(w, http.StatusOK, res)
}
