package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/repoident"
	agentpkg "github.com/multica-ai/multica/server/pkg/agent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ProjectResourceResponse is the JSON shape returned by the project resource API.
type ProjectResourceResponse struct {
	ID           string          `json:"id"`
	ProjectID    string          `json:"project_id"`
	WorkspaceID  string          `json:"workspace_id"`
	ResourceType string          `json:"resource_type"`
	ResourceRef  json.RawMessage `json:"resource_ref"`
	Label        *string         `json:"label"`
	Position     int32           `json:"position"`
	CreatedAt    string          `json:"created_at"`
	CreatedBy    *string         `json:"created_by"`
}

func projectResourceToResponse(r db.ProjectResource) ProjectResourceResponse {
	ref := json.RawMessage(r.ResourceRef)
	if len(ref) == 0 {
		ref = json.RawMessage("{}")
	}
	return ProjectResourceResponse{
		ID:           uuidToString(r.ID),
		ProjectID:    uuidToString(r.ProjectID),
		WorkspaceID:  uuidToString(r.WorkspaceID),
		ResourceType: r.ResourceType,
		ResourceRef:  ref,
		Label:        textToPtr(r.Label),
		Position:     r.Position,
		CreatedAt:    timestampToString(r.CreatedAt),
		CreatedBy:    uuidToPtr(r.CreatedBy),
	}
}

// CreateProjectResourceRequest is the body for POST /api/projects/{id}/resources.
type CreateProjectResourceRequest struct {
	ResourceType string          `json:"resource_type"`
	ResourceRef  json.RawMessage `json:"resource_ref"`
	Label        *string         `json:"label"`
	Position     *int32          `json:"position"`
}

// UpdateProjectResourceRequest is the body for PUT /api/projects/{id}/resources/{resourceId}.
// resource_type cannot change after creation — pick a new type by deleting and
// re-adding. Every field is optional; omitted fields keep their current value.
type UpdateProjectResourceRequest struct {
	ResourceRef json.RawMessage `json:"resource_ref"`
	Label       *string         `json:"label"`
	Position    *int32          `json:"position"`
}

// validateAndNormalizeResourceRef checks the payload for a known resource_type.
// New types are added here without schema migration; unknown types are rejected
// at the API boundary so a typo can't slip through and produce a resource the
// daemon/UI doesn't understand.
func validateAndNormalizeResourceRef(resourceType string, ref json.RawMessage) (json.RawMessage, error) {
	return validateAndNormalizeResourceRefWithOptions(resourceType, ref, false)
}

func validateAndNormalizeResourceRefWithOptions(resourceType string, ref json.RawMessage, allowLegacyWorktreeRename bool) (json.RawMessage, error) {
	if len(ref) == 0 {
		return nil, errors.New("resource_ref is required")
	}
	switch resourceType {
	case "github_repo":
		return validateGithubRepoRef(ref)
	case "local_directory":
		return validateLocalDirectoryRefWithOptions(ref, allowLegacyWorktreeRename)
	default:
		return nil, fmt.Errorf("unknown resource_type %q", resourceType)
	}
}

// mergeOmittedLocalDirectoryIdentity keeps identity metadata written by newer
// clients when an older client resends a partial ref during an edit. Older
// clients only know the original four fields and would otherwise erase these
// values before validation (and, for worktree rows, fail the git capability
// check). Explicit values, including null, remain authoritative.
func mergeOmittedLocalDirectoryIdentity(existing, incoming json.RawMessage) json.RawMessage {
	var stored, next map[string]json.RawMessage
	if json.Unmarshal(existing, &stored) != nil || json.Unmarshal(incoming, &next) != nil {
		return incoming
	}
	for _, key := range []string{"is_git_repo", "real_path", "repo_key", "worktree_root"} {
		if _, present := next[key]; !present {
			if value, ok := stored[key]; ok {
				next[key] = value
			}
		}
	}
	merged, err := json.Marshal(next)
	if err != nil {
		return incoming
	}
	return merged
}

type githubRepoRef struct {
	URL               string `json:"url"`
	DefaultBranchHint string `json:"default_branch_hint,omitempty"`
	Ref               string `json:"ref,omitempty"`
	// RepoKey is the same normalized identity local_directory uses, computed
	// from URL on every save so a client cannot send a mismatched key.
	// Duplicate detection between a github_repo and a local checkout compares
	// these, not folder-name vs repo-name (DENE-618).
	RepoKey string `json:"repo_key,omitempty"`
}

