// Package repocache manages bare git clone caches for workspace repositories.
// The daemon uses these caches as the source for creating per-task worktrees.
package repocache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/processtree"
	"github.com/multica-ai/multica/server/internal/sparsecheckout"
)

// gitEnv returns an environment for git subprocesses that contact remotes.
// It passes the full daemon environment so credential helpers (e.g. gh) can
// locate their config, and disables TTY prompting so auth failures produce
// clear errors instead of blocking on a non-existent terminal.
//
// safe.directory=* is set via GIT_CONFIG_* env vars so git trusts all
// directories regardless of ownership. The daemon manages its own bare
// caches and worktrees, so the ownership check adds no security value
// and breaks CI environments where the runner UID differs from the
// directory owner.
func gitEnv() []string {
	base := os.Environ()

	// Find the existing GIT_CONFIG_COUNT so we append at the next index
	// rather than overwriting any env-scoped git config (auth, URL
	// rewrites, extra headers, etc.).
	existing := 0
	for _, e := range base {
		if strings.HasPrefix(e, "GIT_CONFIG_COUNT=") {
			if n, err := strconv.Atoi(strings.TrimPrefix(e, "GIT_CONFIG_COUNT=")); err == nil {
				existing = n
			}
		}
	}

	idx := strconv.Itoa(existing)
	return append(base,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_COUNT="+strconv.Itoa(existing+1),
		"GIT_CONFIG_KEY_"+idx+"=safe.directory",
		"GIT_CONFIG_VALUE_"+idx+"=*",
	)
}

var agentGitExcludePatterns = []string{
	".agent_context",
	"CLAUDE.md",
	"AGENTS.md",
	".claude",
	".opencode",
	".codeartsdoer",
	".deveco",
	"CODEBUDDY.md",
	".codebuddy",
	".pi",
	".omp",
}

// DefaultGitTimeout bounds a single git subprocess started by this package
// unless SetGitTimeout overrides it.
const DefaultGitTimeout = 10 * time.Minute

// gitTimeoutNanos holds the per-command git timeout. It is package state rather
// than a Cache field because the git helpers are free functions shared with
// callers that have no Cache at hand.
var gitTimeoutNanos atomic.Int64

// SetGitTimeout overrides the per-command git timeout. Non-positive values
// restore DefaultGitTimeout. A cold cache is built in bounded slices (see
// bootstrapBareContext), so this bounds one slice, not the whole download.
func SetGitTimeout(d time.Duration) {
	if d <= 0 {
		d = DefaultGitTimeout
	}
	gitTimeoutNanos.Store(int64(d))
}

func gitTimeout() time.Duration {
	if n := gitTimeoutNanos.Load(); n > 0 {
		return time.Duration(n)
	}
	return DefaultGitTimeout
}

// gitBinaryKey carries the git executable a call tree should run. Tests use it
// to put a fake git in front of one Cache call without touching the process
// PATH, which every other goroutine in the process shares.
type gitBinaryKey struct{}

func gitBinary(ctx context.Context) string {
	if path, ok := ctx.Value(gitBinaryKey{}).(string); ok && path != "" {
		return path
	}
	return "git"
}

func newGitCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.Command(gitBinary(ctx), args...)
	// A daemon can outlive the checkout it was launched from. Run Git from the
	// filesystem root instead of inheriting a cwd that may have been deleted.
	cmd.Dir = filepath.VolumeName(os.TempDir()) + string(os.PathSeparator)
	cmd.Env = gitEnv()
	return cmd
}

func runGitCombinedOutput(args ...string) ([]byte, error) {
	return runGitCombinedOutputContext(context.Background(), args...)
}

func runGitCombinedOutputContext(ctx context.Context, args ...string) ([]byte, error) {
	return runGitCombinedOutputWithTimeoutContext(ctx, gitTimeout(), args...)
}

func runGitCombinedOutputWithTimeout(timeout time.Duration, args ...string) ([]byte, error) {
	return runGitCombinedOutputWithTimeoutContext(context.Background(), timeout, args...)
}

func runGitCombinedOutputWithTimeoutContext(parent context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := newGitCommand(ctx, args...)
	out, err := processtree.CombinedOutput(ctx, cmd, 5*time.Second)
	if ctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("git command timed out after %s: %w", timeout, ctx.Err())
	}
	return out, err
}

// runGitCombinedOutputStdinContext is runGitCombinedOutputContext for commands
// that read their input from stdin.
func runGitCombinedOutputStdinContext(parent context.Context, stdin string, args ...string) ([]byte, error) {
	timeout := gitTimeout()
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := newGitCommand(ctx, args...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := processtree.CombinedOutput(ctx, cmd, 5*time.Second)
	if ctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("git command timed out after %s: %w", timeout, ctx.Err())
	}
	return out, err
}

// sliceTimedOut reports whether a git command hit its own per-command timeout
// while the caller is still willing to wait, i.e. the slice was too big for
// the link rather than the whole operation being cancelled.
func sliceTimedOut(parent context.Context, err error) bool {
	return parent.Err() == nil && errors.Is(err, context.DeadlineExceeded)
}

func runGitOutput(args ...string) ([]byte, error) {
	return runGitOutputContext(context.Background(), args...)
}

func runGitOutputContext(ctx context.Context, args ...string) ([]byte, error) {
	return runGitOutputWithTimeoutContext(ctx, gitTimeout(), args...)
}

func runGitOutputWithTimeout(timeout time.Duration, args ...string) ([]byte, error) {
	return runGitOutputWithTimeoutContext(context.Background(), timeout, args...)
}

func runGitOutputWithTimeoutContext(parent context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := newGitCommand(ctx, args...)
	out, err := processtree.Output(ctx, cmd, 5*time.Second)
	if ctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("git command timed out after %s: %w", timeout, ctx.Err())
	}
	return out, err
}

func runGit(args ...string) error {
	return runGitContext(context.Background(), args...)
}

func runGitContext(ctx context.Context, args ...string) error {
	return runGitWithTimeoutContext(ctx, gitTimeout(), args...)
}

func runGitWithTimeout(timeout time.Duration, args ...string) error {
	return runGitWithTimeoutContext(context.Background(), timeout, args...)
}

func runGitWithTimeoutContext(parent context.Context, timeout time.Duration, args ...string) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := newGitCommand(ctx, args...)
	err := processtree.Run(ctx, cmd, 5*time.Second)
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("git command timed out after %s: %w", timeout, ctx.Err())
	}
	return err
}

// RepoInfo describes a repository to cache.
type RepoInfo struct {
	URL string
}

// CachedRepo describes a cached bare clone ready for worktree creation.
type CachedRepo struct {
	URL       string // remote URL
	LocalPath string // absolute path to the bare clone
}

// Cache manages bare git clones for workspace repositories.
type Cache struct {
	root          string // base directory for all caches (e.g. ~/multica_workspaces/.repos)
	logger        *slog.Logger
	fetchCooldown time.Duration
	// repoLocks maps bare repo path → dedicated mutex. Any mutating operation
	// on a given bare repo (clone, fetch, worktree add, ref update) must
	// hold its lock — git's own lockfiles (packed-refs.lock, config.lock,
	// worktree admin dirs) don't tolerate parallel mutations on the same
	// repo. Separate repos are independent and run concurrently.
	repoLocks sync.Map // barePath -> *repoLock

	// builds tracks the first-time downloads running in this process, so a
	// second caller joins the one in flight instead of queueing behind it and
	// a waiting checkout can be told how far along it is.
	buildsMu sync.Mutex
	builds   map[string]*buildTracker // barePath -> in-flight download
}

// ErrRepoBusy means a foreground checkout could not acquire its repository
// within the caller's bounded wait. Callers that advertised retry support can
// turn this into a retryable HTTP response instead of waiting until their
// transport deadline expires.
var ErrRepoBusy = errors.New("repository is busy")

// ErrRepoBuilding means the repository's first-time cache has not finished
// downloading. It is not a failure and not corruption: the slices fetched so
// far are kept, and the download resumes from them. Match it with errors.Is;
// the concrete *RepoBuildingError carries the progress.
var ErrRepoBuilding = errors.New("repository's first-time cache is still downloading")

// RepoBuildingError is ErrRepoBuilding with the progress known at the time.
type RepoBuildingError struct {
	URL    string
	Status BuildStatus
}

func (e *RepoBuildingError) Error() string {
	return fmt.Sprintf("the first-time cache of %s is not finished yet: %s. "+
		"Nothing is broken and nothing needs deleting: the cache is not corrupted, the part already downloaded is kept, "+
		"and deleting it would only restart the download from zero. Run the same checkout again to keep waiting",
		e.URL, e.Status.Describe(time.Now()))
}

func (e *RepoBuildingError) Is(target error) bool { return target == ErrRepoBuilding }

// Build phases, in the order a first-time download goes through them.
const (
	BuildPhaseSnapshot = "snapshot" // depth-1 snapshot of every branch
	BuildPhaseHistory  = "history"  // deepen slices back to full history
	BuildPhaseFiles    = "files"    // default branch file contents (blobless only)
)

// BuildStatus is how far a first-time download has got.
type BuildStatus struct {
	// Active is false when the cache directory is unfinished but nothing in
	// this process is downloading it (an interrupted download awaiting resume).
	Active bool
	Phase  string
	// Done and Total count history slices in BuildPhaseHistory (Total unknown,
	// left 0) and files in BuildPhaseFiles.
	Done, Total    int
	StartedAt      time.Time
	PhaseStartedAt time.Time
}

// Remaining estimates the time left. Only the files phase has a known total;
// git does not say how much history is left, and a guess there would be the
// same kind of lie this status exists to replace.
func (s BuildStatus) Remaining(now time.Time) (time.Duration, bool) {
	if !s.Active || s.Phase != BuildPhaseFiles || s.Done <= 0 || s.Total <= s.Done {
		return 0, false
	}
	elapsed := now.Sub(s.PhaseStartedAt)
	if elapsed <= 0 {
		return 0, false
	}
	return time.Duration(float64(elapsed) / float64(s.Done) * float64(s.Total-s.Done)), true
}

// Describe renders the status as one clause for an error or a progress line.
func (s BuildStatus) Describe(now time.Time) string {
	if !s.Active {
		return "an earlier download was interrupted and resumes from where it stopped"
	}
	running := now.Sub(s.StartedAt).Round(time.Second)
	switch s.Phase {
	case BuildPhaseHistory:
		return fmt.Sprintf("downloading history, step 2 of 3 (%d slice%s fetched, running for %s; git does not report how much history is left)",
			s.Done, pluralSuffix(s.Done), running)
	case BuildPhaseFiles:
		if left, ok := s.Remaining(now); ok {
			return fmt.Sprintf("downloading files, step 3 of 3 (%d of %d, about %s left)", s.Done, s.Total, left.Round(time.Second))
		}
		return fmt.Sprintf("downloading files, step 3 of 3 (%d of %d)", s.Done, s.Total)
	default:
		return fmt.Sprintf("downloading the first snapshot, step 1 of 3 (running for %s)", running)
	}
}

func pluralSuffix(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// buildTracker is one in-flight first-time download. done closes when it ends;
// err is valid only after that.
type buildTracker struct {
	mu     sync.Mutex
	status BuildStatus
	done   chan struct{}
	err    error
}

func (t *buildTracker) report(phase string, done, total int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status.Phase != phase {
		t.status.Phase = phase
		t.status.PhaseStartedAt = time.Now()
	}
	t.status.Done, t.status.Total = done, total
}

func (t *buildTracker) snapshot() BuildStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.status
}

// beginOrJoinBuild returns the in-flight download for barePath, or registers a
// new one and reports owner=true. The owner must call endBuild.
func (c *Cache) beginOrJoinBuild(barePath string) (t *buildTracker, owner bool) {
	c.buildsMu.Lock()
	defer c.buildsMu.Unlock()
	if t, ok := c.builds[barePath]; ok {
		return t, false
	}
	now := time.Now()
	t = &buildTracker{
		status: BuildStatus{Active: true, Phase: BuildPhaseSnapshot, StartedAt: now, PhaseStartedAt: now},
		done:   make(chan struct{}),
	}
	if c.builds == nil {
		c.builds = make(map[string]*buildTracker)
	}
	c.builds[barePath] = t
	return t, true
}

func (c *Cache) endBuild(barePath string, t *buildTracker, err error) {
	c.buildsMu.Lock()
	delete(c.builds, barePath)
	c.buildsMu.Unlock()
	t.err = err
	close(t.done)
}

// BuildInProgress reports whether the repository's cache is unfinished. ok is
// false for a ready cache and for a repository never seen. done is non-nil only
// while a download is running in this process, and closes when it ends.
func (c *Cache) BuildInProgress(workspaceID, url string) (status BuildStatus, done <-chan struct{}, ok bool) {
	barePath := c.BarePath(workspaceID, url)
	c.buildsMu.Lock()
	t := c.builds[barePath]
	c.buildsMu.Unlock()
	if t != nil {
		return t.snapshot(), t.done, true
	}
	if !IsReady(barePath) && isBareRepo(barePath) {
		return BuildStatus{}, nil, true
	}
	return BuildStatus{}, nil, false
}

// Activity is the path-free repository coordination state exposed through the
// daemon health endpoint. It is diagnostic only.
type Activity struct {
	MaintenanceActive int
	ForegroundWaiters int
}

// repoLock is a foreground-priority mutex. Ordinary cache mutations serialize
// exactly as they did with sync.Mutex. Low-priority maintenance is different:
// it only starts on an idle repository and receives a context that is cancelled
// as soon as a foreground operation queues. The maintenance holder remains
// responsible for stopping its Git process tree before unlocking.
type repoLock struct {
	mu                sync.Mutex
	held              bool
	maintenance       bool
	maintenanceCancel context.CancelCauseFunc
	foregroundWaiters int
	changed           chan struct{}
}

// ErrMaintenancePreempted is the cancellation cause delivered to a running
// maintenance callback when foreground work needs the same repository.
var ErrMaintenancePreempted = errors.New("repository maintenance preempted by foreground work")

func newRepoLock() *repoLock {
	return &repoLock{changed: make(chan struct{})}
}

func (l *repoLock) LockContext(ctx context.Context) error {
	l.mu.Lock()
	l.foregroundWaiters++
	for {
		if err := ctx.Err(); err != nil {
			l.foregroundWaiters--
			l.signalLocked()
			l.mu.Unlock()
			return context.Cause(ctx)
		}
		if l.maintenanceCancel != nil {
			l.maintenanceCancel(ErrMaintenancePreempted)
		}
		if !l.held {
			l.held = true
			l.maintenance = false
			l.foregroundWaiters--
			l.mu.Unlock()
			return nil
		}
		changed := l.changed
		l.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		}
		l.mu.Lock()
	}
}

func (l *repoLock) Lock() {
	_ = l.LockContext(context.Background())
}

