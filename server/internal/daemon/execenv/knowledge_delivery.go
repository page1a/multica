package execenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/projectmemory"
)

// Knowledge sediment rides the delivery (DENE-1661).
//
// A close that claims it wrote AGENTS.md / CONTEXT.md / docs must show those
// files in what it delivers, and a chat that settled something merges it into
// the main line before it reports. The server cannot see the repository, so
// the CLI reads the facts from git here and the server holds the claim against
// them — the same split as the delivery-line merge.

// DeliveredFiles lists the paths HEAD adds or changes relative to the nearest
// main line: the files this delivery ships. bases are tried first, in order
// (a sub-issue's delivery branch); the main lines of the checkout come after.
// The first base HEAD is ahead of wins. nil, "" means no base was found or
// HEAD is already on every one of them, so nothing can be said.
func DeliveredFiles(dir string, bases ...string) ([]string, string, error) {
	r, err := deliveryRange(dir, bases...)
	if err != nil || r.ref == "" {
		return nil, "", err
	}
	files, err := changedFiles(r.root, r.base, r.head)
	return files, r.ref, err
}

type delivery struct{ root, ref, base, head string }

// deliveryRange finds the base DeliveredFiles diffs against; ref "" means
// none was found.
func deliveryRange(dir string, bases ...string) (delivery, error) {
	gitRoot, err := runGitTrimmed(dir, "rev-parse", "--show-toplevel")
	if err != nil || gitRoot == "" {
		return delivery{}, fmt.Errorf("%s is not inside a git checkout", dir)
	}
	head, err := runGitTrimmed(gitRoot, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return delivery{}, fmt.Errorf("resolve HEAD: %w", err)
	}
	pick := func(candidates []string) (string, string) {
		best, bestBase, bestCount := "", "", -1
		for _, ref := range candidates {
			base, err := runGitTrimmed(gitRoot, "merge-base", ref, head)
			if err != nil || base == "" {
				continue
			}
			raw, err := runGitTrimmed(gitRoot, "rev-list", "--count", base+".."+head)
			count, convErr := strconv.Atoi(raw)
			if err != nil || convErr != nil || count == 0 {
				continue
			}
			if bestCount < 0 || count < bestCount {
				best, bestBase, bestCount = ref, base, count
			}
		}
		return best, bestBase
	}
	var ref, base string
	for _, b := range bases {
		if b = strings.TrimSpace(b); b != "" {
			if ref, base = pick([]string{b}); ref != "" {
				break
			}
		}
	}
	if ref == "" {
		ref, base = pick(MainlineCandidates(gitRoot))
	}
	return delivery{root: gitRoot, ref: ref, base: base, head: head}, nil
}

// DeliveredMemoryFiles is the memory-hygiene account (DENE-1680) of the
// delivery DeliveredFiles sees: for each project-memory file it changes, the
// size at HEAD, the lines removed, the added supersede marks, and the
// sections. nil when no base is found.
func DeliveredMemoryFiles(dir string, bases ...string) ([]closeprotocol.MemoryFile, error) {
	r, err := deliveryRange(dir, bases...)
	if err != nil || r.ref == "" {
		return nil, err
	}
	files, err := changedFiles(r.root, r.base, r.head)
	if err != nil {
		return nil, err
	}
	return memoryFiles(r.root, [][2]string{{r.base, r.head}}, files, func(path string) (string, error) {
		return runGitStdout(r.root, "show", r.head+":"+path)
	})
}

// CommitMemoryFiles is DeliveredMemoryFiles for a chat's commits: each
// commit is diffed against its first parent, and sizes are read from the
// checkout, which holds what the chat merged.
func CommitMemoryFiles(dir string, commits, files []string) ([]closeprotocol.MemoryFile, error) {
	gitRoot, err := runGitTrimmed(dir, "rev-parse", "--show-toplevel")
	if err != nil || gitRoot == "" {
		return nil, fmt.Errorf("%s is not inside a git checkout", dir)
	}
	diffs := make([][2]string, 0, len(commits))
	for _, sha := range commits {
		if _, err := runGitTrimmed(gitRoot, "rev-parse", "--verify", sha+"^{commit}"); err != nil {
			// A PR merged on the forge is not here yet: fetch it once.
			_, _ = runGitTrimmed(gitRoot, "fetch", "--quiet", "--no-tags", "origin", sha)
			if _, err := runGitTrimmed(gitRoot, "rev-parse", "--verify", sha+"^{commit}"); err != nil {
				return nil, fmt.Errorf("commit %s is not in this checkout: %w", sha, err)
			}
		}
		parent := sha + "^"
		if _, err := runGitTrimmed(gitRoot, "rev-parse", "--verify", parent); err != nil {
			parent = emptyTree
		}
		diffs = append(diffs, [2]string{parent, sha})
	}
	return memoryFiles(gitRoot, diffs, files, func(path string) (string, error) {
		raw, err := os.ReadFile(filepath.Join(gitRoot, filepath.FromSlash(path)))
		return string(raw), err
	})
}

