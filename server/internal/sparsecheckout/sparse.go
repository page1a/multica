// Package sparsecheckout limits a task worktree to the directories that task
// declared, plus the files cone mode always keeps at the repository root
// (lockfiles, workspace manifests, root build config).
//
// An empty declaration is a full checkout. That is the historical behavior
// and stays the default so a task that says nothing is unchanged.
//
// Reading a path that was left out must not look like "this file is not in
// the repository". Excluded directories carry a marker explaining that, and
// Materialize checks the path out or says, in so many words, that it is not
// in HEAD.
package sparsecheckout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// MetadataKey is the issue-metadata key a task uses to declare the
	// directories it needs. The value is a string of repo-relative paths
	// separated by commas or newlines. Metadata values are scalars, so this
	// is a string rather than a JSON array.
	MetadataKey = "checkout_paths"

	// EnvVar carries the same declaration into the agent process so a checkout
	// command and a person reading the environment see the same paths.
	EnvVar = "MULTICA_CHECKOUT_PATHS"

	// MarkerName is the file written into each directory the cone left out.
	// Its text is the behavior a reader gets instead of a silent absence.
	MarkerName = "MULTICA_SPARSE_EXCLUDED.txt"

	excludeFileName = "multica-sparse-exclude"

	// SkippedKept means a declaration would have removed files from a checkout
	// that was left in place, so the cone was not narrowed.
	SkippedKept = "kept"
	// SkippedRestored means an empty declaration found a sparse checkout and
	// brought the rest of the repository back onto disk.
	SkippedRestored = "restored"
	// SkippedWidened means a broader declaration was applied without moving
	// the branch or discarding the checkout's own work.
	SkippedWidened = "widened"
)

// Scope is a parsed declaration. The zero value checks out the whole tree.
type Scope struct {
	// Paths are cleaned repo-relative paths, in declaration order, deduped.
	Paths []string
}

// Active reports whether the declaration narrows the checkout.
func (s Scope) Active() bool { return len(s.Paths) > 0 }

// Parse splits a declaration. An empty string is a full checkout, not an error.
// "." alone is also a full checkout: it names the whole repository.
func Parse(raw string) (Scope, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Scope{}, nil
	}
	var paths []string
	seen := map[string]bool{}
	for _, token := range splitDeclaration(raw) {
		cleaned, err := cleanPath(token)
		if err != nil {
			return Scope{}, err
		}
		if cleaned == "." {
			// Declaring the repository root means "everything". Mixed with a
			// narrower path it would silently drop that path, so reject the mix.
			if len(paths) > 0 {
				return Scope{}, fmt.Errorf("checkout path %q names the whole repository and cannot be combined with a narrower path", token)
			}
			return Scope{}, nil
		}
		if seen[cleaned] {
			continue
		}
		seen[cleaned] = true
		paths = append(paths, cleaned)
	}
	if len(paths) == 0 {
		return Scope{}, fmt.Errorf("checkout_paths %q did not contain a path", raw)
	}
	return Scope{Paths: paths}, nil
}

func splitDeclaration(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}

func cleanPath(token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", errors.New("checkout path is empty")
	}
	slash := strings.ReplaceAll(token, "\\", "/")
	if strings.HasPrefix(slash, "/") || filepath.IsAbs(token) {
		return "", fmt.Errorf("checkout path %q must be relative to the repository root", token)
	}
	cleaned := pathClean(slash)
	if cleaned == "" || cleaned == "." {
		return ".", nil
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("checkout path %q escapes the repository", token)
	}
	return cleaned, nil
}

// pathClean is filepath.Clean on slash-separated input, still slash-separated.
func pathClean(slash string) string {
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(slash)))
	return strings.TrimPrefix(cleaned, "./")
}

// MetadataString reads MetadataKey from an issue metadata blob.
// A missing key is an empty string. A present non-string is an error: a
// number or bool here would otherwise fall through to a full checkout and
// look like the declaration had been honored.
func MetadataString(raw []byte) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", fmt.Errorf("issue metadata: %w", err)
	}
	value, ok := obj[MetadataKey]
	if !ok || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("issue metadata %s must be a string of comma-separated paths", MetadataKey)
	}
	return strings.TrimSpace(text), nil
}

