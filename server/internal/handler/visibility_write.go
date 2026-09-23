package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/permission"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Resource-level sharing scope, write side (DENE-698).
//
// Changing a scope is its own action, not a field on an ordinary edit: it has
// its own tier rule (permission.ActionChangeVisibility), its own pairing rule
// ("no project, no 'project' scope") and its own audit row. A project's scope
// change is additionally a bulk shortcut — it overwrites the scope of every
// resource the project currently holds — so the endpoint reports what it swept
// and a preview endpoint reports what it would sweep.

const (
	// auditSourceDirect: somebody set this one resource's scope.
	auditSourceDirect = "direct"
	// auditSourceProjectBulk: a project's scope change swept this resource.
	auditSourceProjectBulk = "project_bulk"
)

type visibilityRequest struct {
	Visibility string `json:"visibility"`
}

// visibilityCounts is what a bulk apply did, or would do. previously_private
// is called out separately because it is the number the confirm dialog exists
// for: those are the resources nobody but their creator can see today.
type visibilityCounts struct {
	AffectedCount          int64 `json:"affected_count"`
	PreviouslyPrivateCount int64 `json:"previously_private_count"`
}

func (c *visibilityCounts) add(other visibilityCounts) {
	c.AffectedCount += other.AffectedCount
	c.PreviouslyPrivateCount += other.PreviouslyPrivateCount
}

// parseVisibilityBody reads and validates the requested scope. An unknown
// value is a 400 with the three names spelled out, not a CHECK violation
// surfacing as a 500.
func parseVisibilityBody(w http.ResponseWriter, r *http.Request) (permission.Visibility, bool) {
	var req visibilityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return "", false
	}
	return parseVisibilityValue(w, req.Visibility)
}

func parseVisibilityValue(w http.ResponseWriter, value string) (permission.Visibility, bool) {
	vis := permission.Visibility(strings.TrimSpace(value))
	if !vis.Valid() {
		writeError(w, http.StatusBadRequest,
			"visibility must be one of: private, project, workspace")
		return "", false
	}
	return vis, true
}

// audienceSize is how many people the new scope reaches, recorded with the
// audit row. It is a snapshot, not a live view — months later the project's
// membership has moved on and the question being asked is what the change did
// at the time.
func (h *Handler) audienceSize(ctx context.Context, wsUUID pgtype.UUID, vis permission.Visibility, projectID pgtype.UUID) int32 {
	switch vis {
	case permission.VisibilityWorkspace:
		n, err := h.Queries.CountWorkspaceAudience(ctx, wsUUID)
		if err != nil {
			return 0
		}
		return int32(n)
	case permission.VisibilityProject:
		if !projectID.Valid {
			return 0
		}
		n, err := h.Queries.CountProjectAudience(ctx, db.CountProjectAudienceParams{
			WorkspaceID: wsUUID,
			ProjectID:   projectID,
		})
		if err != nil {
			return 0
		}
		return int32(n)
	default:
		// private: the creator, and nobody else.
		return 1
	}
}

func (h *Handler) visibilityAudienceSize(ctx context.Context, wsUUID pgtype.UUID, change visibilityChange) int32 {
	if change.resourceType == "module" {
		return h.moduleAudienceSize(ctx, wsUUID, change.next, change.projectID)
	}
	return h.audienceSize(ctx, wsUUID, change.next, change.projectID)
}

// moduleAudienceSize is the snapshot written with a module audit row.
// Workspace-scoped modules include guests (they may enter the area; resource
// visibility still filters items). Private modules reach owner/admin only.
func (h *Handler) moduleAudienceSize(ctx context.Context, wsUUID pgtype.UUID, vis permission.Visibility, projectID pgtype.UUID) int32 {
	switch vis {
	case permission.VisibilityWorkspace:
		n, err := h.Queries.CountWorkspaceMembers(ctx, wsUUID)
		if err != nil {
			return 0
		}
		return int32(n)
	case permission.VisibilityProject:
		return h.audienceSize(ctx, wsUUID, vis, projectID)
	default:
		n, err := h.Queries.CountWorkspaceManagers(ctx, wsUUID)
		if err != nil {
			return 0
		}
		return int32(n)
	}
}

