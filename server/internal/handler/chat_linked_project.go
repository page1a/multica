package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/permission"
	"github.com/multica-ai/multica/server/internal/workspacelink"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// A chat can attach projects that arrive through a workspace link as
// read-only references (DENE-1643). They are kept apart from the chat's own
// project set: they never become the primary project, never decide the code
// source, and reach the agent only as reference context. Whether one is still
// readable is decided by internal/workspacelink on every write, every list and
// every claim.

// chatSessionLinkedProjectMax mirrors chatSessionProjectMax.
const chatSessionLinkedProjectMax = 10

var errChatLinkedProjectNotFound = errors.New("linked project not found")

// ChatLinkedProjectRef names one shared project through its link.
type ChatLinkedProjectRef struct {
	LinkID    string `json:"link_id"`
	ProjectID string `json:"project_id"`
}

// ChatLinkedProjectResponse is one attached linked project. Title and source
// name are the selection-time snapshot, refreshed while the link still
// resolves, so a revoked entry can still be named while it is marked.
type ChatLinkedProjectResponse struct {
	LinkID     string  `json:"link_id"`
	ProjectID  string  `json:"project_id"`
	Title      string  `json:"title"`
	Icon       *string `json:"icon"`
	SourceName string  `json:"source_name"`
	// Available is false once the link is revoked or the project is no longer
	// shared. The agent stops receiving it at that point.
	Available bool `json:"available"`
}

// parseChatLinkedProjectRefs validates a linked-project list's size and ids.
// Returns ok=false after writing the error response.
func parseChatLinkedProjectRefs(w http.ResponseWriter, raw []ChatLinkedProjectRef) ([]workspacelink.Ref, bool) {
	if len(raw) > chatSessionLinkedProjectMax {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("too many linked projects: at most %d can be attached to one chat", chatSessionLinkedProjectMax))
		return nil, false
	}
	refs := make([]workspacelink.Ref, 0, len(raw))
	for _, item := range raw {
		linkID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(item.LinkID), "linked_projects.link_id")
		if !ok {
			return nil, false
		}
		projectID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(item.ProjectID), "linked_projects.project_id")
		if !ok {
			return nil, false
		}
		refs = append(refs, workspacelink.Ref{LinkID: linkID, ProjectID: projectID})
	}
	return refs, true
}

// requestMemberRole is the caller's tier in the request workspace; the zero
// role reads nothing through a link.
func requestMemberRole(r *http.Request) permission.Role {
	if member, ok := middleware.MemberFromContext(r.Context()); ok {
		return permission.Role(member.Role)
	}
	return ""
}

// resolveChatLinkedProjects checks every ref is readable by role right now.
// One that is not fails the whole write with errChatLinkedProjectNotFound —
// the same answer whatever the reason, as the link view gives.
func (h *Handler) resolveChatLinkedProjects(ctx context.Context, workspaceID pgtype.UUID, role permission.Role, refs []workspacelink.Ref) ([]workspacelink.ReferenceOption, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	resolved, err := h.workspaceLinks().Reachable(ctx, workspaceID, role, refs)
	if err != nil {
		return nil, err
	}
	if len(resolved) != len(dedupeLinkRefs(refs)) {
		return nil, errChatLinkedProjectNotFound
	}
	return resolved, nil
}

