-- Campaign v2: calling schedule, structured outcomes, dry-run, do-not-call.
ALTER TABLE campaigns
    ADD COLUMN schedule       jsonb   NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN outcomes       jsonb   NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN dry_run_limit  integer NOT NULL DEFAULT 0 CHECK (dry_run_limit >= 0),
    ADD COLUMN dry_run_dialed integer NOT NULL DEFAULT 0,
    ADD COLUMN skipped        integer NOT NULL DEFAULT 0;

ALTER TABLE campaign_targets
    DROP CONSTRAINT IF EXISTS campaign_targets_status_check,
    ADD CONSTRAINT campaign_targets_status_check
        CHECK (status IN ('pending', 'calling', 'done', 'failed', 'skipped')),
    ADD COLUMN outcome      text NOT NULL DEFAULT '',
    ADD COLUMN outcome_note text NOT NULL DEFAULT '';

ALTER TABLE calls
    ADD COLUMN outcome      text NOT NULL DEFAULT '',
    ADD COLUMN outcome_note text NOT NULL DEFAULT '';

CREATE TABLE do_not_call (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    phone      text        NOT NULL,
    reason     text        NOT NULL DEFAULT '',
    created_by uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, phone)
);
