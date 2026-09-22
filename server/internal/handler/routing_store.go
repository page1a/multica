package handler

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// routingStore binds the routing module to this server. Everything the module
// is allowed to touch passes through here, which is also why the module cannot
// write a status: there is no method for it.
type routingStore struct{ h *Handler }

// RoutingStore returns the module's view of this handler.
func (h *Handler) RoutingStore() routing.Store { return routingStore{h: h} }

func (s routingStore) Settings(ctx context.Context, workspaceID string) (routing.Settings, error) {
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return routing.Settings{}, err
	}
	ws, err := s.h.Queries.GetWorkspace(ctx, wsID)
	if err != nil {
		return routing.Settings{}, err
	}
	settings := routing.ParseSettings(ws.Settings)
	// Opened here, at the edge, so nothing above this line ever holds the
	// ciphertext and nothing below ever has to know there was one. An
	// unopenable value yields the empty string, which means "no workspace
	// key" and sends the call to the deployment gateway instead.
	settings.APIKey = s.h.openRoutingKey(settings.APIKeyEnc)
	return settings, nil
}

func (s routingStore) Issue(ctx context.Context, workspaceID, issueID string) (routing.Issue, error) {
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return routing.Issue{}, err
	}
	id, err := util.ParseUUID(issueID)
	if err != nil {
		return routing.Issue{}, err
	}
	row, err := s.h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: id, WorkspaceID: wsID})
	if err != nil {
		return routing.Issue{}, err
	}
	return s.issueView(ctx, row)
}

// descriptionSummaryLimit bounds what leaves the deployment. The judge needs
// enough to tell a one-liner from a project; it does not need the body.
const descriptionSummaryLimit = 800

func (s routingStore) issueView(ctx context.Context, row db.Issue) (routing.Issue, error) {
	out := routing.Issue{
		ID:                 util.UUIDToString(row.ID),
		Title:              row.Title,
		DescriptionSummary: clipRunes(row.Description.String, descriptionSummaryLimit),
		// The state table is written against the seven status categories, so a
		// workspace that renamed or added statuses routes identically to one
		// that did not.
		Status:       issuestatus.Effective(ctx, s.h.Queries, row.WorkspaceID, row.Status),
		AssigneeType: row.AssigneeType.String,
		CreatorType:  row.CreatorType,
		CreatorID:    util.UUIDToString(row.CreatorID),
	}
	// Falls back to updated_at exactly as ListStaleReviewIssues does, so the
	// single-issue re-check cannot disagree with the query that selected it.
	switch {
	case row.LastActivityAt.Valid:
		out.LastActivityAt = row.LastActivityAt.Time
	case row.UpdatedAt.Valid:
		out.LastActivityAt = row.UpdatedAt.Time
	}
	if !row.AssigneeType.Valid {
		out.AssigneeType = ""
	}
	if row.AssigneeID.Valid {
		out.AssigneeID = util.UUIDToString(row.AssigneeID)
	}
	if row.ProjectID.Valid {
		out.ProjectID = util.UUIDToString(row.ProjectID)
		if p, err := s.h.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{
			ID: row.ProjectID, WorkspaceID: row.WorkspaceID,
		}); err == nil {
			out.ProjectName = p.Title
		}
	}
	if labels, err := s.h.Queries.ListLabelsByIssue(ctx, db.ListLabelsByIssueParams{
		IssueID: row.ID, WorkspaceID: row.WorkspaceID,
	}); err == nil {
		for _, l := range labels {
			out.Labels = append(out.Labels, l.Name)
		}
	}
	if row.ParentIssueID.Valid {
		if parent, err := s.h.Queries.GetIssue(ctx, row.ParentIssueID); err == nil &&
			parent.AssigneeType.Valid && parent.AssigneeType.String == "agent" && parent.AssigneeID.Valid {
			if agent, err := s.h.Queries.GetAgent(ctx, parent.AssigneeID); err == nil {
				out.ParentExecutor = agent.Name
			}
		}
	}
	out.Reviewer = s.reviewerRef(ctx, row)
	return out, nil
}

