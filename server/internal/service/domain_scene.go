package service

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// DomainScene is an issue's scene (DENE-1477) as the database stores it and
// as routing reads it. It is the only place that reads an issue's domain
// together with its project's: dispatch, quota relay and the quoted
// base-name swap all start here, then judge fit with routing.DomainFit.
type DomainScene struct {
	// IDs are the scene's domain ids, empty for a generic scene.
	IDs []pgtype.UUID
	// Issue is the issue's own domain name and Project its project's domain
	// names in workspace order — the inputs of routing.ResolveScene, kept
	// apart so routing can still say where the scene came from.
	Issue   string
	Project []string
	// Scene is routing.ResolveScene over those names.
	Scene routing.Scene
	// ProjectName is the project's title, "" without a project.
	ProjectName string
	// Names maps every workspace domain id to its name.
	Names map[pgtype.UUID]string
}

// LoadDomainScene resolves the scene of work in a project with an optional
// issue domain: the issue's own domain, else the project's domains, else
// generic. A read error degrades to generic rather than failing the caller —
// a pick in the generic scene is still a pick.
func LoadDomainScene(ctx context.Context, q *db.Queries, workspaceID, issueDomain, projectID pgtype.UUID) DomainScene {
	out := DomainScene{Names: WorkspaceDomainNames(ctx, q, workspaceID)}
	var projectDomains []pgtype.UUID
	if projectID.Valid {
		if p, err := q.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: workspaceID}); err == nil {
			out.ProjectName = p.Title
			projectDomains = p.DomainIds
		}
	}
	ids := make([]string, 0, len(projectDomains))
	for _, id := range projectDomains {
		if out.Names[id] == "" {
			continue // a domain the workspace no longer has
		}
		ids = append(ids, util.UUIDToString(id))
		out.Project = append(out.Project, out.Names[id])
	}
	issue := ""
	if issueDomain.Valid && out.Names[issueDomain] != "" {
		issue = util.UUIDToString(issueDomain)
		out.Issue = out.Names[issueDomain]
	}
	for _, id := range routing.ResolveScene(issue, ids).Domains {
		out.IDs = append(out.IDs, util.MustParseUUID(id))
	}
	out.Scene = routing.ResolveScene(out.Issue, out.Project)
	return out
}

// IssueDomainScene is LoadDomainScene for a stored issue.
func IssueDomainScene(ctx context.Context, q *db.Queries, issue db.Issue) DomainScene {
	return LoadDomainScene(ctx, q, issue.WorkspaceID, issue.DomainID, issue.ProjectID)
}

// WorkspaceDomainNames maps a workspace's domain ids to their names; empty
// when they cannot be read.
func WorkspaceDomainNames(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID) map[pgtype.UUID]string {
	out := map[pgtype.UUID]string{}
	rows, err := q.ListWorkspaceDomains(ctx, workspaceID)
	if err != nil {
		return out
	}
	for _, d := range rows {
		out[d.ID] = d.Name
	}
	return out
}

// AgentDomainFit is routing.DomainFit for a stored agent in a scene: its
// recorded domain, and only for an agent with none recorded the legacy
// base + domain name suffix.
func AgentDomainFit(scene routing.Scene, agent db.Agent, names map[pgtype.UUID]string) routing.Fit {
	return routing.DomainFit(scene, AgentDomain(agent, names))
}

// AgentDomain is the domain a stored agent is a specialisation for: its
// recorded domain, else read off its name (base + domain) for an agent that
// predates recorded domains. "" for a base role.
func AgentDomain(agent db.Agent, names map[pgtype.UUID]string) string {
	if name := names[agent.DomainID]; name != "" {
		return name
	}
	directions := make([]string, 0, len(names))
	for _, n := range names {
		directions = append(directions, n)
	}
	sort.Strings(directions)
	return routing.DefaultLadder.WithDomains(directions).SeatDomain(agent.Name)
}
