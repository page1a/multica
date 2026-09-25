package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/stagegate"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// stagePlan is what closing a stage should do besides the existing comment.
type stagePlan struct {
	note      string
	skipWake  bool
	parentOff bool
	relay     *db.Agent
}

// disabledParentSeat reports the parent seat that would be woken and cannot
// take work. A squad parent is its leader. Archived seats are not this case.
func (h *Handler) disabledParentSeat(ctx context.Context, parent db.Issue) (db.Agent, bool) {
	if !parent.AssigneeType.Valid || !parent.AssigneeID.Valid {
		return db.Agent{}, false
	}
	var agentID pgtype.UUID
	switch parent.AssigneeType.String {
	case "agent":
		agentID = parent.AssigneeID
	case "squad":
		squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
			ID: parent.AssigneeID, WorkspaceID: parent.WorkspaceID,
		})
		if err != nil {
			return db.Agent{}, false
		}
		agentID = squad.LeaderID
	default:
		return db.Agent{}, false
	}
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID: agentID, WorkspaceID: parent.WorkspaceID,
	})
	if err != nil || agent.ArchivedAt.Valid || agent.WorkEnabled {
		return db.Agent{}, false
	}
	return agent, true
}

// substituteAgent picks a same-tier other-family seat, or one tier down,
// that can take work the failed seat cannot.
func (h *Handler) substituteAgent(ctx context.Context, workspaceID pgtype.UUID, failed db.Agent, avoid []string, direction string) (db.Agent, routing.Seat, bool) {
	agents, err := h.Queries.ListAgents(ctx, workspaceID)
	if err != nil {
		return db.Agent{}, routing.Seat{}, false
	}
	roster := make(map[string]routing.Agent, len(agents))
	byID := make(map[string]db.Agent, len(agents))
	for _, agent := range agents {
		if agent.ArchivedAt.Valid || !agent.WorkEnabled || !agent.RuntimeID.Valid {
			continue
		}
		id := uuidToString(agent.ID)
		tier := ""
		if agent.RoutingTier.Valid {
			tier = agent.RoutingTier.String
		}
		roster[agent.Name] = routing.Agent{ID: id, Name: agent.Name, Tier: tier}
		byID[id] = agent
	}
	holder := routing.Seat{ID: uuidToString(failed.ID), Name: failed.Name}
	if failed.RoutingTier.Valid {
		holder.TierKey = failed.RoutingTier.String
	}
	seat, _, ok := routing.SubstituteSeat(routing.DefaultLadder, holder, roster, avoid, direction)
	if !ok {
		return db.Agent{}, routing.Seat{}, false
	}
	agent, found := byID[seat.ID]
	if !found {
		return db.Agent{}, routing.Seat{}, false
	}
	return agent, seat, true
}

// planStageAdvance promotes next-stage backlog items whose descriptions state
// no extra dependency, and covers a parent seat that cannot receive the wake.
func (h *Handler) planStageAdvance(ctx context.Context, parent db.Issue, children []db.Issue, nextStage int32, statuses resolvedChildStatuses, blockedByCancel bool) stagePlan {
	off, parentOff := h.disabledParentSeat(ctx, parent)
	var plan stagePlan
	plan.parentOff = parentOff
	if nextStage > 0 && !blockedByCancel {
		promoted, started, held := h.promoteClearNextStage(ctx, parent, children, nextStage, statuses)
		plan.note = stagePromotionNote(promoted, started, held)
		if len(promoted) > 0 && len(held) == 0 {
			plan.skipWake = true
			if parentOff {
				plan.note += fmt.Sprintf(" 父票执行人 %s 已停用，这一阶段没有要人拍板的依赖，所以没有再叫人。", off.Name)
			}
			return plan
		}
	}
	if !parentOff {
		return plan
	}
	replacement, _, ok := h.substituteAgent(ctx, parent.WorkspaceID, off, nil, "")
	if !ok {
		plan.skipWake = true
		plan.note += fmt.Sprintf(" 父票执行人 %s 已停用，同档和下一档都没有能接的席位，阶段没有人推进。", off.Name)
		return plan
	}
	plan.relay = &replacement
	plan.note += fmt.Sprintf(" 父票执行人 %s 已停用，不接新活。阶段上还要人看的部分改由 %s 接手。", off.Name, replacement.Name)
	return plan
}

