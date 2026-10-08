package workspacelink

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/permission"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// A pull is the same link seen from the viewer's settings: "let this
// workspace read projects of that one". It is not a new kind of link. A
// person who owns the other workspace and may accept here holds both
// decisions a link needs — the source owner's offer and the viewer's accept —
// so Pull records both in one transaction and the link is active at once.
// Anyone else is refused: only the source's owner can hand its data out, so
// the viewer has to ask that owner to offer the link (DENE-1582).

// ErrPullNotOwner is the refusal when the caller does not own the other
// workspace. It names what to do instead.
var ErrPullNotOwner = forbidden("only an owner of that workspace can pick its projects; ask its owner to offer the link from there")

// PullTarget is what the pull form needs to know about the other workspace.
type PullTarget struct {
	// Allowed: the caller may pull from this workspace (owner there, owner or
	// admin here).
	Allowed bool `json:"allowed"`
	// Projects are the workspace's shareable projects; empty unless Allowed.
	Projects []ProjectRef `json:"projects"`
}

// pullActor is the caller's tier on the other workspace, or ok=false when
// the caller is not a member there.
func (s *Service) pullActor(ctx context.Context, q *db.Queries, actorUser, sourceWS pgtype.UUID, actor Actor) (Actor, bool, error) {
	m, err := q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: actorUser, WorkspaceID: sourceWS})
	if errors.Is(err, pgx.ErrNoRows) {
		return Actor{}, false, nil
	}
	if err != nil {
		return Actor{}, false, err
	}
	return Actor{Role: permission.Role(m.Role), IsAgent: actor.IsAgent}, true, nil
}

// canPull answers whether the caller holds both decisions of a link from
// sourceWS to actorWS.
func (s *Service) canPull(ctx context.Context, q *db.Queries, actorUser, sourceWS pgtype.UUID, actor Actor) (bool, error) {
	if !Decide(OpAccept, SideViewer, actor) {
		return false, nil
	}
	there, ok, err := s.pullActor(ctx, q, actorUser, sourceWS, actor)
	if err != nil || !ok {
		return false, err
	}
	return Decide(OpCreate, SideSource, there), nil
}

// LookupPull reads an address like Lookup and says whether the caller may
// pull from that workspace, listing its shareable projects when so.
func (s *Service) LookupPull(ctx context.Context, actorWS, actorUser pgtype.UUID, actor Actor, input string) (PullTarget, error) {
	out := PullTarget{Projects: []ProjectRef{}}
	source, err := s.target(ctx, s.q, actorWS, input)
	if err != nil {
		return out, err
	}
	sourceWS := source.ID
	ok, err := s.canPull(ctx, s.q, actorUser, sourceWS, actor)
	if err != nil || !ok {
		return out, err
	}
	rows, err := s.q.ListWorkspaceShareableProjects(ctx, sourceWS)
	if err != nil {
		return out, err
	}
	out.Allowed = true
	for _, p := range rows {
		out.Projects = append(out.Projects, ProjectRef{ID: util.UUIDToString(p.ID), Title: p.Title, Icon: util.TextToPtr(p.Icon)})
	}
	return out, nil
}

// Pull creates an active link from the workspace named by sourceAddress to
// the caller's workspace, exposing projectIDs of that workspace.
func (s *Service) Pull(ctx context.Context, actorWS pgtype.UUID, actorUser pgtype.UUID, actor Actor, sourceAddress string, projectIDs []pgtype.UUID) (Link, error) {
	if !Decide(OpAccept, SideViewer, actor) {
		return Link{}, forbidden("only owners and admins can link another workspace in")
	}
	source, err := s.target(ctx, s.q, actorWS, sourceAddress)
	if err != nil {
		return Link{}, err
	}
	var created db.WorkspaceLink
	err = s.inTx(ctx, func(q *db.Queries) error {
		ok, err := s.canPull(ctx, q, actorUser, source.ID, actor)
		if err != nil {
			return err
		}
		if !ok {
			return ErrPullNotOwner
		}
		ids, err := s.shareableProjects(ctx, q, source.ID, projectIDs)
		if err != nil {
			return err
		}
		created, err = q.CreateWorkspaceLink(ctx, db.CreateWorkspaceLinkParams{
			SourceWorkspaceID: source.ID, TargetWorkspaceID: actorWS, CreatedBy: actorUser,
		})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return &Error{Status: 409, Message: "that workspace is already linked to this workspace"}
		}
		if err != nil {
			return err
		}
		if err := q.AddWorkspaceLinkProjects(ctx, db.AddWorkspaceLinkProjectsParams{LinkID: created.ID, ProjectIds: ids}); err != nil {
			return err
		}
		// Each decision is audited on the side whose authority it used.
		if err := audit(ctx, q, created, source.ID, actorUser, "create", map[string]any{
			"project_ids": uuidStrings(ids), "pulled": true,
		}); err != nil {
			return err
		}
		accepted, err := q.AcceptWorkspaceLink(ctx, db.AcceptWorkspaceLinkParams{ID: created.ID, AcceptedBy: actorUser})
		if err != nil {
			return err
		}
		return audit(ctx, q, accepted, actorWS, actorUser, "accept", map[string]any{"pulled": true})
	})
	if err != nil {
		return Link{}, err
	}
	return s.one(ctx, actorWS, actor, created.ID)
}
