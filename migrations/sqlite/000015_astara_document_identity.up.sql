-- Astara stable document identity (SQLite dialect).
ALTER TABLE knowledge ADD COLUMN external_system TEXT;
ALTER TABLE knowledge ADD COLUMN external_id TEXT;
ALTER TABLE knowledge ADD COLUMN source_revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE knowledge ADD COLUMN content_hash TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX uq_knowledge_external_identity
    ON knowledge (knowledge_base_id, external_system, external_id)
    WHERE external_system IS NOT NULL AND external_id IS NOT NULL;
