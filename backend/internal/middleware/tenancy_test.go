package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func TestTenancyOrgGate(t *testing.T) {
	active, suspended, closed, missing, broken := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	lookup := func(_ context.Context, id uuid.UUID) (domain.OrgStatus, error) {
		switch id {
		case active:
			return domain.OrgActive, nil
		case suspended:
			return domain.OrgSuspended, nil
		case closed:
			return domain.OrgClosed, nil
		case missing:
			return "", domain.ErrNotFound
		case broken:
			return "", errors.New("db down")
		}
		return "", nil
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := OrgGate(lookup)(ok)

	cases := []struct {
		org    *uuid.UUID
		method string
		path   string
		want   int
		code   string
	}{
		{nil, http.MethodPost, "/api/calls/dial", http.StatusNoContent, ""},
		{&active, http.MethodPost, "/api/calls/dial", http.StatusNoContent, ""},
		{&suspended, http.MethodGet, "/api/calls", http.StatusNoContent, ""},
		{&suspended, http.MethodPost, "/api/calls/dial", http.StatusPaymentRequired, "payment_required"},
		{&suspended, http.MethodDelete, "/api/contacts/1", http.StatusPaymentRequired, "payment_required"},
		{&suspended, http.MethodPost, "/api/organizations", http.StatusPaymentRequired, "payment_required"},
		{&suspended, http.MethodPost, "/api/billing/invoices/1/pay", http.StatusNoContent, ""},
		{&suspended, http.MethodPost, "/api/auth/logout", http.StatusNoContent, ""},
		{&suspended, http.MethodPut, "/api/org", http.StatusNoContent, ""},
		{&suspended, http.MethodPost, "/api/org/invitations", http.StatusNoContent, ""},
		{&closed, http.MethodGet, "/api/calls", http.StatusForbidden, "forbidden"},
		{&missing, http.MethodGet, "/api/calls", http.StatusForbidden, "forbidden"},
		{&broken, http.MethodGet, "/api/calls", http.StatusInternalServerError, "internal"},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		if tc.org != nil {
			r = r.WithContext(auth.WithClaims(r.Context(), auth.Claims{UserID: uuid.New(), OrgID: *tc.org, Role: domain.RoleOwner}))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		assert.Equal(t, tc.want, w.Code, "%s %s", tc.method, tc.path)
		if tc.code != "" {
			assert.Contains(t, w.Body.String(), `"code":"`+tc.code+`"`)
		}
	}
}

func TestTenancyCachedOrgStatus(t *testing.T) {
	calls := 0
	status := domain.OrgActive
	fail := false
	lookup := CachedOrgStatus(func(context.Context, uuid.UUID) (domain.OrgStatus, error) {
		calls++
		if fail {
			return "", errors.New("boom")
		}
		return status, nil
	}, 50*time.Millisecond)
	id := uuid.New()
	for range 3 {
		st, err := lookup(context.Background(), id)
		assert.NoError(t, err)
		assert.Equal(t, domain.OrgActive, st)
	}
	assert.Equal(t, 1, calls)
	status = domain.OrgSuspended
	time.Sleep(60 * time.Millisecond)
	st, _ := lookup(context.Background(), id)
	assert.Equal(t, domain.OrgSuspended, st)
	fail = true
	_, err := lookup(context.Background(), uuid.New())
	assert.Error(t, err)
}
