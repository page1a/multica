-- Write the creator's own chat pins back onto the session row, then drop the
-- chat pin type. Other viewers' pins have no home in the old model and are lost.
UPDATE chat_session cs
   SET pinned_at = COALESCE(cs.pinned_at, p.created_at)
  FROM pinned_item p
 WHERE p.item_type = 'chat'
   AND p.item_id = cs.id
   AND p.user_id = cs.creator_id;

DELETE FROM pinned_item WHERE item_type = 'chat';

ALTER TABLE pinned_item DROP CONSTRAINT pinned_item_item_type_check;
ALTER TABLE pinned_item ADD CONSTRAINT pinned_item_item_type_check
    CHECK (item_type IN ('issue', 'project', 'view'));
