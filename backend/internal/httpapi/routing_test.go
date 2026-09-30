package httpapi

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func (w *rtWorld) routingHandler() http.Handler {
	d := &RoutingDeps{Numbers: w.numbers, Profiles: w.profiles, Orgs: w.orgs, Audit: w.audit, Now: func() time.Time { return rtNow }}
	return w.rtRouter(func(r chi.Router) { mountRouting(r, d, Config{}, rtLog()) })
}

func TestRoutingResolve(t *testing.T) {
	w := newRTWorld()
	n := w.number
	n.Routing = domain.RoutingConfig{
		BusinessHours:     domain.CampaignSchedule{StartTime: "09:00", EndTime: "18:00"},
		AfterHoursMessage: "Closed, call tomorrow",
	}
	w.numbers.items[n.ID] = n
	h := w.routingHandler()
	path := "/api/sip-numbers/" + n.ID.String() + "/routing/resolve"

	type out struct {
		Route domain.ResolvedRoute `json:"route"`
	}
	var got out

	// Default time: 11:00 in Ulaanbaatar, open.
	res := rtDo(t, h, "operator", http.MethodPost, path, nil)
	require.Equal(t, http.StatusOK, res.Code, string(res.Raw))
	res.decode(t, &got)
	assert.Equal(t, "direct", got.Route.Mode)
	require.NotNil(t, got.Route.AgentProfileID)
	assert.Equal(t, w.profile.ID, *got.Route.AgentProfileID)

	// 20:00 local (12:00 UTC): closed.
	at := url.QueryEscape("2026-09-30T12:00:00Z")
	res = rtDo(t, h, "operator", http.MethodPost, path+"?at="+at, nil)
	require.Equal(t, http.StatusOK, res.Code, string(res.Raw))
	got = out{}
	res.decode(t, &got)
	assert.Equal(t, "after_hours", got.Route.Mode)
	assert.Equal(t, "Closed, call tomorrow", got.Route.Message)
	assert.Nil(t, got.Route.AgentProfileID)

	// The same instant with an offset: 20:00+08:00 = 12:00Z.
	res = rtDo(t, h, "operator", http.MethodPost, path+"?at="+url.QueryEscape("2026-09-30T20:00:00+08:00"), nil)
	got = out{}
	res.decode(t, &got)
	assert.Equal(t, "after_hours", got.Route.Mode)

	res = rtDo(t, h, "operator", http.MethodPost, path+"?at=yesterday", nil)
	assert.Equal(t, http.StatusBadRequest, res.Code)
	assert.Equal(t, "invalid", res.errCode(t))
}

func TestRoutingResolveMenuAndTimezone(t *testing.T) {
	w := newRTWorld()
	n := w.number
	n.Routing = domain.RoutingConfig{
		BusinessHours:     domain.CampaignSchedule{StartTime: "09:00", EndTime: "18:00"},
		AfterHoursMessage: "closed",
		MenuPrompt:        "Press 1 for sales",
		Menu:              []domain.MenuOption{{Key: "1", Label: "Sales", AgentProfileID: w.profile.ID}},
	}
	w.numbers.items[n.ID] = n
	h := w.routingHandler()
	path := "/api/sip-numbers/" + n.ID.String() + "/routing/resolve"

	var got struct {
		Route domain.ResolvedRoute `json:"route"`
	}
	res := rtDo(t, h, "admin", http.MethodPost, path, nil)
	require.Equal(t, http.StatusOK, res.Code)
	res.decode(t, &got)
	assert.Equal(t, "menu", got.Route.Mode)
	assert.Equal(t, "Press 1 for sales", got.Route.MenuPrompt)
	assert.Equal(t, 8, got.Route.MenuTimeoutSec)
	assert.Equal(t, 1, got.Route.MenuRepeat)
	require.Len(t, got.Route.Menu, 1)

	// The org zone drives the hours: 03:00 UTC is 23:00 in New York → closed.
	w.orgs.orgs[w.org.ID] = domain.Organization{ID: w.org.ID, Timezone: "America/New_York"}
	res = rtDo(t, h, "admin", http.MethodPost, path, nil)
	got.Route = domain.ResolvedRoute{}
	res.decode(t, &got)
	assert.Equal(t, "after_hours", got.Route.Mode)
}