func (l *repoLock) Unlock() {
	l.mu.Lock()
	if !l.held {
		l.mu.Unlock()
		panic("repocache: unlock of unlocked repository")
	}
	if l.maintenanceCancel != nil {
		l.maintenanceCancel(nil)
		l.maintenanceCancel = nil
	}
	l.held = false
	l.maintenance = false
	l.signalLocked()
	l.mu.Unlock()
}

func (l *repoLock) tryLockMaintenance(parent context.Context) (context.Context, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if parent.Err() != nil || l.held || l.foregroundWaiters > 0 {
		return nil, false
	}
	ctx, cancel := context.WithCancelCause(parent)
	l.held = true
	l.maintenance = true
	l.maintenanceCancel = cancel
	return ctx, true
}

func (l *repoLock) signalLocked() {
	close(l.changed)
	l.changed = make(chan struct{})
}

func (l *repoLock) activity() (maintenance bool, waiters int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.held && l.maintenance, l.foregroundWaiters
}

func (l *repoLock) cancelMaintenanceAndWait() {
	l.mu.Lock()
	for l.maintenance {
		if l.maintenanceCancel != nil {
			l.maintenanceCancel(ErrMaintenancePreempted)
		}
		changed := l.changed
		l.mu.Unlock()
		<-changed
		l.mu.Lock()
	}
	l.mu.Unlock()
}

// New creates a new repo cache rooted at the given directory.
func New(root string, logger *slog.Logger) *Cache {
	// The daemon supplies its configured default through SetFetchCooldown.
	// Keeping the library constructor uncoupled preserves the explicit Fetch
	// semantics expected by callers that use Cache directly (including tests).
	return &Cache{root: root, logger: logger}
}

// SetFetchCooldown changes the cache-wide fetch de-duplication window.
// Non-positive values disable the window and fetch every time.
func (c *Cache) SetFetchCooldown(d time.Duration) {
	c.fetchCooldown = d
}

// lockForRepo returns the mutex dedicated to the given bare repo path. See
// the Cache.repoLocks field comment for semantics.
func (c *Cache) lockForRepo(barePath string) *repoLock {
	if l, ok := c.repoLocks.Load(barePath); ok {
		return l.(*repoLock)
	}
	newLock := newRepoLock()
	actual, _ := c.repoLocks.LoadOrStore(barePath, newLock)
	return actual.(*repoLock)
}

// Activity reports aggregate repository coordination without exposing cache
// paths. It is safe to call while operations are starting or finishing.
func (c *Cache) Activity() Activity {
	var activity Activity
	c.repoLocks.Range(func(_, value any) bool {
		maintenance, waiters := value.(*repoLock).activity()
		if maintenance {
			activity.MaintenanceActive++
		}
		activity.ForegroundWaiters += waiters
		return true
	})
	return activity
}

// CancelMaintenance stops every active low-priority repository operation and
// waits for it to release repository ownership. Task dispatch uses this as a
// barrier before launching an agent because a task may reuse an existing
// worktree and never pass through CreateWorktree's gate. Waiting also ensures
// interrupted-maintenance lock cleanup completes before direct agent Git work
// can create its own lock files.
func (c *Cache) CancelMaintenance() {
	c.repoLocks.Range(func(_, value any) bool {
		value.(*repoLock).cancelMaintenanceAndWait()
		return true
	})
}

// Sync ensures all repos for a workspace are cloned (or fetched if already cached).
// Repos no longer in the list are left in place (cheap to keep, avoids re-cloning
// if a repo is temporarily removed and re-added).
//
// Per-repo mutation serializes against CreateWorktree on the same bare path
// via lockForRepo. Different repos are independent and run concurrently, so one
// slow or stuck repository never delays the rest of the list.
func (c *Cache) Sync(workspaceID string, repos []RepoInfo) error {
	return c.SyncContext(context.Background(), workspaceID, repos)
}

// syncConcurrency bounds how many repositories one Sync call works on at once.
// It caps the daemon's network and disk fan-out on a workspace with many
// repositories; it is not a fairness mechanism.
const syncConcurrency = 4

// SyncContext is Sync with cancellation propagated through repo lock waits,
// clone, fetch, and ref-layout migration. It returns the error of the earliest
// failing repository in list order, so the result does not depend on timing.
func (c *Cache) SyncContext(ctx context.Context, workspaceID string, repos []RepoInfo) error {
	wsDir := filepath.Join(c.root, workspaceID)
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		return fmt.Errorf("create workspace cache dir: %w", err)
	}

	errs := make([]error, len(repos))
	sem := make(chan struct{}, syncConcurrency)
	var wg sync.WaitGroup
	for i, repo := range repos {
		if repo.URL == "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				errs[i] = context.Cause(ctx)
				return
			}
			errs[i] = c.syncRepoContext(ctx, repo.URL, filepath.Join(wsDir, bareDirName(repo.URL)))
		}()
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// syncRepoContext brings one repository's cache up to date: a fetch when it is
// ready, otherwise the first-time download (or a resume of one).
//
// A first-time download is single-flight per repository. A caller that arrives
// while one is running waits for that download's outcome instead of queueing on
// the repo lock, where it would pile up for the whole download and then run a
// fetch the fresh cache does not need.
func (c *Cache) syncRepoContext(ctx context.Context, url, barePath string) error {
	if !IsReady(barePath) {
		tracker, owner := c.beginOrJoinBuild(barePath)
		if !owner {
			select {
			case <-tracker.done:
				return tracker.err
			case <-ctx.Done():
				return context.Cause(ctx)
			}
		}
		err := c.buildRepoContext(ctx, url, barePath, tracker)
		c.endBuild(barePath, tracker, err)
		return err
	}

	repoLock := c.lockForRepo(barePath)
	if err := repoLock.LockContext(ctx); err != nil {
		return err
	}
	defer repoLock.Unlock()
	return c.fetchCachedRepoContext(ctx, url, barePath)
}

func (c *Cache) fetchCachedRepoContext(ctx context.Context, url, barePath string) error {
	if !c.fetchDue(barePath, time.Now()) {
		c.logger.Debug("repo cache: fetch skipped inside cooldown", "url", url, "path", barePath)
		return nil
	}
	c.logger.Info("repo cache: fetching", "url", url, "path", barePath)
	if err := gitFetchContext(ctx, barePath); err != nil {
		c.logger.Warn("repo cache: fetch failed", "url", url, "error", err)
		return err
	}
	markFetched(barePath, c.logger)
	return nil
}

// buildRepoContext runs as the owner of tracker. The readiness checks repeat
// under the lock because a pre-marker cache is adopted, not downloaded.
func (c *Cache) buildRepoContext(ctx context.Context, url, barePath string, tracker *buildTracker) error {
	repoLock := c.lockForRepo(barePath)
	if err := repoLock.LockContext(ctx); err != nil {
		return err
	}
	defer repoLock.Unlock()

	if IsReady(barePath) || adoptLegacyCacheContext(ctx, barePath) {
		return c.fetchCachedRepoContext(ctx, url, barePath)
	}
	// Not cached, or a previous download was interrupted — build the cache,
	// resuming from whatever slices already landed.
	c.logger.Info("repo cache: downloading", "url", url, "path", barePath, "resuming", isBareRepo(barePath))
	if err := bootstrapBareContext(ctx, url, barePath, c.logger, tracker.report); err != nil {
		c.logger.Error("repo cache: download incomplete, progress kept for the next attempt", "url", url, "error", err)
		return err
	}
	markFetched(barePath, c.logger)
	return nil
}

// Lookup returns the local bare clone path for a repo URL within a workspace.
// Returns "" if not cached, including while a download is still in progress
// or was interrupted: only a cache carrying the ready marker is usable.
func (c *Cache) Lookup(workspaceID, url string) string {
	barePath := c.BarePath(workspaceID, url)
	// Adopting here, not only in Sync, keeps an upgrade from hiding every
	// pre-marker cache until the serial Sync loop reaches it, which can be a
	// long time when an earlier repo is mid-download. It is safe without the
	// repo lock: it only reads git state and writes the marker, and our own
	// unfinished builds are excluded by buildingFile before any git call.
	if IsReady(barePath) || adoptLegacyCacheContext(context.Background(), barePath) {
		return barePath
	}
	return ""
}

// BarePath returns where a repo's bare cache lives, whether or not it exists
// yet. Lookup is the "is it cached?" question; this is the "where would it be?"
// question, which the GC needs to map a set of live repo URLs onto the
// directories it is about to consider evicting.
func (c *Cache) BarePath(workspaceID, url string) string {
	return filepath.Join(c.root, workspaceID, bareDirName(url))
}

// lastUsedFile records the last time a task asked for a worktree from this
// bare repo. It lives inside the bare repo so it is removed with it.
//
// Directory mtime cannot answer this question. Every daemon restart re-syncs
// each registered workspace's full repo list, and that path fetches every
// cached repo (see Sync), refreshing the mtime of repos no task has checked
// out in months. atime is worse: noatime is common on Linux and Windows
// disables it by default. So the signal has to be written explicitly, at the
// one place that means a repo was really used — CreateWorktree.
const lastUsedFile = ".multica_last_used"

// lastFetchedFile records the last successful remote refresh. It is separate
// from lastUsedFile: a checkout can be used repeatedly while the cache still
// needs no network request, and a background Sync can refresh a repo nobody
// has checked out yet.
const lastFetchedFile = ".multica_last_fetched"

func (c *Cache) fetchDue(barePath string, now time.Time) bool {
	if c.fetchCooldown <= 0 {
		return true
	}
	data, err := os.ReadFile(filepath.Join(barePath, lastFetchedFile))
	if err != nil {
		return true
	}
	stamp, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
	if err != nil || now.Before(stamp) {
		return true
	}
	return now.Sub(stamp) >= c.fetchCooldown
}

func (c *Cache) fetchDueForCheckout(ctx context.Context, barePath, ref string) bool {
	if c.fetchDue(barePath, time.Now()) {
		return true
	}
	// A caller naming an object we do not have locally is explicitly asking for
	// it; the cooldown must never turn that into a misleading missing-ref error.
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false
	}
	for _, candidate := range requestedRefCandidates(ref) {
		if gitRefExistsContext(ctx, barePath, candidate+"^{commit}") {
			return false
		}
	}
	return true
}