// visibilityChange is one audit row's worth of facts.
type visibilityChange struct {
	resourceType string
	resourceID   string
	previous     string
	next         permission.Visibility
	source       string
	projectID    pgtype.UUID
}

// recordVisibilityChange writes the audit row. A failed write is logged, not
// returned: the scope change already committed, and refusing to report it
// would leave the caller thinking nothing happened.
func (h *Handler) recordVisibilityChange(r *http.Request, wsUUID pgtype.UUID, change visibilityChange) {
	wsID := uuidToString(wsUUID)
	actorType, actorID := h.resolveActor(r, requestUserID(r), wsID)
	if actorType != "agent" {
		actorType = "member"
	}
	actorUUID, err := parseUUIDSafe(actorID)
	if err != nil {
		slog.Warn("visibility audit: unusable actor id",
			append(logger.RequestAttrs(r), "actor_id", actorID, "error", err)...)
		return
	}
	previous := pgtype.Text{}
	if change.previous != "" {
		previous = pgtype.Text{String: change.previous, Valid: true}
	}
	_, err = h.Queries.RecordVisibilityChange(r.Context(), db.RecordVisibilityChangeParams{
		WorkspaceID:        wsUUID,
		ActorType:          actorType,
		ActorID:            actorUUID,
		ResourceType:       change.resourceType,
		ResourceID:         change.resourceID,
		PreviousVisibility: previous,
		NewVisibility:      string(change.next),
		AudienceSize:       h.visibilityAudienceSize(r.Context(), wsUUID, change),
		Source:             change.source,
	})
	if err != nil {
		slog.Error("visibility audit: write failed",
			append(logger.RequestAttrs(r),
				"resource_type", change.resourceType,
				"resource_id", change.resourceID,
				"error", err)...)
	}
}

// invalidateSharingCaches drops the membership entries that would otherwise
// answer from before this change.
//
// Neither cache holds a tier or a scope (docs/kun/permission-model.md), so
// most of the product is already instant: the scope is re-read from the row on
// every request. What is not instant is the handful of paths that take a
// cached "is a member" as the whole answer — attachment download and the
// daemon's workspace checks. Clearing the entry forces those back through the
// database, which is where the new scope is.
func (h *Handler) invalidateSharingCaches(ctx context.Context, wsUUID pgtype.UUID, userIDs ...pgtype.UUID) {
	if h.MembershipCache == nil {
		return
	}
	wsID := uuidToString(wsUUID)
	for _, id := range userIDs {
		if !id.Valid {
			continue
		}
		h.MembershipCache.Invalidate(ctx, uuidToString(id), wsID)
	}
}

// invalidateProjectAudienceCaches clears the whole audience of a project,
// which is what a project-scoped change moves.
func (h *Handler) invalidateProjectAudienceCaches(ctx context.Context, wsUUID, projectID pgtype.UUID) {
	if h.MembershipCache == nil || !projectID.Valid {
		return
	}
	members, err := h.Queries.ListProjectMembers(ctx, projectID)
	if err != nil {
		return
	}
	ids := make([]pgtype.UUID, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.MemberID)
	}
	h.invalidateSharingCaches(ctx, wsUUID, ids...)
}

// ---------------------------------------------------------------------------
// PUT /api/issues/{id}/visibility
// ---------------------------------------------------------------------------

