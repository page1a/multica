package daemon

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	defaultBlockWaitProbeInterval = 3 * time.Minute
	blockWaitProbeTimeout         = 30 * time.Second
	blockWaitProbeOutputLimit     = 2048
)

// waitProbeShell runs the user-authored one-line probe in the same shell a
// local task would use. The server only stores and routes the result; the
// command never executes on the server.
func waitProbeShell(ctx context.Context, command, dir string) (int, string) {
	command = strings.TrimSpace(command)
	if command == "" {
		return 1, "empty wait probe"
	}
	args := []string{"-c", command}
	name := "sh"
	if runtime.GOOS == "windows" {
		name, args = "cmd.exe", []string{"/d", "/s", "/c", command}
	}
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var output cappedBuffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if err == nil {
		return 0, output.String()
	}
	if ctx.Err() != nil {
		return 124, strings.TrimSpace(output.String() + " probe timed out")
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), output.String()
	}
	return 1, strings.TrimSpace(output.String() + " " + err.Error())
}

type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) String() string {
	data := b.Buffer.Bytes()
	if len(data) > blockWaitProbeOutputLimit {
		data = data[:blockWaitProbeOutputLimit]
	}
	return string(data)
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	remaining := blockWaitProbeOutputLimit - b.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.Buffer.Write(p[:remaining])
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

func (d *Daemon) blockWaitWorkspaces() map[string][]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	workspaces := make(map[string][]string, len(d.workspaces))
	for id, state := range d.workspaces {
		workspaces[id] = append([]string(nil), state.runtimeIDs...)
	}
	return workspaces
}

func (d *Daemon) blockWaitProbeLoop(ctx context.Context) {
	d.blockWaitProbeTick(ctx)
	ticker := time.NewTicker(defaultBlockWaitProbeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.blockWaitProbeTick(ctx)
		}
	}
}

func (d *Daemon) blockWaitProbeTick(ctx context.Context) {
	for workspaceID, runtimeIDs := range d.blockWaitWorkspaces() {
		for _, runtimeID := range runtimeIDs {
			probes, err := d.client.ListBlockWaitProbes(ctx, workspaceID, runtimeID)
			if err != nil {
				d.logger.Warn("block wait probe list failed", "workspace_id", workspaceID, "runtime_id", runtimeID, "error", err)
				continue
			}
			for _, probe := range probes {
				probeCtx, cancel := context.WithTimeout(ctx, blockWaitProbeTimeout)
				dir := probe.WorkDir
				if dir == "" {
					cancel()
					continue
				}
				if stat, statErr := os.Stat(dir); statErr != nil || !stat.IsDir() {
					cancel()
					continue
				}
				exitCode, output := waitProbeShell(probeCtx, probe.WaitProbe, dir)
				cancel()
				if _, err := d.client.ReportBlockWaitProbe(ctx, probe.ID, exitCode, output); err != nil {
					d.logger.Warn("block wait probe report failed", "issue_id", probe.ID, "error", err)
				}
			}
		}
	}
}
