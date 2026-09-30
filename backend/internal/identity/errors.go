package identity

import (
	"errors"
	"fmt"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// ErrQuota is returned (wrapped in *QuotaError) when a plan limit is reached.
// The HTTP layer maps it to 429 "quota_exceeded".
var ErrQuota = errors.New("quota exceeded")

// QuotaError details a reached plan limit.
type QuotaError struct {
	Resource string // "users"
	Limit    int
	Used     int
}

func (e *QuotaError) Error() string {
	return fmt.Sprintf("plan limit reached: %d of %d %s", e.Used, e.Limit, e.Resource)
}

// Unwrap makes errors.Is(err, ErrQuota) true.
func (e *QuotaError) Unwrap() error { return ErrQuota }

// Error is a service error whose message is safe to show to API clients.
// Kind is one of the domain sentinel errors (ErrInvalid, ErrNotFound,
// ErrConflict, ErrUnauthorized, ErrForbidden).
type Error struct {
	Kind error
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// Unwrap returns the domain sentinel.
func (e *Error) Unwrap() error { return e.Kind }

// PublicMessage returns the client-safe message of a service error.
func PublicMessage(err error) (string, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Msg, true
	}
	var q *QuotaError
	if errors.As(err, &q) {
		return q.Error(), true
	}
	return "", false
}

func errInvalid(format string, args ...any) error {
	return &Error{Kind: domain.ErrInvalid, Msg: fmt.Sprintf(format, args...)}
}

func errConflict(msg string) error  { return &Error{Kind: domain.ErrConflict, Msg: msg} }
func errForbidden(msg string) error { return &Error{Kind: domain.ErrForbidden, Msg: msg} }
func errNotFound(what string) error {
	return &Error{Kind: domain.ErrNotFound, Msg: what + " not found"}
}
func errUnauthorized(msg string) error { return &Error{Kind: domain.ErrUnauthorized, Msg: msg} }
