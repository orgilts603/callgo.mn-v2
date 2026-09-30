package crm

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Call listing page sizes.
const (
	defaultCallLimit = 50
	maxCallLimit     = 500
	maxDailyDays     = 366
	defaultDailyDays = 14
)

// activeStatuses are the non-terminal call states shown on the Live Desk.
var activeStatuses = []string{
	string(domain.StatusQueued), string(domain.StatusRinging), string(domain.StatusActive),
}

const callCols = `c.id, c.org_id, c.contact_id, c.campaign_id, c.sip_number_id, c.agent_profile_id,
	c.direction, c.status, c.from_number, c.to_number, c.room_name, c.sip_call_id, c.participant_id,
	c.started_at, c.answered_at, c.ended_at, c.duration_sec, c.recording_url, c.summary, c.sentiment,
	c.intent, c.end_reason, c.llm_model_used, c.metadata, c.created_at, c.updated_at`

func scanCall(row pgx.Row) (*domain.Call, error) {
	var c domain.Call
	err := row.Scan(&c.ID, &c.OrgID, &c.ContactID, &c.CampaignID, &c.SIPNumberID, &c.AgentProfileID,
		&c.Direction, &c.Status, &c.FromNumber, &c.ToNumber, &c.RoomName, &c.SIPCallID, &c.ParticipantID,
		&c.StartedAt, &c.AnsweredAt, &c.EndedAt, &c.DurationSec, &c.RecordingURL, &c.Summary, &c.Sentiment,
		&c.Intent, &c.EndReason, &c.LLMModelUsed, &c.Metadata, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if len(c.Metadata) == 0 {
		c.Metadata = nil
	}
	return &c, nil
}

// jsonObject renders v as JSON text for a jsonb column, using "{}" for nil.
func jsonObject[M ~map[string]V, V any](m M) (string, error) {
	if m == nil {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("crm: marshal json: %w", err)
	}
	return string(b), nil
}

// CreateCall inserts a call. StartedAt defaults to now().
func (s *Store) CreateCall(ctx context.Context, c *domain.Call) error {
	meta, err := jsonObject(c.Metadata)
	if err != nil {
		return err
	}
	row := s.db.QueryRow(ctx,
		`INSERT INTO calls (id, org_id, contact_id, campaign_id, sip_number_id, agent_profile_id, direction,
			status, from_number, to_number, room_name, sip_call_id, participant_id, started_at, answered_at,
			ended_at, duration_sec, recording_url, summary, sentiment, intent, end_reason, llm_model_used, metadata)
		 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
			COALESCE($14, now()), $15, $16, $17, $18, $19, $20, $21, $22, $23, $24)
		 RETURNING id, started_at, created_at, updated_at`,
		nilIfZero(c.ID), c.OrgID, c.ContactID, c.CampaignID, c.SIPNumberID, c.AgentProfileID,
		string(c.Direction), string(c.Status), c.FromNumber, c.ToNumber, c.RoomName, c.SIPCallID,
		c.ParticipantID, nilIfZeroTime(c.StartedAt), c.AnsweredAt, c.EndedAt, c.DurationSec, c.RecordingURL,
		c.Summary, string(c.Sentiment), c.Intent, c.EndReason, c.LLMModelUsed, meta)
	return dbErr("create call", row.Scan(&c.ID, &c.StartedAt, &c.CreatedAt, &c.UpdatedAt))
}

// UpdateCall overwrites all mutable fields of a call (org is immutable).
func (s *Store) UpdateCall(ctx context.Context, c *domain.Call) error {
	meta, err := jsonObject(c.Metadata)
	if err != nil {
		return err
	}
	row := s.db.QueryRow(ctx,
		`UPDATE calls SET contact_id = $2, campaign_id = $3, sip_number_id = $4, agent_profile_id = $5,
			direction = $6, status = $7, from_number = $8, to_number = $9, room_name = $10, sip_call_id = $11,
			participant_id = $12, started_at = COALESCE($13, started_at), answered_at = $14, ended_at = $15,
			duration_sec = $16, recording_url = $17, summary = $18, sentiment = $19, intent = $20,
			end_reason = $21, llm_model_used = $22, metadata = $23, updated_at = now()
		 WHERE id = $1
		 RETURNING org_id, started_at, created_at, updated_at`,
		c.ID, c.ContactID, c.CampaignID, c.SIPNumberID, c.AgentProfileID, string(c.Direction),
		string(c.Status), c.FromNumber, c.ToNumber, c.RoomName, c.SIPCallID, c.ParticipantID,
		nilIfZeroTime(c.StartedAt), c.AnsweredAt, c.EndedAt, c.DurationSec, c.RecordingURL, c.Summary,
		string(c.Sentiment), c.Intent, c.EndReason, c.LLMModelUsed, meta)
	return dbErr("update call", row.Scan(&c.OrgID, &c.StartedAt, &c.CreatedAt, &c.UpdatedAt))
}

// GetCall returns a call by ID.
func (s *Store) GetCall(ctx context.Context, id uuid.UUID) (*domain.Call, error) {
	c, err := scanCall(s.db.QueryRow(ctx, `SELECT `+callCols+` FROM calls c WHERE c.id = $1`, id))
	return c, dbErr("get call", err)
}

// GetCallByRoom returns the call bound to a LiveKit room.
func (s *Store) GetCallByRoom(ctx context.Context, roomName string) (*domain.Call, error) {
	if roomName == "" {
		return nil, fmt.Errorf("crm: get call by room: %w", domain.ErrNotFound)
	}
	c, err := scanCall(s.db.QueryRow(ctx, `SELECT `+callCols+` FROM calls c WHERE c.room_name = $1`, roomName))
	return c, dbErr("get call by room", err)
}

// argList accumulates positional query arguments.
type argList []any

func (a *argList) add(v any) string {
	*a = append(*a, v)
	return "$" + strconv.Itoa(len(*a))
}

// ListCalls returns one page of an organisation's calls (newest first) and the
// total number of matches.
func (s *Store) ListCalls(ctx context.Context, f domain.CallFilter) ([]domain.Call, int, error) {
	limit, offset := clampPage(f.Limit, f.Offset, defaultCallLimit, maxCallLimit)

	var args argList
	where := []string{"c.org_id = " + args.add(f.OrgID)}
	if len(f.Status) > 0 {
		st := make([]string, len(f.Status))
		for i, v := range f.Status {
			st[i] = string(v)
		}
		where = append(where, "c.status = ANY("+args.add(st)+"::text[])")
	}
	if f.Direction != "" {
		where = append(where, "c.direction = "+args.add(string(f.Direction)))
	}
	if f.CampaignID != nil {
		where = append(where, "c.campaign_id = "+args.add(*f.CampaignID))
	}
	if q := strings.TrimSpace(f.Search); q != "" {
		p := args.add(likePattern(q))
		where = append(where, "(c.from_number ILIKE "+p+" OR c.to_number ILIKE "+p+
			" OR c.summary ILIKE "+p+" OR ct.name ILIKE "+p+")")
	}
	if f.From != nil {
		where = append(where, "c.started_at >= "+args.add(*f.From))
	}
	if f.To != nil {
		where = append(where, "c.started_at <= "+args.add(*f.To))
	}
	from := ` FROM calls c LEFT JOIN contacts ct ON ct.id = c.contact_id WHERE ` + strings.Join(where, " AND ")

	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*)`+from, args...).Scan(&total); err != nil {
		return nil, 0, dbErr("count calls", err)
	}
	if total == 0 || offset >= total {
		return []domain.Call{}, total, nil
	}
	sql := `SELECT ` + callCols + from + ` ORDER BY c.started_at DESC, c.id DESC LIMIT ` +
		args.add(limit) + ` OFFSET ` + args.add(offset)
	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, dbErr("list calls", err)
	}
	calls, err := collect("list calls", rows, scanCall)
	if err != nil {
		return nil, 0, err
	}
	return calls, total, nil
}

// ListActiveCalls returns calls in queued/ringing/active state, newest first.
// uuid.Nil lists active calls of every organisation (room reconciliation).
func (s *Store) ListActiveCalls(ctx context.Context, orgID uuid.UUID) ([]domain.Call, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+callCols+` FROM calls c
		 WHERE c.status = ANY($1::text[]) AND ($2::uuid IS NULL OR c.org_id = $2)
		 ORDER BY c.started_at DESC, c.id DESC`,
		activeStatuses, nilIfZero(orgID))
	if err != nil {
		return nil, dbErr("list active calls", err)
	}
	return collect("list active calls", rows, scanCall)
}

// ListCallsByContact returns a contact's most recent calls (not part of the
// domain port; used by GET /api/contacts/{id}).
func (s *Store) ListCallsByContact(ctx context.Context, contactID uuid.UUID, limit int) ([]domain.Call, error) {
	limit, _ = clampPage(limit, 0, 20, maxCallLimit)
	rows, err := s.db.Query(ctx,
		`SELECT `+callCols+` FROM calls c WHERE c.contact_id = $1 ORDER BY c.started_at DESC, c.id DESC LIMIT $2`,
		contactID, limit)
	if err != nil {
		return nil, dbErr("list calls by contact", err)
	}
	return collect("list calls by contact", rows, scanCall)
}

// ---------------------------------------------------------------------------
// Transcript turns
// ---------------------------------------------------------------------------

const turnCols = `id, call_id, seq, speaker, text, raw_text, confidence, start_ms, end_ms, is_final, created_at`

func scanTurn(row pgx.Row) (*domain.TranscriptTurn, error) {
	var t domain.TranscriptTurn
	err := row.Scan(&t.ID, &t.CallID, &t.Seq, &t.Speaker, &t.Text, &t.RawText, &t.Confidence,
		&t.StartMs, &t.EndMs, &t.IsFinal, &t.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// AddTurn appends a transcript turn. When t.Seq is 0 the next sequence number
// for the call (max+1, starting at 1) is assigned atomically: the call row is
// locked for the duration of the insert so concurrent writers serialise.
func (s *Store) AddTurn(ctx context.Context, t *domain.TranscriptTurn) error {
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	return s.inTx(ctx, func(tx *Store) error {
		var locked uuid.UUID
		if err := tx.db.QueryRow(ctx, `SELECT id FROM calls WHERE id = $1 FOR NO KEY UPDATE`, t.CallID).
			Scan(&locked); err != nil {
			return dbErr("add turn: lock call", err)
		}
		row := tx.db.QueryRow(ctx,
			`INSERT INTO call_transcripts (id, call_id, seq, speaker, text, raw_text, confidence, start_ms,
				end_ms, is_final)
			 VALUES ($1, $2,
				CASE WHEN $3::int > 0 THEN $3::int
				     ELSE (SELECT COALESCE(max(seq), 0) + 1 FROM call_transcripts WHERE call_id = $2) END,
				$4, $5, $6, $7, $8, $9, $10)
			 RETURNING seq, created_at`,
			t.ID, t.CallID, t.Seq, string(t.Speaker), t.Text, t.RawText, t.Confidence, t.StartMs, t.EndMs, t.IsFinal)
		return dbErr("add turn", row.Scan(&t.Seq, &t.CreatedAt))
	})
}

// UpdateTurnText replaces a turn's text. The first correction preserves the
// original recognition in raw_text.
func (s *Store) UpdateTurnText(ctx context.Context, turnID uuid.UUID, text string) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE call_transcripts SET raw_text = CASE WHEN raw_text = '' THEN text ELSE raw_text END, text = $2
		 WHERE id = $1`, turnID, text)
	return affected("update turn text", tag, err)
}

