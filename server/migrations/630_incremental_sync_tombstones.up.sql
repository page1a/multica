-- Keep delete events long enough for high-frequency list clients to replay.
-- The table is intentionally independent of the deleted rows so cascades still
-- leave one durable event per resource.
CREATE TABLE IF NOT EXISTS incremental_sync_tombstone (
    resource TEXT NOT NULL CHECK (resource IN ('issues', 'inbox', 'chats')),
    id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    subject_id UUID,
    changed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (resource, id)
);

CREATE INDEX IF NOT EXISTS idx_incremental_sync_tombstone_page
    ON incremental_sync_tombstone (workspace_id, resource, changed_at, id);

CREATE OR REPLACE FUNCTION record_incremental_sync_tombstone()
RETURNS TRIGGER LANGUAGE plpgsql AS $function$
BEGIN
    INSERT INTO incremental_sync_tombstone(resource, id, workspace_id, subject_id, changed_at)
    VALUES (TG_ARGV[0], OLD.id, OLD.workspace_id,
            CASE WHEN TG_ARGV[0] = 'inbox' THEN (to_jsonb(OLD)->>'recipient_id')::uuid
                 WHEN TG_ARGV[0] = 'chats' THEN (to_jsonb(OLD)->>'creator_id')::uuid
                 ELSE NULL END, now())
    ON CONFLICT (resource, id) DO UPDATE
      SET workspace_id = EXCLUDED.workspace_id,
          subject_id = EXCLUDED.subject_id,
          changed_at = EXCLUDED.changed_at;
    RETURN OLD;
END
$function$;

DROP TRIGGER IF EXISTS trg_incremental_sync_issue_delete ON issue;
CREATE TRIGGER trg_incremental_sync_issue_delete
AFTER DELETE ON issue FOR EACH ROW
EXECUTE FUNCTION record_incremental_sync_tombstone('issues');

DROP TRIGGER IF EXISTS trg_incremental_sync_inbox_delete ON inbox_item;
CREATE TRIGGER trg_incremental_sync_inbox_delete
AFTER DELETE ON inbox_item FOR EACH ROW
EXECUTE FUNCTION record_incremental_sync_tombstone('inbox');

DROP TRIGGER IF EXISTS trg_incremental_sync_chat_delete ON chat_session;
CREATE TRIGGER trg_incremental_sync_chat_delete
AFTER DELETE ON chat_session FOR EACH ROW
EXECUTE FUNCTION record_incremental_sync_tombstone('chats');
