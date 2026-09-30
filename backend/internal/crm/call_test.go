package crm

import (
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func TestCallsCRUD(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "acme")

	c := &domain.Call{OrgID: org.ID, Direction: domain.DirectionInbound, Status: domain.StatusRinging,
		FromNumber: "+97699112233", ToNumber: "+97677001234", RoomName: "call-abc",
		Metadata: map[string]any{"sipNumberId": "x", "n": 1.5}}
	require.NoError(t, s.CreateCall(ctx, c))
	require.NotEqual(t, uuid.Nil, c.ID)
	require.False(t, c.StartedAt.IsZero(), "started_at defaults to now")

	requireErrIs(t, s.CreateCall(ctx, &domain.Call{OrgID: org.ID, Direction: "inbound", Status: "ringing",
		RoomName: "call-abc"}), domain.ErrConflict)
	requireErrIs(t, s.CreateCall(ctx, &domain.Call{OrgID: org.ID, Direction: "sideways", Status: "ringing"}),
		domain.ErrInvalid)
	// Several calls without a room yet are allowed.
	newCall(t, ctx, s, domain.Call{OrgID: org.ID, RoomName: "", Status: domain.StatusQueued})
	q2 := &domain.Call{OrgID: org.ID, Direction: "outbound", Status: domain.StatusQueued}
	require.NoError(t, s.CreateCall(ctx, q2))

	got, err := s.GetCallByRoom(ctx, "call-abc")
	require.NoError(t, err)
	require.Equal(t, c.ID, got.ID)
	require.Equal(t, "x", got.Metadata["sipNumberId"])
	require.InDelta(t, 1.5, got.Metadata["n"], 1e-9)
	require.WithinDuration(t, c.StartedAt, got.StartedAt, time.Microsecond)
	_, err = s.GetCallByRoom(ctx, "")
	requireErrIs(t, err, domain.ErrNotFound)

	now := time.Now().UTC().Truncate(time.Microsecond)
	contact := &domain.Contact{OrgID: org.ID, Phone: "+97699112233", Name: "Бат"}
	require.NoError(t, s.UpsertContact(ctx, contact))
	c.Status = domain.StatusCompleted
	c.AnsweredAt = &now
	c.EndedAt = ptr(now.Add(42 * time.Second))
	c.DurationSec = 42
	c.Summary = "Захиалга шалгасан"
	c.Sentiment = domain.SentimentPositive
	c.Intent = "order_status"
	c.EndReason = "hangup"
	c.LLMModelUsed = "gpt-4o-mini"
	c.ContactID = &contact.ID
	c.RecordingURL = "https://rec/1.ogg"
	c.Metadata = nil
	require.NoError(t, s.UpdateCall(ctx, c))

	got, err = s.GetCall(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, domain.StatusCompleted, got.Status)
	require.Equal(t, now, got.AnsweredAt.UTC())
	require.Equal(t, 42, got.DurationSec)
	require.Equal(t, domain.SentimentPositive, got.Sentiment)
	require.Equal(t, contact.ID, *got.ContactID)
	require.Equal(t, "https://rec/1.ogg", got.RecordingURL)
	require.Nil(t, got.Metadata)

	byContact, err := s.ListCallsByContact(ctx, contact.ID, 0)
	require.NoError(t, err)
	require.Len(t, byContact, 1)

	_, err = s.GetCall(ctx, uuid.New())
	requireErrIs(t, err, domain.ErrNotFound)
	requireErrIs(t, s.UpdateCall(ctx, &domain.Call{ID: uuid.New(), Direction: "inbound", Status: "active"}),
		domain.ErrNotFound)

	// Deleting the contact keeps the call.
	require.NoError(t, s.DeleteContact(ctx, contact.ID))
	got, err = s.GetCall(ctx, c.ID)
	require.NoError(t, err)
	require.Nil(t, got.ContactID)
}

