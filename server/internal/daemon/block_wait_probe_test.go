package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestWaitProbeShellUsesWorkingDirectoryAndExitCode(t *testing.T) {
	probe := "test -f marker && printf ready"
	if runtime.GOOS == "windows" {
		probe = "if exist marker (echo ready) else (exit /b 1)"
	}
	code, output := waitProbeShell(context.Background(), probe, t.TempDir())
	if code != 1 {
		t.Fatalf("missing marker exit code = %d, want 1", code)
	}
	if output != "" {
		t.Fatalf("missing marker output = %q", output)
	}

	dir := t.TempDir()
	if err := writeTestFile(dir, "marker", ""); err != nil {
		t.Fatal(err)
	}
	code, output = waitProbeShell(context.Background(), probe, dir)
	wantOutput := "ready"
	if runtime.GOOS == "windows" {
		wantOutput = "ready\r\n"
	}
	if code != 0 || strings.TrimSpace(output) != strings.TrimSpace(wantOutput) {
		t.Fatalf("ready probe = (%d, %q), want (0, %s)", code, output, wantOutput)
	}
}

func TestWaitProbeShellPreservesPendingExitCodeAndCapsOutput(t *testing.T) {
	code, output := waitProbeShell(context.Background(), "printf pending >&2; exit 10", "")
	if code != 10 || output != "pending" {
		t.Fatalf("pending probe = (%d, %q), want (10, pending)", code, output)
	}

	code, output = waitProbeShell(context.Background(), "printf '%03000d' 1; exit 2", "")
	if code != 2 || len(output) != blockWaitProbeOutputLimit {
		t.Fatalf("capped probe = (%d, %d bytes), want (2, %d)", code, len(output), blockWaitProbeOutputLimit)
	}
}

func TestWaitProbeShellReportsTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	command := "sleep 1"
	if runtime.GOOS == "windows" {
		command = "ping -n 3 127.0.0.1 >NUL"
	}
	code, output := waitProbeShell(ctx, command, "")
	if code != 124 || !strings.Contains(output, "timed out") {
		t.Fatalf("timeout probe = (%d, %q), want 124 and timeout text", code, output)
	}
}

func writeTestFile(dir, name, contents string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600)
}
