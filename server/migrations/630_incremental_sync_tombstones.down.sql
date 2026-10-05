DROP TRIGGER IF EXISTS trg_incremental_sync_chat_delete ON chat_session;
DROP TRIGGER IF EXISTS trg_incremental_sync_inbox_delete ON inbox_item;
DROP TRIGGER IF EXISTS trg_incremental_sync_issue_delete ON issue;
DROP FUNCTION IF EXISTS record_incremental_sync_tombstone();
DROP TABLE IF EXISTS incremental_sync_tombstone;
