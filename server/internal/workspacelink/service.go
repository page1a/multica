package workspacelink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Error is a refusal the handler turns into an HTTP status as-is.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// ErrNotFound is the one answer for every link the caller may not read: no
// such link, a pending link, the wrong side, a guest. It is byte-for-byte the
// same in each case so nothing can be learned from the difference.
var ErrNotFound = &Error{Status: 404, Message: "link not found"}

func forbidden(msg string) error { return &Error{Status: 403, Message: msg} }
func invalid(msg string) error   { return &Error{Status: 400, Message: msg} }

// TxStarter opens the transaction a write runs in.
type TxStarter interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Service is the module's only entry point.
type Service struct {
	q  *db.Queries
	tx TxStarter
	// now is swappable for tests.
	now func() time.Time
	// memoryLine renders a shared project's memory line (WithMemoryLine).
	memoryLine MemoryLineFunc
}

// New builds a Service over the shared queries and pool.
func New(q *db.Queries, tx TxStarter) *Service {
	return &Service{q: q, tx: tx, now: time.Now}
}

// Link is a link as the management surfaces show it.
type Link struct {
	ID string `json:"id"`
	// Side is how the caller's workspace relates to this link.
	Side   Side   `json:"side"`
	Status string `json:"status"`
	// Source / Target identify both workspaces by display fields only.
	Source WorkspaceRef `json:"source"`
	Target WorkspaceRef `json:"target"`
	// Projects is only filled for the source side's managers; the viewer
	// learns what is shared through View, which re-checks it.
	Projects []ProjectRef `json:"projects"`
	// Managed: the source lets the viewer's agents manage its issues and
	// autopilots for their run's originator (DENE-1663).
	Managed bool `json:"managed"`
	// CanSetManaged answers whether the caller may switch Managed; only the
	// list fills it (FillCanSetManaged), so the UI disables the switch and
	// says why instead of re-deriving the rule.
	CanSetManaged bool    `json:"can_set_managed"`
	CreatedAt     string  `json:"created_at"`
	AcceptedAt    *string `json:"accepted_at"`
}

// WorkspaceRef names a workspace without exposing anything inside it.
type WorkspaceRef struct {
	// ID is filled on the link list only: `--linked` needs the source's id
	// to address it.
	ID        string  `json:"id,omitempty"`
	Name      string  `json:"name"`
	Slug      string  `json:"slug"`
	AvatarURL *string `json:"avatar_url"`
}

// ProjectRef is a project the source picked for a link.
type ProjectRef struct {
	ID    string  `json:"id"`
	Title string  `json:"title"`
	Icon  *string `json:"icon"`
}

// AuditEntry is one row of the link audit trail.
type AuditEntry struct {
	LinkID    string          `json:"link_id"`
	Action    string          `json:"action"`
	ActorName string          `json:"actor_name"`
	BySide    Side            `json:"by_side"`
	Source    string          `json:"source_workspace_id"`
	Target    string          `json:"target_workspace_id"`
	Detail    json.RawMessage `json:"detail"`
	CreatedAt string          `json:"created_at"`
}

// Patch is an Update request. Exactly one field is set: the source changes
// projects or switches managed access, the viewer accepts. No caller is on
// both sides of a link.
type Patch struct {
	ProjectIDs *[]pgtype.UUID
	Accept     bool
	// Managed switches managed access (DENE-1663).
	Managed *bool
}

// sideOf answers how ws relates to a link.
func sideOf(link db.WorkspaceLink, ws pgtype.UUID) Side {
	switch {
	case link.SourceWorkspaceID == ws:
		return SideSource
	case link.TargetWorkspaceID == ws:
		return SideViewer
	}
	return SideNone
}

