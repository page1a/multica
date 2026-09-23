package main

import (
	"net/http"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/realtime"
)

// TestGuestRouteCoverage keeps the account-level exceptions auditable. Any
// write route in the authenticated tree that is not behind workspace-member
// middleware must be deliberately classified as a guest allow or deny. This
// catches a newly added user-scoped write that would otherwise silently become
// guest-writable (or accidentally lock out an account operation).
func TestGuestRouteCoverage(t *testing.T) {
	router, _ := NewRouterWithOptions(testPool, realtime.NewHub(), events.New(), analytics.NoopClient{}, nil, RouterOptions{})

	// These authenticated writes are intentionally still denied to a guest.
	// Workspace-scoped routes are covered by the final prefix because their
	// resource authorization remains the handler/middleware's concern.
	blocked := []string{
		"/api/auth/refresh",
		"/api/upload-file",
		"/api/composio/", // defensive: retained for old route aliases
		"/api/integrations/composio/",
		"/api/cloud-billing/",
		"/api/cloud-subscriptions/",
		"/api/workspaces/",
	}

	isNamed := func(fn func(http.Handler) http.Handler, name string) bool {
		if fn == nil {
			return false
		}
		pc := reflect.ValueOf(fn).Pointer()
		if pc == 0 {
			return false
		}
		return strings.Contains(runtime.FuncForPC(pc).Name(), name)
	}

	writes := map[string]bool{
		http.MethodPost:   true,
		http.MethodPut:    true,
		http.MethodPatch:  true,
		http.MethodDelete: true,
	}
	if err := chi.Walk(router, func(method, route string, _ http.Handler, mws ...func(http.Handler) http.Handler) error {
		if !writes[method] {
			return nil
		}
		hasGuestGuard := false
		hasWorkspaceMember := false
		for _, mw := range mws {
			hasGuestGuard = hasGuestGuard || isNamed(mw, "GuestReadOnly")
			hasWorkspaceMember = hasWorkspaceMember || isNamed(mw, "RequireWorkspaceMember")
		}
		if !hasGuestGuard || hasWorkspaceMember {
			return nil
		}
		if middleware.GuestWritablePath(route) {
			return nil
		}
		for _, prefix := range blocked {
			if strings.HasPrefix(route, prefix) {
				return nil
			}
		}
		t.Errorf("write route %s %s is missing an explicit guest allow/deny classification", method, route)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
