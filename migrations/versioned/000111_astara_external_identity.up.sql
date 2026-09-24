ALTER TABLE tenants ADD COLUMN IF NOT EXISTS external_system VARCHAR(64);
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS external_id VARCHAR(255);
ALTER TABLE knowledge_bases ADD COLUMN IF NOT EXISTS external_system VARCHAR(64);
ALTER TABLE knowledge_bases ADD COLUMN IF NOT EXISTS external_id VARCHAR(255);

CREATE UNIQUE INDEX IF NOT EXISTS uq_tenants_external_identity
    ON tenants (external_system, external_id)
    WHERE external_system IS NOT NULL AND external_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_knowledge_bases_external_identity
    ON knowledge_bases (tenant_id, external_system, external_id)
    WHERE external_system IS NOT NULL AND external_id IS NOT NULL;
