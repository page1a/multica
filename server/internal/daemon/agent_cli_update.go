package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

// agentCLIUpdateLoop keeps each known agent CLI on the latest release when
// that CLI has no task running, and reports its state for the runtime page.
//
// It does not touch Multica's own CLI (auto_update.go). It also does not
// change any agent's configured model: after a successful upgrade it only
// asks the server to rediscover the model list.
func (d *Daemon) agentCLIUpdateLoop(ctx context.Context) {
	if d.agentCLIUpdateKick == nil {
		return
	}
	// The first look is soon after start, so the page is not blank for a
	// whole version-refresh interval. Later looks share that interval, and a
	// manual update or a CLI's last task finishing wakes the loop at once.
	// The minute check only gives back a provider held past its limit.
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(agentVersionRefreshInterval)
	defer ticker.Stop()
	holdCheck := time.NewTicker(time.Minute)
	defer holdCheck.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			d.reconcileAgentCLIs(ctx)
		case <-ticker.C:
			d.reconcileAgentCLIs(ctx)
		case <-d.agentCLIUpdateKick:
			d.reconcileAgentCLIs(ctx)
		case now := <-holdCheck.C:
			if d.agentCLIHoldOverdue(now) {
				d.reconcileAgentCLIs(ctx)
			}
		}
	}
}

func (d *Daemon) kickAgentCLIUpdate() {
	if d.agentCLIUpdateKick == nil {
		return
	}
	select {
	case d.agentCLIUpdateKick <- struct{}{}:
	default:
	}
}

// handleAgentCLICommand records a follow switch or a manual update delivered
// on the heartbeat. The upgrade itself waits for reconcileAgentCLIs, which
// is what enforces the per-provider idle gate.
func (d *Daemon) handleAgentCLICommand(runtimeID string, cmd *PendingAgentCLI) {
	if cmd == nil || runtimeID == "" {
		return
	}
	rt := d.findRuntime(runtimeID)
	if rt == nil || rt.ProfileID != "" {
		return
	}
	if _, ok := agentCLIReleaseFor(rt.Provider); !ok {
		return
	}
	d.agentCLIMu.Lock()
	d.ensureAgentCLIFollowLocked()
	changed := false
	if cmd.Follow != nil {
		// A repeated heartbeat still carries the click until the server
		// hears that it was applied. The value is one per CLI, so applying
		// the same click twice fights a newer click from another workspace.
		already := cmd.FollowID != "" && d.agentCLIFollowSeen[runtimeID] == cmd.FollowID
		if !already {
			current, ok := d.agentCLIFollow[rt.Provider]
			if !ok || current != *cmd.Follow {
				d.agentCLIFollow[rt.Provider] = *cmd.Follow
				changed = true
				if err := d.writeAgentCLIFollowLocked(); err != nil && d.logger != nil {
					d.logger.Warn("save agent CLI follow preference", "provider", rt.Provider, "error", err)
				}
			}
			if cmd.FollowID != "" {
				if d.agentCLIFollowSeen == nil {
					d.agentCLIFollowSeen = map[string]string{}
				}
				d.agentCLIFollowSeen[runtimeID] = cmd.FollowID
				changed = true
			}
		}
	}
	var replaced string
	if cmd.UpdateNow && cmd.RequestID != "" && d.agentCLIManual.RequestID != cmd.RequestID {
		if prev := d.agentCLIManual.Provider; prev != "" && prev != rt.Provider {
			replaced = prev
		}
		d.agentCLIManual = agentCLIManual{
			RuntimeID: runtimeID,
			Provider:  rt.Provider,
			RequestID: cmd.RequestID,
		}
		changed = true
	}
	d.agentCLIMu.Unlock()
	// One "update now" is pending per machine; a click for another CLI
	// replaces it, and the replaced CLI must not stay held.
	if replaced != "" {
		d.releaseAgentCLIHold(replaced)
	}
	// A heartbeat repeats the same command until it is cleared. Only a new
	// choice wakes the loop; otherwise the idle kick and the regular tick
	// are what retry an upgrade.
	if changed {
		d.kickAgentCLIUpdate()
	}
}

