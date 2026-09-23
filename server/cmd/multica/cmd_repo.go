package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/sparsecheckout"
	"github.com/spf13/cobra"
)

var repoCmd = &cobra.Command{
	Use:   "repo",
	Short: "Work with repositories",
}

var repoListCmd = &cobra.Command{
	Use:   "list",
	Short: "List workspace repositories",
	Long:  "Lists the repository registry for the current workspace. These are workspace-level repos, separate from project resources.",
	Args:  cobra.NoArgs,
	RunE:  runRepoList,
}

var repoAddCmd = &cobra.Command{
	Use:   "add [url]...",
	Short: "Add repositories to the workspace registry",
	Long: "Adds one or more repository URLs to the current workspace repository registry. " +
		"Existing URLs are not duplicated. Use project resources when you need project-specific context instead.",
	Args: cobra.ArbitraryArgs,
	RunE: runRepoAdd,
}

var repoRemoveCmd = &cobra.Command{
	Use:     "remove [url]...",
	Aliases: []string{"rm"},
	Short:   "Remove repositories from the workspace registry",
	Long:    "Removes one or more repository URLs from the current workspace repository registry.",
	Args:    cobra.ArbitraryArgs,
	RunE:    runRepoRemove,
}

var repoCheckoutCmd = &cobra.Command{
	Use:   "checkout <url>",
	Short: "Check out a repository into the working directory",
	Long: "Creates a git worktree from the daemon's bare clone cache. Used by agents to check out repos on demand.\n\n" +
		"Running it again where the repository is already checked out never silently discards work: a checkout " +
		"that has uncommitted changes, untracked files, or unpushed commits, or is already on this task's branch, " +
		"is kept as it is and only its remote refs are fetched. Pass --fresh to discard its uncommitted changes and " +
		"untracked files and start over on a new branch; commits stay on the old branch, but push any you still need first.",
	Args: exactArgs(1),
	RunE: runRepoCheckout,
}

var repoSparseAddCmd = &cobra.Command{
	Use:   "sparse-add <path>",
	Short: "Check out a path this task's sparse checkout left off disk",
	Long: "Brings one repository path into the current checkout when the task declared a sparse checkout and that path was not included.\n\n" +
		"If the path is not in the repository, the command says so. A missing file on disk is not the same thing. " +
		"In a checkout that already has the whole tree, the command does nothing and says that too.",
	Args: exactArgs(1),
	RunE: runRepoSparseAdd,
}

var (
	repoCheckoutRef   string
	repoCheckoutFresh bool
	repoCheckoutPaths string
	repoCheckoutFull  bool
)

func init() {
	repoListCmd.Flags().String("output", "table", "Output format: table or json")

	repoAddCmd.Flags().StringArray("url", nil, "Repository URL to add (may be repeated)")
	repoAddCmd.Flags().String("description", "", "Optional description; only valid when adding one URL")
	repoAddCmd.Flags().String("output", "json", "Output format: table or json")

	repoRemoveCmd.Flags().StringArray("url", nil, "Repository URL to remove (may be repeated)")
	repoRemoveCmd.Flags().String("output", "json", "Output format: table or json")

	repoCheckoutCmd.Flags().StringVar(&repoCheckoutRef, "ref", "", "branch, tag, or commit to check out instead of the remote default branch")
	repoCheckoutCmd.Flags().BoolVar(&repoCheckoutFresh, "fresh", false, "discard an existing checkout's uncommitted changes and untracked files and start over on a new branch from the latest default branch (or --ref); commits stay on the old branch")
	repoCheckoutCmd.Flags().StringVar(&repoCheckoutPaths, "paths", "", "comma-separated repository paths to check out instead of the task's checkout_paths declaration")
	repoCheckoutCmd.Flags().BoolVar(&repoCheckoutFull, "full", false, "check out the whole repository even when the task declared checkout paths")

	repoCmd.AddCommand(repoListCmd)
	repoCmd.AddCommand(repoAddCmd)
	repoCmd.AddCommand(repoRemoveCmd)
	repoCmd.AddCommand(repoCheckoutCmd)
	repoCmd.AddCommand(repoSparseAddCmd)
}

type workspaceRepo struct {
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
}

type repoWorkspaceResponse struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Slug  string          `json:"slug"`
	Repos []workspaceRepo `json:"repos"`
}

type repoMutationResult struct {
	WorkspaceID string          `json:"workspace_id"`
	Added       []workspaceRepo `json:"added,omitempty"`
	Updated     []workspaceRepo `json:"updated,omitempty"`
	Removed     []workspaceRepo `json:"removed,omitempty"`
	Repos       []workspaceRepo `json:"repos"`
}

