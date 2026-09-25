-- Chat pins move from the session row (chat_session.pinned_at, one flag every
-- viewer of a shared chat inherits) to the per-user sidebar pin table, so a
-- project member pinning a shared chat no longer pins it for everyone
-- (DENE-866). `chat` joins issue / project / view as a pinnable item type.
ALTER TABLE pinned_item DROP CONSTRAINT pinned_item_item_type_check;
ALTER TABLE pinned_item ADD CONSTRAINT pinned_item_item_type_check
    CHECK (item_type IN ('issue', 'project', 'view', 'chat'));

-- Backfill: every chat pinned under the old model becomes its creator's own
-- pin. Order is preserved — most recently pinned first, the way the old list
-- sorted — and appended after whatever the creator already had pinned.
INSERT INTO pinned_item (workspace_id, user_id, item_type, item_id, position, created_at)
SELECT cs.workspace_id,
       cs.creator_id,
       'chat',
       cs.id,
       COALESCE((SELECT MAX(p.position) FROM pinned_item p
                  WHERE p.workspace_id = cs.workspace_id AND p.user_id = cs.creator_id), 0)
         + ROW_NUMBER() OVER (PARTITION BY cs.workspace_id, cs.creator_id ORDER BY cs.pinned_at DESC),
       cs.pinned_at
FROM chat_session cs
WHERE cs.pinned_at IS NOT NULL
ON CONFLICT (workspace_id, user_id, item_type, item_id) DO NOTHING;

-- The old flag is no longer read or written; it stays as a column so the
-- workspace transfer bundle format keeps decoding older exports.
