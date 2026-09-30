-- SaaS sprint: tenancy/account state, identity tokens, API keys, audit log,
-- subscriptions, usage metering, invoices and payments.

ALTER TABLE organizations
    ADD COLUMN plan_code  text        NOT NULL DEFAULT 'trial',
    ADD COLUMN status     text        NOT NULL DEFAULT 'active'
                                      CHECK (status IN ('active', 'suspended', 'closed')),
    ADD COLUMN timezone   text        NOT NULL DEFAULT 'Asia/Ulaanbaatar',
    ADD COLUMN settings   jsonb       NOT NULL DEFAULT '{}',
    ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();
CREATE INDEX organizations_created_idx ON organizations (created_at DESC);

ALTER TABLE users
    ADD COLUMN status            text        NOT NULL DEFAULT 'active'
                                             CHECK (status IN ('invited', 'active', 'disabled')),
    ADD COLUMN email_verified_at timestamptz,
    ADD COLUMN last_login_at     timestamptz,
    ADD COLUMN is_platform_admin boolean     NOT NULL DEFAULT false,
    ADD COLUMN updated_at        timestamptz NOT NULL DEFAULT now();

-- ---------------------------------------------------------------------------
-- Identity. Every token column holds a hash (sha256 hex) computed by the caller.
-- ---------------------------------------------------------------------------

CREATE TABLE invitations (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    email       text        NOT NULL CHECK (email <> ''),
    role        text        NOT NULL DEFAULT 'operator'
                            CHECK (role IN ('owner', 'admin', 'operator')),
    token_hash  text        NOT NULL CHECK (token_hash <> ''),
    invited_by  uuid        REFERENCES users (id) ON DELETE SET NULL,
    expires_at  timestamptz NOT NULL,
    accepted_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT invitations_token_hash_key UNIQUE (token_hash)
);
-- At most one open invitation per address and organisation.
CREATE UNIQUE INDEX invitations_org_email_pending_key
    ON invitations (org_id, lower(email)) WHERE accepted_at IS NULL;

CREATE TABLE password_resets (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash text        NOT NULL CHECK (token_hash <> ''),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT password_resets_token_hash_key UNIQUE (token_hash)
);
CREATE INDEX password_resets_user_idx ON password_resets (user_id);

CREATE TABLE email_verifications (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash text        NOT NULL CHECK (token_hash <> ''),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT email_verifications_token_hash_key UNIQUE (token_hash)
);
CREATE INDEX email_verifications_user_idx ON email_verifications (user_id);

CREATE TABLE refresh_sessions (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    org_id       uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    token_hash   text        NOT NULL CHECK (token_hash <> ''),
    user_agent   text        NOT NULL DEFAULT '',
    ip           text        NOT NULL DEFAULT '',
    expires_at   timestamptz NOT NULL,
    revoked_at   timestamptz,
    last_used_at timestamptz NOT NULL DEFAULT now(),
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT refresh_sessions_token_hash_key UNIQUE (token_hash)
);
CREATE INDEX refresh_sessions_user_idx ON refresh_sessions (user_id, expires_at);

CREATE TABLE api_keys (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id       uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name         text        NOT NULL DEFAULT '',
    prefix       text        NOT NULL CHECK (prefix <> ''),
    key_hash     text        NOT NULL CHECK (key_hash <> ''),
    scopes       text[]      NOT NULL DEFAULT '{}',
    created_by   uuid        REFERENCES users (id) ON DELETE SET NULL,
    last_used_at timestamptz,
    revoked_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT api_keys_prefix_key UNIQUE (prefix)
);
CREATE INDEX api_keys_org_idx ON api_keys (org_id, created_at DESC);

