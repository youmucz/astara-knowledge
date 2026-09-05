DROP INDEX IF EXISTS uq_knowledge_external_identity;
ALTER TABLE knowledges DROP COLUMN content_hash;
ALTER TABLE knowledges DROP COLUMN source_revision;
ALTER TABLE knowledges DROP COLUMN external_id;
ALTER TABLE knowledges DROP COLUMN external_system;