func repoURLsFromArgsAndFlags(cmd *cobra.Command, args []string) ([]string, error) {
	flagURLs, _ := cmd.Flags().GetStringArray("url")
	raw := append([]string{}, flagURLs...)
	raw = append(raw, args...)
	if len(raw) == 0 {
		return nil, fmt.Errorf("at least one repository URL is required")
	}

	urls := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, u := range raw {
		u = strings.TrimSpace(u)
		if u == "" {
			return nil, fmt.Errorf("repository URL cannot be empty")
		}
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		urls = append(urls, u)
	}
	return urls, nil
}

func fetchRepoWorkspace(ctx context.Context, client *cli.APIClient, workspaceID string) (repoWorkspaceResponse, error) {
	var ws repoWorkspaceResponse
	if err := client.GetJSON(ctx, "/api/workspaces/"+workspaceID, &ws); err != nil {
		return repoWorkspaceResponse{}, fmt.Errorf("get workspace: %w", err)
	}
	if ws.Repos == nil {
		ws.Repos = []workspaceRepo{}
	}
	return ws, nil
}

func patchWorkspaceRepos(ctx context.Context, client *cli.APIClient, workspaceID string, repos []workspaceRepo) (repoWorkspaceResponse, error) {
	var ws repoWorkspaceResponse
	if err := client.PatchJSON(ctx, "/api/workspaces/"+workspaceID, map[string]any{"repos": repos}, &ws); err != nil {
		return repoWorkspaceResponse{}, fmt.Errorf("update workspace repos: %w", err)
	}
	if ws.Repos == nil {
		ws.Repos = []workspaceRepo{}
	}
	return ws, nil
}

func repoCommandClient(cmd *cobra.Command) (*cli.APIClient, string, error) {
	workspaceID, err := requireWorkspaceID(cmd)
	if err != nil {
		return nil, "", err
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, "", err
	}
	return client, workspaceID, nil
}

func runRepoList(cmd *cobra.Command, _ []string) error {
	client, workspaceID, err := repoCommandClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	ws, err := fetchRepoWorkspace(ctx, client, workspaceID)
	if err != nil {
		return err
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, ws.Repos)
	}
	if len(ws.Repos) == 0 {
		fmt.Fprintln(os.Stderr, "No repositories found.")
		return nil
	}
	rows := make([][]string, 0, len(ws.Repos))
	for _, repo := range ws.Repos {
		rows = append(rows, []string{repo.URL, repo.Description})
	}
	cli.PrintTable(os.Stdout, []string{"URL", "DESCRIPTION"}, rows)
	return nil
}

