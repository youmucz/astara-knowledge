DROP INDEX IF EXISTS uq_users_external_identity;
ALTER TABLE tenant_members DROP COLUMN permission_revision;
ALTER TABLE users DROP COLUMN external_id;
ALTER TABLE users DROP COLUMN external_system;