// Enable configures cone-mode sparse checkout. On a worktree that already has
// files, git itself updates the worktree to match. On a worktree created with
// --no-checkout, the caller must CheckoutCurrent afterwards: set alone records
// the cone and leaves the index looking deleted until that checkout.
//
// sparse-checkout set turns on extensions.worktreeConfig so the cone is stored
// for this worktree only. The repository the worktree was added from keeps
// every file.
func Enable(ctx context.Context, repo string, scope Scope) error {
	if !scope.Active() {
		return nil
	}
	dirs, rootOnly, err := classify(ctx, repo, scope.Paths)
	if err != nil {
		return err
	}
	if rootOnly {
		return gitStdin(ctx, repo, strings.NewReader(""), "sparse-checkout", "set", "--cone", "--stdin")
	}
	args := append([]string{"sparse-checkout", "set", "--cone"}, dirs...)
	return git(ctx, repo, args...)
}

// CheckoutCurrent materializes the current branch under the cone Enable just
// set. It is the step a --no-checkout worktree still needs.
func CheckoutCurrent(ctx context.Context, repo string) error {
	return git(ctx, repo, "checkout")
}

// Finish writes the exclusion markers and a worktree-local exclude file so
// those markers stay out of git status. info/exclude is shared by every
// worktree of a repository, which is why this uses core.excludesFile in the
// worktree config instead: writing the shared file would change what the
// user's own checkout reports.
func Finish(ctx context.Context, repo string) error {
	if !isSparse(ctx, repo) {
		return nil
	}
	top, err := gitStdout(ctx, repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	top = strings.TrimSpace(top)
	included, err := includedDirs(ctx, repo)
	if err != nil {
		return err
	}
	if err := removeMarkers(top); err != nil {
		return err
	}
	if err := writeMarkers(ctx, top, included); err != nil {
		return err
	}
	return installExclude(ctx, repo)
}

// Apply narrows a worktree that already has its tree checked out.
func Apply(ctx context.Context, repo string, scope Scope) error {
	if !scope.Active() {
		return nil
	}
	if err := Enable(ctx, repo, scope); err != nil {
		return err
	}
	return Finish(ctx, repo)
}

// Reconcile makes an existing checkout match scope.
//
// allowNarrow is false when the checkout is being kept: its branch and
// uncommitted work stay. Adding files is still done. An empty scope turns
// sparse checkout off, and a scope that already covers the current cone
// widens it. A scope that would drop directories returns SkippedKept and
// changes nothing — removing files waits for a checkout that is allowed to
// start over.
//
// allowNarrow is true when the checkout was just reset onto a new branch.
// The cone is then set exactly, including a narrower one, and an empty scope
// restores the whole tree.
//
// The returned value is "", SkippedKept, SkippedRestored, or SkippedWidened.
// "" means the checkout already matches scope: a full tree, or the same cone.
func Reconcile(ctx context.Context, repo string, scope Scope, allowNarrow bool) (string, error) {
	sparse := isSparse(ctx, repo)
	if !scope.Active() {
		if !sparse {
			return "", nil
		}
		if err := restoreFull(ctx, repo); err != nil {
			return "", err
		}
		return SkippedRestored, nil
	}
	if !allowNarrow {
		if !sparse {
			return SkippedKept, nil
		}
		current, err := includedDirs(ctx, repo)
		if err != nil {
			return "", err
		}
		requested, err := coneDirs(ctx, repo, scope)
		if err != nil {
			return "", err
		}
		if !coneCovers(current, requested) {
			return SkippedKept, nil
		}
		if coneCovers(requested, current) {
			return "", nil
		}
		if err := Apply(ctx, repo, scope); err != nil {
			return "", err
		}
		return SkippedWidened, nil
	}
	if err := Apply(ctx, repo, scope); err != nil {
		return "", err
	}
	return "", nil
}

// restoreFull turns sparse checkout off and materializes every file. The
// marker files and the worktree exclude that hid them go with it, so a
// restored checkout does not keep overriding the user's global excludes.
func restoreFull(ctx context.Context, repo string) error {
	if err := git(ctx, repo, "sparse-checkout", "disable"); err != nil {
		return err
	}
	top, err := gitStdout(ctx, repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	top = strings.TrimSpace(top)
	if err := removeMarkers(top); err != nil {
		return err
	}
	return clearOwnExclude(ctx, repo)
}

func clearOwnExclude(ctx context.Context, repo string) error {
	out, err := gitStdout(ctx, repo, "config", "--worktree", "--get", "core.excludesFile")
	if err != nil {
		return nil
	}
	path := strings.TrimSpace(out)
	if filepath.Base(path) != excludeFileName {
		return nil
	}
	if err := git(ctx, repo, "config", "--worktree", "--unset", "core.excludesFile"); err != nil {
		return err
	}
	if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		return rmErr
	}
	return nil
}

// coneDirs is the set of directories Enable would pass to sparse-checkout.
// A declaration of only root files is an empty set: cone mode already keeps
// every root file.
func coneDirs(ctx context.Context, repo string, scope Scope) ([]string, error) {
	dirs, rootOnly, err := classify(ctx, repo, scope.Paths)
	if err != nil {
		return nil, err
	}
	if rootOnly {
		return nil, nil
	}
	return dirs, nil
}

// coneCovers reports whether every directory in have sits inside want.
// An empty want is the root-only cone, which covers nothing below the root.
func coneCovers(have, want []string) bool {
	for _, dir := range have {
		if !dirCovered(dir, want) {
			return false
		}
	}
	return true
}

func dirCovered(dir string, cones []string) bool {
	for _, cone := range cones {
		if cone == dir || strings.HasPrefix(dir, cone+"/") {
			return true
		}
	}
	return false
}

// Materialize checks out one path the cone left behind.
//
// A path that is not in HEAD returns PathNotInRepositoryError: that is the
// explicit "it is not in the repository" answer, as opposed to the file
// simply being absent on disk. A checkout that was never sparse returns
// NotSparseError, because adding a directory there would turn a full tree
// into a cone of one directory.
func Materialize(ctx context.Context, repo, request string) (string, error) {
	top, err := gitStdout(ctx, repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("sparse-add must run inside a git checkout: %w", err)
	}
	top = strings.TrimSpace(top)
	if resolved, err := filepath.EvalSymlinks(top); err == nil {
		top = resolved
	}
	if !isSparse(ctx, top) {
		return "", &NotSparseError{}
	}
	rel, err := requestPath(ctx, top, repo, request)
	if err != nil {
		return "", err
	}
	kind, missing := objectKind(ctx, top, rel)
	if missing {
		return "", &PathNotInRepositoryError{Path: rel}
	}
	dir := rel
	if kind == "blob" {
		dir = pathClean(pathDir(rel))
		if dir == "." || dir == "" {
			return fmt.Sprintf("%s is a root file. Sparse checkout already has every root file on disk.", rel), nil
		}
	}
	if err := git(ctx, top, "sparse-checkout", "add", dir); err != nil {
		return "", err
	}
	if err := Finish(ctx, top); err != nil {
		return "", err
	}
	if _, statErr := os.Stat(filepath.Join(top, filepath.FromSlash(rel))); statErr != nil {
		return "", fmt.Errorf("sparse checkout added %s but %s is still not on disk: %w", dir, rel, statErr)
	}
	return fmt.Sprintf("Checked out %s. It was in the repository and outside this checkout's sparse cone; the rest of %s is on disk now too.", rel, dir), nil
}

// WidenToCover brings repo-relative paths inside the cone before something
// writes them. A cherry-pick of a file outside the cone records the change
// in the index and leaves the file off disk, which is the silent miss this
// exists to prevent.
//
// Paths that are not in HEAD yet (a user's new untracked file) are added
// with --skip-checks. Refusing them would drop the user's edits on the floor.
// A checkout that is not sparse is left alone.
func WidenToCover(ctx context.Context, repo string, paths []string) error {
	if !isSparse(ctx, repo) || len(paths) == 0 {
		return nil
	}
	var dirs []string
	seen := map[string]bool{}
	for _, raw := range paths {
		cleaned, err := cleanPath(strings.TrimSpace(raw))
		if err != nil || cleaned == "." || cleaned == "" {
			continue
		}
		dir := cleaned
		if kind, missing := objectKind(ctx, repo, cleaned); !missing && kind == "blob" {
			dir = pathClean(pathDir(cleaned))
		} else if missing {
			dir = pathClean(pathDir(cleaned))
		}
		if dir == "." || dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	if len(dirs) == 0 {
		return nil
	}
	args := append([]string{"sparse-checkout", "add", "--skip-checks"}, dirs...)
	if err := git(ctx, repo, args...); err != nil {
		return err
	}
	return Finish(ctx, repo)
}

// NotSparseError means Materialize was asked to widen a full checkout.
type NotSparseError struct{}

func (e *NotSparseError) Error() string {
	return "this checkout already has the whole repository on disk; there is nothing sparse to add"
}

// PathNotInRepositoryError means the path is absent from HEAD, not merely
// left out of the sparse checkout.
type PathNotInRepositoryError struct {
	Path string
}

func (e *PathNotInRepositoryError) Error() string {
	return fmt.Sprintf("%s is not in this repository at HEAD. The sparse checkout did not hide it; there is no such file to check out.", e.Path)
}

// DeclaredPathError means a path the task declared is not in the repository.
// Enabling the cone anyway would check out an empty tree and look like the
// module had no files.
type DeclaredPathError struct {
	Path string
}

func (e *DeclaredPathError) Error() string {
	return fmt.Sprintf("declared checkout path %q is not in the repository. The checkout was not narrowed, because a typo would otherwise look like an empty module.", e.Path)
}

func classify(ctx context.Context, repo string, paths []string) (dirs []string, rootOnly bool, err error) {
	seen := map[string]bool{}
	for _, path := range paths {
		kind, missing := objectKind(ctx, repo, path)
		if missing {
			return nil, false, &DeclaredPathError{Path: path}
		}
		dir := path
		if kind == "blob" {
			dir = pathClean(pathDir(path))
		}
		if dir == "." || dir == "" {
			continue
		}
		if seen[dir] {
			continue
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	if len(dirs) == 0 {
		return nil, true, nil
	}
	return dirs, false, nil
}

func objectKind(ctx context.Context, repo, path string) (kind string, missing bool) {
	// ls-tree reads the tree, not the blob. A blobless clone still has the
	// trees, so a declared path is not reported missing just because its
	// bytes have not been fetched yet.
	out, err := gitStdout(ctx, repo, "ls-tree", "HEAD", "--", path)
	if err != nil {
		return "", true
	}
	line := strings.TrimSpace(out)
	if line == "" {
		return "", true
	}
	// "<mode> <type> <object>\t<name>" — the first line is the path itself.
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "", true
	}
	return fields[1], false
}

func includedDirs(ctx context.Context, repo string) ([]string, error) {
	out, err := gitStdout(ctx, repo, "sparse-checkout", "list")
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		line = strings.Trim(line, "/")
		if line == "" || line == "." {
			continue
		}
		dirs = append(dirs, pathClean(line))
	}
	return dirs, nil
}

func writeMarkers(ctx context.Context, top string, included []string) error {
	return markChildren(ctx, top, "", included)
}

func markChildren(ctx context.Context, top, dir string, included []string) error {
	spec := "HEAD"
	if dir != "" {
		spec = "HEAD:" + dir
	}
	out, err := gitStdout(ctx, top, "ls-tree", "-d", "--name-only", spec)
	if err != nil {
		return err
	}
	for _, name := range strings.Split(strings.TrimSpace(out), "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		full := name
		if dir != "" {
			full = dir + "/" + name
		}
		switch coverage(full, included) {
		case coverFull:
			continue
		case coverPartial:
			if err := markChildren(ctx, top, full, included); err != nil {
				return err
			}
		default:
			if err := writeMarker(top, full); err != nil {
				return err
			}
		}
	}
	return nil
}

const (
	coverNone = iota
	coverPartial
	coverFull
)

func coverage(dir string, included []string) int {
	best := coverNone
	for _, inc := range included {
		switch {
		case inc == dir || strings.HasPrefix(dir, inc+"/"):
			return coverFull
		case strings.HasPrefix(inc, dir+"/"):
			best = coverPartial
		}
	}
	return best
}

func writeMarker(top, dir string) error {
	host := filepath.Join(top, filepath.FromSlash(dir))
	if err := os.MkdirAll(host, 0o755); err != nil {
		return fmt.Errorf("mark excluded directory %s: %w", dir, err)
	}
	body := fmt.Sprintf(`This directory is in the git repository but was not checked out for this task.
The task asked for a sparse checkout. Files under %s are not missing from the repository.
To bring a path here onto disk, run:

    multica repo sparse-add %s

That command checks the path out when it exists in git, and says so when it does not.
Do not treat a missing file under here as "not in the repository" until that command says it is not.
`, dir, dir)
	target := filepath.Join(host, MarkerName)
	if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write sparse marker %s: %w", dir, err)
	}
	return nil
}

func removeMarkers(top string) error {
	return filepath.WalkDir(top, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() && (name == ".git" || name == "node_modules") {
			return fs.SkipDir
		}
		if !d.IsDir() && name == MarkerName {
			if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
				return rmErr
			}
			pruneEmptyParents(top, filepath.Dir(path))
		}
		return nil
	})
}

