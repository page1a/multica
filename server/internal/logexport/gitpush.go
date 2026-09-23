package logexport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// The log-export pusher commits one generated bundle into a workspace's git
// repository and hands back a link to it. It shells out to `git` rather than
// carrying a git implementation in-process: the daemon already requires the
// binary, the operator already has git's credential and proxy configuration
// story, and the alternative is another dependency whose behaviour diverges
// from the git everyone else is running.
//
// The whole point of this type is that a bundle can be large. It is generated
// once, handed to a browser that does not want to hold it in memory, and
// linked from an issue comment. That is why the pusher never returns content
// and why its failures are plain, token-free sentences a user can read.

// DefaultPushTimeout bounds one whole Push. The callers are HTTP handlers with
// roughly a minute of patience, and a push that has not completed by then has
// already lost the race with the user's attention; the client falls back to
// uploading the artifact as a normal comment attachment.
const DefaultPushTimeout = 60 * time.Second

// DefaultGitInvocationTimeout bounds one git subprocess. It is shorter than the
// whole-push budget so a single hung remote (a clone waiting on a dead TCP
// connection) is cut off with time left to report why, instead of consuming the
// entire request and surfacing as a context deadline with no stage named.
const DefaultGitInvocationTimeout = 45 * time.Second

// The commit author is a fixed bot identity rather than the operator who
// clicked export. The commit is a machine artifact, and attributing it to a
// person would put a human's name on a payload they may never have read.
const (
	botAuthorName  = "multica-log-export"
	botAuthorEmail = "log-export@multica.local"
)

// PushRequest is one artifact to commit.
type PushRequest struct {
	// Filename is the artifact's file name, e.g.
	// log-export-DENE-599-abc123-run-20260921T170000Z.json. It must be a bare
	// name; the repository directory is the GitRepo's business.
	Filename string
	// Content is the serialized bundle, committed verbatim.
	Content []byte
	// Message is the commit message; the caller supplies it so the commit
	// reads like what the workspace asked for.
	Message string
}

// PushResult is what the caller needs to link the artifact.
type PushResult struct {
	// Path is the repository-relative path that was written, e.g.
	// logs/<filename>.
	Path string
	// URL is the human-clickable link to the file.
	URL string
	// Branch is the branch actually pushed to. It is never empty: when the
	// request named no branch, this is the branch the clone checked out.
	Branch string
}

// Pusher is the seam between the handler and git, so the handler's tests can
// assert on what would have been pushed without a remote.
type Pusher interface {
	Push(ctx context.Context, repo GitRepo, token string, req PushRequest) (PushResult, error)
}

// GitCLIPusher is the production Pusher.
type GitCLIPusher struct {
	// Timeout bounds one whole Push. Zero means DefaultPushTimeout.
	Timeout time.Duration
	// InvocationTimeout bounds one git subprocess. Zero means
	// DefaultGitInvocationTimeout.
	InvocationTimeout time.Duration
}

func (p *GitCLIPusher) pushTimeout() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return DefaultPushTimeout
}

func (p *GitCLIPusher) invocationTimeout() time.Duration {
	if p.InvocationTimeout > 0 {
		return p.InvocationTimeout
	}
	return DefaultGitInvocationTimeout
}

