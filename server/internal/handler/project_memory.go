package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/projectmemory"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// ProjectMemoryLocation is the stable JSON shape shared by the CLI, web,
// desktop, and daemon. The checklist itself comes from projectmemory.Locations.
type ProjectMemoryLocation struct {
	Key         string  `json:"key"`
	Path        string  `json:"path"`
	Kind        string  `json:"kind"`
	Exists      bool    `json:"exists"`
	IsDirectory bool    `json:"is_directory"`
	ModifiedAt  *string `json:"modified_at"`
	ObservedAt  *string `json:"observed_at"`
	Error       *string `json:"error"`
}

type ProjectMemoryIssue struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Status     string `json:"status"`
	Title      string `json:"title"`
}

type ProjectMemoryResponse struct {
	ProjectID               string                  `json:"project_id"`
	WorkspaceID             string                  `json:"workspace_id"`
	Locations               []ProjectMemoryLocation `json:"locations"`
	Missing                 []string                `json:"missing"`
	ObservedAt              *string                 `json:"observed_at"`
	LatestSedimentAt        *string                 `json:"latest_sediment_at"`
	SedimentIssue           *ProjectMemoryIssue     `json:"sediment_issue"`
	SedimentAgentConfigured bool                    `json:"sediment_agent_configured"`
	SedimentError           *string                 `json:"sediment_error"`
}

type projectMemoryCheckRequest struct {
	Locations []projectmemory.LocationResult `json:"locations"`
}

type daemonProjectMemoryCheckRequest struct {
	ProjectID string                         `json:"project_id"`
	Locations []projectmemory.LocationResult `json:"locations"`
}

type daemonMemoryTarget struct {
	ProjectID string `json:"project_id"`
	Path      string `json:"path"`
	DaemonID  string `json:"daemon_id"`
}

type daemonMemoryTargetsResponse struct {
	WorkspaceID string                   `json:"workspace_id"`
	Checklist   []projectmemory.Location `json:"checklist"`
	Projects    []daemonMemoryTarget     `json:"projects"`
}

func memoryTime(t pgtype.Timestamptz) *string {
	if !t.Valid {
		return nil
	}
	value := timestampToString(t)
	return &value
}

func memoryText(t pgtype.Text) *string {
	if !t.Valid || strings.TrimSpace(t.String) == "" {
		return nil
	}
	value := t.String
	return &value
}

func sedimentAgentAvailable(agent db.Agent) bool {
	return !agent.ArchivedAt.Valid && agent.WorkEnabled && agent.RuntimeID.Valid
}

func memoryProjectID(id string) (pgtype.UUID, error) {
	return util.ParseUUID(id)
}

func (h *Handler) loadVisibleProjectMemoryProject(ctx context.Context, r *http.Request, id string) (db.Project, bool) {
	projectID, err := memoryProjectID(id)
	if err != nil {
		return db.Project{}, false
	}
	workspaceID := h.resolveWorkspaceID(r)
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return db.Project{}, false
	}
	project, err := h.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: wsID})
	if err != nil {
		return db.Project{}, false
	}
	viewer, err := h.visibilityViewerFor(r, wsID)
	if err != nil || !viewer.canSeeProject(project) {
		return db.Project{}, false
	}
	return project, true
}

