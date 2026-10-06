package workspacelink

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ErrNoTarget is the one answer for an address that names no workspace, so
// the lookup cannot be used to tell a typo from anything else.
var ErrNoTarget = &Error{Status: 404, Message: "no workspace at that address"}

// TargetSlug reads the workspace slug out of what a person pasted into the
// "their workspace" field: a bare slug, a full workspace link
// (https://host/<slug>/issues/...), a host-less path (host/<slug>, /<slug>/x)
// or the slug with stray spaces or slashes. Web, Desktop and the CLI all send
// the raw text and this is the only place it is interpreted.
func TargetSlug(input string) string {
	s := strings.TrimSpace(input)
	if s == "" {
		return ""
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return ""
		}
		s = u.Path
	} else {
		// Drop a query or fragment first so "slug?x" still reads as slug.
		if i := strings.IndexAny(s, "?#"); i >= 0 {
			s = s[:i]
		}
		// "host.tld/slug/..." — a first segment with a dot or a port is a
		// host, never a slug (slugs are [a-z0-9-]).
		if first, rest, ok := strings.Cut(s, "/"); ok && strings.ContainsAny(first, ".:") {
			s = rest
		}
	}
	for _, seg := range strings.Split(s, "/") {
		if seg = strings.TrimSpace(seg); seg != "" {
			if dec, err := url.PathUnescape(seg); err == nil {
				seg = dec
			}
			return strings.ToLower(seg)
		}
	}
	return ""
}

// Lookup resolves an address to the workspace a link from actorWS would
// target, exactly as Create would, so the form can show who it is before
// anything is sent. Only an exact slug matches; there is no search, which
// would hand out the deployment's workspace list.
func (s *Service) Lookup(ctx context.Context, actorWS pgtype.UUID, actor Actor, input string) (WorkspaceRef, error) {
	if !Decide(OpCreate, SideSource, actor) {
		return WorkspaceRef{}, forbidden("only the workspace owner can link data out of the workspace")
	}
	target, err := s.target(ctx, s.q, actorWS, input)
	if err != nil {
		return WorkspaceRef{}, err
	}
	return WorkspaceRef{Name: target.Name, Slug: target.Slug, AvatarURL: util.TextToPtr(target.AvatarUrl)}, nil
}

// target is the one reading of an address shared by Lookup and Create.
func (s *Service) target(ctx context.Context, q *db.Queries, actorWS pgtype.UUID, input string) (db.Workspace, error) {
	slug := TargetSlug(input)
	if slug == "" {
		return db.Workspace{}, invalid("target workspace address is required")
	}
	target, err := q.GetWorkspaceBySlug(ctx, slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Workspace{}, ErrNoTarget
	}
	if err != nil {
		return db.Workspace{}, err
	}
	if target.ID == actorWS {
		return db.Workspace{}, invalid("a workspace cannot link to itself")
	}
	return target, nil
}
