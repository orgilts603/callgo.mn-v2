package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Invite creates an invitation for email with role and emails the accept
// link. Only owners/admins may invite, and only owners may invite owners.
// It fails with ErrConflict when the email already has an account and with
// *QuotaError when the plan's MaxUsers (active users + pending
// invitations) is reached. A pending invitation for the same email is
// replaced.
func (s *Service) Invite(ctx context.Context, actor Actor, email string, role domain.Role) (*domain.Invitation, error) {
	if !actor.isAdmin() {
		return nil, errForbidden("only owners and admins can invite members")
	}
	email = NormalizeEmail(email)
	if !validEmail(email) {
		return nil, errInvalid("email is invalid")
	}
	if role == "" {
		role = domain.RoleOperator
	}
	if !validRole(role) {
		return nil, errInvalid("role must be owner, admin or operator")
	}
	if role == domain.RoleOwner && actor.Role != domain.RoleOwner {
		return nil, errForbidden("only owners can invite owners")
	}
	if _, err := s.users.GetUserByEmail(ctx, email); err == nil {
		return nil, errConflict("a user with this email already exists")
	} else if !errors.Is(err, domain.ErrNotFound) {
		return nil, fmt.Errorf("lookup user: %w", err)
	}
	org, err := s.getOrg(ctx, actor.OrgID)
	if err != nil {
		return nil, err
	}
	pending, err := s.pendingInvitations(ctx, actor.OrgID)
	if err != nil {
		return nil, err
	}
	var replaced []uuid.UUID
	others := 0
	for _, inv := range pending {
		if strings.EqualFold(inv.Email, email) {
			replaced = append(replaced, inv.ID)
		} else {
			others++
		}
	}
	if err := s.checkUserQuota(ctx, actor.OrgID, others); err != nil {
		return nil, err
	}
	plain, hash, err := newToken()
	if err != nil {
		return nil, err
	}
	now := s.now()
	inviter := actor.UserID
	inv := &domain.Invitation{
		ID: uuid.New(), OrgID: actor.OrgID, Email: email, Role: role, TokenHash: hash,
		ExpiresAt: now.Add(s.cfg.InviteTTL), CreatedAt: now,
	}
	if inviter != uuid.Nil {
		inv.InvitedBy = &inviter
	}
	if err := s.repo.CreateInvitation(ctx, inv); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return nil, errConflict("an invitation for this email already exists")
		}
		return nil, fmt.Errorf("create invitation: %w", err)
	}
	for _, id := range replaced {
		if err := s.repo.DeleteInvitation(ctx, id); err != nil && !errors.Is(err, domain.ErrNotFound) {
			s.log.Warn().Err(err).Stringer("invitationId", id).Msg("delete replaced invitation")
		}
	}
	inviterName := ""
	if actor.UserID != uuid.Nil {
		if u, err := s.users.GetUser(ctx, actor.UserID); err == nil {
			inviterName = u.Name
		}
	}
	msg, err := s.tpl.Invitation(org.Name, inviterName, string(role), plain, s.cfg.InviteTTL)
	s.sendMail(ctx, email, msg, err)
	return inv, nil
}

// checkUserQuota enforces Plan.MaxUsers against active users plus
// extraPending reserved seats (0 or negative MaxUsers = unlimited).
func (s *Service) checkUserQuota(ctx context.Context, orgID uuid.UUID, extraPending int) error {
	if s.Limits == nil {
		return nil
	}
	plan, err := s.Limits(ctx, orgID)
	if err != nil {
		return fmt.Errorf("load plan limits: %w", err)
	}
	if plan.MaxUsers <= 0 {
		return nil
	}
	n, err := s.repo.CountUsers(ctx, orgID)
	if err != nil {
		return fmt.Errorf("count users: %w", err)
	}
	used := n + extraPending
	if used >= plan.MaxUsers {
		return &QuotaError{Resource: "users", Limit: plan.MaxUsers, Used: used}
	}
	return nil
}

// pendingInvitations returns the org's not-accepted, not-expired invitations.
func (s *Service) pendingInvitations(ctx context.Context, orgID uuid.UUID) ([]domain.Invitation, error) {
	all, err := s.repo.ListInvitations(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list invitations: %w", err)
	}
	now := s.now()
	out := make([]domain.Invitation, 0, len(all))
	for _, inv := range all {
		if inv.AcceptedAt == nil && !expired(now, inv.ExpiresAt) {
			out = append(out, inv)
		}
	}
	return out, nil
}

