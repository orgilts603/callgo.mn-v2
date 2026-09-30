package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	
	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

const (
	maxJSONBody      = 1 << 20  // 1 MiB
	maxMultipartBody = 32 << 20 // 32 MiB
)

// apiError is an error with an explicit HTTP status and public message.
type apiError struct {
	status  int
	code    string
	message string
}

func (e *apiError) Error() string { return e.code + ": " + e.message }

func errInvalid(format string, args ...any) error {
	return &apiError{http.StatusBadRequest, "invalid", fmt.Sprintf(format, args...)}
}

func errNotFound(what string) error {
	return &apiError{http.StatusNotFound, "not_found", what + " not found"}
}

func errConflict(format string, args ...any) error {
	return &apiError{http.StatusConflict, "conflict", fmt.Sprintf(format, args...)}
}

func errNotConfigured(what string) error {
	return &apiError{http.StatusInternalServerError, "internal", what + " not configured"}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func noContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

// writeErr maps err onto the error envelope. Unknown errors are logged and
// reported as a generic 500.
func (s *server) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apiError
	switch {
	case errors.As(err, &ae):
		auth.WriteError(w, ae.status, ae.code, ae.message)
	case errors.Is(err, domain.ErrNotFound):
		auth.WriteError(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, domain.ErrConflict):
		auth.WriteError(w, http.StatusConflict, "conflict", "conflict")
	case errors.Is(err, domain.ErrInvalid):
		auth.WriteError(w, http.StatusBadRequest, "invalid", err.Error())
	case errors.Is(err, domain.ErrUnauthorized):
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
	case errors.Is(err, domain.ErrForbidden):
		auth.WriteError(w, http.StatusForbidden, "forbidden", "forbidden")
	default:
		s.log.Error().Err(err).Str("method", r.Method).Str("path", r.URL.Path).
			Str("reqId", middleware.GetReqID(r.Context())).Msg("request failed")
		auth.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return errInvalid("request body is empty")
		}
		return errInvalid("malformed JSON: %v", err)
	}
	return nil
}

// decodeOptionalJSON is decodeJSON that tolerates an empty body.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		return errInvalid("malformed JSON: %v", err)
	}
	return nil
}

func claimsOf(r *http.Request) auth.Claims {
	c, _ := auth.FromContext(r.Context())
	return c
}

func urlID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, errInvalid("invalid %s", name)
	}
	return id, nil
}

func parseOptUUID(s, field string) (*uuid.UUID, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return nil, errInvalid("invalid %s", field)
	}
	return &id, nil
}

func queryInt(r *http.Request, name string, def, lo, hi int) (int, error) {
	v := strings.TrimSpace(r.URL.Query().Get(name))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return 0, errInvalid("%s must be an integer between %d and %d", name, lo, hi)
	}
	return n, nil
}

type list[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
}

func newList[T any](items []T, total int) list[T] {
	if items == nil {
		items = []T{}
	}
	return list[T]{Items: items, Total: total}
}

var (
	phoneStrip = strings.NewReplacer(" ", "", "-", "", "(", "", ")", "", ".", "")
	phoneRe    = regexp.MustCompile(`^\+?[0-9]{3,20}$`)
)

// normalizePhone strips formatting characters and validates the number.
// Numbers are kept as given (E.164 with "+" preferred, local extensions allowed).
func normalizePhone(p string) (string, bool) {
	p = phoneStrip.Replace(strings.TrimSpace(p))
	if strings.HasPrefix(p, "00") {
		p = "+" + p[2:]
	}
	return p, phoneRe.MatchString(p)
}

func requirePhone(p, field string) (string, error) {
	n, ok := normalizePhone(p)
	if !ok {
		return "", errInvalid("%s must be a phone number (E.164)", field)
	}
	return n, nil
}
