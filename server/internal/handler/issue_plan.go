package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// planOriginType stamps every issue `multica plan apply` creates (DENE-864).
// Migration 538's partial unique index makes "one issue per plan node" a
// database fact, which is what lets a second apply adopt instead of duplicate.
const planOriginType = "plan"

// planNodeNamespace is half the input of every plan node id. Never change it:
// every issue a plan already created would lose its identity and the next
// apply would build the tree again.
var planNodeNamespace = uuid.MustParse("5f0f7a3c-9d8e-4b61-8a0e-86401c1e7d2b")

// planNodeID derives a node's origin_id from (workspace, plan key, node key).
// The root's node key is empty. NUL separators keep ("a","bc") and ("ab","c")
// apart.
func planNodeID(workspaceID pgtype.UUID, planKey, nodeKey string) pgtype.UUID {
	derived := uuid.NewSHA1(planNodeNamespace, []byte(uuidToString(workspaceID)+"\x00"+planKey+"\x00"+nodeKey))
	return parseUUID(derived.String())
}

// ApplyPlanNode is one issue of a plan. Assignees arrive resolved: the CLI
// turns names into (type, id) and the server validates the pair exactly like
// an ordinary create, so a plan cannot seat anyone a create could not.
type ApplyPlanNode struct {
	Key          string  `json:"key,omitempty"`
	Title        string  `json:"title"`
	Description  string  `json:"description,omitempty"`
	Priority     string  `json:"priority,omitempty"`
	Stage        *int32  `json:"stage,omitempty"`
	AssigneeType *string `json:"assignee_type,omitempty"`
	AssigneeID   *string `json:"assignee_id,omitempty"`
}

// ApplyPlanRequest builds a staged tree. Exactly one of Parent (create a new
// coordinating parent) and ParentIssueID (hang the children under an issue
// that already exists) is set.
type ApplyPlanRequest struct {
	Key           string          `json:"key"`
	Parent        *ApplyPlanNode  `json:"parent,omitempty"`
	ParentIssueID *string         `json:"parent_issue_id,omitempty"`
	ProjectID     *string         `json:"project_id,omitempty"`
	Children      []ApplyPlanNode `json:"children"`
}

type ApplyPlanIssue struct {
	ID           string  `json:"id"`
	Identifier   string  `json:"identifier"`
	Title        string  `json:"title"`
	Status       string  `json:"status"`
	Stage        *int32  `json:"stage,omitempty"`
	AssigneeType *string `json:"assignee_type,omitempty"`
	AssigneeID   *string `json:"assignee_id,omitempty"`
	Created      bool    `json:"created"`
}

// ApplyPlanResponse is the tree as it stands after the apply: the parent and
// every child under it, each flagged with whether THIS apply created it.
type ApplyPlanResponse struct {
	Key      string           `json:"key"`
	Parent   ApplyPlanIssue   `json:"parent"`
	Children []ApplyPlanIssue `json:"children"`
	Created  int              `json:"created"`
	Existing int              `json:"existing"`
}

// planChildStatus: the first stage starts, every later stage waits in backlog
// for `multica issue stage advance`. Same rule as an alignment group.
func planChildStatus(stage int32) string {
	return issueDraftChildStatusForCreate(&stage)
}

