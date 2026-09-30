-- CallGo.mn v2 initial schema.
-- gen_random_uuid() is built into PostgreSQL 13+ (no pgcrypto needed).

CREATE TABLE organizations (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text        NOT NULL,
    slug       text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT organizations_slug_key UNIQUE (slug)
);

CREATE TABLE users (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id        uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    email         text        NOT NULL,
    name          text        NOT NULL DEFAULT '',
    role          text        NOT NULL DEFAULT 'operator'
                              CHECK (role IN ('owner', 'admin', 'operator')),
    password_hash text        NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now()
);
-- Login is by e-mail across all organisations.
CREATE UNIQUE INDEX users_email_key ON users (lower(email));
CREATE INDEX users_org_idx ON users (org_id);

CREATE TABLE llm_configs (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id       uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name         text        NOT NULL,
    provider     text        NOT NULL,
    model        text        NOT NULL,
    base_url     text        NOT NULL DEFAULT '',
    -- base64(nonce || AES-256-GCM ciphertext); '' when no key is set.
    api_key_enc  text        NOT NULL DEFAULT '',
    api_key_hint text        NOT NULL DEFAULT '',
    temperature  real        NOT NULL DEFAULT 0.7,
    max_tokens   integer     NOT NULL DEFAULT 0,
    is_default   boolean     NOT NULL DEFAULT false,
    fallback_id  uuid        REFERENCES llm_configs (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT llm_configs_no_self_fallback CHECK (fallback_id IS NULL OR fallback_id <> id)
);
CREATE INDEX llm_configs_org_idx ON llm_configs (org_id, created_at);
-- At most one default model per organisation.
CREATE UNIQUE INDEX llm_configs_one_default_per_org ON llm_configs (org_id) WHERE is_default;

CREATE TABLE agent_profiles (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id           uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name             text        NOT NULL,
    system_prompt    text        NOT NULL DEFAULT '',
    greeting         text        NOT NULL DEFAULT '',
    language         text        NOT NULL DEFAULT 'mn',
    llm_config_id    uuid        REFERENCES llm_configs (id) ON DELETE SET NULL,
    stt_provider     text        NOT NULL DEFAULT '',
    stt_model        text        NOT NULL DEFAULT '',
    tts_provider     text        NOT NULL DEFAULT '',
    tts_voice        text        NOT NULL DEFAULT '',
    max_duration_sec integer     NOT NULL DEFAULT 0,
    tools            text[]      NOT NULL DEFAULT '{}',
    transfer_number  text        NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX agent_profiles_org_idx ON agent_profiles (org_id, created_at);
CREATE INDEX agent_profiles_llm_config_idx ON agent_profiles (llm_config_id);

CREATE TABLE sip_numbers (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id            uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    number            text        NOT NULL,
    label             text        NOT NULL DEFAULT '',
    inbound_trunk_id  text        NOT NULL DEFAULT '',
    outbound_trunk_id text        NOT NULL DEFAULT '',
    dispatch_rule_id  text        NOT NULL DEFAULT '',
    asterisk_endpoint text        NOT NULL DEFAULT '',
    agent_profile_id  uuid        REFERENCES agent_profiles (id) ON DELETE SET NULL,
    allow_inbound     boolean     NOT NULL DEFAULT true,
    allow_outbound    boolean     NOT NULL DEFAULT true,
    active            boolean     NOT NULL DEFAULT true,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT sip_numbers_number_key UNIQUE (number)
);
CREATE INDEX sip_numbers_org_idx ON sip_numbers (org_id, created_at);
CREATE INDEX sip_numbers_agent_profile_idx ON sip_numbers (agent_profile_id);

CREATE TABLE contacts (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    phone      text        NOT NULL,
    name       text        NOT NULL DEFAULT '',
    tags       text[]      NOT NULL DEFAULT '{}',
    meta       jsonb       NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT contacts_org_phone_key UNIQUE (org_id, phone)
);
CREATE INDEX contacts_org_updated_idx ON contacts (org_id, updated_at DESC);

CREATE TABLE campaigns (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id           uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name             text        NOT NULL,
    sip_number_id    uuid        REFERENCES sip_numbers (id) ON DELETE SET NULL,
    agent_profile_id uuid        REFERENCES agent_profiles (id) ON DELETE SET NULL,
    script           text        NOT NULL DEFAULT '',
    status           text        NOT NULL DEFAULT 'draft'
                                 CHECK (status IN ('draft', 'running', 'paused', 'completed')),
    concurrency      integer     NOT NULL DEFAULT 2 CHECK (concurrency > 0),
    max_attempts     integer     NOT NULL DEFAULT 2 CHECK (max_attempts > 0),
    total            integer     NOT NULL DEFAULT 0,
    completed        integer     NOT NULL DEFAULT 0,
    failed           integer     NOT NULL DEFAULT 0,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX campaigns_org_idx ON campaigns (org_id, created_at DESC);
CREATE INDEX campaigns_running_idx ON campaigns (status) WHERE status = 'running';

CREATE TABLE calls (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id           uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    contact_id       uuid        REFERENCES contacts (id) ON DELETE SET NULL,
    campaign_id      uuid        REFERENCES campaigns (id) ON DELETE SET NULL,
    sip_number_id    uuid        REFERENCES sip_numbers (id) ON DELETE SET NULL,
    agent_profile_id uuid        REFERENCES agent_profiles (id) ON DELETE SET NULL,
    direction        text        NOT NULL CHECK (direction IN ('inbound', 'outbound')),
    status           text        NOT NULL
                                 CHECK (status IN ('queued', 'ringing', 'active', 'completed',
                                                   'failed', 'no_answer', 'busy', 'voicemail')),
    from_number      text        NOT NULL DEFAULT '',
    to_number        text        NOT NULL DEFAULT '',
    room_name        text        NOT NULL DEFAULT '',
    sip_call_id      text        NOT NULL DEFAULT '',
    participant_id   text        NOT NULL DEFAULT '',
    started_at       timestamptz NOT NULL DEFAULT now(),
    answered_at      timestamptz,
    ended_at         timestamptz,
    duration_sec     integer     NOT NULL DEFAULT 0,
    recording_url    text        NOT NULL DEFAULT '',
    summary          text        NOT NULL DEFAULT '',
    sentiment        text        NOT NULL DEFAULT ''
                                 CHECK (sentiment IN ('', 'positive', 'neutral', 'negative')),
    intent           text        NOT NULL DEFAULT '',
    end_reason       text        NOT NULL DEFAULT '',
    llm_model_used   text        NOT NULL DEFAULT '',
    metadata         jsonb       NOT NULL DEFAULT '{}',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX calls_org_started_idx ON calls (org_id, started_at DESC);
-- One LiveKit room per call. Rows without a room yet (e.g. queued outbound)
-- may share the empty string.
CREATE UNIQUE INDEX calls_room_name_key ON calls (room_name) WHERE room_name <> '';
CREATE INDEX calls_status_idx ON calls (status);
CREATE INDEX calls_contact_idx ON calls (contact_id, started_at DESC);
CREATE INDEX calls_campaign_idx ON calls (campaign_id) WHERE campaign_id IS NOT NULL;

CREATE TABLE call_transcripts (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    call_id    uuid        NOT NULL REFERENCES calls (id) ON DELETE CASCADE,
    seq        integer     NOT NULL,
    speaker    text        NOT NULL CHECK (speaker IN ('customer', 'agent', 'human')),
    text       text        NOT NULL DEFAULT '',
    raw_text   text        NOT NULL DEFAULT '',
    confidence real        NOT NULL DEFAULT 0,
    start_ms   integer     NOT NULL DEFAULT 0,
    end_ms     integer     NOT NULL DEFAULT 0,
    is_final   boolean     NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT call_transcripts_call_seq_key UNIQUE (call_id, seq)
);

CREATE TABLE campaign_targets (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    campaign_id uuid        NOT NULL REFERENCES campaigns (id) ON DELETE CASCADE,
    -- Position in the uploaded CSV, for stable listing order.
    pos         integer     NOT NULL DEFAULT 0,
    contact_id  uuid        REFERENCES contacts (id) ON DELETE SET NULL,
    phone       text        NOT NULL,
    name        text        NOT NULL DEFAULT '',
    vars        jsonb       NOT NULL DEFAULT '{}',
    status      text        NOT NULL DEFAULT 'pending'
                            CHECK (status IN ('pending', 'calling', 'done', 'failed')),
    attempts    integer     NOT NULL DEFAULT 0,
    call_id     uuid        REFERENCES calls (id) ON DELETE SET NULL,
    last_error  text        NOT NULL DEFAULT '',
    next_try_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX campaign_targets_claim_idx ON campaign_targets (campaign_id, status, next_try_at);
CREATE INDEX campaign_targets_list_idx ON campaign_targets (campaign_id, pos);
CREATE INDEX campaign_targets_call_idx ON campaign_targets (call_id) WHERE call_id IS NOT NULL;

CREATE TABLE lexicon_corrections (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id         uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    wrong          text        NOT NULL CHECK (wrong <> ''),
    correct        text        NOT NULL,
    phonetic       text        NOT NULL DEFAULT '',
    scope          text        NOT NULL DEFAULT 'stt' CHECK (scope IN ('stt', 'tts', 'both')),
    source_turn_id uuid        REFERENCES call_transcripts (id) ON DELETE SET NULL,
    created_by     uuid        REFERENCES users (id) ON DELETE SET NULL,
    hit_count      integer     NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX lexicon_corrections_org_wrong_key ON lexicon_corrections (org_id, lower(wrong));
