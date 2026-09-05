-- Astara stable document identity: external_system/external_id columns on
-- knowledges rows plus a version fence (source_revision) and content hash.
ALTER TABLE knowledges ADD COLUMN external_system VARCHAR(64) NULL;
ALTER TABLE knowledges ADD COLUMN external_id VARCHAR(255) NULL;
ALTER TABLE knowledges ADD COLUMN source_revision BIGINT NOT NULL DEFAULT 0;
ALTER TABLE knowledges ADD COLUMN content_hash VARCHAR(64) NOT NULL DEFAULT '';

CREATE UNIQUE INDEX uq_knowledge_external_identity
    ON knowledges (knowledge_base_id, external_system, external_id)
    WHERE external_system IS NOT NULL AND external_id IS NOT NULL;