// ApplyPlan is `multica plan apply`: one plan file, one transaction, the whole
// staged tree with its executors seated at create time. Re-applying the same
// plan creates only the nodes that do not exist yet.
func (h *Handler) ApplyPlan(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	var req ApplyPlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Key = strings.TrimSpace(req.Key)
	if req.Key == "" {
		writeError(w, http.StatusBadRequest, "plan key is required: it is what lets a second apply find the first one's issues")
		return
	}
	if (req.Parent == nil) == (req.ParentIssueID == nil || *req.ParentIssueID == "") {
		writeError(w, http.StatusBadRequest, "plan needs exactly one of parent (create a new parent) or parent_issue_id (use an existing one)")
		return
	}
	if len(req.Children) == 0 {
		writeError(w, http.StatusBadRequest, "plan has no children")
		return
	}
	if len(req.Children) > service.MaxIssueGroupChildren {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("plan has %d children; at most %d fit in one apply", len(req.Children), service.MaxIssueGroupChildren))
		return
	}
	seen := make(map[string]bool, len(req.Children))
	for i := range req.Children {
		c := &req.Children[i]
		c.Title = strings.TrimSpace(c.Title)
		c.Key = strings.TrimSpace(c.Key)
		if c.Title == "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("child %d has no title", i+1))
			return
		}
		if c.Key == "" {
			c.Key = c.Title
		}
		if seen[c.Key] {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("child key %q appears twice; give one of them an explicit key", c.Key))
			return
		}
		seen[c.Key] = true
		if c.Stage == nil || *c.Stage < 1 {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("child %q needs stage >= 1", c.Title))
			return
		}
	}

	// A plan under an existing parent is identified per parent (set once the
	// parent is resolved below): the same key under another issue is a
	// different plan, not a re-apply.
	identity := req.Key

	creatorType, creatorID := h.resolveActor(r, userID, workspaceID)
	build := func(node ApplyPlanNode, status string, stage pgtype.Int4, nodeKey string) (service.IssueCreateParams, bool) {
		priority := node.Priority
		if priority == "" {
			priority = "none"
		}
		if !validateIssueEnum(w, "priority", priority, validIssuePriorities) {
			return service.IssueCreateParams{}, false
		}
		var assigneeType pgtype.Text
		var assigneeID pgtype.UUID
		if node.AssigneeType != nil {
			assigneeType = pgtype.Text{String: *node.AssigneeType, Valid: true}
		}
		if node.AssigneeID != nil {
			id, ok := parseUUIDOrBadRequest(w, *node.AssigneeID, "assignee_id")
			if !ok {
				return service.IssueCreateParams{}, false
			}
			assigneeID = id
		}
		if code, msg := h.validateAssigneePair(r.Context(), r, workspaceID, assigneeType, assigneeID); code != 0 {
			writeError(w, code, fmt.Sprintf("%q: %s", node.Title, msg))
			return service.IssueCreateParams{}, false
		}
		return service.IssueCreateParams{
			WorkspaceID:    wsUUID,
			Title:          strings.TrimSpace(node.Title),
			Description:    pgtype.Text{String: node.Description, Valid: node.Description != ""},
			Status:         status,
			Priority:       priority,
			AssigneeType:   assigneeType,
			AssigneeID:     assigneeID,
			CreatorType:    creatorType,
			CreatorID:      parseUUID(creatorID),
			Stage:          stage,
			OriginType:     pgtype.Text{String: planOriginType, Valid: true},
			OriginID:       planNodeID(wsUUID, identity, nodeKey),
			AllowDuplicate: true,
		}, true
	}

	// Find the root. A new-parent plan whose root already exists is a re-apply
	// and continues under it, exactly like the existing-parent form.
	var root db.Issue
	rootExists := false
	if req.ParentIssueID != nil && *req.ParentIssueID != "" {
		parent, ok := h.loadIssueForUser(w, r, *req.ParentIssueID)
		if !ok {
			return
		}
		root, rootExists = parent, true
		identity = req.Key + "\x00parent\x00" + uuidToString(parent.ID)
	} else {
		existing, err := h.Queries.GetIssueByOrigin(r.Context(), db.GetIssueByOriginParams{
			WorkspaceID: wsUUID,
			OriginType:  pgtype.Text{String: planOriginType, Valid: true},
			OriginID:    planNodeID(wsUUID, req.Key, ""),
		})
		switch {
		case err == nil:
			root, rootExists = existing, true
		case !errors.Is(err, pgx.ErrNoRows):
			writeError(w, http.StatusInternalServerError, "failed to look up plan parent")
			return
		}
	}

	group := service.IssueGroupParams{}
	if rootExists {
		group.RootIssueID = root.ID
	} else {
		title := strings.TrimSpace(req.Parent.Title)
		if title == "" {
			writeError(w, http.StatusBadRequest, "parent has no title")
			return
		}
		params, ok := build(*req.Parent, issueDraftCoordinatorStatus, pgtype.Int4{}, "")
		if !ok {
			return
		}
		if req.ProjectID != nil && *req.ProjectID != "" {
			id, ok := parseUUIDOrBadRequest(w, *req.ProjectID, "project_id")
			if !ok {
				return
			}
			if _, ok := h.visibleProjectInWorkspace(w, r, wsUUID, id); !ok {
				return
			}
			params.ProjectID = id
			params.ProjectPinned = true
		}
		// The parent coordinates: it keeps its assignee — the seat the stage
		// barrier wakes — but creating the plan does not hand it a run.
		group.Nodes = append(group.Nodes, service.IssueGroupNode{Params: params, Opts: service.IssueCreateOpts{SuppressAssigneeRun: true}})
	}
	for _, child := range req.Children {
		if rootExists {
			_, err := h.Queries.GetIssueByOrigin(r.Context(), db.GetIssueByOriginParams{
				WorkspaceID: wsUUID,
				OriginType:  pgtype.Text{String: planOriginType, Valid: true},
				OriginID:    planNodeID(wsUUID, identity, child.Key),
			})
			if err == nil {
				continue // Already created by an earlier apply; never rewritten.
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusInternalServerError, "failed to look up plan node")
				return
			}
		}
		params, ok := build(child, planChildStatus(*child.Stage), pgtype.Int4{Int32: *child.Stage, Valid: true}, child.Key)
		if !ok {
			return
		}
		group.Nodes = append(group.Nodes, service.IssueGroupNode{Params: params})
	}

	created := map[string]bool{}
	if len(group.Nodes) > 0 {
		h.preparePlanGroupOpts(r, wsUUID, creatorID, &group)
		res, err := h.IssueService.CreateGroup(r.Context(), group)
		if err != nil {
			if isUniqueViolation(err) {
				// A concurrent apply of the same plan committed first. Nothing
				// of ours was written; the caller re-runs and adopts its tree.
				writeError(w, http.StatusConflict, "another apply of this plan committed first; run it again to see the tree")
				return
			}
			writeIssueDraftCreateError(w, r, err)
			return
		}
		for _, issue := range res.Issues {
			created[uuidToString(issue.ID)] = true
			// Executors are seated by the plan; routing only fills what the plan
			// left empty (the parent's reviewer, an unassigned child).
			h.RouteGroupNodeAsync(r, workspaceID, uuidToString(issue.ID))
		}
		if !rootExists {
			root = res.Issues[0]
		}
	}

	children, err := h.Queries.ListChildIssues(r.Context(), root.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "plan applied but reading the tree back failed")
		return
	}
	prefix := h.getIssuePrefix(r.Context(), wsUUID)
	row := func(issue db.Issue) ApplyPlanIssue {
		return ApplyPlanIssue{
			ID:           uuidToString(issue.ID),
			Identifier:   issueIdentifier(prefix, issue.Number),
			Title:        issue.Title,
			Status:       issue.Status,
			Stage:        int4ToPtr(issue.Stage),
			AssigneeType: textToPtr(issue.AssigneeType),
			AssigneeID:   uuidToPtr(issue.AssigneeID),
			Created:      created[uuidToString(issue.ID)],
		}
	}
	resp := ApplyPlanResponse{Key: req.Key, Parent: row(root), Children: make([]ApplyPlanIssue, 0, len(children))}
	for _, child := range children {
		resp.Children = append(resp.Children, row(child))
	}
	for _, issue := range append([]ApplyPlanIssue{resp.Parent}, resp.Children...) {
		if issue.Created {
			resp.Created++
		} else {
			resp.Existing++
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// preparePlanGroupOpts fills the transport half of every node's options, the
// same way the alignment confirm does, with the caller as actor.
func (h *Handler) preparePlanGroupOpts(r *http.Request, wsUUID pgtype.UUID, actorID string, group *service.IssueGroupParams) {
	prefix := h.getIssuePrefix(r.Context(), wsUUID)
	fillCreated := h.newStatusCategoryFiller(r.Context(), wsUUID)
	platform, _, _ := middleware.ClientMetadataFromContext(r.Context())
	for i := range group.Nodes {
		params := group.Nodes[i].Params
		opts := group.Nodes[i].Opts
		opts.ActorID = actorID
		if params.AssigneeType.Valid && params.AssigneeType.String == "agent" {
			opts.AnalyticsAgentID = uuidToString(params.AssigneeID)
		}
		opts.Platform = platform
		opts.BroadcastPayload = func(issue db.Issue, _ []db.Attachment, labels []db.IssueLabel) map[string]any {
			payload := issueToResponse(issue, prefix)
			fillCreated(&payload)
			labelResponses := labelsToResponse(labels)
			payload.Labels = &labelResponses
			return map[string]any{"issue": payload}
		}
		group.Nodes[i].Opts = opts
	}
}

type StageAdvanceIssue struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	RunStarted bool   `json:"run_started,omitempty"`
}

// StageAdvanceResponse reports what the advance actually did. Advanced is
// false when nothing was promoted; Pending then names what the stage still
// waits on.
type StageAdvanceResponse struct {
	Parent   string              `json:"parent"`
	Stage    int32               `json:"stage,omitempty"`
	Advanced bool                `json:"advanced"`
	Promoted []StageAdvanceIssue `json:"promoted,omitempty"`
	Pending  []StageAdvanceIssue `json:"pending,omitempty"`
	Message  string              `json:"message"`
	// Error repeats Message on a refusal so every client that reads the
	// ordinary {"error": ...} shape shows the reason.
	Error string `json:"error,omitempty"`
}

// planStageAdvance decides the advance from the children's resolved statuses.
// The stage to open is the lowest one that still has open work; everything
// below it is terminal by construction. It advances when that stage still has
// backlog children — they are promoted. When its open children are all
// already active, the previous stage is done and this one is running: nothing
// to promote, and the caller is told which tickets it waits on.
func planStageAdvance(children []db.Issue, statuses resolvedChildStatuses) (stage int32, promote, pending []db.Issue) {
	for _, c := range children {
		if !c.Stage.Valid || statuses.isTerminal(c) {
			continue
		}
		if stage == 0 || c.Stage.Int32 < stage {
			stage = c.Stage.Int32
		}
	}
	if stage == 0 {
		return 0, nil, nil
	}
	for _, c := range children {
		if !c.Stage.Valid || c.Stage.Int32 != stage || statuses.isTerminal(c) {
			continue
		}
		if statuses.status(c) == "backlog" {
			promote = append(promote, c)
		} else {
			pending = append(pending, c)
		}
	}
	return stage, promote, pending
}

// AdvanceIssueStage is `multica issue stage advance <parent>`: if every stage
// below the next one is terminal, promote that stage's backlog children to
// todo and wake their executors; otherwise refuse and name what is missing.
func (h *Handler) AdvanceIssueStage(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	parent, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	workspaceID := uuidToString(parent.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	prefix := h.getIssuePrefix(r.Context(), parent.WorkspaceID)
	parentLabel := issueIdentifier(prefix, parent.Number)

	children, err := h.Queries.ListChildIssues(r.Context(), parent.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list sub-issues")
		return
	}
	if !siblingsAreStaged(children) {
		writeError(w, http.StatusBadRequest, parentLabel+" has no staged sub-issues; there is no stage to advance")
		return
	}
	statuses, err := resolveChildStatuses(children, h.childStatusResolver(r.Context()))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve sub-issue statuses")
		return
	}
	stage, promote, pending := planStageAdvance(children, statuses)
	row := func(c db.Issue, status string) StageAdvanceIssue {
		return StageAdvanceIssue{ID: uuidToString(c.ID), Identifier: issueIdentifier(prefix, c.Number), Title: c.Title, Status: status}
	}
	resp := StageAdvanceResponse{Parent: parentLabel, Stage: stage}
	if stage == 0 {
		resp.Message = "every stage of " + parentLabel + " is terminal; nothing to advance"
		writeJSON(w, http.StatusOK, resp)
		return
	}
	for _, c := range pending {
		resp.Pending = append(resp.Pending, row(c, statuses.status(c)))
	}
	if len(promote) == 0 {
		// The open stage is already running. Its predecessor is done; this
		// one is not, and the refusal says exactly which tickets hold it.
		labels := make([]string, 0, len(pending))
		for _, p := range resp.Pending {
			labels = append(labels, fmt.Sprintf("%s (%s)", p.Identifier, p.Status))
		}
		msg := fmt.Sprintf("stage %d of %s is not finished: %s", stage, parentLabel, strings.Join(labels, ", "))
		if next := stageAbove(children, stage); next > 0 {
			msg += fmt.Sprintf("; stage %d waits for it", next)
		}
		writeJSON(w, http.StatusConflict, StageAdvanceResponse{Parent: parentLabel, Stage: stage, Pending: resp.Pending, Message: msg, Error: msg})
		return
	}

	for _, c := range promote {
		updated, ran, err := h.promoteChildToTodo(r.Context(), c, actorType, actorID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue // A concurrent advance already moved it.
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to promote "+issueIdentifier(prefix, c.Number))
			return
		}
		item := row(updated, updated.Status)
		item.RunStarted = ran
		resp.Promoted = append(resp.Promoted, item)
	}
	resp.Advanced = len(resp.Promoted) > 0
	labels := make([]string, 0, len(resp.Promoted))
	for _, p := range resp.Promoted {
		label := p.Identifier
		if !p.RunStarted {
			label += " (no run started)"
		}
		labels = append(labels, label)
	}
	if resp.Advanced {
		resp.Message = fmt.Sprintf("stage %d of %s promoted to todo: %s", stage, parentLabel, strings.Join(labels, ", "))
	} else {
		resp.Message = fmt.Sprintf("stage %d of %s was already promoted by a concurrent advance", stage, parentLabel)
	}
	writeJSON(w, http.StatusOK, resp)
}

func stageAbove(children []db.Issue, stage int32) int32 {
	stages := []int32{}
	for _, c := range children {
		if c.Stage.Valid && c.Stage.Int32 > stage {
			stages = append(stages, c.Stage.Int32)
		}
	}
	if len(stages) == 0 {
		return 0
	}
	sort.Slice(stages, func(i, j int) bool { return stages[i] < stages[j] })
	return stages[0]
}

// promoteChildToTodo moves one backlog child to todo, broadcasts it, and
// enqueues its assignee's run when the trigger rules say the move starts one.
// pgx.ErrNoRows means the child had already left backlog.
func (h *Handler) promoteChildToTodo(ctx context.Context, child db.Issue, actorType, actorID string) (db.Issue, bool, error) {
	updated, err := h.Queries.PromoteBacklogIssueToTodo(ctx, db.PromoteBacklogIssueToTodoParams{
		ID: child.ID, WorkspaceID: child.WorkspaceID,
	})
	if err != nil {
		return db.Issue{}, false, err
	}
	h.publish(protocol.EventIssueUpdated, uuidToString(updated.WorkspaceID), actorType, actorID, RoutingIssueUpdatedPayload(child, updated))
	if h.IssueService == nil {
		return updated, false, nil
	}
	trigger, ok := h.IssueService.WillEnqueueRun(ctx, service.IssueTriggerInput{
		Issue: updated, PrevStatus: child.Status, StatusChanged: true,
	}, service.IssueTriggerProbe{})
	if !ok {
		return updated, false, nil
	}
	h.dispatchIssueRun(ctx, updated, trigger, actorType, actorID, "")
	return updated, true, nil
}
