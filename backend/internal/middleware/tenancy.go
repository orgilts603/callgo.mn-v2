package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// OrgStatusLookup returns the tenancy status of an organisation.
type OrgStatusLookup func(ctx context.Context, orgID uuid.UUID) (domain.OrgStatus, error)

// suspendedAllowedPrefixes stay fully usable while an org is suspended, so
// its users can sign in, manage the account and pay.
var suspendedAllowedPrefixes = []string{"/api/auth", "/api/billing", "/api/org"}

// OrgGate enforces the org's tenancy status. It must run after
// auth.RequireAuth (requests without claims pass through untouched):
//   - active: everything allowed;
//   - suspended (unpaid): only GET/HEAD/OPTIONS requests and paths under
//     /api/auth, /api/billing and /api/org; anything else gets
//     402 {"error":{"code":"payment_required"}};
//   - closed: 403 forbidden.
func OrgGate(lookup OrgStatusLookup) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, ok := auth.FromContext(r.Context())
			if !ok || lookup == nil {
				next.ServeHTTP(w, r)
				return
			}
			status, err := lookup(r.Context(), c.OrgID)
			switch {
			case errors.Is(err, domain.ErrNotFound):
				writeError(w, http.StatusForbidden, "forbidden", "organisation not found")
				return
			case err != nil:
				writeError(w, http.StatusInternalServerError, "internal", "could not load organisation status")
				return
			}
			switch status {
			case domain.OrgClosed:
				writeError(w, http.StatusForbidden, "forbidden", "organisation is closed")
				return
			case domain.OrgSuspended:
				if !suspendedAllowed(r) {
					writeError(w, http.StatusPaymentRequired, "payment_required",
						"organisation is suspended for non-payment; pay the open invoice to continue")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func suspendedAllowed(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	p := r.URL.Path
	for _, prefix := range suspendedAllowedPrefixes {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

// CachedOrgStatus wraps lookup with a small in-process TTL cache so OrgGate
// does not hit the database on every request. Errors are not cached. Status
// changes (suspend / reactivate) take effect within ttl.
func CachedOrgStatus(lookup OrgStatusLookup, ttl time.Duration) OrgStatusLookup {
	type entry struct {
		status domain.OrgStatus
		at     time.Time
	}
	var (
		mu    sync.Mutex
		cache = map[uuid.UUID]entry{}
	)
	return func(ctx context.Context, orgID uuid.UUID) (domain.OrgStatus, error) {
		now := time.Now()
		mu.Lock()
		e, ok := cache[orgID]
		mu.Unlock()
		if ok && now.Sub(e.at) < ttl {
			return e.status, nil
		}
		st, err := lookup(ctx, orgID)
		if err != nil {
			return "", err
		}
		mu.Lock()
		if len(cache) > 10000 {
			clear(cache)
		}
		cache[orgID] = entry{st, now}
		mu.Unlock()
		return st, nil
	}
}