func validateGithubRepoRef(ref json.RawMessage) (json.RawMessage, error) {
	var payload githubRepoRef
	if err := json.Unmarshal(ref, &payload); err != nil {
		return nil, fmt.Errorf("invalid github_repo payload: %w", err)
	}
	payload.URL = strings.TrimSpace(payload.URL)
	if payload.URL == "" {
		return nil, errors.New("github_repo: url is required")
	}
	if !isValidGitRepoURL(payload.URL) {
		return nil, errors.New("github_repo: url must be a valid http(s) or ssh git URL")
	}
	payload.DefaultBranchHint = strings.TrimSpace(payload.DefaultBranchHint)
	payload.Ref = strings.TrimSpace(payload.Ref)
	// Always recompute from the URL. A client-supplied key would drift the
	// moment the URL changed and the key did not.
	payload.RepoKey = string(repoident.NormalizeURL(payload.URL))
	out, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Execution modes for resource_type=local_directory. The zero value (absent
// field) means in_place, so resources created before worktree mode existed keep
// their original behavior without a data migration.
const (
	// localDirectoryModeInPlace runs the agent directly in the user's
	// directory, serialised by the daemon's per-path mutex: one task at a
	// time, edits land in the user's working tree.
	localDirectoryModeInPlace = "in_place"
	// localDirectoryModeWorktree runs each task in its own git worktree of
	// the user's repo, created inside the daemon's env root. Tasks on the
	// same directory run concurrently and deliver their work as a branch.
	// Only valid when the directory is a git working tree — the daemon
	// verifies that at task time, since the server can't see the filesystem.
	localDirectoryModeWorktree = "worktree"
	// localDirectoryModeShared runs the agent directly in the user's directory
	// like in_place, but WITHOUT the per-path mutex: tasks on the directory run
	// concurrently, and the daemon keeps its own per-task files (context
	// marker, project resources, skills, runtime brief) in the task's env root
	// instead of the user's tree. Meant for directories that are containers of
	// several repositories rather than one working copy, where isolation is
	// already handled by per-task branches and the mutex only serialised.
	localDirectoryModeShared = "shared"
)

// localDirectoryModeCapability maps an execution_mode that a daemon has to
// implement to the capability it advertises when it does. in_place is the
// historical default every daemon implements and needs no entry.
//
// minVersion is display-only, the release shown in the 422 payload so a user
// knows roughly what to update to; the gate itself reads the capability (see
// protocol.DaemonCapabilityLocalWorktreeV1 for why versions cannot gate).
func localDirectoryModeCapability(mode string) (capability, minVersion, humanMode string, gated bool) {
	switch mode {
	case localDirectoryModeWorktree:
		return protocol.DaemonCapabilityLocalWorktreeV1, agentpkg.MinLocalWorktreeCLIVersion, "parallel (worktree)", true
	case localDirectoryModeShared:
		return protocol.DaemonCapabilityLocalSharedV1, agentpkg.MinLocalSharedCLIVersion, "shared workspace", true
	default:
		return "", "", "", false
	}
}

// localDirectoryRef is the JSONB shape stored for resource_type=local_directory.
// It pins a project to an existing directory on a specific user machine. The
// daemon_id scopes the path to one daemon registration — the same string path
// on a different machine is a different resource. The optional label is a
// human-readable hint used by the UI; the row-level project_resource.label
// column remains the generic column for any resource type.
//
// execution_mode selects how tasks share that directory: in_place (default)
// keeps the historical one-task-at-a-time behavior, worktree gives each task an
// isolated git worktree so tasks run concurrently, and shared runs in the
// user's directory without the path mutex.
type localDirectoryRef struct {
	LocalPath     string `json:"local_path"`
	DaemonID      string `json:"daemon_id"`
	Label         string `json:"label,omitempty"`
	ExecutionMode string `json:"execution_mode,omitempty"`
	// RealPath is the directory's IDENTITY: the symlink-resolved absolute
	// path, as the machine holding it reported at pick time. It is what the
	// (project, real_path) uniqueness rule keys on, so two routes to one
	// directory — a symlink and its target, /tmp and /private/tmp — cannot be
	// bound twice under two spellings (DENE-617).
	//
	// Optional on the wire because a client that predates it, or a browser
	// that cannot call realpath, still has to be able to save a directory.
	// Absent means "use local_path as the identity": that is exactly what the
	// rule did before this field existed, so old rows keep their meaning and
	// the DB index (which coalesces the two) agrees with this package.
	RealPath string `json:"real_path,omitempty"`
	// RepoKey is the repository this directory holds, normalized by
	// repoident from the directory's git remote. Empty for a plain folder, a
	// repo with no remote, or any client that did not report one — and an
	// empty key is never equal to another empty key, so those never collide.
	//
	// Its only job is duplicate detection against a github_repo pointing at
	// the same repository, and against a second checkout of that repository
	// on the same machine.
	RepoKey string `json:"repo_key,omitempty"`
	// WorktreeRoot is where parallel mode puts its working copies. It exists
	// so those copies land on the USER's disk, beside their repository,
	// instead of inside the Multica workspace where a workspace GC would
	// reclaim a directory the user still wanted to look at (DENE-617).
	//
	// Empty means "the daemon picks the default", which is the repository's
	// sibling `<repo>.multica-worktrees`. Only parallel mode reads it.
	WorktreeRoot string `json:"worktree_root,omitempty"`
	// IsGitRepo is what the machine holding the directory saw at pick time.
	// The server cannot look at a filesystem on someone else's laptop, so
	// this is the only way it can refuse parallel mode on a folder that has
	// no repository to branch from — a resource whose every task would fail.
	//
	// true means a git working tree with at least one commit. false and
	// absent both refuse parallel mode: a client that cannot measure the
	// disk (the web UI) must not save a mode every task would fail.
	IsGitRepo *bool `json:"is_git_repo,omitempty"`
}

// localDirectoryIdentity is the value the (project, real_path) uniqueness rule
// compares. Kept as one function because the DB index computes the same
// COALESCE and the two must not drift: a rule the application enforces on one
// value while the database enforces it on another is two rules.
func localDirectoryIdentity(ref localDirectoryRef) string {
	if p := strings.TrimSpace(ref.RealPath); p != "" {
		return p
	}
	return strings.TrimSpace(ref.LocalPath)
}

// requireModeCapableDaemon rejects saving a local_directory ref that asks for
// an execution_mode (worktree, shared) while the daemon owning the path is too
// old to implement the mode. An old daemon does not know the field exists: it
// would json-skip it and run tasks IN PLACE — for worktree, editing the
// working copy the user explicitly asked to isolate; for shared, taking the
// mutex and silently re-serialising the directory the user asked to share —
// and it predates the daemon-side unknown-mode refusal, so only the server can
// stop it. Gating at save time surfaces the failure at the moment the user can
// act on it (upgrade the daemon), instead of as a silently-wrong task later.
//
// Residual gap, accepted for now: a daemon downgraded AFTER the resource was
// saved is not caught here; closing that needs a claim-time gate.
//
// Returns true to proceed; on false the 422 response has already been written,
// using the same daemon_version_unsupported code as the quick-create gate so
// clients can branch on it.
func (h *Handler) requireModeCapableDaemon(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, resourceType string, normalizedRef json.RawMessage) bool {
	if resourceType != "local_directory" {
		return true
	}
	var ref localDirectoryRef
	if err := json.Unmarshal(normalizedRef, &ref); err != nil {
		return true
	}
	capability, minVersion, humanMode, gated := localDirectoryModeCapability(ref.ExecutionMode)
	if !gated {
		return true
	}

	// One machine hosts one runtime row per provider, all registered by the
	// same daemon binary. A workspace has a handful of runtimes; the unfiltered
	// list is a tiny read.
	runtimes, err := h.Queries.ListAgentRuntimes(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check runtime capabilities")
		return false
	}
	// Same signal the claim gate uses: what the daemon advertised, recorded on
	// its runtime row at registration. Version numbers cannot answer this — a
	// dev-built daemon reports a git-describe string that the version floor
	// deliberately exempts (MUL-5707).
	if daemonAdvertisesCapability(runtimes, ref.DaemonID, capability) {
		return true
	}
	// Fail closed when no runtime for this daemon advertises it — including a
	// daemon_id with no registered runtime at all: a resource that can never
	// dispatch correctly is worse than a save-time error.
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"error": fmt.Sprintf(
			"local_directory: %q is set to %s mode, but the Multica runtime on that machine does not support it. Update the Multica app on that machine to the latest version, or keep the resource on in_place.",
			ref.LocalPath, humanMode),
		"code":            "daemon_version_unsupported",
		"current_version": latestDaemonCLIVersion(runtimes, ref.DaemonID),
		"min_version":     minVersion,
		"daemon_id":       ref.DaemonID,
	})
	return false
}

