package sms_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/sms"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/webhooks/webhookstest"
)

func TestMock(t *testing.T) {
	m := sms.NewMock()
	require.Equal(t, "mock", m.Name())
	ref, err := m.Send(context.Background(), "+97699112233", "hi")
	require.NoError(t, err)
	require.Equal(t, "mock-1", ref)
	require.Equal(t, []sms.SentMessage{{To: "+97699112233", Body: "hi"}}, m.Sent())
	m.Err = errors.New("down")
	_, err = m.Send(context.Background(), "+97699112233", "x")
	require.Error(t, err)
	require.Len(t, m.Sent(), 1)
}

type gwReq struct {
	method string
	url    string
	header http.Header
	body   string
}

func gateway(t *testing.T, status int, resp string) (*httptest.Server, *gwReq) {
	t.Helper()
	var got gwReq
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = gwReq{r.Method, r.URL.String(), r.Header.Clone(), string(b)}
		mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestHTTPDefaultJSONTemplate(t *testing.T) {
	srv, got := gateway(t, 200, `{"id":"abc-1"}`)
	h, err := sms.NewHTTP(sms.HTTPConfig{URL: srv.URL, APIKey: "k3y", From: "CallGo"})
	require.NoError(t, err)
	ref, err := h.Send(context.Background(), "+97699112233", "Сайн уу \"quote\"\nline2")
	require.NoError(t, err)
	require.Equal(t, "abc-1", ref)
	require.Equal(t, "POST", got.method)
	require.Equal(t, "application/json", got.header.Get("Content-Type"))
	require.Equal(t, "Bearer k3y", got.header.Get("Authorization"))
	var body map[string]string
	require.NoError(t, json.Unmarshal([]byte(got.body), &body))
	require.Equal(t, map[string]string{"to": "+97699112233", "from": "CallGo", "text": "Сайн уу \"quote\"\nline2"}, body)
}

func TestHTTPFormTemplateAndCustomAuthHeader(t *testing.T) {
	srv, got := gateway(t, 202, `{"message_id": 4711}`)
	h, err := sms.NewHTTP(sms.HTTPConfig{
		URL: srv.URL, APIKey: "k3y", From: "1400", AuthHeader: "X-API-Key",
		BodyTemplate: "to={{urlquery .To}}&from={{urlquery .From}}&text={{urlquery .Body}}",
	})
	require.NoError(t, err)
	ref, err := h.Send(context.Background(), "+97699112233", "a&b c")
	require.NoError(t, err)
	require.Equal(t, "4711", ref)
	require.Equal(t, "application/x-www-form-urlencoded", got.header.Get("Content-Type"))
	require.Equal(t, "k3y", got.header.Get("X-API-Key"))
	require.Empty(t, got.header.Get("Authorization"))
	require.Equal(t, "to=%2B97699112233&from=1400&text=a%26b+c", got.body)
}

func TestHTTPGetWithURLTemplate(t *testing.T) {
	srv, got := gateway(t, 200, `ok`)
	h, err := sms.NewHTTP(sms.HTTPConfig{
		URL: srv.URL + "/send?key={{urlquery .APIKey}}&to={{urlquery .To}}&text={{urlquery .Body}}", APIKey: "k y", Method: "get", AuthHeader: "-",
	})
	require.NoError(t, err)
	ref, err := h.Send(context.Background(), "+97699112233", "hello world")
	require.NoError(t, err)
	require.Empty(t, ref)
	require.Equal(t, "GET", got.method)
	require.Equal(t, "/send?key=k+y&to=%2B97699112233&text=hello+world", got.url)
	require.Empty(t, got.body)
	require.Empty(t, got.header.Get("Authorization"))
}

func TestHTTPErrorStatus(t *testing.T) {
	srv, _ := gateway(t, 400, `{"error":"bad number"}`)
	h, err := sms.NewHTTP(sms.HTTPConfig{URL: srv.URL})
	require.NoError(t, err)
	_, err = h.Send(context.Background(), "+97699112233", "x")
	require.ErrorContains(t, err, "http 400")
	require.ErrorContains(t, err, "bad number")
}

func TestHTTPTransportErrorRedactsKey(t *testing.T) {
	srv, _ := gateway(t, 200, "")
	url := srv.URL + "/send?key=TOPSECRET"
	srv.Close()
	h, err := sms.NewHTTP(sms.HTTPConfig{URL: url, APIKey: "TOPSECRET"})
	require.NoError(t, err)
	_, err = h.Send(context.Background(), "+97699112233", "x")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "TOPSECRET")
}

func TestNewHTTPValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  sms.HTTPConfig
		ok   bool
	}{
		{"https", sms.HTTPConfig{URL: "https://gw.example/send"}, true},
		{"localhost http", sms.HTTPConfig{URL: "http://localhost:8080/x"}, true},
		{"plain http", sms.HTTPConfig{URL: "http://gw.example/send"}, false},
		{"empty", sms.HTTPConfig{}, false},
		{"ftp", sms.HTTPConfig{URL: "ftp://gw.example"}, false},
		{"bad method", sms.HTTPConfig{URL: "https://gw.example", Method: "PATCH"}, false},
		{"bad template", sms.HTTPConfig{URL: "https://gw.example", BodyTemplate: "{{.To"}, false},
		{"unknown field", sms.HTTPConfig{URL: "https://gw.example", BodyTemplate: "{{.Nope}}"}, true}, // fails at send time
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sms.NewHTTP(tc.cfg)
			if tc.ok {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, domain.ErrInvalid)
			}
		})
	}
	require.ErrorIs(t, sms.ValidateBodyTemplate("{{.Nope}}"), domain.ErrInvalid)
	require.NoError(t, sms.ValidateBodyTemplate(""))
}

// --- Service ---

type fakeMeter struct {
	mu    sync.Mutex
	calls []*uuid.UUID
}

func (m *fakeMeter) RecordSMS(_ context.Context, _ uuid.UUID, callID *uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, callID)
	return nil
}

type fakeEnt struct{ features map[string]bool }

func (e fakeEnt) CanStartCall(context.Context, uuid.UUID) (bool, string, error) { return true, "", nil }
func (e fakeEnt) HasFeature(_ context.Context, _ uuid.UUID, f string) (bool, error) {
	return e.features[f], nil
}
func (e fakeEnt) Limits(context.Context, uuid.UUID) (domain.Plan, error) { return domain.Plan{}, nil }

func xorCrypto() sms.Crypto {
	x := func(b []byte) ([]byte, error) {
		out := make([]byte, len(b))
		for i := range b {
			out[i] = b[i] ^ 0x5a
		}
		return out, nil
	}
	return sms.Crypto{Encrypt: x, Decrypt: x}
}

type svcRig struct {
	svc      *sms.Service
	repo     *webhookstest.MemRepo
	meter    *fakeMeter
	mock     *sms.Mock
	org      uuid.UUID
	settings map[string]any
}

func newSvcRig(t *testing.T, features map[string]bool) *svcRig {
	t.Helper()
	r := &svcRig{repo: webhookstest.New(), meter: &fakeMeter{}, mock: sms.NewMock(), org: uuid.New(),
		settings: map[string]any{"sms": map[string]any{"provider": "mock"}, "other": "keep"}}
	c := xorCrypto()
	r.svc = sms.New(r.repo,
		sms.SettingsLoaderFunc(func(context.Context, uuid.UUID) (map[string]any, error) { return r.settings, nil }),
		sms.DefaultFactory(c.Decrypt, sms.WithMock(r.mock)), r.meter, fakeEnt{features}, zerolog.Nop(), sms.WithCrypto(c))
	return r
}

func TestServiceSend(t *testing.T) {
	r := newSvcRig(t, map[string]bool{"sms": true})
	callID := uuid.New()
	msg, err := r.svc.Send(context.Background(), r.org, &callID, "9911 2233", "  Сайн байна уу  ")
	require.NoError(t, err)
	require.Equal(t, "+97699112233", msg.To)
	require.Equal(t, "Сайн байна уу", msg.Body)
	require.Equal(t, "sent", msg.Status)
	require.Equal(t, "mock", msg.Provider)
	require.Equal(t, "mock-1", msg.ProviderRef)
	require.NotNil(t, msg.SentAt)
	require.Equal(t, []sms.SentMessage{{To: "+97699112233", Body: "Сайн байна уу"}}, r.mock.Sent())
	require.Len(t, r.meter.calls, 1)
	require.Equal(t, callID, *r.meter.calls[0])

	stored := r.repo.SMSMessages()
	require.Len(t, stored, 1)
	require.Equal(t, "sent", stored[0].Status)
	items, total, err := r.repo.ListSMS(context.Background(), r.org, 10, 0)
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Equal(t, msg.ID, items[0].ID)
}

