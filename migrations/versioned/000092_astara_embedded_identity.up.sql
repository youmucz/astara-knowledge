-- Embedded (Plane-hosted) identity exchange.
--
-- Shadow users provisioned through the astara identity exchange carry the
-- immutable external identity of their owning Plane user. TenantMember rows
-- gain the permission revision of the membership snapshot the session was
-- minted against, so stale sessions are rejected when Plane-side
-- membership/role state changes.
ALTER TABLE users ADD COLUMN IF NOT EXISTS external_system VARCHAR(64);
ALTER TABLE users ADD COLUMN IF NOT EXISTS external_id VARCHAR(255);

CREATE UNIQUE INDEX IF NOT EXISTS uq_users_external_identity
    ON users (external_system, external_id)
    WHERE external_system IS NOT NULL AND external_id IS NOT NULL;

ALTER TABLE tenant_members ADD COLUMN IF NOT EXISTS permission_revision VARCHAR(64) NOT NULL DEFAULT '';