// emptyTree is git's well-known empty tree, the parent of a root commit.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

func memoryFiles(root string, diffs [][2]string, files []string, read func(string) (string, error)) ([]closeprotocol.MemoryFile, error) {
	out := []closeprotocol.MemoryFile{}
	for _, path := range files {
		if !isMemoryFile(path) {
			continue
		}
		content, err := read(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		file := closeprotocol.MemoryFile{Path: path, Bytes: len(content), Sections: closeprotocol.MemorySections(content)}
		for _, d := range diffs {
			patch, err := runGitStdout(root, "diff", "--no-renames", "-U0", d[0], d[1], "--", path)
			if err != nil {
				return nil, fmt.Errorf("git diff %s: %w", path, err)
			}
			for _, line := range strings.Split(patch, "\n") {
				switch {
				case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
				case strings.HasPrefix(line, "-"):
					file.Deleted++
				case strings.HasPrefix(line, "+") && closeprotocol.IsSupersedeMark(line):
					file.SupersedeMarks++
				}
			}
		}
		out = append(out, file)
	}
	return out, nil
}

func isMemoryFile(path string) bool {
	for _, key := range projectmemory.LocationKeys() {
		if len(projectmemory.MatchFiles(key, []string{path})) > 0 {
			return true
		}
	}
	return false
}

// changedFiles lists the paths to adds or changes against from. Deletions
// are left out: removing a memory file does not write it.
func changedFiles(dir, from, to string) ([]string, error) {
	out, err := runGitStdout(dir, "diff", "--name-only", "--no-renames", "--diff-filter=ACMRT", "-z", from, to)
	if err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}
	files := []string{}
	for _, path := range strings.Split(out, "\x00") {
		if path != "" {
			files = append(files, path)
		}
	}
	return files, nil
}

// MainlineCandidates are the refs a task branch may have forked from: the
// branch the user's own checkout is on and its upstream, the remote's default
// branch, and a local main/master. Task branches (agent/…) never count.
func MainlineCandidates(gitRoot string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(ref string) {
		ref = strings.TrimSpace(ref)
		if ref == "" || ref == "HEAD" || seen[ref] || strings.HasPrefix(ref, "agent/") || strings.Contains(ref, "/agent/") {
			return
		}
		if _, err := runGitTrimmed(gitRoot, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err != nil {
			return
		}
		seen[ref] = true
		out = append(out, ref)
	}
	if user := worktreeBranch(mainWorktree(gitRoot)); user != "" {
		add(user)
		upstream, _ := runGitTrimmed(gitRoot, "rev-parse", "--abbrev-ref", user+"@{upstream}")
		add(upstream)
	}
	if def, err := runGitTrimmed(gitRoot, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		add(def)
	}
	add("main")
	add("master")
	return out
}

// MainlineConfigKey names a repository's main line when nothing else can:
// `git config multica.mainline <branch>`.
const MainlineConfigKey = "multica.mainline"

// Mainline is the branch the project counts as its main line — where a
// chat's settled work has to land (DENE-1668). It never follows the branch
// the project directory happens to be on, which is often a feature branch.
// In order: the repository's multica.mainline setting; with a remote, the
// remote's default branch (asked live, then the local origin/HEAD); without
// one, the only one of main and master. "" when none decides it, and callers
// refuse rather than guess.
func Mainline(dir string) string {
	gitRoot, err := runGitTrimmed(dir, "rev-parse", "--show-toplevel")
	if err != nil || gitRoot == "" {
		return ""
	}
	if name, err := runGitTrimmed(gitRoot, "config", "--get", MainlineConfigKey); err == nil && name != "" {
		return name
	}
	if RemoteURL(gitRoot) != "" {
		if out, err := runGitTrimmedEnv(gitRoot, []string{"GIT_TERMINAL_PROMPT=0"}, "ls-remote", "--symref", "origin", "HEAD"); err == nil {
			for _, line := range strings.Split(out, "\n") {
				if rest, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
					if name, _, ok := strings.Cut(rest, "\t"); ok && name != "" {
						return name
					}
				}
			}
		}
		if def, err := runGitTrimmed(gitRoot, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
			if _, name, ok := strings.Cut(def, "/"); ok {
				return name
			}
		}
		return ""
	}
	found := ""
	for _, name := range []string{"main", "master"} {
		if _, err := runGitTrimmed(gitRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+name); err == nil {
			if found != "" {
				return ""
			}
			found = name
		}
	}
	return found
}

