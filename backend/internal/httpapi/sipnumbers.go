package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func (s *server) loadSIPNumber(ctx context.Context, orgID, id uuid.UUID) (*domain.SIPNumber, error) {
	n, err := s.d.SIPNumber.GetSIPNumber(ctx, id)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && (n == nil || n.OrgID != orgID)) {
		return nil, errNotFound("SIP number")
	}
	if err != nil {
		return nil, fmt.Errorf("get sip number: %w", err)
	}
	return n, nil
}

type sipNumberBody struct {
	Number           string `json:"number"`
	Label            string `json:"label"`
	AgentProfileID   string `json:"agentProfileId"`
	AllowInbound     bool   `json:"allowInbound"`
	AllowOutbound    bool   `json:"allowOutbound"`
	AsteriskEndpoint string `json:"asteriskEndpoint"`
	Active           *bool  `json:"active"`
}

// apply validates the body and copies it onto n (which must carry OrgID/ID).
func (s *server) applySIPNumberBody(ctx context.Context, b sipNumberBody, n *domain.SIPNumber) error {
	num, err := requirePhone(b.Number, "number")
	if err != nil {
		return err
	}
	label := strings.TrimSpace(b.Label)
	if len(label) > 100 {
		return errInvalid("label must be at most 100 characters")
	}
	profID, err := parseOptUUID(b.AgentProfileID, "agentProfileId")
	if err != nil {
		return err
	}
	if profID != nil {
		if _, err := s.loadProfile(ctx, n.OrgID, *profID); err != nil {
			return asInvalidRef(err, "agentProfileId")
		}
	}
	existing, err := s.d.SIPNumber.GetSIPNumberByNumber(ctx, num)
	switch {
	case err == nil && existing != nil && existing.ID != n.ID:
		return errConflict("number %s is already registered", num)
	case err != nil && !errors.Is(err, domain.ErrNotFound):
		return fmt.Errorf("lookup sip number: %w", err)
	}
	n.Number, n.Label, n.AgentProfileID = num, label, profID
	n.AllowInbound, n.AllowOutbound = b.AllowInbound, b.AllowOutbound
	n.AsteriskEndpoint = strings.TrimSpace(b.AsteriskEndpoint)
	if b.Active != nil {
		n.Active = *b.Active
	}
	return nil
}

func (s *server) listSIPNumbers(w http.ResponseWriter, r *http.Request) {
	items, err := s.d.SIPNumber.ListSIPNumbers(r.Context(), claimsOf(r).OrgID)
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("list sip numbers: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, newList(items, len(items)))
}

func (s *server) getSIPNumber(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	n, err := s.loadSIPNumber(r.Context(), claimsOf(r).OrgID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sipNumber": n})
}

func (s *server) createSIPNumber(w http.ResponseWriter, r *http.Request) {
	var b sipNumberBody
	if err := decodeJSON(w, r, &b); err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	now := s.now()
	n := &domain.SIPNumber{ID: uuid.New(), OrgID: claimsOf(r).OrgID, Active: true, CreatedAt: now, UpdatedAt: now}
	if err := s.applySIPNumberBody(ctx, b, n); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.d.SIPNumber.CreateSIPNumber(ctx, n); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			s.writeErr(w, r, errConflict("number %s is already registered", n.Number))
			return
		}
		s.writeErr(w, r, fmt.Errorf("create sip number: %w", err))
		return
	}
	if n.Active {
		if err := s.provision(ctx, n); err != nil {
			// Roll back so the client can simply retry.
			if derr := s.d.Telephony.DeprovisionNumber(ctx, n); derr != nil {
				s.log.Warn().Err(derr).Str("number", n.Number).Msg("rollback deprovision")
			}
			if derr := s.d.SIPNumber.DeleteSIPNumber(ctx, n.ID); derr != nil {
				s.log.Error().Err(derr).Str("number", n.Number).Msg("rollback delete sip number")
			}
			s.writeErr(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"sipNumber": n})
}

// provision runs Telephony.EnsureNumberProvisioned and stores the LiveKit IDs.
func (s *server) provision(ctx context.Context, n *domain.SIPNumber) error {
	if err := s.d.Telephony.EnsureNumberProvisioned(ctx, n); err != nil {
		s.log.Error().Err(err).Str("number", n.Number).Msg("provision sip number")
		return &apiError{http.StatusInternalServerError, "internal", "provisioning in LiveKit failed: " + err.Error()}
	}
	n.UpdatedAt = s.now()
	if err := s.d.SIPNumber.UpdateSIPNumber(ctx, n); err != nil {
		return fmt.Errorf("save provisioned sip number: %w", err)
	}
	return nil
}

func (s *server) updateSIPNumber(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	var b sipNumberBody
	if err := decodeJSON(w, r, &b); err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	n, err := s.loadSIPNumber(ctx, claimsOf(r).OrgID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.applySIPNumberBody(ctx, b, n); err != nil {
		s.writeErr(w, r, err)
		return
	}
	n.UpdatedAt = s.now()
	if err := s.d.SIPNumber.UpdateSIPNumber(ctx, n); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			s.writeErr(w, r, errConflict("number %s is already registered", n.Number))
			return
		}
		s.writeErr(w, r, fmt.Errorf("update sip number: %w", err))
		return
	}
	if n.Active {
		err = s.provision(ctx, n)
	} else {
		err = s.deprovision(ctx, n)
	}
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sipNumber": n})
}

func (s *server) deprovision(ctx context.Context, n *domain.SIPNumber) error {
	if n.InboundTrunkID == "" && n.OutboundTrunkID == "" && n.DispatchRuleID == "" {
		return nil
	}
	if err := s.d.Telephony.DeprovisionNumber(ctx, n); err != nil {
		return fmt.Errorf("deprovision sip number: %w", err)
	}
	n.InboundTrunkID, n.OutboundTrunkID, n.DispatchRuleID = "", "", ""
	n.UpdatedAt = s.now()
	if err := s.d.SIPNumber.UpdateSIPNumber(ctx, n); err != nil {
		return fmt.Errorf("save deprovisioned sip number: %w", err)
	}
	return nil
}

func (s *server) deleteSIPNumber(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	n, err := s.loadSIPNumber(ctx, claimsOf(r).OrgID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.d.Telephony.DeprovisionNumber(ctx, n); err != nil {
		// Do not block deletion on LiveKit cleanup; stale trunks are harmless
		// without a matching number row and are reported in the log.
		s.log.Warn().Err(err).Str("number", n.Number).Msg("deprovision on delete failed")
	}
	if err := s.d.SIPNumber.DeleteSIPNumber(ctx, n.ID); err != nil {
		s.writeErr(w, r, fmt.Errorf("delete sip number: %w", err))
		return
	}
	noContent(w)
}

func (s *server) provisionSIPNumber(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	n, err := s.loadSIPNumber(ctx, claimsOf(r).OrgID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.provision(ctx, n); err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sipNumber": n})
}
