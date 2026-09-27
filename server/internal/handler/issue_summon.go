package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// SummonIssueRequest is the body of POST /api/issues/{id}/summon — the one
// "叫人" entry behind `multica issue summon` (DENE-880). The call says only
// which ticket (the URL), who, and why.
type SummonIssueRequest struct {
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// SummonIssueResponse reports what landed, not what was asked for.
type SummonIssueResponse struct {
	SummonID      string   `json:"summon_id"`
	RecipientID   string   `json:"recipient_id"`
	RecipientName string   `json:"recipient_name"`
	Duplicate     bool     `json:"duplicate"`
	CommentID     string   `json:"comment_id,omitempty"`
	InboxItemID   string   `json:"inbox_item_id,omitempty"`
	Result        []string `json:"result"`
}

// WaitingSummonResponse is one row of GET /api/summons/waiting: a call to the
// current user they have not answered yet, on a ticket that is not finished.
// Stage 2's "等你" column reads only this.
type WaitingSummonResponse struct {
	ID            string  `json:"id"`
	IssueID       string  `json:"issue_id"`
	Identifier    string  `json:"identifier"`
	IssueTitle    string  `json:"issue_title"`
	IssueStatus   string  `json:"issue_status"`
	IssuePriority string  `json:"issue_priority"`
	CallerType    string  `json:"caller_type"`
	CallerID      *string `json:"caller_id"`
	CallerName    string  `json:"caller_name"`
	Source        string  `json:"source"`
	Reason        string  `json:"reason"`
	CommentID     *string `json:"comment_id"`
	InboxItemID   *string `json:"inbox_item_id"`
	CreatedAt     string  `json:"created_at"`
}

func (h *Handler) summoner() service.Summoner {
	return service.Summoner{Queries: h.Queries, TxStarter: h.TxStarter, Bus: h.Bus}
}

// summonPerson is the handler-side call into the entry. Failures are logged
// and reported as a zero result: every caller has already done its own
// durable write, and a missing notification must not undo it.
func (h *Handler) summonPerson(ctx context.Context, in service.SummonInput) (service.SummonResult, error) {
	out, err := h.summoner().Summon(ctx, in)
	if err != nil {
		slog.Warn("summon failed", "error", err, "issue_id", uuidToString(in.Issue.ID),
			"recipient_id", uuidToString(in.Recipient), "source", in.Source)
	}
	return out, err
}

// SummonIssue is `multica issue summon <id> --to <member> --reason "..."`.
func (h *Handler) SummonIssue(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req SummonIssueRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	to := strings.TrimSpace(req.To)
	if to == "" {
		writeError(w, http.StatusBadRequest, "缺 --to：叫谁（成员名字、邮箱或用户 UUID）")
		return
	}
	reason := strings.TrimSpace(sanitizeNullBytes(req.Reason))
	if reason == "" {
		writeError(w, http.StatusBadRequest, "缺 --reason：为什么叫他，一句话写清要他做什么决定或给什么信息")
		return
	}
	member, found, err := h.findWorkspaceMember(r.Context(), issue.WorkspaceID, to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load members failed")
		return
	}
	if !found {
		writeError(w, http.StatusBadRequest, "--to "+to+" 不是这个工作区的成员；只能叫人（智能体用 `multica issue handoff`）")
		return
	}
	actorType, actorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))
	out, err := h.summonPerson(r.Context(), service.SummonInput{
		Issue:      issue,
		Recipient:  member.UserID,
		CallerType: actorType,
		CallerID:   parseUUID(actorID),
		Source:     service.SummonSourceManual,
		Reason:     reason,
	})
	if err != nil {
		if errors.Is(err, service.ErrSummonNotMember) {
			writeError(w, http.StatusBadRequest, "--to "+to+" 不是这个工作区的成员")
			return
		}
		writeError(w, http.StatusInternalServerError, "summon failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, summonResponse(out, member.UserName))
}

func summonResponse(out service.SummonResult, name string) SummonIssueResponse {
	resp := SummonIssueResponse{
		SummonID:      uuidToString(out.Summon.ID),
		RecipientID:   uuidToString(out.Summon.RecipientID),
		RecipientName: name,
		Duplicate:     out.Duplicate,
		CommentID:     uuidToString(out.Summon.CommentID),
		InboxItemID:   uuidToString(out.Summon.InboxItemID),
	}
	if out.Duplicate {
		resp.Result = []string{"已经叫过 " + name + "（" + util.TimestampToString(out.Summon.CreatedAt) + "），他还没回复；这次没有重复通知"}
		return resp
	}
	resp.Result = []string{
		"已写进 " + name + " 的收件箱（需要你，最高紧急）",
		"已把 " + name + " 加进关注者",
		"票上留了一条带 @ 的评论",
		name + " 回复后会叫醒这张票的执行智能体",
	}
	return resp
}

// findWorkspaceMember resolves --to: user UUID, email, or name (exact,
// case-insensitive).
func (h *Handler) findWorkspaceMember(ctx context.Context, workspaceID pgtype.UUID, ref string) (db.ListMembersWithUserRow, bool, error) {
	members, err := h.Queries.ListMembersWithUser(ctx, workspaceID)
	if err != nil {
		return db.ListMembersWithUserRow{}, false, err
	}
	ref = strings.TrimSpace(strings.TrimPrefix(ref, "@"))
	for _, m := range members {
		if uuidToString(m.UserID) == ref || uuidToString(m.ID) == ref ||
			strings.EqualFold(m.UserEmail, ref) || strings.EqualFold(strings.TrimSpace(m.UserName), ref) {
			return m, true, nil
		}
	}
	return db.ListMembersWithUserRow{}, false, nil
}

// ListWaitingSummons is GET /api/summons/waiting: the current user's
// unanswered calls in this workspace, newest first. Visibility follows the
// inbox rule — a call on a ticket the viewer cannot see is not listed.
func (h *Handler) ListWaitingSummons(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace id")
	if !ok {
		return
	}
	rows, err := h.Queries.ListOpenIssueSummonsForRecipient(r.Context(), db.ListOpenIssueSummonsForRecipientParams{
		WorkspaceID: wsUUID,
		RecipientID: parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list summons")
		return
	}
	viewer, viewerErr := h.visibilityViewerFor(r, wsUUID)
	if viewerErr != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve viewer")
		return
	}
	prefix := h.getIssuePrefix(r.Context(), wsUUID)
	resp := make([]WaitingSummonResponse, 0, len(rows))
	for _, row := range rows {
		if !viewer.canSeeIssueFields(row.IssueID, row.IssueVisibility, row.IssueCreatorType, row.IssueCreatorID,
			row.IssueProjectID, row.IssueAssigneeType, row.IssueAssigneeID) {
			continue
		}
		resp = append(resp, WaitingSummonResponse{
			ID:            uuidToString(row.ID),
			IssueID:       uuidToString(row.IssueID),
			Identifier:    issueIdentifier(prefix, row.IssueNumber),
			IssueTitle:    row.IssueTitle,
			IssueStatus:   row.IssueStatus,
			IssuePriority: row.IssuePriority,
			CallerType:    row.CallerType,
			CallerID:      uuidToPtr(row.CallerID),
			CallerName:    row.CallerName,
			Source:        row.Source,
			Reason:        row.Reason,
			CommentID:     uuidToPtr(row.CommentID),
			InboxItemID:   uuidToPtr(row.InboxItemID),
			CreatedAt:     timestampToString(row.CreatedAt),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// summonNeedsHuman is the close/status side of --needs-human: the ticket
// already records who it waits on, and the entry makes that person actually
// hear it — inbox, subscription, a visible @ — instead of a metadata key
// nobody reads.
func (h *Handler) summonNeedsHuman(ctx context.Context, issue db.Issue, human, actorType, actorID, why string) {
	human = strings.TrimSpace(human)
	if human == "" {
		return
	}
	recipient, err := util.ParseUUID(human)
	if err != nil {
		return
	}
	reason := strings.TrimSpace(why)
	if reason == "" {
		reason = "这张票在等你拍板，执行智能体停下来了。"
	}
	callerID := pgtype.UUID{}
	if actorType == "member" || actorType == "agent" {
		callerID = parseUUID(actorID)
	} else {
		actorType = "system"
	}
	_, _ = h.summonPerson(ctx, service.SummonInput{
		Issue:      issue,
		Recipient:  recipient,
		CallerType: actorType,
		CallerID:   callerID,
		Source:     service.SummonSourceNeedsHuman,
		Reason:     reason,
	})
}

// recordMentionSummons turns a member @ in a person's or agent's comment into
// an open call, so the mentioned person's reply wakes the executor (DENE-880
// item 6: before this, a person @-ing another and getting an answer woke
// nobody). The mention listener already wrote the inbox row; this only adds
// the record. System comments are not callers — their @ is inert by design.
func (h *Handler) recordMentionSummons(ctx context.Context, issue db.Issue, comment db.Comment, actorType, actorID string) {
	if actorType != "member" && actorType != "agent" {
		return
	}
	seen := map[string]bool{}
	for _, m := range util.ParseMentions(comment.Content) {
		if m.Type != "member" || m.ID == actorID || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		recipient, err := util.ParseUUID(m.ID)
		if err != nil {
			continue
		}
		_, err = h.summoner().Summon(ctx, service.SummonInput{
			Issue:      issue,
			Recipient:  recipient,
			CallerType: actorType,
			CallerID:   parseUUID(actorID),
			Source:     service.SummonSourceMention,
			Reason:     mentionSummonReason(comment.Content),
			CommentID:  comment.ID,
			SkipInbox:  true,
		})
		if err != nil && !errors.Is(err, service.ErrSummonNotMember) {
			slog.Warn("summon: record mention failed", "error", err, "issue_id", uuidToString(issue.ID))
		}
	}
}

func mentionSummonReason(content string) string {
	text := strings.TrimSpace(util.MentionRe.ReplaceAllString(content, "@$1"))
	if text == "" {
		text = "在评论里 @ 了你"
	}
	return truncateRunes(text, 200)
}

// answerSummons marks the author's open calls on this ticket answered and,
// when the comment itself started no run, wakes whoever is waiting on the
// answer: the agent that made the call, or else the ticket's executor.
func (h *Handler) answerSummons(ctx context.Context, issue db.Issue, comment db.Comment, actorType, actorID string, woke bool) {
	if actorType != "member" {
		return
	}
	answered, err := h.Queries.AnswerIssueSummons(ctx, db.AnswerIssueSummonsParams{
		IssueID:         issue.ID,
		RecipientID:     parseUUID(actorID),
		AnswerCommentID: comment.ID,
	})
	if err != nil {
		slog.Warn("summon: answer failed", "error", err, "issue_id", uuidToString(issue.ID))
		return
	}
	service.SettleSummonInbox(ctx, h.Queries, h.Bus, answered)
	if len(answered) == 0 || woke {
		return
	}
	for _, s := range answered {
		if s.CallerType == "agent" && s.CallerID.Valid {
			h.triggerWaitingOnAgent(ctx, issue, s.CallerID, comment.ID)
			return
		}
	}
	h.dispatchWaitingOnAssigneeTrigger(ctx, issue, comment.ID)
}

func triggerOutcomesStartedRun(outcomes []CommentTriggerOutcome) bool {
	for _, o := range outcomes {
		if o.Status == DispatchQueued || o.Status == DispatchCoalesced {
			return true
		}
	}
	return false
}