// agentCLIManual is one "update now" still waiting to run or to be acked.
// HoldExpired is set once it held the provider for agentCLIHoldLimit; from
// then on it waits like follow does, without holding new tasks back.
type agentCLIManual struct {
	RuntimeID   string
	Provider    string
	RequestID   string
	HoldExpired bool
}

func (d *Daemon) reconcileAgentCLIs(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	d.agentCLIMu.Lock()
	d.ensureAgentCLIFollowLocked()
	d.agentCLIMu.Unlock()
	// Before anything can return early (a failed version check, a missing
	// CLI), give back any provider held past its limit.
	d.expireOverdueAgentCLIHold(time.Now())

	agents := d.agents()
	if len(agents) == 0 {
		return
	}
	for _, spec := range agentCLIReleases {
		entry, ok := agents[spec.Provider]
		if !ok || strings.TrimSpace(entry.Path) == "" {
			continue
		}
		d.reconcileOneAgentCLI(ctx, spec, entry.Path)
		if ctx.Err() != nil {
			return
		}
	}
	// Installed CLIs we do not know how to upgrade still get a row, so the
	// page can say so instead of looking like the check never ran.
	for provider, entry := range agents {
		if _, known := agentCLIReleaseFor(provider); known || strings.TrimSpace(entry.Path) == "" {
			continue
		}
		d.publishAgentCLIStatus(ctx, provider, agentCLIStatus{
			CurrentVersion: d.agentCLICurrentVersion(provider),
			AutoFollow:     false,
			Phase:          agentCLIPhaseUnsupported,
			BinaryPath:     entry.Path,
			CheckedAt:      time.Now().UTC().Format(time.RFC3339),
			Note:           "This copy: " + entry.Path,
		}, agentCLIManual{})
	}
}