func TestRoutingResolveAccess(t *testing.T) {
	w := newRTWorld()
	h := w.routingHandler()
	path := "/api/sip-numbers/" + w.number.ID.String() + "/routing/resolve"

	assert.Equal(t, http.StatusUnauthorized, rtDo(t, h, "", http.MethodPost, path, nil).Code)
	res := rtDo(t, h, "foreign", http.MethodPost, path, nil)
	assert.Equal(t, http.StatusNotFound, res.Code, "another org's number is invisible")
	assert.Equal(t, http.StatusNotFound, rtDo(t, h, "admin", http.MethodPost, "/api/sip-numbers/"+uuid.NewString()+"/routing/resolve", nil).Code)
	assert.Equal(t, http.StatusBadRequest, rtDo(t, h, "admin", http.MethodPost, "/api/sip-numbers/nope/routing/resolve", nil).Code)
}

func TestRoutingPut(t *testing.T) {
	w := newRTWorld()
	h := w.routingHandler()
	path := "/api/sip-numbers/" + w.number.ID.String() + "/routing"
	body := map[string]any{
		"businessHours":       map[string]any{"startTime": "09:00", "endTime": "18:00", "weekdays": []int{1, 2, 3, 4, 5}},
		"afterHoursProfileId": w.profile.ID.String(),
		"menuPrompt":          "Press 1",
		"menu":                []map[string]any{{"key": "1", "label": "Sales", "agentProfileId": w.profile.ID.String()}},
	}

	res := rtDo(t, h, "admin", http.MethodPut, path, body)
	require.Equal(t, http.StatusOK, res.Code, string(res.Raw))
	var out struct {
		SIPNumber domain.SIPNumber `json:"sipNumber"`
	}
	res.decode(t, &out)
	assert.Equal(t, w.number.ID, out.SIPNumber.ID)
	stored := w.numbers.get(w.number.ID)
	assert.Equal(t, "09:00", stored.Routing.BusinessHours.StartTime)
	assert.Equal(t, 8, stored.Routing.MenuTimeoutSec, "defaults applied")
	assert.Equal(t, 1, stored.Routing.MenuRepeat)
	require.Len(t, stored.Routing.Menu, 1)
	assert.Equal(t, w.number.Number, stored.Number, "only routing changes")
	assert.Equal(t, w.number.AgentProfileID, stored.AgentProfileID)
	assert.Equal(t, []string{"sip_number.routing.update"}, w.audit.actions())

	// `{}` clears the routing.
	res = rtDo(t, h, "admin", http.MethodPut, path, `{}`)
	require.Equal(t, http.StatusOK, res.Code, string(res.Raw))
	assert.True(t, w.numbers.get(w.number.ID).Routing.IsZero())
}