// resolveChatLinkedProjectsReplace resolves the complete next set for an open
// chat. A ref the chat already carries may stay even after its link stopped
// resolving: it is kept as its stored snapshot (still stale, still never sent
// to the agent) so the user can drop stale entries one at a time and edit the
// rest of the set around them. A ref the chat does not carry yet is held to
// the same strict check as on create.
func (h *Handler) resolveChatLinkedProjectsReplace(ctx context.Context, session db.ChatSession, role permission.Role, refs []workspacelink.Ref) ([]workspacelink.ReferenceOption, error) {
	refs = dedupeLinkRefs(refs)
	if len(refs) == 0 {
		return nil, nil
	}
	rows, err := h.Queries.ListChatSessionLinkedProjectsForSessions(ctx, []pgtype.UUID{session.ID})
	if err != nil {
		return nil, err
	}
	key := func(link, project pgtype.UUID) string { return uuidToString(link) + "/" + uuidToString(project) }
	bound := make(map[string]db.ChatSessionLinkedProject, len(rows))
	for _, row := range rows {
		bound[key(row.LinkID, row.ProjectID)] = row
	}
	reachable, err := h.workspaceLinks().Reachable(ctx, session.WorkspaceID, role, refs)
	if err != nil {
		return nil, err
	}
	live := make(map[string]workspacelink.ReferenceOption, len(reachable))
	for _, ref := range reachable {
		live[ref.LinkID+"/"+ref.ID] = ref
	}
	out := make([]workspacelink.ReferenceOption, 0, len(refs))
	for _, ref := range refs {
		k := key(ref.LinkID, ref.ProjectID)
		if option, ok := live[k]; ok {
			out = append(out, option)
			continue
		}
		row, ok := bound[k]
		if !ok {
			return nil, errChatLinkedProjectNotFound
		}
		out = append(out, workspacelink.ReferenceOption{
			LinkID: uuidToString(row.LinkID),
			ID:     uuidToString(row.ProjectID),
			Title:  row.Title,
			Source: workspacelink.LinkedWorkspace{Name: row.SourceName},
		})
	}
	return out, nil
}

func dedupeLinkRefs(refs []workspacelink.Ref) []workspacelink.Ref {
	seen := map[[32]byte]bool{}
	out := make([]workspacelink.Ref, 0, len(refs))
	for _, ref := range refs {
		var k [32]byte
		copy(k[:16], ref.LinkID.Bytes[:])
		copy(k[16:], ref.ProjectID.Bytes[:])
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, ref)
	}
	return out
}

// insertChatSessionLinkedProjects stores a resolved set in selection order.
func insertChatSessionLinkedProjects(ctx context.Context, qtx *db.Queries, session db.ChatSession, resolved []workspacelink.ReferenceOption) error {
	for position, ref := range resolved {
		if err := qtx.InsertChatSessionLinkedProject(ctx, db.InsertChatSessionLinkedProjectParams{
			WorkspaceID:   session.WorkspaceID,
			ChatSessionID: session.ID,
			LinkID:        parseUUID(ref.LinkID),
			ProjectID:     parseUUID(ref.ID),
			Title:         ref.Title,
			SourceName:    ref.Source.Name,
			Position:      int32(position),
		}); err != nil {
			return err
		}
	}
	return nil
}