// daemonAdvertisesWorktree reports whether the daemon's MOST RECENTLY SEEN
// runtime row advertised worktree support.
func daemonAdvertisesWorktree(runtimes []db.AgentRuntime, daemonID string) bool {
	return daemonAdvertisesCapability(runtimes, daemonID, protocol.DaemonCapabilityLocalWorktreeV1)
}

// daemonAdvertisesCapability reports whether the daemon's MOST RECENTLY SEEN
// runtime row advertised capability.
//
// Deliberately not "any row advertised it". Deregistering a runtime only flips
// the row to offline — its metadata survives — and ListAgentRuntimes returns
// every row. So a machine that once ran a capable daemon, then downgraded,
// still has an old capable row sitting next to the fresh incapable one, and an
// any-match would keep saying yes forever. Newest-wins reads the machine's
// CURRENT binary, which is the question being asked.
//
// A row missing the capability is never skipped: being the newest is what makes
// it authoritative, not whether its answer is convenient.
func daemonAdvertisesCapability(runtimes []db.AgentRuntime, daemonID, capability string) bool {
	if strings.TrimSpace(daemonID) == "" {
		return false
	}
	var newest *db.AgentRuntime
	for i := range runtimes {
		rt := &runtimes[i]
		if !rt.DaemonID.Valid || rt.DaemonID.String != daemonID {
			continue
		}
		if newest == nil || runtimeSeenAfter(rt, newest) {
			newest = rt
		}
	}
	if newest == nil {
		return false
	}
	return runtimeHasCapability(newest.Metadata, capability)
}

// runtimeSeenAfter orders two rows of the same daemon by last_seen_at. A row
// that never reported (NULL) sorts oldest, so a live row always wins over one
// that never checked in.
func runtimeSeenAfter(candidate, current *db.AgentRuntime) bool {
	if !candidate.LastSeenAt.Valid {
		return false
	}
	if !current.LastSeenAt.Valid {
		return true
	}
	return candidate.LastSeenAt.Time.After(current.LastSeenAt.Time)
}

// latestDaemonCLIVersion returns the cli_version of the freshest runtime row
// registered by daemonID, or "" when the daemon has no row carrying one. Rows
// without a version are skipped rather than treated as authoritative: one
// machine registers a row per provider, and only the freshest version-bearing
// row reflects the binary currently running there.
func latestDaemonCLIVersion(runtimes []db.AgentRuntime, daemonID string) string {
	current := ""
	var currentSeen pgtype.Timestamptz
	for _, rt := range runtimes {
		if !rt.DaemonID.Valid || rt.DaemonID.String != daemonID {
			continue
		}
		v := readRuntimeCLIVersion(rt.Metadata)
		if v == "" {
			continue
		}
		if current == "" || (rt.LastSeenAt.Valid && rt.LastSeenAt.Time.After(currentSeen.Time)) {
			current = v
			currentSeen = rt.LastSeenAt
		}
	}
	return current
}

func validateLocalDirectoryRef(ref json.RawMessage) (json.RawMessage, error) {
	return validateLocalDirectoryRefWithOptions(ref, false)
}