func markFetched(barePath string, logger *slog.Logger) {
	if barePath == "" {
		return
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if err := os.WriteFile(filepath.Join(barePath, lastFetchedFile), []byte(stamp), 0o644); err != nil && logger != nil {
		logger.Warn("repo cache: write last-fetched stamp failed", "repo", barePath, "error", err)
	}
}

// MarkUsed records that this bare repo was just used for a checkout. Callers
// must already hold the repo lock. Best-effort: a failed stamp only risks the
// repo looking idle later, and the GC's own missing-stamp grace period
// (see LastUsed) absorbs that.
func MarkUsed(barePath string, logger *slog.Logger) {
	if barePath == "" {
		return
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if err := os.WriteFile(filepath.Join(barePath, lastUsedFile), []byte(stamp), 0o644); err != nil && logger != nil {
		logger.Warn("repo cache: write last-used stamp failed", "repo", barePath, "error", err)
	}
}

// LastUsed reports when this bare repo was last used for a checkout, and
// whether a stamp existed at all.
//
// ok=false means "unknown", never "ancient". Every cache created before this
// stamp existed reports unknown, so treating it as infinitely old would make
// the first GC cycle after an upgrade wipe every repo cache on the machine and
// force a full re-clone of each. Callers must stamp an unknown repo and let it
// age from now.
func LastUsed(barePath string) (time.Time, bool) {
	data, err := os.ReadFile(filepath.Join(barePath, lastUsedFile))
	if err != nil {
		return time.Time{}, false
	}
	stamp, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
	if err != nil {
		return time.Time{}, false
	}
	return stamp, true
}

// WithRepoLock serializes caller-supplied mutations on a bare repo against all
// other same-repo operations that use the cache's lock (Sync, Fetch,
// CreateWorktree, and daemon GC maintenance).
func (c *Cache) WithRepoLock(barePath string, fn func() error) error {
	return c.WithRepoLockContext(context.Background(), barePath, fn)
}

// WithRepoLockContext is the cancellable form of WithRepoLock.
func (c *Cache) WithRepoLockContext(ctx context.Context, barePath string, fn func() error) error {
	repoLock := c.lockForRepo(barePath)
	if err := repoLock.LockContext(ctx); err != nil {
		return err
	}
	defer repoLock.Unlock()
	return fn()
}

// WithRepoMaintenance runs fn only when the repository is idle. A foreground
// waiter cancels the context passed to fn, then waits for fn to stop its process
// tree and release the repository. ran=false means maintenance was skipped
// because foreground work already owned or was waiting for the repository.
func (c *Cache) WithRepoMaintenance(ctx context.Context, barePath string, fn func(context.Context) error) (ran bool, err error) {
	repoLock := c.lockForRepo(barePath)
	maintenanceCtx, ok := repoLock.tryLockMaintenance(ctx)
	if !ok {
		return false, nil
	}
	defer repoLock.Unlock()
	return true, fn(maintenanceCtx)
}

// Fetch runs `git fetch origin` on a cached bare clone to get latest refs.
func (c *Cache) Fetch(barePath string) error {
	return c.WithRepoLock(barePath, func() error {
		if err := gitFetch(barePath); err != nil {
			return err
		}
		markFetched(barePath, c.logger)
		return nil
	})
}

// bareDirName returns a filesystem-safe, collision-free directory name for
// the bare clone of rawURL. The name is built from the host plus each
// path segment, joined by '+'. '+' is disallowed in GitHub and GitLab
// path segments, so two URLs produce the same name only if they point at
// the same repository on the same host.
//
// Examples:
//
//	https://github.com/org/my-repo.git           -> github.com+org+my-repo.git
//	git@github.com:org/my-repo                   -> github.com+org+my-repo.git
//	git@github.com:foo/bar-baz.git               -> github.com+foo+bar-baz.git
//	git@github.com:foo-bar/baz.git               -> github.com+foo-bar+baz.git
//	git@github.com:org/repo.git                  -> github.com+org+repo.git
//	git@gitlab.example.com:org/repo.git          -> gitlab.example.com+org+repo.git
//	ssh://git@gitlab.example.com:22/g/s/r.git    -> gitlab.example.com%3A22+g+s+r.git
//	git@gitlab.example.com-22:org/repo.git       -> gitlab.example.com-22+org+repo.git
//	my-repo                                      -> my-repo.git (bare name fallback)
func bareDirName(rawURL string) string {
	rawURL = strings.TrimRight(rawURL, "/")

	host, path := splitHostAndPath(rawURL)
	host = strings.ToLower(strings.TrimSpace(host))
	// Encode ':' as '%3A' so host:port is lossless. A naive ':'->'-' rewrite
	// would collapse `gitlab.example.com:22` onto a literal hostname
	// `gitlab.example.com-22`, reintroducing the silent wrong-remote class
	// this function exists to prevent. '%' is forbidden in valid hostnames
	// (RFC 952 / RFC 1123), and in GitHub/GitLab path segments, so the
	// encoded marker can never come from a legal input.
	host = strings.ReplaceAll(host, ":", "%3A")

	var parts []string
	if host != "" {
		parts = append(parts, host)
	}
	for _, seg := range strings.Split(path, "/") {
		if seg != "" {
			parts = append(parts, seg)
		}
	}

	name := strings.Join(parts, "+")
	if !strings.HasSuffix(name, ".git") {
		name += ".git"
	}
	if name == "" || name == ".git" {
		name = "repo.git"
	}
	return name
}

// splitHostAndPath extracts the host and path-with-namespace from the
// supported git URL forms:
//
//   - URL form (ssh://user@host[:port]/path, https://host/path) — returns
//     u.Host verbatim (may include :port) and u.Path without the leading slash.
//   - scp-style ([user@]host:path) — splits on the first ':' after the
//     optional 'user@'.
//   - Anything else (bare repo names, absolute filesystem paths) — returns
//     an empty host and the raw input as the path.
func splitHostAndPath(rawURL string) (host, path string) {
	if u, err := url.Parse(rawURL); err == nil && u.Scheme != "" && u.Host != "" {
		return u.Host, strings.TrimPrefix(u.Path, "/")
	}
	s := rawURL
	if i := strings.Index(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.Index(s, ":"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return "", s
}

// isBareRepo reports whether a directory has been initialized as a bare git
// repository. It says nothing about whether the download finished: git writes
// HEAD in the first second of a clone. Use IsReady for "can this be used".
func isBareRepo(path string) bool {
	_, err := os.Stat(filepath.Join(path, "HEAD"))
	return err == nil
}

// readyFile marks a bare cache whose download completed. It is written last,
// after the full history has landed, and is the only readiness signal: every
// other file in the directory (HEAD, config, refs/, objects/) exists from the
// first second of a download.
const readyFile = ".multica_cache_ready"

// IsReady reports whether a bare cache finished downloading and can serve
// worktrees. It is a single stat so lock-free callers can afford it.
func IsReady(barePath string) bool {
	_, err := os.Stat(filepath.Join(barePath, readyFile))
	return err == nil
}

// buildingFile marks a cache whose download this package started and has not
// finished. It exists so adoptLegacyCacheContext can tell an unfinished build
// from a complete pre-marker cache: once history is unshallowed the two look
// identical to git, yet the former may still lack the default branch's files.
const buildingFile = ".multica_cache_building"

func markReady(barePath string) error {
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if err := os.WriteFile(filepath.Join(barePath, readyFile), []byte(stamp), 0o644); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(barePath, buildingFile)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func isBuilding(barePath string) bool {
	_, err := os.Stat(filepath.Join(barePath, buildingFile))
	return err == nil
}

// adoptLegacyCacheContext stamps the ready marker onto a complete cache that
// was created before the marker existed, so an upgrade does not re-download
// every repository on the machine. A directory qualifies only when it has at
// least one ref, is not shallow, and is not one of our own unfinished builds:
// an interrupted `git clone --bare` has no refs (git writes them after the
// pack lands), and an interrupted sliced download carries buildingFile.
func adoptLegacyCacheContext(ctx context.Context, barePath string) bool {
	if !isBareRepo(barePath) || isBuilding(barePath) || !hasAnyRefContext(ctx, barePath) || isShallowContext(ctx, barePath) {
		return false
	}
	return markReady(barePath) == nil
}

func hasAnyRefContext(ctx context.Context, barePath string) bool {
	out, err := runGitOutputWithTimeoutContext(ctx, 30*time.Second, "-C", barePath, "for-each-ref", "--count=1", "--format=%(refname)")
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// isShallowContext reports whether history is still truncated. An unreadable
// repository counts as shallow so it is never mistaken for complete.
func isShallowContext(ctx context.Context, barePath string) bool {
	out, err := runGitOutputWithTimeoutContext(ctx, 30*time.Second, "-C", barePath, "rev-parse", "--is-shallow-repository")
	return err != nil || strings.TrimSpace(string(out)) != "false"
}

// modernFetchRefspec is the remote-tracking refspec that keeps fetched heads
// out of the bare repo's refs/heads/* namespace. That namespace is reserved
// for per-task worktree branches created by `git worktree add -b ...`, and any
// mirror-style fetch that targets refs/heads/* can collide with those locked
// refs and abort the entire fetch.
const modernFetchRefspec = "+refs/heads/*:refs/remotes/origin/*"

// Slice sizing for a cold download. Git cannot resume an interrupted pack
// transfer: a killed fetch discards everything it received. Progress therefore
// has to be committed in units git considers complete, so history is fetched
// as a depth-1 snapshot followed by deepen steps that grow geometrically. Each
// finished slice survives an interruption; a retry restarts from the smallest
// step, so it always re-reaches (and then passes) the slice that failed.
const (
	bootstrapInitialDeepen = 64
	bootstrapDeepenGrowth  = 4
	bootstrapMaxDeepen     = 1 << 20
	// bootstrapMaxStalledDeepens is how many deepen steps in a row may fetch
	// nothing before the download gives up. More than one, because each retry
	// asks for a geometrically larger slice than the last.
	bootstrapMaxStalledDeepens = 3

	// Blob batches are sized by count because a missing blob's size is unknown
	// until it arrives. The batch grows while batches finish quickly and
	// shrinks when one is slow, steering each toward blobBatchTargetDuration.
	blobBatchInitial        = 256
	blobBatchMax            = 8192
	blobBatchTargetDuration = 45 * time.Second
)

// filterUnsupportedMarker is what git prints when the server ignores --filter.
const filterUnsupportedMarker = "filtering not recognized by server"

// bootstrapBareContext builds the bare cache for url at dest, or resumes a
// build an earlier call left unfinished. It never deletes dest: on any error
// (including a timeout or cancellation) the slices already fetched stay on
// disk and the ready marker stays absent, so the next call continues from them.
//
// The cache is created as a blobless partial clone when the server supports
// object filters, and as a full clone otherwise. Three resumable phases run in
// order: the depth-1 snapshot, the deepen slices back to full history, and
// (blobless only) the default branch's file contents. The ready marker is
// written only after all three. Callers must hold the repo lock.
//
// progress, when non-nil, is told the phase and how far it has got.
func bootstrapBareContext(ctx context.Context, url, dest string, logger *slog.Logger, progress func(phase string, done, total int)) error {
	if progress == nil {
		progress = func(string, int, int) {}
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dest, buildingFile), nil, 0o644); err != nil {
		return fmt.Errorf("write building marker: %w", err)
	}
	// `git init` is idempotent on an existing repository, which is what makes
	// it the resume entry point as well as the cold-start one.
	if out, err := runGitCombinedOutputContext(ctx, "init", "--bare", dest); err != nil {
		return fmt.Errorf("git init --bare: %s: %w", strings.TrimSpace(string(out)), err)
	}
	// Set the remote-tracking layout up front so fetched heads never land in
	// refs/heads/*, which is reserved for per-task worktree branches.
	for _, kv := range [][2]string{
		{"remote.origin.url", url},
		{"remote.origin.fetch", modernFetchRefspec},
	} {
		if out, err := runGitCombinedOutputContext(ctx, "-C", dest, "config", kv[0], kv[1]); err != nil {
			return fmt.Errorf("set %s: %s: %w", kv[0], strings.TrimSpace(string(out)), err)
		}
	}
	removeStaleTempPacks(dest)

	if !hasAnyRefContext(ctx, dest) {
		if err := fetchFirstSliceContext(ctx, dest, logger); err != nil {
			return err
		}
	}

	step := bootstrapInitialDeepen
	slices, stalled := 0, 0
	for isShallowContext(ctx, dest) {
		if err := ctx.Err(); err != nil {
			return context.Cause(ctx)
		}
		progress(BuildPhaseHistory, slices, 0)
		boundary := shallowBoundary(dest)
		if out, err := runGitCombinedOutputContext(ctx, "-C", dest, "fetch", "--deepen="+strconv.Itoa(step), "origin"); err != nil {
			if sliceTimedOut(ctx, err) && step > 1 {
				// The slice outgrew one timeout window on this link. Nothing
				// of it was kept, so take a smaller bite instead of failing.
				step = max(1, step/bootstrapDeepenGrowth/bootstrapDeepenGrowth)
				removeStaleTempPacks(dest)
				continue
			}
			return fmt.Errorf("git fetch --deepen=%d: %s: %w", step, strings.TrimSpace(string(out)), err)
		}
		// A deepen that exits 0 yet leaves the shallow boundary where it was
		// fetched nothing. Without this the loop would spin on such a remote
		// forever while holding the repo lock.
		if bytes.Equal(boundary, shallowBoundary(dest)) {
			stalled++
			if stalled >= bootstrapMaxStalledDeepens {
				return fmt.Errorf("git fetch --deepen=%d: history stopped growing after %d slices (%d attempts in a row fetched nothing); the remote may not serve its full history", step, slices, stalled)
			}
		} else {
			stalled = 0
			slices++
		}
		if logger != nil {
			logger.Info("repo cache: history slice fetched", "path", dest, "deepen", step)
		}
		if step < bootstrapMaxDeepen {
			step *= bootstrapDeepenGrowth
		}
	}

	// Non-fatal: getRemoteDefaultBranch has fallbacks when origin/HEAD is unset.
	_ = runGitContext(ctx, "-C", dest, "remote", "set-head", "origin", "--auto")
	pointBareHeadAtDefaultBranchContext(ctx, dest)
	if err := prefetchDefaultBranchBlobsContext(ctx, dest, logger, progress); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	if err := markReady(dest); err != nil {
		return fmt.Errorf("write ready marker: %w", err)
	}
	return nil
}

// shallowBoundary returns the commits history is currently cut at. It changes
// whenever a deepen step actually lands more history.
func shallowBoundary(barePath string) []byte {
	data, _ := os.ReadFile(filepath.Join(barePath, "shallow"))
	return data
}

// pointBareHeadAtDefaultBranchContext reproduces the one thing `git clone
// --bare` did that init + fetch does not: a bare HEAD that resolves to the
// remote's default branch. `git init` leaves HEAD on an unborn branch, which
// breaks `HEAD` as a worktree base and the bare-HEAD hint in
// getRemoteDefaultBranch. Best-effort: every consumer has other fallbacks.
func pointBareHeadAtDefaultBranchContext(ctx context.Context, barePath string) {
	out, err := runGitOutputContext(ctx, "-C", barePath, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
	if err != nil {
		return
	}
	branch := strings.TrimPrefix(strings.TrimSpace(string(out)), "refs/remotes/origin/")
	if branch == "" {
		return
	}
	if err := runGitContext(ctx, "-C", barePath, "update-ref", "refs/heads/"+branch, "refs/remotes/origin/"+branch); err != nil {
		return
	}
	_ = runGitContext(ctx, "-C", barePath, "symbolic-ref", "HEAD", "refs/heads/"+branch)
}

// prefetchDefaultBranchBlobsContext downloads the file contents of the default
// branch tip into a blobless cache before it is declared ready.
//
// Without this a blobless cache only moves the problem: the first checkout has
// to fetch every blob of the tree in one lazy, non-resumable request, and a
// repository whose current files alone exceed one timeout window would fail
// there forever. Here the same blobs arrive in batches, each batch a complete
// pack that survives an interruption; a resume recomputes what is still
// missing. Blobs only reachable from other branches or older commits are still
// fetched on demand, which is what keeps the cache small.
func prefetchDefaultBranchBlobsContext(ctx context.Context, barePath string, logger *slog.Logger, progress func(phase string, done, total int)) error {
	if progress == nil {
		progress = func(string, int, int) {}
	}
	// Read the promisor flag strictly: isPartialCloneContext folds every
	// failure into "not partial", and here that would declare a cache ready
	// while its files are still missing.
	promisor, err := runGitOutputContext(ctx, "-C", barePath, "config", "--get", "remote.origin.promisor")
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil // key unset: a full clone has nothing to prefetch
		}
		return fmt.Errorf("read remote.origin.promisor: %w", err)
	}
	if strings.TrimSpace(string(promisor)) != "true" {
		return nil
	}
	tip, err := runGitOutputContext(ctx, "-C", barePath, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/HEAD^{tree}")
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			// No resolvable default branch; checkouts fall back to lazy fetching.
			return nil
		}
		return fmt.Errorf("resolve default branch tree: %w", err)
	}
	out, err := runGitOutputContext(ctx, "-C", barePath, "rev-list", "--objects", "--missing=print", "--no-object-names", strings.TrimSpace(string(tip)))
	if err != nil {
		return fmt.Errorf("list missing blobs: %w", err)
	}
	var missing []string
	for _, line := range strings.Split(string(out), "\n") {
		if oid, ok := strings.CutPrefix(strings.TrimSpace(line), "?"); ok {
			missing = append(missing, oid)
		}
	}

	total := len(missing)
	batch := blobBatchInitial
	for len(missing) > 0 {
		if err := ctx.Err(); err != nil {
			return context.Cause(ctx)
		}
		progress(BuildPhaseFiles, total-len(missing), total)
		n := min(batch, len(missing))
		started := time.Now()
		// The same invocation git itself uses for a lazy promisor fetch.
		out, err := runGitCombinedOutputStdinContext(ctx, strings.Join(missing[:n], "\n")+"\n",
			"-C", barePath, "-c", "fetch.negotiationAlgorithm=noop", "fetch", "origin",
			"--no-tags", "--no-write-fetch-head", "--recurse-submodules=no",
			"--filter="+partialCloneFilter, "--stdin")
		if err != nil {
			if sliceTimedOut(ctx, err) && batch > 1 {
				batch = max(1, batch/4)
				removeStaleTempPacks(barePath)
				continue
			}
			return fmt.Errorf("prefetch blobs (%d of %d left): %s: %w", len(missing), total, strings.TrimSpace(string(out)), err)
		}
		missing = missing[n:]
		if logger != nil {
			logger.Info("repo cache: file contents batch fetched", "path", barePath, "done", total-len(missing), "total", total)
		}
		switch took := time.Since(started); {
		case took < blobBatchTargetDuration/2:
			batch = min(blobBatchMax, batch*2)
		case took > blobBatchTargetDuration*2:
			batch = max(1, batch/2)
		}
	}
	return nil
}

// fetchFirstSliceContext fetches the depth-1 snapshot of every branch. It asks
// for a blobless partial clone first; git records the promisor config itself
// when given --filter. Two fallbacks keep a less capable server working: one
// that ignores the filter gets its promisor config removed again (the objects
// it sent are complete), and one that rejects shallow or filtered requests
// outright gets a plain full fetch.
func fetchFirstSliceContext(ctx context.Context, dest string, logger *slog.Logger) error {
	out, err := runGitCombinedOutputContext(ctx, "-C", dest, "fetch", "--depth=1", "--filter="+partialCloneFilter, "origin")
	if err == nil {
		if strings.Contains(string(out), filterUnsupportedMarker) {
			if logger != nil {
				logger.Info("repo cache: server does not support object filters, building a full clone", "path", dest)
			}
			return unsetPromisorRemoteContext(ctx, dest)
		}
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("git fetch --depth=1: %s: %w", strings.TrimSpace(string(out)), err)
	}
	if logger != nil {
		logger.Warn("repo cache: sliced partial fetch rejected, falling back to a full fetch", "path", dest, "error", strings.TrimSpace(string(out)))
	}
	if err := unsetPromisorRemoteContext(ctx, dest); err != nil {
		return err
	}
	if out, err := runGitCombinedOutputContext(ctx, "-C", dest, "fetch", "origin"); err != nil {
		return fmt.Errorf("git fetch: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func unsetPromisorRemoteContext(ctx context.Context, repoPath string) error {
	for _, key := range []string{"remote.origin.promisor", "remote.origin.partialclonefilter"} {
		_, err := runGitCombinedOutputContext(ctx, "-C", repoPath, "config", "--unset-all", key)
		// Exit 5 means the key was not set.
		if ee, ok := err.(*exec.ExitError); err != nil && !(ok && ee.ExitCode() == 5) {
			return fmt.Errorf("unset %s: %w", key, err)
		}
	}
	return nil
}

// removeStaleTempPacks deletes pack fragments a killed fetch left behind. Git
// never reuses them, so they are only dead weight in an unfinished cache.
func removeStaleTempPacks(barePath string) {
	matches, _ := filepath.Glob(filepath.Join(barePath, "objects", "pack", "tmp_*"))
	for _, m := range matches {
		_ = os.Remove(m)
	}
}

// gitFetch runs `git fetch origin` on a bare cache, migrating its fetch
// refspec to the remote-tracking layout first if it's still using the legacy
// mirror-style layout from an older version of this package. After a
// successful fetch it also refreshes refs/remotes/origin/HEAD so a remote
// default-branch change (e.g. master→main on an existing repo) actually
// takes effect in getRemoteDefaultBranch. Plain `git fetch origin` never
// touches that symref on its own, so without this call an existing cache
// would keep basing new worktrees on the original default branch forever
// after the remote flipped.
func gitFetch(barePath string) error {
	return gitFetchContext(context.Background(), barePath)
}

func gitFetchContext(ctx context.Context, barePath string) error {
	if err := ensureRemoteTrackingLayoutContext(ctx, barePath); err != nil {
		return fmt.Errorf("ensure refspec: %w", err)
	}
	if err := runGitFetchContext(ctx, barePath); err != nil {
		return err
	}
	// Refresh refs/remotes/origin/HEAD after every successful fetch.
	// set-head --auto is lightweight (a single ls-remote HEAD round-trip)
	// and non-fatal: if it fails we still have the step 2-5 fallbacks in
	// getRemoteDefaultBranch, but the modern-cache default-branch-change
	// path (the only path that can't be recovered any other way) relies
	// on this call.
	_ = runGitContext(ctx, "-C", barePath, "remote", "set-head", "origin", "--auto")
	return nil
}

// runGitFetch is the raw `git fetch origin` wrapper. Callers should go through
// gitFetch, which migrates legacy caches first.
func runGitFetch(barePath string) error {
	return runGitFetchContext(context.Background(), barePath)
}

func runGitFetchContext(ctx context.Context, barePath string) error {
	if out, err := runGitCombinedOutputContext(ctx, "-C", barePath, "fetch", "origin"); err != nil {
		return fmt.Errorf("git fetch: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// ensureRemoteTrackingLayout upgrades a bare repo from the legacy mirror
// refspec (+refs/heads/*:refs/heads/*) to the standard remote-tracking refspec
// (+refs/heads/*:refs/remotes/origin/*). It's idempotent: on an already-modern
// cache it's a single `git config --get` call. On legacy caches it rewrites
// the refspec, performs a backfill fetch to populate refs/remotes/origin/*,
// and runs `git remote set-head origin --auto` so getRemoteDefaultBranch can
// resolve the remote's default branch.
func ensureRemoteTrackingLayout(barePath string) error {
	return ensureRemoteTrackingLayoutContext(context.Background(), barePath)
}

func ensureRemoteTrackingLayoutContext(ctx context.Context, barePath string) error {
	cur, err := readFetchRefspecContext(ctx, barePath)
	if err != nil {
		return err
	}
	if cur == modernFetchRefspec || cur == strings.TrimPrefix(modernFetchRefspec, "+") {
		return nil // already modern
	}
	if err := setFetchRefspecContext(ctx, barePath, modernFetchRefspec); err != nil {
		return err
	}
	// Backfill refs/remotes/origin/* by fetching with the new refspec. This
	// writes to the origin/* namespace, so even worktree-locked refs/heads/*
	// branches can't collide.
	if err := runGitFetchContext(ctx, barePath); err != nil {
		return fmt.Errorf("backfill fetch after refspec migration: %w", err)
	}
	// Set refs/remotes/origin/HEAD so getRemoteDefaultBranch can read it.
	// Non-fatal: if this fails we fall back to origin/main, origin/master.
	_ = runGitContext(ctx, "-C", barePath, "remote", "set-head", "origin", "--auto")
	return nil
}

// readFetchRefspec returns the current remote.origin.fetch config value, or
// the empty string if it's not set. Distinguishes "missing" (exit 1) from
// real git errors.
func readFetchRefspec(barePath string) (string, error) {
	return readFetchRefspecContext(context.Background(), barePath)
}

func readFetchRefspecContext(ctx context.Context, barePath string) (string, error) {
	out, err := runGitOutputContext(ctx, "-C", barePath, "config", "--get", "remote.origin.fetch")
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return "", nil // key missing, not an error
		}
		return "", fmt.Errorf("read remote.origin.fetch: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func setFetchRefspec(barePath, refspec string) error {
	return setFetchRefspecContext(context.Background(), barePath, refspec)
}

func setFetchRefspecContext(ctx context.Context, barePath, refspec string) error {
	out, err := runGitCombinedOutputContext(ctx, "-C", barePath, "config", "remote.origin.fetch", refspec)
	if err != nil {
		return fmt.Errorf("set remote.origin.fetch: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// WorktreeParams holds inputs for creating a worktree from a cached bare clone.
type WorktreeParams struct {
	WorkspaceID         string // workspace that owns the repo
	RepoURL             string // remote URL to look up in the cache
	WorkDir             string // parent directory for the worktree (e.g. task workdir)
	Ref                 string // optional branch, tag, or commit to base the worktree on
	AgentName           string // for branch naming
	TaskID              string // for branch naming uniqueness
	CoAuthoredByEnabled bool   // install prepare-commit-msg hook for Co-authored-by trailer
	// LockWaitTimeout bounds only the wait for another same-repository
	// operation. Zero preserves the historical unbounded wait for internal and
	// older callers; retry-aware HTTP checkout requests set a finite value.
	LockWaitTimeout time.Duration
	// IsolatedGitMetadata creates a local clone whose .git directory lives
	// inside WorkDir instead of a linked worktree whose gitdir lives under the
	// shared cache. Codex tasks need this because workspace-write keeps a
	// resolved external worktree gitdir read-only even when it is explicitly
	// listed as a writable root — on Linux (multica-ai/multica#2925) and on the
	// Windows native sandbox (multica-ai/multica#6449).
	IsolatedGitMetadata bool
	// Fresh discards what an existing checkout at the target path holds —
	// uncommitted changes, untracked files, its branch position — and starts
	// over on a new branch from the base ref. It deletes no branch holding
	// unpushed commits. Without it, an existing checkout that holds work or is
	// already on this task's branch is kept as it is.
	Fresh bool
	// SparsePaths is the task's checkout declaration: comma- or newline-
	// separated paths relative to the repository root. Empty checks out the
	// whole tree, which is what every task that declares nothing still gets.
	SparsePaths string
}

// WorktreeResult describes a successfully created worktree.
type WorktreeResult struct {
	Path       string `json:"path"`        // absolute path to the worktree
	BranchName string `json:"branch_name"` // branch checked out; empty for a kept checkout on a detached HEAD
	// Kept is set when an existing checkout was left exactly as it was — not
	// reset, cleaned, switched, or pruned — and only its remote refs were
	// fetched. It names why: KeptTaskBranch or KeptLocalWork.
	Kept string `json:"kept,omitempty"`
	// UncommittedFiles and UnpushedCommits describe a kept checkout: the paths
	// `git status` reports, untracked files included, and the commits on HEAD
	// that no remote-tracking ref reaches.
	UncommittedFiles int `json:"uncommitted_files,omitempty"`
	UnpushedCommits  int `json:"unpushed_commits,omitempty"`
	// Stale is set when the fetch before the checkout failed or timed out, so
	// the checkout was built from whatever the cache last held and may be
	// behind the remote. StaleReason is the fetch error.
	Stale       bool   `json:"stale,omitempty"`
	StaleReason string `json:"stale_reason,omitempty"`
	// SparsePaths is the declaration this checkout was asked to honor. Empty
	// when the checkout is the whole tree. SparseSkipped is "kept" when a
	// declaration would have removed files from a checkout that was left in
	// place, "restored" when an empty declaration found a sparse checkout and
	// brought the rest of the repository back, and "widened" when a broader
	// declaration was applied without moving the branch.
	SparsePaths   string `json:"sparse_paths,omitempty"`
	SparseSkipped string `json:"sparse_skipped,omitempty"`
	// sparseReconciled is set once an existing checkout has been compared
	// with the declaration. noteSparse then records that outcome instead of
	// assuming a kept checkout ignored the declaration.
	sparseReconciled bool
}

// Reasons CreateWorktree keeps an existing checkout, reported in
// WorktreeResult.Kept.
const (
	// KeptTaskBranch: the checkout is already on this task's branch, so the
	// checkout was already done.
	KeptTaskBranch = "task_branch"
	// KeptLocalWork: the checkout holds uncommitted changes, untracked files,
	// or unpushed commits that moving it to a new branch would lose.
	KeptLocalWork = "local_work"
)

// CreateWorktree looks up the bare cache for a repo, fetches latest, and creates
// a git worktree in the agent's working directory. If a checkout already exists
// at the target path (reused environment), updateExistingCheckoutContext
// decides what happens to it.
func (c *Cache) CreateWorktree(params WorktreeParams) (*WorktreeResult, error) {
	return c.CreateWorktreeContext(context.Background(), params)
}

// CreateWorktreeContext is CreateWorktree with cancellation propagated through
// lock acquisition and every Git subprocess. Git work begins only after the
// lock is held, so a client that times out behind maintenance cannot leave a
// late, unwanted checkout.
func (c *Cache) CreateWorktreeContext(ctx context.Context, params WorktreeParams) (*WorktreeResult, error) {
	scope, parseErr := sparsecheckout.Parse(params.SparsePaths)
	if parseErr != nil {
		return nil, fmt.Errorf("checkout paths: %w", parseErr)
	}
	barePath := c.Lookup(params.WorkspaceID, params.RepoURL)
	if barePath == "" {
		// An unfinished first-time download is not "not found", and it is not
		// corruption either. Say which, so nobody deletes hours of progress.
		if status, _, building := c.BuildInProgress(params.WorkspaceID, params.RepoURL); building {
			return nil, &RepoBuildingError{URL: params.RepoURL, Status: status}
		}
		return nil, fmt.Errorf("repo not found in cache: %s (workspace: %s)", params.RepoURL, params.WorkspaceID)
	}

	// Serialize concurrent CreateWorktree calls on the same bare repo. Git's
	// own lockfiles (packed-refs.lock, config.lock, worktree admin dirs)
	// can't tolerate parallel fetch + worktree mutations on the same repo.
	repoLock := c.lockForRepo(barePath)
	lockCtx := ctx
	cancel := func() {}
	if params.LockWaitTimeout > 0 {
		lockCtx, cancel = context.WithTimeout(ctx, params.LockWaitTimeout)
	}
	err := repoLock.LockContext(lockCtx)
	cancel()
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: %s", ErrRepoBusy, params.RepoURL)
		}
		return nil, err
	}
	defer repoLock.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, context.Cause(ctx)
	}

	// Stamp before doing the work, not after: a task asking for a worktree is
	// what "this cache is still wanted" means, whether or not the checkout
	// ultimately succeeds. Stamping only on success would let a repo whose
	// checkouts keep failing age out from under the tasks still trying to use it.
	MarkUsed(barePath, c.logger)

	// Fetch latest from origin. This also migrates the bare cache's refspec
	// to the modern remote-tracking layout on first run, so subsequent fetches
	// never collide with the refs/heads/agent/* branches that worktree creation
	// locks in this same bare repo.
	var fetchErr error
	if c.fetchDueForCheckout(ctx, barePath, params.Ref) {
		fetchErr = gitFetchContext(ctx, barePath)
		if fetchErr == nil {
			markFetched(barePath, c.logger)
		}
	} else {
		c.logger.Debug("repo checkout: fetch skipped inside cooldown", "url", params.RepoURL, "path", barePath, "ref", params.Ref)
	}
	if fetchErr != nil {
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		// Non-fatal: preserve cached state and continue. The agent will receive
		// an older snapshot than the remote head, so the result carries the
		// failure back to the task (WorktreeResult.Stale); the daemon log alone
		// is somewhere the task never looks.
		c.logger.Warn("repo checkout: fetch failed, agent will see possibly stale code",
			"url", params.RepoURL,
			"error", fetchErr,
		)
	}
	if err := ctx.Err(); err != nil {
		return nil, context.Cause(ctx)
	}

	// Determine the ref to base the worktree on. By default this is the remote's
	// default branch (resolved internally via getRemoteDefaultBranch, which walks
	// origin/HEAD → origin/main, origin/master → bare-HEAD hint into origin/<same>
	// → single-entry scan of origin/* → bare HEAD when origin/* is empty).
	// Callers may request a specific branch, tag, or commit so review/QA agents
	// can inspect the exact revision without trying to mutate the daemon-owned
	// worktree metadata themselves.
	baseRef, err := resolveBaseRefContext(ctx, barePath, params.Ref)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, context.Cause(ctx)
	}

	// Empty here means params.Ref was unset and getRemoteDefaultBranch couldn't
	// resolve a default — the cache is in a state we refuse to guess from (no
	// origin/HEAD, no main/master, bare HEAD doesn't match any origin/* entry,
	// and origin/* has multiple candidates). The requested-ref path returns an
	// explicit error before reaching here, so this branch only fires for the
	// default-branch case.
	if baseRef == "" {
		return nil, fmt.Errorf("cannot resolve default branch for %s: bare cache at %s has no usable refs (origin/* is empty or ambiguous and bare HEAD has no match). Do not delete the cache: it finished downloading and deleting it only forces the whole download again. Name the branch explicitly with --ref <branch>; if the remote really has no branches yet, push one first", params.RepoURL, barePath)
	}

	// Build branch name: agent/{sanitized-name}/{task-id}
	branchName := fmt.Sprintf("agent/%s/%s", sanitizeName(params.AgentName), taskKey(params.TaskID))

	// Derive directory name from repo URL.
	dirName := repoNameFromURL(params.RepoURL)
	worktreePath := filepath.Join(params.WorkDir, dirName)

	// Once a workdir has moved to isolated metadata, keep using that safer
	// shape even if a later task comes from an older CLI or a different runtime
	// that omits the mode hint. This also makes provider transitions on a reused
	// workdir backward compatible.
	if params.IsolatedGitMetadata || isIsolatedCheckoutContext(ctx, worktreePath) {
		result, err := c.createOrUpdateIsolatedCheckoutContext(
			ctx,
			barePath,
			params.RepoURL,
			worktreePath,
			branchName,
			baseRef,
			params.Fresh,
			scope,
		)
		if err != nil {
			return nil, fmt.Errorf("create isolated checkout: %w", err)
		}

		for _, pattern := range agentGitExcludePatterns {
			_ = excludeFromGitContext(ctx, worktreePath, pattern)
		}
		if err := isolateWorktreeIdentityContext(ctx, barePath, worktreePath); err != nil {
			return nil, fmt.Errorf("isolate checkout Git identity: %w", err)
		}
		c.applyCoAuthoredBySettingContext(ctx, worktreePath, params)
		if err := ctx.Err(); err != nil {
			return nil, context.Cause(ctx)
		}

		c.logCheckoutReady("repo checkout: isolated checkout ready", params.RepoURL, baseRef, result)
		return noteSparse(result, params.SparsePaths).withFetchFailure(fetchErr), nil
	}

	// If worktree already exists (reused environment from a prior task),
	// reuse it instead of creating a new one.
	if isGitWorktree(worktreePath) {
		result, err := updateExistingCheckoutContext(ctx, worktreePath, branchName, baseRef, params.Fresh)
		if err != nil {
			return nil, fmt.Errorf("update existing worktree: %w", err)
		}
		if err := reconcileSparse(ctx, worktreePath, scope, result); err != nil {
			return nil, err
		}

		for _, pattern := range agentGitExcludePatterns {
			_ = excludeFromGitContext(ctx, worktreePath, pattern)
		}

		if err := isolateWorktreeIdentityContext(ctx, barePath, worktreePath); err != nil {
			return nil, fmt.Errorf("isolate checkout Git identity: %w", err)
		}

		// Reconcile the Co-authored-by hook and the workspace's recorded
		// setting. The hook lives in the bare repo's shared hooks dir, so we
		// must actively remove it when disabled — otherwise a previously
		// installed hook keeps appending the trailer to every commit even
		// after the user toggles the setting off.
		c.applyCoAuthoredBySettingContext(ctx, worktreePath, params)
		if err := ctx.Err(); err != nil {
			return nil, context.Cause(ctx)
		}

		c.logCheckoutReady("repo checkout: existing worktree updated", params.RepoURL, baseRef, result)
		return noteSparse(result, params.SparsePaths).withFetchFailure(fetchErr), nil
	}

	// Create a new worktree. createWorktree may rename the branch to avoid
	// collisions with stale per-task refs left over from previous runs.
	actualBranch, err := createWorktreeContext(ctx, barePath, worktreePath, branchName, baseRef, scope)
	if err != nil {
		return nil, fmt.Errorf("create worktree: %w", err)
	}

	// Exclude agent context files from git tracking.
	for _, pattern := range agentGitExcludePatterns {
		_ = excludeFromGitContext(ctx, worktreePath, pattern)
	}

	if err := isolateWorktreeIdentityContext(ctx, barePath, worktreePath); err != nil {
		return nil, fmt.Errorf("isolate checkout Git identity: %w", err)
	}

	// Reconcile the Co-authored-by hook and the workspace's recorded setting.
	// See the existing-worktree branch above for why removal is required when
	// the setting is disabled.
	c.applyCoAuthoredBySettingContext(ctx, worktreePath, params)
	if err := ctx.Err(); err != nil {
		return nil, context.Cause(ctx)
	}

	c.logger.Info("repo checkout: worktree created",
		"url", params.RepoURL,
		"path", worktreePath,
		"branch", actualBranch,
		"base", baseRef,
	)

	result := &WorktreeResult{
		Path:       worktreePath,
		BranchName: actualBranch,
	}
	return noteSparse(result, params.SparsePaths).withFetchFailure(fetchErr), nil
}

// noteSparse records the declaration on the result. A checkout reconcile
// already ran keeps that outcome: restoring the whole tree, widening the
// cone, or refusing to narrow a checkout that was left in place. A kept
// checkout that never went through reconcile still must not claim a cone
// was applied.
func noteSparse(result *WorktreeResult, declared string) *WorktreeResult {
	if result == nil {
		return result
	}
	declared = strings.TrimSpace(declared)
	if result.sparseReconciled {
		if result.SparseSkipped == sparsecheckout.SkippedRestored {
			result.SparsePaths = ""
			return result
		}
		if declared != "" {
			result.SparsePaths = declared
		}
		return result
	}
	if declared == "" {
		return result
	}
	result.SparsePaths = declared
	if result.Kept != "" {
		result.SparseSkipped = sparsecheckout.SkippedKept
	}
	return result
}

// reconcileSparse makes an existing checkout match scope. Adding files is
// done even when the branch is kept. Removing files is not: that waits for
// a checkout that is allowed to start over.
func reconcileSparse(ctx context.Context, path string, scope sparsecheckout.Scope, result *WorktreeResult) error {
	if result == nil {
		return nil
	}
	action, err := sparsecheckout.Reconcile(ctx, path, scope, result.Kept == "")
	if err != nil {
		return fmt.Errorf("sparse checkout: %w", err)
	}
	result.sparseReconciled = true
	if action != "" {
		result.SparseSkipped = action
	}
	return nil
}

// withFetchFailure marks the result stale when the pre-checkout fetch failed.
func (r *WorktreeResult) withFetchFailure(fetchErr error) *WorktreeResult {
	if fetchErr != nil {
		r.Stale = true
		// Git's stderr is multi-line; the reason is shown inline to the agent.
		r.StaleReason = strings.Join(strings.Fields(fetchErr.Error()), " ")
	}
	return r
}

// logCheckoutReady logs how CreateWorktree left an existing or isolated
// checkout, naming a kept one as such so the log never reads as if its work
// had moved to baseRef.
func (c *Cache) logCheckoutReady(msg, repoURL, baseRef string, result *WorktreeResult) {
	if result.Kept != "" {
		c.logger.Info("repo checkout: existing checkout kept",
			"url", repoURL,
			"path", result.Path,
			"branch", result.BranchName,
			"reason", result.Kept,
			"uncommitted_files", result.UncommittedFiles,
			"unpushed_commits", result.UnpushedCommits,
		)
		return
	}
	c.logger.Info(msg,
		"url", repoURL,
		"path", result.Path,
		"branch", result.BranchName,
		"base", baseRef,
	)
}

const (
	isolatedCheckoutConfigKey   = "multica.checkout-mode"
	isolatedCheckoutConfigValue = "isolated"
	isolatedCacheRemoteName     = "multica-cache"
)

// createOrUpdateIsolatedCheckout keeps Git metadata inside the task workdir.
// The fresh path uses a local clone, so immutable Git objects are hard-linked
// (copied on Windows, see localCloneArgs) from the daemon's cache while refs,
// index, logs, config, and new objects remain private to the task checkout.
// The temporary cache remote is then replaced with the real repository URL so
// an agent's normal fetch / push commands still target GitHub rather than the
// daemon-owned bare cache.
func (c *Cache) createOrUpdateIsolatedCheckout(barePath, repoURL, checkoutPath, branchName, baseRef string, fresh bool) (*WorktreeResult, error) {
	return c.createOrUpdateIsolatedCheckoutContext(context.Background(), barePath, repoURL, checkoutPath, branchName, baseRef, fresh, sparsecheckout.Scope{})
}

func (c *Cache) createOrUpdateIsolatedCheckoutContext(ctx context.Context, barePath, repoURL, checkoutPath, branchName, baseRef string, fresh bool, scope sparsecheckout.Scope) (*WorktreeResult, error) {
	baseCommit, err := resolveCommitContext(ctx, barePath, baseRef)
	if err != nil {
		return nil, err
	}

	if isIsolatedCheckoutContext(ctx, checkoutPath) {
		if err := setIsolatedCheckoutOriginContext(ctx, checkoutPath, repoURL); err != nil {
			return nil, err
		}
		// Idempotent, and required for a workdir that was first created while
		// the cache was still a full clone: without it, a checkout backed by a
		// blobless cache resolves missing blobs to nothing instead of fetching.
		if isPartialCloneContext(ctx, barePath) {
			if err := configurePromisorRemoteContext(ctx, checkoutPath); err != nil {
				return nil, err
			}
		}
		// Refresh the remote refs before inspecting the checkout: a kept
		// checkout still gets them, and they decide which commits are unpushed.
		if err := syncIsolatedCheckoutRefsContext(ctx, barePath, checkoutPath, baseRef); err != nil {
			return nil, err
		}
		result, err := updateExistingCheckoutContext(ctx, checkoutPath, branchName, baseCommit, fresh)
		if err != nil {
			return result, err
		}
		if err := reconcileSparse(ctx, checkoutPath, scope, result); err != nil {
			return nil, err
		}
		if result.Kept != "" {
			return result, nil
		}
		// Drop earlier tasks' agent/* heads so a reused workdir doesn't grow a
		// new local branch on every checkout. Non-fatal: leftover branches are
		// harmless clutter and must never fail the checkout.
		if err := deleteStaleAgentBranchesContext(ctx, checkoutPath, result.BranchName); err != nil {
			c.logger.Warn("repo checkout: prune stale branches failed (non-fatal)", "error", err)
		}
		return result, nil
	}
	// A daemon upgrade can resume a pre-fix Codex workdir that still has a
	// linked worktree. Remove it through Git (so the shared admin record is
	// cleaned too), then recreate the same checkout path with local metadata.
	// Removing it deletes its working tree, so one keepReason claims stays a
	// linked worktree until the caller asks for fresh. Even then its branch
	// comes along when it holds unpushed commits: fresh discards the working
	// tree, not commits, and left in the shared cache the branch would be
	// out of the agent's reach and dropped by the next GC.
	var carryBranch string
	if isGitWorktree(checkoutPath) {
		state, err := inspectCheckoutContext(ctx, checkoutPath)
		if err != nil {
			return nil, err
		}
		if !fresh {
			if state.Kept = keepReason(state, branchName); state.Kept != "" {
				if err := reconcileSparse(ctx, checkoutPath, scope, state); err != nil {
					return nil, err
				}
				return state, nil
			}
		}
		if state.BranchName != "" && state.UnpushedCommits > 0 {
			carryBranch = state.BranchName
		}
		if err := removeLinkedWorktreeContext(ctx, barePath, checkoutPath); err != nil {
			return nil, err
		}
	}
	if _, err := os.Stat(checkoutPath); err == nil {
		return nil, fmt.Errorf("checkout path already exists and is not a Multica isolated checkout: %s", checkoutPath)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat checkout path: %w", err)
	}

	actualBranch, err := createIsolatedCheckoutContext(ctx, barePath, repoURL, checkoutPath, branchName, baseRef, baseCommit, carryBranch, scope)
	if err != nil {
		return nil, err
	}
	return &WorktreeResult{Path: checkoutPath, BranchName: actualBranch}, nil
}

func removeLinkedWorktree(barePath, checkoutPath string) error {
	return removeLinkedWorktreeContext(context.Background(), barePath, checkoutPath)
}

func removeLinkedWorktreeContext(ctx context.Context, barePath, checkoutPath string) error {
	out, err := runGitOutputContext(ctx, "-C", checkoutPath, "rev-parse", "--git-common-dir")
	if err != nil {
		return fmt.Errorf("resolve linked worktree common dir: %w", err)
	}
	commonDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(checkoutPath, commonDir)
	}
	if !sameResolvedPath(commonDir, barePath) {
		return fmt.Errorf("linked worktree common dir %s does not match cache %s", commonDir, barePath)
	}
	if out, err := runGitCombinedOutputContext(ctx, "-C", barePath, "worktree", "remove", "--force", checkoutPath); err != nil {
		return fmt.Errorf("remove linked worktree: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// sameResolvedPath reports whether a and b denote the same location after
// resolving to absolute, symlink-free, cleaned form. It is a path-equality
// check (not a same-device check): the migration guard uses it to confirm a
// linked worktree's git-common-dir really points at this cache bare repo
// before removing the worktree.
func sameResolvedPath(a, b string) bool {
	clean := func(path string) string {
		abs, err := filepath.Abs(path)
		if err == nil {
			path = abs
		}
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			path = resolved
		}
		return filepath.Clean(path)
	}
	return clean(a) == clean(b)
}

// localCloneArgs builds the `git clone --local` invocation that seeds a fresh
// isolated checkout from the shared bare cache.
//
// On Windows the clone also passes --no-hardlinks. A local clone hardlinks
// .git/objects whenever it can, but an NTFS hard link only exists within a
// single volume and every link shares one underlying file *and* one security
// descriptor. Copying the objects instead keeps a cache and workdir that live
// on different drives working, and stops a task checkout — whose tree the
// sandbox makes writable — from re-permissioning the daemon-owned cache's
// object files. The cost is extra disk and a slower first checkout on Windows.
func localCloneArgs(goos, barePath, checkoutPath string) []string {
	args := []string{"clone", "--local", "--no-checkout", "--no-tags"}
	if goos == "windows" {
		args = append(args, "--no-hardlinks")
	}
	return append(args, "--origin", isolatedCacheRemoteName, barePath, checkoutPath)
}

// createIsolatedCheckout seeds a new isolated checkout from the cache. A
// non-empty carryBranch names a cache branch to import as a local branch of
// the same name — the branch of the linked worktree this checkout replaces.
func createIsolatedCheckout(barePath, repoURL, checkoutPath, branchName, baseRef, baseCommit, carryBranch string) (_ string, retErr error) {
	return createIsolatedCheckoutContext(context.Background(), barePath, repoURL, checkoutPath, branchName, baseRef, baseCommit, carryBranch, sparsecheckout.Scope{})
}

func createIsolatedCheckoutContext(ctx context.Context, barePath, repoURL, checkoutPath, branchName, baseRef, baseCommit, carryBranch string, scope sparsecheckout.Scope) (_ string, retErr error) {
	if out, err := runGitCombinedOutputContext(
		ctx,
		localCloneArgs(runtime.GOOS, barePath, checkoutPath)...,
	); err != nil {
		// Do not remove checkoutPath here. A different repository with the
		// same basename could have won the path race after our pre-check; Git
		// then fails safely, and deleting the path would destroy its checkout.
		return "", fmt.Errorf("git clone --local: %s: %w", strings.TrimSpace(string(out)), err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(checkoutPath)
		}
	}()

	// The origin swap has to happen before the first checkout when the cache
	// is a partial clone. `git clone --local` hardlinks the objects it can see
	// and does NOT inherit the promisor configuration, so a blobless cache
	// yields a checkout whose blobs are unreachable — and git reports that as
	// success with every file "deleted" rather than as an error. Pointing
	// origin at the real remote and restoring the promisor config first lets
	// the checkout below lazily fetch what it needs.
	if out, err := runGitCombinedOutputContext(ctx, "-C", checkoutPath, "remote", "remove", isolatedCacheRemoteName); err != nil {
		return "", fmt.Errorf("remove cache remote: %s: %w", strings.TrimSpace(string(out)), err)
	}
	if out, err := runGitCombinedOutputContext(ctx, "-C", checkoutPath, "remote", "add", "origin", repoURL); err != nil {
		return "", fmt.Errorf("add origin remote: %s: %w", strings.TrimSpace(string(out)), err)
	}
	if isPartialCloneContext(ctx, barePath) {
		if err := configurePromisorRemoteContext(ctx, checkoutPath); err != nil {
			return "", err
		}
	}
	// A shallow bare cache can have a freshly fetched base commit reachable
	// only from refs/remotes/origin/* while its copied refs/heads/* remain
	// stale. Git ignores --local for a shallow source and the initial clone can
	// therefore omit baseCommit even though it exists in the cache. Import the
	// cache refs and selected base before trying to check it out.
	if err := syncIsolatedCheckoutRefsContext(ctx, barePath, checkoutPath, baseRef); err != nil {
		return "", err
	}
	// Cone before the first checkout, so the blobs outside it are never
	// written. A blobless cache would otherwise fetch every one of them here.
	if scope.Active() {
		if err := sparsecheckout.Enable(ctx, checkoutPath, scope); err != nil {
			return "", err
		}
	}

	if out, err := runGitCombinedOutputContext(ctx, "-C", checkoutPath, "checkout", "--detach", baseCommit); err != nil {
		return "", fmt.Errorf("git checkout --detach: %s: %w", strings.TrimSpace(string(out)), err)
	}
	if err := deleteAllLocalBranchesContext(ctx, checkoutPath); err != nil {
		return "", err
	}
	// Import the carried branch before creating the task branch, so a carried
	// branch with the task branch's own name moves the new one to a
	// timestamped name instead of being overwritten.
	if carryBranch != "" {
		ref := "refs/heads/" + carryBranch
		if out, err := runGitCombinedOutputContext(ctx, "-C", checkoutPath, "fetch", "--no-tags", barePath, ref+":"+ref); err != nil {
			return "", fmt.Errorf("carry branch %s: %s: %w", carryBranch, strings.TrimSpace(string(out)), err)
		}
	}
	if out, err := runGitCombinedOutputContext(ctx, "-C", checkoutPath, "config", isolatedCheckoutConfigKey, isolatedCheckoutConfigValue); err != nil {
		return "", fmt.Errorf("mark isolated checkout: %s: %w", strings.TrimSpace(string(out)), err)
	}

	actualBranch, err := checkoutNewBranchContext(ctx, checkoutPath, branchName, baseCommit)
	if err != nil {
		return "", err
	}
	if scope.Active() {
		if err := sparsecheckout.Finish(ctx, checkoutPath); err != nil {
			return "", err
		}
	}
	cleanup = false
	return actualBranch, nil
}

func resolveCommit(repoPath, ref string) (string, error) {
	return resolveCommitContext(context.Background(), repoPath, ref)
}

func resolveCommitContext(ctx context.Context, repoPath, ref string) (string, error) {
	out, err := runGitOutputContext(ctx, "-C", repoPath, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve checkout base %q: %w", ref, err)
	}
	commit := strings.TrimSpace(string(out))
	if commit == "" {
		return "", fmt.Errorf("resolve checkout base %q: empty commit", ref)
	}
	return commit, nil
}

func isIsolatedCheckout(path string) bool {
	return isIsolatedCheckoutContext(context.Background(), path)
}

func isIsolatedCheckoutContext(ctx context.Context, path string) bool {
	info, err := os.Stat(filepath.Join(path, ".git"))
	if err != nil || !info.IsDir() {
		return false
	}
	out, err := runGitOutputContext(ctx, "-C", path, "config", "--get", isolatedCheckoutConfigKey)
	return err == nil && strings.TrimSpace(string(out)) == isolatedCheckoutConfigValue
}

// partialCloneFilter is the object filter a blobless partial clone is created
// with, and the value that has to be restored on any repository that inherits
// such a clone's incomplete object store.
const partialCloneFilter = "blob:none"

// isPartialClone reports whether a repository was created as a partial clone,
// i.e. whether git will lazily fetch missing objects from its promisor remote.
func isPartialClone(repoPath string) bool {
	return isPartialCloneContext(context.Background(), repoPath)
}

func isPartialCloneContext(ctx context.Context, repoPath string) bool {
	out, err := runGitOutputWithTimeoutContext(ctx, 30*time.Second, "-C", repoPath, "config", "--get", "remote.origin.promisor")
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// configurePromisorRemote marks origin as the promisor remote for a repository
// whose object store is incomplete, so git lazily fetches missing blobs from
// the real remote instead of failing. It mirrors the two config keys
// `git clone --filter=blob:none` writes; `git clone --local` does not copy
// them across, which is why they have to be restored by hand.
func configurePromisorRemote(repoPath string) error {
	return configurePromisorRemoteContext(context.Background(), repoPath)
}

func configurePromisorRemoteContext(ctx context.Context, repoPath string) error {
	settings := [][2]string{
		{"remote.origin.promisor", "true"},
		{"remote.origin.partialclonefilter", partialCloneFilter},
	}
	for _, kv := range settings {
		if out, err := runGitCombinedOutputContext(ctx, "-C", repoPath, "config", kv[0], kv[1]); err != nil {
			return fmt.Errorf("set %s: %s: %w", kv[0], strings.TrimSpace(string(out)), err)
		}
	}
	return nil
}

func setIsolatedCheckoutOrigin(path, repoURL string) error {
	return setIsolatedCheckoutOriginContext(context.Background(), path, repoURL)
}

func setIsolatedCheckoutOriginContext(ctx context.Context, path, repoURL string) error {
	out, err := runGitCombinedOutputContext(ctx, "-C", path, "remote", "set-url", "origin", repoURL)
	if err != nil {
		return fmt.Errorf("set origin remote: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// syncIsolatedCheckoutRefs mirrors the cache's real origin/* and tag refs into
// the task-local repository. It also fetches the selected base ref directly so
// a reused checkout can move to a newly-fetched commit without depending on
// the cache after this function returns.
func syncIsolatedCheckoutRefs(barePath, checkoutPath, baseRef string) error {
	return syncIsolatedCheckoutRefsContext(context.Background(), barePath, checkoutPath, baseRef)
}

func syncIsolatedCheckoutRefsContext(ctx context.Context, barePath, checkoutPath, baseRef string) error {
	refspecs := []string{
		"+refs/remotes/origin/*:refs/remotes/origin/*",
		"+refs/tags/*:refs/tags/*",
	}
	args := []string{"-C", checkoutPath, "fetch", "--force", "--no-tags", barePath}
	args = append(args, refspecs...)
	if out, err := runGitCombinedOutputContext(ctx, args...); err != nil {
		return fmt.Errorf("sync cache refs: %s: %w", strings.TrimSpace(string(out)), err)
	}
	if out, err := runGitCombinedOutputContext(ctx, "-C", checkoutPath, "fetch", "--force", "--no-tags", barePath, baseRef); err != nil {
		return fmt.Errorf("fetch checkout base: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// deleteAllLocalBranches removes the heads copied from the bare cache into a
// fresh local clone. The clone is detached and has no task-created branches to
// preserve yet.
func deleteAllLocalBranches(repoPath string) error {
	return deleteAllLocalBranchesContext(context.Background(), repoPath)
}

func deleteAllLocalBranchesContext(ctx context.Context, repoPath string) error {
	return deleteLocalBranchesUnderContext(ctx, repoPath, "refs/heads/", nil)
}

// deleteStaleAgentBranches prunes branches left by earlier Multica tasks while
// preserving the current task branch, every user-created local branch, and
// every agent branch holding commits no remote-tracking ref reaches. In an
// isolated checkout that branch is the only copy of those commits; deleting it
// would leave them to the reflog (MUL-7284).
func deleteStaleAgentBranches(repoPath, keepBranch string) error {
	return deleteStaleAgentBranchesContext(context.Background(), repoPath, keepBranch)
}

func deleteStaleAgentBranchesContext(ctx context.Context, repoPath, keepBranch string) error {
	keepRef := "refs/heads/" + keepBranch
	return deleteLocalBranchesUnderContext(ctx, repoPath, "refs/heads/agent/", func(ref string) (bool, error) {
		if ref == keepRef {
			return true, nil
		}
		unpushed, err := countUnpushedCommitsContext(ctx, repoPath, ref)
		return unpushed > 0, err
	})
}

// deleteLocalBranchesUnder deletes every branch under namespace that keep does
// not claim. A nil keep deletes them all.
func deleteLocalBranchesUnder(repoPath, namespace string, keep func(ref string) (bool, error)) error {
	return deleteLocalBranchesUnderContext(context.Background(), repoPath, namespace, keep)
}

func deleteLocalBranchesUnderContext(ctx context.Context, repoPath, namespace string, keep func(ref string) (bool, error)) error {
	out, err := runGitOutputContext(ctx, "-C", repoPath, "for-each-ref", "--format=%(refname)", namespace)
	if err != nil {
		return fmt.Errorf("list local branches: %w", err)
	}
	for _, ref := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if keep != nil {
			kept, err := keep(ref)
			if err != nil {
				return err
			}
			if kept {
				continue
			}
		}
		if out, err := runGitCombinedOutputContext(ctx, "-C", repoPath, "update-ref", "-d", ref); err != nil {
			return fmt.Errorf("delete local branch %s: %s: %w", ref, strings.TrimSpace(string(out)), err)
		}
	}
	return nil
}

func checkoutNewBranch(repoPath, branchName, baseRef string) (string, error) {
	return checkoutNewBranchContext(context.Background(), repoPath, branchName, baseRef)
}

func checkoutNewBranchContext(ctx context.Context, repoPath, branchName, baseRef string) (string, error) {
	out, err := runGitCombinedOutputContext(ctx, "-C", repoPath, "checkout", "-b", branchName, baseRef)
	if err == nil {
		return branchName, nil
	}
	wrapped := fmt.Errorf("git checkout -b: %s: %w", strings.TrimSpace(string(out)), err)
	if !isBranchCollisionError(wrapped) {
		return "", wrapped
	}
	branchName = fmt.Sprintf("%s-%d", branchName, time.Now().Unix())
	if out2, err2 := runGitCombinedOutputContext(ctx, "-C", repoPath, "checkout", "-b", branchName, baseRef); err2 != nil {
		return "", fmt.Errorf("git checkout -b (retry): %s: %w", strings.TrimSpace(string(out2)), err2)
	}
	return branchName, nil
}

func resolveBaseRef(barePath, requestedRef string) (string, error) {
	return resolveBaseRefContext(context.Background(), barePath, requestedRef)
}

func resolveBaseRefContext(ctx context.Context, barePath, requestedRef string) (string, error) {
	ref := strings.TrimSpace(requestedRef)
	if ref == "" {
		return getRemoteDefaultBranchContext(ctx, barePath), nil
	}

	// Prefer remote-tracking branches for human branch names. Then allow full
	// local refs, tags, and raw commits that exist in the fetched bare cache.
	candidates := requestedRefCandidates(ref)
	for _, candidate := range candidates {
		if gitRefExistsContext(ctx, barePath, candidate+"^{commit}") {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("cannot resolve requested ref %q in repo cache at %s", ref, barePath)
}

func requestedRefCandidates(ref string) []string {
	return []string{
		"refs/remotes/origin/" + ref,
		"refs/tags/" + ref,
		ref,
	}
}

func gitRefExists(repoPath, ref string) bool {
	return gitRefExistsContext(context.Background(), repoPath, ref)
}

func gitRefExistsContext(ctx context.Context, repoPath, ref string) bool {
	return runGitContext(ctx, "-C", repoPath, "rev-parse", "--verify", "--quiet", ref) == nil
}

// createWorktree creates a git worktree at the given path with a new branch.
// Returns the actual branch name used — which may differ from the requested
// branchName if a collision was resolved by appending a timestamp suffix.
func createWorktree(gitRoot, worktreePath, branchName, baseRef string) (string, error) {
	return createWorktreeContext(context.Background(), gitRoot, worktreePath, branchName, baseRef, sparsecheckout.Scope{})
}

func createWorktreeContext(ctx context.Context, gitRoot, worktreePath, branchName, baseRef string, scope sparsecheckout.Scope) (string, error) {
	// Pre-check: if the worktree path already exists we would get a confusing
	// "already exists" error from `git worktree add` — which used to be
	// misclassified as a branch collision, causing the retry to leak branches
	// into the bare repo. Fail cleanly here instead. The caller is expected
	// to route reused workdirs through updateExistingWorktree via isGitWorktree.
	if _, err := os.Stat(worktreePath); err == nil {
		return "", fmt.Errorf("worktree path already exists and is not a valid git worktree: %s", worktreePath)
	}

	add := runWorktreeAddContext
	if scope.Active() {
		add = runWorktreeAddNoCheckoutContext
	}
	err := add(ctx, gitRoot, worktreePath, branchName, baseRef)
	if err != nil && isBranchCollisionError(err) {
		// Branch name collision: append timestamp and retry once.
		branchName = fmt.Sprintf("%s-%d", branchName, time.Now().Unix())
		err = add(ctx, gitRoot, worktreePath, branchName, baseRef)
	}
	if err != nil {
		return "", err
	}
	if scope.Active() {
		if err := materializeSparseWorktree(ctx, gitRoot, worktreePath, branchName, scope); err != nil {
			return "", err
		}
	}
	return branchName, nil
}

// materializeSparseWorktree checks out only the declared cone. The worktree
// was added with --no-checkout, so nothing is on disk yet; a failed cone
// removes that worktree and the branch it created.
func materializeSparseWorktree(ctx context.Context, gitRoot, worktreePath, branchName string, scope sparsecheckout.Scope) error {
	fail := func(err error) error {
		if out, rmErr := runGitCombinedOutputContext(ctx, "-C", gitRoot, "worktree", "remove", "--force", worktreePath); rmErr != nil {
			_ = os.RemoveAll(worktreePath)
			_ = rmErr
			_ = out
		}
		_, _ = runGitCombinedOutputContext(ctx, "-C", gitRoot, "branch", "-D", branchName)
		return err
	}
	if err := sparsecheckout.Enable(ctx, worktreePath, scope); err != nil {
		return fail(fmt.Errorf("sparse checkout: %w", err))
	}
	if err := sparsecheckout.CheckoutCurrent(ctx, worktreePath); err != nil {
		return fail(fmt.Errorf("sparse checkout: %w", err))
	}
	if err := sparsecheckout.Finish(ctx, worktreePath); err != nil {
		return fail(fmt.Errorf("sparse checkout: %w", err))
	}
	return nil
}

func runWorktreeAdd(gitRoot, worktreePath, branchName, baseRef string) error {
	return runWorktreeAddContext(context.Background(), gitRoot, worktreePath, branchName, baseRef)
}

func runWorktreeAddContext(ctx context.Context, gitRoot, worktreePath, branchName, baseRef string) error {
	return runWorktreeAddArgs(ctx, gitRoot, false, branchName, worktreePath, baseRef)
}

func runWorktreeAddNoCheckoutContext(ctx context.Context, gitRoot, worktreePath, branchName, baseRef string) error {
	return runWorktreeAddArgs(ctx, gitRoot, true, branchName, worktreePath, baseRef)
}

func runWorktreeAddArgs(ctx context.Context, gitRoot string, noCheckout bool, branchName, worktreePath, baseRef string) error {
	args := []string{"-C", gitRoot, "worktree", "add"}
	if noCheckout {
		args = append(args, "--no-checkout")
	}
	args = append(args, "-b", branchName, worktreePath, baseRef)
	if out, err := runGitCombinedOutputContext(ctx, args...); err != nil {
		return fmt.Errorf("git worktree add: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// isBranchCollisionError returns true if err is specifically about a branch
// name already existing. Git's other "already exists" messages (notably path
// collisions from `git worktree add`) must NOT be treated as branch
// collisions, or the retry-with-timestamp logic will leak branches while
// still failing on the original path collision.
func isBranchCollisionError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// Git's message is "fatal: a branch named 'X' already exists".
	return strings.Contains(msg, "a branch named")
}

// isGitWorktree checks if a path is an existing git worktree.
// Worktrees have a .git *file* (not directory) that points to the main repo.
func isGitWorktree(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && !info.IsDir()
}

// updateExistingCheckoutContext handles a checkout CreateWorktree finds already
// in place. Re-running checkout must never silently lose work (MUL-7284), and
// a reused workdir reaches here within one task (a repeated checkout), in a
// follow-up turn, and from a fresh session that has no memory of the directory.
// So unless fresh is set, a checkout keepReason claims is left exactly as it
// is. Only one with nothing to lose — or any, when fresh is set — is moved to a
// new branch from baseRef. The caller fetches beforehand, so a kept checkout
// still has current remote refs.
func updateExistingCheckoutContext(ctx context.Context, path, branchName, baseRef string, fresh bool) (*WorktreeResult, error) {
	if !fresh {
		state, err := inspectCheckoutContext(ctx, path)
		if err != nil {
			return nil, err
		}
		if state.Kept = keepReason(state, branchName); state.Kept != "" {
			return state, nil
		}
	}
	actualBranch, err := updateExistingWorktreeContext(ctx, path, branchName, baseRef)
	if err != nil {
		return nil, err
	}
	return &WorktreeResult{Path: path, BranchName: actualBranch}, nil
}

// keepReason says why an existing checkout must be left as it is, or returns
// "" when moving it to a new branch loses nothing. It is kept when it is
// already on this task's branch — the checkout was already done — or when it
// holds uncommitted changes, untracked files, or unpushed commits.
func keepReason(state *WorktreeResult, branchName string) string {
	switch {
	case isTaskBranch(state.BranchName, branchName):
		return KeptTaskBranch
	case state.UncommittedFiles > 0 || state.UnpushedCommits > 0:
		return KeptLocalWork
	default:
		return ""
	}
}

// inspectCheckoutContext describes what an existing checkout holds: its branch
// (empty on a detached HEAD), the paths `git status` reports, untracked files
// included, and the commits on HEAD that no remote-tracking ref reaches.
func inspectCheckoutContext(ctx context.Context, path string) (*WorktreeResult, error) {
	result := &WorktreeResult{Path: path}
	// symbolic-ref fails on a detached HEAD, which leaves BranchName empty.
	if out, err := runGitOutputContext(ctx, "-C", path, "symbolic-ref", "--quiet", "HEAD"); err == nil {
		result.BranchName = strings.TrimPrefix(strings.TrimSpace(string(out)), "refs/heads/")
	}
	// Untracked files are counted one by one because `git clean -fd` would
	// delete each of them. Ignored files are not: nothing here deletes them.
	out, err := runGitOutputContext(ctx, "-C", path, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return nil, fmt.Errorf("inspect existing checkout %s: git status: %w", path, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if line != "" {
			result.UncommittedFiles++
		}
	}
	if result.UnpushedCommits, err = countUnpushedCommitsContext(ctx, path, "HEAD"); err != nil {
		return nil, fmt.Errorf("inspect existing checkout %s: %w", path, err)
	}
	return result, nil
}

// countUnpushedCommitsContext counts the commits reachable from ref that no
// remote-tracking ref reaches: work whose only copy is this repository.
func countUnpushedCommitsContext(ctx context.Context, repoPath, ref string) (int, error) {
	out, err := runGitOutputContext(ctx, "-C", repoPath, "rev-list", "--count", ref, "--not", "--remotes")
	if err != nil {
		return 0, fmt.Errorf("count unpushed commits on %s: %w", ref, err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("count unpushed commits on %s: %w", ref, err)
	}
	return count, nil
}

// isTaskBranch reports whether branch is the one CreateWorktree gives the task
// that owns branchName: that name itself, or the timestamp-suffixed name a
// branch collision retry picks (see updateExistingWorktree).
func isTaskBranch(branch, branchName string) bool {
	if branch == branchName {
		return true
	}
	suffix, ok := strings.CutPrefix(branch, branchName+"-")
	if !ok || suffix == "" {
		return false
	}
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// updateExistingWorktree resets the worktree to a clean state and checks out a
// new branch from the default branch, discarding uncommitted changes and
// untracked files. Callers go through updateExistingCheckoutContext, which
// reaches it only when that loses nothing or the caller asked for fresh. The
// caller is responsible for fetching the bare cache beforehand (worktrees share
// the same object store). Returns the actual branch name used (may differ from
// input on collision).
func updateExistingWorktree(worktreePath, branchName, baseRef string) (string, error) {
	return updateExistingWorktreeContext(context.Background(), worktreePath, branchName, baseRef)
}

func updateExistingWorktreeContext(ctx context.Context, worktreePath, branchName, baseRef string) (string, error) {
	// Discard any leftover uncommitted changes from the previous task.
	if out, err := runGitCombinedOutputContext(ctx, "-C", worktreePath, "reset", "--hard"); err != nil {
		return "", fmt.Errorf("git reset --hard: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// Clean untracked files (e.g. build artifacts from previous task).
	if out, err := runGitCombinedOutputContext(ctx, "-C", worktreePath, "clean", "-fd"); err != nil {
		return "", fmt.Errorf("git clean -fd: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// Create a new branch from the resolved default-branch ref and switch to
	// it. baseRef is a ref path returned by getRemoteDefaultBranch — usually
	// "refs/remotes/origin/<branch>" but may be "refs/heads/<branch>" on a
	// legacy/migration-pending cache. Either form is valid as a checkout
	// startpoint.
	out, err := runGitCombinedOutputContext(ctx, "-C", worktreePath, "checkout", "-b", branchName, baseRef)
	if err == nil {
		return branchName, nil
	}
	wrapped := fmt.Errorf("git checkout -b: %s: %w", strings.TrimSpace(string(out)), err)
	if !isBranchCollisionError(wrapped) {
		return "", wrapped
	}
	// Branch name collision: append timestamp and retry once.
	branchName = fmt.Sprintf("%s-%d", branchName, time.Now().Unix())
	if out2, err2 := runGitCombinedOutputContext(ctx, "-C", worktreePath, "checkout", "-b", branchName, baseRef); err2 != nil {
		return "", fmt.Errorf("git checkout -b (retry): %s: %w", strings.TrimSpace(string(out2)), err2)
	}
	return branchName, nil
}

// getRemoteDefaultBranch returns a ref path (e.g. "refs/remotes/origin/main")
// that points at the remote's default branch in a bare cache. The return value
// is usable directly as a `git worktree add` / `git checkout -b` startpoint.
//
// Resolution order:
//  1. refs/remotes/origin/HEAD (verified; set by `git remote set-head origin --auto`)
//  2. refs/remotes/origin/main, refs/remotes/origin/master (common defaults)
//  3. The bare repo's own HEAD mapped into refs/remotes/origin/<same name> —
//     `git clone --bare` sets HEAD to the remote's default, so this is a
//     reliable hint for custom default branches (trunk, develop, …) when
//     `git remote set-head --auto` failed to populate refs/remotes/origin/HEAD.
//  4. Scan refs/remotes/origin/* — returns a result ONLY when exactly one
//     non-HEAD ref exists. Multiple refs cannot be disambiguated from refname
//     order alone (git for-each-ref sorts alphabetically), so we refuse to
//     guess; returning a wrong default would silently base new agent work on
//     an arbitrary feature branch.
//  5. Legacy last-resort: the bare repo's own HEAD as a plain refs/heads/*
//     ref, for caches that haven't populated refs/remotes/origin/* at all
//     yet (e.g. a migration-pending cache whose backfill fetch failed).
//     Gated on refs/remotes/origin/* being completely empty so we don't fall
//     back to a stale snapshot when the cache has real remote-tracking refs
//     but we just can't pick between them.
//
// Returns "" only when none of the above resolve — which the caller treats
// as a hard error with a clear "cache has no usable refs" message.
func getRemoteDefaultBranch(barePath string) string {
	return getRemoteDefaultBranchContext(context.Background(), barePath)
}

func getRemoteDefaultBranchContext(ctx context.Context, barePath string) string {
	// 1) Primary: refs/remotes/origin/HEAD set by `git remote set-head
	//    origin --auto` during ensureRemoteTrackingLayout. Verify the
	//    target actually exists — a partial set-head or a manually-broken
	//    repo can leave a symref pointing at a deleted ref, and returning
	//    it here would later fail in `git worktree add` with a confusing
	//    "invalid reference" error.
	if out, err := runGitOutputContext(ctx, "-C", barePath, "symbolic-ref", "refs/remotes/origin/HEAD"); err == nil {
		ref := strings.TrimSpace(string(out))
		if ref != "" {
			if err := runGitContext(ctx, "-C", barePath, "rev-parse", "--verify", ref); err == nil {
				return ref
			}
		}
	}
	// 2) Common default branch names under the origin namespace.
	for _, candidate := range []string{"refs/remotes/origin/main", "refs/remotes/origin/master"} {
		if err := runGitContext(ctx, "-C", barePath, "rev-parse", "--verify", candidate); err == nil {
			return candidate
		}
	}
	// 3) Use the bare repo's own HEAD as a hint. `git clone --bare` sets HEAD
	//    to the remote's default branch, so this reliably identifies custom
	//    default branch names (trunk, develop, ...) when set-head --auto
	//    didn't populate refs/remotes/origin/HEAD. We only return when the
	//    matching origin/<name> exists, so we still pick up up-to-date code
	//    rather than a stale local head.
	bareRef := bareHeadBranchContext(ctx, barePath)
	if bareRef != "" {
		originRef := "refs/remotes/origin/" + strings.TrimPrefix(bareRef, "refs/heads/")
		if err := runGitContext(ctx, "-C", barePath, "rev-parse", "--verify", originRef); err == nil {
			return originRef
		}
	}
	// 4) Scan refs/remotes/origin/* — return a result ONLY when there's
	//    exactly one non-HEAD candidate. Multiple candidates cannot be
	//    disambiguated from refname order alone; returning the alphabetically-
	//    first entry would silently base new agent work on a feature branch
	//    instead of the real default. Count entries here so step 5 can tell
	//    "legacy empty" apart from "ambiguous".
	originCount := 0
	var singleton string
	if out, err := runGitOutputContext(ctx, "-C", barePath, "for-each-ref", "--format=%(refname)", "refs/remotes/origin/"); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || line == "refs/remotes/origin/HEAD" {
				continue
			}
			originCount++
			if singleton == "" {
				singleton = line
			}
		}
		if originCount == 1 {
			return singleton
		}
	}
	// 5) Last-resort fallback: legacy / migration-pending caches still have
	//    refs/heads/* and a bare HEAD from the mirror-style layout. Gate this
	//    on refs/remotes/origin/* being completely empty — if origin/* has
	//    multiple refs but none match bare HEAD, the cache is in an
	//    ambiguous state and returning the local head would mask the
	//    problem with a stale snapshot. Let the caller fail loudly instead.
	if originCount == 0 && bareRef != "" {
		return bareRef
	}
	return ""
}

// bareHeadBranch returns the bare repo's local HEAD ref (e.g.
// "refs/heads/main") if HEAD is a symbolic ref to an existing branch.
// Returns "" if HEAD is detached, missing, or points at a non-existent ref.
//
// Only used by getRemoteDefaultBranch as a last-resort fallback for caches
// that haven't successfully populated refs/remotes/origin/* yet. Healthy
// modern caches should never reach this path because origin/* resolution
// succeeds first.
func bareHeadBranch(barePath string) string {
	return bareHeadBranchContext(context.Background(), barePath)
}

func bareHeadBranchContext(ctx context.Context, barePath string) string {
	out, err := runGitOutputContext(ctx, "-C", barePath, "symbolic-ref", "HEAD")
	if err != nil {
		return ""
	}
	ref := strings.TrimSpace(string(out))
	if ref == "" {
		return ""
	}
	if err := runGitContext(ctx, "-C", barePath, "rev-parse", "--verify", ref); err != nil {
		return ""
	}
	return ref
}

// multicaHookMarker is a sentinel comment embedded in every prepare-commit-msg
// hook installed by the daemon. removeCoAuthoredByHook uses it to recognize
// hooks it owns so it never deletes a hook installed by the user or another
// tool. Do not change without bumping the recognition logic.
const multicaHookMarker = "# multica:prepare-commit-msg:co-authored-by"

// daemonInstalledHookSignatures lists substrings that identify a
// prepare-commit-msg hook as one the daemon installed. removeCoAuthoredByHook
// treats a hook as Multica-owned if its content contains ANY of these
// substrings. The list deliberately includes the legacy comment that the
// daemon used before multicaHookMarker existed, so disabling the toggle on
// existing installations still cleans up old hooks seeded by previous daemon
// versions. Add to this list — never remove from it — so future tweaks to
// prepareCommitMsgHook keep recognizing every previously-shipped variant.
var daemonInstalledHookSignatures = []string{
	multicaHookMarker,
	"# Installed by the Multica daemon.",
}

// coAuthoredByStateFile records the workspace's current Co-authored-by setting
// for hooks that are already on disk. It lives beside the workspace's bare
// caches (`<root>/<workspace-id>/`) and is rewritten every time the daemon
// learns the setting, so a checkout made before the toggle was flipped still
// commits under the current value. Without it the decision would be frozen
// into the hook file at checkout time (MUL-6921).
const (
	coAuthoredByStateFile     = ".multica_co_authored_by"
	coAuthoredByStateEnabled  = "1"
	coAuthoredByStateDisabled = "0"
)

// CoAuthoredByStatePath returns the file the prepare-commit-msg hook consults
// at commit time for a workspace. Empty when the workspace is unknown, which
// installs a hook with no gate — the pre-MUL-6921 behavior.
func (c *Cache) CoAuthoredByStatePath(workspaceID string) string {
	if workspaceID == "" {
		return ""
	}
	return filepath.Join(c.root, workspaceID, coAuthoredByStateFile)
}

// WriteCoAuthoredByState publishes the workspace's current setting to every
// prepare-commit-msg hook the daemon has installed for it, including hooks in
// checkouts this call knows nothing about. The daemon calls it whenever it
// refreshes workspace settings, which is what makes the toggle apply without
// waiting for the repo to be checked out again.
//
// The write is atomic (temp file + rename) so a hook running concurrently
// reads either the old value or the new one, never a half-written file.
func (c *Cache) WriteCoAuthoredByState(workspaceID string, enabled bool) error {
	path := c.CoAuthoredByStatePath(workspaceID)
	if path == "" {
		return nil
	}
	value := coAuthoredByStateDisabled
	if enabled {
		value = coAuthoredByStateEnabled
	}
	if current, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(current)) == value {
		// Already published. The daemon republishes on every workspace sync so
		// the file survives cache GC and daemon restarts; skipping the rewrite
		// keeps that cheap.
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create workspace cache dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, coAuthoredByStateFile+".*")
	if err != nil {
		return fmt.Errorf("create co-authored-by state temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(value + "\n"); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write co-authored-by state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("write co-authored-by state: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("publish co-authored-by state: %w", err)
	}
	return nil
}

// applyCoAuthoredBySettingContext reconciles this checkout's hook file with the
// workspace setting.
//
// It deliberately does NOT publish the state file. params.CoAuthoredByEnabled
// is a snapshot the handler took before fetch and lock waits, so a checkout can
// finish long after the value it captured stopped being true; writing that
// snapshot to the workspace-wide state file would let a slow checkout resurrect
// a trailer the user has since turned off. The daemon is the only publisher —
// it holds the ordering between settings updates — and this reads what the
// daemon published, falling back to the snapshot only when nothing has been
// published yet (a cache driven without a daemon, or a state file removed under
// us).
func (c *Cache) applyCoAuthoredBySettingContext(ctx context.Context, worktreePath string, params WorktreeParams) {
	enabled := params.CoAuthoredByEnabled
	if published, ok := c.readCoAuthoredByState(params.WorkspaceID); ok {
		enabled = published
	}
	if enabled {
		if err := installCoAuthoredByHookContext(ctx, worktreePath, c.CoAuthoredByStatePath(params.WorkspaceID)); err != nil {
			c.logger.Warn("repo checkout: install co-authored-by hook failed (non-fatal)", "error", err)
		}
		return
	}
	if err := removeCoAuthoredByHookContext(ctx, worktreePath); err != nil {
		c.logger.Warn("repo checkout: remove co-authored-by hook failed (non-fatal)", "error", err)
	}
}

// readCoAuthoredByState reports the setting the daemon last published for a
// workspace. ok is false when nothing has been published — the caller decides
// what "unknown" means for it.
func (c *Cache) readCoAuthoredByState(workspaceID string) (enabled, ok bool) {
	path := c.CoAuthoredByStatePath(workspaceID)
	if path == "" {
		return false, false
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return false, false
	}
	switch strings.TrimSpace(string(contents)) {
	case coAuthoredByStateEnabled:
		return true, true
	case coAuthoredByStateDisabled:
		return false, true
	default:
		return false, false
	}
}

// ReconcileCoAuthoredByHooks brings the hooks in a workspace's bare caches in
// line with enabled, without waiting for those repos to be checked out again.
//
// This is the migration path for hooks installed by earlier daemon versions:
// they predate the state file and read nothing at commit time, so publishing a
// new value cannot reach them. Enabled rewrites them to the current gated
// script (a later toggle-off then applies at commit time); disabled deletes
// them. Hooks the daemon does not own are never touched.
//
// Only bare caches are enumerable here — the cache root is all this type
// knows. Isolated checkouts keep their hook inside a task workdir; the daemon
// finds those and reconciles them one at a time through
// ReconcileCoAuthoredByHookInCheckout.
func (c *Cache) ReconcileCoAuthoredByHooks(workspaceID string, enabled bool) error {
	if workspaceID == "" {
		return nil
	}
	wsDir := filepath.Join(c.root, workspaceID)
	entries, err := os.ReadDir(wsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read workspace cache dir: %w", err)
	}

	var firstErr error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		barePath := filepath.Join(wsDir, entry.Name())
		if !IsReady(barePath) {
			continue
		}
		if err := c.reconcileHookAt(filepath.Join(barePath, "hooks"), workspaceID, enabled); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ReconcileCoAuthoredByHookInCheckout applies the workspace setting to a single
// checkout that owns its git metadata — an isolated checkout, whose .git lives
// in the task workdir instead of the shared bare cache and is therefore
// invisible to ReconcileCoAuthoredByHooks. The daemon knows where those
// workdirs are and calls this for each one it finds.
//
// A linked worktree has a .git FILE pointing at the bare cache; it is skipped
// here because its hook is reconciled through the bare cache instead.
func (c *Cache) ReconcileCoAuthoredByHookInCheckout(checkoutPath, workspaceID string, enabled bool) error {
	if checkoutPath == "" || workspaceID == "" {
		return nil
	}
	gitDir := filepath.Join(checkoutPath, ".git")
	info, err := os.Stat(gitDir)
	if err != nil || !info.IsDir() {
		return nil
	}
	return c.reconcileHookAt(filepath.Join(gitDir, "hooks"), workspaceID, enabled)
}

// reconcileHookAt applies the workspace setting to one hooks directory. It
// leaves a hook the daemon did not install alone, and never creates a hooks
// directory it did not find.
func (c *Cache) reconcileHookAt(hooksDir, workspaceID string, enabled bool) error {
	hookPath := filepath.Join(hooksDir, "prepare-commit-msg")
	current, err := os.ReadFile(hookPath)
	switch {
	case err == nil && !isDaemonInstalledHook(current):
		return nil // user or third-party hook: not ours to reconcile
	case err != nil && !os.IsNotExist(err):
		return fmt.Errorf("read prepare-commit-msg hook: %w", err)
	}
	exists := err == nil

	if !enabled {
		if !exists {
			return nil
		}
		if err := os.Remove(hookPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove prepare-commit-msg hook: %w", err)
		}
		return nil
	}

	want := prepareCommitMsgHook(c.CoAuthoredByStatePath(workspaceID))
	if exists && string(current) == want {
		return nil
	}
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return fmt.Errorf("create hooks dir: %w", err)
	}
	if err := writeHookFile(hookPath, want); err != nil {
		return fmt.Errorf("write prepare-commit-msg hook: %w", err)
	}
	return nil
}

// prepareCommitMsgHook builds the prepare-commit-msg hook script that appends
// a Co-authored-by trailer for the Multica Agent to every commit message.
//
// The script re-reads statePath on every commit instead of trusting its own
// presence on disk: the hook is installed in the git common directory and
// outlives the checkout that created it, so a workspace toggled off after the
// checkout must still stop the trailer at the very next commit — including
// commits the daemon never sees, and `git commit --no-verify`, which bypasses
// pre-commit and commit-msg but not prepare-commit-msg.
//
// A missing or unreadable state file keeps the trailer: the hook only exists
// because a checkout ran with the setting enabled, so "no state recorded" must
// mean the same thing it meant before the state file existed.
func prepareCommitMsgHook(statePath string) string {
	var gate string
	if statePath != "" {
		gate = fmt.Sprintf(`# Current workspace setting, refreshed by the daemon. "0" means the
# Co-authored-by toggle is off — leave the message alone.
STATE_FILE=%s
if [ -f "$STATE_FILE" ]; then
  case "$(cat "$STATE_FILE" 2>/dev/null)" in
    0) exit 0 ;;
  esac
fi

`, shellSingleQuoted(filepath.ToSlash(statePath)))
	}
	return `#!/bin/sh
# multica:prepare-commit-msg:co-authored-by
# Multica: add Co-authored-by trailer for the Multica Agent.
# Installed by the Multica daemon. Do not edit — it will be overwritten.

COMMIT_MSG_FILE="$1"
COMMIT_SOURCE="$2"

# Skip merge and squash commits.
case "$COMMIT_SOURCE" in
  merge|squash) exit 0 ;;
esac

` + gate + `TRAILER="Co-authored-by: multica-agent <github@multica.ai>"

# Don't add if already present.
if grep -qF "$TRAILER" "$COMMIT_MSG_FILE"; then
  exit 0
fi

# Use git interpret-trailers for proper formatting.
git interpret-trailers --in-place --trailer "$TRAILER" "$COMMIT_MSG_FILE"
`
}

// shellSingleQuoted renders s as a POSIX shell single-quoted literal so a path
// with spaces or shell metacharacters survives being embedded in the hook.
func shellSingleQuoted(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// installCoAuthoredByHook installs a prepare-commit-msg git hook that appends
// a Co-authored-by trailer for the Multica Agent. The hook is installed in the
// git common directory (the bare repo for worktrees) so it applies to all
// worktrees created from this cache.
//
// statePath is the file the hook re-reads on every commit to decide whether
// the trailer is still wanted; pass "" to install an ungated hook.
func installCoAuthoredByHook(worktreePath, statePath string) error {
	return installCoAuthoredByHookContext(context.Background(), worktreePath, statePath)
}

func installCoAuthoredByHookContext(ctx context.Context, worktreePath, statePath string) error {
	out, err := runGitOutputContext(ctx, "-C", worktreePath, "rev-parse", "--git-common-dir")
	if err != nil {
		return fmt.Errorf("resolve git common dir: %w", err)
	}
	commonDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(worktreePath, commonDir)
	}

	hooksDir := filepath.Join(commonDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return fmt.Errorf("create hooks dir: %w", err)
	}

	hookPath := filepath.Join(hooksDir, "prepare-commit-msg")
	if err := writeHookFile(hookPath, prepareCommitMsgHook(statePath)); err != nil {
		return fmt.Errorf("write prepare-commit-msg hook: %w", err)
	}
	return nil
}

// writeHookFile publishes a hook script by rename. A plain truncate-and-write
// would be visible to a commit running at that moment as a half-written
// script, and this path rewrites hooks in repos with checkouts live on them.
func writeHookFile(hookPath, contents string) error {
	dir := filepath.Dir(hookPath)
	tmp, err := os.CreateTemp(dir, ".multica-hook-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(contents); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, hookPath); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// isDaemonInstalledHook reports whether a prepare-commit-msg hook on disk was
// installed by the Multica daemon (current or any previously released
// version). It returns false for hooks that don't carry any known daemon
// signature, so a user-installed hook at the same path is left alone.
func isDaemonInstalledHook(contents []byte) bool {
	body := string(contents)
	for _, sig := range daemonInstalledHookSignatures {
		if strings.Contains(body, sig) {
			return true
		}
	}
	return false
}

// removeCoAuthoredByHook removes the prepare-commit-msg hook installed by
// installCoAuthoredByHook. It only deletes the file when the content matches
// a known daemon signature (current marker or any previously released hook
// content), so a user-installed prepare-commit-msg hook is never touched.
// Returns nil when no hook is present or when an unrelated hook occupies
// the path.
func removeCoAuthoredByHook(worktreePath string) error {
	return removeCoAuthoredByHookContext(context.Background(), worktreePath)
}

func removeCoAuthoredByHookContext(ctx context.Context, worktreePath string) error {
	out, err := runGitOutputContext(ctx, "-C", worktreePath, "rev-parse", "--git-common-dir")
	if err != nil {
		return fmt.Errorf("resolve git common dir: %w", err)
	}
	commonDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(worktreePath, commonDir)
	}

	hookPath := filepath.Join(commonDir, "hooks", "prepare-commit-msg")
	contents, err := os.ReadFile(hookPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read prepare-commit-msg hook: %w", err)
	}
	if !isDaemonInstalledHook(contents) {
		// Unrelated hook (user or third-party): leave it alone.
		return nil
	}
	if err := os.Remove(hookPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove prepare-commit-msg hook: %w", err)
	}
	return nil
}

// excludeFromGit adds a pattern to the worktree's .git/info/exclude file.
func excludeFromGit(worktreePath, pattern string) error {
	return excludeFromGitContext(context.Background(), worktreePath, pattern)
}

func excludeFromGitContext(ctx context.Context, worktreePath, pattern string) error {
	out, err := runGitOutputContext(ctx, "-C", worktreePath, "rev-parse", "--git-dir")
	if err != nil {
		return fmt.Errorf("resolve git dir: %w", err)
	}

	gitDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(worktreePath, gitDir)
	}

	excludePath := filepath.Join(gitDir, "info", "exclude")

	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return fmt.Errorf("create info dir: %w", err)
	}

	existing, _ := os.ReadFile(excludePath)
	if strings.Contains(string(existing), pattern) {
		return nil
	}

	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open exclude file: %w", err)
	}
	defer f.Close()

	if _, err := fmt.Fprintf(f, "\n%s\n", pattern); err != nil {
		return fmt.Errorf("write exclude pattern: %w", err)
	}
	return nil
}

// repoNameFromURL extracts a short directory name from a git remote URL.
// e.g. "https://github.com/org/my-repo.git" → "my-repo"
func repoNameFromURL(url string) string {
	url = strings.TrimRight(url, "/")
	url = strings.TrimSuffix(url, ".git")

	if i := strings.LastIndex(url, "/"); i >= 0 {
		url = url[i+1:]
	}
	if i := strings.LastIndex(url, ":"); i >= 0 {
		url = url[i+1:]
		if j := strings.LastIndex(url, "/"); j >= 0 {
			url = url[j+1:]
		}
	}

	name := strings.TrimSpace(url)
	if name == "" {
		return "repo"
	}
	return name
}

var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

// sanitizeName produces a git-branch-safe name from a human-readable string.
func sanitizeName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = nonAlphanumeric.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 30 {
		s = s[:30]
		s = strings.TrimRight(s, "-")
	}
	if s == "" {
		s = "agent"
	}
	return s
}

// taskKeyLen mirrors execenv.taskKeyLen — see that constant for why the
// segment is short: a branch name becomes a path under .git/refs/heads/ inside
// the task checkout, and Windows enforces MAX_PATH there.
const taskKeyLen = 12

// taskKey returns the git-safe branch segment identifying a task: the LAST
// taskKeyLen hex chars of the id. Mirrors execenv.taskKey — a UUIDv7's LEADING
// 8 hex chars are timestamp bits that only advance every ~65.5s, so taking the
// front gave two concurrently created tasks the same branch name (#7326). The
// tail is random.
func taskKey(uuid string) string {
	s := strings.ReplaceAll(uuid, "-", "")
	if len(s) > taskKeyLen {
		return s[len(s)-taskKeyLen:]
	}
	return s
}

// shortID returns the first 8 characters of a UUID string (dashes stripped).
// Display and logging only — see taskKey for anything that must be unique.
func shortID(uuid string) string {
	s := strings.ReplaceAll(uuid, "-", "")
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
