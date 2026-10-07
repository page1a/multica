package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/routing"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Workspace domains (DENE-1451).
//
// A domain is one row of the workspace's list. A project carries any number of
// them (none = generic), an issue works in at most one of its project's, and a
// specialisation is "a base role + one domain". Routing and the per-quote rule
// both read the issue's domain to pick which specialisation of a base role
// takes the work, so every reference is by id: renaming a project or a domain
// never loses the classification.

const maxDomainNameRunes = 20

type DomainResponse struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Position     int32  `json:"position"`
	ProjectCount int64  `json:"project_count"`
	AgentCount   int64  `json:"agent_count"`
}

type domainRequest struct {
	Name string `json:"name"`
}

func validateDomainName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	switch {
	case name == "":
		return "", errors.New("name is required")
	case name == routing.GenericDirection:
		return "", fmt.Errorf("%s is the absence of a domain, not a domain", routing.GenericDirection)
	case utf8.RuneCountInString(name) > maxDomainNameRunes:
		return "", fmt.Errorf("name must be at most %d characters", maxDomainNameRunes)
	case strings.ContainsAny(name, "*,"):
		return "", errors.New("name may not contain * or ,")
	}
	return name, nil
}

func (h *Handler) ListDomains(w http.ResponseWriter, r *http.Request) {
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return
	}
	out, err := h.listDomainResponses(r.Context(), wsUUID)
	if err != nil {
		slog.Warn("ListDomains failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list domains")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": out})
}

func (h *Handler) listDomainResponses(ctx context.Context, wsUUID pgtype.UUID) ([]DomainResponse, error) {
	rows, err := h.Queries.ListWorkspaceDomains(ctx, wsUUID)
	if err != nil {
		return nil, err
	}
	usage, err := h.Queries.CountWorkspaceDomainUsage(ctx, wsUUID)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]db.CountWorkspaceDomainUsageRow, len(usage))
	for _, u := range usage {
		counts[uuidToString(u.ID)] = u
	}
	out := make([]DomainResponse, 0, len(rows))
	for _, d := range rows {
		id := uuidToString(d.ID)
		out = append(out, DomainResponse{
			ID: id, Name: d.Name, Position: d.Position,
			ProjectCount: counts[id].ProjectCount, AgentCount: counts[id].AgentCount,
		})
	}
	return out, nil
}

func (h *Handler) CreateDomain(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}
	var req domainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name, err := validateDomainName(req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	d, err := h.Queries.CreateWorkspaceDomain(r.Context(), db.CreateWorkspaceDomainParams{
		WorkspaceID: parseUUID(workspaceID), Name: name,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a domain with that name already exists")
			return
		}
		slog.Warn("CreateDomain failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create domain")
		return
	}
	writeJSON(w, http.StatusCreated, DomainResponse{ID: uuidToString(d.ID), Name: d.Name, Position: d.Position})
}

// RenameDomain renames a domain and every specialisation named after it, so
// 孙悟空出海 becomes 孙悟空海外 when 出海 becomes 海外: the name of a
// specialisation is derived from its base role and domain, never typed.
func (h *Handler) RenameDomain(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "domain id")
	if !ok {
		return
	}
	var req domainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name, err := validateDomainName(req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	wsUUID := parseUUID(workspaceID)
	ctx := r.Context()
	prev, err := h.Queries.GetWorkspaceDomain(ctx, db.GetWorkspaceDomainParams{ID: id, WorkspaceID: wsUUID})
	if err != nil {
		writeError(w, http.StatusNotFound, "domain not found")
		return
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to rename domain")
		return
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	d, err := qtx.RenameWorkspaceDomain(ctx, db.RenameWorkspaceDomainParams{ID: id, WorkspaceID: wsUUID, Name: name})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a domain with that name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to rename domain")
		return
	}
	seats, err := qtx.ListSpecializationsByDomain(ctx, db.ListSpecializationsByDomainParams{
		WorkspaceID: wsUUID, DomainID: id, OldName: prev.Name,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to rename domain")
		return
	}
	renamed := make([]db.Agent, 0, len(seats))
	for _, seat := range seats {
		next := strings.TrimSuffix(seat.Name, prev.Name) + name
		a, err := qtx.RenameAgentForDomain(ctx, db.RenameAgentForDomainParams{ID: seat.ID, WorkspaceID: wsUUID, Name: next})
		if err != nil {
			if isUniqueViolation(err) {
				writeError(w, http.StatusConflict, "an agent named "+next+" already exists")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to rename domain")
			return
		}
		renamed = append(renamed, a)
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to rename domain")
		return
	}
	for _, a := range renamed {
		h.publishAgentUpdate(r, a)
	}
	writeJSON(w, http.StatusOK, DomainResponse{ID: uuidToString(d.ID), Name: d.Name, Position: d.Position})
}

// DeleteDomain removes a domain nobody uses. A project or a live
// specialisation still carrying it blocks the delete: dropping it silently
// would re-route that work to generic seats without anyone deciding so.
func (h *Handler) DeleteDomain(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "domain id")
	if !ok {
		return
	}
	wsUUID := parseUUID(workspaceID)
	ctx := r.Context()
	d, err := h.Queries.GetWorkspaceDomain(ctx, db.GetWorkspaceDomainParams{ID: id, WorkspaceID: wsUUID})
	if err != nil {
		writeError(w, http.StatusNotFound, "domain not found")
		return
	}
	usage, err := h.Queries.CountWorkspaceDomainUsage(ctx, wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete domain")
		return
	}
	for _, u := range usage {
		if u.ID == id && (u.ProjectCount > 0 || u.AgentCount > 0) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":         fmt.Sprintf("%s is still used by %d project(s) and %d specialisation(s)", d.Name, u.ProjectCount, u.AgentCount),
				"code":          "domain_in_use",
				"project_count": u.ProjectCount,
				"agent_count":   u.AgentCount,
			})
			return
		}
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete domain")
		return
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	if _, err := qtx.ClearIssueDomain(ctx, db.ClearIssueDomainParams{WorkspaceID: wsUUID, DomainID: id}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete domain")
		return
	}
	if _, err := qtx.ClearArchivedAgentDomain(ctx, db.ClearArchivedAgentDomainParams{WorkspaceID: wsUUID, DomainID: id}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete domain")
		return
	}
	if _, err := qtx.DeleteWorkspaceDomain(ctx, db.DeleteWorkspaceDomainParams{ID: id, WorkspaceID: wsUUID}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete domain")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete domain")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// domainIndex is one workspace's domain list, addressable by id and by name.
