package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/permission"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Guest is the read-only tier of DENE-695's matrix: it may view what has been
// shared with it and do nothing else. Spreading that rule across the ~250
// write routes under /api would make it a convention — true until someone
// adds route 251 — so it lives here instead, in front of every authenticated
// route, keyed on the HTTP method rather than on anyone remembering to ask.
//
// The interceptor answers one question: may this caller's tier write at all
// (permission.Role.CanWrite). Which resources a caller may see, and whether a
// Member may touch this particular one, stay with the handlers — this layer
// only closes the door that has no per-resource nuance.
//
// Fail-closed: an unrecognised role in member.role cannot write either.

// guestWritablePrefixes are the write paths a guest keeps. Every one of them
// writes only the caller's own account state — never workspace content, never
// anything another person reads. Anything not listed here is denied.
var guestWritablePrefixes = []string{
	"/api/me",                       // own profile and onboarding
	"/api/cli-token",                // own CLI token
	"/api/feedback",                 // product feedback
	"/api/client-usage",             // own client telemetry
	"/api/inbox",                    // own read / archive state
	"/api/notification-preferences", // own delivery settings
	"/api/invitations",              // accept or decline invitations addressed to the caller
	"/api/share-links/join",         // join a workspace with a share link
	"/api/tokens",                   // own personal access tokens
	"/api/lark/binding/redeem",      // bind the caller's Lark identity
	"/api/slack/binding/redeem",     // bind the caller's Slack identity
	"/api/dingtalk/binding/redeem",  // bind the caller's DingTalk identity
	"/api/wecom/binding/redeem",     // bind the caller's WeCom identity
	"/api/telegram/binding/redeem",  // bind the caller's Telegram identity
}

// pathWithin reports whether path is prefix itself or a segment below it, so
// "/api/me" does not also match "/api/members".
func pathWithin(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// GuestWritablePath reports whether a write to path is allowed for a guest
// regardless of workspace. Exported so route-coverage tests can walk the
// router and assert that every other write route is intercepted.
func GuestWritablePath(path string) bool {
	for _, prefix := range guestWritablePrefixes {
		if pathWithin(path, prefix) {
			return true
		}
	}
	// Creating a workspace makes the caller its owner; being a guest
	// somewhere else must not block that.
	if path == "/api/workspaces" {
		return true
	}
	// Leaving a workspace is the one write a guest may aim at a workspace:
	// refusing it would trap a read-only member inside it.
	if strings.HasPrefix(path, "/api/workspaces/") && strings.HasSuffix(path, "/leave") {
		return true
	}
	return false
}

func isReadMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

// GuestReadOnly rejects every write from a workspace's guests. It runs before
// the workspace middlewares, so it resolves the target workspace with the same
// helper handlers use; requests carrying no workspace context fall through to
// whatever guard the route already has.
func GuestReadOnly(queries *db.Queries) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isReadMethod(r.Method) || GuestWritablePath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			userID := r.Header.Get("X-User-ID")
			workspaceID := ResolveWorkspaceIDFromRequest(r, queries)
			if userID == "" || workspaceID == "" {
				next.ServeHTTP(w, r)
				return
			}
			userUUID, err := util.ParseUUID(userID)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			wsUUID, err := util.ParseUUID(workspaceID)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			member, err := queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
				UserID:      userUUID,
				WorkspaceID: wsUUID,
			})
			if err != nil {
				// Not a member of the workspace they named. That is the
				// route's own guard to answer, and it answers "not found"
				// rather than disclosing anything here.
				next.ServeHTTP(w, r)
				return
			}
			if !permission.Role(member.Role).CanWrite() {
				writeError(w, http.StatusForbidden, "guests have read-only access to this workspace")
				return
			}
			// The workspace middleware downstream would load the same row
			// again; hand it over so this interceptor costs no extra query.
			next.ServeHTTP(w, r.WithContext(withPrefetchedMember(r.Context(), userUUID, wsUUID, member)))
		})
	}
}

// prefetchedMember carries a member row already loaded this request, with the
// pair it was loaded for so a different workspace or user cannot reuse it.
type prefetchedMember struct {
	userID      pgtype.UUID
	workspaceID pgtype.UUID
	member      db.Member
}

func withPrefetchedMember(ctx context.Context, userID, workspaceID pgtype.UUID, member db.Member) context.Context {
	return context.WithValue(ctx, ctxKeyPrefetchedMember, prefetchedMember{
		userID:      userID,
		workspaceID: workspaceID,
		member:      member,
	})
}

// prefetchedMemberFor returns the member row GuestReadOnly already loaded for
// exactly this user and workspace, if any.
func prefetchedMemberFor(ctx context.Context, userID, workspaceID pgtype.UUID) (db.Member, bool) {
	p, ok := ctx.Value(ctxKeyPrefetchedMember).(prefetchedMember)
	if !ok {
		return db.Member{}, false
	}
	if !p.userID.Valid || !p.workspaceID.Valid {
		return db.Member{}, false
	}
	if p.userID.Bytes != userID.Bytes || p.workspaceID.Bytes != workspaceID.Bytes {
		return db.Member{}, false
	}
	return p.member, true
}
