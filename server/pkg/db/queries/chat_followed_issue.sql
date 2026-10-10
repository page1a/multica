-- name: FollowIssueFromChat :execrows
-- Auto-follow (DENE-1719): the chat's run acted on the issue. Never revives a
-- hand-made take-down and never overwrites a manual pin.
INSERT INTO chat_followed_issue (chat_session_id, issue_id, workspace_id, source)
VALUES (@chat_session_id, @issue_id, @workspace_id, 'auto')
ON CONFLICT (chat_session_id, issue_id) DO NOTHING;

-- name: SetChatFollowedIssue :exec
-- Pin (hidden = false) or take down (hidden = true) an issue by hand, the
-- one write behind the chat bar's buttons and `multica chat tickets add|remove`.
INSERT INTO chat_followed_issue (chat_session_id, issue_id, workspace_id, source, hidden)
VALUES (@chat_session_id, @issue_id, @workspace_id, 'manual', @hidden)
ON CONFLICT (chat_session_id, issue_id) DO UPDATE
SET source = 'manual', hidden = EXCLUDED.hidden, updated_at = now();

-- name: ListChatSessionTicketIssues :many
-- A chat's tickets (`multica chat tickets`): the issues it opened (source
-- 'created') and the ones it follows ('auto' / 'manual'), minus the ones taken
-- down by hand, in the order they joined the chat.
SELECT sqlc.embed(i), l.source::text AS source, l.linked_at::timestamptz AS linked_at
FROM (
    SELECT DISTINCT ON (u.issue_id) u.issue_id, u.source, u.linked_at
    FROM (
        SELECT o.id AS issue_id, 'created' AS source, o.created_at AS linked_at, 0 AS rank
        FROM issue o
        WHERE o.workspace_id = @workspace_id AND o.origin_chat_session_id = @chat_session_id::uuid
        UNION ALL
        SELECT f.issue_id, f.source, f.created_at, 1
        FROM chat_followed_issue f
        WHERE f.chat_session_id = @chat_session_id::uuid AND f.workspace_id = @workspace_id AND NOT f.hidden
    ) u
    ORDER BY u.issue_id, u.rank
) l
JOIN issue i ON i.id = l.issue_id AND i.workspace_id = @workspace_id
WHERE NOT EXISTS (
    SELECT 1 FROM chat_followed_issue h
    WHERE h.chat_session_id = @chat_session_id::uuid AND h.issue_id = l.issue_id AND h.hidden
)
ORDER BY l.linked_at ASC, i.id ASC
LIMIT 200;
