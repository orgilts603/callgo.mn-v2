// Package mock is a fake domain.PaymentProvider for development and tests.
// Payments stay pending until MarkPaid is called (or AutoPayAfter elapses).
package mock

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Name is the provider name.
const Name = "mock"

// ErrUnknownRef is returned for a reference the provider never issued.
var ErrUnknownRef = errors.New("mock: unknown payment reference")

type entry struct {
	createdAt time.Time
	paid      bool
}

// Provider is an in-memory payment provider.
type Provider struct {
	// AutoPayAfter > 0 makes Check report paid once the payment is that old.
	AutoPayAfter time.Duration
	// TTL sets Payment.ExpiresAt (default 24h).
	TTL time.Duration
	// Now is the clock (default time.Now).
	Now func() time.Time

	mu      sync.Mutex
	entries map[string]*entry
}

var _ domain.PaymentProvider = (*Provider)(nil)

// New returns a mock provider.
func New() *Provider {
	return &Provider{entries: map[string]*entry{}}
}

func (p *Provider) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

// Name implements domain.PaymentProvider.
func (p *Provider) Name() string { return Name }

// Ref returns the provider reference issued for a payment ID.
func Ref(paymentID string) string { return "mock_" + paymentID }

// CreateInvoice implements domain.PaymentProvider: it fills a fake reference,
// QR text "MOCK:<paymentId>", a deep link and an expiry.
func (p *Provider) CreateInvoice(_ context.Context, pay *domain.Payment, description string) error {
	if pay.AmountMNT <= 0 {
		return fmt.Errorf("mock: amount must be positive")
	}
	now := p.now()
	ttl := p.TTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	exp := now.Add(ttl)
	id := pay.ID.String()
	pay.ProviderRef = Ref(id)
	pay.QRText = "MOCK:" + id
	pay.QRImage = ""
	pay.DeepLinks = []domain.PaymentLink{{Name: "Mock bank", Link: "mockbank://pay?ref=" + pay.ProviderRef}}
	pay.ExpiresAt = &exp
	pay.Raw = map[string]any{"description": description}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entries[pay.ProviderRef] = &entry{createdAt: now}
	return nil
}

// Check implements domain.PaymentProvider.
func (p *Provider) Check(_ context.Context, pay *domain.Payment) (domain.PaymentStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.entries[pay.ProviderRef]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownRef, pay.ProviderRef)
	}
	if e.paid || (p.AutoPayAfter > 0 && p.now().Sub(e.createdAt) >= p.AutoPayAfter) {
		e.paid = true
		return domain.PaymentPaid, nil
	}
	return domain.PaymentPending, nil
}

// MarkPaid simulates the customer paying. ref may be the provider reference
// or the payment ID.
func (p *Provider) MarkPaid(ref string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !strings.HasPrefix(ref, "mock_") {
		ref = Ref(ref)
	}
	e, ok := p.entries[ref]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownRef, ref)
	}
	e.paid = true
	return nil
}

// VerifyCallback implements domain.PaymentProvider: the callback carries
// ?payment_id=<uuid>, which is returned as the reference to resolve. With
// &paid=true the callback also simulates the customer having paid (the bank
// confirming), so a dev flow can settle a payment with one request.
func (p *Provider) VerifyCallback(_ context.Context, query map[string]string, _ []byte) (string, error) {
	id := strings.TrimSpace(query["payment_id"])
	if id == "" {
		return "", errors.New("mock: callback without payment_id")
	}
	switch strings.ToLower(strings.TrimSpace(query["paid"])) {
	case "1", "true", "yes":
		if err := p.MarkPaid(id); err != nil {
			return "", err
		}
	}
	return id, nil
}
