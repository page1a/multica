-- Validate the widened CHECK separately from migration 536 so the validation
-- scan does not inherit 536's ACCESS EXCLUSIVE lock. 536 only widened the
-- allowed set, so no pre-existing row can fail it.
ALTER TABLE issue VALIDATE CONSTRAINT issue_origin_type_check;