func TestListCallsFilters(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "acme")
	other := newOrg(t, ctx, s, "other")

	bold := &domain.Contact{OrgID: org.ID, Phone: "+97688000001", Name: "Болд Батбаяр"}
	require.NoError(t, s.UpsertContact(ctx, bold))
	camp := &domain.Campaign{OrgID: org.ID, Name: "Promo"}
	require.NoError(t, s.CreateCampaign(ctx, camp, nil))

	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	mk := func(i int, dir domain.CallDirection, st domain.CallStatus, from, summary string, contactID, campID *uuid.UUID) *domain.Call {
		return newCall(t, ctx, s, domain.Call{OrgID: org.ID, Direction: dir, Status: st, FromNumber: from,
			ToNumber: "+97677001234", StartedAt: base.Add(time.Duration(i) * time.Hour), Summary: summary,
			ContactID: contactID, CampaignID: campID})
	}
	c0 := mk(0, "inbound", domain.StatusCompleted, "+97699000000", "Үнийн мэдээлэл асуусан", nil, nil)
	c1 := mk(1, "inbound", domain.StatusActive, "+97699000001", "", &bold.ID, nil)
	c2 := mk(2, "outbound", domain.StatusRinging, "+97677001234", "", nil, &camp.ID)
	c3 := mk(3, "outbound", domain.StatusFailed, "+97677001234", "100%_done", nil, &camp.ID)
	c4 := mk(4, "inbound", domain.StatusNoAnswer, "+97699000004", "", nil, nil)
	newCall(t, ctx, s, domain.Call{OrgID: other.ID, FromNumber: "+97699000000", StartedAt: base})

	ids := func(cs []domain.Call) []uuid.UUID {
		out := make([]uuid.UUID, len(cs))
		for i := range cs {
			out[i] = cs[i].ID
		}
		return out
	}

	all, total, err := s.ListCalls(ctx, domain.CallFilter{OrgID: org.ID})
	require.NoError(t, err)
	require.Equal(t, 5, total)
	require.Equal(t, []uuid.UUID{c4.ID, c3.ID, c2.ID, c1.ID, c0.ID}, ids(all), "newest first, org scoped")

	cases := []struct {
		name  string
		f     domain.CallFilter
		want  []uuid.UUID
		total int
	}{
		{"status list", domain.CallFilter{Status: []domain.CallStatus{"active", "ringing"}}, []uuid.UUID{c2.ID, c1.ID}, 2},
		{"direction", domain.CallFilter{Direction: domain.DirectionOutbound}, []uuid.UUID{c3.ID, c2.ID}, 2},
		{"campaign", domain.CallFilter{CampaignID: &camp.ID}, []uuid.UUID{c3.ID, c2.ID}, 2},
		{"search phone", domain.CallFilter{Search: "99000004"}, []uuid.UUID{c4.ID}, 1},
		{"search to_number", domain.CallFilter{Search: "77001234"}, []uuid.UUID{c4.ID, c3.ID, c2.ID, c1.ID, c0.ID}, 5},
		{"search summary", domain.CallFilter{Search: "үнийн"}, []uuid.UUID{c0.ID}, 1},
		{"search contact name", domain.CallFilter{Search: "болд"}, []uuid.UUID{c1.ID}, 1},
		{"search escapes like", domain.CallFilter{Search: "0%_"}, []uuid.UUID{c3.ID}, 1},
		{"search no match", domain.CallFilter{Search: "zzz"}, []uuid.UUID{}, 0},
		{"time range", domain.CallFilter{From: ptr(base.Add(time.Hour)), To: ptr(base.Add(3 * time.Hour))},
			[]uuid.UUID{c3.ID, c2.ID, c1.ID}, 3},
		{"combined", domain.CallFilter{Direction: "outbound", Status: []domain.CallStatus{"failed"}, CampaignID: &camp.ID},
			[]uuid.UUID{c3.ID}, 1},
		{"paging", domain.CallFilter{Limit: 2, Offset: 1}, []uuid.UUID{c3.ID, c2.ID}, 5},
		{"offset past end", domain.CallFilter{Limit: 2, Offset: 10}, []uuid.UUID{}, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.f.OrgID = org.ID
			got, total, err := s.ListCalls(ctx, tc.f)
			require.NoError(t, err)
			require.Equal(t, tc.total, total)
			require.Equal(t, tc.want, ids(got))
		})
	}

	active, err := s.ListActiveCalls(ctx, org.ID)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{c2.ID, c1.ID}, ids(active))
	allActive, err := s.ListActiveCalls(ctx, uuid.Nil)
	require.NoError(t, err)
	require.Len(t, allActive, 3, "uuid.Nil spans organisations (other org's call is ringing)")
}

func TestTranscriptTurns(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "acme")
	call := newCall(t, ctx, s, domain.Call{OrgID: org.ID})

	first := &domain.TranscriptTurn{CallID: call.ID, Speaker: domain.SpeakerAgent, Text: "Сайн байна уу", Confidence: 0.9,
		StartMs: 0, EndMs: 1200, IsFinal: true}
	require.NoError(t, s.AddTurn(ctx, first))
	require.NotEqual(t, uuid.Nil, first.ID)
	require.Equal(t, 1, first.Seq)

	// Concurrent appends get unique, gap-free sequence numbers.
	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.AddTurn(ctx, &domain.TranscriptTurn{CallID: call.ID, Speaker: domain.SpeakerCustomer,
				Text: "turn", StartMs: i})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	// Explicit seq is honoured; duplicate explicit seq conflicts.
	explicit := &domain.TranscriptTurn{CallID: call.ID, Seq: 100, Speaker: domain.SpeakerHuman, Text: "x"}
	require.NoError(t, s.AddTurn(ctx, explicit))
	require.Equal(t, 100, explicit.Seq)
	requireErrIs(t, s.AddTurn(ctx, &domain.TranscriptTurn{CallID: call.ID, Seq: 100, Speaker: "agent"}), domain.ErrConflict)
	requireErrIs(t, s.AddTurn(ctx, &domain.TranscriptTurn{CallID: uuid.New(), Speaker: "agent"}), domain.ErrNotFound)
	requireErrIs(t, s.AddTurn(ctx, &domain.TranscriptTurn{CallID: call.ID, Speaker: "robot"}), domain.ErrInvalid)

	turns, err := s.ListTurns(ctx, call.ID)
	require.NoError(t, err)
	require.Len(t, turns, n+2)
	seqs := make([]int, 0, len(turns))
	for _, tr := range turns {
		seqs = append(seqs, tr.Seq)
	}
	require.True(t, sort.IntsAreSorted(seqs))
	for i := 0; i <= n; i++ {
		require.Equal(t, i+1, seqs[i])
	}
	require.Equal(t, 100, seqs[n+1])

	require.NoError(t, s.UpdateTurnText(ctx, first.ID, "Сайн байна уу!"))
	require.NoError(t, s.UpdateTurnText(ctx, first.ID, "Сайн байна уу!!"))
	got, err := s.GetTurn(ctx, first.ID)
	require.NoError(t, err)
	require.Equal(t, "Сайн байна уу!!", got.Text)
	require.Equal(t, "Сайн байна уу", got.RawText, "original recognition preserved")
	require.InDelta(t, 0.9, got.Confidence, 1e-6)
	require.Equal(t, 1200, got.EndMs)
	require.True(t, got.IsFinal)

	requireErrIs(t, s.UpdateTurnText(ctx, uuid.New(), "x"), domain.ErrNotFound)
	_, err = s.GetTurn(ctx, uuid.New())
	requireErrIs(t, err, domain.ErrNotFound)
	empty, err := s.ListTurns(ctx, uuid.New())
	require.NoError(t, err)
	require.Empty(t, empty)
}

