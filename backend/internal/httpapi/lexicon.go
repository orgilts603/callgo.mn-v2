package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

type lexiconBody struct {
	Wrong    string `json:"wrong"`
	Correct  string `json:"correct"`
	Scope    string `json:"scope"`
	Phonetic string `json:"phonetic"`
}

func (b *lexiconBody) validate(defaultScope domain.LexiconScope) (domain.LexiconScope, error) {
	b.Wrong = strings.TrimSpace(b.Wrong)
	b.Correct = strings.TrimSpace(b.Correct)
	b.Phonetic = strings.TrimSpace(b.Phonetic)
	if b.Wrong == "" || b.Correct == "" {
		return "", errInvalid("wrong and correct are required")
	}
	if len(b.Wrong) > 200 || len(b.Correct) > 200 || len(b.Phonetic) > 200 {
		return "", errInvalid("wrong, correct and phonetic must be at most 200 characters")
	}
	if strings.EqualFold(b.Wrong, b.Correct) && b.Phonetic == "" {
		return "", errInvalid("wrong and correct must differ")
	}
	return parseScope(b.Scope, defaultScope)
}

func parseScope(v string, def domain.LexiconScope) (domain.LexiconScope, error) {
	switch sc := domain.LexiconScope(strings.TrimSpace(v)); sc {
	case "":
		return def, nil
	case domain.ScopeSTT, domain.ScopeTTS, domain.ScopeBoth:
		return sc, nil
	default:
		return "", errInvalid("scope must be stt, tts or both")
	}
}

func (s *server) findCorrection(ctx context.Context, orgID uuid.UUID, match func(*domain.LexiconCorrection) bool) (*domain.LexiconCorrection, error) {
	all, err := s.d.Lexicon.ListCorrections(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list corrections: %w", err)
	}
	for i := range all {
		if all[i].OrgID == orgID && match(&all[i]) {
			c := all[i]
			return &c, nil
		}
	}
	return nil, nil
}

func (s *server) lexiconChanged(ctx context.Context, c *domain.LexiconCorrection, action string) {
	cp := *c
	s.publish(ctx, c.OrgID, nil, domain.EventLexiconUpdated, map[string]any{"correction": &cp, "action": action})
	if s.d.LexiconEngine != nil {
		s.d.LexiconEngine.Invalidate(c.OrgID)
	}
}

// upsertCorrection inserts a correction or, when one with the same wrong
// (case-insensitive) exists in the org, updates it. Returns the action.
func (s *server) upsertCorrection(ctx context.Context, orgID uuid.UUID, b lexiconBody, scope domain.LexiconScope, sourceTurn, createdBy *uuid.UUID) (*domain.LexiconCorrection, string, error) {
	existing, err := s.findCorrection(ctx, orgID, func(c *domain.LexiconCorrection) bool {
		return strings.EqualFold(strings.TrimSpace(c.Wrong), b.Wrong)
	})
	if err != nil {
		return nil, "", err
	}
	if existing != nil {
		existing.Correct = b.Correct
		existing.Scope = scope
		if b.Phonetic != "" {
			existing.Phonetic = b.Phonetic
		}
		if sourceTurn != nil {
			existing.SourceTurn = sourceTurn
		}
		if err := s.d.Lexicon.UpdateCorrection(ctx, existing); err != nil {
			return nil, "", fmt.Errorf("update correction: %w", err)
		}
		return existing, "updated", nil
	}
	c := &domain.LexiconCorrection{
		ID:         uuid.New(),
		OrgID:      orgID,
		Wrong:      b.Wrong,
		Correct:    b.Correct,
		Phonetic:   b.Phonetic,
		Scope:      scope,
		SourceTurn: sourceTurn,
		CreatedBy:  createdBy,
		CreatedAt:  s.now(),
	}
	if err := s.d.Lexicon.AddCorrection(ctx, c); err != nil {
		return nil, "", fmt.Errorf("add correction: %w", err)
	}
	return c, "created", nil
}