// userinfoPattern matches the `scheme://user:pass@` prefix of a URL so any
// credential git echoes back can be removed even when it is not the exact
// string this process injected.
var userinfoPattern = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)[^/@\s]+@`)

// gitRedactor removes credential material from anything that might be shown to
// a human. It is built once per push from the exact strings this process knows
// about, and its job is to be correct on the failure path — the success path
// has nothing to hide.
type gitRedactor struct {
	replacements [][2]string
	token        string
}

func newGitRedactor(token, plainURL, authURL string) gitRedactor {
	r := gitRedactor{token: token}
	if authURL != "" && authURL != plainURL {
		r.replacements = append(r.replacements, [2]string{authURL, plainURL})
	}
	if token != "" {
		r.replacements = append(r.replacements, [2]string{token, "***"})
	}
	return r
}

func (r gitRedactor) redact(s string) string {
	for _, rep := range r.replacements {
		if rep[0] == "" {
			continue
		}
		s = strings.ReplaceAll(s, rep[0], rep[1])
	}
	if r.token != "" {
		for _, escaped := range []string{url.QueryEscape(r.token), url.PathEscape(r.token)} {
			if escaped != r.token && escaped != "" {
				s = strings.ReplaceAll(s, escaped, "***")
			}
		}
	}
	return userinfoPattern.ReplaceAllString(s, "$1***@")
}

// authenticatedURL injects a token into an HTTP(S) remote as basic-auth
// userinfo. It is used for the git invocation only; the token never reaches
// PushResult or an error string.
//
// A non-HTTP URL (an ssh remote, a local path) is returned unchanged: the token
// this feature stores is a forge access token, and quietly pasting it into a
// URL scheme that does not understand it would leak it to an unrelated host.
func authenticatedURL(rawURL, token string) string {
	trimmed := strings.TrimSpace(rawURL)
	if strings.TrimSpace(token) == "" {
		return trimmed
	}
	u, err := url.Parse(trimmed)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return trimmed
	}
	u.User = url.UserPassword("x-access-token", token)
	return u.String()
}

// runGit runs one git subprocess with its own timeout derived from ctx, so a
// hung remote cannot pin the request until the outer deadline. It returns the
// combined output for the caller to sanitize.
func (p *GitCLIPusher) runGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, p.invocationTimeout())
	defer cancel()

	cmd := exec.CommandContext(cctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	// GIT_TERMINAL_PROMPT=0 makes an auth failure an error instead of a
	// subprocess blocked forever on a terminal this server does not have.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=")

	out, err := cmd.CombinedOutput()
	if err != nil && cctx.Err() != nil {
		err = fmt.Errorf("%w (%w)", err, cctx.Err())
	}
	return out, err
}

func (p *GitCLIPusher) fail(stage string, out []byte, err error, redactor gitRedactor) error {
	detail := strings.TrimSpace(redactor.redact(string(out)))
	if detail == "" {
		return fmt.Errorf("%s failed: %w", stage, err)
	}
	return fmt.Errorf("%s failed: %s: %w", stage, detail, err)
}

// Push clones the repository shallowly, writes the bundle, and commits it. It
// is idempotent for identical bytes: the file is compared against the checked
// out copy first, so a retry reports the existing blob instead of adding a
// second empty-history commit.
func (p *GitCLIPusher) Push(ctx context.Context, repo GitRepo, token string, req PushRequest) (PushResult, error) {
	filename := strings.TrimSpace(req.Filename)
	if filename == "" || filename == "." || filename == ".." || strings.ContainsAny(filename, `/\`) {
		return PushResult{}, fmt.Errorf("invalid log export filename %q", req.Filename)
	}
	if repo.URL == "" || !repo.Enabled {
		return PushResult{}, errors.New("log export git repository is not configured")
	}

	ctx, cancel := context.WithTimeout(ctx, p.pushTimeout())
	defer cancel()

	// Mirror Settings.Repo()'s normalization so a caller that built a GitRepo
	// by hand still lands in logs/ rather than at the repository root — and so
	// a hand-built Dir cannot reach the filesystem without the same escaping
	// check the settings path applies. The write below happens inside a
	// temporary clone, and `..` would put the bundle outside it, where the
	// trailing RemoveAll does not reach.
	dir, ok := normalizeRepoDir(repo.Dir)
	if !ok {
		return PushResult{}, fmt.Errorf("invalid log export repository directory %q", repo.Dir)
	}
	repo.Dir = dir
	repo.Branch = strings.TrimSpace(repo.Branch)

	plainURL := strings.TrimSpace(repo.URL)
	authURL := authenticatedURL(plainURL, token)
	redactor := newGitRedactor(token, plainURL, authURL)

	workdir, err := os.MkdirTemp("", "multica-log-export-*")
	if err != nil {
		return PushResult{}, fmt.Errorf("create workdir failed: %w", err)
	}
	defer os.RemoveAll(workdir)

	// -c credential.helper= disables every configured helper for this
	// invocation. The token is already in the URL; a helper that also ran
	// could answer a challenge with some other credential, or write the
	// token into a credential store on the server.
	cloneArgs := []string{"-c", "credential.helper=", "clone", "--depth", "1"}
	if repo.Branch != "" {
		cloneArgs = append(cloneArgs, "--branch", repo.Branch)
	}
	cloneArgs = append(cloneArgs, authURL, workdir)

	if out, err := p.runGit(ctx, "", cloneArgs...); err != nil {
		return PushResult{}, p.fail("git clone", out, err, redactor)
	}

	branch := repo.Branch
	if branch == "" {
		out, err := p.runGit(ctx, workdir, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return PushResult{}, p.fail("git rev-parse HEAD", out, err, redactor)
		}
		branch = strings.TrimSpace(string(out))
		if branch == "" || branch == "HEAD" {
			return PushResult{}, fmt.Errorf("could not determine the remote's default branch")
		}
	}

	relPath := repo.Path(filename)
	// relPath is repo-relative and always forward-slashed; the local filesystem
	// path is the OS's business, so FromSlash is what keeps this correct on a
	// Windows server without changing what gets committed.
	localPath := filepath.Join(workdir, filepath.FromSlash(relPath))
	// Belt and braces: normalizeRepoDir already refused every `..`, but the
	// destination must be proven inside the clone before anything is written,
	// read, or staged. A path that fails this is a configuration the pusher
	// must refuse rather than a file to publish.
	if !insideDir(workdir, localPath) {
		return PushResult{}, fmt.Errorf("invalid log export repository directory %q", repo.Dir)
	}
	// insideDir compares path strings; the filesystem follows symbolic links.
	// A clone is a working copy of a repository somebody else controls, and git
	// records a link verbatim, so a tracked `logs` that points elsewhere makes
	// the joined path leave the clone the moment it is used. WriteFile follows
	// it and runs before `git add`; the trailing RemoveAll only covers the
	// clone, so the escaped file would outlive the push. Prove the destination
	// stays inside before the first read, mkdir, write, or git invocation.
	if err := refuseLinkEscape(workdir, relPath, localPath); err != nil {
		return PushResult{}, err
	}

	// Identical bytes already tracked: there is nothing to commit, and a
	// second push would otherwise add a no-op commit to the history. Report
	// the blob that is already there.
	if existing, readErr := os.ReadFile(localPath); readErr == nil && bytes.Equal(existing, req.Content) {
		return PushResult{
			Path:   relPath,
			URL:    fileWebURL(plainURL, branch, relPath),
			Branch: branch,
		}, nil
	}

	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return PushResult{}, fmt.Errorf("create %s directory failed: %w", path.Dir(relPath), err)
	}
	if err := os.WriteFile(localPath, req.Content, 0o644); err != nil {
		return PushResult{}, fmt.Errorf("write %s failed: %w", relPath, err)
	}

	message := strings.TrimSpace(req.Message)
	if message == "" {
		message = "chore(log-export): add " + filename
	}

	addArgs := []string{"-c", "credential.helper=", "add", "--", relPath}
	if out, err := p.runGit(ctx, workdir, addArgs...); err != nil {
		return PushResult{}, p.fail("git add", out, err, redactor)
	}

	commitArgs := []string{
		"-c", "user.name=" + botAuthorName,
		"-c", "user.email=" + botAuthorEmail,
		"commit", "-m", message,
	}
	if out, err := p.runGit(ctx, workdir, commitArgs...); err != nil {
		return PushResult{}, p.fail("git commit", out, err, redactor)
	}

	pushArgs := []string{"-c", "credential.helper=", "push", "origin", "HEAD:refs/heads/" + branch}
	if out, err := p.runGit(ctx, workdir, pushArgs...); err != nil {
		return PushResult{}, p.fail("git push", out, err, redactor)
	}

	return PushResult{
		Path:   relPath,
		URL:    fileWebURL(plainURL, branch, relPath),
		Branch: branch,
	}, nil
}

// insideDir reports whether target is root itself or a path beneath it. It is
// the last line of defence around the one write the pusher performs: the
// artifact and its directory must land in the temporary clone, or nothing is
// committed at all.
func insideDir(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (!filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// refuseLinkEscape refuses a destination that only looks like it is inside the
// clone. insideDir answers a question about strings; this one answers it about
// the filesystem the strings will be handed to.
//
// Every segment of relPath that already exists is inspected with Lstat, so a
// symbolic link — an ancestor, or the artifact path itself — is refused rather
// than followed. A log repository has no legitimate reason to route its own
// artifacts through a link, and resolving one to decide would leave a window
// between the check and the write in which the link could change. Whatever
// part of the chain does exist must also resolve into the clone, which catches
// a link that appeared between the walk and this call.
func refuseLinkEscape(workdir, relPath, localPath string) error {
	escaped := func() error {
		return fmt.Errorf("refusing to write %s: the path resolves outside the cloned repository", relPath)
	}

	realWorkdir, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		return fmt.Errorf("resolve the cloned repository failed: %w", err)
	}

	current := workdir
	for _, segment := range strings.Split(filepath.FromSlash(relPath), string(filepath.Separator)) {
		if segment == "" || segment == "." {
			continue
		}
		current = filepath.Join(current, segment)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				// Nothing below this point exists, so nothing below it can be
				// a link; MkdirAll will create real directories.
				break
			}
			return fmt.Errorf("inspect %s failed: %w", relPath, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return escaped()
		}
	}

	if resolved, resErr := filepath.EvalSymlinks(filepath.Dir(localPath)); resErr == nil {
		if !insideDir(realWorkdir, resolved) {
			return escaped()
		}
	}
	return nil
}

// WebURL is the repository's browsable root: the configured URL with any
// embedded credentials removed and a trailing `.git` dropped. The handler shows
// it as the "repo" field of a push response, so it must be safe to render.
func WebURL(rawRepoURL string) string {
	trimmed := strings.TrimSpace(rawRepoURL)
	if trimmed == "" {
		return ""
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return strings.TrimSuffix(strings.TrimSuffix(trimmed, "/"), ".git")
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	p := strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), ".git")
	return u.Scheme + "://" + u.Host + p
}

// fileWebURL builds the link a human clicks to read one committed artifact.
// GitHub and GitLab both have a `/blob/` route, but GitLab inserts a `/-`
// segment; everything else gets the conservative `<base>/<branch>/<path>`
// form rather than a guessed route that may 404.
func fileWebURL(rawRepoURL, branch, filePath string) string {
	base := WebURL(rawRepoURL)
	if base == "" {
		return ""
	}
	encoded := encodeRepoPath(filePath)
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return base + "/" + branch + "/" + encoded
	}
	switch strings.ToLower(u.Hostname()) {
	case "github.com":
		return base + "/blob/" + branch + "/" + encoded
	case "gitlab.com":
		return base + "/-/blob/" + branch + "/" + encoded
	default:
		return base + "/" + branch + "/" + encoded
	}
}

// encodeRepoPath percent-encodes each path segment, keeping the separators so
// the URL still names a nested path.
func encodeRepoPath(p string) string {
	segments := strings.Split(p, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return strings.Join(segments, "/")
}
