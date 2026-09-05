DROP INDEX IF EXISTS uq_knowledge_external_identity;
ALTER TABLE knowledge DROP COLUMN content_hash;
ALTER TABLE knowledge DROP COLUMN source_revision;
ALTER TABLE knowledge DROP COLUMN external_id;
ALTER TABLE knowledge DROP COLUMN external_system;