// load reads a link and the caller's side. A link the caller's workspace is
// not on is ErrNotFound, exactly like a missing one.
func (s *Service) load(ctx context.Context, q *db.Queries, ws, linkID pgtype.UUID) (db.WorkspaceLink, Side, error) {
	link, err := q.GetWorkspaceLink(ctx, linkID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.WorkspaceLink{}, SideNone, ErrNotFound
	}
	if err != nil {
		return db.WorkspaceLink{}, SideNone, err
	}
	side := sideOf(link, ws)
	if side == SideNone {
		return db.WorkspaceLink{}, SideNone, ErrNotFound
	}
	return link, side, nil
}

// List returns the links the caller may know about. Owners and admins get the
// management list (both directions, pending ones, the source's project
// picks). Everyone else who may read — members and agents — gets only the
// active links their workspace views, which is what the sidebar and the
// `workspace link list` command need to reach View, plus — for an agent of an
// owner or admin — the offers waiting for this workspace's answer. Guests get
// nothing.
func (s *Service) List(ctx context.Context, actorWS pgtype.UUID, actor Actor) ([]Link, error) {
	rows, err := s.q.ListWorkspaceLinksForWorkspace(ctx, actorWS)
	if err != nil {
		return nil, err
	}
	links := make([]Link, 0, len(rows))
	for _, row := range rows {
		link := db.WorkspaceLink{
			ID: row.ID, SourceWorkspaceID: row.SourceWorkspaceID, TargetWorkspaceID: row.TargetWorkspaceID,
			Status: row.Status, CreatedAt: row.CreatedAt, AcceptedAt: row.AcceptedAt,
		}
		side := sideOf(link, actorWS)
		manage := Decide(OpManage, side, actor)
		readable := Decide(OpView, side, actor) && row.Status == "active"
		waiting := Decide(OpSeePending, side, actor) && row.Status == "pending"
		if !manage && !readable && !waiting {
			continue
		}
		out := Link{
			ID:     util.UUIDToString(row.ID),
			Side:   side,
			Status: row.Status,
			Source: WorkspaceRef{ID: util.UUIDToString(row.SourceWorkspaceID), Name: row.SourceName, Slug: row.SourceSlug, AvatarURL: util.TextToPtr(row.SourceAvatarUrl)},
			Target: WorkspaceRef{ID: util.UUIDToString(row.TargetWorkspaceID), Name: row.TargetName, Slug: row.TargetSlug, AvatarURL: util.TextToPtr(row.TargetAvatarUrl)},

			Projects:   []ProjectRef{},
			Managed:    row.Managed,
			CreatedAt:  util.TimestampToString(row.CreatedAt),
			AcceptedAt: timestampPtr(row.AcceptedAt),
		}
		if manage && side == SideSource {
			projects, err := s.q.ListWorkspaceLinkProjects(ctx, db.ListWorkspaceLinkProjectsParams{
				LinkID: row.ID, SourceWorkspaceID: actorWS,
			})
			if err != nil {
				return nil, err
			}
			for _, p := range projects {
				out.Projects = append(out.Projects, ProjectRef{
					ID: util.UUIDToString(p.ID), Title: p.Title, Icon: util.TextToPtr(p.Icon),
				})
			}
		}
		links = append(links, out)
	}
	return links, nil
}

// Audit returns the audit trail of every link the workspace was ever on,
// including revoked ones. Owners only.
func (s *Service) Audit(ctx context.Context, actorWS pgtype.UUID, actor Actor) ([]AuditEntry, error) {
	// The workspace is on every link it is asked about, so either side works
	// for the table; the source/viewer split does not change OpAudit.
	if !Decide(OpAudit, SideSource, actor) {
		return nil, forbidden("only the workspace owner can read the link audit trail")
	}
	rows, err := s.q.ListWorkspaceLinkAudit(ctx, actorWS)
	if err != nil {
		return nil, err
	}
	out := make([]AuditEntry, 0, len(rows))
	for _, row := range rows {
		by := SideViewer
		if row.WorkspaceID == row.SourceWorkspaceID {
			by = SideSource
		}
		detail := json.RawMessage(row.Detail)
		if len(detail) == 0 {
			detail = json.RawMessage("{}")
		}
		out = append(out, AuditEntry{
			LinkID:    util.UUIDToString(row.LinkID),
			Action:    row.Action,
			ActorName: row.ActorName.String,
			BySide:    by,
			Source:    util.UUIDToString(row.SourceWorkspaceID),
			Target:    util.UUIDToString(row.TargetWorkspaceID),
			Detail:    detail,
			CreatedAt: util.TimestampToString(row.CreatedAt),
		})
	}
	return out, nil
}

