package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Summon sources: which scene called the person. The "等你" list shows it;
// nothing branches on it except the inbox type the routing notices keep.
const (
	SummonSourceManual     = "manual"      // `multica issue summon`
	SummonSourceNeedsHuman = "needs_human" // close/status with --needs-human
	SummonSourceRouting    = "routing"     // routing has nobody left to push the ticket
	SummonSourcePatrol     = "patrol"      // block-wait patrol could not seat a reviewer
	SummonSourceTimeLimit  = "time_limit"  // a run hit the workspace time limit
	SummonSourceQuotaRelay = "quota_relay" // quota tripped with no seat to relay to
	SummonSourceMention    = "mention"     // a person or agent @-mentioned a member
)

// InboxTypeNeedsYou is the inbox row the summon entry writes by default.
const InboxTypeNeedsYou = "needs_you"

// ErrSummonNotMember is returned when the person called is not a member of
// the ticket's workspace.
var ErrSummonNotMember = errors.New("summon: recipient is not a workspace member")

// SummonInput is the whole call: which ticket, who, why. The rest is how the
// scene already said it (an existing comment carrying the @, a mention the
// listener already delivered to the inbox, a legacy inbox type).
type SummonInput struct {
	Issue      db.Issue
	Recipient  pgtype.UUID
	CallerType string // member / agent / system
	CallerID   pgtype.UUID
	Source     string
	Reason     string
	// CommentID is a comment already on the ticket that carries the @ (a
	// routing notice, a member's own mention). Unset means the entry posts
	// its own system comment with the @.
	CommentID pgtype.UUID
	// NoComment is set when the caller posts its own comment carrying the @
	// and cannot hand over its id (routing notices written under their own
	// unique kind).
	NoComment bool
	// SkipInbox is set when CommentID is a member- or agent-authored comment
	// whose @ the mention listener already turned into an inbox row.
	SkipInbox bool
	// InboxType and InboxTitle override the defaults (needs_you, issue title)
	// for scenes whose inbox rows other code already queries by type.
	InboxType  string
	InboxTitle string
	Details    map[string]any
}

// SummonResult reports what landed. Duplicate means an unanswered call to
// the same person on the same ticket already exists; nothing new was written.
type SummonResult struct {
	Summon    db.IssueSummon
	Duplicate bool
	Comment   *db.Comment
	InboxItem *db.InboxItem
}

// Summoner is the one server entry for "this ticket needs this person"
// (DENE-880). One call writes the inbox row (highest severity), subscribes
// the person, leaves a visible @ on the ticket, and records the open call so
// a second call dedupes and the person's reply can wake the executor.
//
// The @ in a system comment never reaches the inbox on its own — the mention
// listener skips platform-authored bodies on purpose — so this entry writes
// the inbox row itself instead of relying on the comment.
type Summoner struct {
	Queries   *db.Queries
	TxStarter TxStarter
	Bus       *events.Bus
}

func (s Summoner) Summon(ctx context.Context, in SummonInput) (SummonResult, error) {
	var out SummonResult
	write := func(q *db.Queries) error {
		var err error
		out, err = SummonWith(ctx, q, in)
		return err
	}
	if s.TxStarter != nil {
		tx, err := s.TxStarter.Begin(ctx)
		if err != nil {
			return out, err
		}
		defer tx.Rollback(ctx)
		if err := write(s.Queries.WithTx(tx)); err != nil {
			return SummonResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return SummonResult{}, err
		}
	} else if err := write(s.Queries); err != nil {
		return SummonResult{}, err
	}
	s.Publish(in.Issue, out)
	return out, nil
}