// reviewerRef reads the reviewer pair off the issue and resolves the display
// name from the roster. The name is resolved on every read and stored nowhere,
// which is what a reference buys over the old select option: renaming a seat
// renames it on every ticket, and archiving one is visible instead of leaving
// a ticket holding a word that no longer means anybody.
func (s routingStore) reviewerRef(ctx context.Context, row db.Issue) routing.ReviewerRef {
	if !row.ReviewerType.Valid || row.ReviewerType.String == "" {
		return routing.ReviewerRef{}
	}
	ref := routing.ReviewerRef{Kind: routing.ReviewerTarget(row.ReviewerType.String)}
	if ref.Kind == routing.ReviewerNoReview {
		return ref
	}
	if !row.ReviewerID.Valid {
		// A pair with a type but no id is not a reviewer. Reporting it as an
		// empty slot lets routing decide again rather than hand off to
		// nobody.
		return routing.ReviewerRef{}
	}
	ref.ID = util.UUIDToString(row.ReviewerID)
	switch ref.Kind {
	case routing.ReviewerAgent:
		if agent, err := s.h.Queries.GetAgent(ctx, row.ReviewerID); err == nil {
			ref.Name = agent.Name
		}
	case routing.ReviewerMember:
		if u, err := s.h.Queries.GetUser(ctx, row.ReviewerID); err == nil {
			ref.Name = u.Name
		}
	default:
		// An unknown reviewer_type is not something this server can hand off
		// to. Fail closed: read it as empty.
		return routing.ReviewerRef{}
	}
	return ref
}

func (s routingStore) Roster(ctx context.Context, workspaceID string) (map[string]routing.Agent, error) {
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return nil, err
	}
	agents, err := s.h.Queries.ListAgents(ctx, wsID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]routing.Agent, len(agents))
	for _, a := range agents {
		// Disabled seats stay on the agents list but are not routing
		// candidates (DENE-714). Archive is already excluded by ListAgents.
		if !a.WorkEnabled {
			continue
		}
		out[a.Name] = routing.Agent{
			ID:   util.UUIDToString(a.ID),
			Name: a.Name,
			Tier: a.RoutingTier.String,
		}
	}
	return out, nil
}

func (s routingStore) AssignAgentIfUnassigned(ctx context.Context, workspaceID, issueID string, seat routing.Seat) (bool, error) {
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return false, err
	}
	id, err := util.ParseUUID(issueID)
	if err != nil {
		return false, err
	}
	seatID, err := util.ParseUUID(seat.ID)
	if err != nil {
		return false, err
	}
	issue, err := s.h.Queries.AssignIssueIfUnassigned(ctx, db.AssignIssueIfUnassignedParams{
		ID: id, WorkspaceID: wsID, AssigneeType: "agent", AssigneeID: seatID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Somebody else filled the slot between the read and this write —
		// which is exactly what the guard exists for. Not an error, and not
		// an assignment this call may claim.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// The conditional write only fires while the slot is empty, so the value
	// this call replaced is known without a second read: nothing.
	prev := issue
	prev.AssigneeType = pgtype.Text{}
	prev.AssigneeID = pgtype.UUID{}
	s.publishIssueUpdated(prev, issue)
	// Assignment IS the wake-up: the seat's run starts from the assignment,
	// so routing never needs to mention an agent it just dispatched.
	s.h.IssueService.StartAssignedAgent(ctx, issue)
	return true, nil
}

func (s routingStore) SetReviewerIfUnset(ctx context.Context, workspaceID, issueID string, ref routing.ReviewerRef) (bool, error) {
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return false, err
	}
	id, err := util.ParseUUID(issueID)
	if err != nil {
		return false, err
	}
	if ref.Empty() {
		// Routing never writes an empty slot: an empty slot is what it writes
		// INTO. A caller that reaches here has nothing to say.
		return false, nil
	}
	params := db.SetIssueReviewerIfUnsetParams{
		ID: id, WorkspaceID: wsID, ReviewerType: string(ref.Kind),
	}
	if ref.Kind != routing.ReviewerNoReview {
		refID, err := util.ParseUUID(ref.ID)
		if err != nil {
			return false, err
		}
		params.ReviewerID = refID
	}
	issue, err := s.h.Queries.SetIssueReviewerIfUnset(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// A reviewer write, not an assignment: prev == next on the assignee pair,
	// so assignee_changed comes out false and no spurious owner-change lands
	// in the timeline.
	s.publishIssueUpdated(issue, issue)
	return true, nil
}

func (s routingStore) Handoff(ctx context.Context, workspaceID, issueID, assigneeType, assigneeID string) error {
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return err
	}
	id, err := util.ParseUUID(issueID)
	if err != nil {
		return err
	}
	target, err := util.ParseUUID(assigneeID)
	if err != nil {
		return err
	}
	// Read the row this write replaces. A handoff is the one routing write
	// that overwrites an existing owner, so the previous pair has to come
	// from the database rather than be inferred.
	prev, err := s.h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: id, WorkspaceID: wsID})
	if err != nil {
		return err
	}
	issue, err := s.h.Queries.ReassignIssue(ctx, db.ReassignIssueParams{
		ID: id, WorkspaceID: wsID, AssigneeType: assigneeType, AssigneeID: target,
	})
	if err != nil {
		return err
	}
	s.publishIssueUpdated(prev, issue)
	if assigneeType == "agent" {
		s.h.IssueService.StartAssignedAgent(ctx, issue)
	}
	return nil
}

