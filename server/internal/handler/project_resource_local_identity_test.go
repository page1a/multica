package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/coderesolve"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// A local_directory's identity fields and the rules they enable (DENE-617).
// The uniqueness rules themselves are also Postgres partial unique indexes
// (migrations 498/499); these cover the application's copy, which exists to
// turn a violation into a sentence naming the directory.

func localRef(t *testing.T, fields map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func normalizeLocalRef(t *testing.T, fields map[string]any) localDirectoryRef {
	t.Helper()
	out, err := validateAndNormalizeResourceRef("local_directory", localRef(t, fields))
	if err != nil {
		t.Fatalf("validateAndNormalizeResourceRef: %v", err)
	}
	var ref localDirectoryRef
	if err := json.Unmarshal(out, &ref); err != nil {
		t.Fatal(err)
	}
	return ref
}

// real_path is the directory's identity. Absent, it falls back to local_path —
// which is what the rule compared before the field existed, so old rows keep
// their meaning and the DB index (which COALESCEs the same two) agrees.
func TestLocalDirectoryIdentityPrefersRealPathAndFallsBackToLocalPath(t *testing.T) {
	withReal := localDirectoryRef{LocalPath: "/tmp/link", RealPath: "/private/tmp/target"}
	if got := localDirectoryIdentity(withReal); got != "/private/tmp/target" {
		t.Fatalf("identity = %q, want the resolved real_path", got)
	}
	legacy := localDirectoryRef{LocalPath: "/Users/me/code/app"}
	if got := localDirectoryIdentity(legacy); got != "/Users/me/code/app" {
		t.Fatalf("identity of a row with no real_path = %q, want its local_path", got)
	}
	if got := localDirectoryIdentity(localDirectoryRef{}); got != "" {
		t.Fatalf("identity of an empty ref = %q, want empty — an unidentifiable ref must not collide with another", got)
	}
}

// repo_key is normalized on the way in, so a client sending a full remote URL
// and one sending the already-normalized key produce the same stored value.
// Without that, one project could hold two rows for one repository simply
// because two clients spelled it differently.
func TestLocalDirectoryRepoKeyIsNormalizedOnSave(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://github.com/Owner/Repo.git", "github.com/owner/repo"},
		{"git@github.com:Owner/Repo.git", "github.com/owner/repo"},
		{"github.com/owner/repo", "github.com/owner/repo"},
		// A path is a location on one machine, not a repository identity —
		// letting it parse would make two unrelated folders compare equal.
		{"/Users/me/code/app", ""},
		{"", ""},
	}
	for _, tc := range cases {
		ref := normalizeLocalRef(t, map[string]any{
			"local_path": "/Users/me/code/app", "daemon_id": "d1", "repo_key": tc.in,
		})
		if ref.RepoKey != tc.want {
			t.Errorf("repo_key %q normalized to %q, want %q", tc.in, ref.RepoKey, tc.want)
		}
	}
}

// Invariant 12: a folder the machine holding it proved has no repository
// cannot be stored as parallel mode. Parallel mode delivers work as a branch;
// without a repository every single task on it would fail.
func TestWorktreeModeIsRefusedOnAProvenNonGitFolder(t *testing.T) {
	_, err := validateAndNormalizeResourceRef("local_directory", localRef(t, map[string]any{
		"local_path": "/Users/me/notes", "daemon_id": "d1",
		"execution_mode": "worktree", "is_git_repo": false,
	}))
	if err == nil {
		t.Fatal("parallel mode was accepted on a folder proven not to be a repository")
	}
	if !strings.Contains(err.Error(), "cannot use parallel") {
		t.Fatalf("error = %v, want it to refuse parallel mode", err)
	}

	// Proven-a-repo is fine. Absent is not: a client that cannot look at the
	// disk (the web UI) used to save worktree on a plain folder because the
	// field was missing. Parallel mode now requires an explicit true.
	if _, err := validateAndNormalizeResourceRef("local_directory", localRef(t, map[string]any{
		"local_path": "/Users/me/code/app", "daemon_id": "d1",
		"execution_mode": "worktree", "is_git_repo": true,
	})); err != nil {
		t.Errorf("a proven repo was refused parallel mode: %v", err)
	}
	if _, err := validateAndNormalizeResourceRef("local_directory", localRef(t, map[string]any{
		"local_path": "/Users/me/code/app", "daemon_id": "d1",
		"execution_mode": "worktree",
	})); err == nil {
		t.Fatal("parallel mode was accepted without is_git_repo; web and an un-enriched CLI would save a mode every task would fail")
	}

	// A plain folder stays perfectly legal in every other mode (form C).
	if _, err := validateAndNormalizeResourceRef("local_directory", localRef(t, map[string]any{
		"local_path": "/Users/me/notes", "daemon_id": "d1", "is_git_repo": false,
	})); err != nil {
		t.Errorf("a plain folder was refused in the default mode: %v", err)
	}
}