func validateLocalDirectoryRefWithOptions(ref json.RawMessage, allowLegacyWorktreeRename bool) (json.RawMessage, error) {
	var payload localDirectoryRef
	if err := json.Unmarshal(ref, &payload); err != nil {
		return nil, fmt.Errorf("invalid local_directory payload: %w", err)
	}
	payload.LocalPath = strings.TrimSpace(payload.LocalPath)
	if payload.LocalPath == "" {
		return nil, errors.New("local_directory: local_path is required")
	}
	if !isAbsoluteLocalPath(payload.LocalPath) {
		return nil, errors.New("local_directory: local_path must be an absolute path")
	}
	payload.DaemonID = strings.TrimSpace(payload.DaemonID)
	if payload.DaemonID == "" {
		return nil, errors.New("local_directory: daemon_id is required")
	}
	payload.Label = strings.TrimSpace(payload.Label)
	payload.ExecutionMode = strings.TrimSpace(payload.ExecutionMode)
	switch payload.ExecutionMode {
	case "", localDirectoryModeInPlace, localDirectoryModeWorktree, localDirectoryModeShared:
	default:
		return nil, fmt.Errorf("local_directory: execution_mode must be %q, %q or %q, got %q",
			localDirectoryModeInPlace, localDirectoryModeWorktree, localDirectoryModeShared, payload.ExecutionMode)
	}
	payload.RealPath = strings.TrimSpace(payload.RealPath)
	if payload.RealPath != "" && !isAbsoluteLocalPath(payload.RealPath) {
		return nil, errors.New("local_directory: real_path must be an absolute path")
	}
	// Normalize rather than trust: the client may send a full remote URL or an
	// already-normalized key, and repoident maps both onto the same value.
	// A key it cannot identify is dropped — storing an unidentifiable string
	// would make two unrelated directories compare equal under the
	// (project, daemon, repo_key) rule.
	payload.RepoKey = string(repoident.NormalizeURL(payload.RepoKey))
	payload.WorktreeRoot = strings.TrimSpace(payload.WorktreeRoot)
	if payload.WorktreeRoot != "" && !isAbsoluteLocalPath(payload.WorktreeRoot) {
		return nil, errors.New("local_directory: worktree_root must be an absolute path")
	}
	if payload.WorktreeRoot != "" {
		// The bound directory is the best git-root the server has. A client
		// that can see the disk may send a subdirectory; refusing a root
		// inside that path is still correct, and the daemon re-checks against
		// the real git top-level at task time.
		bound := localDirectoryIdentity(payload)
		if bound != "" && pathContains(bound, payload.WorktreeRoot) {
			return nil, fmt.Errorf(
				"local_directory: worktree_root %q sits inside %q — working copies there would appear in the repository's own git status; put them beside the repository instead",
				payload.WorktreeRoot, payload.LocalPath)
		}
	}
	// Parallel mode branches from a repository with at least one commit.
	// The server cannot see the user's disk, so it requires the client that
	// can to say so. Absent used to be treated as "nobody looked, allow" —
	// which let the web UI and an un-enriched CLI save worktree on a plain
	// folder. Web cannot measure a filesystem, so missing is now a refusal
	// (DENE-618).
	if payload.ExecutionMode == localDirectoryModeWorktree &&
		(payload.IsGitRepo == nil || !*payload.IsGitRepo) {
		if allowLegacyWorktreeRename && payload.IsGitRepo == nil {
			// A pre-identity client may resend an already-persisted worktree ref
			// solely to rename it. Preserve that legacy edit without making the
			// old row prove a capability it never stored.
		} else {
			return nil, fmt.Errorf(
				"local_directory: %q cannot use parallel (worktree) mode — "+
					"parallel mode delivers work as a branch and needs a git repository with at least one commit. "+
					"Keep it on in_place, or bind it from the desktop app / CLI on the machine that holds the folder",
				payload.LocalPath)
		}
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// localDirectoryRefLabel reads the label carried inside a local_directory ref,
// trimmed. Returns "" for a missing label or a ref that does not parse — the
// callers only compare labels, so an unreadable ref behaves like an unlabeled
// one instead of failing the write.
func localDirectoryRefLabel(ref json.RawMessage) string {
	var payload localDirectoryRef
	if err := json.Unmarshal(ref, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Label)
}

// localDirectoryRefDiffersOnlyByLabel reports whether two refs have the same
// execution semantics once their display label and identity metadata are set
// aside. Identity fields are deliberately ignored because newer clients may
// enrich an old ref while an older client is only renaming the resource.
//
// This is what separates "a ≤ v0.4.28 client renamed the folder" from "a client
// sent a ref it had been holding since before someone else renamed it". Both
// arrive as a full ref whose label differs from the stored one; only the first
// is a rename. The mode dialog snapshots the whole ref when it opens, so a
// second device renaming in between turns an execution-mode save into a name
// rollback unless the two are told apart.
//
// Compared as decoded values rather than bytes: the stored ref may have been
// written by a different build, so key order and spacing prove nothing. Unknown
// keys count — a difference this binary cannot interpret is still a difference,
// and calling such a request a pure rename would be a guess.
func localDirectoryRefDiffersOnlyByLabel(a, b json.RawMessage) bool {
	strip := func(raw json.RawMessage) (map[string]any, bool) {
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, false
		}
		delete(fields, "label")
		for _, key := range []string{"real_path", "repo_key", "is_git_repo", "worktree_root"} {
			delete(fields, key)
		}
		return fields, true
	}
	left, ok := strip(a)
	if !ok {
		return false
	}
	right, ok := strip(b)
	if !ok {
		return false
	}
	return reflect.DeepEqual(left, right)
}

// withLocalDirectoryRefLabel returns ref with only its "label" key set (or
// removed, when label is NULL) and every other key preserved.
//
// A local_directory's display name has two homes: desktop builds up to v0.4.28
// rename by rewriting resource_ref.label, newer clients rename the top-level
// column precisely so they never resend the ref (an older server re-normalizes
// a resent ref and silently drops the fields it does not know — see
// handleRenameLocalDirectory in project-resources-section.tsx). This server is
// the only writer that sees both kinds of client, so it converges the two
// copies on every write; without that, each client generation keeps editing
// its own copy and the same row shows different names on different devices.
//
// Patch the raw map rather than round-tripping localDirectoryRef: the stored
// ref may carry fields written by a NEWER server, and re-marshaling through
// this binary's struct would drop them — the exact failure mode this PR exists
// to close. Key order and whitespace are not preserved (nor promised by JSONB);
// every key and value is.
func withLocalDirectoryRefLabel(ref json.RawMessage, label pgtype.Text) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(ref, &fields); err != nil {
		return nil, err
	}
	if label.Valid {
		encoded, err := json.Marshal(label.String)
		if err != nil {
			return nil, err
		}
		fields["label"] = encoded
	} else {
		delete(fields, "label")
	}
	return json.Marshal(fields)
}

// isAbsoluteLocalPath checks the path looks absolute on either POSIX or
// Windows daemons. The server can't know which OS the daemon runs on, so we
// accept the union: a leading "/" (POSIX), a UNC prefix "\\", or a drive
// letter like "C:\" or "C:/". The daemon still verifies existence at run
// time — this is a typo guard, not a filesystem check.
func isAbsoluteLocalPath(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '/' {
		return true
	}
	if strings.HasPrefix(s, `\\`) {
		return true
	}
	if len(s) >= 3 && isDriveLetter(s[0]) && s[1] == ':' && (s[2] == '\\' || s[2] == '/') {
		return true
	}
	return false
}

func isDriveLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// isValidGitRepoURL accepts the three forms a user can paste from GitHub's
// "Code" menu: https://, ssh:// (with explicit scheme), and the scp-like
// shorthand `git@host:owner/repo.git`. The check is intentionally lax — we are
// guarding against pasted garbage like "not-a-url", not enforcing a strict
// grammar — because the actual fetch happens client-side via `git clone` and
// the user gets a clearer error from git than from us.
func isValidGitRepoURL(s string) bool {
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		switch u.Scheme {
		case "http", "https", "ssh", "git":
			return true
		}
	}
	// scp-like ssh shorthand: [user@]host:path with a non-empty host and path,
	// and no spaces. Reject anything that looks like a URL with a scheme
	// (those should go through url.Parse above).
	if strings.Contains(s, " ") || strings.Contains(s, "://") {
		return false
	}
	colon := strings.Index(s, ":")
	if colon <= 0 || colon == len(s)-1 {
		return false
	}
	// In scp-like ssh shorthand `[user@]host:path`, `@` is only meaningful
	// as a user separator before the first ':'. If '@' appears at or after
	// the colon it is not the user separator — reject as malformed rather
	// than guess (and avoid a slice-bounds panic from blindly slicing).
	at := strings.Index(s, "@")
	if at >= colon {
		return false
	}
	hostStart := 0
	if at >= 0 {
		hostStart = at + 1
	}
	host := s[hostStart:colon]
	path := s[colon+1:]
	if host == "" || path == "" {
		return false
	}
	return true
}

// loadProjectForResource resolves the project, enforces workspace ownership,
// and returns its DB row. Used by all project_resource handlers.
func (h *Handler) loadProjectForResource(w http.ResponseWriter, r *http.Request, projectIDParam string) (db.Project, bool) {
	projectUUID, ok := parseUUIDOrBadRequest(w, projectIDParam, "project id")
	if !ok {
		return db.Project{}, false
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace id")
	if !ok {
		return db.Project{}, false
	}
	project, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
		ID: projectUUID, WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return db.Project{}, false
	}
	return project, true
}