func (h *Handler) projectMemoryResponse(ctx context.Context, project db.Project) ProjectMemoryResponse {
	rows, _ := h.Queries.ListProjectMemoryStatus(ctx, db.ListProjectMemoryStatusParams{
		ProjectID: project.ID, WorkspaceID: project.WorkspaceID,
	})
	byKey := make(map[string]db.ProjectMemoryStatus, len(rows))
	for _, row := range rows {
		byKey[row.LocationKey] = row
	}

	response := ProjectMemoryResponse{
		ProjectID:   uuidToString(project.ID),
		WorkspaceID: uuidToString(project.WorkspaceID),
		Locations:   make([]ProjectMemoryLocation, 0, len(projectmemory.Locations())),
		Missing:     make([]string, 0, len(projectmemory.Locations())),
	}
	for _, location := range projectmemory.Locations() {
		item := ProjectMemoryLocation{Key: location.Key, Path: location.Path, Kind: location.Kind}
		if row, ok := byKey[location.Key]; ok {
			item.Exists = row.ExistsOnDisk
			item.IsDirectory = row.IsDirectory
			item.ModifiedAt = memoryTime(row.ModifiedAt)
			item.ObservedAt = memoryTime(row.ObservedAt)
			item.Error = memoryText(row.Error)
			if response.ObservedAt == nil {
				response.ObservedAt = item.ObservedAt
			}
		} else {
			item.Error = stringPtr("no daemon check has been reported")
		}
		if !item.Exists {
			response.Missing = append(response.Missing, item.Key)
		}
		response.Locations = append(response.Locations, item)
	}

	ws, wsErr := h.Queries.GetWorkspace(ctx, project.WorkspaceID)
	if wsErr == nil {
		agentID, configErr := configuredSedimentAgent(ws.Settings)
		if configErr == nil {
			if agent, agentErr := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: project.WorkspaceID}); agentErr == nil && sedimentAgentAvailable(agent) {
				response.SedimentAgentConfigured = true
			} else {
				response.SedimentError = stringPtr("memory.sediment_agent is not an available agent in this workspace")
			}
		} else {
			response.SedimentError = stringPtr(configErr.Error())
		}
	}
	if issue, issueErr := h.Queries.FindLatestSedimentIssue(ctx, db.FindLatestSedimentIssueParams{
		WorkspaceID: project.WorkspaceID, Column2: uuidToString(project.ID),
	}); issueErr == nil {
		response.SedimentIssue = h.projectMemoryIssue(ctx, issue)
		response.LatestSedimentAt = memoryTime(issue.UpdatedAt)
	}
	return response
}

func (h *Handler) projectMemoryIssue(ctx context.Context, issue db.Issue) *ProjectMemoryIssue {
	identifier := fmt.Sprintf("%d", issue.Number)
	if workspace, err := h.Queries.GetWorkspace(ctx, issue.WorkspaceID); err == nil && workspace.IssuePrefix != "" {
		identifier = workspace.IssuePrefix + "-" + identifier
	}
	return &ProjectMemoryIssue{ID: uuidToString(issue.ID), Identifier: identifier, Status: issue.Status, Title: issue.Title}
}

func stringPtr(value string) *string { return &value }

// ListProjectMemoryLocations is the checklist the close dialog and any other
// client submit against. It is the same list projectmemory.Locations owns;
// callers must not keep a second copy of the keys.
func (h *Handler) ListProjectMemoryLocations(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"locations": projectmemory.Locations()})
}

// GetProjectMemory is the read-only status endpoint used by `project memory
// status` and the project page. It always returns all five positions, even
// before a daemon has reported its first check.
func (h *Handler) GetProjectMemory(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "id")
	project, ok := h.loadVisibleProjectMemoryProject(r.Context(), r, projectID)
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	writeJSON(w, http.StatusOK, h.projectMemoryResponse(r.Context(), project))
}

func (h *Handler) recordProjectMemoryCheck(ctx context.Context, project db.Project, locations []projectmemory.LocationResult) error {
	byKey := make(map[string]projectmemory.LocationResult, len(locations))
	for _, result := range locations {
		byKey[result.Key] = result
	}
	observedAt := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	for _, location := range projectmemory.Locations() {
		result, ok := byKey[location.Key]
		if !ok {
			result = projectmemory.LocationResult{Location: location, Error: "daemon did not report this location"}
		}
		var modifiedAt pgtype.Timestamptz
		if result.ModifiedAt != nil {
			modifiedAt = pgtype.Timestamptz{Time: result.ModifiedAt.UTC(), Valid: true}
		}
		var errorText pgtype.Text
		if result.Error != "" {
			errorText = pgtype.Text{String: result.Error, Valid: true}
		}
		if _, err := h.Queries.UpsertProjectMemoryStatus(ctx, db.UpsertProjectMemoryStatusParams{
			ProjectID: project.ID, WorkspaceID: project.WorkspaceID, LocationKey: location.Key,
			Path: location.Path, ExistsOnDisk: result.Exists, IsDirectory: result.IsDirectory,
			ModifiedAt: modifiedAt, ObservedAt: observedAt, Error: errorText,
		}); err != nil {
			return err
		}
	}
	return nil
}

