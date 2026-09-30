package crm

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Optional repository methods used by the SaaS services through local
// interfaces (recording.RecordingURLSetter, identity.UserDeleter,
// billing.UsageChecker, the webhook delivery retry handler).

// SetCallRecordingURL updates calls.recording_url only.
func (s *Store) SetCallRecordingURL(ctx context.Context, callID uuid.UUID, url string) error {
	tag, err := s.db.Exec(ctx, `UPDATE calls SET recording_url = $2, updated_at = now() WHERE id = $1`, callID, url)
	if err != nil {
		return fmt.Errorf("set recording url: %w", mapErr(err))
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// DeleteUser removes a user row (sessions/api keys cascade or null out).
func (s *Store) DeleteUser(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", mapErr(err))
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// HasUsageForCall reports whether any usage record exists for the call
// (used to make call metering idempotent).
func (s *Store) HasUsageForCall(ctx context.Context, callID uuid.UUID) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM usage_records WHERE call_id = $1)`, callID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("has usage for call: %w", mapErr(err))
	}
	return exists, nil
}

// GetDelivery returns one webhook delivery.
func (s *Store) GetDelivery(ctx context.Context, id uuid.UUID) (*domain.WebhookDelivery, error) {
	row := s.db.QueryRow(ctx, `SELECT id, webhook_id, event_id, event_type, status, attempts, response_code,
		last_error, next_try_at, payload, created_at, updated_at FROM webhook_deliveries WHERE id = $1`, id)
	var d domain.WebhookDelivery
	err := row.Scan(&d.ID, &d.WebhookID, &d.EventID, &d.EventType, &d.Status, &d.Attempts, &d.ResponseCode,
		&d.LastError, &d.NextTryAt, &d.Payload, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get delivery: %w", mapErr(err))
	}
	return &d, nil
}

// EncryptBytes / DecryptBytes expose the store's AES-GCM key cipher for
// other services that keep secrets in org settings (e.g. SMS gateway keys).
func (s *Store) EncryptBytes(b []byte) ([]byte, error) {
	enc, err := s.cipher.encrypt(string(b))
	if err != nil {
		return nil, err
	}
	return []byte(enc), nil
}

// DecryptBytes reverses EncryptBytes.
func (s *Store) DecryptBytes(b []byte) ([]byte, error) {
	plain, err := s.cipher.decrypt(string(b))
	if err != nil {
		return nil, err
	}
	return []byte(plain), nil
}
