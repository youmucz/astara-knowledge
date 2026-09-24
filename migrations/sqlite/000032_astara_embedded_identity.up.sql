-- Embedded (Plane-hosted) identity exchange (SQLite variant).
ALTER TABLE users ADD COLUMN external_system TEXT;
ALTER TABLE users ADD COLUMN external_id TEXT;

CREATE UNIQUE INDEX uq_users_external_identity
    ON users (external_system, external_id)
    WHERE external_system IS NOT NULL AND external_id IS NOT NULL;

ALTER TABLE tenant_members ADD COLUMN permission_revision TEXT NOT NULL DEFAULT '';