// ListProjectResources returns the resources attached to a project.
func (h *Handler) ListProjectResources(w http.ResponseWriter, r *http.Request) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	resources, err := h.Queries.ListProjectResources(r.Context(), project.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list project resources")
		return
	}
	resp := make([]ProjectResourceResponse, len(resources))
	for i, res := range resources {
		resp[i] = projectResourceToResponse(res)
	}
	writeJSON(w, http.StatusOK, map[string]any{"resources": resp, "total": len(resp)})
}

// CreateProjectResource attaches a new resource to a project.
func (h *Handler) CreateProjectResource(w http.ResponseWriter, r *http.Request) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req CreateProjectResourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.ResourceType = strings.TrimSpace(req.ResourceType)
	if req.ResourceType == "" {
		writeError(w, http.StatusBadRequest, "resource_type is required")
		return
	}
	normalizedRef, err := validateAndNormalizeResourceRef(req.ResourceType, req.ResourceRef)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if conflict, reason, err := h.findLocalDirectoryConflictReason(r.Context(), project.ID, req.ResourceType, normalizedRef, pgtype.UUID{}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check existing resources")
		return
	} else if conflict {
		writeError(w, http.StatusConflict, reason)
		return
	}

	if !h.requireModeCapableDaemon(w, r, project.WorkspaceID, req.ResourceType, normalizedRef) {
		return
	}

	var label pgtype.Text
	if req.Label != nil && strings.TrimSpace(*req.Label) != "" {
		label = pgtype.Text{String: strings.TrimSpace(*req.Label), Valid: true}
	}
	var position int32
	if req.Position != nil {
		position = *req.Position
	} else {
		// Append after existing resources.
		count, _ := h.Queries.CountProjectResources(r.Context(), project.ID)
		position = int32(count)
	}

	creator, _ := h.parseUserUUIDOrZero(userID)
	resource, err := h.Queries.CreateProjectResource(r.Context(), db.CreateProjectResourceParams{
		ProjectID:    project.ID,
		WorkspaceID:  project.WorkspaceID,
		ResourceType: req.ResourceType,
		ResourceRef:  normalizedRef,
		Label:        label,
		Position:     position,
		CreatedBy:    creator,
	})
	if err != nil {
		if isUniqueViolation(err) {
			h.writeProjectResourceUniqueConflict(w, r.Context(), project.ID, req.ResourceType, normalizedRef, pgtype.UUID{})
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create project resource")
		return
	}

	resp := projectResourceToResponse(resource)
	h.publish(
		protocol.EventProjectResourceCreated,
		uuidToString(project.WorkspaceID),
		"member",
		userID,
		map[string]any{"resource": resp, "project_id": uuidToString(project.ID)},
	)
	writeJSON(w, http.StatusCreated, resp)
}

// UpdateProjectResource edits an existing resource's ref/label/position.
// resource_type is immutable — re-pointing a resource at a different type is
// almost always a different conceptual entity, so the caller should delete and
// re-add instead. Omitted fields keep their current value, including the
// `label` JSON null vs. missing distinction (missing = keep, explicit "" =
// clear).
func (h *Handler) UpdateProjectResource(w http.ResponseWriter, r *http.Request) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	resourceUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "resourceId"), "resource id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	existing, err := h.Queries.GetProjectResourceInWorkspace(r.Context(), db.GetProjectResourceInWorkspaceParams{
		ID: resourceUUID, WorkspaceID: project.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "project resource not found")
		return
	}
	if uuidToString(existing.ProjectID) != uuidToString(project.ID) {
		writeError(w, http.StatusNotFound, "project resource not found")
		return
	}

	// Decode into a raw map first so we can tell "field omitted" from
	// "field present with zero value" — the label clear case in particular
	// relies on this distinction.
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	nextRef := json.RawMessage(existing.ResourceRef)
	rawRef, refProvided := raw["resource_ref"]
	if refProvided {
		allowLegacyWorktreeRename := false
		if existing.ResourceType == "local_directory" {
			rawRef = mergeOmittedLocalDirectoryIdentity(existing.ResourceRef, rawRef)
			allowLegacyWorktreeRename = localDirectoryRefDiffersOnlyByLabel(rawRef, existing.ResourceRef)
		}
		normalized, err := validateAndNormalizeResourceRefWithOptions(existing.ResourceType, rawRef, allowLegacyWorktreeRename)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		nextRef = normalized
	}

	if conflict, reason, err := h.findLocalDirectoryConflictReason(r.Context(), project.ID, existing.ResourceType, nextRef, existing.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check existing resources")
		return
	} else if conflict {
		writeError(w, http.StatusConflict, reason)
		return
	}

	// A ≤ v0.4.28 client renames by resending the ref, so "the ref was sent"
	// does not mean "the execution mode was touched". Anything else about the
	// ref changing does mean it might have been.
	refRenameOnly := refProvided &&
		existing.ResourceType == "local_directory" &&
		localDirectoryRefDiffersOnlyByLabel(nextRef, existing.ResourceRef)

	// Gate only when the caller is actually changing what would run: a label or
	// position update — including an old client's rename, which carries the ref
	// along — must not start failing because the daemon's registration drifted
	// after the mode was legitimately saved. The row already says worktree; the
	// claim gate is what stops it from running somewhere that cannot.
	if refProvided && !refRenameOnly {
		if !h.requireModeCapableDaemon(w, r, project.WorkspaceID, existing.ResourceType, nextRef) {
			return
		}
	}

	nextLabel := existing.Label
	// Tracks an explicit clear, as opposed to a column that was never set.
	// Only the former may remove the ref's legacy label copy below: rows
	// created by older clients keep their only name inside the ref, and an
	// unrelated update must not strip it just because the column is NULL.
	labelCleared := false
	if rawLabel, ok := raw["label"]; ok {
		var labelStr *string
		if err := json.Unmarshal(rawLabel, &labelStr); err != nil {
			writeError(w, http.StatusBadRequest, "label must be a string or null")
			return
		}
		if labelStr == nil || strings.TrimSpace(*labelStr) == "" {
			nextLabel = pgtype.Text{}
			labelCleared = true
		} else {
			nextLabel = pgtype.Text{String: strings.TrimSpace(*labelStr), Valid: true}
		}
	} else if refRenameOnly {
		// No label field, and the ref differs ONLY by its embedded label: that
		// is how desktop builds up to v0.4.28 rename — they rewrite ref.label
		// and never send the column. Follow the rename into the column, or the
		// newer clients (which read the column first) keep showing the old
		// name this rename just replaced.
		//
		// A ref that also changes something else is not a rename however
		// different its label looks: the mode dialog snapshots the ref when it
		// opens, so a rename on another device in the meantime would otherwise
		// be undone by whoever saves an execution mode next.
		if refLabel := localDirectoryRefLabel(nextRef); refLabel != localDirectoryRefLabel(existing.ResourceRef) {
			if refLabel == "" {
				nextLabel = pgtype.Text{}
				labelCleared = true
			} else {
				nextLabel = pgtype.Text{String: refLabel, Valid: true}
			}
		}
	}

	nextPosition := existing.Position
	if rawPos, ok := raw["position"]; ok {
		var pos *int32
		if err := json.Unmarshal(rawPos, &pos); err != nil {
			writeError(w, http.StatusBadRequest, "position must be an integer")
			return
		}
		if pos != nil {
			nextPosition = *pos
		}
	}

	// Mirror the final label into the ref's legacy copy so both client
	// generations read the same name, including removing it on an explicit
	// clear — otherwise the display falls back to the name the user just
	// deleted. Rows the two-copy era left disagreeing converge on their first
	// write here. A NULL column that was never set stays out of the ref: for
	// rows created by older clients the ref copy IS the name.
	if existing.ResourceType == "local_directory" {
		// The name this request is entitled to write. Only a rename or an
		// explicit label field may change it; anything else keeps whatever the
		// row is called today, wherever that name currently lives — so a stale
		// ref snapshot cannot carry an old name back in behind an unrelated
		// edit.
		name := nextLabel
		if !nextLabel.Valid && !labelCleared {
			if stored := localDirectoryRefLabel(existing.ResourceRef); stored != "" {
				name = pgtype.Text{String: stored, Valid: true}
			}
		}
		// Rows that have never had a name at all are left alone, and so is the
		// ref on updates that do not touch it: an unrelated position change
		// must not rewrite a ref, and for old rows the ref copy IS the name.
		if refProvided || nextLabel.Valid || labelCleared {
			synced, err := withLocalDirectoryRefLabel(nextRef, name)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to update project resource")
				return
			}
			nextRef = synced
		}
	}

	updated, err := h.Queries.UpdateProjectResource(r.Context(), db.UpdateProjectResourceParams{
		ID:          existing.ID,
		ResourceRef: nextRef,
		Label:       nextLabel,
		Position:    nextPosition,
	})
	if err != nil {
		if isUniqueViolation(err) {
			h.writeProjectResourceUniqueConflict(w, r.Context(), project.ID, existing.ResourceType, nextRef, existing.ID)
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update project resource")
		return
	}

	resp := projectResourceToResponse(updated)
	h.publish(
		protocol.EventProjectResourceUpdated,
		uuidToString(project.WorkspaceID),
		"member",
		userID,
		map[string]any{"resource": resp, "project_id": uuidToString(project.ID)},
	)
	writeJSON(w, http.StatusOK, resp)
}

