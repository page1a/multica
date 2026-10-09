package workspacelink

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/permission"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Sharing a project shares its project context too (DENE-1643): the
// description, the resources (local directories and repositories) and the
// project-memory line. That is what lets a viewer's chat attach the project
// as a read-only reference for its agent. Like View, everything here is
// re-read on every call, so a revoke or an unticked project stops reaching
// the viewer at once.

// Ref names one shared project through the link it arrives on.
type Ref struct {
	LinkID    pgtype.UUID
	ProjectID pgtype.UUID
}

// ReferenceOption is a shared project a viewer may attach to a chat.
type ReferenceOption struct {
	LinkID string          `json:"link_id"`
	ID     string          `json:"id"`
	Title  string          `json:"title"`
	Icon   *string         `json:"icon"`
	Source LinkedWorkspace `json:"source"`
}

// LinkedResource is one project resource as the viewer sees it: a repository
// URL or a directory path, nothing of the source's machine bookkeeping
// (daemon id, execution mode, repo key).
type LinkedResource struct {
	Type  string  `json:"type"`
	Label *string `json:"label"`
	URL   string  `json:"url,omitempty"`
	Path  string  `json:"path,omitempty"`
}

// ProjectContext is the read-only project context of one shared project.
type ProjectContext struct {
	Description string           `json:"description"`
	Resources   []LinkedResource `json:"resources"`
	// MemoryLine is the project-memory sentence the source's own agents get.
	MemoryLine string `json:"memory_line"`
}

// Reference is a shared project resolved for a viewer's chat run.
type Reference struct {
	ReferenceOption
	ProjectContext
}

// MemoryLineFunc renders a project's memory line; the handler owns the
// project-memory model, so it is injected rather than imported.
type MemoryLineFunc func(context.Context, db.Project) string

// WithMemoryLine returns the service with the memory-line renderer set.
func (s *Service) WithMemoryLine(fn MemoryLineFunc) *Service {
	s.memoryLine = fn
	return s
}

// viewerLinks yields the active links viewerWS may read with role, keyed by
// link id. Same rule as View: viewer side, accepted, Decide(OpView).
func (s *Service) viewerLinks(ctx context.Context, viewerWS pgtype.UUID, role permission.Role) (map[[16]byte]db.ListWorkspaceLinksForWorkspaceRow, []db.ListWorkspaceLinksForWorkspaceRow, error) {
	rows, err := s.q.ListWorkspaceLinksForWorkspace(ctx, viewerWS)
	if err != nil {
		return nil, nil, err
	}
	byID := map[[16]byte]db.ListWorkspaceLinksForWorkspaceRow{}
	ordered := make([]db.ListWorkspaceLinksForWorkspaceRow, 0, len(rows))
	for _, row := range rows {
		if row.TargetWorkspaceID != viewerWS || row.Status != "active" {
			continue
		}
		if !Decide(OpView, SideViewer, Actor{Role: role}) {
			continue
		}
		byID[row.ID.Bytes] = row
		ordered = append(ordered, row)
	}
	return byID, ordered, nil
}

// ReferenceOptions lists every shared project the viewer may attach.
func (s *Service) ReferenceOptions(ctx context.Context, viewerWS pgtype.UUID, role permission.Role) ([]ReferenceOption, error) {
	_, links, err := s.viewerLinks(ctx, viewerWS, role)
	if err != nil {
		return nil, err
	}
	out := []ReferenceOption{}
	for _, link := range links {
		projects, err := s.q.ListLinkedReferenceProjects(ctx, db.ListLinkedReferenceProjectsParams{
			LinkID: link.ID, SourceWorkspaceID: link.SourceWorkspaceID, ProjectIds: []pgtype.UUID{},
		})
		if err != nil {
			return nil, err
		}
		for _, p := range projects {
			out = append(out, optionOf(link, p))
		}
	}
	return out, nil
}

// References resolves refs for a viewer, in refs order, with their project
// context. A ref the viewer can no longer read — revoked, unticked, made
// private, never shared — is simply absent; callers compare against what they
// asked for.
func (s *Service) References(ctx context.Context, viewerWS pgtype.UUID, role permission.Role, refs []Ref) ([]Reference, error) {
	return s.resolve(ctx, viewerWS, role, refs, true)
}

// Reachable is References without the project context: which refs still
// resolve, for marking stale selections.
func (s *Service) Reachable(ctx context.Context, viewerWS pgtype.UUID, role permission.Role, refs []Ref) ([]ReferenceOption, error) {
	resolved, err := s.resolve(ctx, viewerWS, role, refs, false)
	if err != nil {
		return nil, err
	}
	out := make([]ReferenceOption, len(resolved))
	for i, r := range resolved {
		out[i] = r.ReferenceOption
	}
	return out, nil
}

