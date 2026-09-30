// Package qpay implements domain.PaymentProvider against the QPay v2
// merchant API (https://merchant.qpay.mn/v2):
//
//	POST /auth/token     basic auth → access/refresh token (cached)
//	POST /auth/refresh   bearer refresh token → new tokens
//	POST /invoice        → invoice_id, qr_text, qr_image, urls[]
//	POST /payment/check  object_type INVOICE → rows[].payment_status
//
// The callback URL carries ?payment_id=<CallGo payment uuid>; callbacks are
// never trusted on their own — the billing service always confirms with
// Check.
package qpay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Name is the provider name.
const Name = "qpay"

// DefaultBaseURL is the production QPay v2 merchant API.
const DefaultBaseURL = "https://merchant.qpay.mn/v2"

// Config configures the client.
type Config struct {
	BaseURL     string // default DefaultBaseURL (sandbox: https://merchant-sandbox.qpay.mn/v2)
	Username    string // QPay merchant client id
	Password    string // QPay merchant client secret
	InvoiceCode string // merchant invoice template code, e.g. "CALLGO_INVOICE"
	// CallbackURL is the public URL of POST /api/billing/webhooks/qpay; the
	// client appends payment_id=<uuid>.
	CallbackURL string
	Client      *http.Client // default: 20s timeout
	Now         func() time.Time
}

// FromEnv reads QPAY_BASE_URL, QPAY_USERNAME, QPAY_PASSWORD,
// QPAY_INVOICE_CODE and QPAY_CALLBACK_URL through getenv (os.Getenv).
func FromEnv(getenv func(string) string) Config {
	return Config{
		BaseURL:     getenv("QPAY_BASE_URL"),
		Username:    getenv("QPAY_USERNAME"),
		Password:    getenv("QPAY_PASSWORD"),
		InvoiceCode: getenv("QPAY_INVOICE_CODE"),
		CallbackURL: getenv("QPAY_CALLBACK_URL"),
	}
}

// Configured reports whether the credentials needed to talk to QPay are set.
func (c Config) Configured() bool {
	return c.Username != "" && c.Password != "" && c.InvoiceCode != "" && c.CallbackURL != ""
}

// Client is a QPay v2 merchant API client. It is safe for concurrent use.
type Client struct {
	cfg  Config
	http *http.Client
	now  func() time.Time

	mu            sync.Mutex
	accessToken   string
	accessExpiry  time.Time
	refreshToken  string
	refreshExpiry time.Time
}

var _ domain.PaymentProvider = (*Client)(nil)

// APIError is a non-2xx answer from QPay.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string { return fmt.Sprintf("qpay: HTTP %d: %s", e.Status, e.Body) }

// New builds a client.
func New(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	hc := cfg.Client
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Client{cfg: cfg, http: hc, now: now}
}

// Name implements domain.PaymentProvider.
func (c *Client) Name() string { return Name }

// ---------------------------------------------------------------------------
// auth
// ---------------------------------------------------------------------------

type tokenResponse struct {
	TokenType        string  `json:"token_type"`
	AccessToken      string  `json:"access_token"`
	ExpiresIn        flexNum `json:"expires_in"`
	RefreshToken     string  `json:"refresh_token"`
	RefreshExpiresIn flexNum `json:"refresh_expires_in"`
}

// expiry interprets QPay's expires_in, which is an absolute Unix timestamp
// in production answers but a lifetime in seconds in some sandboxes.
func expiry(now time.Time, v flexNum, def time.Duration) time.Time {
	n := int64(v)
	switch {
	case n <= 0:
		return now.Add(def)
	case n > 1_000_000_000:
		return time.Unix(n, 0)
	default:
		return now.Add(time.Duration(n) * time.Second)
	}
}

const tokenSkew = 30 * time.Second

// token returns a valid access token, refreshing or re-authenticating.
func (c *Client) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.accessToken != "" && now.Add(tokenSkew).Before(c.accessExpiry) {
		return c.accessToken, nil
	}
	if c.refreshToken != "" && now.Add(tokenSkew).Before(c.refreshExpiry) {
		if err := c.authLocked(ctx, "/auth/refresh", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+c.refreshToken)
		}); err == nil {
			return c.accessToken, nil
		}
	}
	if err := c.authLocked(ctx, "/auth/token", func(r *http.Request) {
		r.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	}); err != nil {
		return "", err
	}
	return c.accessToken, nil
}

// invalidate drops the cached access token (after a 401).
func (c *Client) invalidate(tok string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.accessToken == tok {
		c.accessToken = ""
	}
}

func (c *Client) authLocked(ctx context.Context, path string, authorize func(*http.Request)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+path, http.NoBody)
	if err != nil {
		return fmt.Errorf("qpay: auth request: %w", err)
	}
	authorize(req)
	var tr tokenResponse
	if err := c.send(req, &tr); err != nil {
		return fmt.Errorf("qpay: auth %s: %w", path, err)
	}
	if tr.AccessToken == "" {
		return errors.New("qpay: auth: empty access_token")
	}
	now := c.now()
	c.accessToken = tr.AccessToken
	c.accessExpiry = expiry(now, tr.ExpiresIn, time.Hour)
	if tr.RefreshToken != "" {
		c.refreshToken = tr.RefreshToken
		c.refreshExpiry = expiry(now, tr.RefreshExpiresIn, 24*time.Hour)
	}
	return nil
}