// replaceChatSessionLinkedProjects rewrites a session's linked set in one
// transaction. Callers resolved it first.
func (h *Handler) replaceChatSessionLinkedProjects(ctx context.Context, session db.ChatSession, resolved []workspacelink.ReferenceOption) error {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	if err := qtx.DeleteChatSessionLinkedProjectsForSession(ctx, db.DeleteChatSessionLinkedProjectsForSessionParams{
		ChatSessionID: session.ID, WorkspaceID: session.WorkspaceID,
	}); err != nil {
		return err
	}
	if err := insertChatSessionLinkedProjects(ctx, qtx, session, resolved); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// hydrateChatSessionLinkedProjects fills linked_projects on every response.
// Availability is the workspace's view of the link (any member reads what a
// member reads); the claim re-checks it as the person the run is for.
func (h *Handler) hydrateChatSessionLinkedProjects(ctx context.Context, sessions []ChatSessionResponse) error {
	for i := range sessions {
		sessions[i].LinkedProjects = []ChatLinkedProjectResponse{}
	}
	if len(sessions) == 0 {
		return nil
	}
	ids := make([]pgtype.UUID, 0, len(sessions))
	for _, session := range sessions {
		ids = append(ids, parseUUID(session.ID))
	}
	rows, err := h.Queries.ListChatSessionLinkedProjectsForSessions(ctx, ids)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	byWorkspace := map[[16]byte][]workspacelink.Ref{}
	for _, row := range rows {
		byWorkspace[row.WorkspaceID.Bytes] = append(byWorkspace[row.WorkspaceID.Bytes], workspacelink.Ref{LinkID: row.LinkID, ProjectID: row.ProjectID})
	}
	live := map[string]workspacelink.ReferenceOption{}
	for wsBytes, refs := range byWorkspace {
		resolved, err := h.workspaceLinks().Reachable(ctx, pgtype.UUID{Bytes: wsBytes, Valid: true}, permission.RoleMember, refs)
		if err != nil {
			return err
		}
		for _, ref := range resolved {
			live[ref.LinkID+"/"+ref.ID] = ref
		}
	}
	index := make(map[string]int, len(sessions))
	for i, session := range sessions {
		index[session.ID] = i
	}
	for _, row := range rows {
		i, ok := index[uuidToString(row.ChatSessionID)]
		if !ok {
			continue
		}
		item := ChatLinkedProjectResponse{
			LinkID:     uuidToString(row.LinkID),
			ProjectID:  uuidToString(row.ProjectID),
			Title:      row.Title,
			SourceName: row.SourceName,
		}
		if ref, ok := live[item.LinkID+"/"+item.ProjectID]; ok {
			item.Available = true
			item.Title = ref.Title
			item.Icon = ref.Icon
			item.SourceName = ref.Source.Name
		}
		sessions[i].LinkedProjects = append(sessions[i].LinkedProjects, item)
	}
	return nil
}

// ListChatLinkedProjectOptions — GET /api/workspace-links/projects
// The shared projects this workspace may attach to a chat as read-only
// references. Empty for guests, like the link view.
func (h *Handler) ListChatLinkedProjectOptions(w http.ResponseWriter, r *http.Request) {
	ws, _, actor, ok := h.workspaceLinkCaller(w, r)
	if !ok {
		return
	}
	options, err := h.workspaceLinks().ReferenceOptions(r.Context(), ws, actor.Role)
	if err != nil {
		writeWorkspaceLinkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": options})
}

// TaskLinkedProjectData is one read-only reference project on the daemon
// claim (DENE-1643). Mirror type: daemon LinkedProjectData.
type TaskLinkedProjectData struct {
	Title       string                         `json:"title"`
	SourceName  string                         `json:"source_name"`
	Description string                         `json:"description,omitempty"`
	Resources   []workspacelink.LinkedResource `json:"resources,omitempty"`
	MemoryLine  string                         `json:"memory_line,omitempty"`
}

// resolveClaimChatLinkedProjects re-checks a chat's linked projects for one
// run, as the person the run is for: the task's originator, else the chat's
// creator. A revoked link or a guest simply yields none. A read failure is an
// error so the task is redelivered rather than run without its context.
func (h *Handler) resolveClaimChatLinkedProjects(ctx context.Context, task db.AgentTaskQueue, cs db.ChatSession) ([]TaskLinkedProjectData, error) {
	rows, err := h.Queries.ListChatSessionLinkedProjectsForSessions(ctx, []pgtype.UUID{cs.ID})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	person := task.OriginatorUserID
	if !person.Valid {
		person = cs.CreatorID
	}
	member, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID: person, WorkspaceID: cs.WorkspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	refs := make([]workspacelink.Ref, 0, len(rows))
	for _, row := range rows {
		refs = append(refs, workspacelink.Ref{LinkID: row.LinkID, ProjectID: row.ProjectID})
	}
	resolved, err := h.workspaceLinks().References(ctx, cs.WorkspaceID, permission.Role(member.Role), refs)
	if err != nil {
		return nil, err
	}
	if dropped := len(rows) - len(resolved); dropped > 0 {
		slog.Info("chat claim: linked projects no longer shared were left out",
			"chat_session_id", uuidToString(cs.ID), "dropped", dropped)
	}
	out := make([]TaskLinkedProjectData, 0, len(resolved))
	for _, ref := range resolved {
		out = append(out, TaskLinkedProjectData{
			Title:       ref.Title,
			SourceName:  ref.Source.Name,
			Description: ref.Description,
			Resources:   ref.Resources,
			MemoryLine:  ref.MemoryLine,
		})
	}
	return out, nil
}
