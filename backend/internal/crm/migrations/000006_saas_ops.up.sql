-- SaaS ops: inbound routing, post-call actions, recordings / operator handoff
-- / usage on calls, outbound webhooks (+ delivery log), SMS and callbacks.
-- Independent of 000005 (identity/billing): only touches the tables below.

ALTER TABLE sip_numbers
    ADD COLUMN routing jsonb NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE agent_profiles
    ADD COLUMN post_call_actions jsonb NOT NULL DEFAULT '[]'::jsonb;

ALTER TABLE calls
    ADD COLUMN recording   jsonb,
    ADD COLUMN handoff     text  NOT NULL DEFAULT ''
                                 CHECK (handoff IN ('', 'requested', 'active', 'ended')),
    ADD COLUMN operator_id uuid  REFERENCES users (id) ON DELETE SET NULL,
    ADD COLUMN usage       jsonb;
-- Recording retention sweep: ready recordings of ended calls.
CREATE INDEX calls_recording_ready_idx ON calls (ended_at)
    WHERE recording IS NOT NULL AND recording ->> 'status' = 'ready';
CREATE INDEX calls_handoff_idx ON calls (handoff) WHERE handoff <> '';

CREATE TABLE webhooks (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id        uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    url           text        NOT NULL,
    -- base64(nonce || AES-256-GCM ciphertext) of the HMAC signing secret.
    secret_enc    text        NOT NULL DEFAULT '',
    secret_hint   text        NOT NULL DEFAULT '',
    events        text[]      NOT NULL DEFAULT '{}',
    active        boolean     NOT NULL DEFAULT true,
    description   text        NOT NULL DEFAULT '',
    failure_count integer     NOT NULL DEFAULT 0,
    last_status   integer     NOT NULL DEFAULT 0,
    last_at       timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webhooks_org_idx ON webhooks (org_id, created_at);
CREATE INDEX webhooks_events_idx ON webhooks USING gin (events) WHERE active;

CREATE TABLE webhook_deliveries (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    webhook_id    uuid        NOT NULL REFERENCES webhooks (id) ON DELETE CASCADE,
    event_id      text        NOT NULL DEFAULT '',
    event_type    text        NOT NULL DEFAULT '',
    status        text        NOT NULL DEFAULT 'pending'
                              CHECK (status IN ('pending', 'delivered', 'failed')),
    attempts      integer     NOT NULL DEFAULT 0,
    response_code integer     NOT NULL DEFAULT 0,
    last_error    text        NOT NULL DEFAULT '',
    next_try_at   timestamptz,
    payload       bytea       NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webhook_deliveries_due_idx ON webhook_deliveries (status, next_try_at);
CREATE INDEX webhook_deliveries_webhook_idx ON webhook_deliveries (webhook_id, created_at DESC);

CREATE TABLE sms_messages (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id       uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    call_id      uuid        REFERENCES calls (id) ON DELETE SET NULL,
    to_number    text        NOT NULL,
    body         text        NOT NULL DEFAULT '',
    provider     text        NOT NULL DEFAULT '',
    provider_ref text        NOT NULL DEFAULT '',
    status       text        NOT NULL DEFAULT 'queued'
                             CHECK (status IN ('queued', 'sent', 'failed')),
    error        text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    sent_at      timestamptz
);
CREATE INDEX sms_messages_org_idx ON sms_messages (org_id, created_at DESC);
CREATE INDEX sms_messages_call_idx ON sms_messages (call_id) WHERE call_id IS NOT NULL;

CREATE TABLE callback_requests (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id           uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    source_call_id   uuid        REFERENCES calls (id) ON DELETE SET NULL,
    contact_id       uuid        REFERENCES contacts (id) ON DELETE SET NULL,
    phone            text        NOT NULL,
    name             text        NOT NULL DEFAULT '',
    note             text        NOT NULL DEFAULT '',
    due_at           timestamptz NOT NULL DEFAULT now(),
    sip_number_id    uuid        REFERENCES sip_numbers (id) ON DELETE SET NULL,
    agent_profile_id uuid        REFERENCES agent_profiles (id) ON DELETE SET NULL,
    status           text        NOT NULL DEFAULT 'pending'
                                 CHECK (status IN ('pending', 'dialed', 'done', 'canceled', 'failed')),
    result_call_id   uuid        REFERENCES calls (id) ON DELETE SET NULL,
    attempts         integer     NOT NULL DEFAULT 0,
    created_by       uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX callback_requests_due_idx ON callback_requests (status, due_at);
CREATE INDEX callback_requests_org_idx ON callback_requests (org_id, due_at DESC);
