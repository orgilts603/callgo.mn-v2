package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

type authResponse struct {
	Token string               `json:"token"`
	User  *domain.User         `json:"user"`
	Org   *domain.Organization `json:"org"`
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || req.Password == "" {
		s.writeErr(w, r, errInvalid("email and password are required"))
		return
	}
	u, err := s.d.Org.GetUserByEmail(r.Context(), email)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		s.writeErr(w, r, fmt.Errorf("get user: %w", err))
		return
	}
	if u == nil || !auth.CheckPassword(u.PasswordHash, req.Password) {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "invalid email or password")
		return
	}
	s.respondAuth(w, r, u, http.StatusOK)
}

func (s *server) respondAuth(w http.ResponseWriter, r *http.Request, u *domain.User, status int) {
	org, err := s.d.Org.GetOrg(r.Context(), u.OrgID)
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("get org: %w", err))
		return
	}
	tok, err := auth.IssueToken(s.cfg.JWTSecret, auth.Claims{UserID: u.ID, OrgID: u.OrgID, Role: u.Role}, s.cfg.TokenTTL)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, status, authResponse{Token: tok, User: u, Org: org})
}

func (s *server) me(w http.ResponseWriter, r *http.Request) {
	c := claimsOf(r)
	u, err := s.d.Org.GetUser(r.Context(), c.UserID)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && u.OrgID != c.OrgID) {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "user no longer exists")
		return
	}
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("get user: %w", err))
		return
	}
	org, err := s.d.Org.GetOrg(r.Context(), u.OrgID)
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("get org: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": u, "org": org})
}

func (s *server) orgCreator() OrgCreator {
	if s.d.OrgCreator != nil {
		return s.d.OrgCreator
	}
	if oc, ok := s.d.Org.(OrgCreator); ok {
		return oc
	}
	return nil
}

func (s *server) register(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.AllowSignup {
		auth.WriteError(w, http.StatusForbidden, "forbidden", "signup is disabled")
		return
	}
	var req struct {
		OrgName  string `json:"orgName"`
		Email    string `json:"email"`
		Password string `json:"password"`
		Name     string `json:"name"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	req.OrgName = strings.TrimSpace(req.OrgName)
	req.Name = strings.TrimSpace(req.Name)
	email := strings.ToLower(strings.TrimSpace(req.Email))
	switch {
	case req.OrgName == "":
		s.writeErr(w, r, errInvalid("orgName is required"))
		return
	case req.Name == "":
		s.writeErr(w, r, errInvalid("name is required"))
		return
	case !validEmail(email):
		s.writeErr(w, r, errInvalid("email is invalid"))
		return
	case len(req.Password) < 8 || len(req.Password) > 72:
		s.writeErr(w, r, errInvalid("password must be 8-72 characters"))
		return
	}
	creator := s.orgCreator()
	if creator == nil {
		s.writeErr(w, r, errNotConfigured("organisation signup"))
		return
	}
	ctx := r.Context()
	if existing, err := s.d.Org.GetUserByEmail(ctx, email); err == nil && existing != nil {
		s.writeErr(w, r, errConflict("email already registered"))
		return
	} else if err != nil && !errors.Is(err, domain.ErrNotFound) {
		s.writeErr(w, r, fmt.Errorf("lookup user: %w", err))
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	slug, err := s.uniqueSlug(ctx, req.OrgName)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	now := s.now()
	org := &domain.Organization{ID: uuid.New(), Name: req.OrgName, Slug: slug, CreatedAt: now}
	if err := creator.CreateOrg(ctx, org); err != nil {
		s.writeErr(w, r, fmt.Errorf("create org: %w", err))
		return
	}
	u := &domain.User{ID: uuid.New(), OrgID: org.ID, Email: email, Name: req.Name, Role: domain.RoleOwner, PasswordHash: hash, CreatedAt: now}
	if err := s.d.Org.CreateUser(ctx, u); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			s.writeErr(w, r, errConflict("email already registered"))
			return
		}
		s.writeErr(w, r, fmt.Errorf("create user: %w", err))
		return
	}
	s.respondAuth(w, r, u, http.StatusCreated)
}

func validEmail(e string) bool {
	if e == "" || len(e) > 254 {
		return false
	}
	a, err := mail.ParseAddress(e)
	return err == nil && a.Address == e
}

// slugify turns a name into a URL-safe slug (ASCII letters/digits and dashes).
func slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 40 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

func (s *server) uniqueSlug(ctx context.Context, name string) (string, error) {
	base := slugify(name)
	if base == "" {
		base = "org"
	}
	slug := base
	for range 5 {
		_, err := s.d.Org.GetOrgBySlug(ctx, slug)
		if errors.Is(err, domain.ErrNotFound) {
			return slug, nil
		}
		if err != nil {
			return "", fmt.Errorf("lookup slug: %w", err)
		}
		var buf [3]byte
		_, _ = rand.Read(buf[:])
		slug = base + "-" + hex.EncodeToString(buf[:])
	}
	return "", errConflict("could not allocate organisation slug")
}
