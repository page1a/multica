-- 验收席 becomes a first-class field on the issue, alongside the assignee pair.
--
-- It used to be a workspace `select` property whose options were seat NAMES,
-- copied off the roster when the slot was provisioned. That copy is what made
-- it wrong: renaming a seat, archiving one, or hiring one left the option list
-- describing a roster that no longer exists, and a ticket could hold a value
-- that pointed at nobody. Reviewer is a reference to an actor, exactly like
-- assignee, so it follows the roster instead of remembering it.
--
-- reviewer_type: 'agent' / 'member' reference agent(id) / "user"(id) in
-- reviewer_id, mirroring assignee_type. 'none' is the written answer for "this
-- ticket needs no acceptance pass" and carries no id — it has to be a value
-- rather than an empty slot, or routing would re-judge the slot on every later
-- status change. NULL is the empty slot, the only state routing may write.
ALTER TABLE issue ADD COLUMN IF NOT EXISTS reviewer_type TEXT;
ALTER TABLE issue ADD COLUMN IF NOT EXISTS reviewer_id UUID;

-- Backfill from the select property, one option meaning at a time. Every
-- statement is conditional on the slot still being empty, so re-running the
-- migration cannot overwrite a value a person has since set by hand.

-- 「不需要验收」 -> 'none'.
UPDATE issue i
SET reviewer_type = 'none'
FROM issue_property p,
     LATERAL jsonb_array_elements(COALESCE(p.config -> 'options', '[]'::jsonb)) o
WHERE p.workspace_id = i.workspace_id
  AND p.name = '验收席'
  AND p.type = 'select'
  AND i.reviewer_type IS NULL
  AND i.properties ->> (p.id::text) = (o ->> 'id')
  AND o ->> 'name' = '不需要验收';

-- A seat name -> that agent. Matching by name is correct exactly once: here,
-- while the names in the option list are still the names the seats had.
UPDATE issue i
SET reviewer_type = 'agent', reviewer_id = a.id
FROM issue_property p,
     LATERAL jsonb_array_elements(COALESCE(p.config -> 'options', '[]'::jsonb)) o,
     agent a
WHERE p.workspace_id = i.workspace_id
  AND p.name = '验收席'
  AND p.type = 'select'
  AND i.reviewer_type IS NULL
  AND i.properties ->> (p.id::text) = (o ->> 'id')
  AND a.workspace_id = i.workspace_id
  AND a.name = o ->> 'name';

-- 「交给人」 named no person, because a select option could not hold one. The
-- person it meant is the one routing would have notified: the creator, or the
-- workspace owner when an agent created the ticket.
UPDATE issue i
SET reviewer_type = 'member',
    reviewer_id = COALESCE(
        CASE WHEN i.creator_type = 'member' THEN i.creator_id END,
        (SELECT m.user_id FROM member m
          WHERE m.workspace_id = i.workspace_id AND m.role = 'owner'
          ORDER BY m.created_at ASC LIMIT 1)
    )
FROM issue_property p,
     LATERAL jsonb_array_elements(COALESCE(p.config -> 'options', '[]'::jsonb)) o
WHERE p.workspace_id = i.workspace_id
  AND p.name = '验收席'
  AND p.type = 'select'
  AND i.reviewer_type IS NULL
  AND i.properties ->> (p.id::text) = (o ->> 'id')
  AND o ->> 'name' = '交给人';

-- A 'member' row whose person could not be resolved (no owner, no member
-- creator) would be a reviewer slot holding a dangling reference. Empty is the
-- truthful state: routing will decide it again.
UPDATE issue SET reviewer_type = NULL
WHERE reviewer_type = 'member' AND reviewer_id IS NULL;

ALTER TABLE issue DROP CONSTRAINT IF EXISTS issue_reviewer_type_check;
ALTER TABLE issue ADD CONSTRAINT issue_reviewer_type_check
    CHECK (reviewer_type IS NULL OR reviewer_type IN ('member', 'agent', 'none'));

-- The property is retired, not deleted: archiving keeps every value that was
-- ever written while taking the slot out of the picker, so a workspace does
-- not end up with two reviewer fields that disagree.
UPDATE issue_property
SET archived_at = now(), updated_at = now()
WHERE name = '验收席' AND type = 'select' AND archived_at IS NULL;