// PostProjectMemoryCheck accepts the daemon's stat-only observation. The
// server owns the checklist and paths; client-provided paths are ignored.
func (h *Handler) PostProjectMemoryCheck(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "id")
	project, ok := h.loadVisibleProjectMemoryProject(r.Context(), r, projectID)
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	var request projectMemoryCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid memory check request")
		return
	}
	if err := h.recordProjectMemoryCheck(r.Context(), project, request.Locations); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store project memory check")
		return
	}
	response := h.projectMemoryResponse(r.Context(), project)
	if len(response.Missing) > 0 {
		reason := "缺少项目记忆位置：" + strings.Join(response.Missing, ", ")
		result, err := h.EnsureMemoryRound(r.Context(), project, reason)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "project memory sediment failed")
			return
		}
		if result.Error != "" {
			response.SedimentError = stringPtr(result.Error)
		}
		response = h.projectMemoryResponse(r.Context(), project)
	}
	writeJSON(w, http.StatusOK, response)
}

func configuredSedimentAgent(raw []byte) (pgtype.UUID, error) {
	var settings struct {
		Memory struct {
			SedimentAgent string `json:"sediment_agent"`
		} `json:"memory"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &settings) != nil || strings.TrimSpace(settings.Memory.SedimentAgent) == "" {
		return pgtype.UUID{}, errors.New("memory.sediment_agent is not configured")
	}
	agentID, err := util.ParseUUID(strings.TrimSpace(settings.Memory.SedimentAgent))
	if err != nil {
		return pgtype.UUID{}, errors.New("memory.sediment_agent is not a valid agent UUID")
	}
	return agentID, nil
}

type MemoryRoundResult struct {
	Issue   *db.Issue
	Created bool
	Error   string
}

// EnsureMemoryRound is the idempotent opening trigger. Existing open issues
// identified by sediment_project receive a reason comment; a new issue uses
// the normal IssueService create path so assignment and dispatch stay aligned
// with every other issue. The sediment_round/project keys are the only
// correlation metadata; no separate dedupe table is introduced.
func (h *Handler) EnsureMemoryRound(ctx context.Context, project db.Project, reason string) (MemoryRoundResult, error) {
	workspace, err := h.Queries.GetWorkspace(ctx, project.WorkspaceID)
	if err != nil {
		return MemoryRoundResult{}, err
	}
	agentID, configErr := configuredSedimentAgent(workspace.Settings)
	if configErr != nil {
		return MemoryRoundResult{Error: configErr.Error()}, nil
	}
	if h.IssueService == nil {
		return MemoryRoundResult{Error: "issue service is unavailable; sediment ticket was not created"}, nil
	}
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: project.WorkspaceID})
	if err != nil || !sedimentAgentAvailable(agent) {
		return MemoryRoundResult{Error: "memory.sediment_agent is not an available agent in this workspace"}, nil
	}

	issue, findErr := h.Queries.FindOpenSedimentIssue(ctx, db.FindOpenSedimentIssueParams{
		WorkspaceID: project.WorkspaceID, Column2: uuidToString(project.ID),
	})
	if findErr == nil {
		if _, err := h.Queries.CreateComment(ctx, db.CreateCommentParams{
			IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, AuthorType: "agent", AuthorID: agentID,
			Content: reason, Type: "progress_update", ID: dbid.NewV7(),
		}); err != nil {
			return MemoryRoundResult{}, err
		}
		return MemoryRoundResult{Issue: &issue}, nil
	}
	if !errors.Is(findErr, pgx.ErrNoRows) {
		return MemoryRoundResult{}, findErr
	}

	title := "项目记忆沉淀（" + uuidToString(project.ID) + "）"
	created, createErr := h.IssueService.Create(ctx, service.IssueCreateParams{
		WorkspaceID:    project.WorkspaceID,
		Title:          title,
		Description:    pgtype.Text{String: "补齐项目的五个记忆位置。\n\n" + reason, Valid: true},
		Status:         "todo",
		Priority:       "medium",
		AssigneeType:   pgtype.Text{String: "agent", Valid: true},
		AssigneeID:     agentID,
		CreatorType:    "agent",
		CreatorID:      agentID,
		ProjectID:      project.ID,
		ProjectPinned:  true,
		AllowDuplicate: false,
	}, service.IssueCreateOpts{ActorID: uuidToString(agentID), AnalyticsAgentID: uuidToString(agentID), Platform: "daemon"})
	if createErr != nil && !errors.Is(createErr, service.ErrActiveDuplicate) {
		return MemoryRoundResult{}, createErr
	}
	if created.DuplicateIssue != nil {
		issue = *created.DuplicateIssue
	} else {
		issue = created.Issue
	}
	if _, err := h.Queries.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
		ID: issue.ID, WorkspaceID: issue.WorkspaceID, Key: "sediment_project",
		Value: []byte(fmt.Sprintf("%q", uuidToString(project.ID))),
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		// ErrNoRows means the key is already this project. A concurrent
		// caller won the write; the round still belongs to one ticket.
		return MemoryRoundResult{}, err
	}
	if _, err := h.Queries.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
		ID: issue.ID, WorkspaceID: issue.WorkspaceID, Key: "sediment_round",
		Value: []byte(fmt.Sprintf("%q", uuidToString(dbid.NewV7()))),
	}); err != nil {
		return MemoryRoundResult{}, err
	}
	if created.DuplicateIssue != nil {
		if _, err := h.Queries.CreateComment(ctx, db.CreateCommentParams{
			IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, AuthorType: "agent", AuthorID: agentID,
			Content: reason, Type: "progress_update", ID: dbid.NewV7(),
		}); err != nil {
			return MemoryRoundResult{}, err
		}
	}
	return MemoryRoundResult{Issue: &issue, Created: created.DuplicateIssue == nil}, nil
}

// noteMemoryProgress opens or extends the project's sediment round. A missing
// seat, a bad setting, or a write failure is logged and returned as text. It
// never fails the business event that noticed the progress.
func (h *Handler) noteMemoryProgress(ctx context.Context, workspaceID, projectID pgtype.UUID, reason string) string {
	reason = strings.NewReplacer("\r", " ", "\n", " ").Replace(strings.TrimSpace(reason))
	if !projectID.Valid || reason == "" || h.Queries == nil {
		return ""
	}
	project, err := h.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{
		ID: projectID, WorkspaceID: workspaceID,
	})
	if err != nil {
		slog.Warn("memory round: sediment failed", "error", err, "project_id", uuidToString(projectID), "reason", reason)
		return "project memory sediment failed"
	}
	result, err := h.EnsureMemoryRound(ctx, project, reason)
	if err != nil {
		slog.Warn("memory round: sediment failed", "error", err, "project_id", uuidToString(projectID), "reason", reason)
		return "project memory sediment failed"
	}
	if result.Error != "" {
		slog.Warn("memory round: sediment skipped", "error", result.Error, "project_id", uuidToString(projectID), "reason", reason)
		return result.Error
	}
	return ""
}

// sedimentClosedBarrier records one objective milestone when a child
// completion closes a stage barrier. An intermediate stage passes a stage
// reason; the same call passes the parent reason only once every child the
// state machine counts is terminal. A replay appends to the open round
// instead of opening a second ticket.
func (h *Handler) sedimentClosedBarrier(ctx context.Context, parent, completed db.Issue, children []db.Issue, isTerminal func(db.Issue) bool) {
	if !parent.ProjectID.Valid || isTerminal == nil {
		return
	}
	label := issueIdentifier(h.getIssuePrefix(ctx, parent.WorkspaceID), parent.Number)
	var reason string
	if siblingsAreStaged(children) {
		if !completed.Stage.Valid {
			return
		}
		if next := nextOpenStage(children, completed.Stage.Int32, isTerminal); next > 0 {
			reason = fmt.Sprintf("阶段推进：%s 的第 %d 阶段已终态，下一阶段是 %d", label, completed.Stage.Int32, next)
		} else if stagedChildrenAllTerminal(children, isTerminal) {
			reason = fmt.Sprintf("父票子票全部终态：%s", label)
		} else {
			return
		}
	} else if childrenAllTerminal(children, isTerminal) {
		reason = fmt.Sprintf("父票子票全部终态：%s", label)
	} else {
		return
	}
	h.noteMemoryProgress(ctx, parent.WorkspaceID, parent.ProjectID, reason)
}

func nextOpenStage(children []db.Issue, closedStage int32, isTerminal func(db.Issue) bool) int32 {
	var next int32
	for _, child := range children {
		if !child.Stage.Valid || child.Stage.Int32 <= closedStage || isTerminal(child) {
			continue
		}
		if next == 0 || child.Stage.Int32 < next {
			next = child.Stage.Int32
		}
	}
	return next
}

func stagedChildrenAllTerminal(children []db.Issue, isTerminal func(db.Issue) bool) bool {
	sawStaged := false
	for _, child := range children {
		if !child.Stage.Valid {
			continue
		}
		sawStaged = true
		if !isTerminal(child) {
			return false
		}
	}
	return sawStaged
}

func childrenAllTerminal(children []db.Issue, isTerminal func(db.Issue) bool) bool {
	if len(children) == 0 {
		return false
	}
	for _, child := range children {
		if !isTerminal(child) {
			return false
		}
	}
	return true
}

// projectMemoryBriefLine is the one line a task brief may carry about project
// memory: where the map is, and which checklist paths are missing. It never
// reads file bodies. A status read failure returns an empty line so claim
// still succeeds.
func (h *Handler) projectMemoryBriefLine(ctx context.Context, project db.Project) string {
	if h.Queries == nil {
		return ""
	}
	rows, err := h.Queries.ListProjectMemoryStatus(ctx, db.ListProjectMemoryStatusParams{
		ProjectID: project.ID, WorkspaceID: project.WorkspaceID,
	})
	if err != nil {
		slog.Warn("memory round: brief line skipped", "error", err, "project_id", uuidToString(project.ID))
		return ""
	}
	byKey := make(map[string]db.ProjectMemoryStatus, len(rows))
	for _, row := range rows {
		byKey[row.LocationKey] = row
	}
	mapPath := "AGENTS.md"
	missing := make([]string, 0, len(projectmemory.Locations()))
	for _, location := range projectmemory.Locations() {
		if location.Key == projectmemory.LocationAgents {
			mapPath = location.Path
		}
		row, ok := byKey[location.Key]
		if !ok || !row.ExistsOnDisk {
			missing = append(missing, location.Path)
		}
	}
	missingText := "none"
	if len(missing) > 0 {
		quoted := make([]string, len(missing))
		for i, path := range missing {
			quoted[i] = "`" + path + "`"
		}
		missingText = strings.Join(quoted, ", ")
	}
	return fmt.Sprintf("Project memory: map is `%s`; missing: %s. Open a listed file only when this task needs it.", mapPath, missingText)
}

// GetDaemonProjectMemoryTargets gives a daemon the local-directory resources
// it owns. Non-git and shared directories are intentionally returned too.
func (h *Handler) GetDaemonProjectMemoryTargets(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "workspaceId")
	if !h.requireDaemonWorkspaceAccess(w, r, workspaceID) {
		return
	}
	id, err := util.ParseUUID(workspaceID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid workspace id")
		return
	}
	targets, err := h.listDaemonMemoryTargets(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list project memory targets")
		return
	}
	writeJSON(w, http.StatusOK, daemonMemoryTargetsResponse{WorkspaceID: workspaceID, Checklist: projectmemory.Locations(), Projects: targets})
}

func (h *Handler) listDaemonMemoryTargets(ctx context.Context, workspaceID pgtype.UUID) ([]daemonMemoryTarget, error) {
	resources, err := h.Queries.ListProjectMemoryTargets(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	targets := make([]daemonMemoryTarget, 0, len(resources))
	for _, resource := range resources {
		var ref struct {
			LocalPath string `json:"local_path"`
			DaemonID  string `json:"daemon_id"`
		}
		if json.Unmarshal(resource.ResourceRef, &ref) != nil || strings.TrimSpace(ref.LocalPath) == "" {
			continue
		}
		targets = append(targets, daemonMemoryTarget{ProjectID: uuidToString(resource.ProjectID), Path: ref.LocalPath, DaemonID: ref.DaemonID})
	}
	return targets, nil
}

// ReportDaemonProjectMemoryCheck is the daemon-authenticated write path. The
// daemon token scopes the workspace; the project id in the body is checked
// against that workspace before any status row is written.
func (h *Handler) ReportDaemonProjectMemoryCheck(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "workspaceId")
	if !h.requireDaemonWorkspaceAccess(w, r, workspaceID) {
		return
	}
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid workspace id")
		return
	}
	var request daemonProjectMemoryCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid memory check request")
		return
	}
	projectID, err := util.ParseUUID(request.ProjectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	project, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: wsID})
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if err := h.recordProjectMemoryCheck(r.Context(), project, request.Locations); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store project memory check")
		return
	}
	response := h.projectMemoryResponse(r.Context(), project)
	if len(response.Missing) > 0 {
		result, ensureErr := h.EnsureMemoryRound(r.Context(), project, "缺少项目记忆位置："+strings.Join(response.Missing, ", "))
		if ensureErr != nil {
			writeError(w, http.StatusInternalServerError, "project memory sediment failed")
			return
		}
		if result.Error != "" {
			response.SedimentError = stringPtr(result.Error)
		}
		response = h.projectMemoryResponse(r.Context(), project)
	}
	writeJSON(w, http.StatusOK, response)
}
