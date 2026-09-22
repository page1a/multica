-- Safe rollback: narrow every entry to private before the key is dropped, for
-- the same reason as 503 and 504 — a rolled-back server has no visibility
-- filter at all, and the audit trail (migration 512) is what replays the
-- narrowing after a roll forward.
UPDATE workspace
SET repos = COALESCE((
        SELECT jsonb_agg((entry - 'visibility' - 'created_by') ORDER BY ordinality)
        FROM jsonb_array_elements(repos) WITH ORDINALITY AS t(entry, ordinality)
        WHERE jsonb_typeof(entry) = 'object'
    ), '[]'::jsonb)
WHERE jsonb_typeof(repos) = 'array'
  AND jsonb_array_length(repos) > 0;