// GetTurn returns one transcript turn.
func (s *Store) GetTurn(ctx context.Context, turnID uuid.UUID) (*domain.TranscriptTurn, error) {
	t, err := scanTurn(s.db.QueryRow(ctx, `SELECT `+turnCols+` FROM call_transcripts WHERE id = $1`, turnID))
	return t, dbErr("get turn", err)
}

// ListTurns returns a call's transcript in sequence order.
func (s *Store) ListTurns(ctx context.Context, callID uuid.UUID) ([]domain.TranscriptTurn, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+turnCols+` FROM call_transcripts WHERE call_id = $1 ORDER BY seq`, callID)
	if err != nil {
		return nil, dbErr("list turns", err)
	}
	return collect("list turns", rows, scanTurn)
}

// ---------------------------------------------------------------------------
// Dashboard statistics
// ---------------------------------------------------------------------------

// Stats computes the dashboard summary. "Today" is the current UTC date.
// AvgDurationSec averages completed calls with a positive duration; the
// sentiment ratios are over calls that have a sentiment.
func (s *Store) Stats(ctx context.Context, orgID uuid.UUID) (domain.CallStats, error) {
	var st domain.CallStats
	err := s.db.QueryRow(ctx,
		`WITH b AS (SELECT date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS today)
		 SELECT
			count(*),
			count(*) FILTER (WHERE c.status = ANY($2::text[])),
			count(*) FILTER (WHERE c.status = 'completed' AND c.started_at >= b.today),
			COALESCE(avg(c.duration_sec) FILTER (WHERE c.status = 'completed' AND c.duration_sec > 0), 0)::float8,
			COALESCE(count(*) FILTER (WHERE c.sentiment = 'positive')::float8
				/ NULLIF(count(*) FILTER (WHERE c.sentiment <> ''), 0), 0)::float8,
			COALESCE(count(*) FILTER (WHERE c.sentiment = 'negative')::float8
				/ NULLIF(count(*) FILTER (WHERE c.sentiment <> ''), 0), 0)::float8,
			count(*) FILTER (WHERE c.direction = 'inbound' AND c.started_at >= b.today),
			count(*) FILTER (WHERE c.direction = 'outbound' AND c.started_at >= b.today)
		 FROM calls c CROSS JOIN b
		 WHERE c.org_id = $1`,
		orgID, activeStatuses).Scan(&st.TotalCalls, &st.ActiveCalls, &st.CompletedToday, &st.AvgDurationSec,
		&st.PositiveRatio, &st.NegativeRatio, &st.InboundToday, &st.OutboundToday)
	return st, dbErr("call stats", err)
}

