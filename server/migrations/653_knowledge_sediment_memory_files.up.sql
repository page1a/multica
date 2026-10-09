-- What a sediment deleted (DENE-1681): git's account of the delivered memory
-- files at the time — [{path, bytes, deleted, supersede_marks}] — so the
-- project's monitor can say what each delivery removed, not only what it
-- added. Additive column with a default; the previous release neither reads
-- nor writes it, and older rows stay '[]' (unknown).
ALTER TABLE knowledge_sediment
    ADD COLUMN memory_files JSONB NOT NULL DEFAULT '[]'::jsonb;
