-- Which alignment capabilities a draft was opened with, and which version of
-- the capability library its prompt was assembled from.
--
-- Alignment capabilities are the built-in methods the carrier may reach for —
-- wayfinder, grill, grilling, grill-frontend-look (issue_draft_capability.go).
-- They are chosen per conversation: the create dialog's skill checkboxes are the
-- input, and the prompt installed on the carrier is assembled from the resolved
-- set. Both values are recorded here for the same reason `policy_key` /
-- `policy_version` are recorded next door — a finished alignment has to be able
-- to answer "which prompt produced this issue" after the registry has moved on.
--
-- Two columns rather than one because the assembled prompt has three parts:
-- the shared contract and the policy's behaviour, which `policy_version` pins,
-- and the capability fragments, which it cannot — the same policy version is
-- recorded by drafts that selected different capabilities. `capability_keys`
-- names the methods; `capability_version` names their text.
--
-- The defaults backfill the drafts that already existed. Every one of them was
-- created before the library did, so it ran no fragments: an empty key list is
-- the accurate record, and the version it carries is inert — there is no text
-- for it to name.
ALTER TABLE issue_draft
    ADD COLUMN IF NOT EXISTS capability_keys TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS capability_version TEXT NOT NULL DEFAULT '1';
