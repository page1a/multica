package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func newRepoRegistryTestCmd(serverURL string) *cobra.Command {
	cmd := &cobra.Command{Use: "repo-test"}
	cmd.Flags().String("server-url", "", "")
	cmd.Flags().String("workspace-id", "", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().StringArray("url", nil, "")
	cmd.Flags().String("description", "", "")
	cmd.Flags().String("output", "json", "")
	_ = cmd.Flags().Set("server-url", serverURL)
	_ = cmd.Flags().Set("workspace-id", "ws-1")
	return cmd
}

func TestRunRepoAddAppendsAndDedupes(t *testing.T) {
	initialRepos := []workspaceRepo{{URL: "https://git.example.com/web.git"}}
	var patched []workspaceRepo
	patchCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/workspaces/ws-1":
			json.NewEncoder(w).Encode(repoWorkspaceResponse{ID: "ws-1", Repos: initialRepos})
		case r.Method == http.MethodPatch && r.URL.Path == "/api/workspaces/ws-1":
			patchCount++
			var body struct {
				Repos []workspaceRepo `json:"repos"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode patch body: %v", err)
			}
			patched = body.Repos
			json.NewEncoder(w).Encode(repoWorkspaceResponse{ID: "ws-1", Repos: body.Repos})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cmd := newRepoRegistryTestCmd(srv.URL)
	if err := cmd.Flags().Set("url", "https://git.example.com/web.git"); err != nil {
		t.Fatal(err)
	}
	err := runRepoAdd(cmd, []string{
		"https://git.example.com/api.git",
		"https://git.example.com/api.git",
	})
	if err != nil {
		t.Fatalf("runRepoAdd: %v", err)
	}
	if patchCount != 1 {
		t.Fatalf("patchCount = %d, want 1", patchCount)
	}
	if len(patched) != 2 {
		t.Fatalf("patched repos = %+v, want 2 entries", patched)
	}
	if patched[0].URL != "https://git.example.com/web.git" || patched[1].URL != "https://git.example.com/api.git" {
		t.Fatalf("unexpected patched repos: %+v", patched)
	}
}

func TestRunRepoAddUpdatesDescriptionForExistingRepo(t *testing.T) {
	initialRepos := []workspaceRepo{{URL: "https://git.example.com/web.git", Description: "old"}}
	var patched []workspaceRepo

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/workspaces/ws-1":
			json.NewEncoder(w).Encode(repoWorkspaceResponse{ID: "ws-1", Repos: initialRepos})
		case r.Method == http.MethodPatch && r.URL.Path == "/api/workspaces/ws-1":
			var body struct {
				Repos []workspaceRepo `json:"repos"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode patch body: %v", err)
			}
			patched = body.Repos
			json.NewEncoder(w).Encode(repoWorkspaceResponse{ID: "ws-1", Repos: body.Repos})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cmd := newRepoRegistryTestCmd(srv.URL)
	if err := cmd.Flags().Set("description", "new"); err != nil {
		t.Fatal(err)
	}
	if err := runRepoAdd(cmd, []string{"https://git.example.com/web.git"}); err != nil {
		t.Fatalf("runRepoAdd: %v", err)
	}
	if len(patched) != 1 || patched[0].Description != "new" {
		t.Fatalf("patched repos = %+v, want updated description", patched)
	}
}

func TestRunRepoAddRejectsDescriptionForMultipleRepos(t *testing.T) {
	cmd := newRepoRegistryTestCmd("http://127.0.0.1:0")
	if err := cmd.Flags().Set("description", "shared"); err != nil {
		t.Fatal(err)
	}
	err := runRepoAdd(cmd, []string{"https://git.example.com/a.git", "https://git.example.com/b.git"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "--description") {
		t.Fatalf("error = %q, want description guidance", err)
	}
}

func TestRunRepoRemoveDeletesExistingRepos(t *testing.T) {
	initialRepos := []workspaceRepo{
		{URL: "https://git.example.com/web.git"},
		{URL: "https://git.example.com/api.git"},
		{URL: "https://git.example.com/mobile.git"},
	}
	var patched []workspaceRepo

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/workspaces/ws-1":
			json.NewEncoder(w).Encode(repoWorkspaceResponse{ID: "ws-1", Repos: initialRepos})
		case r.Method == http.MethodPatch && r.URL.Path == "/api/workspaces/ws-1":
			var body struct {
				Repos []workspaceRepo `json:"repos"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode patch body: %v", err)
			}
			patched = body.Repos
			json.NewEncoder(w).Encode(repoWorkspaceResponse{ID: "ws-1", Repos: body.Repos})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cmd := newRepoRegistryTestCmd(srv.URL)
	if err := cmd.Flags().Set("url", "https://git.example.com/mobile.git"); err != nil {
		t.Fatal(err)
	}
	if err := runRepoRemove(cmd, []string{"https://git.example.com/web.git"}); err != nil {
		t.Fatalf("runRepoRemove: %v", err)
	}
	if len(patched) != 1 || patched[0].URL != "https://git.example.com/api.git" {
		t.Fatalf("patched repos = %+v, want only api repo", patched)
	}
}

func TestRunRepoRemoveRejectsMissingRepoWithoutPatch(t *testing.T) {
	patchCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/workspaces/ws-1":
			json.NewEncoder(w).Encode(repoWorkspaceResponse{
				ID:    "ws-1",
				Repos: []workspaceRepo{{URL: "https://git.example.com/web.git"}},
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/api/workspaces/ws-1":
			patchCount++
			json.NewEncoder(w).Encode(repoWorkspaceResponse{ID: "ws-1"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cmd := newRepoRegistryTestCmd(srv.URL)
	err := runRepoRemove(cmd, []string{"https://git.example.com/missing.git"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %q, want not found", err)
	}
	if patchCount != 0 {
		t.Fatalf("patchCount = %d, want 0", patchCount)
	}
}

func TestRunRepoCheckoutForwardsManagedCheckoutMode(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repo/checkout" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode checkout body: %v", err)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer mat_repo_checkout_test" {
			t.Fatalf("Authorization = %q, want task-scoped bearer", got)
		}
		json.NewEncoder(w).Encode(map[string]string{
			"path":        "/work/repo",
			"branch_name": "agent/test/task",
		})
	}))
	defer srv.Close()

	t.Setenv("MULTICA_DAEMON_PORT", strings.TrimPrefix(srv.URL, "http://127.0.0.1:"))
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_AGENT_NAME", "Test Agent")
	t.Setenv("MULTICA_TASK_ID", "task-1")
	t.Setenv("MULTICA_TOKEN", "mat_repo_checkout_test")
	t.Setenv("MULTICA_REPO_CHECKOUT_MODE", "isolated")

	previousRef, previousFresh := repoCheckoutRef, repoCheckoutFresh
	repoCheckoutRef, repoCheckoutFresh = "release/v2", true
	defer func() { repoCheckoutRef, repoCheckoutFresh = previousRef, previousFresh }()

	if err := runRepoCheckout(&cobra.Command{}, []string{"https://github.com/org/repo.git"}); err != nil {
		t.Fatalf("runRepoCheckout: %v", err)
	}
	if got := body["checkout_mode"]; got != "isolated" {
		t.Fatalf("checkout_mode = %q, want isolated", got)
	}
	if got := body["ref"]; got != "release/v2" {
		t.Fatalf("ref = %q, want release/v2", got)
	}
	if got := body["retry_busy"]; got != true {
		t.Fatalf("retry_busy = %v, want true", got)
	}
	if got := body["fresh"]; got != true {
		t.Fatalf("fresh = %v, want true", got)
	}
}

func TestRepoCheckoutSummary(t *testing.T) {
	t.Parallel()
	const repoURL = "https://github.com/org/repo.git"
	for _, tc := range []struct {
		name   string
		result repoCheckoutResult
		want   []string
		absent []string
	}{
		{
			name:   "new branch",
			result: repoCheckoutResult{Path: "/work/repo", BranchName: "agent/test/task"},
			want:   []string{"Checked out " + repoURL + " → /work/repo (branch: agent/test/task)"},
		},
		{
			name:   "kept for local work",
			result: repoCheckoutResult{Path: "/work/repo", BranchName: "agent/test/old", Kept: "local_work", UncommittedFiles: 2, UnpushedCommits: 1},
			want: []string{
				"Kept the existing checkout of " + repoURL + " at /work/repo (branch: agent/test/old; 2 uncommitted files, 1 unpushed commit)",
				"nothing was reset, cleaned, or switched",
				"re-run with --fresh",
			},
		},
		{
			name:   "kept on the task branch",
			result: repoCheckoutResult{Path: "/work/repo", BranchName: "agent/test/task", Kept: "task_branch"},
			want:   []string{"(branch: agent/test/task, this task's branch; 0 uncommitted files, 0 unpushed commits)"},
		},
		{
			name:   "kept on a detached HEAD",
			result: repoCheckoutResult{Path: "/work/repo", Kept: "local_work", UnpushedCommits: 3},
			want:   []string{"(branch: detached HEAD; 0 uncommitted files, 3 unpushed commits)"},
		},
		{
			name:   "kept sparse checkout restored to the whole tree",
			result: repoCheckoutResult{Path: "/work/repo", BranchName: "agent/test/task", Kept: "task_branch", SparseSkipped: "restored"},
			want: []string{
				"sparse checkout was turned off so the whole repository is on disk again",
				"every file in the repository is on disk now",
			},
			absent: []string{"only remote refs were fetched"},
		},
		{
			name:   "kept sparse checkout widened",
			result: repoCheckoutResult{Path: "/work/repo", BranchName: "agent/test/task", Kept: "task_branch", SparsePaths: "apps/web,server", SparseSkipped: "widened"},
			want: []string{
				"the sparse checkout was widened",
				"Sparse checkout now includes apps/web,server",
			},
			absent: []string{"only remote refs were fetched"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := repoCheckoutSummary(repoURL, tc.result)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("summary missing %q:\n%s", want, got)
				}
			}
			for _, phrase := range tc.absent {
				if strings.Contains(got, phrase) {
					t.Fatalf("summary still says %q:\n%s", phrase, got)
				}
			}
			if kept := strings.HasPrefix(got, "Kept "); kept != (tc.result.Kept != "") {
				t.Fatalf("summary reads kept=%v for Kept=%q:\n%s", kept, tc.result.Kept, got)
			}
		})
	}
}

func TestRunRepoCheckoutRequiresTaskCredential(t *testing.T) {
	t.Setenv("MULTICA_DAEMON_PORT", "12345")
	t.Setenv("MULTICA_TOKEN", "")

	err := runRepoCheckout(&cobra.Command{}, []string{"https://github.com/org/repo.git"})
	if err == nil || !strings.Contains(err.Error(), "MULTICA_TOKEN not set") {
		t.Fatalf("runRepoCheckout error = %v, want missing task credential", err)
	}
}

func TestRunRepoCheckoutRetriesServiceUnavailable(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("X-Multica-Retryable", "repo-busy")
			w.Header().Set("Retry-After", "0")
			http.Error(w, "repository busy", http.StatusServiceUnavailable)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{
			"path":        "/work/repo",
			"branch_name": "agent/test/task",
		})
	}))
	defer srv.Close()

	t.Setenv("MULTICA_DAEMON_PORT", strings.TrimPrefix(srv.URL, "http://127.0.0.1:"))
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_AGENT_NAME", "Test Agent")
	t.Setenv("MULTICA_TASK_ID", "task-1")
	t.Setenv("MULTICA_TOKEN", "mat_repo_checkout_test")

	if err := runRepoCheckout(&cobra.Command{}, []string{"https://github.com/org/repo.git"}); err != nil {
		t.Fatalf("runRepoCheckout: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("checkout attempts = %d, want 2", attempts)
	}
}

func TestRunRepoCheckoutDoesNotRetryUnmarkedServiceUnavailable(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, "daemon unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	t.Setenv("MULTICA_DAEMON_PORT", strings.TrimPrefix(srv.URL, "http://127.0.0.1:"))
	t.Setenv("MULTICA_TOKEN", "mat_repo_checkout_test")
	if err := runRepoCheckout(&cobra.Command{}, []string{"https://github.com/org/repo.git"}); err == nil {
		t.Fatal("runRepoCheckout unexpectedly succeeded")
	}
	if attempts != 1 {
		t.Fatalf("checkout attempts = %d, want 1", attempts)
	}
}

func TestRepoCheckoutRetryDelay(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	if got := repoCheckoutRetryDelay("7", now); got != 7*time.Second {
		t.Fatalf("seconds delay = %s, want 7s", got)
	}
	if got := repoCheckoutRetryDelay(now.Add(time.Minute).Format(http.TimeFormat), now); got != 30*time.Second {
		t.Fatalf("capped date delay = %s, want 30s", got)
	}
	if got := repoCheckoutRetryDelay("invalid", now); got != time.Second {
		t.Fatalf("default delay = %s, want 1s", got)
	}
}

// A local-directory answer must not read like a fresh clone. An agent told
// "Checked out X → /path" reasonably assumes a disposable tree and may reset
// or clean it — in the user's own working copy (DENE-595).
func TestRepoCheckoutSummaryForLocalDirectory(t *testing.T) {
	got := repoCheckoutSummary("https://github.com/jeff-kunkun/multica", repoCheckoutResult{
		Path:       "/Users/kunkun/.agents/multica",
		BranchName: "kun",
		Source:     repoCheckoutSourceLocalDirectory,
	})
	for _, want := range []string{
		"local directory",
		"/Users/kunkun/.agents/multica",
		"kun",
		"was NOT cloned",
		"nothing here was reset",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Checked out") {
		t.Errorf("a local directory must not be reported as a checkout:\n%s", got)
	}
}

// A daemon too old to send `source` still returns a usable path, and the
// generic line it produces stays true.
func TestRepoCheckoutSummaryWithoutSourceIsUnchanged(t *testing.T) {
	got := repoCheckoutSummary("https://github.com/org/repo", repoCheckoutResult{
		Path:       "/work/repo",
		BranchName: "agent/test/task",
	})
	if !strings.Contains(got, "Checked out https://github.com/org/repo → /work/repo") {
		t.Errorf("old-daemon summary changed:\n%s", got)
	}
}

// DENE-598: when the wait on a first-time download runs out, the error is the
// daemon's own progress report, not a bare "deadline exceeded".
func TestRunRepoCheckoutTimeoutReportsWhatItWaitedOn(t *testing.T) {
	const progress = "the first-time cache of https://github.com/org/repo.git is not finished yet: downloading files, step 3 of 3 (40 of 100, about 2m0s left)"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Multica-Retryable", "repo-busy")
		w.Header().Set("X-Multica-Repo-Building", "1")
		w.Header().Set("Retry-After", "0")
		http.Error(w, progress, http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	t.Setenv("MULTICA_DAEMON_PORT", strings.TrimPrefix(srv.URL, "http://127.0.0.1:"))
	t.Setenv("MULTICA_TOKEN", "mat_repo_checkout_test")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	err := runRepoCheckout(cmd, []string{"https://github.com/org/repo.git"})
	if err == nil {
		t.Fatal("runRepoCheckout unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "40 of 100") {
		t.Fatalf("timeout error = %q, want it to carry the download progress", err)
	}
}

func TestRepoCheckoutStaleWarning(t *testing.T) {
	const repoURL = "https://github.com/org/repo.git"
	if got := repoCheckoutStaleWarning(repoURL, repoCheckoutResult{Path: "/work/repo"}); got != "" {
		t.Fatalf("a fresh checkout produced a warning: %q", got)
	}
	got := repoCheckoutStaleWarning(repoURL, repoCheckoutResult{Path: "/work/repo", Stale: true, StaleReason: "git fetch: timed out"})
	for _, want := range []string{"WARNING", "OUT OF DATE", "/work/repo", "git fetch: timed out", "git fetch origin"} {
		if !strings.Contains(got, want) {
			t.Errorf("stale warning %q does not mention %q", got, want)
		}
	}
	// A daemon that flags staleness without a reason still gets a warning.
	if got := repoCheckoutStaleWarning(repoURL, repoCheckoutResult{Path: "/work/repo", Stale: true}); !strings.Contains(got, "OUT OF DATE") {
		t.Errorf("reasonless stale warning = %q", got)
	}
}