// SetIssueVisibility changes one issue's sharing scope.
func (h *Handler) SetIssueVisibility(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	viewer, err := h.visibilityViewerFor(r, issue.WorkspaceID)
	if err != nil || !viewer.canSeeIssue(issue) {
		hiddenIssueNotFound(w)
		return
	}
	vis, ok := parseVisibilityBody(w, r)
	if !ok {
		return
	}
	rel := viewer.relation(issue.CreatorType, issue.CreatorID, issue.ProjectID)
	if !viewer.bypasses() &&
		!permission.Allowed(viewer.role, permission.ActionChangeVisibility, permission.Visibility(issue.Visibility), rel) {
		// The caller can see the issue, so naming the reason leaks nothing.
		writeError(w, http.StatusForbidden, "you cannot change this issue's sharing scope")
		return
	}
	if !permission.CanSetVisibility(vis, issue.ProjectID.Valid) {
		writeError(w, http.StatusBadRequest,
			"project visibility needs a project: add this issue to a project first")
		return
	}

	previous := issue.Visibility
	updated, err := h.Queries.SetIssueVisibility(r.Context(), db.SetIssueVisibilityParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Visibility:  string(vis),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to change sharing scope")
		return
	}

	h.recordVisibilityChange(r, issue.WorkspaceID, visibilityChange{
		resourceType: "issue",
		resourceID:   uuidToString(issue.ID),
		previous:     previous,
		next:         vis,
		source:       auditSourceDirect,
		projectID:    issue.ProjectID,
	})
	h.invalidateProjectAudienceCaches(r.Context(), issue.WorkspaceID, issue.ProjectID)

	wsID := uuidToString(issue.WorkspaceID)
	actorType, actorID := h.resolveActor(r, requestUserID(r), wsID)
	// The id-only invalidation goes first: recipients who just lost access must
	// evict their cached copy, and the content frame below is filtered for
	// exactly those recipients. Recipients who gained access have nothing
	// cached and pick the issue up from the update that follows.
	h.publishIssueInvalidated(wsID, actorType, actorID, uuidToString(updated.ID), "", "")
	h.publish(protocol.EventIssueUpdated, wsID, actorType, actorID, map[string]any{
		"issue": issueToResponse(updated, h.getIssuePrefix(r.Context(), issue.WorkspaceID)),
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"id":            uuidToString(updated.ID),
		"visibility":    updated.Visibility,
		"audience_size": h.audienceSize(r.Context(), issue.WorkspaceID, vis, issue.ProjectID),
	})
}

// ---------------------------------------------------------------------------
// GET/PUT /api/projects/{id}/visibility
// ---------------------------------------------------------------------------

// loadProjectForVisibility resolves the project and the caller's facts, or
// writes the deny response. Invisible and absent look identical.
func (h *Handler) loadProjectForVisibility(w http.ResponseWriter, r *http.Request) (db.Project, visibilityViewer, bool) {
	idUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "project id")
	if !ok {
		return db.Project{}, visibilityViewer{}, false
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace id")
	if !ok {
		return db.Project{}, visibilityViewer{}, false
	}
	project, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
		ID: idUUID, WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return db.Project{}, visibilityViewer{}, false
	}
	viewer, err := h.visibilityViewerFor(r, wsUUID)
	if err != nil || !viewer.canSeeProject(project) {
		writeError(w, http.StatusNotFound, "project not found")
		return db.Project{}, visibilityViewer{}, false
	}
	return project, viewer, true
}

// PreviewProjectVisibility answers "how much would this sweep" without
// sweeping: GET /api/projects/{id}/visibility/preview.
func (h *Handler) PreviewProjectVisibility(w http.ResponseWriter, r *http.Request) {
	project, _, ok := h.loadProjectForVisibility(w, r)
	if !ok {
		return
	}
	counts, err := h.countProjectVisibilitySweep(r.Context(), project)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count affected resources")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project_id":               uuidToString(project.ID),
		"visibility":               project.Visibility,
		"affected_count":           counts.AffectedCount,
		"previously_private_count": counts.PreviouslyPrivateCount,
	})
}

// countProjectVisibilitySweep counts the resources a project currently holds:
// its issues, plus the workspace repos it lists as resources.
func (h *Handler) countProjectVisibilitySweep(ctx context.Context, project db.Project) (visibilityCounts, error) {
	var counts visibilityCounts
	issueCounts, err := h.Queries.CountProjectIssueVisibility(ctx, db.CountProjectIssueVisibilityParams{
		WorkspaceID: project.WorkspaceID,
		ProjectID:   project.ID,
	})
	if err != nil {
		return counts, err
	}
	counts.add(visibilityCounts{
		AffectedCount:          issueCounts.AffectedCount,
		PreviouslyPrivateCount: issueCounts.PreviouslyPrivateCount,
	})
	repoCounts, _, err := h.projectRepoSweep(ctx, project)
	if err != nil {
		return counts, err
	}
	counts.add(repoCounts)
	return counts, nil
}

// repoSweepEntry is one repository a project sweep will re-scope, carrying the
// scope it is about to lose so the audit row can name it.
type repoSweepEntry struct {
	url      string
	previous string
}

