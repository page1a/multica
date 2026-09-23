package daemon

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestProbeAgentCLIs_DiscoversDevin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}

	fakeDir := t.TempDir()
	writeDaemonTestExecutable(t, filepath.Join(fakeDir, "devin"), []byte("#!/bin/sh\nexit 0\n"))

	orig := resolveAgentsViaLoginShell
	t.Cleanup(func() { resolveAgentsViaLoginShell = orig })
	resolveAgentsViaLoginShell = func([]string) map[string]string {
		return map[string]string{}
	}
	resetShellResolveCacheForTest(t)

	t.Setenv("PATH", fakeDir)
	t.Setenv("MULTICA_DEVIN_PATH", "")

	entry, ok := probeAgentCLIs()["devin"]
	if !ok {
		t.Fatal("devin was not discovered; a devin binary on PATH should register a runtime")
	}
	if entry.Command != "devin" {
		t.Fatalf("devin command = %q, want devin", entry.Command)
	}
	if got := providerDisplayName("devin"); got != "Devin" {
		t.Fatalf("devin display name = %q, want Devin", got)
	}
}