// Create offers a pending link from the caller's workspace (the source) to
// the workspace named by targetSlug (any address TargetSlug reads),
// exposing projectIDs.
func (s *Service) Create(ctx context.Context, actorWS pgtype.UUID, actorUser pgtype.UUID, actor Actor, targetSlug string, projectIDs []pgtype.UUID) (Link, error) {
	if !Decide(OpCreate, SideSource, actor) {
		return Link{}, forbidden("only the workspace owner can link data out of the workspace")
	}
	target, err := s.target(ctx, s.q, actorWS, targetSlug)
	if err != nil {
		return Link{}, err
	}

	var created db.WorkspaceLink
	err = s.inTx(ctx, func(q *db.Queries) error {
		ids, err := s.shareableProjects(ctx, q, actorWS, projectIDs)
		if err != nil {
			return err
		}
		created, err = q.CreateWorkspaceLink(ctx, db.CreateWorkspaceLinkParams{
			SourceWorkspaceID: actorWS, TargetWorkspaceID: target.ID, CreatedBy: actorUser,
		})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return &Error{Status: 409, Message: "this workspace is already linked to that workspace"}
		}
		if err != nil {
			return err
		}
		if err := q.AddWorkspaceLinkProjects(ctx, db.AddWorkspaceLinkProjectsParams{LinkID: created.ID, ProjectIds: ids}); err != nil {
			return err
		}
		return audit(ctx, q, created, actorWS, actorUser, "create", map[string]any{
			"target_slug": target.Slug, "project_ids": uuidStrings(ids),
		})
	})
	if err != nil {
		return Link{}, err
	}
	return s.one(ctx, actorWS, actor, created.ID)
}

// Update changes the projects a link exposes (source owner) or accepts it
// (viewer owner/admin).
func (s *Service) Update(ctx context.Context, actorWS pgtype.UUID, actorUser pgtype.UUID, actor Actor, linkID pgtype.UUID, patch Patch) (Link, error) {
	set := 0
	for _, on := range []bool{patch.ProjectIDs != nil, patch.Accept, patch.Managed != nil} {
		if on {
			set++
		}
	}
	if set != 1 {
		return Link{}, invalid("pass exactly one of a project list, accept or managed")
	}
	err := s.inTx(ctx, func(q *db.Queries) error {
		link, side, err := s.load(ctx, q, actorWS, linkID)
		if err != nil {
			return err
		}
		if patch.Managed != nil {
			return s.setManaged(ctx, q, link, side, actorWS, actorUser, actor, *patch.Managed)
		}
		if patch.Accept {
			if !Decide(OpAccept, side, actor) {
				if side == SideSource {
					return forbidden("only the linked workspace can accept this link")
				}
				return forbidden("only owners and admins can accept a link")
			}
			if link.Status != "pending" {
				return &Error{Status: 409, Message: "link is already active"}
			}
			accepted, err := q.AcceptWorkspaceLink(ctx, db.AcceptWorkspaceLinkParams{ID: link.ID, AcceptedBy: actorUser})
			if err != nil {
				return err
			}
			return audit(ctx, q, accepted, actorWS, actorUser, "accept", nil)
		}
		if !Decide(OpUpdateProjects, side, actor) {
			return forbidden("only the source workspace owner can change what a link shares")
		}
		ids, err := s.shareableProjects(ctx, q, actorWS, *patch.ProjectIDs)
		if err != nil {
			return err
		}
		if err := q.ClearWorkspaceLinkProjects(ctx, link.ID); err != nil {
			return err
		}
		if err := q.AddWorkspaceLinkProjects(ctx, db.AddWorkspaceLinkProjectsParams{LinkID: link.ID, ProjectIds: ids}); err != nil {
			return err
		}
		return audit(ctx, q, link, actorWS, actorUser, "update_projects", map[string]any{"project_ids": uuidStrings(ids)})
	})
	if err != nil {
		return Link{}, err
	}
	return s.one(ctx, actorWS, actor, linkID)
}