// projectRepoSweep returns the counts for the repos a project holds and the
// entries they were found in.
func (h *Handler) projectRepoSweep(ctx context.Context, project db.Project) (visibilityCounts, []repoSweepEntry, error) {
	var counts visibilityCounts
	urls, err := h.Queries.ListProjectRepoURLs(ctx, db.ListProjectRepoURLsParams{
		WorkspaceID: project.WorkspaceID,
		ProjectID:   project.ID,
	})
	if err != nil {
		return counts, nil, err
	}
	if len(urls) == 0 {
		return counts, nil, nil
	}
	ws, err := h.Queries.GetWorkspace(ctx, project.WorkspaceID)
	if err != nil {
		return counts, nil, err
	}
	stored := workspaceReposByURL(ws.Repos)
	matched := make([]repoSweepEntry, 0, len(urls))
	for _, url := range urls {
		if url == "" {
			continue
		}
		entry, ok := stored[url]
		if !ok {
			// The project lists a repo the workspace registry does not hold.
			// There is nothing to re-scope, so it is not an affected resource.
			continue
		}
		matched = append(matched, repoSweepEntry{url: url, previous: entry.Visibility})
		counts.AffectedCount++
		if entry.Visibility == string(permission.VisibilityPrivate) || entry.Visibility == "" {
			counts.PreviouslyPrivateCount++
		}
	}
	return counts, matched, nil
}

// SetProjectVisibility changes a project's scope and sweeps it over everything
// the project currently holds. The sweep is the point: a project is the
// product's bulk shortcut for sharing, so the response reports how many
// resources it overwrote and how many of those only their creator could see.
func (h *Handler) SetProjectVisibility(w http.ResponseWriter, r *http.Request) {
	project, viewer, ok := h.loadProjectForVisibility(w, r)
	if !ok {
		return
	}
	vis, ok := parseVisibilityBody(w, r)
	if !ok {
		return
	}
	rel := permission.Relation{
		IsCreator: project.CreatedBy.Valid && viewer.userID.Valid && project.CreatedBy.Bytes == viewer.userID.Bytes,
		InProject: viewer.inProject(project.ID),
	}
	if !viewer.bypasses() &&
		!permission.Allowed(viewer.role, permission.ActionChangeVisibility, permission.Visibility(project.Visibility), rel) {
		writeError(w, http.StatusForbidden, "you cannot change this project's sharing scope")
		return
	}

	previous := project.Visibility
	// Count before writing: afterwards every row already says the new value,
	// and "how many were private" would read as zero.
	repoCounts, repoSweep, err := h.projectRepoSweep(r.Context(), project)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load project resources")
		return
	}

	updated, err := h.Queries.SetProjectVisibility(r.Context(), db.SetProjectVisibilityParams{
		ID:          project.ID,
		WorkspaceID: project.WorkspaceID,
		Visibility:  string(vis),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to change sharing scope")
		return
	}

	swept, err := h.Queries.ApplyProjectVisibilityToIssues(r.Context(), db.ApplyProjectVisibilityToIssuesParams{
		WorkspaceID: project.WorkspaceID,
		ProjectID:   project.ID,
		Visibility:  string(vis),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to apply sharing scope to issues")
		return
	}
	if err := h.applyRepoVisibility(r.Context(), project.WorkspaceID, repoSweepURLs(repoSweep), vis); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to apply sharing scope to repositories")
		return
	}

	counts := visibilityCounts{AffectedCount: int64(len(swept))}
	for _, row := range swept {
		if row.PreviousVisibility == string(permission.VisibilityPrivate) {
			counts.PreviouslyPrivateCount++
		}
	}
	counts.add(repoCounts)

	h.recordVisibilityChange(r, project.WorkspaceID, visibilityChange{
		resourceType: "project",
		resourceID:   uuidToString(project.ID),
		previous:     previous,
		next:         vis,
		source:       auditSourceDirect,
		projectID:    project.ID,
	})
	// The swept resources get their own rows, one per kind: the acceptance
	// criterion is that any sharing change can be located, and "the project
	// changed" does not say which issues moved with it.
	issueIDs := make([]string, 0, len(swept))
	issuePrevious := make([]string, 0, len(swept))
	for _, row := range swept {
		issueIDs = append(issueIDs, uuidToString(row.ID))
		issuePrevious = append(issuePrevious, row.PreviousVisibility)
	}
	repoIDs := make([]string, 0, len(repoSweep))
	repoPrevious := make([]string, 0, len(repoSweep))
	for _, entry := range repoSweep {
		repoIDs = append(repoIDs, entry.url)
		previous := entry.previous
		if previous == "" {
			previous = string(permission.VisibilityPrivate)
		}
		repoPrevious = append(repoPrevious, previous)
	}
	h.recordSweptResources(r, project, vis, "issue", issueIDs, issuePrevious)
	h.recordSweptResources(r, project, vis, "repo", repoIDs, repoPrevious)
	h.invalidateProjectAudienceCaches(r.Context(), project.WorkspaceID, project.ID)

	wsID := uuidToString(project.WorkspaceID)
	actorType, actorID := h.resolveActor(r, requestUserID(r), wsID)
	// A project's scope sweep re-scopes every issue it holds, so the invalidation
	// names the project rather than each issue: the client refetches its lists
	// and the HTTP filter decides what comes back.
	h.publishIssueInvalidated(wsID, actorType, actorID, "", uuidToString(updated.ID), "")
	h.publish(protocol.EventProjectUpdated, wsID, actorType, actorID, map[string]any{
		"project": projectToResponse(updated),
	})
	// The sweep may also have re-scoped repositories, which live in the
	// workspace snapshot rather than in the project's own events.
	if len(repoSweep) > 0 {
		h.publishWorkspaceSnapshot(r.Context(), project.WorkspaceID, wsID, actorType, actorID)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"project_id":               uuidToString(updated.ID),
		"visibility":               updated.Visibility,
		"affected_count":           counts.AffectedCount,
		"previously_private_count": counts.PreviouslyPrivateCount,
		"audience_size":            h.audienceSize(r.Context(), project.WorkspaceID, vis, project.ID),
	})
}