func TestRoutingPutRejects(t *testing.T) {
	w := newRTWorld()
	h := w.routingHandler()
	path := "/api/sip-numbers/" + w.number.ID.String() + "/routing"
	menu := func(key string, profile uuid.UUID) map[string]any {
		return map[string]any{"menuPrompt": "p", "menu": []map[string]any{{"key": key, "label": "x", "agentProfileId": profile.String()}}}
	}

	tests := []struct {
		name string
		body any
	}{
		{"bad key", menu("a", w.profile.ID)},
		{"foreign profile", menu("1", w.profile2.ID)},
		{"unknown profile", menu("1", uuid.New())},
		{"duplicate keys", map[string]any{"menuPrompt": "p", "menu": []map[string]any{
			{"key": "1", "agentProfileId": w.profile.ID.String()}, {"key": "1", "agentProfileId": w.profile.ID.String()}}}},
		{"timeout too small", map[string]any{"menuPrompt": "p", "menuTimeoutSec": 2, "menu": menu("1", w.profile.ID)["menu"]}},
		{"repeat too big", map[string]any{"menuPrompt": "p", "menuRepeat": 4, "menu": menu("1", w.profile.ID)["menu"]}},
		{"hours without after-hours", map[string]any{"businessHours": map[string]any{"startTime": "09:00", "endTime": "18:00"}}},
		{"foreign after-hours profile", map[string]any{"afterHoursProfileId": w.profile2.ID.String()}},
		{"malformed json", `{"menu": [`},
		{"empty body", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := rtDo(t, h, "admin", http.MethodPut, path, tc.body)
			assert.Equal(t, http.StatusBadRequest, res.Code, string(res.Raw))
			assert.Equal(t, "invalid", res.errCode(t))
		})
	}
	assert.True(t, w.numbers.get(w.number.ID).Routing.IsZero(), "nothing stored")
	assert.Empty(t, w.audit.actions())

	ok := map[string]any{"afterHoursMessage": "x"}
	assert.Equal(t, http.StatusForbidden, rtDo(t, h, "operator", http.MethodPut, path, ok).Code, "operators cannot change routing")
	assert.Equal(t, http.StatusUnauthorized, rtDo(t, h, "", http.MethodPut, path, ok).Code)
	assert.Equal(t, http.StatusNotFound, rtDo(t, h, "foreign", http.MethodPut, path, ok).Code)
}

func TestRoutingValidateRoutingBody(t *testing.T) {
	w := newRTWorld()
	ctx := t.Context()

	cfg, err := ValidateRoutingBody(ctx, w.profiles, w.org.ID, domain.RoutingConfig{
		MenuPrompt: " Press 1 ", Menu: []domain.MenuOption{{Key: "1", AgentProfileID: w.profile.ID}},
	})
	require.NoError(t, err)
	assert.Equal(t, "Press 1", cfg.MenuPrompt)
	assert.Equal(t, 8, cfg.MenuTimeoutSec)

	_, err = ValidateRoutingBody(ctx, w.profiles, w.org.ID, domain.RoutingConfig{
		MenuPrompt: "p", Menu: []domain.MenuOption{{Key: "1", AgentProfileID: w.profile2.ID}},
	})
	var ae *apiError
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, http.StatusBadRequest, ae.status)
	assert.NotContains(t, ae.message, "invalid input")
}

// The routing routes are registered with full paths so they can live next to
// the existing r.Route("/sip-numbers", …) subrouter.
func TestRoutingCoexistsWithSIPNumbersRoute(t *testing.T) {
	w := newRTWorld()
	d := &RoutingDeps{Numbers: w.numbers, Profiles: w.profiles, Orgs: w.orgs, Now: func() time.Time { return rtNow }}
	for _, mountFirst := range []bool{true, false} {
		h := w.rtRouter(func(r chi.Router) {
			sip := func() {
				r.Route("/sip-numbers", func(r chi.Router) {
					r.Get("/{id}", func(rw http.ResponseWriter, _ *http.Request) {
						writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
					})
				})
			}
			if mountFirst {
				mountRouting(r, d, Config{}, rtLog())
				sip()
			} else {
				sip()
				mountRouting(r, d, Config{}, rtLog())
			}
		})
		id := w.number.ID.String()
		assert.Equal(t, http.StatusOK, rtDo(t, h, "admin", http.MethodGet, "/api/sip-numbers/"+id, nil).Code)
		assert.Equal(t, http.StatusOK, rtDo(t, h, "admin", http.MethodPost, "/api/sip-numbers/"+id+"/routing/resolve", nil).Code)
		assert.Equal(t, http.StatusOK, rtDo(t, h, "admin", http.MethodPut, "/api/sip-numbers/"+id+"/routing", `{}`).Code)
	}
}
