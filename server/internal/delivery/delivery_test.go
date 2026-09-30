package delivery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGapFor(t *testing.T) {
	merged := Pull{Provider: "github", State: "merged", URL: "https://github.com/o/r/pull/1"}
	if GapFor(Situation{Identifier: "DENE-1", Pulls: []Pull{merged}, Connected: true}) != nil {
		t.Fatal("a merged pull is a delivery")
	}
	open := Pull{Provider: "github", State: "open", URL: "https://github.com/o/r/pull/2"}
	gap := GapFor(Situation{Identifier: "DENE-1", Pulls: []Pull{open}, Queried: true, Connected: true})
	if gap == nil || gap.Kind != GapNotMerged || !strings.Contains(gap.NextCommand, "gh pr merge --squash") {
		t.Fatalf("open github = %#v", gap)
	}
	gl := Pull{Provider: "gitlab", State: "open", URL: "https://gitlab.com/g/r/-/merge_requests/3"}
	gap = GapFor(Situation{Identifier: "DENE-1", Pulls: []Pull{gl}, Connected: true})
	if gap == nil || !strings.Contains(gap.NextCommand, "glab mr merge") {
		t.Fatalf("open gitlab = %#v", gap)
	}
	gap = GapFor(Situation{Identifier: "DENE-1"})
	if gap == nil || gap.Kind != GapNoConnection || !strings.Contains(gap.NextCommand, "--pr") {
		t.Fatalf("no connection = %#v", gap)
	}
	gap = GapFor(Situation{Identifier: "DENE-1", Connected: true, Queried: true})
	if gap == nil || gap.Kind != GapNotFound || !strings.Contains(gap.Message, "DENE-1") {
		t.Fatalf("not found = %#v", gap)
	}
}

func TestParsePullURL(t *testing.T) {
	gh, err := ParsePullURL("https://github.com/jeff-kunkun/multica/pull/392")
	if err != nil || gh.Provider != "github" || gh.Owner != "jeff-kunkun" || gh.Repo != "multica" || gh.Number != 392 {
		t.Fatalf("github = %#v %v", gh, err)
	}
	gl, err := ParsePullURL("https://gitlab.example.com/group/sub/repo/-/merge_requests/12")
	if err != nil || gl.Provider != "gitlab" || gl.Owner != "group/sub" || gl.Repo != "repo" || gl.Number != 12 {
		t.Fatalf("gitlab = %#v %v", gl, err)
	}
	if gl.ProjectPath() != "group/sub/repo" {
		t.Fatalf("project path = %s", gl.ProjectPath())
	}
	fj, err := ParsePullURL("https://git.example.com/acme/widget/pulls/3")
	if err != nil || fj.Provider != "forgejo" || fj.Number != 3 || fj.Owner != "acme" || fj.Repo != "widget" {
		t.Fatalf("forgejo = %#v %v", fj, err)
	}
	if _, err := ParsePullURL("https://github.com/o/r"); err == nil {
		t.Fatal("a repository home page is not a pull url")
	}
}

func TestSearchGitHub(t *testing.T) {
	var sawStatus bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		switch {
		case strings.Contains(r.URL.Path, "/search/issues"):
			if !strings.Contains(r.URL.Query().Get("q"), "DENE-908") || !strings.Contains(r.URL.Query().Get("q"), "in:title") {
				t.Errorf("query = %s", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items":[{"number":392,"pull_request":{}}]}`))
		case strings.Contains(r.URL.Path, "/pulls/392"):
			_, _ = w.Write([]byte(`{"number":392,"title":"DENE-908 fix","state":"open","html_url":"https://github.com/o/r/pull/392","mergeable_state":"clean","draft":false,"user":{"login":"kun"},"head":{"ref":"agent/dene-908","sha":"abc"}}`))
		case strings.Contains(r.URL.Path, "/commits/abc/status"):
			sawStatus = true
			_, _ = w.Write([]byte(`{"state":"success"}`))
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	pulls, err := Search(context.Background(), srv.Client(), "github", srv.URL, "tok", "o", "r", "DENE-908")
	if err != nil {
		t.Fatal(err)
	}
	if len(pulls) != 1 || pulls[0].Number != 392 || pulls[0].Mergeable != "clean" || pulls[0].Checks != "success" || pulls[0].State != "open" {
		t.Fatalf("pulls = %#v", pulls)
	}
	if !sawStatus {
		t.Fatal("did not read commit status")
	}
}

func TestSearchGitLabAndMerge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "tok" {
			t.Errorf("token header = %q", r.Header.Get("PRIVATE-TOKEN"))
		}
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/merge_requests"):
			_, _ = w.Write([]byte(`[{"iid":7,"title":"DENE-1 ship","state":"opened","web_url":"https://gitlab.example/g/r/-/merge_requests/7","source_branch":"agent/dene-1","sha":"def","detailed_merge_status":"mergeable","head_pipeline":{"status":"success"},"author":{"username":"kun"}}]`))
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/merge"):
			_, _ = w.Write([]byte(`{"state":"merged"}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	pulls, err := Search(context.Background(), srv.Client(), "gitlab", srv.URL, "tok", "g", "r", "DENE-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(pulls) != 1 || pulls[0].Mergeable != "clean" || pulls[0].Checks != "success" || pulls[0].State != "open" {
		t.Fatalf("pulls = %#v", pulls)
	}
	ref := Ref{Provider: "gitlab", Owner: "g", Repo: "r", Number: 7}
	if err := Merge(context.Background(), srv.Client(), "gitlab", srv.URL, "tok", ref); err != nil {
		t.Fatal(err)
	}
}

func TestFetchUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := Fetch(context.Background(), srv.Client(), "github", srv.URL, "tok", Ref{Owner: "o", Repo: "r", Number: 1})
	if !Unauthorized(err) {
		t.Fatalf("err = %v", err)
	}
}