// findLocalDirectoryConflict enforces the two identity rules a project's local
// directories must satisfy (DENE-617):
//
//  1. (project, daemon_id, real_path) — one row per DIRECTORY on one machine.
//     Binding the same directory twice is never a thing a user meant; it just
//     makes the choice of which row wins arbitrary. daemon_id is in the key
//     because a path string only names a directory on the machine holding it.
//  2. (project, daemon_id, repo_key), when repo_key is non-empty — one row per
//     REPOSITORY per machine. Two checkouts of one repository on one machine
//     are two copies of the same code, which is the duplication this whole
//     change exists to stop.
//
// What it deliberately no longer enforces is "at most one local_directory per
// (project, daemon)". A project can legitimately span several directories on
// one machine — four unrelated plain folders, a repo plus its docs checkout —
// and the old rule made that impossible. What made the old rule necessary was
// the daemon picking an ARBITRARY matching row; it now takes the first in
// `position` order and exposes the rest read-only, so "which directory" has a
// stated answer instead of a race.
//
// Both rules are also Postgres partial unique indexes (migrations 498/499), so
// a client that bypasses this API cannot create the state either. This copy
// exists to turn the constraint violation into a message naming the directory.
//
// `excludeID` lets the update path ignore the row being edited.
func (h *Handler) findLocalDirectoryConflict(ctx context.Context, projectID pgtype.UUID, resourceType string, normalizedRef json.RawMessage, excludeID pgtype.UUID) (bool, error) {
	conflict, _, err := h.findLocalDirectoryConflictReason(ctx, projectID, resourceType, normalizedRef, excludeID)
	return conflict, err
}

// findLocalDirectoryConflictReason is findLocalDirectoryConflict plus the
// sentence explaining which rule fired, so the caller can say which directory
// is already bound instead of a generic refusal.
func (h *Handler) findLocalDirectoryConflictReason(ctx context.Context, projectID pgtype.UUID, resourceType string, normalizedRef json.RawMessage, excludeID pgtype.UUID) (bool, string, error) {
	if resourceType != "local_directory" {
		return false, "", nil
	}
	var incoming localDirectoryRef
	if err := json.Unmarshal(normalizedRef, &incoming); err != nil {
		return false, "", err
	}
	rows, err := h.Queries.ListProjectResources(ctx, projectID)
	if err != nil {
		return false, "", err
	}
	incomingIdentity := localDirectoryIdentity(incoming)
	for _, row := range rows {
		if row.ResourceType != "local_directory" {
			continue
		}
		if excludeID.Valid && uuidToString(row.ID) == uuidToString(excludeID) {
			continue
		}
		var existing localDirectoryRef
		if err := json.Unmarshal(row.ResourceRef, &existing); err != nil {
			continue
		}
		if existing.DaemonID != incoming.DaemonID {
			// A path string names a directory only on the machine holding it.
			// Two machines may each carry /Users/me/code/app for one project.
			continue
		}
		if incomingIdentity != "" && localDirectoryIdentity(existing) == incomingIdentity {
			return true, fmt.Sprintf(
				"%q is already added to this project", existing.LocalPath), nil
		}
		if incoming.RepoKey != "" && existing.RepoKey == incoming.RepoKey {
			return true, fmt.Sprintf(
				"this repository is already added on this machine as %q — a second checkout of it would be a second copy of the same code",
				existing.LocalPath), nil
		}
		if incoming.WorktreeRoot != "" {
			existingID := localDirectoryIdentity(existing)
			if existingID != "" && (pathContains(existingID, incoming.WorktreeRoot) || pathContains(incoming.WorktreeRoot, existingID)) {
				return true, fmt.Sprintf(
					"worktree_root %q conflicts with the directory already bound as %q",
					incoming.WorktreeRoot, existing.LocalPath), nil
			}
		}
	}
	return false, "", nil
}

