package daemon

import (
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/pkg/agent"
)

// skillUsageMessageType is the transcript row the daemon appends the first
// time a run uses one of its bound skills. Tool carries the skill name. The
// server aggregates these rows into a run's skills_used; an older daemon that
// never sends them simply leaves the list empty.
const skillUsageMessageType = "skill"

// skillUsageDetector recognises when a run uses a skill the daemon wrote for
// it. Two signals count:
//
//   - a Claude-style `Skill` tool call naming the skill (input.skill);
//   - any tool call whose arguments reference `<root>/<skill-dir>/SKILL.md`
//     under one of the skill roots this run was given (Codex, and every
//     runtime that reads the file through its own read or shell tool).
//
// Only names bound to this run count, and only under this run's skill roots:
// a repository file that merely shares the layout — say
// server/internal/service/builtin_skills/<name>/SKILL.md — is not a use.
// Each skill is reported once per run.
//
// Not safe for concurrent use; the drain loop feeds it from one goroutine,
// and executeAndDrain retries run one after another.
type skillUsageDetector struct {
	names    []string
	slugs    []string
	prefixes []skillRootPrefix
	reported []bool
}

type skillRootPrefix struct {
	// path is slash-separated with no trailing slash.
	path string
	// relative marks a workdir-relative form (".claude/skills"), which needs
	// a stricter left boundary than an absolute path.
	relative bool
}

// newSkillUsageDetector builds a detector for the named skills, written under
// roots. workDir lets it also recognise the workdir-relative spelling of a
// root that lives inside it. Returns nil when there is nothing to detect; a
// nil detector observes nothing.
func newSkillUsageDetector(names []string, workDir string, roots ...string) *skillUsageDetector {
	if len(names) == 0 {
		return nil
	}
	d := &skillUsageDetector{
		names:    append([]string(nil), names...),
		slugs:    execenv.SkillDirSlugs(names),
		reported: make([]bool, len(names)),
	}
	seen := map[skillRootPrefix]bool{}
	add := func(p skillRootPrefix) {
		p.path = strings.TrimRight(filepath.ToSlash(p.path), "/")
		if p.path == "" || p.path == "." || seen[p] {
			return
		}
		seen[p] = true
		d.prefixes = append(d.prefixes, p)
	}
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		root = filepath.Clean(root)
		forms := []string{root}
		// macOS temp dirs live behind /var -> /private/var; a tool may print
		// either spelling.
		if real, err := filepath.EvalSymlinks(root); err == nil && real != root {
			forms = append(forms, real)
		}
		for _, form := range forms {
			add(skillRootPrefix{path: form})
			if workDir == "" {
				continue
			}
			if rel, err := filepath.Rel(workDir, form); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
				add(skillRootPrefix{path: rel, relative: true})
			}
		}
	}
	return d
}

// Observe returns the bound skills msg uses for the first time, in the order
// it names them. Only tool calls are inspected.
func (d *skillUsageDetector) Observe(msg agent.Message) []string {
	if d == nil || msg.Type != agent.MessageToolUse {
		return nil
	}
	var used []string
	mark := func(i int) {
		if i < 0 || d.reported[i] {
			return
		}
		d.reported[i] = true
		used = append(used, d.names[i])
	}
	if strings.EqualFold(msg.Tool, "skill") {
		if name, ok := msg.Input["skill"].(string); ok {
			mark(d.indexByName(name))
		}
	}
	walkInputStrings(msg.Input, func(s string) {
		for _, dir := range d.skillDirsIn(s) {
			mark(d.indexByDir(dir))
		}
	})
	return used
}

func (d *skillUsageDetector) indexByName(name string) int {
	name = strings.TrimSpace(name)
	// A plugin-qualified call ("plugin:skill") names the skill after the colon.
	if i := strings.LastIndex(name, ":"); i >= 0 {
		name = name[i+1:]
	}
	if name == "" {
		return -1
	}
	for i, n := range d.names {
		if n == name {
			return i
		}
	}
	slug := execenv.SanitizeSkillName(name)
	for i, s := range d.slugs {
		if s == slug {
			return i
		}
	}
	return -1
}

func (d *skillUsageDetector) indexByDir(dir string) int {
	for i, s := range d.slugs {
		if s == dir {
			return i
		}
	}
	for i, s := range d.slugs {
		if execenv.SkillDirMatchesSlug(dir, s) {
			return i
		}
	}
	return -1
}

// skillDirsIn returns the skill directory names s references as
// `<root>/<dir>/SKILL.md` for any of this run's roots.
func (d *skillUsageDetector) skillDirsIn(s string) []string {
	if !strings.Contains(s, "SKILL.md") {
		return nil
	}
	s = strings.ReplaceAll(s, `\`, "/")
	var dirs []string
	for _, p := range d.prefixes {
		needle := p.path + "/"
		for from := 0; from < len(s); {
			i := strings.Index(s[from:], needle)
			if i < 0 {
				break
			}
			i += from
			from = i + len(needle)
			if !skillRootBoundary(s, i, p.relative) {
				continue
			}
			rest := s[from:]
			slash := strings.IndexByte(rest, '/')
			if slash <= 0 || !strings.HasPrefix(rest[slash+1:], "SKILL.md") {
				continue
			}
			dirs = append(dirs, rest[:slash])
		}
	}
	return dirs
}

// skillRootBoundary reports whether a root match at s[i] starts a path rather
// than sitting in the middle of a longer one. A relative root may be written
// as "./.claude/skills".
func skillRootBoundary(s string, i int, relative bool) bool {
	if i == 0 {
		return true
	}
	prev := s[i-1]
	if !isPathByte(prev) {
		return true
	}
	if relative && prev == '/' && i >= 2 && s[i-2] == '.' {
		return i == 2 || !isPathByte(s[i-3])
	}
	return false
}

func isPathByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	switch c {
	case '/', '.', '_', '-', '~':
		return true
	}
	return c >= 0x80
}

// walkInputStrings calls fn for every string value anywhere in a tool input.
func walkInputStrings(v any, fn func(string)) {
	switch t := v.(type) {
	case string:
		fn(t)
	case map[string]any:
		for _, inner := range t {
			walkInputStrings(inner, fn)
		}
	case []any:
		for _, inner := range t {
			walkInputStrings(inner, fn)
		}
	case []string:
		for _, inner := range t {
			fn(inner)
		}
	}
}

// newTaskSkillUsageDetector wires a detector to the skill roots Prepare wrote
// for this task: the provider's native dir under the workdir, the shared-mode
// sidecar copy, and the CODEX_HOME / HERMES_HOME skill dirs.
func newTaskSkillUsageDetector(skills []SkillData, provider string, env *execenv.Environment) *skillUsageDetector {
	if len(skills) == 0 || env == nil {
		return nil
	}
	names := make([]string, len(skills))
	for i, s := range skills {
		names[i] = s.Name
	}
	roots := []string{execenv.SkillsDirPath(env.WorkDir, provider)}
	if env.SidecarRoot != "" {
		roots = append(roots, execenv.SkillsDirPath(env.SidecarRoot, provider))
	}
	if env.CodexHome != "" {
		roots = append(roots, filepath.Join(env.CodexHome, "skills"))
	}
	if env.HermesHome != "" {
		roots = append(roots, filepath.Join(env.HermesHome, "skills"))
	}
	return newSkillUsageDetector(names, env.WorkDir, roots...)
}