func (s routingStore) HasComment(ctx context.Context, workspaceID, issueID string, kind routing.CommentKind) (bool, error) {
	id, err := util.ParseUUID(issueID)
	if err != nil {
		return false, err
	}
	return s.h.Queries.HasRoutingComment(ctx, db.HasRoutingCommentParams{
		IssueID: id, RoutingKind: string(kind),
	})
}

func (s routingStore) PostComment(ctx context.Context, workspaceID, issueID string, kind routing.CommentKind, body string) (bool, error) {
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return false, err
	}
	id, err := util.ParseUUID(issueID)
	if err != nil {
		return false, err
	}
	// author_type='system', author_id=zero UUID, matching the other
	// platform-authored comments. Clients branch on author_type.
	comment, err := s.h.Queries.CreateRoutingComment(ctx, db.CreateRoutingCommentParams{
		IssueID:     id,
		WorkspaceID: wsID,
		AuthorID:    pgtype.UUID{Valid: true},
		Content:     body,
		RoutingKind: string(kind),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The unique index rejected a duplicate: a concurrent Route call got
		// there first. Nothing to publish, and the caller must not notify.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if s.h.Bus != nil {
		s.h.Bus.Publish(events.Event{
			Type:        protocol.EventCommentCreated,
			WorkspaceID: workspaceID,
			ActorType:   "system",
			Payload: map[string]any{
				"comment": map[string]any{
					"id":          util.UUIDToString(comment.ID),
					"issue_id":    util.UUIDToString(comment.IssueID),
					"author_type": comment.AuthorType,
					"author_id":   util.UUIDToString(comment.AuthorID),
					"content":     comment.Content,
					"type":        comment.Type,
					"revision":    comment.Revision,
				},
			},
		})
	}
	return true, nil
}

// Subscribe adds the notification target to the issue and writes the inbox row
// that actually reaches them.
//
// Both halves are needed. The mention in the comment body is what a reader
// sees, but the notification and subscriber listeners short-circuit on
// author_type='system' — so a routing comment's mention notifies nobody on its
// own, and nothing on the ticket shows the difference. The inbox row is the
// notification; the subscription is what keeps them on the thread afterwards.
func (s routingStore) Subscribe(ctx context.Context, workspaceID, issueID, userID string) error {
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return err
	}
	id, err := util.ParseUUID(issueID)
	if err != nil {
		return err
	}
	uid, err := util.ParseUUID(userID)
	if err != nil {
		return err
	}
	if _, err := s.h.Queries.AddIssueSubscriber(ctx, db.AddIssueSubscriberParams{
		IssueID: id, UserType: "member", UserID: uid, Reason: "mentioned",
	}); err != nil {
		return err
	}
	issue, err := s.h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: id, WorkspaceID: wsID})
	if err != nil {
		return err
	}
	if _, err := s.h.Queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
		ID:            dbid.NewV7(),
		WorkspaceID:   wsID,
		RecipientType: "member",
		RecipientID:   uid,
		Type:          "routing_needs_you",
		Severity:      "action_required",
		IssueID:       id,
		Title:         issue.Title,
		Body:          pgtype.Text{String: "这张票需要你看一眼——路由没有人会继续推进它。", Valid: true},
		ActorType:     pgtype.Text{String: "system", Valid: true},
		Details:       []byte("{}"),
	}); err != nil {
		return err
	}
	return nil
}

// NotifyTarget is the whole "who do we @" rule: the person who created the
// issue, or the workspace owner when an agent created it. No setting, because
// a setting here is a second place for the answer to be wrong.
func (s routingStore) NotifyTarget(ctx context.Context, workspaceID string, issue routing.Issue) (routing.Member, error) {
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return routing.Member{}, err
	}
	if issue.CreatorType == "member" && issue.CreatorID != "" {
		if uid, err := util.ParseUUID(issue.CreatorID); err == nil {
			if m, err := s.h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
				UserID: uid, WorkspaceID: wsID,
			}); err == nil {
				return s.member(ctx, m), nil
			}
		}
	}
	members, err := s.h.Queries.ListMembers(ctx, wsID)
	if err != nil {
		return routing.Member{}, err
	}
	for _, m := range members {
		if m.Role == "owner" {
			return s.member(ctx, m), nil
		}
	}
	return routing.Member{}, nil
}