// noMainlineHint is how to fix a repository whose main line is unknown.
const noMainlineHint = "no main line can be determined (no " + MainlineConfigKey + ", no remote default branch, not exactly one of main/master); set it with `git config " + MainlineConfigKey + " <branch>`"

// RemoteURL is origin's URL, "" for a repository that only lives here.
func RemoteURL(dir string) string {
	out, err := runGitTrimmed(dir, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return out
}

// MainlineMergeResult is what MergeIntoMainline did.
type MainlineMergeResult struct {
	Mainline string `json:"mainline"`
	// Source is the branch that was merged; equal to Mainline when the
	// work was committed on the main line directly (a shared directory).
	Source string `json:"source"`
	// Tip is the main line after the merge.
	Tip string `json:"tip"`
	// Commits are the work's commits, oldest first; the merge commit, when
	// one was made, is last.
	Commits []DeliveryMergeCommit `json:"commits"`
	// Files are the paths the work adds or changes.
	Files []string `json:"files"`
}

// MergeIntoMainline lands the current branch's commits on the main line of
// a repository that has no remote to open a PR on (DENE-1661). The main line
// is Mainline's, not the project directory's current branch. The merge is
// always a merge commit whose message is message, so the main line says
// where the work came from. A checkout holding the main line is merged in
// place and must be clean; otherwise the branch moves with a compare-and-swap.
// since bounds the commits reported for work already on the main line.
func MergeIntoMainline(dir, message, since string) (MainlineMergeResult, error) {
	var res MainlineMergeResult
	gitRoot, err := runGitTrimmed(dir, "rev-parse", "--show-toplevel")
	if err != nil || gitRoot == "" {
		return res, fmt.Errorf("%w: %s is not inside a git checkout", ErrDeliveryMergeRefused, dir)
	}
	res.Mainline = Mainline(gitRoot)
	if res.Mainline == "" {
		return res, fmt.Errorf("%w: %s", ErrDeliveryMergeRefused, noMainlineHint)
	}
	res.Source = worktreeBranch(gitRoot)
	if res.Source == "" {
		return res, fmt.Errorf("%w: the checkout is not on a branch; switch back to the chat's branch first", ErrDeliveryMergeRefused)
	}
	if dirty, err := uncommittedWork(gitRoot); err != nil {
		return res, err
	} else if len(dirty) > 0 {
		return res, fmt.Errorf("%w: uncommitted changes (%s); commit them on %s first — only commits are merged", ErrDeliveryMergeRefused, strings.Join(clip(dirty, 5), ", "), res.Source)
	}
	head, err := runGitTrimmed(gitRoot, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return res, fmt.Errorf("resolve HEAD: %w", err)
	}
	ref := "refs/heads/" + res.Mainline
	tip, _ := runGitTrimmed(gitRoot, "rev-parse", "--verify", "--quiet", ref)
	if tip == "" {
		return res, fmt.Errorf("%w: the main line %s does not exist", ErrDeliveryMergeRefused, res.Mainline)
	}

	if isAncestor(gitRoot, head, tip) {
		// Already on the main line: a shared directory works on it directly.
		res.Tip = tip
		args := []string{"--since=" + since}
		if since == "" {
			args = []string{"-n", "20"}
		}
		if upstream, err := runGitTrimmed(gitRoot, "rev-parse", "--verify", "--quiet", res.Mainline+"@{upstream}"); err == nil && upstream != "" {
			args = append(args, "^"+upstream)
		}
		if res.Commits, err = listCommits(gitRoot, head, args...); err != nil {
			return res, err
		}
		res.Files, err = commitFiles(gitRoot, res.Commits)
		return res, err
	}

	base, err := runGitTrimmed(gitRoot, "merge-base", tip, head)
	if err != nil {
		return res, fmt.Errorf("git merge-base: %w", err)
	}
	if res.Files, err = changedFiles(gitRoot, base, head); err != nil {
		return res, err
	}
	if res.Commits, err = listCommits(gitRoot, head, "^"+tip); err != nil {
		return res, err
	}

	if holder := worktreeHolding(gitRoot, res.Mainline); holder != "" {
		if dirty, err := uncommittedWork(holder); err != nil {
			return res, err
		} else if len(dirty) > 0 {
			return res, fmt.Errorf("%w: the project directory %s has uncommitted changes (%s); the merge would mix into them. Commit or stash them there and run again", ErrDeliveryMergeRefused, holder, strings.Join(clip(dirty, 5), ", "))
		}
		if out, err := runGit(holder, "merge", "--no-ff", "--no-edit", "-m", message, head); err != nil {
			_, _ = runGit(holder, "merge", "--abort")
			return res, fmt.Errorf("%w: merging into %s in %s failed, nothing changed: %s", ErrDeliveryMergeRefused, res.Mainline, holder, strings.TrimSpace(out))
		}
		res.Tip, _ = runGitTrimmed(holder, "rev-parse", "HEAD")
	} else {
		tree, conflicts, err := mergeTree(gitRoot, tip, head)
		if err != nil {
			return res, err
		}
		if len(conflicts) > 0 {
			return res, fmt.Errorf("%w: merging into %s conflicts on %s; merge %s into this branch, resolve, commit, and run again", ErrDeliveryMergeRefused, res.Mainline, strings.Join(clip(conflicts, 5), ", "), res.Mainline)
		}
		args := append(commitIdentityArgs(gitRoot), "commit-tree", tree, "-p", tip, "-p", head, "-m", message)
		next, err := runGitTrimmed(gitRoot, args...)
		if err != nil {
			return res, fmt.Errorf("git commit-tree: %w", err)
		}
		if out, err := runGit(gitRoot, "update-ref", ref, next, tip); err != nil {
			return res, fmt.Errorf("move %s: %s: %w (it moved meanwhile; run again)", res.Mainline, strings.TrimSpace(out), err)
		}
		res.Tip = next
	}
	res.Commits = append(res.Commits, DeliveryMergeCommit{SHA: res.Tip, Subject: message})
	return res, nil
}

// RemoteLanding is what CheckRemoteLanding found.
type RemoteLanding struct {
	Mainline string `json:"mainline"`
	// Landed: origin's main line, fetched just now, contains HEAD.
	Landed bool `json:"landed"`
	// RemoteTip is origin's main line as fetched.
	RemoteTip string                `json:"remote_tip"`
	Commits   []DeliveryMergeCommit `json:"commits"`
	Files     []string              `json:"files"`
}

// CheckRemoteLanding asks origin whether its main line already holds this
// checkout's HEAD — work pushed there directly, or merged in some other way —
// for a repository with a remote and no PR to point at (DENE-1668). The
// commits reported are HEAD's since since (the chat's start; the last 20
// without one). A fetch that fails is an error: unknown is not landed.
func CheckRemoteLanding(dir, mainline, since string) (RemoteLanding, error) {
	res := RemoteLanding{Mainline: mainline}
	gitRoot, err := runGitTrimmed(dir, "rev-parse", "--show-toplevel")
	if err != nil || gitRoot == "" {
		return res, fmt.Errorf("%s is not inside a git checkout", dir)
	}
	if mainline == "" {
		return res, fmt.Errorf("%s", noMainlineHint)
	}
	head, err := runGitTrimmed(gitRoot, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return res, fmt.Errorf("resolve HEAD: %w", err)
	}
	tracking := "refs/remotes/origin/" + mainline
	if _, err := runGitTrimmedEnv(gitRoot, []string{"GIT_TERMINAL_PROMPT=0"}, "fetch", "--quiet", "--no-tags", "origin", "+refs/heads/"+mainline+":"+tracking); err != nil {
		return res, fmt.Errorf("fetch origin %s to check what it holds: %w", mainline, err)
	}
	if res.RemoteTip, err = runGitTrimmed(gitRoot, "rev-parse", "--verify", tracking); err != nil {
		return res, fmt.Errorf("resolve %s: %w", tracking, err)
	}
	res.Landed = isAncestor(gitRoot, head, res.RemoteTip)
	args := []string{"--since=" + since}
	if since == "" {
		args = []string{"-n", "20"}
	}
	if res.Commits, err = listCommits(gitRoot, head, args...); err != nil {
		return res, err
	}
	res.Files, err = commitFiles(gitRoot, res.Commits)
	return res, err
}

// commitFiles is the union of the paths the commits add or change.
func commitFiles(dir string, commits []DeliveryMergeCommit) ([]string, error) {
	seen := map[string]bool{}
	files := []string{}
	for _, c := range commits {
		out, err := runGitStdout(dir, "show", "--name-only", "--no-renames", "--diff-filter=ACMRT", "--format=", "-z", c.SHA)
		if err != nil {
			return nil, fmt.Errorf("git show: %w", err)
		}
		for _, path := range strings.Split(out, "\x00") {
			if path = strings.TrimSpace(path); path != "" && !seen[path] {
				seen[path] = true
				files = append(files, path)
			}
		}
	}
	return files, nil
}