type domainIndex struct {
	byID   map[string]db.WorkspaceDomain
	byName map[string]db.WorkspaceDomain
	order  []db.WorkspaceDomain
}

func (h *Handler) loadDomainIndex(ctx context.Context, wsUUID pgtype.UUID) (domainIndex, error) {
	rows, err := h.Queries.ListWorkspaceDomains(ctx, wsUUID)
	if err != nil {
		return domainIndex{}, err
	}
	idx := domainIndex{
		byID:   make(map[string]db.WorkspaceDomain, len(rows)),
		byName: make(map[string]db.WorkspaceDomain, len(rows)),
		order:  rows,
	}
	for _, d := range rows {
		idx.byID[uuidToString(d.ID)] = d
		idx.byName[strings.ToLower(d.Name)] = d
	}
	return idx, nil
}

// resolve accepts a domain id or a domain name — the web sends ids, a person
// typing a CLI flag types names.
func (idx domainIndex) resolve(ref string) (db.WorkspaceDomain, bool) {
	ref = strings.TrimSpace(ref)
	if d, ok := idx.byID[strings.ToLower(ref)]; ok {
		return d, true
	}
	d, ok := idx.byName[strings.ToLower(ref)]
	return d, ok
}

func (idx domainIndex) name(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return idx.byID[uuidToString(id)].Name
}

func (idx domainIndex) names() []string {
	out := make([]string, 0, len(idx.order))
	for _, d := range idx.order {
		out = append(out, d.Name)
	}
	return out
}

// resolveDomainRefs turns a request's domain list into ids, in the
// workspace's order and without repeats. 通用 and empty strings mean "no
// domain" and are dropped, so ["通用"] clears the field.
func (idx domainIndex) resolveRefs(refs []string) ([]pgtype.UUID, error) {
	picked := map[string]bool{}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" || ref == routing.GenericDirection {
			continue
		}
		d, ok := idx.resolve(ref)
		if !ok {
			return nil, fmt.Errorf("unknown domain %q; known domains: %s", ref, strings.Join(idx.names(), ", "))
		}
		picked[uuidToString(d.ID)] = true
	}
	out := make([]pgtype.UUID, 0, len(picked))
	for _, d := range idx.order {
		if picked[uuidToString(d.ID)] {
			out = append(out, d.ID)
		}
	}
	return out, nil
}

func domainIDStrings(ids []pgtype.UUID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, uuidToString(id))
	}
	return out
}