func runRepoAdd(cmd *cobra.Command, args []string) error {
	urls, err := repoURLsFromArgsAndFlags(cmd, args)
	if err != nil {
		return err
	}
	description, _ := cmd.Flags().GetString("description")
	descriptionChanged := cmd.Flags().Changed("description")
	if descriptionChanged && len(urls) > 1 {
		return fmt.Errorf("--description can only be used when adding one repository URL")
	}

	client, workspaceID, err := repoCommandClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	ws, err := fetchRepoWorkspace(ctx, client, workspaceID)
	if err != nil {
		return err
	}

	indexByURL := make(map[string]int, len(ws.Repos))
	for i, repo := range ws.Repos {
		indexByURL[repo.URL] = i
	}

	added := []workspaceRepo{}
	updated := []workspaceRepo{}
	repos := append([]workspaceRepo{}, ws.Repos...)
	for _, u := range urls {
		if idx, ok := indexByURL[u]; ok {
			if descriptionChanged && repos[idx].Description != description {
				repos[idx].Description = description
				updated = append(updated, repos[idx])
			}
			continue
		}
		repo := workspaceRepo{URL: u}
		if descriptionChanged {
			repo.Description = description
		}
		indexByURL[u] = len(repos)
		repos = append(repos, repo)
		added = append(added, repo)
	}

	if len(added) > 0 || len(updated) > 0 {
		ws, err = patchWorkspaceRepos(ctx, client, workspaceID, repos)
		if err != nil {
			return err
		}
	} else {
		ws.Repos = repos
	}

	result := repoMutationResult{
		WorkspaceID: ws.ID,
		Added:       added,
		Updated:     updated,
		Repos:       ws.Repos,
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	if len(added) == 0 && len(updated) == 0 {
		fmt.Fprintln(os.Stdout, "No repository changes.")
		return nil
	}
	rows := make([][]string, 0, len(added)+len(updated))
	for _, repo := range added {
		rows = append(rows, []string{"added", repo.URL, repo.Description})
	}
	for _, repo := range updated {
		rows = append(rows, []string{"updated", repo.URL, repo.Description})
	}
	cli.PrintTable(os.Stdout, []string{"ACTION", "URL", "DESCRIPTION"}, rows)
	return nil
}

func runRepoRemove(cmd *cobra.Command, args []string) error {
	urls, err := repoURLsFromArgsAndFlags(cmd, args)
	if err != nil {
		return err
	}

	client, workspaceID, err := repoCommandClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	ws, err := fetchRepoWorkspace(ctx, client, workspaceID)
	if err != nil {
		return err
	}

	removeSet := make(map[string]struct{}, len(urls))
	for _, u := range urls {
		removeSet[u] = struct{}{}
	}
	removedSet := make(map[string]struct{}, len(urls))
	removed := []workspaceRepo{}
	repos := make([]workspaceRepo, 0, len(ws.Repos))
	for _, repo := range ws.Repos {
		if _, ok := removeSet[repo.URL]; ok {
			removed = append(removed, repo)
			removedSet[repo.URL] = struct{}{}
			continue
		}
		repos = append(repos, repo)
	}
	missing := []string{}
	for _, u := range urls {
		if _, ok := removedSet[u]; !ok {
			missing = append(missing, u)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("repository not found in workspace registry: %s", strings.Join(missing, ", "))
	}

	ws, err = patchWorkspaceRepos(ctx, client, workspaceID, repos)
	if err != nil {
		return err
	}

	result := repoMutationResult{
		WorkspaceID: ws.ID,
		Removed:     removed,
		Repos:       ws.Repos,
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	rows := make([][]string, 0, len(removed))
	for _, repo := range removed {
		rows = append(rows, []string{repo.URL, repo.Description})
	}
	cli.PrintTable(os.Stdout, []string{"REMOVED URL", "DESCRIPTION"}, rows)
	return nil
}

func runRepoCheckout(cmd *cobra.Command, args []string) error {
	repoURL := args[0]

	daemonPort := os.Getenv("MULTICA_DAEMON_PORT")
	if daemonPort == "" {
		return fmt.Errorf("MULTICA_DAEMON_PORT not set (this command is intended to be run by an agent inside a daemon task)")
	}

	workspaceID := os.Getenv("MULTICA_WORKSPACE_ID")
	agentName := os.Getenv("MULTICA_AGENT_NAME")
	taskID := os.Getenv("MULTICA_TASK_ID")
	taskToken := os.Getenv("MULTICA_TOKEN")
	if taskToken == "" {
		return fmt.Errorf("MULTICA_TOKEN not set (repo checkout requires the active task credential)")
	}

	// Use current working directory as the checkout target.
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	reqBody := map[string]any{
		"url":           repoURL,
		"workspace_id":  workspaceID,
		"workdir":       workDir,
		"ref":           repoCheckoutRef,
		"agent_name":    agentName,
		"task_id":       taskID,
		"checkout_mode": strings.TrimSpace(os.Getenv("MULTICA_REPO_CHECKOUT_MODE")),
		"retry_busy":    true,
		"fresh":         repoCheckoutFresh,
	}
	if repoCheckoutFull {
		reqBody["sparse_paths_set"] = true
		reqBody["sparse_paths"] = []string{}
	} else if strings.TrimSpace(repoCheckoutPaths) != "" {
		scope, err := sparsecheckout.Parse(repoCheckoutPaths)
		if err != nil {
			return fmt.Errorf("checkout paths: %w", err)
		}
		reqBody["sparse_paths_set"] = true
		reqBody["sparse_paths"] = scope.Paths
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	parentCtx := cmd.Context()
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(parentCtx, 5*time.Minute)
	defer cancel()
	client := &http.Client{}
	checkoutURL := fmt.Sprintf("http://127.0.0.1:%s/repo/checkout", daemonPort)
	var body []byte
	// lastWait is the daemon's latest explanation of why the checkout is not
	// ready. It becomes the error if the wait runs out, so a timeout says what
	// was being waited on instead of a bare "deadline exceeded".
	var lastWait string
	var lastProgressAt time.Time
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, checkoutURL, bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("create daemon checkout request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+taskToken)
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil && lastWait != "" {
				return fmt.Errorf("checkout is not ready yet, gave up waiting: %s", lastWait)
			}
			return fmt.Errorf("connect to daemon: %w", err)
		}
		body, err = io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()
		if err != nil {
			return fmt.Errorf("read daemon checkout response: %w", err)
		}
		if closeErr != nil {
			return fmt.Errorf("close daemon checkout response: %w", closeErr)
		}
		if resp.StatusCode == http.StatusServiceUnavailable && resp.Header.Get("X-Multica-Retryable") == "repo-busy" {
			lastWait = strings.TrimSpace(string(body))
			// A first-time download can take far longer than this command
			// waits. Show how far it is so the agent sees a moving download,
			// not a hang.
			if resp.Header.Get("X-Multica-Repo-Building") != "" && time.Since(lastProgressAt) >= repoCheckoutProgressInterval {
				fmt.Fprintf(os.Stderr, "Waiting: %s\n", lastWait)
				lastProgressAt = time.Now()
			}
			delay := repoCheckoutRetryDelay(resp.Header.Get("Retry-After"), time.Now())
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return fmt.Errorf("checkout is not ready yet, gave up waiting: %s", lastWait)
			case <-timer.C:
				continue
			}
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("checkout failed: %s", string(body))
		}
		break
	}

	var result repoCheckoutResult
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	fmt.Fprintf(os.Stdout, "%s\n", result.Path)
	fmt.Fprintln(os.Stderr, repoCheckoutSummary(repoURL, result))
	if warning := repoCheckoutStaleWarning(repoURL, result); warning != "" {
		fmt.Fprintln(os.Stderr, warning)
	}

	return nil
}

// repoCheckoutProgressInterval spaces out the progress lines printed while
// waiting on a first-time download.
const repoCheckoutProgressInterval = 30 * time.Second

// repoCheckoutStaleWarning is printed when the daemon could not refresh the
// repository before the checkout. The checkout still succeeded, which is
// exactly why it must be said out loud: without it the agent builds on an old
// base and nothing anywhere on the task says so.
func repoCheckoutStaleWarning(repoURL string, result repoCheckoutResult) string {
	if !result.Stale {
		return ""
	}
	reason := strings.TrimSpace(result.StaleReason)
	if reason == "" {
		reason = "no reason reported"
	}
	return fmt.Sprintf("WARNING: the code in %s may be OUT OF DATE. Fetching the latest %s failed (%s), so this checkout was built from what the daemon had cached earlier.\n"+
		"Before relying on it, run `git fetch origin` inside the checkout and compare with the remote branch; if that also fails, say in your result that the work is based on a possibly stale revision.",
		result.Path, repoURL, reason)
}

// repoCheckoutResult is the daemon's /repo/checkout response. Daemons older
// than MUL-7284 never keep an existing checkout and omit Kept and the counts.
type repoCheckoutResult struct {
	Path             string `json:"path"`
	BranchName       string `json:"branch_name"`
	Kept             string `json:"kept"`
	UncommittedFiles int    `json:"uncommitted_files"`
	UnpushedCommits  int    `json:"unpushed_commits"`
	// Source is "local_directory" when the daemon answered from the directory
	// the project pinned on this machine rather than cloning (DENE-595). Older
	// daemons omit it; the generic summary they get is still true, since Path
	// is where the code is either way.
	Source string `json:"source,omitempty"`
	// ExecutionMode is the pinned resource's execution mode. It decides whose
	// checkout Path is: the user's own in in_place and shared, this task's
	// private worktree in worktree mode. Daemons older than DENE-595 omit it.
	ExecutionMode string `json:"execution_mode,omitempty"`
	// Stale reports that the fetch before the checkout failed, so the code may
	// be behind the remote; StaleReason is the fetch error. Daemons older than
	// DENE-598 omit both and only log the failure.
	Stale       bool   `json:"stale,omitempty"`
	StaleReason string `json:"stale_reason,omitempty"`
	// SparsePaths is the declaration the daemon applied. SparseSkipped is
	// "kept" when an existing checkout was left as it was, so the files were
	// not removed.
	SparsePaths   string `json:"sparse_paths,omitempty"`
	SparseSkipped string `json:"sparse_skipped,omitempty"`
}

// repoCheckoutSourceLocalDirectory mirrors the daemon-side constant.
const repoCheckoutSourceLocalDirectory = "local_directory"

// repoCheckoutModeWorktree mirrors the daemon-side execution mode in which the
// task runs in its own worktree rather than the user's directory.
const repoCheckoutModeWorktree = "worktree"

// repoCheckoutSummary says what the checkout did. A kept checkout has to read
// differently from a new branch off the default branch, or the agent works on
// as if the checkout were fresh and loses track of what it holds.
func repoCheckoutSummary(repoURL string, result repoCheckoutResult) string {
	if result.Source == repoCheckoutSourceLocalDirectory {
		// Say plainly that nothing was cloned. An agent told only "checked
		// out <path>" would reasonably assume a fresh tree and start by
		// resetting it — in the user's own working copy.
		branch := result.BranchName
		if branch == "" {
			branch = "detached HEAD"
		}
		// Whose checkout this is changes what the agent may do in it, so the
		// two cases must not share a sentence. Saying "the user's own checkout"
		// about a worktree invites the agent to treat the user's working copy
		// as off-limits when it is not even the directory it was handed — and
		// the reverse mistake, in in_place, is worse.
		ownership := "This is the user's own checkout: it may carry uncommitted work, and nothing here was reset, cleaned, or switched."
		if result.ExecutionMode == repoCheckoutModeWorktree {
			ownership = "This is this task's own git worktree of that directory, not the user's working copy: commit here and deliver the branch."
		}
		return fmt.Sprintf("Using the local directory this project is configured with: %s (branch: %s).\n"+
			"%s was NOT cloned — this machine already holds it, and a second copy is what the local-directory setting exists to avoid.\n"+
			"%s",
			result.Path, branch, repoURL, ownership)
	}
	if result.Kept == "" {
		return fmt.Sprintf("Checked out %s → %s (branch: %s)%s", repoURL, result.Path, result.BranchName, sparseCheckoutNote(result))
	}
	branch := result.BranchName
	if branch == "" {
		branch = "detached HEAD"
	}
	if result.Kept == "task_branch" {
		branch += ", this task's branch"
	}
	keptAction := "nothing was reset, cleaned, or switched; only remote refs were fetched."
	switch result.SparseSkipped {
	case sparsecheckout.SkippedRestored:
		keptAction = "the branch was left as it was, and sparse checkout was turned off so the whole repository is on disk again."
	case sparsecheckout.SkippedWidened:
		keptAction = "the branch was left as it was, and the sparse checkout was widened so the newly declared directories are on disk."
	}
	return fmt.Sprintf("Kept the existing checkout of %s at %s (branch: %s; %d uncommitted file%s, %d unpushed commit%s): "+
		"%s\n"+
		"To discard its uncommitted changes and untracked files and start over on a new branch from the latest default branch (or --ref), "+
		"re-run with --fresh; commits stay on the old branch, but push any you still need first.%s",
		repoURL, result.Path, branch,
		result.UncommittedFiles, pluralS(result.UncommittedFiles),
		result.UnpushedCommits, pluralS(result.UnpushedCommits),
		keptAction,
		sparseCheckoutNote(result))
}

func sparseCheckoutNote(result repoCheckoutResult) string {
	switch result.SparseSkipped {
	case sparsecheckout.SkippedRestored:
		return "\nThe previous checkout was sparse. Nothing was declared this time, so every file in the repository is on disk now."
	case sparsecheckout.SkippedWidened:
		return "\nSparse checkout now includes " + result.SparsePaths + " plus root project files. Directories left out before are on disk."
	case sparsecheckout.SkippedKept:
		if strings.TrimSpace(result.SparsePaths) == "" {
			return ""
		}
		return "\nThis task declared a sparse checkout (" + result.SparsePaths + ") but the existing checkout was kept, so its files were not removed. Re-run with --fresh to apply it; that discards uncommitted changes."
	}
	if strings.TrimSpace(result.SparsePaths) == "" {
		return ""
	}
	return "\nSparse checkout: only " + result.SparsePaths + " plus root project files are on disk. A missing file may still be in the repository; run `multica repo sparse-add <path>` before concluding it does not exist."
}

func runRepoSparseAdd(cmd *cobra.Command, args []string) error {
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	msg, err := sparsecheckout.Materialize(parent, workDir, args[0])
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, msg)
	return nil
}

func repoCheckoutRetryDelay(value string, now time.Time) time.Duration {
	const (
		defaultDelay = time.Second
		maxDelay     = 30 * time.Second
	)
	value = strings.TrimSpace(value)
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return min(time.Duration(seconds)*time.Second, maxDelay)
	}
	if retryAt, err := http.ParseTime(value); err == nil {
		return min(max(retryAt.Sub(now), time.Duration(0)), maxDelay)
	}
	return defaultDelay
}
