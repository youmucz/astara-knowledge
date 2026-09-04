ALTER TABLE tenants ADD COLUMN external_system TEXT;
ALTER TABLE tenants ADD COLUMN external_id TEXT;
ALTER TABLE knowledge_bases ADD COLUMN external_system TEXT;
ALTER TABLE knowledge_bases ADD COLUMN external_id TEXT;

CREATE UNIQUE INDEX uq_tenants_external_identity
    ON tenants (external_system, external_id)
    WHERE external_system IS NOT NULL AND external_id IS NOT NULL;
CREATE UNIQUE INDEX uq_knowledge_bases_external_identity
    ON knowledge_bases (tenant_id, external_system, external_id)
    WHERE external_system IS NOT NULL AND external_id IS NOT NULL;
