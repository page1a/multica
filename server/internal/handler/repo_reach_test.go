package handler

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/gitconn"
)

func TestDecideRepoReach(t *testing.T) {
	cases := []struct {
		name                       string
		in                         reachFacts
		mode, state, kind, forKind string
		optional                   bool
	}{
		{
			name: "token wins over app and cli",
			in:   reachFacts{Provider: "github", HasToken: true, AppCovers: true, HasCLI: true, AppReady: true, CanConfigure: true},
			mode: "token", state: "connected",
		},
		{
			name: "broken token asks to replace it",
			in:   reachFacts{Provider: "github", HasToken: true, TokenBroken: true, CanConfigure: true, Command: "multica connection add --from-gh"},
			mode: "token", state: "connected", kind: "replace_token",
		},
		{
			name: "app covers this owner",
			in:   reachFacts{Provider: "github", AppCovers: true, AppReady: true, CanConfigure: true},
			mode: "app", state: "connected",
		},
		{
			name: "cli fallback suggests a token",
			in:   reachFacts{Provider: "github", HasCLI: true, AppReady: true, CanConfigure: true},
			mode: "cli", state: "cli", kind: "add_token", optional: true,
		},
		{
			name: "installed account does not cover another",
			in:   reachFacts{Provider: "github", AppReady: true, CanConfigure: true, Owner: "beta", InstallURL: "https://github.com/apps/multica/installations/new"},
			mode: "none", state: "pending_install", kind: "install_app",
		},
		{
			name: "no github app yet",
			in:   reachFacts{Provider: "github", CanConfigure: true, CreateURL: "/acme/settings?tab=git-connections"},
			mode: "none", state: "disconnected", kind: "create_app",
		},
		{
			name: "gitlab needs a token",
			in:   reachFacts{Provider: "gitlab", CanConfigure: true, Command: "multica connection add --from-glab"},
			mode: "none", state: "disconnected", kind: "add_token",
		},
		{
			name: "caller without permission is sent to an owner",
			in:   reachFacts{Provider: "github", AppReady: true, Owner: "beta"},
			mode: "none", state: "pending_install", kind: "ask_owner", forKind: "install_app",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideRepoReach(tc.in)
			if got.Mode != tc.mode || got.State != tc.state {
				t.Fatalf("mode/state = %s/%s, want %s/%s", got.Mode, got.State, tc.mode, tc.state)
			}
			if got.Hint == "" {
				t.Fatal("hint is empty")
			}
			if tc.kind == "" {
				if got.NextAction != nil {
					t.Fatalf("next_action = %#v, want nil", got.NextAction)
				}
				return
			}
			if got.NextAction == nil || got.NextAction.Kind != tc.kind || got.NextAction.For != tc.forKind || got.NextAction.Optional != tc.optional {
				t.Fatalf("next_action = %#v", got.NextAction)
			}
		})
	}
}

func TestRepoReachDoesNotLetOneInstallationCoverAnotherAccount(t *testing.T) {
	const acme = "github.com/acme/docs"
	const beta = "github.com/beta/docs"
	if !gitconn.AppCovers("acme", acme) {
		t.Fatal("installation on acme should cover acme/docs")
	}
	if gitconn.AppCovers("acme", beta) {
		t.Fatal("installation on acme must not cover beta/docs")
	}
	covered := DecideRepoReach(reachFacts{
		Provider: "github", AppCovers: gitconn.AppCovers("acme", acme), AppReady: true, CanConfigure: true,
	})
	other := DecideRepoReach(reachFacts{
		Provider: "github", AppCovers: gitconn.AppCovers("acme", beta), AppReady: true, CanConfigure: true, Owner: "beta",
	})
	if covered.Mode != "app" || covered.NextAction != nil {
		t.Fatalf("covered reach = %#v", covered)
	}
	if other.Mode != "none" || other.NextAction == nil || other.NextAction.Kind != "install_app" {
		t.Fatalf("other account reach = %#v", other)
	}
}