func (s *server) patchTurn(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	var req struct {
		Text     *string `json:"text"`
		Wrong    string  `json:"wrong"`
		Correct  string  `json:"correct"`
		Scope    string  `json:"scope"`
		Phonetic string  `json:"phonetic"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	hasWrong, hasCorrect := strings.TrimSpace(req.Wrong) != "", strings.TrimSpace(req.Correct) != ""
	if hasWrong != hasCorrect {
		s.writeErr(w, r, errInvalid("wrong and correct must be given together"))
		return
	}
	if req.Text != nil && strings.TrimSpace(*req.Text) == "" {
		s.writeErr(w, r, errInvalid("text must not be empty"))
		return
	}
	if req.Text == nil && !hasWrong {
		s.writeErr(w, r, errInvalid("text or wrong+correct is required"))
		return
	}
	body := lexiconBody{Wrong: req.Wrong, Correct: req.Correct, Scope: req.Scope, Phonetic: req.Phonetic}
	var scope domain.LexiconScope
	if hasWrong {
		if scope, err = body.validate(domain.ScopeSTT); err != nil {
			s.writeErr(w, r, err)
			return
		}
	}

	ctx := r.Context()
	cl := claimsOf(r)
	turn, err := s.d.Call.GetTurn(ctx, id)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && turn == nil) {
		s.writeErr(w, r, errNotFound("turn"))
		return
	}
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("get turn: %w", err))
		return
	}
	if _, err := s.loadCall(ctx, cl.OrgID, turn.CallID); err != nil {
		s.writeErr(w, r, asNotFound(err, "turn"))
		return
	}
	if req.Text != nil {
		text := strings.TrimSpace(*req.Text)
		if err := s.d.Call.UpdateTurnText(ctx, turn.ID, text); err != nil {
			s.writeErr(w, r, fmt.Errorf("update turn: %w", err))
			return
		}
		if t, err := s.d.Call.GetTurn(ctx, turn.ID); err == nil && t != nil {
			turn = t
		} else {
			turn.Text = text
		}
	}
	var correction *domain.LexiconCorrection
	if hasWrong {
		tid, uid := turn.ID, cl.UserID
		c, action, err := s.upsertCorrection(ctx, cl.OrgID, body, scope, &tid, &uid)
		if err != nil {
			s.writeErr(w, r, err)
			return
		}
		correction = c
		s.lexiconChanged(ctx, c, action)
	}
	writeJSON(w, http.StatusOK, map[string]any{"turn": turn, "correction": correction})
}

// asNotFound rewrites any 404 into a 404 for what.
func asNotFound(err error, what string) error {
	var ae *apiError
	if errors.As(err, &ae) && ae.status == http.StatusNotFound {
		return errNotFound(what)
	}
	return err
}

func (s *server) listLexicon(w http.ResponseWriter, r *http.Request) {
	items, err := s.d.Lexicon.ListCorrections(r.Context(), claimsOf(r).OrgID)
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("list corrections: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, newList(items, len(items)))
}

func (s *server) createLexicon(w http.ResponseWriter, r *http.Request) {
	var b lexiconBody
	if err := decodeJSON(w, r, &b); err != nil {
		s.writeErr(w, r, err)
		return
	}
	scope, err := b.validate(domain.ScopeSTT)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	cl := claimsOf(r)
	dup, err := s.findCorrection(ctx, cl.OrgID, func(c *domain.LexiconCorrection) bool { return strings.EqualFold(strings.TrimSpace(c.Wrong), b.Wrong) })
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if dup != nil {
		s.writeErr(w, r, errConflict("a correction for %q already exists", b.Wrong))
		return
	}
	uid := cl.UserID
	c, action, err := s.upsertCorrection(ctx, cl.OrgID, b, scope, nil, &uid)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.lexiconChanged(ctx, c, action)
	writeJSON(w, http.StatusCreated, map[string]any{"correction": c})
}

func (s *server) updateLexicon(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	var b lexiconBody
	if err := decodeJSON(w, r, &b); err != nil {
		s.writeErr(w, r, err)
		return
	}
	scope, err := b.validate(domain.ScopeSTT)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	orgID := claimsOf(r).OrgID
	all, err := s.d.Lexicon.ListCorrections(ctx, orgID)
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("list corrections: %w", err))
		return
	}
	var cur *domain.LexiconCorrection
	for i := range all {
		switch {
		case all[i].ID == id && all[i].OrgID == orgID:
			cur = &all[i]
		case strings.EqualFold(strings.TrimSpace(all[i].Wrong), b.Wrong):
			s.writeErr(w, r, errConflict("a correction for %q already exists", b.Wrong))
			return
		}
	}
	if cur == nil {
		s.writeErr(w, r, errNotFound("correction"))
		return
	}
	cur.Wrong, cur.Correct, cur.Phonetic, cur.Scope = b.Wrong, b.Correct, b.Phonetic, scope
	if err := s.d.Lexicon.UpdateCorrection(ctx, cur); err != nil {
		s.writeErr(w, r, fmt.Errorf("update correction: %w", err))
		return
	}
	s.lexiconChanged(ctx, cur, "updated")
	writeJSON(w, http.StatusOK, map[string]any{"correction": cur})
}

func (s *server) deleteLexicon(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	orgID := claimsOf(r).OrgID
	cur, err := s.findCorrection(ctx, orgID, func(c *domain.LexiconCorrection) bool { return c.ID == id })
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if cur == nil {
		s.writeErr(w, r, errNotFound("correction"))
		return
	}
	if err := s.d.Lexicon.DeleteCorrection(ctx, id); err != nil {
		s.writeErr(w, r, fmt.Errorf("delete correction: %w", err))
		return
	}
	s.lexiconChanged(ctx, cur, "deleted")
	noContent(w)
}

func (s *server) applyLexicon(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	orgID := claimsOf(r).OrgID
	var (
		out  string
		hits []LexiconHit
		err  error
	)
	if s.d.LexiconEngine != nil {
		out, hits, err = s.d.LexiconEngine.Apply(ctx, orgID, req.Text)
	} else {
		out, hits, err = s.applyLexiconFallback(ctx, orgID, req.Text)
	}
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("apply lexicon: %w", err))
		return
	}
	if hits == nil {
		hits = []LexiconHit{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"text": out, "hits": hits})
}

// applyLexiconFallback is a simple whole-word, case-insensitive replacement
// used when no lexicon engine is wired.
func (s *server) applyLexiconFallback(ctx context.Context, orgID uuid.UUID, text string) (string, []LexiconHit, error) {
	all, err := s.d.Lexicon.ListCorrections(ctx, orgID)
	if err != nil {
		return "", nil, fmt.Errorf("list corrections: %w", err)
	}
	var hits []LexiconHit
	for _, c := range all {
		if c.Scope == domain.ScopeTTS || strings.TrimSpace(c.Wrong) == "" {
			continue
		}
		re, err := regexp.Compile(`(?i)(^|[^\p{L}\p{N}])` + regexp.QuoteMeta(strings.TrimSpace(c.Wrong)) + `($|[^\p{L}\p{N}])`)
		if err != nil {
			continue
		}
		if !re.MatchString(text) {
			continue
		}
		text = re.ReplaceAllString(text, "${1}"+strings.ReplaceAll(c.Correct, "$", "$$")+"${2}")
		hits = append(hits, LexiconHit{ID: c.ID, Wrong: c.Wrong, Correct: c.Correct})
	}
	return text, hits, nil
}