// ListMembers returns the org's users and pending invitations.
func (s *Service) ListMembers(ctx context.Context, orgID uuid.UUID) ([]domain.User, []domain.Invitation, error) {
	users, err := s.users.ListUsers(ctx, orgID)
	if err != nil {
		return nil, nil, fmt.Errorf("list users: %w", err)
	}
	inv, err := s.pendingInvitations(ctx, orgID)
	if err != nil {
		return nil, nil, err
	}
	return users, inv, nil
}

// CancelInvitation deletes a (pending or expired) invitation of the org.
func (s *Service) CancelInvitation(ctx context.Context, orgID, invitationID uuid.UUID) (*domain.Invitation, error) {
	all, err := s.repo.ListInvitations(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list invitations: %w", err)
	}
	for _, inv := range all {
		if inv.ID != invitationID {
			continue
		}
		if err := s.repo.DeleteInvitation(ctx, inv.ID); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return nil, errNotFound("invitation")
			}
			return nil, fmt.Errorf("delete invitation: %w", err)
		}
		return &inv, nil
	}
	return nil, errNotFound("invitation")
}

// AcceptInvitationInput is the POST /api/auth/accept-invitation body.
type AcceptInvitationInput struct {
	Token    string `json:"token"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

// AcceptInvitation creates the invited user (active, email verified — the
// link proved the mailbox) and opens a session.
func (s *Service) AcceptInvitation(ctx context.Context, in AcceptInvitationInput, c Client) (*AuthResult, error) {
	invalid := errInvalid("invitation is invalid or has expired")
	name, err := validateName("name", in.Name, 120)
	if err != nil {
		return nil, err
	}
	if err := validatePassword(in.Password); err != nil {
		return nil, err
	}
	token := strings.TrimSpace(in.Token)
	if token == "" {
		return nil, invalid
	}
	inv, err := s.repo.GetInvitationByHash(ctx, HashToken(token))
	if errors.Is(err, domain.ErrNotFound) {
		return nil, invalid
	}
	if err != nil {
		return nil, fmt.Errorf("get invitation: %w", err)
	}
	now := s.now()
	if inv.AcceptedAt != nil || expired(now, inv.ExpiresAt) {
		return nil, invalid
	}
	org, err := s.getOrg(ctx, inv.OrgID)
	if err != nil {
		return nil, err
	}
	if org.Status == domain.OrgClosed {
		return nil, errForbidden("organisation is closed")
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return nil, err
	}
	u := &domain.User{
		ID: uuid.New(), OrgID: inv.OrgID, Email: NormalizeEmail(inv.Email), Name: name, Role: inv.Role,
		PasswordHash: hash, Status: domain.UserActive, EmailVerifiedAt: &now, LastLoginAt: &now,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.users.CreateUser(ctx, u); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return nil, errConflict("a user with this email already exists")
		}
		return nil, fmt.Errorf("create user: %w", err)
	}
	if err := s.repo.MarkInvitationAccepted(ctx, inv.ID); err != nil {
		s.log.Error().Err(err).Stringer("invitationId", inv.ID).Msg("mark invitation accepted")
	}
	res, err := s.issue(ctx, u, org, c)
	if err != nil {
		return nil, err
	}
	_ = s.Audit(ctx, org.ID, Actor{UserID: u.ID, OrgID: org.ID, Role: u.Role, Email: u.Email}, "invitation.accept", "user", u.ID.String(),
		map[string]any{"invitationId": inv.ID.String(), "role": string(u.Role)}, c.IP)
	return res, nil
}

// MemberUpdate changes a member's role and/or status (nil = unchanged).
type MemberUpdate struct {
	Role   *domain.Role       `json:"role,omitempty"`
	Status *domain.UserStatus `json:"status,omitempty"`
}

// UpdateMember changes another member's role or status. Guards: nobody can
// change themselves; admins cannot touch owners or grant owner; the last
// active owner cannot be demoted or disabled. Disabling revokes sessions.
func (s *Service) UpdateMember(ctx context.Context, actor Actor, userID uuid.UUID, upd MemberUpdate) (*domain.User, error) {
	if !actor.isAdmin() {
		return nil, errForbidden("only owners and admins can manage members")
	}
	if upd.Role != nil && !validRole(*upd.Role) {
		return nil, errInvalid("role must be owner, admin or operator")
	}
	if upd.Status != nil && *upd.Status != domain.UserActive && *upd.Status != domain.UserDisabled {
		return nil, errInvalid("status must be active or disabled")
	}
	u, err := s.memberOf(ctx, actor.OrgID, userID)
	if err != nil {
		return nil, err
	}
	if u.ID == actor.UserID {
		return nil, errForbidden("you cannot change your own role or status")
	}
	if actor.Role != domain.RoleOwner && (u.Role == domain.RoleOwner || (upd.Role != nil && *upd.Role == domain.RoleOwner)) {
		return nil, errForbidden("only owners can manage owners")
	}
	newRole, newStatus := u.Role, u.Status
	if upd.Role != nil {
		newRole = *upd.Role
	}
	if upd.Status != nil {
		newStatus = *upd.Status
	}
	losingOwner := u.Role == domain.RoleOwner && userActive(u) && (newRole != domain.RoleOwner || newStatus == domain.UserDisabled)
	if losingOwner {
		if err := s.guardLastOwner(ctx, actor.OrgID, u.ID); err != nil {
			return nil, err
		}
	}
	disabling := newStatus == domain.UserDisabled && u.Status != domain.UserDisabled
	u.Role, u.Status = newRole, newStatus
	if err := s.repo.UpdateUser(ctx, u); err != nil {
		return nil, fmt.Errorf("update user: %w", err)
	}
	if disabling {
		if err := s.repo.RevokeUserSessions(ctx, u.ID); err != nil {
			return nil, fmt.Errorf("revoke sessions: %w", err)
		}
	}
	return u, nil
}

// guardLastOwner fails with ErrConflict when no other active owner exists.
func (s *Service) guardLastOwner(ctx context.Context, orgID, excluding uuid.UUID) error {
	users, err := s.users.ListUsers(ctx, orgID)
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}
	for i := range users {
		if users[i].ID != excluding && users[i].Role == domain.RoleOwner && userActive(&users[i]) {
			return nil
		}
	}
	return errConflict("the organisation must keep at least one active owner")
}

// RemoveMember deletes another member (disables them when the repository
// cannot delete users) and revokes their sessions. Same guards as
// UpdateMember.
func (s *Service) RemoveMember(ctx context.Context, actor Actor, userID uuid.UUID) (*domain.User, error) {
	if !actor.isAdmin() {
		return nil, errForbidden("only owners and admins can manage members")
	}
	u, err := s.memberOf(ctx, actor.OrgID, userID)
	if err != nil {
		return nil, err
	}
	if u.ID == actor.UserID {
		return nil, errForbidden("you cannot remove yourself")
	}
	if u.Role == domain.RoleOwner {
		if actor.Role != domain.RoleOwner {
			return nil, errForbidden("only owners can manage owners")
		}
		if userActive(u) {
			if err := s.guardLastOwner(ctx, actor.OrgID, u.ID); err != nil {
				return nil, err
			}
		}
	}
	if err := s.repo.RevokeUserSessions(ctx, u.ID); err != nil {
		return nil, fmt.Errorf("revoke sessions: %w", err)
	}
	if d := s.userDeleter(); d != nil {
		if err := d.DeleteUser(ctx, u.ID); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return nil, errNotFound("user")
			}
			return nil, fmt.Errorf("delete user: %w", err)
		}
		return u, nil
	}
	s.log.Warn().Stringer("userId", u.ID).Msg("repository cannot delete users; disabling instead")
	u.Status = domain.UserDisabled
	if err := s.repo.UpdateUser(ctx, u); err != nil {
		return nil, fmt.Errorf("disable user: %w", err)
	}
	return u, nil
}

func (s *Service) userDeleter() UserDeleter {
	if d, ok := s.repo.(UserDeleter); ok {
		return d
	}
	if d, ok := s.users.(UserDeleter); ok {
		return d
	}
	return nil
}
