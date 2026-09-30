DROP TABLE IF EXISTS payments;
DROP TABLE IF EXISTS invoices;
DROP SEQUENCE IF EXISTS invoice_number_seq;
DROP TABLE IF EXISTS usage_records;
DROP TABLE IF EXISTS subscriptions;
DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS api_keys;
DROP TABLE IF EXISTS refresh_sessions;
DROP TABLE IF EXISTS email_verifications;
DROP TABLE IF EXISTS password_resets;
DROP TABLE IF EXISTS invitations;

ALTER TABLE users
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS is_platform_admin,
    DROP COLUMN IF EXISTS last_login_at,
    DROP COLUMN IF EXISTS email_verified_at,
    DROP COLUMN IF EXISTS status;

DROP INDEX IF EXISTS organizations_created_idx;
ALTER TABLE organizations
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS settings,
    DROP COLUMN IF EXISTS timezone,
    DROP COLUMN IF EXISTS status,
    DROP COLUMN IF EXISTS plan_code;