// recordSweptResources writes one audit row per resource a project sweep
// overwrote. They are written individually — a findable row per resource is
// the acceptance criterion, and "the project changed" does not say which
// issues moved with it — but in one statement, because a project can hold
// thousands of issues and a round trip each would make the sweep's cost the
// audit's cost. The audience size is the same for every row, so it is computed
// once.
//
// A failed write is logged, not returned: the sweep already committed.
func (h *Handler) recordSweptResources(r *http.Request, project db.Project, vis permission.Visibility, resourceType string, ids, previous []string) {
	if len(ids) == 0 {
		return
	}
	wsID := uuidToString(project.WorkspaceID)
	actorType, actorID := h.resolveActor(r, requestUserID(r), wsID)
	if actorType != "agent" {
		actorType = "member"
	}
	actorUUID, err := parseUUIDSafe(actorID)
	if err != nil {
		slog.Warn("visibility audit: unusable actor id",
			append(logger.RequestAttrs(r), "actor_id", actorID, "error", err)...)
		return
	}
	err = h.Queries.RecordVisibilityChangesBulk(r.Context(), db.RecordVisibilityChangesBulkParams{
		WorkspaceID:          project.WorkspaceID,
		ActorType:            actorType,
		ActorID:              actorUUID,
		ResourceType:         resourceType,
		NewVisibility:        string(vis),
		AudienceSize:         h.audienceSize(r.Context(), project.WorkspaceID, vis, project.ID),
		Source:               auditSourceProjectBulk,
		ResourceIds:          ids,
		PreviousVisibilities: previous,
	})
	if err != nil {
		slog.Error("visibility audit: bulk write failed",
			append(logger.RequestAttrs(r),
				"project_id", uuidToString(project.ID),
				"resource_type", resourceType,
				"count", len(ids),
				"error", err)...)
	}
}

// repoSweepURLs is the apply path's view of a sweep: just the keys.
func repoSweepURLs(entries []repoSweepEntry) []string {
	urls := make([]string, 0, len(entries))
	for _, entry := range entries {
		urls = append(urls, entry.url)
	}
	return urls
}

// ---------------------------------------------------------------------------
// PUT /api/repos/visibility
// ---------------------------------------------------------------------------

type repoVisibilityRequest struct {
	URL        string `json:"url"`
	Visibility string `json:"visibility"`
}

