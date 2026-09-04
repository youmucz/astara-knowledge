DROP INDEX IF EXISTS uq_knowledge_bases_external_identity;
DROP INDEX IF EXISTS uq_tenants_external_identity;
ALTER TABLE knowledge_bases DROP COLUMN IF EXISTS external_id;
ALTER TABLE knowledge_bases DROP COLUMN IF EXISTS external_system;
ALTER TABLE tenants DROP COLUMN IF EXISTS external_id;
ALTER TABLE tenants DROP COLUMN IF EXISTS external_system;