// SummonWith runs the summon writes on q without opening a transaction or
// publishing. Callers already inside a transaction use it and call Publish
// after commit; everyone else uses Summoner.Summon.
func SummonWith(ctx context.Context, q *db.Queries, in SummonInput) (SummonResult, error) {
	var out SummonResult
	if !in.Issue.ID.Valid || !in.Recipient.Valid {
		return out, fmt.Errorf("summon: issue and recipient are required")
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return out, fmt.Errorf("summon: reason is required")
	}
	if _, err := q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID: in.Recipient, WorkspaceID: in.Issue.WorkspaceID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, ErrSummonNotMember
		}
		return out, err
	}
	callerType := in.CallerType
	if callerType == "" {
		callerType = "system"
	}
	source := in.Source
	if source == "" {
		source = SummonSourceManual
	}

	row, err := q.CreateIssueSummon(ctx, db.CreateIssueSummonParams{
		WorkspaceID: in.Issue.WorkspaceID,
		IssueID:     in.Issue.ID,
		RecipientID: in.Recipient,
		CallerType:  callerType,
		CallerID:    in.CallerID,
		Source:      source,
		Reason:      reason,
		CommentID:   in.CommentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		open, getErr := q.GetOpenIssueSummon(ctx, db.GetOpenIssueSummonParams{IssueID: in.Issue.ID, RecipientID: in.Recipient})
		if getErr != nil {
			return out, getErr
		}
		out.Summon, out.Duplicate = open, true
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Summon = row
	if _, err := q.AddIssueSubscriber(ctx, db.AddIssueSubscriberParams{
		IssueID: in.Issue.ID, UserType: "member", UserID: in.Recipient, Reason: "mentioned",
	}); err != nil {
		return out, err
	}
	commentID := in.CommentID
	if !commentID.Valid && !in.NoComment {
		created, err := q.CreateComment(ctx, db.CreateCommentParams{
			ID:          dbid.NewV7(),
			IssueID:     in.Issue.ID,
			WorkspaceID: in.Issue.WorkspaceID,
			AuthorType:  "system",
			AuthorID:    pgtype.UUID{Valid: true},
			Content:     summonMention(ctx, q, in.Recipient) + reason,
			Type:        "system",
		})
		if err != nil {
			return out, err
		}
		c := created.Comment()
		out.Comment = &c
		commentID = c.ID
	}
	var inboxID pgtype.UUID
	if !in.SkipInbox {
		item, err := q.CreateInboxItem(ctx, db.CreateInboxItemParams{
			ID:            dbid.NewV7(),
			WorkspaceID:   in.Issue.WorkspaceID,
			RecipientType: "member",
			RecipientID:   in.Recipient,
			Type:          firstNonEmpty(in.InboxType, InboxTypeNeedsYou),
			Severity:      "action_required",
			IssueID:       in.Issue.ID,
			Title:         firstNonEmpty(in.InboxTitle, in.Issue.Title),
			Body:          pgtype.Text{String: reason, Valid: true},
			ActorType:     pgtype.Text{String: callerType, Valid: true},
			ActorID:       in.CallerID,
			Details:       summonDetails(row, commentID, in.Details),
		})
		if err != nil {
			return out, err
		}
		out.InboxItem = &item
		inboxID = item.ID
	}
	out.Summon.CommentID = commentID
	out.Summon.InboxItemID = inboxID
	return out, q.SetIssueSummonDelivery(ctx, db.SetIssueSummonDeliveryParams{
		ID: row.ID, CommentID: commentID, InboxItemID: inboxID,
	})
}

// Publish broadcasts what a summon wrote. SummonWith callers inside their own
// transaction call it after commit.
func (s Summoner) Publish(issue db.Issue, out SummonResult) {
	if s.Bus == nil || out.Duplicate {
		return
	}
	wsID := util.UUIDToString(issue.WorkspaceID)
	if out.Comment != nil {
		s.Bus.Publish(events.Event{
			Type:        protocol.EventCommentCreated,
			WorkspaceID: wsID,
			ActorType:   "system",
			Payload:     map[string]any{"comment": *out.Comment, "issue_title": issue.Title},
		})
	}
	if out.InboxItem != nil {
		item := *out.InboxItem
		s.Bus.Publish(events.Event{
			Type:        protocol.EventInboxNew,
			WorkspaceID: wsID,
			ActorType:   item.ActorType.String,
			ActorID:     util.UUIDToString(item.ActorID),
			Payload: map[string]any{"item": map[string]any{
				"id":             util.UUIDToString(item.ID),
				"workspace_id":   util.UUIDToString(item.WorkspaceID),
				"recipient_type": item.RecipientType,
				"recipient_id":   util.UUIDToString(item.RecipientID),
				"type":           item.Type,
				"severity":       item.Severity,
				"issue_id":       util.UUIDToPtr(item.IssueID),
				"title":          item.Title,
				"body":           util.TextToPtr(item.Body),
				"read":           item.Read,
				"archived":       item.Archived,
				"created_at":     util.TimestampToString(item.CreatedAt),
				"actor_type":     util.TextToPtr(item.ActorType),
				"actor_id":       util.UUIDToPtr(item.ActorID),
				"details":        json.RawMessage(item.Details),
				"issue_status":   issue.Status,
			}},
		})
	}
}

// summonMention is the "[@Name](mention://member/<id>) " prefix for a member.
func summonMention(ctx context.Context, q *db.Queries, userID pgtype.UUID) string {
	name := "member"
	if u, err := q.GetUser(ctx, userID); err == nil {
		if cleaned := strings.TrimSpace(strings.NewReplacer("[", "", "]", "").Replace(u.Name)); cleaned != "" {
			name = cleaned
		}
	}
	return fmt.Sprintf("[@%s](mention://member/%s) ", name, util.UUIDToString(userID))
}

func summonDetails(row db.IssueSummon, commentID pgtype.UUID, extra map[string]any) []byte {
	details := map[string]any{}
	for k, v := range extra {
		details[k] = v
	}
	details["summon_id"] = util.UUIDToString(row.ID)
	details["source"] = row.Source
	details["reason"] = row.Reason
	if commentID.Valid {
		details["comment_id"] = util.UUIDToString(commentID)
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return []byte("{}")
	}
	return raw
}

func (s *TaskService) summoner() Summoner {
	return Summoner{Queries: s.Queries, TxStarter: s.TxStarter, Bus: s.Bus}
}

// remindSummonAnswerClose is the last half of the summon loop: a person
// answered, their reply woke the executor, and that run ended with the ticket
// still blocked — the executor read the answer but never closed again. It is
// told once, with a fresh run, to carry on and close. Keyed on the answer
// comment, so the reminder run (triggered by its own notice) never reminds.
func (s *TaskService) remindSummonAnswerClose(ctx context.Context, task db.AgentTaskQueue) {
	if !task.IssueID.Valid || !task.TriggerCommentID.Valid || !task.AgentID.Valid {
		return
	}
	if _, err := s.Queries.ClaimIssueSummonReminder(ctx, task.TriggerCommentID); err != nil {
		return
	}
	issue, err := s.Queries.GetIssue(ctx, task.IssueID)
	if err != nil || issue.Status != "blocked" {
		return
	}
	notice := s.createSystemNoticeComment(ctx, issue, "人已经回复，这次运行结束时票还停在 blocked。按回复继续推进，做完用 `multica issue close` 重新收口。")
	if notice == nil {
		return
	}
	if _, err := s.EnqueueTaskForMention(ctx, issue, task.AgentID, notice.ID, OriginDerived); err != nil {
		slog.Warn("summon reminder: enqueue failed", "issue_id", util.UUIDToString(issue.ID), "error", err)
	}
}

// SettleSummonInbox reads the inbox rows the given calls hang on, once the
// calls are answered or closed (DENE-901). Those rows are the ones the board's
// mark-all and the ticket's mark-read leave alone; without this they would
// stay unread forever. Each recipient's clients hear an inbox:batch-read so
// the badge follows at once.
func SettleSummonInbox(ctx context.Context, q *db.Queries, bus *events.Bus, summons []db.IssueSummon) {
	if len(summons) == 0 {
		return
	}
	ids := make([]pgtype.UUID, 0, len(summons))
	for _, s := range summons {
		ids = append(ids, s.ID)
	}
	rows, err := q.MarkIssueSummonInboxRead(ctx, ids)
	if err != nil {
		slog.Warn("summon: read inbox rows failed", "error", err)
		return
	}
	if bus == nil {
		return
	}
	type recipientKey struct{ workspace, recipient string }
	counts := map[recipientKey]int{}
	for _, row := range rows {
		counts[recipientKey{util.UUIDToString(row.WorkspaceID), util.UUIDToString(row.RecipientID)}]++
	}
	for k, n := range counts {
		bus.Publish(events.Event{
			Type:        protocol.EventInboxBatchRead,
			WorkspaceID: k.workspace,
			ActorType:   "system",
			Payload:     map[string]any{"recipient_id": k.recipient, "count": n},
		})
	}
}

// CloseSummonsOnProgress closes the calls a ticket no longer waits on
// (DENE-901): every call once the ticket is done or cancelled, and the
// actor's own calls when the person called changed the ticket's status or
// owner themselves — the ticket moved on without a reply. The rows those
// calls hang on are read with them.
func CloseSummonsOnProgress(ctx context.Context, q *db.Queries, bus *events.Bus, issueID pgtype.UUID, actorType, actorID string, statusChanged, assigneeChanged bool) {
	if !issueID.Valid || (!statusChanged && !assigneeChanged) {
		return
	}
	issue, err := q.GetIssue(ctx, issueID)
	if err != nil {
		return
	}
	params := db.CloseOpenIssueSummonsParams{IssueID: issue.ID}
	switch {
	case issue.Status == "done" || issue.Status == "cancelled":
	case actorType == "member":
		recipient, err := util.ParseUUID(actorID)
		if err != nil {
			return
		}
		params.RecipientID = recipient
	default:
		return
	}
	closed, err := q.CloseOpenIssueSummons(ctx, params)
	if err != nil {
		slog.Warn("summon: close on progress failed", "error", err, "issue_id", util.UUIDToString(issue.ID))
		return
	}
	SettleSummonInbox(ctx, q, bus, closed)
}