// DailySeries returns one bucket per UTC day for the last `days` days
// (including today and days without calls), oldest first. Failed counts
// failed, no_answer and busy outcomes. days <= 0 means 14; capped at 366.
func (s *Store) DailySeries(ctx context.Context, orgID uuid.UUID, days int) ([]domain.DailyCallCount, error) {
	if days <= 0 {
		days = defaultDailyDays
	}
	if days > maxDailyDays {
		days = maxDailyDays
	}
	rows, err := s.db.Query(ctx,
		`WITH days AS (
			SELECT d::date AS day
			FROM generate_series((now() AT TIME ZONE 'UTC')::date - ($2::int - 1),
			                     (now() AT TIME ZONE 'UTC')::date, interval '1 day') AS d
		 ), agg AS (
			SELECT (c.started_at AT TIME ZONE 'UTC')::date AS day,
				count(*) FILTER (WHERE c.direction = 'inbound')  AS inbound,
				count(*) FILTER (WHERE c.direction = 'outbound') AS outbound,
				count(*) FILTER (WHERE c.status = 'completed')   AS completed,
				count(*) FILTER (WHERE c.status IN ('failed', 'no_answer', 'busy')) AS failed
			FROM calls c
			WHERE c.org_id = $1
			  AND c.started_at >= ((now() AT TIME ZONE 'UTC')::date - ($2::int - 1))::timestamp AT TIME ZONE 'UTC'
			GROUP BY 1
		 )
		 SELECT days.day::timestamp AT TIME ZONE 'UTC',
			COALESCE(agg.inbound, 0), COALESCE(agg.outbound, 0),
			COALESCE(agg.completed, 0), COALESCE(agg.failed, 0)
		 FROM days LEFT JOIN agg ON agg.day = days.day
		 ORDER BY days.day`,
		orgID, days)
	if err != nil {
		return nil, dbErr("daily series", err)
	}
	return collect("daily series", rows, func(r pgx.Row) (*domain.DailyCallCount, error) {
		var d domain.DailyCallCount
		if err := r.Scan(&d.Day, &d.Inbound, &d.Outbound, &d.Completed, &d.Failed); err != nil {
			return nil, err
		}
		d.Day = d.Day.UTC()
		return &d, nil
	})
}
