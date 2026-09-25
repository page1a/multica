package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// agentCLIRelease is how one built-in agent CLI is versioned and upgraded.
//
// Latest comes from the npm registry when the CLI is published there, and
// from GitHub Releases when npm has no such package. The upgrade runs the
// tool's own command against the binary this daemon actually executes, and
// only falls back to `npm install -g` when that same binary lives inside
// that package. A second copy on the machine is left alone.
type agentCLIRelease struct {
	Provider   string
	NativeArgs []string // argv after the resolved binary, e.g. {"update"} for `claude update`
	NPMPackage string
	GitHubRepo string // owner/repo
}

// agentCLIReleases is the CLIs this daemon knows how to follow. A provider
// that is not listed is still shown; it is just not upgraded.
var agentCLIReleases = []agentCLIRelease{
	{
		Provider:   "claude",
		NativeArgs: []string{"update"},
		NPMPackage: "@anthropic-ai/claude-code",
		GitHubRepo: "anthropics/claude-code",
	},
	{
		Provider:   "codex",
		NPMPackage: "@openai/codex",
		GitHubRepo: "openai/codex",
	},
	{
		Provider:   "opencode",
		NativeArgs: []string{"upgrade"},
		NPMPackage: "opencode-ai",
		GitHubRepo: "anomalyco/opencode",
	},
}

func agentCLIReleaseFor(provider string) (agentCLIRelease, bool) {
	for _, spec := range agentCLIReleases {
		if spec.Provider == provider {
			return spec, true
		}
	}
	return agentCLIRelease{}, false
}

const (
	agentCLIPhaseCurrent     = "current"
	agentCLIPhaseAvailable   = "available"
	agentCLIPhaseWaiting     = "waiting"
	agentCLIPhaseUpdating    = "updating"
	agentCLIPhaseFailed      = "failed"
	agentCLIPhaseCheckFailed = "check_failed"
	agentCLIPhaseUnsupported = "unsupported"
	agentCLIPhaseUnknown     = "unknown"
)

// agentCLIStatus is what the runtime page shows next to one CLI.
// AutoFollow has no omitempty: false is a real choice.
type agentCLIStatus struct {
	CurrentVersion string `json:"current_version,omitempty"`
	LatestVersion  string `json:"latest_version,omitempty"`
	AutoFollow     bool   `json:"auto_follow"`
	Phase          string `json:"phase"`
	Error          string `json:"error,omitempty"`
	Note           string `json:"note,omitempty"`
	BinaryPath     string `json:"binary_path,omitempty"`
	CheckedAt      string `json:"checked_at,omitempty"`
}

// cliVersionPattern pulls the first x.y.z out of strings like
// "2.1.5 (Claude Code)" or "codex-cli 0.118.0".
var cliVersionPattern = regexp.MustCompile(`v?(\d+)\.(\d+)\.(\d+)`)

// cliVersionNewer reports whether latest is a strictly newer x.y.z than
// current. ok is false when either side has no three-part version, which
// must not be treated as "please upgrade".
func cliVersionNewer(latest, current string) (newer bool, ok bool) {
	l, lok := cliSemver(latest)
	c, cok := cliSemver(current)
	if !lok || !cok {
		return false, false
	}
	if l[0] != c[0] {
		return l[0] > c[0], true
	}
	if l[1] != c[1] {
		return l[1] > c[1], true
	}
	return l[2] > c[2], true
}

func cliSemver(raw string) ([3]int, bool) {
	m := cliVersionPattern.FindStringSubmatch(raw)
	if m == nil {
		return [3]int{}, false
	}
	var out [3]int
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// npmPackageDir is the node_modules segment that identifies one install,
// e.g. node_modules/@anthropic-ai/claude-code.
func npmPackageDir(pkg string) string {
	return filepath.Join("node_modules", filepath.FromSlash(pkg))
}

// npmInstallOf reports whether binaryPath (after symlink resolution) is the
// npm package pkg, and the npm prefix that owns that copy.
func npmInstallOf(binaryPath, pkg string) (prefix string, owned bool) {
	if binaryPath == "" || pkg == "" {
		return "", false
	}
	resolved := binaryPath
	if eval, err := filepath.EvalSymlinks(binaryPath); err == nil && eval != "" {
		resolved = eval
	}
	needle := npmPackageDir(pkg)
	idx := strings.Index(resolved, needle)
	if idx < 0 {
		return "", false
	}
	// The directory that contains node_modules. A unix global install is
	// <prefix>/lib/node_modules; Windows is <prefix>/node_modules.
	root := filepath.Clean(resolved[:idx])
	if filepath.Base(root) == "lib" {
		return filepath.Dir(root), true
	}
	return root, true
}

type agentCLIUpgradeStep struct {
	Name string
	Argv []string
}

// planAgentCLIUpgrade orders the commands that update the binary in use.
// Native first. npm only when this path is that package's install, so a
// global `npm install -g` cannot retarget a different copy.
func planAgentCLIUpgrade(spec agentCLIRelease, binaryPath string) ([]agentCLIUpgradeStep, string) {
	var steps []agentCLIUpgradeStep
	if len(spec.NativeArgs) > 0 && binaryPath != "" {
		argv := make([]string, 0, 1+len(spec.NativeArgs))
		argv = append(argv, binaryPath)
		argv = append(argv, spec.NativeArgs...)
		steps = append(steps, agentCLIUpgradeStep{Name: "native", Argv: argv})
	}
	if spec.NPMPackage != "" {
		if prefix, owned := npmInstallOf(binaryPath, spec.NPMPackage); owned {
			steps = append(steps, agentCLIUpgradeStep{
				Name: "npm",
				Argv: []string{"npm", "install", "-g", spec.NPMPackage + "@latest", "--prefix", prefix},
			})
		}
	}
	if len(steps) == 0 {
		return nil, fmt.Sprintf("no updater for the copy at %s", binaryPath)
	}
	return steps, ""
}

func agentCLILatestURL(spec agentCLIRelease) (npmURL, githubURL string) {
	if spec.NPMPackage != "" {
		npmURL = "https://registry.npmjs.org/" + spec.NPMPackage + "/latest"
	}
	if spec.GitHubRepo != "" {
		githubURL = "https://api.github.com/repos/" + spec.GitHubRepo + "/releases/latest"
	}
	return npmURL, githubURL
}

func parseNPMLatestVersion(body []byte) (string, error) {
	var doc struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", err
	}
	if strings.TrimSpace(doc.Version) == "" {
		return "", fmt.Errorf("npm latest response has no version")
	}
	return doc.Version, nil
}

func parseGitHubLatestVersion(body []byte) (string, error) {
	var doc struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", err
	}
	tag := strings.TrimSpace(doc.TagName)
	if tag == "" {
		return "", fmt.Errorf("github release has no tag")
	}
	return tag, nil
}

func clipCLIMessage(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := s[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "…"
}

func fetchURL(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "multica-daemon")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return body, nil
}

func agentCLIHTTPClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Second}
}