func (s routingStore) member(ctx context.Context, m db.Member) routing.Member {
	out := routing.Member{UserID: util.UUIDToString(m.UserID)}
	if u, err := s.h.Queries.GetUser(ctx, m.UserID); err == nil {
		out.Name = u.Name
	}
	if strings.TrimSpace(out.Name) == "" {
		out.Name = "there"
	}
	return out
}

// publishIssueUpdated announces a routing write.
//
// prevAssigneeType / prevAssigneeID are the values the issue held BEFORE this
// write, and they are not optional bookkeeping: the activity log, the
// subscriber listener and the client's assignee-grouped lists all key on
// `assignee_changed` plus the previous pair. Publishing without them made
// routing's reassignments invisible in `multica issue timeline` — the very
// command the spec names as the way to verify there is no loop and no double
// assignment — and left no audit trail for an owner changed by the system
// (DENE-633 review, F3).
//
// A human newly holding the issue therefore now gets the ordinary
// `issue_assigned` notification in addition to the routing comment's @. That
// is two notifications of two different kinds, not the same one twice: the
// spec requires the @ because assigning a person starts no run, and the
// assignment notification is what every other assignment in the product
// already produces. Suppressing either would make routing's writes either
// silent or untraceable.
func (s routingStore) publishIssueUpdated(prev, issue db.Issue) {
	if s.h.Bus == nil {
		return
	}
	s.h.Bus.Publish(events.Event{
		Type:        protocol.EventIssueUpdated,
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   "system",
		Payload:     RoutingIssueUpdatedPayload(prev, issue),
	})
}

// RoutingIssueUpdatedPayload builds the issue:updated payload for a routing
// write. Exported so the listener test in cmd/server can drive the REAL
// payload through the REAL activity listener: the bug this replaces was a
// payload that satisfied every test in its own package and still produced no
// timeline row downstream, and only a test that crosses that boundary can
// catch the next one.
//
// The types matter as much as the keys. The listeners read the previous pair
// with `payload["prev_assignee_type"].(*string)`, so a plain string here would
// type-assert to nil and silently drop the "from" half of the record.
func RoutingIssueUpdatedPayload(prev, issue db.Issue) map[string]any {
	assigneeChanged := prev.AssigneeType.String != issue.AssigneeType.String ||
		uuidToString(prev.AssigneeID) != uuidToString(issue.AssigneeID)
	return map[string]any{
		"issue":              issueToResponse(issue, ""),
		"assignee_changed":   assigneeChanged,
		"status_changed":     prev.Status != issue.Status,
		"prev_status":        prev.Status,
		"prev_assignee_type": textToPtr(prev.AssigneeType),
		"prev_assignee_id":   uuidToPtr(prev.AssigneeID),
		"creator_type":       issue.CreatorType,
		"creator_id":         uuidToString(issue.CreatorID),
	}
}

// inReviewKeys is the awaiting-acceptance status set the stale-review sweep
// looks at.
//
// It is the concrete `in_review` built-in, not a category: MUL-7365 collapsed
// the stored vocabulary into four lifecycle categories (unstarted, started,
// done, closed), and In Progress, In Review and Blocked all live in `started`.
// Expanding "in_review" through ExpandCategories therefore resolves to the
// whole `started` set — the sweep started picking up tickets actively being
// worked on, and the completion guard would write done over an in_progress
// ticket (DENE-730). The rest of internal/routing already matches on the
// concrete key (`route.go`, `stale.go`), so this keeps the store consistent
// with the decision layer.
func (s routingStore) inReviewKeys(_ context.Context, _ pgtype.UUID) ([]string, error) {
	return []string{issuestatus.InReview}, nil
}

// EnabledWorkspaces lists the workspaces the sweep should visit at all.
func (s routingStore) EnabledWorkspaces(ctx context.Context) ([]string, error) {
	ids, err := s.h.Queries.ListRoutingEnabledWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, util.UUIDToString(id))
	}
	return out, nil
}

