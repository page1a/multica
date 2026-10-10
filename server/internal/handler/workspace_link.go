package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/permission"
	"github.com/multica-ai/multica/server/internal/workspacelink"
)

// Cross-workspace read-only links (DENE-1225). These handlers only parse the
// request and render the answer: who may do what is decided inside
// internal/workspacelink, and the caller is always scoped to its own
// workspace by RequireWorkspaceMember.

func (h *Handler) workspaceLinks() *workspacelink.Service {
	return workspacelink.New(h.Queries, h.TxStarter).WithMemoryLine(h.projectMemoryBriefLine)
}

// workspaceLinkCaller reads the caller's workspace, user and tier from the
// member row RequireWorkspaceMember put on the context.
func (h *Handler) workspaceLinkCaller(w http.ResponseWriter, r *http.Request) (pgtype.UUID, pgtype.UUID, workspacelink.Actor, bool) {
	member, ok := middleware.MemberFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusNotFound, "workspace not found")
		return pgtype.UUID{}, pgtype.UUID{}, workspacelink.Actor{}, false
	}
	actorType, _ := h.resolveActor(r, requestUserID(r), uuidToString(member.WorkspaceID))
	return member.WorkspaceID, member.UserID, workspacelink.Actor{
		Role:    permission.Role(member.Role),
		IsAgent: actorType == "agent" || isMachineCredentialActor(r),
	}, true
}

func writeWorkspaceLinkError(w http.ResponseWriter, err error) {
	var linkErr *workspacelink.Error
	if errors.As(err, &linkErr) {
		writeError(w, linkErr.Status, linkErr.Message)
		return
	}
	slog.Error("workspace link", "error", err)
	writeError(w, http.StatusInternalServerError, "workspace link request failed")
}

// workspaceLinkID parses {id}. A malformed id is "link not found" like any
// other link the caller cannot read.
func workspaceLinkID(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	id, err := parseUUIDSafe(chi.URLParam(r, "id"))
	if err != nil {
		writeWorkspaceLinkError(w, workspacelink.ErrNotFound)
		return pgtype.UUID{}, false
	}
	return id, true
}

func parseProjectUUIDs(w http.ResponseWriter, ids []string) ([]pgtype.UUID, bool) {
	out := make([]pgtype.UUID, 0, len(ids))
	for _, s := range ids {
		id, ok := parseUUIDOrBadRequest(w, s, "project id")
		if !ok {
			return nil, false
		}
		out = append(out, id)
	}
	return out, true
}

// ListWorkspaceLinks — GET /api/workspace-links
func (h *Handler) ListWorkspaceLinks(w http.ResponseWriter, r *http.Request) {
	ws, user, actor, ok := h.workspaceLinkCaller(w, r)
	if !ok {
		return
	}
	links, err := h.workspaceLinks().List(r.Context(), ws, actor)
	if err == nil {
		err = h.workspaceLinks().FillCanSetManaged(r.Context(), links, user, actor)
	}
	if err != nil {
		writeWorkspaceLinkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"links": links,
		// The UI disables what the table would refuse and says why; it
		// reads the answers here instead of re-deriving the table.
		"can": map[string]bool{
			"create": workspacelink.Decide(workspacelink.OpCreate, workspacelink.SideSource, actor),
			"accept": workspacelink.Decide(workspacelink.OpAccept, workspacelink.SideViewer, actor),
			// Pulling also needs ownership of the other workspace; the
			// lookup answers that per address.
			"pull":   workspacelink.Decide(workspacelink.OpAccept, workspacelink.SideViewer, actor),
			"manage": workspacelink.Decide(workspacelink.OpManage, workspacelink.SideSource, actor),
			"audit":  workspacelink.Decide(workspacelink.OpAudit, workspacelink.SideSource, actor),
		},
	})
}