func TestStatsAndDailySeries(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "acme")
	other := newOrg(t, ctx, s, "other")

	st, err := s.Stats(ctx, org.ID)
	require.NoError(t, err)
	require.Equal(t, domain.CallStats{}, st, "empty org has zero stats")

	now := time.Now().UTC()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	today := midnight.Add(now.Sub(midnight) / 2) // safely inside today
	daysAgo := func(d int) time.Time { return midnight.AddDate(0, 0, -d).Add(12 * time.Hour) }

	add := func(at time.Time, dir domain.CallDirection, st domain.CallStatus, dur int, sent domain.Sentiment) {
		newCall(t, ctx, s, domain.Call{OrgID: org.ID, Direction: dir, Status: st, StartedAt: at, DurationSec: dur, Sentiment: sent})
	}
	add(today, "inbound", domain.StatusCompleted, 60, domain.SentimentPositive)
	add(today, "inbound", domain.StatusCompleted, 120, domain.SentimentNegative)
	add(today, "outbound", domain.StatusActive, 0, "")
	add(today, "outbound", domain.StatusFailed, 0, "")
	add(daysAgo(2), "inbound", domain.StatusCompleted, 30, domain.SentimentPositive)
	add(daysAgo(2), "outbound", domain.StatusNoAnswer, 0, domain.SentimentNeutral)
	add(daysAgo(2), "outbound", domain.StatusBusy, 0, "")
	add(daysAgo(10), "inbound", domain.StatusCompleted, 90, "")
	newCall(t, ctx, s, domain.Call{OrgID: other.ID, StartedAt: today, Status: domain.StatusCompleted, DurationSec: 999})

	st, err = s.Stats(ctx, org.ID)
	require.NoError(t, err)
	require.Equal(t, 8, st.TotalCalls)
	require.Equal(t, 1, st.ActiveCalls)
	require.Equal(t, 2, st.CompletedToday)
	require.InDelta(t, 75.0, st.AvgDurationSec, 1e-9) // (60+120+30+90)/4
	require.InDelta(t, 0.5, st.PositiveRatio, 1e-9)   // 2 of 4 with sentiment
	require.InDelta(t, 0.25, st.NegativeRatio, 1e-9)
	require.Equal(t, 2, st.InboundToday)
	require.Equal(t, 2, st.OutboundToday)

	series, err := s.DailySeries(ctx, org.ID, 7)
	require.NoError(t, err)
	require.Len(t, series, 7)
	require.Equal(t, midnight.AddDate(0, 0, -6), series[0].Day)
	require.Equal(t, midnight, series[6].Day)
	require.Equal(t, time.UTC, series[6].Day.Location())
	for i, d := range series {
		switch i {
		case 6:
			require.Equal(t, domain.DailyCallCount{Day: d.Day, Inbound: 2, Outbound: 2, Completed: 2, Failed: 1}, d)
		case 4:
			require.Equal(t, domain.DailyCallCount{Day: d.Day, Inbound: 1, Outbound: 2, Completed: 1, Failed: 2}, d)
		default:
			require.Equal(t, domain.DailyCallCount{Day: d.Day}, d, "zero day %d", i)
		}
	}

	series, err = s.DailySeries(ctx, org.ID, 0)
	require.NoError(t, err)
	require.Len(t, series, 14)
	require.Equal(t, 1, series[3].Inbound, "10 days ago bucket")
	series, err = s.DailySeries(ctx, org.ID, 10000)
	require.NoError(t, err)
	require.Len(t, series, 366)
	series, err = s.DailySeries(ctx, org.ID, 1)
	require.NoError(t, err)
	require.Len(t, series, 1)
	require.Equal(t, midnight, series[0].Day)
}