// pathContains reports whether candidate is parent or lives under it, compared
// segment-wise. `/repo-backup` is not inside `/repo`. Separators are folded so
// a Windows path and a POSIX path can be compared as the strings the client
// sent — the server has no filesystem to canonicalise them against.
func pathContains(parent, candidate string) bool {
	p := normalizePathForCompare(parent)
	c := normalizePathForCompare(candidate)
	if p == "" || c == "" {
		return false
	}
	if p == c {
		return true
	}
	return strings.HasPrefix(c, p+"/")
}

func normalizePathForCompare(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\\", "/")
	s = strings.TrimRight(s, "/")
	return s
}

func (h *Handler) writeProjectResourceUniqueConflict(w http.ResponseWriter, ctx context.Context, projectID pgtype.UUID, resourceType string, ref json.RawMessage, excludeID pgtype.UUID) {
	if conflict, reason, err := h.findLocalDirectoryConflictReason(ctx, projectID, resourceType, ref, excludeID); err == nil && conflict {
		writeError(w, http.StatusConflict, reason)
		return
	}
	if resourceType == "local_directory" {
		var incoming localDirectoryRef
		if json.Unmarshal(ref, &incoming) == nil {
			if p := strings.TrimSpace(incoming.LocalPath); p != "" {
				writeError(w, http.StatusConflict, fmt.Sprintf("%q is already added to this project", p))
				return
			}
		}
	}
	writeError(w, http.StatusConflict, "this resource is already attached to the project")
}

// DeleteProjectResource removes a resource from a project.
func (h *Handler) DeleteProjectResource(w http.ResponseWriter, r *http.Request) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	resourceUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "resourceId"), "resource id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	resource, err := h.Queries.GetProjectResourceInWorkspace(r.Context(), db.GetProjectResourceInWorkspaceParams{
		ID: resourceUUID, WorkspaceID: project.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "project resource not found")
		return
	}
	if uuidToString(resource.ProjectID) != uuidToString(project.ID) {
		writeError(w, http.StatusNotFound, "project resource not found")
		return
	}
	if err := h.Queries.DeleteProjectResource(r.Context(), resource.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete project resource")
		return
	}
	h.publish(
		protocol.EventProjectResourceDeleted,
		uuidToString(project.WorkspaceID),
		"member",
		userID,
		map[string]any{
			"project_id":  uuidToString(project.ID),
			"resource_id": uuidToString(resource.ID),
		},
	)
	w.WriteHeader(http.StatusNoContent)
}

// parseUserUUIDOrZero converts a user ID string to a pgtype.UUID, returning a
// zero value on any error so the caller can store NULL for created_by when the
// authenticated principal is not a workspace member (e.g. internal-server use).
func (h *Handler) parseUserUUIDOrZero(userID string) (pgtype.UUID, bool) {
	if userID == "" {
		return pgtype.UUID{}, false
	}
	u, err := parseUUIDLoose(userID)
	if err != nil {
		return pgtype.UUID{}, false
	}
	return u, true
}

// parseUUIDLoose mirrors util.ParseUUID but lives here to avoid pulling util
// into a tiny one-off helper. Keep the body minimal.
func parseUUIDLoose(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return pgtype.UUID{}, err
	}
	return u, nil
}

// claimProjectContext is the project-scoped context a daemon claim exposes to
// the agent: the project identities the prompt names, the resource manifest
// execenv materializes into .multica/project/resources.json, and the repo list
// `multica repo checkout` reads.
//
// A task can carry several projects (DENE-523): a chat session binds a set of
// them, while an issue, autopilot, or quick-create task carries at most one.
// Projects holds them in priority order; Repos is the union across all of
// them, because `multica repo checkout` serves one flat list.
type claimProjectContext struct {
	Projects []claimProject
	Repos    []RepoData
}

// claimProject is one attached project: the identity the brief names, the
// resources the agent may open, and the repos lifted out of them.
type claimProject struct {
	ID          string
	Title       string
	Description string
	Resources   []ProjectResourceData
	Repos       []RepoData
}

// applyTo copies the resolved context onto a claim response. Callers assign the
// whole context or none of it, so a claim can never carry a project's title
// without its resources.
//
// The singular fields stay populated from the FIRST project — the task's
// primary one — so a daemon that predates projects[] still renders the primary
// project's context instead of none. Projects[] is the full set.
func (c claimProjectContext) applyTo(resp *AgentTaskResponse) {
	if len(c.Projects) > 0 {
		resp.Projects = make([]TaskProjectContextData, 0, len(c.Projects))
		for _, p := range c.Projects {
			resp.Projects = append(resp.Projects, TaskProjectContextData{
				ID:          p.ID,
				Title:       p.Title,
				Description: p.Description,
				Resources:   p.Resources,
			})
		}
		primary := c.Projects[0]
		resp.ProjectID = primary.ID
		resp.ProjectTitle = primary.Title
		resp.ProjectDescription = primary.Description
		if len(primary.Resources) > 0 {
			resp.ProjectResources = primary.Resources
		}
	}
	resp.Repos = c.Repos
}

// resolveClaimProjectContext loads the project context for one daemon claim
// whose task carries at most one project (issue, autopilot, quick-create).
// Chat tasks carry a set and go through resolveClaimChatProjectContext.
func (h *Handler) resolveClaimProjectContext(ctx context.Context, projectID, workspaceID pgtype.UUID) (claimProjectContext, error) {
	if !projectID.Valid {
		return h.resolveClaimProjectContexts(ctx, nil, workspaceID)
	}
	return h.resolveClaimProjectContexts(ctx, []pgtype.UUID{projectID}, workspaceID)
}

// resolveClaimChatProjectContext loads the project context of a chat turn: the
// session's whole project set, in selection order, so one conversation can
// carry several projects' descriptions and repositories (DENE-523).
//
// The set lives in chat_session_project. A session whose set is empty but
// whose legacy chat_session.project_id is still set (a row written by a server
// that predates the set, e.g. during a rolling deploy) degrades to that single
// project rather than silently losing context the session still names.
func (h *Handler) resolveClaimChatProjectContext(ctx context.Context, session db.ChatSession) (claimProjectContext, error) {
	projects, err := h.Queries.ListChatSessionProjectsInWorkspace(ctx, db.ListChatSessionProjectsInWorkspaceParams{
		ChatSessionID: session.ID,
		WorkspaceID:   session.WorkspaceID,
	})
	if err != nil {
		return claimProjectContext{}, fmt.Errorf("list chat session projects: %w", err)
	}
	projectIDs := make([]pgtype.UUID, 0, len(projects)+1)
	for _, p := range projects {
		projectIDs = append(projectIDs, p.ID)
	}
	if len(projectIDs) == 0 && session.ProjectID.Valid {
		projectIDs = append(projectIDs, session.ProjectID)
	}
	return h.resolveClaimProjectContexts(ctx, projectIDs, session.WorkspaceID)
}