// Revoke deletes a link from either side. The audit trail stays.
func (s *Service) Revoke(ctx context.Context, actorWS pgtype.UUID, actorUser pgtype.UUID, actor Actor, linkID pgtype.UUID) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		link, side, err := s.load(ctx, q, actorWS, linkID)
		if err != nil {
			return err
		}
		if !Decide(OpRevoke, side, actor) {
			if side == SideSource {
				return forbidden("only the workspace owner can disconnect this link")
			}
			return forbidden("only owners and admins can disconnect this link")
		}
		if err := audit(ctx, q, link, actorWS, actorUser, "revoke", nil); err != nil {
			return err
		}
		return q.DeleteWorkspaceLink(ctx, link.ID)
	})
}

// one re-reads a single link through List so a write answers with exactly
// what the management list shows.
func (s *Service) one(ctx context.Context, actorWS pgtype.UUID, actor Actor, linkID pgtype.UUID) (Link, error) {
	links, err := s.List(ctx, actorWS, actor)
	if err != nil {
		return Link{}, err
	}
	id := util.UUIDToString(linkID)
	for _, l := range links {
		if l.ID == id {
			return l, nil
		}
	}
	return Link{}, ErrNotFound
}

// shareableProjects checks every requested project belongs to the source and
// is not private, and returns them de-duplicated.
func (s *Service) shareableProjects(ctx context.Context, q *db.Queries, sourceWS pgtype.UUID, projectIDs []pgtype.UUID) ([]pgtype.UUID, error) {
	seen := map[[16]byte]bool{}
	ids := make([]pgtype.UUID, 0, len(projectIDs))
	for _, id := range projectIDs {
		if !id.Valid || seen[id.Bytes] {
			continue
		}
		seen[id.Bytes] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, invalid("pick at least one project to share")
	}
	ok, err := q.ListShareableProjects(ctx, db.ListShareableProjectsParams{WorkspaceID: sourceWS, ProjectIds: ids})
	if err != nil {
		return nil, err
	}
	if len(ok) != len(ids) {
		return nil, invalid("every project must belong to this workspace and must not be private")
	}
	return ids, nil
}

func (s *Service) inTx(ctx context.Context, fn func(q *db.Queries) error) error {
	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(s.q.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func audit(ctx context.Context, q *db.Queries, link db.WorkspaceLink, actorWS, actorUser pgtype.UUID, action string, detail map[string]any) error {
	if detail == nil {
		detail = map[string]any{}
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("workspace link audit detail: %w", err)
	}
	return q.InsertWorkspaceLinkAudit(ctx, db.InsertWorkspaceLinkAuditParams{
		LinkID:            link.ID,
		SourceWorkspaceID: link.SourceWorkspaceID,
		TargetWorkspaceID: link.TargetWorkspaceID,
		WorkspaceID:       actorWS,
		ActorID:           actorUser,
		Action:            action,
		Detail:            raw,
	})
}

func uuidStrings(ids []pgtype.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = util.UUIDToString(id)
	}
	return out
}

func timestampPtr(t pgtype.Timestamptz) *string {
	if !t.Valid {
		return nil
	}
	s := util.TimestampToString(t)
	return &s
}