CREATE TABLE audit_log (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    actor_id    uuid        REFERENCES users (id) ON DELETE SET NULL,
    actor_email text        NOT NULL DEFAULT '',
    action      text        NOT NULL CHECK (action <> ''),
    target_type text        NOT NULL DEFAULT '',
    target_id   text        NOT NULL DEFAULT '',
    meta        jsonb       NOT NULL DEFAULT '{}',
    ip          text        NOT NULL DEFAULT '',
    at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_org_at_idx ON audit_log (org_id, at DESC);

-- ---------------------------------------------------------------------------
-- Billing. Amounts are integer MNT (VAT excluded unless named vat/total).
-- ---------------------------------------------------------------------------

CREATE TABLE subscriptions (
    id                   uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id               uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    plan_code            text        NOT NULL CHECK (plan_code <> ''),
    status               text        NOT NULL DEFAULT 'trialing'
                                     CHECK (status IN ('trialing', 'active', 'past_due', 'canceled')),
    current_period_start timestamptz NOT NULL,
    current_period_end   timestamptz NOT NULL,
    trial_ends_at        timestamptz,
    canceled_at          timestamptz,
    -- Enterprise overrides (a domain.Plan document); NULL = plan defaults.
    custom_limits        jsonb,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT subscriptions_org_key UNIQUE (org_id),
    CONSTRAINT subscriptions_period_check CHECK (current_period_end >= current_period_start)
);
CREATE INDEX subscriptions_status_idx ON subscriptions (status, current_period_end);

CREATE TABLE usage_records (
    id       uuid             PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id   uuid             NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    call_id  uuid             REFERENCES calls (id) ON DELETE SET NULL,
    kind     text             NOT NULL
                              CHECK (kind IN ('call_minutes', 'llm_tokens_in', 'llm_tokens_out',
                                              'stt_seconds', 'tts_chars', 'sms')),
    quantity double precision NOT NULL DEFAULT 0 CHECK (quantity >= 0),
    cost_mnt bigint           NOT NULL DEFAULT 0,
    at       timestamptz      NOT NULL DEFAULT now()
);
CREATE INDEX usage_records_org_at_idx ON usage_records (org_id, at);
CREATE INDEX usage_records_call_idx ON usage_records (call_id) WHERE call_id IS NOT NULL;

-- Invoice numbers: CG-<year of period_start in the org timezone>-<6-digit seq>.
CREATE SEQUENCE invoice_number_seq AS bigint START 1;

CREATE TABLE invoices (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id       uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    number       text        NOT NULL,
    period_start timestamptz NOT NULL,
    period_end   timestamptz NOT NULL,
    lines        jsonb       NOT NULL DEFAULT '[]',
    subtotal_mnt bigint      NOT NULL DEFAULT 0,
    vat_mnt      bigint      NOT NULL DEFAULT 0,
    total_mnt    bigint      NOT NULL DEFAULT 0,
    status       text        NOT NULL DEFAULT 'draft'
                             CHECK (status IN ('draft', 'open', 'paid', 'void')),
    due_at       timestamptz NOT NULL,
    paid_at      timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT invoices_number_key UNIQUE (number),
    CONSTRAINT invoices_org_period_key UNIQUE (org_id, period_start)
);
CREATE INDEX invoices_status_idx ON invoices (status, due_at);

CREATE TABLE payments (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id       uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    invoice_id   uuid        NOT NULL REFERENCES invoices (id) ON DELETE CASCADE,
    provider     text        NOT NULL CHECK (provider <> ''),
    provider_ref text        NOT NULL DEFAULT '',
    amount_mnt   bigint      NOT NULL DEFAULT 0,
    status       text        NOT NULL DEFAULT 'pending'
                             CHECK (status IN ('pending', 'paid', 'failed', 'expired')),
    qr_text      text        NOT NULL DEFAULT '',
    qr_image     text        NOT NULL DEFAULT '',
    deep_links   jsonb       NOT NULL DEFAULT '[]',
    expires_at   timestamptz,
    paid_at      timestamptz,
    raw          jsonb       NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX payments_provider_ref_key ON payments (provider, provider_ref) WHERE provider_ref <> '';
CREATE INDEX payments_invoice_idx ON payments (invoice_id, created_at);
CREATE INDEX payments_pending_idx ON payments (status) WHERE status = 'pending';
