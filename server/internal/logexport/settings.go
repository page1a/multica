package logexport

import (
	"encoding/json"
	"path"
	"strings"
)

// SettingsKey is the key under which log-export configuration lives inside the
// workspace `settings` JSONB column. The column is shared with the rest of the
// product's settings, so everything this package owns is nested under one key.
const SettingsKey = "logExport"

// DefaultDir is the directory inside the repository that receives exported
// bundles when the workspace did not name one. A fixed default keeps the
// destination predictable: an operator who enables the feature and stops gets
// `logs/`, not a repository root strewn with artifacts.
const DefaultDir = "logs"

// GitRepo is where a workspace wants its exported bundles committed. It is the
// non-secret half of the `logExport.gitRepo` block; the access token lives
// beside it in the settings JSON, sealed, and is never part of this struct
// because nothing that renders settings may see it.
type GitRepo struct {
	// Enabled is the switch. A repository URL without it is inert config: the
	// feature must not start pushing because somebody pasted a URL and closed
	// the dialog.
	Enabled bool `json:"enabled"`
	// URL is the remote to clone from and push to. Empty while Enabled is the
	// "incomplete" state and resolves to no repo.
	URL string `json:"url"`
	// Branch is the branch to push to. Empty means the remote's default
	// branch: the pusher pushes to whatever the clone checked out rather than
	// inventing a name.
	Branch string `json:"branch"`
	// Dir is the directory inside the repository. Empty means DefaultDir.
	Dir string `json:"dir"`
}

// Settings is the whole log-export configuration block.
type Settings struct {
	GitRepo *GitRepo `json:"gitRepo"`
}

// ParseSettings reads the log-export block out of a workspace `settings`
// payload. A missing block, a malformed one, or a null yields the zero
// Settings — a workspace whose settings JSON cannot be parsed must not start
// pushing artifacts anywhere.
func ParseSettings(raw []byte) Settings {
	if len(raw) == 0 {
		return Settings{}
	}
	var envelope struct {
		LogExport *Settings `json:"logExport"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.LogExport == nil {
		return Settings{}
	}
	return *envelope.LogExport
}

// normalizeRepoDir validates and normalizes a configured repository directory.
//
// The directory becomes the prefix of a path that is joined under a temporary
// clone, so a `..` segment is not a naming choice: `foo/../../tmp/escaped`
// resolves outside the clone and the bundle is written there, before `git add`
// ever runs and past the RemoveAll that only covers the clone. A backslash is
// an ordinary character on Linux but a separator on Windows, which means the
// same row would escape on one platform and not the other; it is rejected
// rather than reinterpreted. Both are refused outright instead of repaired —
// a repository directory the operator did not mean should stop the push, not
// be silently rewritten into a different one.
//
// A blank value (or one that only names the root, e.g. `/`) means DefaultDir.
// Surrounding slashes are trimmed as before, so `/logs/` stays `logs` and the
// resulting path never starts with a separator.
func normalizeRepoDir(raw string) (string, bool) {
	dir := strings.Trim(strings.TrimSpace(raw), "/")
	if dir == "" {
		return DefaultDir, true
	}
	if strings.Contains(dir, `\`) {
		return "", false
	}
	for _, segment := range strings.Split(dir, "/") {
		if segment == ".." {
			return "", false
		}
	}
	cleaned := path.Clean(dir)
	if cleaned == "." || cleaned == "/" {
		return DefaultDir, true
	}
	return cleaned, true
}

// Repo resolves the configured repository, reporting false when it is not
// usable. Both halves are required: the switch must be on, and the URL must be
// non-empty. Nothing else — a half-filled row is a workspace that has not
// finished configuring, and treating it as usable would push to a URL the
// operator never finished typing.
//
// The returned value is a normalized copy: Dir falls back to DefaultDir and
// the string fields are trimmed. Branch is deliberately allowed to stay empty,
// meaning "the remote's default branch". A Dir that would escape the clone
// makes the whole row unusable, exactly like an unfinished URL.
func (s Settings) Repo() (GitRepo, bool) {
	if s.GitRepo == nil || !s.GitRepo.Enabled {
		return GitRepo{}, false
	}
	repo := *s.GitRepo
	repo.URL = strings.TrimSpace(repo.URL)
	if repo.URL == "" {
		return GitRepo{}, false
	}
	repo.Branch = strings.TrimSpace(repo.Branch)
	dir, ok := normalizeRepoDir(repo.Dir)
	if !ok {
		return GitRepo{}, false
	}
	repo.Dir = dir
	return repo, true
}

// Path is the repo-relative destination of one artifact. It joins with forward
// slashes rather than filepath.Join so a server running on Windows still
// commits `logs/x.json` and produces the same repository history as one
// running on Linux.
func (g GitRepo) Path(filename string) string {
	return path.Join(g.Dir, filename)
}