func (h *Handler) promoteClearNextStage(ctx context.Context, parent db.Issue, children []db.Issue, nextStage int32, statuses resolvedChildStatuses) (promoted []string, started int, held []stagegate.Hold) {
	prefix := h.getIssuePrefix(ctx, parent.WorkspaceID)
	items := make([]stagegate.Item, 0, len(children))
	byID := make(map[string]db.Issue, len(children))
	for _, child := range children {
		id := uuidToString(child.ID)
		byID[id] = child
		desc := ""
		if child.Description.Valid {
			desc = child.Description.String
		}
		items = append(items, stagegate.Item{
			ID:          id,
			Title:       child.Title,
			Description: desc,
			Identifier:  prefix + "-" + fmt.Sprint(child.Number),
			Stage:       child.Stage.Int32,
			HasStage:    child.Stage.Valid,
			Status:      statuses.status(child),
		})
	}
	ready, held := stagegate.Classify(nextStage, items)
	for _, item := range ready {
		child := byID[item.ID]
		label := item.Identifier
		if label == "" {
			label = item.Title
		}
		updated, err := h.Queries.PromoteBacklogIssueToTodo(ctx, db.PromoteBacklogIssueToTodoParams{
			ID: child.ID, WorkspaceID: child.WorkspaceID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// Another close already moved this child out of backlog.
			continue
		}
		if err != nil {
			slog.Warn("stage advance: promote failed", "error", err, "child_id", item.ID)
			held = append(held, stagegate.Hold{ID: item.ID, Identifier: item.Identifier, Title: item.Title, Reason: "提到待办失败"})
			continue
		}
		h.publish(protocol.EventIssueUpdated, uuidToString(updated.WorkspaceID), "system", "", RoutingIssueUpdatedPayload(child, updated))
		ran := false
		if h.IssueService != nil {
			if trigger, ok := h.IssueService.WillEnqueueRun(ctx, service.IssueTriggerInput{
				Issue: updated, PrevStatus: child.Status, StatusChanged: true,
			}, service.IssueTriggerProbe{}); ok {
				h.dispatchIssueRun(ctx, updated, trigger, "system", "", "")
				ran = true
				started++
			}
		}
		if ran {
			promoted = append(promoted, label)
			continue
		}
		promoted = append(promoted, label+"（还没开跑）")
	}
	return promoted, started, held
}

func stagePromotionNote(promoted []string, started int, held []stagegate.Hold) string {
	if len(promoted) == 0 && len(held) == 0 {
		return ""
	}
	var b strings.Builder
	if len(promoted) > 0 {
		b.WriteString(" 平台已把下一阶段里没有额外依赖的子票提到待办：")
		b.WriteString(strings.Join(promoted, "、"))
		b.WriteString("。")
		if started > 0 {
			b.WriteString("能接活的执行席已经开跑。")
		}
	}
	if len(held) > 0 {
		b.WriteString(" 这些还留在待办，因为依赖没说清或和阶段安排冲突：")
		parts := make([]string, 0, len(held))
		for _, item := range held {
			label := item.Identifier
			if label == "" {
				label = item.Title
			}
			parts = append(parts, label+"（"+item.Reason+"）")
		}
		b.WriteString(strings.Join(parts, "、"))
		b.WriteString("。")
	}
	return b.String()
}

// coverDisabledMention is the @ relay: the named seat is switched off, so
// the run goes to another family. ok is false when nobody can take it.
func (h *Handler) coverDisabledMention(ctx context.Context, issue db.Issue, failed db.Agent, authorType, authorID, originator, wsID string) (db.Agent, bool) {
	avoid := []string{}
	for range 4 {
		agent, _, ok := h.substituteAgent(ctx, issue.WorkspaceID, failed, avoid, "")
		if !ok {
			return db.Agent{}, false
		}
		if h.canInvokeAgent(ctx, agent, authorType, authorID, originator, wsID) {
			return agent, true
		}
		avoid = append(avoid, uuidToString(agent.ID))
	}
	return db.Agent{}, false
}

func (h *Handler) noteMentionRelay(ctx context.Context, issue db.Issue, commentID pgtype.UUID, trigger commentAgentTrigger) {
	if trigger.RelayFromID == "" || !commentID.Valid {
		return
	}
	body := fmt.Sprintf("%s 已停用，不接新活。这条 @ 改由同档另一家模型的 %s 接手。原席位恢复后不会自动把这条叫醒抢回去。", trigger.RelayFromName, trigger.RelayToName)
	h.postSeatRelayComment(ctx, issue, "mention_relay:"+uuidToString(commentID)+":"+trigger.RelayFromID, body)
}

func (h *Handler) noteMissedMentionRelays(ctx context.Context, issue db.Issue, commentID pgtype.UUID, targets []commentMentionTarget) {
	if !commentID.Valid {
		return
	}
	for _, target := range targets {
		if !target.RelayMissed {
			continue
		}
		name := target.RelayedFromName
		if name == "" {
			name = "被 @ 的席位"
		}
		body := fmt.Sprintf("%s 已停用，不接新活，同档和下一档都没有能接的席位。这条 @ 没有人收到。", name)
		h.postSeatRelayComment(ctx, issue, "mention_relay_miss:"+uuidToString(commentID)+":"+target.TargetID, body)
	}
}

func (h *Handler) postSeatRelayComment(ctx context.Context, issue db.Issue, kind, body string) {
	_, err := h.Queries.CreateRoutingComment(ctx, db.CreateRoutingCommentParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		AuthorID:    pgtype.UUID{Valid: true},
		Content:     body,
		RoutingKind: kind,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("seat relay: comment failed", "error", err, "issue_id", uuidToString(issue.ID), "kind", kind)
	}
}

func (h *Handler) wakeStageRelay(ctx context.Context, parent db.Issue, commentID pgtype.UUID, relay *db.Agent) {
	if relay == nil {
		return
	}
	if _, err := h.TaskService.EnqueueTaskForMention(ctx, parent, relay.ID, commentID, service.OriginDerived); err != nil {
		slog.Warn("stage advance: relay wake failed",
			"error", err,
			"parent_id", uuidToString(parent.ID),
			"agent_id", uuidToString(relay.ID))
	}
}
