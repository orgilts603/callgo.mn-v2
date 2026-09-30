DROP TABLE IF EXISTS do_not_call;
ALTER TABLE calls DROP COLUMN IF EXISTS outcome, DROP COLUMN IF EXISTS outcome_note;
-- v1 has no "skipped" status: keep such rows valid under the old constraint.
ALTER TABLE campaign_targets DROP CONSTRAINT IF EXISTS campaign_targets_status_check;
UPDATE campaign_targets SET status = 'failed' WHERE status = 'skipped';
ALTER TABLE campaign_targets
    DROP COLUMN IF EXISTS outcome,
    DROP COLUMN IF EXISTS outcome_note,
    ADD CONSTRAINT campaign_targets_status_check
        CHECK (status IN ('pending', 'calling', 'done', 'failed'));
ALTER TABLE campaigns
    DROP COLUMN IF EXISTS schedule,
    DROP COLUMN IF EXISTS outcomes,
    DROP COLUMN IF EXISTS dry_run_limit,
    DROP COLUMN IF EXISTS dry_run_dialed,
    DROP COLUMN IF EXISTS skipped;
