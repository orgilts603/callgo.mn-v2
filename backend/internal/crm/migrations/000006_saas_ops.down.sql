DROP TABLE IF EXISTS callback_requests;
DROP TABLE IF EXISTS sms_messages;
DROP TABLE IF EXISTS webhook_deliveries;
DROP TABLE IF EXISTS webhooks;

DROP INDEX IF EXISTS calls_handoff_idx;
DROP INDEX IF EXISTS calls_recording_ready_idx;
ALTER TABLE calls
    DROP COLUMN IF EXISTS usage,
    DROP COLUMN IF EXISTS operator_id,
    DROP COLUMN IF EXISTS handoff,
    DROP COLUMN IF EXISTS recording;

ALTER TABLE agent_profiles DROP COLUMN IF EXISTS post_call_actions;

ALTER TABLE sip_numbers DROP COLUMN IF EXISTS routing;
