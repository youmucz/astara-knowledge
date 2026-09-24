DROP INDEX IF EXISTS uq_users_external_identity;
ALTER TABLE tenant_members DROP COLUMN IF EXISTS permission_revision;
ALTER TABLE users DROP COLUMN IF EXISTS external_id;
ALTER TABLE users DROP COLUMN IF EXISTS external_system;
