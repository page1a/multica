package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/delivery"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestSelectDeliveryReposUsesProjectGithubReposAndFallsBackWhenEmpty(t *testing.T) {
	workspace := []workspaceRepoRef{
		{URL: "https://github.com/acme/alpha"},
		{URL: "https://github.com/beta/beta"},
	}
	ref, err := json.Marshal(map[string]string{"url": "https://github.com/beta/beta"})
	if err != nil {
		t.Fatal(err)
	}

	selected := selectDeliveryRepos(workspace, []db.ProjectResource{{ResourceType: "github_repo", ResourceRef: ref}})
	if len(selected) != 1 || selected[0].URL != workspace[1].URL {
		t.Fatalf("selected project repos = %+v, want only beta", selected)
	}

	fallback := selectDeliveryRepos(workspace, nil)
	if len(fallback) != len(workspace) {
		t.Fatalf("empty project resources = %+v, want workspace fallback", fallback)
	}
}

func TestApplyRepoReachGapUsesInstallAction(t *testing.T) {
	gap := &delivery.Gap{Kind: delivery.GapNotFound, Message: "仓库已经接上"}
	applyRepoReachGap(gap, "https://github.com/beta/beta", &RepoNextAction{
		Kind: "install_app",
		URL:  "https://github.com/apps/multica/installations/new?state=test",
	})

	if gap.Kind != delivery.GapNoConnection {
		t.Fatalf("gap kind = %q, want no_connection", gap.Kind)
	}
	if strings.Contains(gap.Message, "已接上") {
		t.Fatalf("gap message falsely claims connection: %q", gap.Message)
	}
	if !strings.Contains(gap.NextCommand, "安装 GitHub App") || !strings.Contains(gap.NextCommand, "installations/new") {
		t.Fatalf("gap next command = %q, want install action and link", gap.NextCommand)
	}
}

func TestFinishDeliveriesAppOnlyMessageRequiresAllQueriedReposToBeApp(t *testing.T) {
	appOnly := finishDeliveries("DENE-1011", nil, nil, false, true, true, "")
	if appOnly.Gap == nil || !strings.Contains(appOnly.Gap.Message, "GitHub App 已接上") {
		t.Fatalf("app-only gap = %+v, want the app-connected message", appOnly.Gap)
	}

	mixed := finishDeliveries("DENE-1011", nil, nil, false, true, false, "")
	applyRepoReachGap(mixed.Gap, "https://github.com/beta/beta", &RepoNextAction{Kind: "install_app"})
	if mixed.Gap == nil || strings.Contains(mixed.Gap.Message, "GitHub App 已接上") {
		t.Fatalf("mixed-reach gap = %+v, must not claim every repo is app-connected", mixed.Gap)
	}
}
