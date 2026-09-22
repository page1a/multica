// The paths a guest may still write are a fixed list, cheaper to pin here
// than through a request. The request path is covered by
// guest_readonly_test.go.
package middleware

import (
	"net/http"
	"testing"
)

// TestGuestWritablePath pins the exception list. Every entry is a write that
// touches only the caller's own account state; anything added here that other
// people can read is a hole in the read-only tier, so the list is spelled out
// once, in full, rather than derived.
func TestGuestWritablePath(t *testing.T) {
	allowed := []string{
		"/api/me",
		"/api/me/onboarding",
		"/api/me/onboarding/complete",
		"/api/cli-token",
		"/api/feedback",
		"/api/client-usage",
		"/api/inbox/mark-all-read",
		"/api/inbox/abc-123/read",
		"/api/notification-preferences",
		"/api/workspaces",
		"/api/workspaces/11111111-1111-1111-1111-111111111111/leave",
	}
	for _, path := range allowed {
		if !GuestWritablePath(path) {
			t.Errorf("GuestWritablePath(%q) = false, want true: a guest would be locked out of their own account state", path)
		}
	}

	denied := []string{
		"/api/issues",
		"/api/issues/abc/comments",
		"/api/projects",
		"/api/attachments/abc",
		"/api/upload-file",
		"/api/workspaces/11111111-1111-1111-1111-111111111111",
		"/api/workspaces/11111111-1111-1111-1111-111111111111/members",
		// Prefix matching must respect segment boundaries: these share a
		// string prefix with an allowed path and are not below it.
		"/api/members",
		"/api/mercury",
		"/api/inboxes",
		"/api/feedback-templates",
	}
	for _, path := range denied {
		if GuestWritablePath(path) {
			t.Errorf("GuestWritablePath(%q) = true, want false: a guest must not write this", path)
		}
	}
}

func TestIsReadMethod(t *testing.T) {
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		if !isReadMethod(m) {
			t.Errorf("isReadMethod(%q) = false, want true", m)
		}
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if isReadMethod(m) {
			t.Errorf("isReadMethod(%q) = true, want false: %s is a write", m, m)
		}
	}
}
