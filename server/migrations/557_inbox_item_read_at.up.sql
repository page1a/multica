-- When a notification was read (DENE-975). The inbox board replays a visit's
-- unread snapshot from it: rows read after the visit began still count as new
-- for that visit. NULL for unread rows and for rows read before this column.
ALTER TABLE inbox_item ADD COLUMN read_at TIMESTAMPTZ;