// StaleReviews lists tickets awaiting acceptance that have been quiet since
// before the given instant and have no run working on them.
func (s routingStore) StaleReviews(ctx context.Context, workspaceID string, before time.Time, limit int) ([]string, error) {
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return nil, err
	}
	keys, err := s.inReviewKeys(ctx, wsID)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, nil
	}
	rows, err := s.h.Queries.ListStaleReviewIssues(ctx, db.ListStaleReviewIssuesParams{
		WorkspaceID: wsID,
		Statuses:    keys,
		Before:      pgtype.Timestamptz{Time: before, Valid: true},
		Lim:         int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, id := range rows {
		out = append(out, util.UUIDToString(id))
	}
	return out, nil
}

// ReviewRemarks returns what the reviewer themselves wrote on this ticket in
// the CURRENT review round — since the last time the ticket entered the
// awaiting-acceptance category.
//
// The round boundary is the point of this method, not a refinement of it. A
// ticket can be reviewed more than once: the reviewer passes it, a person
// sends it back, the executor redoes the work, and it returns to in_review
// with the first round's "looks good" still sitting on the thread. Reading
// that remark as acceptance of the second round's work would align the status
// to an expired fact — the one failure the completion gate exists to prevent.
//
// A ticket whose entry moment cannot be established yields nothing rather than
// its whole history. That is the same safe direction the rest of this gate
// takes: no remark means no acceptance, which means the sweep wakes the
// reviewer instead of closing the ticket.
//
// "none" and an empty slot have no author, so they return nothing and can
// never unlock a completion — which is the correct reading of both: a ticket
// nobody was asked to accept carries no acceptance.
func (s routingStore) ReviewRemarks(ctx context.Context, workspaceID, issueID string, reviewer routing.ReviewerRef) ([]string, error) {
	if reviewer.Kind != routing.ReviewerAgent && reviewer.Kind != routing.ReviewerMember {
		return nil, nil
	}
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return nil, err
	}
	id, err := util.ParseUUID(issueID)
	if err != nil {
		return nil, err
	}
	authorID, err := util.ParseUUID(reviewer.ID)
	if err != nil {
		return nil, err
	}
	keys, err := s.inReviewKeys(ctx, wsID)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, nil
	}
	since, err := s.h.Queries.LastEnteredReviewAt(ctx, db.LastEnteredReviewAtParams{
		IssueID:     id,
		WorkspaceID: wsID,
		Statuses:    keys,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if !since.Valid {
		return nil, nil
	}
	rows, err := s.h.Queries.ListReviewerCommentsForIssue(ctx, db.ListReviewerCommentsForIssueParams{
		IssueID:     id,
		WorkspaceID: wsID,
		AuthorType:  string(reviewer.Kind),
		AuthorID:    authorID,
		Since:       since,
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// CompleteFromReview moves a ticket out of the awaiting-acceptance category.
// It is conditional on the ticket still being there, so a ticket somebody else
// moved in the meantime reports written=false and the caller says nothing.
func (s routingStore) CompleteFromReview(ctx context.Context, workspaceID, issueID string) (bool, error) {
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return false, err
	}
	id, err := util.ParseUUID(issueID)
	if err != nil {
		return false, err
	}
	keys, err := s.inReviewKeys(ctx, wsID)
	if err != nil {
		return false, err
	}
	if len(keys) == 0 {
		return false, nil
	}
	prev, err := s.h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: id, WorkspaceID: wsID})
	if err != nil {
		return false, err
	}
	issue, err := s.h.Queries.CompleteIssueFromReview(ctx, db.CompleteIssueFromReviewParams{
		ID: id, WorkspaceID: wsID, Statuses: keys,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	s.publishIssueUpdated(prev, issue)
	// A status write is not finished when the row is written. Everything that
	// depends on a ticket reaching a terminal status — the parent's child-done
	// comment, the stage barrier that wakes the next stage, the cross-family
	// waiters — hangs off these two helpers, which the request path calls on
	// every status change (see UpdateIssue). Leaving them out here would trade
	// one stall for a quieter one: an agent cannot set done itself, so "the
	// reviewer passed it and the status never moved" is the commonest way a
	// SUB-issue stalls, and closing it without telling the parent would stop
	// the next stage from ever waking.
	//
	// Both are best-effort and guard on the transition themselves; the status
	// write has already committed, so neither can undo it.
	s.h.notifyParentOfChildDone(ctx, prev, issue)
	s.h.notifyWaitersOfIssueDone(ctx, prev, issue)
	return true, nil
}

func clipRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}
