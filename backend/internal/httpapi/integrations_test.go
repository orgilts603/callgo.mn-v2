package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/webhooks"
)

func integDo(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func integDecode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &v), w.Body.String())
	return v
}

func integErr(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	require.Equal(t, status, w.Code, w.Body.String())
	var b struct {
		Error struct{ Code string } `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &b), w.Body.String())
	require.Equal(t, code, b.Error.Code)
}

type integWebhookResp struct {
	Webhook struct {
		ID          uuid.UUID `json:"id"`
		URL         string    `json:"url"`
		SecretHint  string    `json:"secretHint"`
		Events      []string  `json:"events"`
		Active      bool      `json:"active"`
		Description string    `json:"description"`
	} `json:"webhook"`
	Secret string `json:"secret"`
}

func TestIntegrationsAuthAndFeatureGates(t *testing.T) {
	e := newIntegEnv(t)
	routes := []struct{ method, path string }{
		{"GET", "/webhooks"}, {"POST", "/webhooks"}, {"PUT", "/webhooks/" + uuid.NewString()},
		{"DELETE", "/webhooks/" + uuid.NewString()}, {"POST", "/webhooks/" + uuid.NewString() + "/test"},
		{"POST", "/webhooks/" + uuid.NewString() + "/rotate-secret"}, {"GET", "/webhooks/" + uuid.NewString() + "/deliveries"},
		{"POST", "/webhook-deliveries/" + uuid.NewString() + "/retry"},
		{"GET", "/sms/config"}, {"PUT", "/sms/config"}, {"POST", "/sms/send"}, {"GET", "/sms"},
	}
	for _, rt := range routes {
		integErr(t, e.do(rt.method, rt.path, "", nil), 401, "unauthorized")
	}
	// Operators may not manage webhooks or SMS config, but can send / list SMS.
	for _, rt := range routes[:9] {
		integErr(t, e.do(rt.method, rt.path, e.opTok, map[string]any{}), 403, "forbidden")
	}
	require.Equal(t, 200, e.do("GET", "/sms", e.opTok, nil).Code)

	e.features["webhooks"], e.features["sms"] = false, false
	for _, rt := range routes {
		integErr(t, e.do(rt.method, rt.path, e.adminTok, map[string]any{}), 403, "feature_unavailable")
	}
}

func TestIntegrationsWebhookCRUD(t *testing.T) {
	e := newIntegEnv(t)

	// Empty list.
	w := e.do("GET", "/webhooks", e.adminTok, nil)
	require.Equal(t, 200, w.Code)
	require.JSONEq(t, `{"items":[]}`, w.Body.String())

	// Create.
	w = e.do("POST", "/webhooks", e.adminTok, map[string]any{
		"url": "https://hooks.example.com/callgo", "events": []string{"call.ended", "call.ended", " call.started "}, "description": "  CRM  ",
	})
	require.Equal(t, 201, w.Code, w.Body.String())
	created := integDecode[integWebhookResp](t, w)
	require.Regexp(t, `^whsec_[0-9a-f]{64}$`, created.Secret)
	require.Equal(t, "whsec_…"+created.Secret[len(created.Secret)-4:], created.Webhook.SecretHint)
	require.Equal(t, []string{"call.ended", "call.started"}, created.Webhook.Events)
	require.True(t, created.Webhook.Active)
	require.Equal(t, "CRM", created.Webhook.Description)
	require.NotContains(t, w.Body.String(), `"Secret"`)
	stored, err := e.repo.GetWebhook(context.Background(), created.Webhook.ID)
	require.NoError(t, err)
	require.Equal(t, created.Secret, stored.Secret)
	require.Equal(t, e.org.ID, stored.OrgID)

	// List never leaks secrets.
	w = e.do("GET", "/webhooks", e.adminTok, nil)
	require.NotContains(t, w.Body.String(), created.Secret)
	items := integDecode[struct{ Items []domain.Webhook }](t, w).Items
	require.Len(t, items, 1)

	// Update: partial, keeps the rest.
	id := created.Webhook.ID.String()
	w = e.do("PUT", "/webhooks/"+id, e.adminTok, map[string]any{"events": []string{"*"}, "active": false})
	require.Equal(t, 200, w.Code, w.Body.String())
	upd := integDecode[integWebhookResp](t, w)
	require.Equal(t, []string{"*"}, upd.Webhook.Events)
	require.False(t, upd.Webhook.Active)
	require.Equal(t, "https://hooks.example.com/callgo", upd.Webhook.URL)
	require.Empty(t, upd.Secret)

	// Re-enabling resets the failure counter.
	stored, _ = e.repo.GetWebhook(context.Background(), created.Webhook.ID)
	stored.FailureCount = 20
	require.NoError(t, e.repo.UpdateWebhook(context.Background(), stored))
	w = e.do("PUT", "/webhooks/"+id, e.adminTok, map[string]any{"active": true, "url": "http://localhost:9000/x"})
	require.Equal(t, 200, w.Code, w.Body.String())
	stored, _ = e.repo.GetWebhook(context.Background(), created.Webhook.ID)
	require.True(t, stored.Active)
	require.Equal(t, 0, stored.FailureCount)
	require.Equal(t, "http://localhost:9000/x", stored.URL)

	// Rotate secret.
	w = e.do("POST", "/webhooks/"+id+"/rotate-secret", e.adminTok, nil)
	require.Equal(t, 200, w.Code)
	rot := integDecode[struct{ Secret string }](t, w)
	require.NotEqual(t, created.Secret, rot.Secret)
	stored, _ = e.repo.GetWebhook(context.Background(), created.Webhook.ID)
	require.Equal(t, rot.Secret, stored.Secret)
	require.Equal(t, webhooks.SecretHint(rot.Secret), stored.SecretHint)

	// Delete.
	require.Equal(t, 204, e.do("DELETE", "/webhooks/"+id, e.adminTok, nil).Code)
	integErr(t, e.do("DELETE", "/webhooks/"+id, e.adminTok, nil), 404, "not_found")

	require.Equal(t, []string{"webhook.create", "webhook.update", "webhook.update", "webhook.rotate_secret", "webhook.delete"}, e.audit.actions())
	for _, a := range e.audit.entries {
		require.Equal(t, e.org.ID, a.OrgID)
		require.NotNil(t, a.ActorID)
	}
}

func TestIntegrationsWebhookValidation(t *testing.T) {
	e := newIntegEnv(t)
	post := func(body map[string]any) *httptest.ResponseRecorder {
		return e.do("POST", "/webhooks", e.adminTok, body)
	}
	ok := []string{"call.ended"}

	for name, url := range map[string]string{
		"empty":       "",
		"http remote": "http://hooks.example.com/x",
		"ftp":         "ftp://hooks.example.com/x",
		"no host":     "https:///path",
		"credentials": "https://u:p@hooks.example.com/x",
		"private ip":  "https://10.0.0.5/x",
		"metadata ip": "https://169.254.169.254/latest",
		"garbage":     "not a url",
		"too long":    "https://a.example/" + strings.Repeat("x", 2100),
	} {
		integErr(t, post(map[string]any{"url": url, "events": ok}), 400, "invalid")
		_ = name
	}
	for _, url := range []string{"https://hooks.example.com/x", "http://localhost:8080/x", "http://127.0.0.1:8080/x", "https://8.8.8.8/x"} {
		w := post(map[string]any{"url": url, "events": ok})
		require.Equal(t, 201, w.Code, url+" "+w.Body.String())
	}

	url := "https://hooks.example.com/x"
	integErr(t, post(map[string]any{"url": url}), 400, "invalid")
	integErr(t, post(map[string]any{"url": url, "events": []string{}}), 400, "invalid")
	integErr(t, post(map[string]any{"url": url, "events": []string{"call.exploded"}}), 400, "invalid")
	integErr(t, post(map[string]any{"url": url, "events": []string{"transcript.partial"}}), 400, "invalid")
	integErr(t, post(map[string]any{"url": url, "events": ok, "description": strings.Repeat("d", 501)}), 400, "invalid")
	for _, ev := range append(webhooks.KnownEvents(), "*") {
		require.Equal(t, 201, post(map[string]any{"url": url, "events": []string{string(ev)}}).Code, string(ev))
	}
	req := httptest.NewRequest("POST", "/webhooks", strings.NewReader("{bad"))
	req.Header.Set("Authorization", "Bearer "+e.adminTok)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	integErr(t, rec, 400, "invalid")

	// Update validation.
	hook := e.seedHook(e.org.ID, url, "*")
	integErr(t, e.do("PUT", "/webhooks/"+hook.ID.String(), e.adminTok, map[string]any{"url": "http://evil.example"}), 400, "invalid")
	integErr(t, e.do("PUT", "/webhooks/"+hook.ID.String(), e.adminTok, map[string]any{"events": []string{}}), 400, "invalid")
	integErr(t, e.do("PUT", "/webhooks/not-a-uuid", e.adminTok, map[string]any{}), 400, "invalid")
}

func TestIntegrationsWebhookTenantIsolation(t *testing.T) {
	e := newIntegEnv(t)
	mine := e.seedHook(e.org.ID, "https://a.example/x", "*")
	theirs := e.seedHook(e.otherOrg.ID, "https://b.example/x", "*")
	dl := domain.WebhookDelivery{ID: uuid.New(), WebhookID: theirs.ID, EventType: domain.EventCallEnded, Status: "failed"}
	require.NoError(t, e.repo.CreateDelivery(context.Background(), &dl))

	items := integDecode[struct{ Items []domain.Webhook }](t, e.do("GET", "/webhooks", e.adminTok, nil)).Items
	require.Len(t, items, 1)
	require.Equal(t, mine.ID, items[0].ID)

	tid := theirs.ID.String()
	integErr(t, e.do("PUT", "/webhooks/"+tid, e.adminTok, map[string]any{"active": false}), 404, "not_found")
	integErr(t, e.do("DELETE", "/webhooks/"+tid, e.adminTok, nil), 404, "not_found")
	integErr(t, e.do("POST", "/webhooks/"+tid+"/test", e.adminTok, nil), 404, "not_found")
	integErr(t, e.do("POST", "/webhooks/"+tid+"/rotate-secret", e.adminTok, nil), 404, "not_found")
	integErr(t, e.do("GET", "/webhooks/"+tid+"/deliveries", e.adminTok, nil), 404, "not_found")
	integErr(t, e.do("POST", "/webhook-deliveries/"+dl.ID.String()+"/retry", e.adminTok, nil), 404, "not_found")
	still, _ := e.repo.GetWebhook(context.Background(), theirs.ID)
	require.True(t, still.Active)
	require.Empty(t, e.audit.actions())
}

func TestIntegrationsWebhookTestEndpoint(t *testing.T) {
	e := newIntegEnv(t)
	var mu sync.Mutex
	var gotHdr http.Header
	var gotBody []byte
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotHdr = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer target.Close()
	hook := e.seedHook(e.org.ID, target.URL, "call.ended")

	w := e.do("POST", "/webhooks/"+hook.ID.String()+"/test", e.adminTok, nil)
	require.Equal(t, 200, w.Code, w.Body.String())
	res := integDecode[struct{ Delivery domain.WebhookDelivery }](t, w)
	require.Equal(t, "delivered", res.Delivery.Status)
	require.Equal(t, domain.EventSystem, res.Delivery.EventType)
	require.Equal(t, 200, res.Delivery.ResponseCode)
	mu.Lock()
	require.Equal(t, "system", gotHdr.Get(webhooks.HeaderEvent))
	require.True(t, webhooks.Verify("whsec_seed", gotHdr.Get(webhooks.HeaderTimestamp), gotBody, gotHdr.Get(webhooks.HeaderSignature)))
	mu.Unlock()

	// A failing endpoint is reported through the delivery, not as an HTTP error.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", 500) }))
	defer bad.Close()
	hook2 := e.seedHook(e.org.ID, bad.URL, "*")
	w = e.do("POST", "/webhooks/"+hook2.ID.String()+"/test", e.adminTok, nil)
	require.Equal(t, 200, w.Code)
	res = integDecode[struct{ Delivery domain.WebhookDelivery }](t, w)
	require.Equal(t, "failed", res.Delivery.Status)
	require.Equal(t, 500, res.Delivery.ResponseCode)
}

func TestIntegrationsDeliveriesAndRetry(t *testing.T) {
	e := newIntegEnv(t)
	hook := e.seedHook(e.org.ID, "https://a.example/x", "*")
	var ids []uuid.UUID
	for i := 0; i < 3; i++ {
		d := domain.WebhookDelivery{ID: uuid.New(), WebhookID: hook.ID, EventID: uuid.NewString(), EventType: domain.EventCallEnded, Status: "failed", Attempts: 6, LastError: "http 500"}
		require.NoError(t, e.repo.CreateDelivery(context.Background(), &d))
		ids = append(ids, d.ID)
	}
	w := e.do("GET", "/webhooks/"+hook.ID.String()+"/deliveries?limit=2&offset=0", e.adminTok, nil)
	require.Equal(t, 200, w.Code)
	page := integDecode[struct {
		Items []domain.WebhookDelivery
		Total int
	}](t, w)
	require.Equal(t, 3, page.Total)
	require.Len(t, page.Items, 2)
	require.Equal(t, ids[2], page.Items[0].ID) // newest first
	require.NotContains(t, w.Body.String(), "payload")

	integErr(t, e.do("GET", "/webhooks/"+hook.ID.String()+"/deliveries?limit=0", e.adminTok, nil), 400, "invalid")
	integErr(t, e.do("GET", "/webhooks/"+hook.ID.String()+"/deliveries?limit=abc", e.adminTok, nil), 400, "invalid")

	w = e.do("POST", "/webhook-deliveries/"+ids[0].String()+"/retry", e.adminTok, nil)
	require.Equal(t, 200, w.Code, w.Body.String())
	got := integDecode[struct{ Delivery domain.WebhookDelivery }](t, w)
	require.Equal(t, ids[0], got.Delivery.ID)
	require.Equal(t, "pending", got.Delivery.Status)
	require.Equal(t, 0, got.Delivery.Attempts)
	require.NotNil(t, got.Delivery.NextTryAt)
	for _, d := range e.repo.Deliveries() {
		if d.ID == ids[0] {
			require.Equal(t, "pending", d.Status)
		} else {
			require.Equal(t, "failed", d.Status)
		}
	}

	integErr(t, e.do("POST", "/webhook-deliveries/"+uuid.NewString()+"/retry", e.adminTok, nil), 404, "not_found")
	integErr(t, e.do("POST", "/webhook-deliveries/bad/retry", e.adminTok, nil), 400, "invalid")

	// Disabled webhook: conflict.
	hook.Active = false
	require.NoError(t, e.repo.UpdateWebhook(context.Background(), &hook))
	integErr(t, e.do("POST", "/webhook-deliveries/"+ids[1].String()+"/retry", e.adminTok, nil), 409, "conflict")
}

func TestIntegrationsSMSConfig(t *testing.T) {
	e := newIntegEnv(t)

	w := e.do("GET", "/sms/config", e.adminTok, nil)
	require.Equal(t, 200, w.Code)
	require.False(t, integDecode[map[string]any](t, w)["configured"].(bool))

	// Mock provider.
	w = e.do("PUT", "/sms/config", e.adminTok, map[string]any{"provider": "mock", "from": "CallGo"})
	require.Equal(t, 200, w.Code, w.Body.String())
	cfg := integDecode[map[string]any](t, w)
	require.Equal(t, "mock", cfg["provider"])
	require.Equal(t, "CallGo", cfg["from"])
	require.Equal(t, true, cfg["configured"])

	// HTTP provider with key: encrypted at rest, never returned, other settings kept.
	w = e.do("PUT", "/sms/config", e.adminTok, map[string]any{
		"provider": "http", "url": "https://gw.example/send", "apiKey": "PLAINTEXT-KEY", "from": "1400",
		"bodyTemplate": `{"to":{{json .To}},"text":{{json .Body}}}`,
	})
	require.Equal(t, 200, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), "PLAINTEXT-KEY")
	cfg = integDecode[map[string]any](t, w)
	require.Equal(t, true, cfg["hasApiKey"])
	org, _ := e.orgs.GetOrg(context.Background(), e.org.ID)
	raw := org.Settings["sms"].(map[string]any)
	require.NotEmpty(t, raw["apiKey"])
	require.NotContains(t, raw["apiKey"], "PLAINTEXT-KEY")
	require.Equal(t, true, org.Settings["recordCalls"], "unrelated settings survive")

	// Omitted apiKey keeps the stored one.
	w = e.do("PUT", "/sms/config", e.adminTok, map[string]any{"provider": "http", "url": "https://gw2.example/send"})
	require.Equal(t, 200, w.Code)
	require.Equal(t, true, integDecode[map[string]any](t, w)["hasApiKey"])
	w = e.do("GET", "/sms/config", e.adminTok, nil)
	require.Equal(t, "https://gw2.example/send", integDecode[map[string]any](t, w)["url"])

	// Validation.
	for _, body := range []map[string]any{
		{"provider": "twilio"},
		{"provider": ""},
		{"provider": "http"},
		{"provider": "http", "url": "http://gw.example"},
		{"provider": "http", "url": "https://gw.example", "bodyTemplate": "{{"},
	} {
		integErr(t, e.do("PUT", "/sms/config", e.adminTok, body), 400, "invalid")
	}
	// Other org unaffected.
	other, _ := e.orgs.GetOrg(context.Background(), e.otherOrg.ID)
	require.Nil(t, other.Settings["sms"])
	require.Equal(t, []string{"sms.config.update", "sms.config.update", "sms.config.update"}, e.audit.actions())
	for _, a := range e.audit.entries {
		require.NotContains(t, a.Meta, "apiKey")
	}
}

func TestIntegrationsSMSSendAndList(t *testing.T) {
	e := newIntegEnv(t)

	// Not configured yet.
	integErr(t, e.do("POST", "/sms/send", e.adminTok, map[string]any{"to": "99112233", "body": "hi"}), 409, "conflict")

	require.Equal(t, 200, e.do("PUT", "/sms/config", e.adminTok, map[string]any{"provider": "mock"}).Code)

	w := e.do("POST", "/sms/send", e.opTok, map[string]any{"to": "9911 2233", "body": " Сайн байна уу ", "callId": e.call.ID.String()})
	require.Equal(t, 201, w.Code, w.Body.String())
	msg := integDecode[struct{ Message domain.SMSMessage }](t, w).Message
	require.Equal(t, "+97699112233", msg.To)
	require.Equal(t, "Сайн байна уу", msg.Body)
	require.Equal(t, "sent", msg.Status)
	require.Equal(t, "mock", msg.Provider)
	require.Equal(t, e.call.ID, *msg.CallID)
	require.Equal(t, e.org.ID, msg.OrgID)
	require.Len(t, e.mock.Sent(), 1)

	// Validation.
	integErr(t, e.do("POST", "/sms/send", e.adminTok, map[string]any{"to": "abc", "body": "x"}), 400, "invalid")
	integErr(t, e.do("POST", "/sms/send", e.adminTok, map[string]any{"to": "99112233", "body": "  "}), 400, "invalid")
	integErr(t, e.do("POST", "/sms/send", e.adminTok, map[string]any{"to": "99112233", "body": "x", "callId": "nope"}), 400, "invalid")
	integErr(t, e.do("POST", "/sms/send", e.adminTok, map[string]any{"to": "99112233", "body": "x", "callId": uuid.NewString()}), 400, "invalid")
	integErr(t, e.do("POST", "/sms/send", e.adminTok, map[string]any{"to": "99112233", "body": "x", "callId": e.foreignCall.ID.String()}), 400, "invalid")
	require.Len(t, e.mock.Sent(), 1)

	// Gateway failure surfaces as 502 but is recorded.
	e.mock.Err = context.DeadlineExceeded
	integErr(t, e.do("POST", "/sms/send", e.adminTok, map[string]any{"to": "99112233", "body": "x"}), 502, "bad_gateway")
	e.mock.Err = nil

	// List: scoped to the org, newest first, paginated.
	w = e.do("GET", "/sms?limit=1", e.adminTok, nil)
	require.Equal(t, 200, w.Code)
	page := integDecode[struct {
		Items []domain.SMSMessage
		Total int
	}](t, w)
	require.Equal(t, 2, page.Total)
	require.Len(t, page.Items, 1)
	require.Equal(t, "failed", page.Items[0].Status)
	w = e.do("GET", "/sms", e.othTok, nil)
	require.JSONEq(t, `{"items":[],"total":0}`, w.Body.String())

	require.Contains(t, e.audit.actions(), "sms.send")
	for _, a := range e.audit.entries {
		if a.Action == "sms.send" {
			require.NotContains(t, a.Meta["to"], "99112233")
		}
	}
}
