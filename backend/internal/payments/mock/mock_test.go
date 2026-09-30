package mock_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/payments/mock"
)

func TestMockRoundTrip(t *testing.T) {
	ctx := context.Background()
	p := mock.New()
	assert.Equal(t, "mock", p.Name())
	pay := &domain.Payment{ID: uuid.New(), AmountMNT: 1000}
	require.NoError(t, p.CreateInvoice(ctx, pay, "test"))
	assert.Equal(t, "mock_"+pay.ID.String(), pay.ProviderRef)
	assert.Equal(t, "MOCK:"+pay.ID.String(), pay.QRText)
	require.NotNil(t, pay.ExpiresAt)
	require.Len(t, pay.DeepLinks, 1)

	st, err := p.Check(ctx, pay)
	require.NoError(t, err)
	assert.Equal(t, domain.PaymentPending, st)

	require.NoError(t, p.MarkPaid(pay.ID.String()))
	st, err = p.Check(ctx, pay)
	require.NoError(t, err)
	assert.Equal(t, domain.PaymentPaid, st)

	ref, err := p.VerifyCallback(ctx, map[string]string{"payment_id": pay.ID.String()}, nil)
	require.NoError(t, err)
	assert.Equal(t, pay.ID.String(), ref)
	_, err = p.VerifyCallback(ctx, map[string]string{}, nil)
	assert.Error(t, err)

	assert.ErrorIs(t, p.MarkPaid("mock_nope"), mock.ErrUnknownRef)
	_, err = p.Check(ctx, &domain.Payment{ProviderRef: "mock_nope"})
	assert.ErrorIs(t, err, mock.ErrUnknownRef)
	assert.Error(t, p.CreateInvoice(ctx, &domain.Payment{ID: uuid.New()}, "zero"))
}

func TestMockAutoPay(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	p := mock.New()
	p.Now = func() time.Time { return now }
	p.AutoPayAfter = 5 * time.Second
	pay := &domain.Payment{ID: uuid.New(), AmountMNT: 1}
	require.NoError(t, p.CreateInvoice(ctx, pay, ""))
	st, _ := p.Check(ctx, pay)
	assert.Equal(t, domain.PaymentPending, st)
	now = now.Add(5 * time.Second)
	st, _ = p.Check(ctx, pay)
	assert.Equal(t, domain.PaymentPaid, st)
}
