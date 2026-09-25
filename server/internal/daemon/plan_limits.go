package daemon

import (
	"context"
	"os"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const planQuotaProbeInterval = 2 * time.Minute

// recordPlanLimits keeps the newest provider snapshot in daemon memory until a
// heartbeat delivers it. Built-in runtimes for the same provider share one CLI
// account across watched workspaces, so a snapshot observed on one is copied to
// its siblings. Custom-profile runtimes remain isolated because their command
// can authenticate as a different provider account.
func (d *Daemon) recordPlanLimits(runtimeID string, snapshot *protocol.PlanLimitsSnapshot) {
	if snapshot == nil || runtimeID == "" {
		return
	}

	d.mu.Lock()
	source, ok := d.runtimeIndex[runtimeID]
	if !ok {
		d.mu.Unlock()
		return
	}
	targets := []string{runtimeID}
	if source.ProfileID == "" {
		targets = targets[:0]
		for id, runtime := range d.runtimeIndex {
			if runtime.ProfileID == "" && runtime.Provider == source.Provider {
				targets = append(targets, id)
			}
		}
	}
	d.mu.Unlock()

	copySnapshot := clonePlanLimitsSnapshot(snapshot)
	copySnapshot.Provider = source.Provider
	d.planLimitsMu.Lock()
	if d.planLimits == nil {
		d.planLimits = make(map[string]protocol.PlanLimitsSnapshot)
	}
	for _, id := range targets {
		d.planLimits[id] = copySnapshot
	}
	d.planLimitsMu.Unlock()
}

func (d *Daemon) planLimitsForRuntime(runtimeID string) *protocol.PlanLimitsSnapshot {
	d.planLimitsMu.RLock()
	snapshot, ok := d.planLimits[runtimeID]
	d.planLimitsMu.RUnlock()
	if !ok {
		return nil
	}
	cloned := clonePlanLimitsSnapshot(&snapshot)
	return &cloned
}

// recordPlanLimitsForProvider copies a live probe snapshot onto every built-in
// runtime of that provider. Custom-profile runtimes stay untouched.
func (d *Daemon) recordPlanLimitsForProvider(provider string, snapshot *protocol.PlanLimitsSnapshot) {
	if provider == "" || snapshot == nil {
		return
	}
	d.mu.Lock()
	var runtimeID string
	for id, runtime := range d.runtimeIndex {
		if runtime.ProfileID == "" && runtime.Provider == provider {
			runtimeID = id
			break
		}
	}
	d.mu.Unlock()
	if runtimeID == "" {
		return
	}
	d.recordPlanLimits(runtimeID, snapshot)
}

// planLimitsByProvider is the localhost /health overlay. Desktop merges these
// onto runtime rows when the cloud API has no stored snapshot (official
// backends still ignore heartbeat plan_limits).
func (d *Daemon) planLimitsByProvider() map[string]protocol.PlanLimitsSnapshot {
	d.mu.Lock()
	providerRuntime := make(map[string]string)
	for id, runtime := range d.runtimeIndex {
		if runtime.ProfileID != "" {
			continue
		}
		if _, ok := providerRuntime[runtime.Provider]; !ok {
			providerRuntime[runtime.Provider] = id
		}
	}
	d.mu.Unlock()

	out := make(map[string]protocol.PlanLimitsSnapshot)
	for provider, runtimeID := range providerRuntime {
		if snap := d.planLimitsForRuntime(runtimeID); snap != nil {
			out[provider] = *snap
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (d *Daemon) maybeRefreshPlanQuota() {
	if !d.planQuotaInflight.CompareAndSwap(false, true) {
		return
	}
	d.planQuotaMu.Lock()
	if !d.lastPlanQuotaProbe.IsZero() && time.Since(d.lastPlanQuotaProbe) < planQuotaProbeInterval {
		d.planQuotaMu.Unlock()
		d.planQuotaInflight.Store(false)
		return
	}
	d.lastPlanQuotaProbe = time.Now()
	d.planQuotaMu.Unlock()

	go func() {
		defer d.planQuotaInflight.Store(false)
		d.refreshPlanQuota()
	}()
}

func (d *Daemon) refreshPlanQuota() {
	d.mu.Lock()
	var wantClaude, wantCodex, wantGemini, wantGrok, wantKimi, wantGLM, wantMiniMax, wantDeepSeek bool
	for _, runtime := range d.runtimeIndex {
		if runtime.ProfileID != "" {
			continue
		}
		switch runtime.Provider {
		case "claude":
			wantClaude = true
		case "codex":
			wantCodex = true
		case "gemini", "antigravity":
			wantGemini = true
		case "grok":
			wantGrok = true
		case "kimi":
			wantKimi = true
		case "glm":
			wantGLM = true
		case "minimax":
			wantMiniMax = true
		case "dsh":
			wantDeepSeek = true
		}
	}
	d.mu.Unlock()

	probe := d.planQuotaProbe()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Per-seat windows are probed on every cycle, including the one with no
	// built-in runtime to report (DENE-715): a bound agent may run on a
	// custom-profile runtime the index above deliberately skips.
	wantBuiltin := wantClaude || wantCodex || wantGemini || wantGrok || wantKimi || wantGLM || wantMiniMax || wantDeepSeek
	if !wantBuiltin {
		d.refreshAgentPlanQuota(ctx, probe)
		return
	}

	type probeJob struct {
		name    string
		targets []string
		run     func() (*protocol.PlanLimitsSnapshot, error)
	}
	jobs := make([]probeJob, 0, 8)
	if wantClaude {
		jobs = append(jobs, probeJob{name: "claude", targets: []string{"claude"}, run: func() (*protocol.PlanLimitsSnapshot, error) {
			return probe.ProbeClaude(ctx)
		}})
	}
	if wantCodex {
		jobs = append(jobs, probeJob{name: "codex", targets: []string{"codex"}, run: func() (*protocol.PlanLimitsSnapshot, error) {
			return probe.ProbeCodex(ctx)
		}})
	}
	if wantGemini {
		jobs = append(jobs, probeJob{name: "gemini", targets: []string{"gemini", "antigravity"}, run: func() (*protocol.PlanLimitsSnapshot, error) {
			return probe.ProbeGemini(ctx)
		}})
	}
	if wantGrok {
		jobs = append(jobs, probeJob{name: "grok", targets: []string{"grok"}, run: func() (*protocol.PlanLimitsSnapshot, error) {
			return probe.ProbeGrok(ctx)
		}})
	}
	if wantKimi {
		jobs = append(jobs, probeJob{name: "kimi", targets: []string{"kimi"}, run: func() (*protocol.PlanLimitsSnapshot, error) {
			return probe.ProbeKimi(ctx)
		}})
	}
	if wantGLM {
		jobs = append(jobs, probeJob{name: "glm", targets: []string{"glm"}, run: func() (*protocol.PlanLimitsSnapshot, error) {
			return probe.ProbeGLM(ctx)
		}})
	}
	if wantMiniMax {
		jobs = append(jobs, probeJob{name: "minimax", targets: []string{"minimax"}, run: func() (*protocol.PlanLimitsSnapshot, error) {
			return probe.ProbeMiniMax(ctx)
		}})
	}
	if wantDeepSeek {
		jobs = append(jobs, probeJob{name: "dsh", targets: []string{"dsh"}, run: func() (*protocol.PlanLimitsSnapshot, error) {
			return probe.ProbeDeepSeek(ctx)
		}})
	}

	for _, job := range jobs {
		snapshot, err := job.run()
		if err != nil {
			if d.logger != nil {
				d.logger.Debug("plan quota probe failed", "provider", job.name, "error", err)
			}
			continue
		}
		if snapshot == nil {
			continue
		}
		for _, provider := range job.targets {
			d.recordPlanLimitsForProvider(provider, snapshot)
		}
	}

	// Per-seat windows ride the same throttled cycle (DENE-715). They are
	// probed from the account directory each bound agent's task environment
	// names, not from the daemon's own account, so they stay separate from the
	// runtime snapshots above.
	d.refreshAgentPlanQuota(ctx, probe)
}

func (d *Daemon) planQuotaProbe() agent.PlanQuotaProbe {
	if d.planQuotaProbeFn != nil {
		return d.planQuotaProbeFn()
	}
	home := d.planQuotaHome
	if home == "" {
		if dir, err := os.UserHomeDir(); err == nil {
			home = dir
		}
	}
	probe := agent.PlanQuotaProbe{Home: home, Client: d.planQuotaClient}
	if d.planQuotaClaudeURL != "" {
		probe.ClaudeUsageURL = d.planQuotaClaudeURL
	}
	if d.planQuotaCodexURL != "" {
		probe.CodexUsageURL = d.planQuotaCodexURL
	}
	return probe
}

func clonePlanLimitsSnapshot(snapshot *protocol.PlanLimitsSnapshot) protocol.PlanLimitsSnapshot {
	cloned := *snapshot
	cloned.Windows = make([]protocol.PlanLimitWindow, len(snapshot.Windows))
	for i, window := range snapshot.Windows {
		cloned.Windows[i] = window
		if window.UsedPercent != nil {
			value := *window.UsedPercent
			cloned.Windows[i].UsedPercent = &value
		}
		if window.WindowMinutes != nil {
			value := *window.WindowMinutes
			cloned.Windows[i].WindowMinutes = &value
		}
		if window.ResetsAt != nil {
			value := *window.ResetsAt
			cloned.Windows[i].ResetsAt = &value
		}
		if window.Remaining != nil {
			value := *window.Remaining
			cloned.Windows[i].Remaining = &value
		}
	}
	return cloned
}