// resolveClaimProjectContexts loads the project context for one daemon claim
// from the task's attached project set, in priority order.
//
// Every claim path (issue, chat, autopilot, quick-create) resolves the same
// thing from soft project references, so the tenant and failure rules live
// here once rather than in a copy per path:
//
//   - Every read is workspace-scoped. project_resource carries its own
//     workspace_id, so a corrupt project reference cannot lift another tenant's
//     repository URLs or local paths into a claim.
//   - A read FAILURE is not "no project". It returns an error so the caller can
//     preserve the task for redelivery; collapsing it into the workspace-repo
//     fallback is what lets a transient DB error silently run an agent against
//     the wrong repository (the same rule the chat-input load follows,
//     MUL-4351).
//   - A project that resolves to no row IS "no project": the reference is stale,
//     deleted, or points outside this workspace, and it drops out of the set.
//     Losing every project that way degrades the claim to workspace context.
//
// Repo precedence: project-bound github_repo resources override workspace repos
// when present. Mixing both would just confuse the agent — if a project
// explicitly attached its repos, those are the authoritative set. With no
// project, no github_repo resources, or only stale references, the workspace
// repos are the fallback.
func (h *Handler) resolveClaimProjectContexts(ctx context.Context, projectIDs []pgtype.UUID, workspaceID pgtype.UUID) (claimProjectContext, error) {
	var out claimProjectContext

	resolved := make([]db.Project, 0, len(projectIDs))
	for _, projectID := range projectIDs {
		if !projectID.Valid {
			continue
		}
		project, err := h.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{
			ID:          projectID,
			WorkspaceID: workspaceID,
		})
		switch {
		case err == nil:
			if !containsProjectID(resolved, project.ID) {
				resolved = append(resolved, project)
			}
		case errors.Is(err, pgx.ErrNoRows):
			// Stale/deleted/foreign reference: drop it from the set.
		default:
			return claimProjectContext{}, fmt.Errorf("get project: %w", err)
		}
	}

	if len(resolved) > 0 {
		ids := make([]pgtype.UUID, 0, len(resolved))
		for _, project := range resolved {
			ids = append(ids, project.ID)
		}
		rows, err := h.Queries.ListProjectResourcesForProjectsInWorkspace(ctx, db.ListProjectResourcesForProjectsInWorkspaceParams{
			WorkspaceID: workspaceID,
			ProjectIds:  ids,
		})
		if err != nil {
			return claimProjectContext{}, fmt.Errorf("list project resources: %w", err)
		}
		byProject := make(map[string][]db.ProjectResource, len(resolved))
		for _, row := range rows {
			key := uuidToString(row.ProjectID)
			byProject[key] = append(byProject[key], row)
		}
		for _, project := range resolved {
			resources, repos := projectResourcesForClaim(byProject[uuidToString(project.ID)])
			out.Projects = append(out.Projects, claimProject{
				ID:          uuidToString(project.ID),
				Title:       project.Title,
				Description: project.Description.String,
				Resources:   resources,
				Repos:       repos,
			})
		}
	}

	if out.hasRepos() {
		out.Repos = out.unionRepos()
		return out, nil
	}

	ws, err := h.Queries.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return claimProjectContext{}, fmt.Errorf("get workspace: %w", err)
	}
	if ws.Repos != nil {
		var repos []RepoData
		if jsonErr := json.Unmarshal(ws.Repos, &repos); jsonErr != nil {
			// Corrupt stored JSON is not transient: failing the claim would
			// wedge every claim in this workspace until someone repairs the
			// row. Degrade to no repos and leave a trail instead.
			slog.Error("claim project context: workspace repos are not valid JSON; claiming without repos",
				"workspace_id", uuidToString(workspaceID), "error", jsonErr)
		} else if len(repos) > 0 {
			out.Repos = repos
		}
	}
	return out, nil
}

// hasRepos reports whether any attached project contributed a repository.
func (c claimProjectContext) hasRepos() bool {
	for _, p := range c.Projects {
		if len(p.Repos) > 0 {
			return true
		}
	}
	return false
}

// unionRepos flattens every project's repos into the one list the daemon hands
// to `multica repo checkout`. A URL attached to two projects appears once — it
// is the same checkout either way, and the first project's ref wins because
// projects arrive in priority order.
func (c claimProjectContext) unionRepos() []RepoData {
	var repos []RepoData
	seen := make(map[string]struct{})
	for _, p := range c.Projects {
		for _, repo := range p.Repos {
			if _, ok := seen[repo.URL]; ok {
				continue
			}
			seen[repo.URL] = struct{}{}
			repos = append(repos, repo)
		}
	}
	return repos
}

// containsProjectID reports whether a project already resolved into the set —
// a chat session's set cannot repeat a project (unique index), but the legacy
// fallback path appends the primary column to a set that may already hold it.
func containsProjectID(projects []db.Project, id pgtype.UUID) bool {
	for _, project := range projects {
		if uuidToString(project.ID) == uuidToString(id) {
			return true
		}
	}
	return false
}

// projectResourcesForClaim maps resource rows onto the claim wire shape and
// lifts github_repo resources into the repo list so `multica repo checkout` and
// the meta-skill render them as the task's repos.
func projectResourcesForClaim(rows []db.ProjectResource) ([]ProjectResourceData, []RepoData) {
	if len(rows) == 0 {
		return nil, nil
	}
	resources := make([]ProjectResourceData, 0, len(rows))
	var repos []RepoData
	for _, row := range rows {
		label := ""
		if row.Label.Valid {
			label = row.Label.String
		}
		ref := json.RawMessage(row.ResourceRef)
		if len(ref) == 0 {
			ref = json.RawMessage("{}")
		}
		resources = append(resources, ProjectResourceData{
			ID:           uuidToString(row.ID),
			ResourceType: row.ResourceType,
			ResourceRef:  ref,
			Label:        label,
		})
		if row.ResourceType == "github_repo" {
			var payload struct {
				URL string `json:"url"`
				Ref string `json:"ref,omitempty"`
			}
			if json.Unmarshal(row.ResourceRef, &payload) == nil && payload.URL != "" {
				repos = append(repos, RepoData{URL: payload.URL, Ref: strings.TrimSpace(payload.Ref)})
			}
		}
	}
	return resources, repos
}
