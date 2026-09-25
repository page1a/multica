package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCLIVersionNewer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		latest, current string
		newer, ok       bool
	}{
		{"2.1.9", "2.1.5 (Claude Code)", true, true},
		{"codex-cli 0.118.0", "0.118.0", false, true},
		{"1.2.0", "1.2.0", false, true},
		{"1.0.0", "1.0.1", false, true},
		{"dev", "1.0.0", false, false},
		{"2.0.0", "", false, false},
	}
	for _, tc := range cases {
		newer, ok := cliVersionNewer(tc.latest, tc.current)
		if newer != tc.newer || ok != tc.ok {
			t.Errorf("cliVersionNewer(%q, %q) = %v, %v; want %v, %v", tc.latest, tc.current, newer, ok, tc.newer, tc.ok)
		}
	}
}

func TestNPMInstallOfFollowsTheCopyInUse(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	pkgDir := filepath.Join(root, "lib", "node_modules", "@anthropic-ai", "claude-code", "bin")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(pkgDir, "claude")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(binDir, "claude")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	prefix, owned := npmInstallOf(link, "@anthropic-ai/claude-code")
	if !owned {
		t.Fatal("symlink into the npm package should count as that install")
	}
	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if prefix != wantRoot {
		t.Fatalf("prefix = %q, want %q", prefix, wantRoot)
	}

	other := filepath.Join(root, "elsewhere", "claude")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, owned := npmInstallOf(other, "@anthropic-ai/claude-code"); owned {
		t.Fatal("a binary outside node_modules must not be treated as the npm install")
	}
}

func TestPlanAgentCLIUpgradeKeepsTheOtherCopy(t *testing.T) {
	t.Parallel()
	spec, ok := agentCLIReleaseFor("claude")
	if !ok {
		t.Fatal("claude release missing")
	}
	steps, why := planAgentCLIUpgrade(spec, "/usr/local/bin/claude")
	if why != "" {
		t.Fatal(why)
	}
	if len(steps) != 1 || steps[0].Name != "native" {
		t.Fatalf("native-only plan = %#v", steps)
	}
	if steps[0].Argv[0] != "/usr/local/bin/claude" || steps[0].Argv[1] != "update" {
		t.Fatalf("argv = %#v", steps[0].Argv)
	}
}

func TestPlanAgentCLIUpgradeAddsNPMForThatPrefix(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := filepath.Join(root, "lib", "node_modules", "@openai", "codex", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec, _ := agentCLIReleaseFor("codex")
	steps, why := planAgentCLIUpgrade(spec, bin)
	if why != "" {
		t.Fatal(why)
	}
	if len(steps) != 1 || steps[0].Name != "npm" {
		t.Fatalf("codex has no native updater; plan = %#v", steps)
	}
	wantPrefix, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	gotPrefix := ""
	for i, arg := range steps[0].Argv {
		if arg == "--prefix" && i+1 < len(steps[0].Argv) {
			gotPrefix = steps[0].Argv[i+1]
		}
	}
	if gotPrefix != wantPrefix {
		t.Fatalf("prefix = %q, want %q (argv %v)", gotPrefix, wantPrefix, steps[0].Argv)
	}
}

func TestParseLatestVersions(t *testing.T) {
	t.Parallel()
	npm, err := parseNPMLatestVersion([]byte(`{"version":"2.1.9"}`))
	if err != nil || npm != "2.1.9" {
		t.Fatalf("npm = %q, %v", npm, err)
	}
	tag, err := parseGitHubLatestVersion([]byte(`{"tag_name":"v1.2.3"}`))
	if err != nil || tag != "v1.2.3" {
		t.Fatalf("github = %q, %v", tag, err)
	}
}
