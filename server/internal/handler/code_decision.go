package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/coderesolve"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// This file is the claim path's only seam onto internal/coderesolve. Which
// code a run uses is decided there, as a pure function, so the server, the
// daemon and the desktop UI all read one answer instead of each deriving the
// rule from the resource list (DENE-619).
//
// The handler's job is the part a pure function cannot do: read the
// capabilities off this request, convert the wire structs, and stamp the
// result onto the response.

// daemonForRequest reads the claiming daemon's declared capabilities off the
// request it is claiming with.
//
// Declarations, never version numbers: a git-describe dev build is exempted
// from the version floor so `make daemon` stays unblocked, and that exemption
// let a daemon with no worktree implementation straight through the gate
// (MUL-5707). A daemon that implements something says so.
func daemonForRequest(r *http.Request, daemonID string) coderesolve.Daemon {
	multi := requestHasClientCapability(r, protocol.DaemonCapabilityLocalDirectoryMultiV1)
	userRoot := requestHasClientCapability(r, protocol.DaemonCapabilityLocalWorktreeUserRootV1)
	return coderesolve.Daemon{
		ID: daemonID,
		// Already-shipped daemons declare the user-root capability and handle
		// several directories, because both landed together; the multi
		// capability was split out afterwards to name the two promises
		// separately. Reading either as "can take more than one directory"
		// keeps those daemons working exactly as they do today, instead of
		// narrowing them to one directory the moment this server deploys.
		MultiLocalDirectory: multi || userRoot,
		WorktreeUserRoot:    userRoot,
		LocalWorktree:       requestHasClientCapability(r, protocol.DaemonCapabilityLocalWorktreeV1),
		LocalShared:         requestHasClientCapability(r, protocol.DaemonCapabilityLocalSharedV1),
	}
}

func resourcesToResolve(resources []ProjectResourceData) []coderesolve.Resource {
	if len(resources) == 0 {
		return nil
	}
	out := make([]coderesolve.Resource, 0, len(resources))
	for _, res := range resources {
		out = append(out, coderesolve.Resource{
			ID:           res.ID,
			ResourceType: res.ResourceType,
			Ref:          res.ResourceRef,
			Label:        res.Label,
		})
	}
	return out
}

func resourcesFromResolve(resources []coderesolve.Resource) []ProjectResourceData {
	if len(resources) == 0 {
		return nil
	}
	out := make([]ProjectResourceData, 0, len(resources))
	for _, res := range resources {
		out = append(out, ProjectResourceData{
			ID:           res.ID,
			ResourceType: res.ResourceType,
			ResourceRef:  res.Ref,
			Label:        res.Label,
		})
	}
	return out
}

// applyCodeDecision resolves where this run's code lives and stamps the answer
// onto the claim response: once as the task's own field, and once per resource
// as read-write/read-only.
//
// Called AFTER capability narrowing, because the decision must be about the
// resource set the daemon actually receives — otherwise the server would name
// a directory it never sent.
//
// One run writes one directory (DENE-617 invariant 1). The others travel with
// access: "read-only", which is what the daemon writes into
// .multica/project/resources.json and renders in the brief, so an agent can
// read across a project's directories while only one of them is its workspace.
func applyCodeDecision(resp *AgentTaskResponse, daemon coderesolve.Daemon) {
	projects := make([]coderesolve.Project, 0, len(resp.Projects))
	for _, project := range resp.Projects {
		projects = append(projects, coderesolve.Project{
			ID:        project.ID,
			Resources: resourcesToResolve(project.Resources),
		})
	}
	// A claim whose projects were not populated still carries the singular
	// resource list; resolving over an empty project set would report "no
	// directory here" for a task that has one.
	if len(projects) == 0 && len(resp.ProjectResources) > 0 {
		projects = append(projects, coderesolve.Project{
			ID:        resp.ProjectID,
			Resources: resourcesToResolve(resp.ProjectResources),
		})
	}

	repos := make([]string, 0, len(resp.Repos))
	for _, repo := range resp.Repos {
		repos = append(repos, repo.URL)
	}

	decision := coderesolve.Resolve(coderesolve.Task{
		ID:              resp.ID,
		IssueIdentifier: resp.IssueIdentifier,
		SessionID:       resp.ChatSessionID,
		IsLeader:        resp.IsLeaderTask,
		// No surface pins a resource id on a task yet; the rule's first step
		// exists so the one that does needs no second decision path.
		Repos: repos,
	}, projects, daemon)

	resp.CodeDecision = &decision

	for i := range resp.ProjectResources {
		resp.ProjectResources[i].Access = coderesolve.AccessFor(decision,
			resp.ProjectResources[i].ResourceType, resp.ProjectResources[i].ID)
	}
	for p := range resp.Projects {
		for i := range resp.Projects[p].Resources {
			resp.Projects[p].Resources[i].Access = coderesolve.AccessFor(decision,
				resp.Projects[p].Resources[i].ResourceType, resp.Projects[p].Resources[i].ID)
		}
	}
}

// persistCodeDecision stores the claim's decision on the task row so later
// reads — the task page, the run list — serve the same answer the daemon got.
//
// Best-effort: the decision has already shipped with the task by the time this
// runs, so a failed write costs the UI its "where did this run go" line and
// nothing else. Failing the claim over it would turn a display gap into a task
// that does not run.
func (h *Handler) persistCodeDecision(ctx context.Context, taskID pgtype.UUID, decision coderesolve.Decision) {
	encoded, err := json.Marshal(decision)
	if err != nil {
		slog.Warn("task claim: failed to encode code decision", "task_id", uuidToString(taskID), "error", err)
		return
	}
	if err := h.Queries.SetAgentTaskCodeDecision(ctx, db.SetAgentTaskCodeDecisionParams{
		ID:           taskID,
		CodeDecision: encoded,
	}); err != nil {
		slog.Warn("task claim: failed to record code decision", "task_id", uuidToString(taskID), "error", err)
	}
}

// codeDecisionFromRow decodes a stored decision for a user-facing task
// response. A row from before this column, or one a claim could not write,
// yields nil — "this server never recorded one", which the UI renders as
// absent rather than as a resolution failure.
func codeDecisionFromRow(raw []byte) *coderesolve.Decision {
	if len(raw) == 0 {
		return nil
	}
	var decision coderesolve.Decision
	if err := json.Unmarshal(raw, &decision); err != nil {
		return nil
	}
	return &decision
}
