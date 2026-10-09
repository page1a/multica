package service

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// chatOriginTypes are the origins whose origin_id is the chat itself: the IM
// `/issue` commands and the alignment draft's root.
var chatOriginTypes = map[string]bool{
	"lark_chat": true, "slack_chat": true, "dingtalk_chat": true,
	"wecom_chat": true, "telegram_chat": true, "issue_draft": true,
}

// chatOriginSession is the chat an issue opened by an IM `/issue` command or
// an alignment came from (DENE-1672), so its receipts flow back there like a
// chat run's. Unset when the origin names no chat of this workspace.
func chatOriginSession(ctx context.Context, q *db.Queries, p IssueCreateParams) pgtype.UUID {
	if !p.OriginType.Valid || !chatOriginTypes[p.OriginType.String] || !p.OriginID.Valid {
		return pgtype.UUID{}
	}
	cs, err := q.GetChatSession(ctx, p.OriginID)
	if err != nil || cs.WorkspaceID != p.WorkspaceID {
		return pgtype.UUID{}
	}
	return cs.ID
}
