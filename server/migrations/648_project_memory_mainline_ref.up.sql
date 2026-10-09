-- DENE-1660: a checklist slot missing from the local directory but present on
-- the remote mainline means the local directory is behind, not that the memory
-- was never written. The daemon records which ref already has it.
ALTER TABLE project_memory_status ADD COLUMN IF NOT EXISTS mainline_ref TEXT;