func TestWorktreeRootRefusedInsideTheBoundDirectory(t *testing.T) {
	_, err := validateAndNormalizeResourceRef("local_directory", localRef(t, map[string]any{
		"local_path": "/Users/me/code/app", "daemon_id": "d1",
		"real_path":     "/Users/me/code/app",
		"worktree_root": "/Users/me/code/app/.worktrees",
	}))
	if err == nil {
		t.Fatal("worktree_root inside the bound directory was accepted")
	}
	if !strings.Contains(err.Error(), "inside") {
		t.Fatalf("error = %v, want it to say the root sits inside the repository", err)
	}

	// Sibling is the default shape and must stay legal.
	if _, err := validateAndNormalizeResourceRef("local_directory", localRef(t, map[string]any{
		"local_path": "/Users/me/code/app", "daemon_id": "d1",
		"worktree_root": "/Users/me/code/app.multica-worktrees",
	})); err != nil {
		t.Errorf("a sibling worktree_root was refused: %v", err)
	}
}

func TestPathContainsIsSegmentWise(t *testing.T) {
	if !pathContains("/Users/me/repo", "/Users/me/repo/.worktrees") {
		t.Error("a child should be inside its parent")
	}
	if pathContains("/Users/me/repo", "/Users/me/repo-backup") {
		t.Error("/repo-backup is not inside /repo")
	}
	if !pathContains("/Users/me/repo", "/Users/me/repo") {
		t.Error("a directory contains itself")
	}
}

func TestGithubRepoStoresNormalizedRepoKey(t *testing.T) {
	out, err := validateAndNormalizeResourceRef("github_repo", localRef(t, map[string]any{
		"url": "git@github.com:Owner/Repo.git",
	}))
	if err != nil {
		t.Fatal(err)
	}
	var ref githubRepoRef
	if err := json.Unmarshal(out, &ref); err != nil {
		t.Fatal(err)
	}
	if ref.RepoKey != "github.com/owner/repo" {
		t.Fatalf("repo_key = %q, want github.com/owner/repo (host and path lowercased: DNS and GitHub are case-insensitive, so mixed case would let the same repository bind twice)", ref.RepoKey)
	}
}

func TestLocalDirectoryPathsMustBeAbsolute(t *testing.T) {
	for _, field := range []string{"real_path", "worktree_root"} {
		_, err := validateAndNormalizeResourceRef("local_directory", localRef(t, map[string]any{
			"local_path": "/Users/me/code/app", "daemon_id": "d1", field: "relative/path",
		}))
		if err == nil {
			t.Errorf("%s accepted a relative path; it would resolve against whatever the daemon's cwd happens to be", field)
		}
	}
}

// The capability-narrowing matrix is canonical in
// internal/coderesolve/filter_test.go, where the rule lives. What is left here
// is the handler's own contract: the conversion is faithful and the caller's
// slice is not mutated, because the same response is built once and the claim
// gates below read it.
func TestFilterForDaemonCapabilitiesConvertsWithoutMutatingTheCaller(t *testing.T) {
	resources := []ProjectResourceData{
		{ID: "r1", ResourceType: "local_directory", Label: "app", ResourceRef: localRef(t, map[string]any{
			"local_path": "/Users/me/code/app", "daemon_id": "d1",
			"execution_mode": "worktree", "worktree_root": "/Users/me/code/app.multica-worktrees",
		})},
		{ID: "r2", ResourceType: "github_repo", ResourceRef: localRef(t, map[string]any{"url": "https://github.com/o/r"})},
		{ID: "r3", ResourceType: "local_directory", ResourceRef: localRef(t, map[string]any{
			"local_path": "/Users/me/code/docs", "daemon_id": "d1",
		})},
	}

	narrowed := filterResourcesForDaemonCapabilities(resources, coderesolve.Daemon{ID: "d1"})

	if len(narrowed) != 2 || narrowed[0].ID != "r1" || narrowed[1].ID != "r2" {
		t.Fatalf("delivered %+v, want r1 and r2", narrowed)
	}
	if narrowed[0].Label != "app" || narrowed[0].ResourceType != "local_directory" {
		t.Errorf("conversion lost fields: %+v", narrowed[0])
	}
	if strings.Contains(string(narrowed[0].ResourceRef), "worktree_root") {
		t.Error("worktree_root reached a daemon that cannot honour it")
	}
	if string(narrowed[1].ResourceRef) != string(resources[1].ResourceRef) {
		t.Error("a github_repo resource was rewritten by the local_directory filter")
	}

	var original map[string]any
	if err := json.Unmarshal(resources[0].ResourceRef, &original); err != nil {
		t.Fatal(err)
	}
	if _, present := original["worktree_root"]; !present {
		t.Fatal("filtering mutated the caller's resource slice")
	}

	full := filterResourcesForDaemonCapabilities(resources, coderesolve.Daemon{
		ID: "d1", MultiLocalDirectory: true, WorktreeUserRoot: true,
	})
	if len(full) != len(resources) {
		t.Errorf("a capable daemon received %d of %d resources", len(full), len(resources))
	}
}

// The capability string is part of the wire contract in both directions:
// renaming it on one side alone silently downgrades every daemon.
func TestUserWorktreeRootCapabilityName(t *testing.T) {
	if protocol.DaemonCapabilityLocalWorktreeUserRootV1 != "local-worktree-user-root-v1" {
		t.Fatalf("capability = %q; daemons advertise the literal string, so renaming it strips worktree_root for every one of them",
			protocol.DaemonCapabilityLocalWorktreeUserRootV1)
	}
}