// ListWorkspaceLinkAudit — GET /api/workspace-links/audit
func (h *Handler) ListWorkspaceLinkAudit(w http.ResponseWriter, r *http.Request) {
	ws, _, actor, ok := h.workspaceLinkCaller(w, r)
	if !ok {
		return
	}
	entries, err := h.workspaceLinks().Audit(r.Context(), ws, actor)
	if err != nil {
		writeWorkspaceLinkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// LookupWorkspaceLinkTarget — GET /api/workspace-links/lookup?target=<address>
// Reads a pasted link or slug the same way Create does and names the
// workspace it points to. Exact match only; there is no search. `pull` says
// whether the caller may pull that workspace's projects in, and lists them.
func (h *Handler) LookupWorkspaceLinkTarget(w http.ResponseWriter, r *http.Request) {
	ws, user, actor, ok := h.workspaceLinkCaller(w, r)
	if !ok {
		return
	}
	address := r.URL.Query().Get("target")
	svc := h.workspaceLinks()
	target, err := svc.Lookup(r.Context(), ws, actor, address)
	if err != nil {
		writeWorkspaceLinkError(w, err)
		return
	}
	pull, err := svc.LookupPull(r.Context(), ws, user, actor, address)
	if err != nil {
		writeWorkspaceLinkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspace": target, "pull": pull})
}

type createWorkspaceLinkRequest struct {
	// TargetSlug takes any address workspacelink.TargetSlug reads. With
	// Direction "pull" it names the workspace whose projects come in.
	TargetSlug string   `json:"target_slug"`
	ProjectIDs []string `json:"project_ids"`
	// Direction is "offer" (default: share this workspace's projects) or
	// "pull" (read the other workspace's projects).
	Direction string `json:"direction"`
}

// CreateWorkspaceLink — POST /api/workspace-links
func (h *Handler) CreateWorkspaceLink(w http.ResponseWriter, r *http.Request) {
	ws, user, actor, ok := h.workspaceLinkCaller(w, r)
	if !ok {
		return
	}
	var req createWorkspaceLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	projects, ok := parseProjectUUIDs(w, req.ProjectIDs)
	if !ok {
		return
	}
	var link workspacelink.Link
	var err error
	switch req.Direction {
	case "", "offer":
		link, err = h.workspaceLinks().Create(r.Context(), ws, user, actor, req.TargetSlug, projects)
		if err == nil {
			if id, perr := parseUUIDSafe(link.ID); perr == nil {
				h.notifyWorkspaceLinkOffered(r.Context(), id, user)
			}
		}
	case "pull":
		link, err = h.workspaceLinks().Pull(r.Context(), ws, user, actor, req.TargetSlug, projects)
	default:
		writeError(w, http.StatusBadRequest, `direction must be "offer" or "pull"`)
		return
	}
	if err != nil {
		writeWorkspaceLinkError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, link)
}

type updateWorkspaceLinkRequest struct {
	ProjectIDs *[]string `json:"project_ids"`
	Accept     bool      `json:"accept"`
	// Managed switches managed access (DENE-1663).
	Managed *bool `json:"managed"`
}

// UpdateWorkspaceLink — PATCH /api/workspace-links/{id}
func (h *Handler) UpdateWorkspaceLink(w http.ResponseWriter, r *http.Request) {
	ws, user, actor, ok := h.workspaceLinkCaller(w, r)
	if !ok {
		return
	}
	id, ok := workspaceLinkID(w, r)
	if !ok {
		return
	}
	var req updateWorkspaceLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	patch := workspacelink.Patch{Accept: req.Accept, Managed: req.Managed}
	if req.ProjectIDs != nil {
		projects, ok := parseProjectUUIDs(w, *req.ProjectIDs)
		if !ok {
			return
		}
		patch.ProjectIDs = &projects
	}
	link, err := h.workspaceLinks().Update(r.Context(), ws, user, actor, id, patch)
	if err != nil {
		writeWorkspaceLinkError(w, err)
		return
	}
	if patch.Accept {
		if row, err := h.Queries.GetWorkspaceLink(r.Context(), id); err == nil {
			h.answerWorkspaceLinkRequest(r.Context(), row, user, inboxTypeWorkspaceLinkAccepted)
		}
	}
	writeJSON(w, http.StatusOK, link)
}

// RevokeWorkspaceLink — DELETE /api/workspace-links/{id}
func (h *Handler) RevokeWorkspaceLink(w http.ResponseWriter, r *http.Request) {
	ws, user, actor, ok := h.workspaceLinkCaller(w, r)
	if !ok {
		return
	}
	id, ok := workspaceLinkID(w, r)
	if !ok {
		return
	}
	// Read before the delete: the notices need to know what was revoked.
	before, beforeErr := h.Queries.GetWorkspaceLink(r.Context(), id)
	if err := h.workspaceLinks().Revoke(r.Context(), ws, user, actor, id); err != nil {
		writeWorkspaceLinkError(w, err)
		return
	}
	if beforeErr == nil {
		switch {
		case before.Status != "pending":
			h.archiveWorkspaceLinkRequest(r.Context(), before)
		case before.TargetWorkspaceID == ws:
			h.answerWorkspaceLinkRequest(r.Context(), before, user, inboxTypeWorkspaceLinkDeclined)
		default:
			// The source withdrew its own offer: nobody needs a receipt.
			h.answerWorkspaceLinkRequest(r.Context(), before, user, "")
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetWorkspaceLinkView — GET /api/workspace-links/{id}/view
// The only route source data leaves its workspace through.
func (h *Handler) GetWorkspaceLinkView(w http.ResponseWriter, r *http.Request) {
	ws, _, actor, ok := h.workspaceLinkCaller(w, r)
	if !ok {
		return
	}
	id, ok := workspaceLinkID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	page := workspacelink.Page{Cursor: q.Get("cursor")}
	if l := q.Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil {
			page.Limit = v
		}
	}
	if p := q.Get("project_id"); p != "" {
		pid, err := parseUUIDSafe(p)
		if err != nil {
			writeWorkspaceLinkError(w, workspacelink.ErrNotFound)
			return
		}
		page.ProjectID = pid
	}
	// Role only: an agent reads exactly what its human would.
	view, err := h.workspaceLinks().View(r.Context(), ws, actor.Role, id, page)
	if err != nil {
		writeWorkspaceLinkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}