func (d *Daemon) reconcileOneAgentCLI(ctx context.Context, spec agentCLIRelease, binaryPath string) {
	follow := d.agentCLIWantsFollow(spec.Provider)
	manual := d.peekAgentCLIManual(spec.Provider)
	status := agentCLIStatus{
		CurrentVersion: d.agentCLICurrentVersion(spec.Provider),
		AutoFollow:     follow,
		BinaryPath:     binaryPath,
		CheckedAt:      time.Now().UTC().Format(time.RFC3339),
		Note:           "This copy: " + binaryPath,
	}

	if strings.TrimSpace(status.CurrentVersion) == "" {
		status.Phase = agentCLIPhaseUnknown
		status.Error = "local version has not been read yet"
		d.publishAgentCLIStatus(ctx, spec.Provider, status, agentCLIManual{})
		return
	}

	latest, err := d.fetchAgentCLILatest(ctx, spec)
	if err != nil {
		status.Phase = agentCLIPhaseCheckFailed
		status.Error = clipCLIMessage(err.Error(), 400)
		d.publishAgentCLIStatus(ctx, spec.Provider, status, agentCLIManual{})
		return
	}
	status.LatestVersion = latest
	newer, comparable := cliVersionNewer(latest, status.CurrentVersion)
	if !comparable {
		status.Phase = agentCLIPhaseCheckFailed
		status.Error = "cannot compare " + status.CurrentVersion + " with " + latest
		d.publishAgentCLIStatus(ctx, spec.Provider, status, agentCLIManual{})
		return
	}
	if !newer {
		status.Phase = agentCLIPhaseCurrent
		if manual.RequestID != "" {
			status.Note = "Already the latest copy: " + binaryPath
		}
		d.publishAgentCLIStatus(ctx, spec.Provider, status, manual)
		d.finishAgentCLIManual(spec.Provider, manual.RequestID)
		return
	}

	wantUpgrade := follow || manual.RequestID != ""
	steps, why := planAgentCLIUpgrade(spec, binaryPath)
	if !wantUpgrade {
		status.Phase = agentCLIPhaseAvailable
		d.clearAgentCLIWaiting(spec.Provider)
		d.publishAgentCLIStatus(ctx, spec.Provider, status, agentCLIManual{})
		return
	}
	if len(steps) == 0 {
		status.Phase = agentCLIPhaseUnsupported
		status.Error = why
		d.publishAgentCLIStatus(ctx, spec.Provider, status, manual)
		d.finishAgentCLIManual(spec.Provider, manual.RequestID)
		return
	}

	// Only this CLI waits. Other providers keep taking tasks throughout;
	// an "update now" also stops this provider taking new ones, for at most
	// agentCLIHoldLimit, so its running tasks can drain.
	now := time.Now()
	holding := manual.RequestID != "" && !manual.HoldExpired
	if holding {
		since := d.holdAgentCLIClaims(spec.Provider, now)
		if now.Sub(since) >= agentCLIHoldLimit {
			d.expireAgentCLIHold(spec.Provider, manual.RequestID)
			manual.HoldExpired = true
			holding = false
		}
	}
	started, running, selfUpdate := d.tryBeginAgentCLIUpgrade(spec.Provider, now)
	if !started {
		status.Phase = agentCLIPhaseWaiting
		status.WaitingTasks = running
		status.ClaimsPaused = holding
		switch {
		case selfUpdate:
			status.WaitReason = agentCLIWaitDaemonUpdate
			status.Note = "Waiting for the Multica daemon update on this machine to finish"
		case manual.HoldExpired:
			status.WaitReason = agentCLIWaitHoldExpired
			status.Note = fmt.Sprintf("Held new %s tasks for %s but %d still running; taking tasks again and updating once none is running", spec.Provider, agentCLIHoldLimit, running)
		case running == 0:
			// Only a claim is in flight; its exit wakes the updater.
			status.WaitReason = agentCLIWaitTasks
			status.Note = fmt.Sprintf("Waiting for a %s task claim to finish", spec.Provider)
		default:
			status.WaitReason = agentCLIWaitTasks
			if holding {
				status.Note = fmt.Sprintf("Not taking new %s tasks; updating once %d running finish", spec.Provider, running)
			} else {
				status.Note = fmt.Sprintf("Updating once %d running %s tasks finish", running, spec.Provider)
			}
		}
		d.publishAgentCLIStatus(ctx, spec.Provider, status, agentCLIManual{})
		return
	}
	defer d.endAgentCLIUpgrade(spec.Provider)

	status.Phase = agentCLIPhaseUpdating
	status.Error = ""
	status.ClaimsPaused = true
	d.publishAgentCLIStatus(ctx, spec.Provider, status, agentCLIManual{})
	status.ClaimsPaused = false

	if err := d.runAgentCLIUpgrade(ctx, steps); err != nil {
		status.Phase = agentCLIPhaseFailed
		status.Error = clipCLIMessage(err.Error(), 500)
		d.publishAgentCLIStatus(ctx, spec.Provider, status, manual)
		d.finishAgentCLIManual(spec.Provider, manual.RequestID)
		return
	}

	if d.agentCLIAfterUpgrade != nil {
		d.agentCLIAfterUpgrade(ctx, spec.Provider)
	} else {
		d.refreshAgentVersions(ctx)
		d.refreshAgentCLIModelCatalogs(ctx, spec.Provider)
	}
	if v := d.agentCLICurrentVersion(spec.Provider); v != "" {
		status.CurrentVersion = v
	}
	status.Error = ""
	status.Note = "Updated this copy: " + binaryPath
	status.Phase = agentCLIPhaseCurrent
	status.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if stillNewer, ok := cliVersionNewer(latest, status.CurrentVersion); ok && stillNewer {
		status.Phase = agentCLIPhaseAvailable
		status.UpdatedAt = ""
		status.Note = "Updater finished, but this copy still reads " + status.CurrentVersion + ": " + binaryPath
	}
	d.publishAgentCLIStatus(ctx, spec.Provider, status, manual)
	d.finishAgentCLIManual(spec.Provider, manual.RequestID)
}

func (d *Daemon) agentCLICurrentVersion(provider string) string {
	d.versionsMu.RLock()
	defer d.versionsMu.RUnlock()
	if d.agentVersions == nil {
		return ""
	}
	return d.agentVersions[provider]
}

func (d *Daemon) agentCLIWantsFollow(provider string) bool {
	d.agentCLIMu.Lock()
	defer d.agentCLIMu.Unlock()
	d.ensureAgentCLIFollowLocked()
	follow, ok := d.agentCLIFollow[provider]
	if !ok {
		return true
	}
	return follow
}