func pruneEmptyParents(top, dir string) {
	for dir != top && strings.HasPrefix(dir, top+string(filepath.Separator)) {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

func installExclude(ctx context.Context, repo string) error {
	gitDir, err := gitStdout(ctx, repo, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return err
	}
	gitDir = strings.TrimSpace(gitDir)
	info := filepath.Join(gitDir, "info")
	if err := os.MkdirAll(info, 0o755); err != nil {
		return fmt.Errorf("create worktree exclude dir: %w", err)
	}
	excludePath := filepath.Join(info, excludeFileName)
	if err := os.WriteFile(excludePath, []byte(MarkerName+"\n"), 0o644); err != nil {
		return fmt.Errorf("write worktree exclude file: %w", err)
	}
	// --worktree keeps this off the user's checkout. Enable has already turned
	// on extensions.worktreeConfig; without that, this config command fails
	// rather than writing the shared config.
	return git(ctx, repo, "config", "--worktree", "core.excludesFile", excludePath)
}

func isSparse(ctx context.Context, repo string) bool {
	out, err := gitStdout(ctx, repo, "config", "--worktree", "--get", "core.sparseCheckout")
	return err == nil && strings.TrimSpace(out) == "true"
}

func requestPath(ctx context.Context, top, cwd, request string) (string, error) {
	request = strings.TrimSpace(request)
	if request == "" {
		return "", errors.New("sparse-add needs a path")
	}
	// A clean relative path is repo-relative. When it is also a real path
	// from the caller's cwd (they are sitting in a subdirectory), prefer the
	// one that exists. When neither exists, keep the repo-relative form so
	// the error names the path they typed rather than a walk out of the
	// checkout through a symlinked temp directory.
	if cleaned, err := cleanPath(request); err == nil && cleaned != "." {
		if _, missing := objectKind(ctx, top, cleaned); !missing {
			return cleaned, nil
		}
		if alt, ok := existingCwdPath(ctx, top, cwd, request); ok {
			return alt, nil
		}
		return cleaned, nil
	}
	if alt, ok := existingCwdPath(ctx, top, cwd, request); ok {
		return alt, nil
	}
	return "", fmt.Errorf("checkout path %q must be relative to the repository root", request)
}

func existingCwdPath(ctx context.Context, top, cwd, request string) (string, bool) {
	base := cwd
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		base = resolved
	}
	abs := request
	if !filepath.IsAbs(request) {
		abs = filepath.Join(base, request)
	} else if resolved, err := filepath.EvalSymlinks(request); err == nil {
		abs = resolved
	}
	rel, err := filepath.Rel(top, abs)
	if err != nil {
		return "", false
	}
	cleaned, err := cleanPath(filepath.ToSlash(rel))
	if err != nil || cleaned == "." {
		return "", false
	}
	if _, missing := objectKind(ctx, top, cleaned); missing {
		return "", false
	}
	return cleaned, true
}

// DiffNames parses `git diff --name-only` output, including the C-quoted
// paths git uses once a name contains a space or a non-ASCII byte.
func DiffNames(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "\"") {
			if unquoted, err := strconv.Unquote(line); err == nil {
				line = unquoted
			}
		}
		names = append(names, line)
	}
	return names
}

func pathDir(slash string) string {
	i := strings.LastIndex(slash, "/")
	if i < 0 {
		return "."
	}
	return slash[:i]
}

func git(ctx context.Context, repo string, args ...string) error {
	_, err := gitCombined(ctx, repo, args...)
	return err
}

func gitStdin(ctx context.Context, repo string, stdin *strings.Reader, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	cmd.Stdin = stdin
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return nil
}

func gitStdout(ctx context.Context, repo string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		stderr := ""
		if errors.As(err, &exit) {
			stderr = strings.TrimSpace(string(exit.Stderr))
		}
		return "", fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), stderr, err)
	}
	return string(out), nil
}

func gitCombined(ctx context.Context, repo string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return string(out), nil
}