func (s *Service) resolve(ctx context.Context, viewerWS pgtype.UUID, role permission.Role, refs []Ref, withContext bool) ([]Reference, error) {
	if len(refs) == 0 {
		return []Reference{}, nil
	}
	links, _, err := s.viewerLinks(ctx, viewerWS, role)
	if err != nil {
		return nil, err
	}
	type key struct{ link, project [16]byte }
	found := map[key]Reference{}
	wanted := map[[16]byte][]pgtype.UUID{}
	for _, ref := range refs {
		if _, ok := links[ref.LinkID.Bytes]; ok && ref.ProjectID.Valid {
			wanted[ref.LinkID.Bytes] = append(wanted[ref.LinkID.Bytes], ref.ProjectID)
		}
	}
	for linkID, ids := range wanted {
		link := links[linkID]
		projects, err := s.q.ListLinkedReferenceProjects(ctx, db.ListLinkedReferenceProjectsParams{
			LinkID: link.ID, SourceWorkspaceID: link.SourceWorkspaceID, ProjectIds: ids,
		})
		if err != nil {
			return nil, err
		}
		contexts := map[[16]byte]ProjectContext{}
		if withContext {
			if contexts, err = s.projectContexts(ctx, link.SourceWorkspaceID, projects); err != nil {
				return nil, err
			}
		}
		for _, p := range projects {
			found[key{linkID, p.ID.Bytes}] = Reference{ReferenceOption: optionOf(link, p), ProjectContext: contexts[p.ID.Bytes]}
		}
	}
	out := make([]Reference, 0, len(refs))
	seen := map[key]bool{}
	for _, ref := range refs {
		k := key{ref.LinkID.Bytes, ref.ProjectID.Bytes}
		if r, ok := found[k]; ok && !seen[k] {
			seen[k] = true
			out = append(out, r)
		}
	}
	return out, nil
}

// projectContexts reads the description, whitelisted resources and memory
// line of source projects already filtered by the link rule.
func (s *Service) projectContexts(ctx context.Context, sourceWS pgtype.UUID, projects []db.Project) (map[[16]byte]ProjectContext, error) {
	out := make(map[[16]byte]ProjectContext, len(projects))
	if len(projects) == 0 {
		return out, nil
	}
	ids := make([]pgtype.UUID, len(projects))
	for i, p := range projects {
		ids[i] = p.ID
	}
	rows, err := s.q.ListProjectResourcesForProjectsInWorkspace(ctx, db.ListProjectResourcesForProjectsInWorkspaceParams{
		WorkspaceID: sourceWS, ProjectIds: ids,
	})
	if err != nil {
		return nil, err
	}
	resources := map[[16]byte][]LinkedResource{}
	for _, row := range rows {
		if r, ok := linkedResource(row); ok {
			resources[row.ProjectID.Bytes] = append(resources[row.ProjectID.Bytes], r)
		}
	}
	for _, p := range projects {
		pc := ProjectContext{Description: p.Description.String, Resources: resources[p.ID.Bytes]}
		if pc.Resources == nil {
			pc.Resources = []LinkedResource{}
		}
		if s.memoryLine != nil {
			pc.MemoryLine = s.memoryLine(ctx, p)
		}
		out[p.ID.Bytes] = pc
	}
	return out, nil
}

// linkedResource keeps only the pointer an agent reads with. Unknown types
// stay in the source.
func linkedResource(row db.ProjectResource) (LinkedResource, bool) {
	var ref struct {
		URL       string `json:"url"`
		LocalPath string `json:"local_path"`
	}
	_ = json.Unmarshal(row.ResourceRef, &ref)
	out := LinkedResource{Type: row.ResourceType, Label: util.TextToPtr(row.Label)}
	switch row.ResourceType {
	case "github_repo":
		out.URL = strings.TrimSpace(ref.URL)
		return out, out.URL != ""
	case "local_directory":
		out.Path = strings.TrimSpace(ref.LocalPath)
		return out, out.Path != ""
	}
	return LinkedResource{}, false
}

func optionOf(link db.ListWorkspaceLinksForWorkspaceRow, p db.Project) ReferenceOption {
	return ReferenceOption{
		LinkID: util.UUIDToString(link.ID),
		ID:     util.UUIDToString(p.ID),
		Title:  p.Title,
		Icon:   util.TextToPtr(p.Icon),
		Source: LinkedWorkspace{Name: link.SourceName, AvatarURL: util.TextToPtr(link.SourceAvatarUrl)},
	}
}
