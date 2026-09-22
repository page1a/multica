// Package repoident answers one question, the same way everywhere: do two
// pointers to source code name the SAME repository?
//
// It exists because a project can carry both a github_repo resource and a
// local_directory resource for one repository, and nothing in the data model
// says they are the same thing. The server sees a URL and an absolute path;
// the daemon additionally sees the directory's git remotes. Both have to reach
// the same verdict, or a project would show "duplicate" in the UI while tasks
// still cloned a second copy (DENE-595).
package repoident

import (
	"path"
	"path/filepath"
	"strings"
)

// Key is a normalized repository identity: "<host>/<owner>/<name>", lowercased,
// with any .git suffix, credentials, port, and transport removed. It is the
// value two git URLs must share to be the same repository.
//
// Empty means "could not be identified" — never treat an empty Key as equal to
// another empty Key, or every unparseable string would collapse into one repo.
type Key string

// Name is the repository's short name ("multica"), lowercased. It is a WEAKER
// signal than Key: two unrelated hosts can serve a repo of the same name. It is
// what a local directory can offer without reading its git config, so it backs
// the "these look like the same repository" hint rather than any silent action.
type Name string

// NormalizeURL maps a git remote URL to its Key.
//
// Handles the three forms a user can type into the resource picker:
//
//	https://github.com/owner/repo.git
//	ssh://git@github.com:22/owner/repo
//	git@github.com:owner/repo.git      (scp-like, no scheme)
//
// A URL that carries no host (a bare path, a relative reference) yields "".
func NormalizeURL(raw string) Key {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if isFilesystemPath(s) {
		// A filesystem path is a location on one machine, not a repository
		// identity. Letting "/Users/kunkun/.agents/multica" parse as
		// host "users" would make two unrelated local paths compare equal
		// the moment they shared a leading directory.
		return ""
	}
	// Strip transport. Everything git supports here maps to the same identity:
	// the host and the path are what name the repository, not how you reach it.
	hadScheme := false
	for _, scheme := range []string{"https://", "http://", "ssh://", "git://", "git+ssh://"} {
		if len(s) >= len(scheme) && strings.EqualFold(s[:len(scheme)], scheme) {
			s = s[len(scheme):]
			hadScheme = true
			break
		}
	}
	if !hadScheme {
		// scp-like syntax has no scheme: user@host:path. Convert the single
		// colon that separates host from path into a slash so one parser
		// handles both forms.
		if i := strings.Index(s, ":"); i >= 0 && !strings.Contains(s[:i], "/") {
			s = s[:i] + "/" + strings.TrimPrefix(s[i+1:], "/")
		}
	}
	// Credentials are not identity: the same repo cloned by two users is one repo.
	if i := strings.LastIndex(s, "@"); i >= 0 {
		if j := strings.Index(s, "/"); j < 0 || i < j {
			s = s[i+1:]
		}
	}
	s = strings.TrimLeft(s, "/")
	s = strings.TrimRight(s, "/")
	if s == "" {
		return ""
	}
	host, rest, found := strings.Cut(s, "/")
	if !found || strings.TrimSpace(rest) == "" {
		// A host with no path names no repository.
		return ""
	}
	// A port is reachability, not identity.
	if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return ""
	}
	rest = strings.TrimSuffix(path.Clean(rest), "/")
	rest = strings.TrimSuffix(rest, ".git")
	rest = strings.Trim(rest, "/")
	if rest == "" || rest == "." {
		return ""
	}
	// Host and path are lowercased on purpose: DNS is case-insensitive
	// (RFC 4343) and GitHub/GitLab treat owner/repo as case-insensitive.
	// Comparing them as-typed would let github.com/Foo/Bar and
	// GitHub.com/foo/bar both bind.
	return Key(host + "/" + strings.ToLower(rest))
}

// isFilesystemPath reports whether the input is a local path rather than a
// remote URL. Checked before any URL parsing: `file://` is deliberately NOT in
// the accepted transport list for the same reason.
func isFilesystemPath(s string) bool {
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, ".") || strings.HasPrefix(s, "~") || strings.HasPrefix(s, `\\`) {
		return true
	}
	if strings.HasPrefix(strings.ToLower(s), "file://") {
		return true
	}
	// Windows drive letter, e.g. C:\src\repo or C:/src/repo.
	if len(s) >= 3 && s[1] == ':' && (s[2] == '\\' || s[2] == '/') {
		c := s[0] | 0x20
		if c >= 'a' && c <= 'z' {
			return true
		}
	}
	return false
}

// NameFromURL returns the repository's short name from a remote URL.
func NameFromURL(raw string) Name {
	key := NormalizeURL(raw)
	if key == "" {
		// Fall back to the last path segment so a URL shape this package does
		// not model still contributes the weak signal it can.
		s := strings.TrimRight(strings.TrimSpace(raw), "/")
		s = strings.TrimSuffix(s, ".git")
		if i := strings.LastIndexAny(s, "/:"); i >= 0 {
			s = s[i+1:]
		}
		return Name(strings.ToLower(strings.TrimSpace(s)))
	}
	s := string(key)
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	return Name(s)
}

// NameFromLocalPath returns the repository name a local directory most likely
// holds: its basename, lowercased. This is a guess and is documented as one —
// the authoritative answer needs the directory's git remote, which only the
// machine holding the directory can read (see MatchesLocalDirectory).
func NameFromLocalPath(p string) Name {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	base := filepath.Base(filepath.Clean(strings.ReplaceAll(p, "\\", "/")))
	if base == "." || base == string(filepath.Separator) || base == "/" {
		return ""
	}
	return Name(strings.ToLower(base))
}

// SameRepo reports whether two remote URLs name the same repository. Two URLs
// neither of which can be identified are NOT the same repo.
func SameRepo(a, b string) bool {
	ka, kb := NormalizeURL(a), NormalizeURL(b)
	return ka != "" && ka == kb
}

// LooksLikeSameRepo is the weak, filesystem-free comparison between a remote
// URL and a local directory path: their short names match. It is deliberately
// only good enough to RAISE A QUESTION in the UI — "these two sources look like
// the same repository, merge them?" — and never good enough to silently drop a
// resource or redirect a checkout. Both of those require the strong check.
func LooksLikeSameRepo(repoURL, localPath string) bool {
	n := NameFromURL(repoURL)
	return n != "" && n == NameFromLocalPath(localPath)
}

// MatchesRemotes is the STRONG check, usable only where the directory's git
// remotes have actually been read: the requested URL resolves to the same Key
// as one of the remotes configured in the directory.
func MatchesRemotes(repoURL string, remotes []string) bool {
	want := NormalizeURL(repoURL)
	if want == "" {
		return false
	}
	for _, r := range remotes {
		if NormalizeURL(r) == want {
			return true
		}
	}
	return false
}