func TestServiceSendErrors(t *testing.T) {
	ctx := context.Background()
	r := newSvcRig(t, map[string]bool{"sms": true})

	_, err := r.svc.Send(ctx, r.org, nil, "not-a-number", "x")
	require.ErrorIs(t, err, domain.ErrInvalid)
	_, err = r.svc.Send(ctx, r.org, nil, "99112233", "   ")
	require.ErrorIs(t, err, domain.ErrInvalid)
	_, err = r.svc.Send(ctx, r.org, nil, "99112233", string(make([]rune, sms.MaxBodyRunes+1)))
	require.ErrorIs(t, err, domain.ErrInvalid)
	require.Empty(t, r.repo.SMSMessages())

	// Gateway failure: message stored as failed, not metered.
	r.mock.Err = errors.New("gateway down")
	msg, err := r.svc.Send(ctx, r.org, nil, "99112233", "x")
	require.ErrorIs(t, err, sms.ErrSendFailed)
	require.NotNil(t, msg)
	require.Equal(t, "failed", msg.Status)
	require.Contains(t, msg.Error, "gateway down")
	require.Equal(t, "failed", r.repo.SMSMessages()[0].Status)
	require.Empty(t, r.meter.calls)

	// Not configured.
	r.settings = map[string]any{}
	_, err = r.svc.Send(ctx, r.org, nil, "99112233", "x")
	require.ErrorIs(t, err, sms.ErrNotConfigured)

	// Feature off.
	off := newSvcRig(t, map[string]bool{})
	_, err = off.svc.Send(ctx, off.org, nil, "99112233", "x")
	require.ErrorIs(t, err, sms.ErrFeatureUnavailable)
	require.Empty(t, off.mock.Sent())
}

func TestServiceConfigRoundTrip(t *testing.T) {
	r := newSvcRig(t, map[string]bool{"sms": true})

	require.Equal(t, sms.SMSConfig{}, r.svc.Config(nil))
	require.Equal(t, sms.SMSConfig{Provider: "mock", Configured: true}, r.svc.Config(r.settings))

	key := "s3cr3t-key"
	frag, err := r.svc.SetConfig(r.settings, sms.ConfigInput{
		Provider: "http", URL: "https://gw.example/send", APIKey: &key, From: "CallGo",
		BodyTemplate: `{"t":{{json .To}}}`, Method: "post", AuthHeader: "X-API-Key",
	})
	require.NoError(t, err)
	raw := frag["sms"].(map[string]any)
	require.NotEqual(t, key, raw["apiKey"])
	require.NotContains(t, raw["apiKey"], key)
	require.Equal(t, "POST", raw["method"])

	settings := map[string]any{"sms": raw}
	cfg := r.svc.Config(settings)
	require.Equal(t, sms.SMSConfig{Provider: "http", URL: "https://gw.example/send", From: "CallGo", Method: "POST",
		BodyTemplate: `{"t":{{json .To}}}`, AuthHeader: "X-API-Key", HasAPIKey: true, Configured: true}, cfg)

	// The factory decrypts the key and uses the gateway.
	srv, got := gateway(t, 200, `{"id":"z"}`)
	frag2, err := r.svc.SetConfig(settings, sms.ConfigInput{Provider: "http", URL: srv.URL, From: "CG", AuthHeader: "X-API-Key"}) // nil key = keep
	require.NoError(t, err)
	settings = map[string]any{"sms": frag2["sms"]}
	require.True(t, r.svc.Config(settings).HasAPIKey)
	sender, err := sms.DefaultFactory(xorCrypto().Decrypt)(settings)
	require.NoError(t, err)
	ref, err := sender.Send(context.Background(), "+97699112233", "hey")
	require.NoError(t, err)
	require.Equal(t, "z", ref)
	require.Equal(t, key, got.header.Get("X-API-Key"))

	// Empty key clears it.
	empty := ""
	frag3, err := r.svc.SetConfig(settings, sms.ConfigInput{Provider: "http", URL: srv.URL, APIKey: &empty})
	require.NoError(t, err)
	require.False(t, r.svc.Config(map[string]any{"sms": frag3["sms"]}).HasAPIKey)
}

func TestServiceSetConfigValidation(t *testing.T) {
	r := newSvcRig(t, nil)
	key := "k"
	for _, in := range []sms.ConfigInput{
		{Provider: "twilio"},
		{Provider: "http"},
		{Provider: "http", URL: "http://gw.example"},
		{Provider: "http", URL: "https://gw.example", BodyTemplate: "{{"},
		{Provider: "http", URL: "https://gw.example", Method: "PATCH"},
		{Provider: "http", URL: "https://gw.example", AuthHeader: "bad header"},
		{Provider: "mock", From: string(make([]rune, 40))},
	} {
		_, err := r.svc.SetConfig(nil, in)
		require.ErrorIs(t, err, domain.ErrInvalid, "%+v", in)
	}
	// Storing a key without a cipher is refused.
	nocrypto := sms.New(r.repo, nil, nil, nil, nil, zerolog.Nop())
	_, err := nocrypto.SetConfig(nil, sms.ConfigInput{Provider: "http", URL: "https://gw.example", APIKey: &key})
	require.Error(t, err)
	// Mock needs nothing else.
	frag, err := r.svc.SetConfig(nil, sms.ConfigInput{Provider: "MOCK", From: "x"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"sms": map[string]any{"provider": "mock", "from": "x"}}, frag)
}