func (d *Daemon) peekAgentCLIManual(provider string) agentCLIManual {
	d.agentCLIMu.Lock()
	defer d.agentCLIMu.Unlock()
	if d.agentCLIManual.Provider == provider {
		return d.agentCLIManual
	}
	return agentCLIManual{}
}

// finishAgentCLIManual ends one "update now": its request is acked and its
// hold on the provider's tasks, if any, is given back.
func (d *Daemon) finishAgentCLIManual(provider, requestID string) {
	d.clearAgentCLIWaiting(provider)
	if requestID == "" {
		return
	}
	d.agentCLIMu.Lock()
	cleared := d.agentCLIManual.RequestID == requestID
	if cleared {
		d.agentCLIManual = agentCLIManual{}
	}
	d.agentCLIMu.Unlock()
	if cleared {
		d.releaseAgentCLIHold(provider)
	}
}

func (d *Daemon) expireOverdueAgentCLIHold(now time.Time) {
	if !d.agentCLIHoldOverdue(now) {
		return
	}
	d.agentCLIMu.Lock()
	manual := d.agentCLIManual
	d.agentCLIMu.Unlock()
	d.claimMu.Lock()
	var overdue []string
	for provider, h := range d.cliGate.held {
		if !h.upgrading && now.Sub(h.since) >= agentCLIHoldLimit {
			overdue = append(overdue, provider)
		}
	}
	d.claimMu.Unlock()
	for _, provider := range overdue {
		if manual.Provider == provider {
			d.expireAgentCLIHold(provider, manual.RequestID)
		} else {
			d.releaseAgentCLIHold(provider)
		}
	}
}

// expireAgentCLIHold gives the provider back its tasks after a hold ran out,
// while the request itself keeps waiting for an idle moment.
func (d *Daemon) expireAgentCLIHold(provider, requestID string) {
	d.agentCLIMu.Lock()
	if d.agentCLIManual.RequestID == requestID {
		d.agentCLIManual.HoldExpired = true
	}
	d.agentCLIMu.Unlock()
	d.releaseAgentCLIHold(provider)
	if d.logger != nil {
		d.logger.Warn("agent CLI update held new tasks past its limit; taking tasks again", "provider", provider, "limit", agentCLIHoldLimit)
	}
}

func (d *Daemon) fetchAgentCLILatest(ctx context.Context, spec agentCLIRelease) (string, error) {
	npmURL, githubURL := agentCLILatestURL(spec)
	fetch := d.agentCLIFetch
	if fetch == nil {
		client := agentCLIHTTPClient()
		fetch = func(ctx context.Context, url string) ([]byte, error) {
			return fetchURL(ctx, client, url)
		}
	}
	var npmErr error
	if npmURL != "" {
		body, err := fetch(ctx, npmURL)
		if err == nil {
			version, parseErr := parseNPMLatestVersion(body)
			if parseErr == nil {
				return version, nil
			}
			npmErr = parseErr
		} else {
			npmErr = err
		}
		// A missing npm package falls through to GitHub. A network failure
		// does not: both sources need the network, and the page should show
		// that failure instead of waiting out a second timeout.
		if !isNPMNotFound(npmErr) || githubURL == "" {
			return "", npmErr
		}
	}
	if githubURL == "" {
		if npmErr != nil {
			return "", npmErr
		}
		return "", fmt.Errorf("no version source for %s", spec.Provider)
	}
	body, err := fetch(ctx, githubURL)
	if err != nil {
		return "", err
	}
	return parseGitHubLatestVersion(body)
}

func isNPMNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "404") || strings.Contains(msg, "Not Found")
}

func (d *Daemon) runAgentCLIUpgrade(ctx context.Context, steps []agentCLIUpgradeStep) error {
	var last error
	for i, step := range steps {
		err := d.runOneAgentCLIStep(ctx, step)
		if err == nil {
			return nil
		}
		last = err
		if i == len(steps)-1 {
			break
		}
		if d.logger != nil {
			d.logger.Info("agent CLI updater failed; trying the next one", "step", step.Name, "error", err)
		}
	}
	if last == nil {
		return fmt.Errorf("no upgrade step ran")
	}
	return last
}

