-- Issues created by `multica plan apply` (DENE-864) carry origin_type 'plan'
-- and an origin_id derived from the plan key and the node's key, so applying
-- the same plan again finds every issue it already created instead of
-- creating the tree a second time.
--
-- Same widening pattern as 484/485: recreate the CHECK as NOT VALID here and
-- VALIDATE it in 537, so the ACCESS EXCLUSIVE lock on issue is not held
-- through a full-table scan.
ALTER TABLE issue DROP CONSTRAINT IF EXISTS issue_origin_type_check;
ALTER TABLE issue ADD CONSTRAINT issue_origin_type_check
    CHECK (origin_type IN ('autopilot', 'quick_create', 'lark_chat', 'slack_chat',
      'agent_create', 'dingtalk_chat', 'wecom_chat', 'telegram_chat', 'issue_draft',
      'plan'))
    NOT VALID;