// setProjectDomains writes a project's domains and keeps its issues inside
// them: an issue whose domain the project dropped goes generic, and when the
// project is left with exactly one domain every generic issue takes it.
func (h *Handler) setProjectDomains(ctx context.Context, q *db.Queries, project db.Project, ids []pgtype.UUID) (db.Project, error) {
	if ids == nil {
		ids = []pgtype.UUID{}
	}
	updated, err := q.SetProjectDomains(ctx, db.SetProjectDomainsParams{
		ID: project.ID, WorkspaceID: project.WorkspaceID, DomainIds: ids,
	})
	if err != nil {
		return db.Project{}, err
	}
	if _, err := q.ClearProjectIssueDomainsOutside(ctx, db.ClearProjectIssueDomainsOutsideParams{
		WorkspaceID: project.WorkspaceID, ProjectID: project.ID, DomainIds: ids,
	}); err != nil {
		return db.Project{}, err
	}
	if len(ids) == 1 {
		if _, err := q.FillProjectIssueDomain(ctx, db.FillProjectIssueDomainParams{
			WorkspaceID: project.WorkspaceID, ProjectID: project.ID, DomainID: ids[0],
		}); err != nil {
			return db.Project{}, err
		}
	}
	return updated, nil
}

// issueDomainFor decides the domain an issue is stored with.
//
//   - requested is the request's value: nil = not sent, "" or 通用 = generic.
//   - current is what the issue holds now (zero on create).
//
// The answer must be one of the project's domains. A project with exactly one
// domain gives it to an issue nobody classified; an issue with no project has
// no domain.
func (h *Handler) issueDomainFor(ctx context.Context, wsUUID, projectID pgtype.UUID, requested *string, current pgtype.UUID) (pgtype.UUID, error) {
	if !projectID.Valid {
		if requested != nil && strings.TrimSpace(*requested) != "" && strings.TrimSpace(*requested) != routing.GenericDirection {
			return pgtype.UUID{}, errors.New("domain needs a project: an issue can only work in one of its project's domains")
		}
		return pgtype.UUID{}, nil
	}
	project, err := h.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: wsUUID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, nil
		}
		return pgtype.UUID{}, err
	}
	allowed := make(map[pgtype.UUID]bool, len(project.DomainIds))
	for _, id := range project.DomainIds {
		allowed[id] = true
	}
	if requested != nil {
		ref := strings.TrimSpace(*requested)
		if ref == "" || ref == routing.GenericDirection {
			return pgtype.UUID{}, nil
		}
		idx, err := h.loadDomainIndex(ctx, wsUUID)
		if err != nil {
			return pgtype.UUID{}, err
		}
		d, ok := idx.resolve(ref)
		if !ok || !allowed[d.ID] {
			names := make([]string, 0, len(project.DomainIds))
			for _, id := range project.DomainIds {
				names = append(names, idx.name(id))
			}
			if len(names) == 0 {
				return pgtype.UUID{}, fmt.Errorf("project %s has no domains, so its issues are generic", project.Title)
			}
			return pgtype.UUID{}, fmt.Errorf("domain %q is not one of project %s's domains (%s)", ref, project.Title, strings.Join(names, ", "))
		}
		return d.ID, nil
	}
	if current.Valid && allowed[current] {
		return current, nil
	}
	if len(project.DomainIds) == 1 {
		return project.DomainIds[0], nil
	}
	return pgtype.UUID{}, nil
}

// domainIDField is an issue's domain as a full read reports it: the id, or ""
// for generic. Never nil, so the key is always present on a full read.
func domainIDField(id pgtype.UUID) *string {
	s := ""
	if id.Valid {
		s = uuidToString(id)
	}
	return &s
}

// settleIssueDomain brings an issue's stored domain in line with its project
// after a write: the requested domain (already validated) when one was sent,
// otherwise the current one if the project still carries it, else the
// project's only domain, else generic. Best effort — the issue write itself
// already landed.
func (h *Handler) settleIssueDomain(ctx context.Context, issue db.Issue, requested *string) db.Issue {
	next, err := h.issueDomainFor(ctx, issue.WorkspaceID, issue.ProjectID, requested, issue.DomainID)
	if err != nil {
		slog.Warn("settle issue domain", "issue_id", uuidToString(issue.ID), "error", err)
		return issue
	}
	if next == issue.DomainID {
		return issue
	}
	updated, err := h.Queries.SetIssueDomain(ctx, db.SetIssueDomainParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, DomainID: next})
	if err != nil {
		slog.Warn("settle issue domain", "issue_id", uuidToString(issue.ID), "error", err)
		return issue
	}
	return updated
}

// requestedDomain reads domain_id from a request body: nil when the key was
// not sent, "" for an explicit null.
func requestedDomain(raw map[string]json.RawMessage, value *string) *string {
	if _, ok := raw["domain_id"]; !ok {
		return nil
	}
	if value == nil {
		empty := ""
		return &empty
	}
	return value
}

// domainNameList is a workspace's domain names in list order; empty on a
// failed read.
func (h *Handler) domainNameList(ctx context.Context, wsUUID pgtype.UUID) []string {
	rows, err := h.Queries.ListWorkspaceDomains(ctx, wsUUID)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(rows))
	for _, d := range rows {
		out = append(out, d.Name)
	}
	return out
}
