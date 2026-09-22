package coderesolve

import (
	"regexp"
	"strings"
)

// Path handling here is deliberately NOT path/filepath. The paths this package
// reasons about belong to the user's machine, which may be Windows while the
// server is Linux: filepath.IsAbs(`C:\Users\x`) is false on Linux, and
// filepath.Join would rewrite the separators of a path the daemon has to match
// literally. So every helper below works on either flavour and preserves the
// separator the path was written with.

// IsAbsolutePath reports whether a path looks absolute on either POSIX or
// Windows. Mirrors handler.isAbsoluteLocalPath, which guards the same strings
// on the way into the database; a row that passed that check passes this one.
func IsAbsolutePath(s string) bool {
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

// separatorOf returns the separator a path is written with. A path holding a
// backslash and no forward slash is Windows; everything else is treated as
// POSIX, which is also the right answer for a Windows path already written
// with forward slashes.
func separatorOf(p string) string {
	if strings.Contains(p, `\`) && !strings.Contains(p, "/") {
		return `\`
	}
	return "/"
}

// trimTrailingSeparators removes trailing separators without eating a root
// ("/" and `C:\` keep theirs).
func trimTrailingSeparators(p string) string {
	for len(p) > 1 && (p[len(p)-1] == '/' || p[len(p)-1] == '\\') {
		if len(p) == 3 && isDriveLetter(p[0]) && p[1] == ':' {
			return p
		}
		p = p[:len(p)-1]
	}
	return p
}

// Base returns the last element of a path, treating both separators as such.
func Base(p string) string {
	p = trimTrailingSeparators(p)
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// dir returns everything before the last element, keeping the separator style.
func dir(p string) string {
	p = trimTrailingSeparators(p)
	i := strings.LastIndexAny(p, `/\`)
	if i < 0 {
		return ""
	}
	if i == 0 {
		return string(p[0])
	}
	return p[:i]
}

// Join appends child to parent with parent's separator.
func Join(parent, child string) string {
	if parent == "" {
		return child
	}
	if child == "" {
		return parent
	}
	sep := separatorOf(parent)
	trimmed := trimTrailingSeparators(parent)
	if strings.HasSuffix(trimmed, sep) {
		return trimmed + child
	}
	return trimmed + sep + child
}

// worktreeRootSuffix names the sibling directory holding a repository's
// Multica working copies. Mirrors execenv.worktreeRootSuffix; the two must
// agree or the server would tell the user one location and the daemon would
// build the copy in another. Kept inline rather than imported for the reason
// handler.taskDirSegment gives: the server must not depend on the daemon
// package.
const worktreeRootSuffix = ".multica-worktrees"

// DefaultWorktreeRoot is where a repository's working copies go when the
// resource names no location: the repository's sibling. Mirrors
// execenv.DefaultWorktreeRoot.
func DefaultWorktreeRoot(repoPath string) string {
	clean := trimTrailingSeparators(repoPath)
	return Join(dir(clean), Base(clean)+worktreeRootSuffix)
}

// readablePathSegmentMax, taskKeyLen, nonAlphanumeric and TaskPathSegment
// mirror execenv.readablePathSegment / execenv.taskKey — the naming rule for a
// task's own directory. The server needs it to state, before the run starts,
// which directory a parallel run will land in; the daemon needs it to build
// that directory. Same lock-step contract as handler.taskDirSegment, and the
// same reason for the duplication: no handler import of the daemon package.
//
// The suffix must keep coming from the TAIL of the id. A UUIDv7's leading hex
// chars are timestamp bits shared by every task created within ~65.5s (#7326).
const (
	readablePathSegmentMax = 24
	taskKeyLen             = 12
)

var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

// TaskPathSegment converts a user-facing label plus a task id into the bounded
// lowercase segment a task's directory is named with.
func TaskPathSegment(label, fallback, id string) string {
	prefix := strings.ToLower(strings.TrimSpace(label))
	prefix = nonAlphanumeric.ReplaceAllString(prefix, "-")
	prefix = strings.Trim(prefix, "-")
	if prefix == "" {
		prefix = fallback
	}
	suffix := strings.ToLower(taskKey(id))
	maxPrefix := readablePathSegmentMax - len(suffix) - 1
	if maxPrefix < 0 {
		maxPrefix = 0
	}
	if len(prefix) > maxPrefix {
		prefix = strings.TrimRight(prefix[:maxPrefix], "-")
	}
	if prefix == "" {
		prefix = fallback
	}
	return prefix + "-" + suffix
}

func taskKey(uuid string) string {
	s := strings.ReplaceAll(uuid, "-", "")
	if len(s) > taskKeyLen {
		return s[len(s)-taskKeyLen:]
	}
	return s
}