// send executes req and decodes a 2xx JSON answer into out.
func (c *Client) send(req *http.Request, out any) error {
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet := string(body)
		if len(snippet) > 512 {
			snippet = snippet[:512]
		}
		return &APIError{Status: resp.StatusCode, Body: snippet}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

// call POSTs JSON to path with the bearer token, retrying once on 401 with
// a fresh token.
func (c *Client) call(ctx context.Context, path string, in, out any) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("qpay: encode %s: %w", path, err)
	}
	for attempt := 0; ; attempt++ {
		tok, err := c.token(ctx)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+path, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("qpay: request %s: %w", path, err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		err = c.send(req, out)
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == http.StatusUnauthorized && attempt == 0 {
			c.invalidate(tok)
			continue
		}
		if err != nil {
			return fmt.Errorf("qpay: %s: %w", path, err)
		}
		return nil
	}
}

// ---------------------------------------------------------------------------
// PaymentProvider
// ---------------------------------------------------------------------------

type invoiceRequest struct {
	InvoiceCode         string `json:"invoice_code"`
	SenderInvoiceNo     string `json:"sender_invoice_no"`
	InvoiceReceiverCode string `json:"invoice_receiver_code"`
	InvoiceDescription  string `json:"invoice_description"`
	Amount              int64  `json:"amount"`
	CallbackURL         string `json:"callback_url"`
}

type invoiceURL struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Logo        string `json:"logo"`
	Link        string `json:"link"`
}

type invoiceResponse struct {
	InvoiceID string       `json:"invoice_id"`
	QRText    string       `json:"qr_text"`
	QRImage   string       `json:"qr_image"`
	ShortURL  string       `json:"qPay_shortUrl"`
	URLs      []invoiceURL `json:"urls"`
}

// CallbackURLFor returns the callback URL for a payment ID.
func (c *Client) CallbackURLFor(paymentID string) (string, error) {
	u, err := url.Parse(c.cfg.CallbackURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("qpay: invalid callback URL %q", c.cfg.CallbackURL)
	}
	q := u.Query()
	q.Set("payment_id", paymentID)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// CreateInvoice implements domain.PaymentProvider (POST /invoice).
func (c *Client) CreateInvoice(ctx context.Context, p *domain.Payment, description string) error {
	if p.AmountMNT <= 0 {
		return errors.New("qpay: amount must be positive")
	}
	cb, err := c.CallbackURLFor(p.ID.String())
	if err != nil {
		return err
	}
	in := invoiceRequest{
		InvoiceCode:         c.cfg.InvoiceCode,
		SenderInvoiceNo:     p.ID.String(),
		InvoiceReceiverCode: p.OrgID.String(),
		InvoiceDescription:  description,
		Amount:              p.AmountMNT,
		CallbackURL:         cb,
	}
	var out invoiceResponse
	if err := c.call(ctx, "/invoice", in, &out); err != nil {
		return err
	}
	if out.InvoiceID == "" {
		return errors.New("qpay: invoice: empty invoice_id")
	}
	p.ProviderRef = out.InvoiceID
	p.QRText = out.QRText
	p.QRImage = out.QRImage
	p.DeepLinks = make([]domain.PaymentLink, 0, len(out.URLs))
	for _, u := range out.URLs {
		name := u.Description
		if name == "" {
			name = u.Name
		}
		p.DeepLinks = append(p.DeepLinks, domain.PaymentLink{Name: name, Logo: u.Logo, Link: u.Link})
	}
	p.Raw = map[string]any{"invoiceId": out.InvoiceID, "shortUrl": out.ShortURL}
	return nil
}

type checkRequest struct {
	ObjectType string      `json:"object_type"`
	ObjectID   string      `json:"object_id"`
	Offset     checkOffset `json:"offset"`
}

type checkOffset struct {
	PageNumber int `json:"page_number"`
	PageLimit  int `json:"page_limit"`
}

type checkRow struct {
	PaymentID     string  `json:"payment_id"`
	PaymentStatus string  `json:"payment_status"` // NEW | FAILED | PAID | REFUNDED
	PaymentAmount flexNum `json:"payment_amount"`
}

type checkResponse struct {
	Count      int        `json:"count"`
	PaidAmount flexNum    `json:"paid_amount"`
	Rows       []checkRow `json:"rows"`
}

// Check implements domain.PaymentProvider (POST /payment/check). The payment
// is paid when QPay reports PAID rows covering the amount.
func (c *Client) Check(ctx context.Context, p *domain.Payment) (domain.PaymentStatus, error) {
	if p.ProviderRef == "" {
		return "", errors.New("qpay: payment has no invoice id")
	}
	in := checkRequest{ObjectType: "INVOICE", ObjectID: p.ProviderRef, Offset: checkOffset{PageNumber: 1, PageLimit: 100}}
	var out checkResponse
	if err := c.call(ctx, "/payment/check", in, &out); err != nil {
		return "", err
	}
	var paid float64
	anyPaid := false
	for _, r := range out.Rows {
		if strings.EqualFold(r.PaymentStatus, "PAID") {
			anyPaid = true
			paid += float64(r.PaymentAmount)
		}
	}
	if !anyPaid {
		return domain.PaymentPending, nil
	}
	if paid == 0 {
		paid = float64(out.PaidAmount)
	}
	if paid > 0 && paid+0.5 < float64(p.AmountMNT) {
		return domain.PaymentPending, nil // partial payment
	}
	return domain.PaymentPaid, nil
}

// VerifyCallback implements domain.PaymentProvider: it returns the CallGo
// payment id from ?payment_id= (the billing service then confirms with
// Check).
func (c *Client) VerifyCallback(_ context.Context, query map[string]string, _ []byte) (string, error) {
	id := strings.TrimSpace(query["payment_id"])
	if id == "" {
		return "", errors.New("qpay: callback without payment_id")
	}
	return id, nil
}

// flexNum decodes a JSON number or numeric string ("100.00").
type flexNum float64

func (f *flexNum) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("qpay: bad number %q: %w", s, err)
	}
	*f = flexNum(v)
	return nil
}