func (d *Daemon) runOneAgentCLIStep(ctx context.Context, step agentCLIUpgradeStep) error {
	if len(step.Argv) == 0 {
		return fmt.Errorf("empty upgrade command")
	}
	argv := append([]string(nil), step.Argv...)
	timeout := 3 * time.Minute
	if step.Name == "npm" {
		timeout = 5 * time.Minute
		npm, err := d.lookupAgentCLI("npm")
		if err != nil {
			return fmt.Errorf("npm is not on PATH: %w", err)
		}
		argv[0] = npm
	}
	run := d.agentCLIRun
	if run == nil {
		run = defaultAgentCLIRun
	}
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := run(stepCtx, argv[0], argv[1:]...)
	if err != nil {
		return fmt.Errorf("%s: %w: %s", step.Name, err, clipCLIMessage(string(out), 400))
	}
	return nil
}

func (d *Daemon) lookupAgentCLI(name string) (string, error) {
	if d.agentCLILookPath != nil {
		return d.agentCLILookPath(name)
	}
	return lookPath(name)
}

func defaultAgentCLIRun(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func (d *Daemon) refreshAgentCLIModelCatalogs(ctx context.Context, provider string) {
	if d.client == nil {
		return
	}
	for _, rt := range d.runtimesForProvider(provider) {
		if err := d.client.RefreshModelCatalog(ctx, rt.ID); err != nil && d.logger != nil {
			d.logger.Warn("refresh model catalog after CLI upgrade", "provider", provider, "runtime_id", rt.ID, "error", err)
		}
	}
}

func (d *Daemon) runtimesForProvider(provider string) []Runtime {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []Runtime
	for _, rt := range d.runtimeIndex {
		if rt.Provider == provider && rt.ProfileID == "" {
			out = append(out, rt)
		}
	}
	return out
}

func (d *Daemon) publishAgentCLIStatus(ctx context.Context, provider string, status agentCLIStatus, manual agentCLIManual) {
	if d.client == nil {
		return
	}
	acks := d.agentCLIFollowAcks()
	for _, rt := range d.runtimesForProvider(provider) {
		body := map[string]any{"cli_update": status}
		if manual.RequestID != "" && manual.RuntimeID == rt.ID {
			body["applied_request_id"] = manual.RequestID
		}
		if id := acks[rt.ID]; id != "" {
			body["applied_follow_id"] = id
		}
		if err := d.client.ReportAgentCLIStatus(ctx, rt.ID, body); err != nil && d.logger != nil {
			d.logger.Warn("report agent CLI status", "provider", provider, "runtime_id", rt.ID, "error", err)
		}
	}
}

func (d *Daemon) agentCLIFollowAcks() map[string]string {
	d.agentCLIMu.Lock()
	defer d.agentCLIMu.Unlock()
	if len(d.agentCLIFollowSeen) == 0 {
		return nil
	}
	out := make(map[string]string, len(d.agentCLIFollowSeen))
	for runtimeID, followID := range d.agentCLIFollowSeen {
		out[runtimeID] = followID
	}
	return out
}

func (d *Daemon) ensureAgentCLIFollowLocked() {
	if d.agentCLIFollowLoaded {
		return
	}
	d.agentCLIFollowLoaded = true
	if d.agentCLIFollow == nil {
		d.agentCLIFollow = map[string]bool{}
	}
	path, err := d.agentCLIFollowFilePath()
	if err != nil {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var saved map[string]bool
	if err := json.Unmarshal(raw, &saved); err != nil {
		return
	}
	for provider, follow := range saved {
		d.agentCLIFollow[provider] = follow
	}
}

func (d *Daemon) writeAgentCLIFollowLocked() error {
	path, err := d.agentCLIFollowFilePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(d.agentCLIFollow, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (d *Daemon) agentCLIFollowFilePath() (string, error) {
	if d.agentCLIFollowFile != "" {
		return d.agentCLIFollowFile, nil
	}
	dir, err := cli.ProfileDir(d.cfg.Profile)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "agent-cli-follow.json"), nil
}
