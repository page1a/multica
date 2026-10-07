package routing

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// DENE-1451: the issue's domain decides the seat, not the project name.

func TestIssueSceneReadsTheIssueDomainFirst(t *testing.T) {
	l := DefaultLadder
	cases := []struct {
		name  string
		issue Issue
		want  Scene
	}{
		{"issue domain", Issue{ProjectName: "tarot", ProjectDomains: []string{"出海", "自媒体"}, Domain: "自媒体"},
			Scene{Domains: []string{"自媒体"}, Source: DirectionFromIssue}},
		{"single project domain", Issue{ProjectName: "relay", ProjectDomains: []string{"中转"}},
			Scene{Domains: []string{"中转"}, Source: DirectionFromProject}},
		{"several project domains, none picked", Issue{ProjectName: "tarot", ProjectDomains: []string{"出海", "自媒体"}},
			Scene{Domains: []string{"出海", "自媒体"}, Source: DirectionFromProject}},
		{"no domains falls back to the prefix row", Issue{ProjectName: "game-new"},
			Scene{Domains: []string{"游戏"}, Source: DirectionFromTable}},
		{"no domains, no row", Issue{ProjectName: "elsewhere"}, GenericScene},
	}
	for _, tc := range cases {
		if got := l.IssueScene(tc.issue); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestWorkspaceDomainsBecomeTheDirections(t *testing.T) {
	l := DefaultLadder.For(Settings{Domains: []string{"游戏", "出海", "自媒体", "中转", "学术"}})
	roster := map[string]Agent{
		"孙悟空":   {ID: "g", Name: "孙悟空", Tier: "strong"},
		"孙悟空中转": {ID: "gr", Name: "孙悟空中转", Tier: "strong"},
	}
	seats := l.Candidates("中转", roster)
	if len(seats) != 1 || seats[0].Name != "孙悟空中转" || seats[0].Direction != "中转" {
		t.Fatalf("中转 seat not picked: %+v", seats)
	}
}

func TestRecordedDomainBeatsTheNameSuffix(t *testing.T) {
	// A specialisation renamed away from base + domain still serves its
	// recorded domain, tagged or not.
	l := DefaultLadder
	tagged := map[string]Agent{
		"孙悟空":    {ID: "g", Name: "孙悟空", Tier: "strong"},
		"悟空-海外版": {ID: "go", Name: "悟空-海外版", Tier: "strong", Direction: "出海", Base: "孙悟空"},
	}
	if seats := l.Candidates("出海", tagged); len(seats) != 1 || seats[0].ID != "go" {
		t.Fatalf("tagged: recorded domain ignored: %+v", seats)
	}
	untagged := map[string]Agent{
		"孙悟空":    {ID: "g", Name: "孙悟空"},
		"悟空-海外版": {ID: "go", Name: "悟空-海外版", Direction: "出海", Base: "孙悟空"},
	}
	seats := l.Candidates("出海", untagged)
	if len(seats) != 1 || seats[0].ID != "go" || seats[0].Direction != "出海" {
		t.Fatalf("untagged: recorded domain ignored: %+v", seats)
	}
}

func TestMultiDomainProjectRoutesAnIssueByItsOwnDomain(t *testing.T) {
	store := newFakeStore()
	store.issue.ProjectName = "tarot"
	store.issue.ProjectDomains = []string{"出海", "自媒体"}
	store.issue.Domain = "自媒体"
	store.settings.Domains = []string{"游戏", "出海", "自媒体", "中转", "学术"}
	store.roster["孙悟空自媒体"] = Agent{ID: "a-goku-m", Name: "孙悟空自媒体", Direction: "自媒体", Base: "孙悟空"}

	if _, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.assigns) != 1 || store.assigns[0] != "孙悟空自媒体" {
		t.Errorf("assigns = %v, want [孙悟空自媒体]", store.assigns)
	}
	if body := store.comments[KindAssignment][0]; !strings.Contains(body, "自媒体（任务的领域）") {
		t.Errorf("comment does not say the direction came from the issue:\n%s", body)
	}
}

func TestMultiDomainProjectWithoutAnIssueDomainFitsEitherDomain(t *testing.T) {
	// Rule 1 of DENE-1477: the project's domains, all of them, are the scene.
	store := newFakeStore()
	store.issue.ProjectName = "tarot"
	store.issue.ProjectDomains = []string{"出海", "游戏"}

	if _, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.assigns) != 1 || store.assigns[0] != "孙悟空游戏" {
		t.Errorf("assigns = %v, want the 游戏 specialisation", store.assigns)
	}
	if body := store.comments[KindAssignment][0]; !strings.Contains(body, "出海、游戏（项目「tarot」的领域）") {
		t.Errorf("comment does not name the project's domains:\n%s", body)
	}
}

func TestOverseasProjectRoutesToTheOverseasSpecialisation(t *testing.T) {
	// The tarot acceptance case: a project in 出海 lands on *出海.
	store := newFakeStore()
	store.issue.ProjectName = "online-tarot"
	store.issue.ProjectDomains = []string{"出海"}
	store.roster["孙悟空出海"] = Agent{ID: "a-goku-o", Name: "孙悟空出海", Direction: "出海", Base: "孙悟空"}

	if _, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.assigns) != 1 || store.assigns[0] != "孙悟空出海" {
		t.Errorf("assigns = %v, want [孙悟空出海]", store.assigns)
	}
}

func TestProjectWithoutDomainsSaysGeneric(t *testing.T) {
	// The Multica 魔改 acceptance case: no domain is 通用, never 未知.
	store := newFakeStore()
	store.issue.ProjectName = "Multica 魔改"

	if _, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.assigns) != 1 || store.assigns[0] != "孙悟空" {
		t.Errorf("assigns = %v, want the base role", store.assigns)
	}
	body := store.comments[KindAssignment][0]
	if strings.Contains(body, "未知") || !strings.Contains(body, "- **方向**：通用\n") {
		t.Errorf("comment must say 通用:\n%s", body)
	}
}
