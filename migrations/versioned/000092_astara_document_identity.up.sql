-- Astara stable document identity: external_system/external_id columns on
-- knowledge rows plus a version fence (source_revision) and content hash.
ALTER TABLE knowledge ADD COLUMN external_system VARCHAR(64) NULL;
ALTER TABLE knowledge ADD COLUMN external_id VARCHAR(255) NULL;
ALTER TABLE knowledge ADD COLUMN source_revision BIGINT NOT NULL DEFAULT 0;
ALTER TABLE knowledge ADD COLUMN content_hash VARCHAR(64) NOT NULL DEFAULT '';

CREATE UNIQUE INDEX uq_knowledge_external_identity
    ON knowledge (knowledge_base_id, external_system, external_id)
    WHERE external_system IS NOT NULL AND external_id IS NOT NULL;