// SetRepoVisibility changes one workspace repository's scope. A repo lives in
// workspace.repos as a JSONB entry keyed by URL (migration 511), so this
// rewrites that entry rather than a row.
func (h *Handler) SetRepoVisibility(w http.ResponseWriter, r *http.Request) {
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace id")
	if !ok {
		return
	}
	var req repoVisibilityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}
	vis, ok := parseVisibilityValue(w, req.Visibility)
	if !ok {
		return
	}
	viewer, err := h.visibilityViewerFor(r, wsUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}
	ws, err := h.Queries.GetWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}
	entries := decodeWorkspaceRepos(ws.Repos)
	index := -1
	for i, entry := range entries {
		if entry.URL == req.URL {
			index = i
			break
		}
	}
	projectIDs := h.repoProjectIDs(r.Context(), wsUUID, req.URL)
	if index < 0 || !viewer.canSeeRepo(entries[index], projectIDs) {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}
	entry := entries[index]
	rel := permission.Relation{
		IsCreator: entry.CreatedBy != "" && entry.CreatedBy == uuidToString(viewer.userID),
	}
	for _, id := range projectIDs {
		if viewer.inProject(id) {
			rel.InProject = true
			break
		}
	}
	if !viewer.bypasses() &&
		!permission.Allowed(viewer.role, permission.ActionChangeVisibility, permission.Visibility(entry.Visibility), rel) {
		writeError(w, http.StatusForbidden, "you cannot change this repository's sharing scope")
		return
	}
	if !permission.CanSetVisibility(vis, len(projectIDs) > 0) {
		writeError(w, http.StatusBadRequest,
			"project visibility needs a project: add this repository to a project first")
		return
	}

	previous := entry.Visibility
	if err := h.applyRepoVisibility(r.Context(), wsUUID, []string{req.URL}, vis); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to change sharing scope")
		return
	}
	scopedProject := pgtype.UUID{}
	if len(projectIDs) > 0 {
		scopedProject = projectIDs[0]
	}
	h.recordVisibilityChange(r, wsUUID, visibilityChange{
		resourceType: "repo",
		resourceID:   req.URL,
		previous:     previous,
		next:         vis,
		source:       auditSourceDirect,
		projectID:    scopedProject,
	})

	// Repos travel inside the workspace snapshot, so the snapshot is what a
	// client refetches — with its own visibility applied at delivery time.
	wsID := uuidToString(wsUUID)
	actorType, actorID := h.resolveActor(r, requestUserID(r), wsID)
	h.publishWorkspaceSnapshot(r.Context(), wsUUID, wsID, actorType, actorID)

	writeJSON(w, http.StatusOK, map[string]any{
		"url":           req.URL,
		"visibility":    string(vis),
		"audience_size": h.audienceSize(r.Context(), wsUUID, vis, scopedProject),
	})
}

// repoProjectIDs is which projects list this repository as a resource. It is
// the repo's answer to "does it belong to a project", the pairing rule's
// input (migration 511).
func (h *Handler) repoProjectIDs(ctx context.Context, wsUUID pgtype.UUID, url string) []pgtype.UUID {
	ids, err := h.Queries.ListProjectIDsForRepoURL(ctx, db.ListProjectIDsForRepoURLParams{
		WorkspaceID: wsUUID,
		Url:         url,
	})
	if err != nil {
		return nil
	}
	return ids
}

// applyRepoVisibility rewrites the scope of the named entries in
// workspace.repos, leaving every other entry and field untouched.
func (h *Handler) applyRepoVisibility(ctx context.Context, wsUUID pgtype.UUID, urls []string, vis permission.Visibility) error {
	if len(urls) == 0 {
		return nil
	}
	ws, err := h.Queries.GetWorkspace(ctx, wsUUID)
	if err != nil {
		return err
	}
	target := make(map[string]struct{}, len(urls))
	for _, url := range urls {
		target[url] = struct{}{}
	}
	entries := decodeWorkspaceRepos(ws.Repos)
	changed := false
	for i := range entries {
		if _, ok := target[entries[i].URL]; !ok {
			continue
		}
		if entries[i].Visibility != string(vis) {
			entries[i].Visibility = string(vis)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	_, err = h.Queries.UpdateWorkspace(ctx, db.UpdateWorkspaceParams{
		ID:    wsUUID,
		Repos: encoded,
	})
	return err
}
