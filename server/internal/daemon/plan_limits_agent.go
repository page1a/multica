package daemon

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// This file is the per-seat half of the plan-quota channel (DENE-715).
//
// Why it exists: `agent_runtime.plan_limits` is one row per
// (workspace, daemon, provider), but a single Claude runtime serves every
// Claude seat on the machine — ten agents, one runtime row. An agent switched
// to a numbered account is served by `custom_env.CLAUDE_CONFIG_DIR` at launch,
// and the runtime row has no way to say so. Probing that directory into the
// runtime row would fix the bound agent's panel by breaking its unbound
// siblings', which is why the answer travels per agent instead.
//
// The daemon learns a binding from the task it is about to run — the same
// custom_env it is already assembling the child's environment from, so there is
// no second derivation of "which directory does this agent use". An agent it
// has never run keeps no entry, keeps reading the runtime row, and therefore
// keeps its previous behavior.

// maxAgentPlanQuotaEntries bounds the tracked set. A daemon runs the agents of
// the workspaces it is authorized for; the cap is far above any real fleet and
// only exists so a pathological claim stream cannot grow the map forever.
const maxAgentPlanQuotaEntries = 512

// agentPlanQuota is one tracked agent: the CLI account directory its task
// environment binds, the runtime it was seen on, and the newest snapshot
// observed for that directory. An empty dir means the runtime's own account —
// what a probe with no explicit config directory reads — which is where an
// agent lands after dropping a numbered-account binding.
type agentPlanQuota struct {
	runtimeID string
	cli       string
	dir       string
	snapshot  *protocol.PlanLimitsSnapshot
}

// agentPlanQuotaProbeable reports whether this daemon can read a plan-window
// snapshot out of one account directory for cli.
//
// Only Claude qualifies today: its usage endpoint is reachable from the OAuth
// token in the account's own `.credentials.json`. dsh's DeepSeek balance comes
// from an API key rather than a home directory, and Codex and Cursor cannot be
// rebound at all (the task environment rewrites CODEX_HOME / CURSOR_DATA_DIR),
// so tracking a directory for them would report a switch that does not exist.
func agentPlanQuotaProbeable(cli string) bool {
	return cli == "claude"
}

// agentBoundAccountDir returns the account directory an agent's own custom_env
// explicitly binds for cli, resolved exactly the way the task environment
// resolves it, or "" when the agent binds nothing.
//
// customEnv is the agent's stored binding; taskEnv is the environment the child
// process is actually launched with. Reading the value out of taskEnv (rather
// than out of customEnv directly) keeps this in step with the layer that
// applies it — including its blocked-key filtering — while the membership check
// on customEnv is what distinguishes "this agent chose an account" from "this
// agent inherited the daemon's own".
func agentBoundAccountDir(cli string, customEnv, taskEnv map[string]string, home string) string {
	home = strings.TrimSpace(home)
	for _, probe := range agentCLIProbes {
		if probe.CLI != cli {
			continue
		}
		key := accountLeverEnvKey(probe.Lever)
		if key == "" {
			return ""
		}
		if _, bound := customEnv[key]; !bound {
			return ""
		}
		value := strings.TrimSpace(taskEnv[key])
		if value == "" {
			return ""
		}
		if home != "" {
			value = expandAccountHomePrefix(value, home)
		}
		// A relative override cannot be probed as a directory: it would resolve
		// against whatever cwd the daemon happens to have, which is a guess.
		if !filepath.IsAbs(value) {
			return ""
		}
		return agent.NormalizeAgyDir(value)
	}
	return ""
}

// recordAgentAccountBinding remembers which account directory this agent's next
// run will use. It is called from the task-environment assembly, next to the
// layer that writes the lever, so the directory recorded here is the directory
// the child gets.
//
// An agent the daemon has never seen bound is not tracked at all: no entry
// means no per-agent snapshot, and its panel keeps reading the runtime row
// exactly as it did before DENE-715.
//
// An agent that HAD a binding and dropped it keeps its entry, now pointing at
// the runtime's own account (the empty directory), because the alternative is
// worse: an entry that simply vanished stops refreshing the agent row, and the
// row would then freeze the numbered account's last numbers — a panel naming a
// seat the agent no longer uses, which is the failure this whole change exists
// to remove. Following the runtime's account makes the agent row agree with the
// runtime row within one probe cycle. A move to a provider with no lever at all
// does forget the entry: there is nothing truthful left to report for it.
func (d *Daemon) recordAgentAccountBinding(runtimeID, agentID, provider string, customEnv, taskEnv map[string]string) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return
	}
	cli := strings.TrimSpace(provider)
	probeable := agentPlanQuotaProbeable(cli)
	dir := ""
	if probeable {
		home, err := agyQuotaHomeFn()
		if err != nil {
			home = ""
		}
		dir = agentBoundAccountDir(cli, customEnv, taskEnv, home)
	}

	d.planAgentMu.Lock()
	defer d.planAgentMu.Unlock()
	existing := d.planAgentQuota[agentID]
	if !probeable {
		delete(d.planAgentQuota, agentID)
		return
	}
	if existing == nil && dir == "" {
		return
	}
	if existing != nil {
		if existing.dir != dir {
			// A new seat (or the runtime's own account after a dropped binding).
			// The observed snapshot belongs to the seat that produced it, so it
			// is not carried over: the next probe fills this one in.
			existing.snapshot = nil
		}
		existing.runtimeID = runtimeID
		existing.cli = cli
		existing.dir = dir
		return
	}
	if len(d.planAgentQuota) >= maxAgentPlanQuotaEntries {
		return
	}
	if d.planAgentQuota == nil {
		d.planAgentQuota = make(map[string]*agentPlanQuota)
	}
	d.planAgentQuota[agentID] = &agentPlanQuota{runtimeID: runtimeID, cli: cli, dir: dir}
}

// refreshAgentPlanQuota probes every distinct bound directory once and stores
// the result against each agent that shares it.
//
// Grouping by directory is the point: a machine with two numbered accounts
// costs two usage requests per cycle no matter how many agents are bound to
// them, and the daemon never asks the provider a question it already answered
// this cycle.
func (d *Daemon) refreshAgentPlanQuota(ctx context.Context, probe agent.PlanQuotaProbe) {
	d.planAgentMu.Lock()
	targets := make([]*agentPlanQuota, 0, len(d.planAgentQuota))
	for _, entry := range d.planAgentQuota {
		targets = append(targets, entry)
	}
	d.planAgentMu.Unlock()
	if len(targets) == 0 {
		return
	}

	type dirKey struct{ cli, dir string }
	snapshots := make(map[dirKey]*protocol.PlanLimitsSnapshot, len(targets))
	for _, entry := range targets {
		key := dirKey{cli: entry.cli, dir: entry.dir}
		if _, done := snapshots[key]; done {
			continue
		}
		snapshot, err := probeAccountDir(ctx, probe, entry.cli, entry.dir)
		if err != nil {
			if d.logger != nil {
				d.logger.Debug("agent plan quota probe failed",
					"provider", entry.cli, "error", err)
			}
			// Record the failure as "nothing to say" so the next cycle retries
			// instead of serving a stale snapshot for this directory.
			snapshots[key] = nil
			continue
		}
		snapshots[key] = snapshot
	}

	d.planAgentMu.Lock()
	defer d.planAgentMu.Unlock()
	for _, entry := range d.planAgentQuota {
		snapshot := snapshots[dirKey{cli: entry.cli, dir: entry.dir}]
		if snapshot == nil {
			// Includes the failed probe: clearing means the previous cycle's
			// number is never re-published as if it were this cycle's.
			entry.snapshot = nil
			continue
		}
		clone := clonePlanLimitsSnapshot(snapshot)
		entry.snapshot = &clone
	}
}

// probeAccountDir reads one account directory's plan windows.
func probeAccountDir(ctx context.Context, probe agent.PlanQuotaProbe, cli, dir string) (*protocol.PlanLimitsSnapshot, error) {
	switch cli {
	case "claude":
		scoped := probe
		scoped.ClaudeConfigDir = dir
		return scoped.ProbeClaude(ctx)
	}
	return nil, nil
}

// agentPlanLimitsForRuntime returns the snapshots this heartbeat should carry
// for runtimeID, keyed by agent id, or nil when there are none.
//
// Only agents seen on that runtime are included: a heartbeat is a statement
// about one runtime, and an agent that moved away must not have its old seat's
// windows re-published under the new one.
func (d *Daemon) agentPlanLimitsForRuntime(runtimeID string) map[string]protocol.PlanLimitsSnapshot {
	d.planAgentMu.Lock()
	defer d.planAgentMu.Unlock()
	if len(d.planAgentQuota) == 0 {
		return nil
	}
	out := make(map[string]protocol.PlanLimitsSnapshot, len(d.planAgentQuota))
	for agentID, entry := range d.planAgentQuota {
		if entry.runtimeID != runtimeID || entry.snapshot == nil {
			continue
		}
		out[agentID] = clonePlanLimitsSnapshot(entry.snapshot)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// agentPlanQuotaDirs snapshots the tracked agent -> directory map, for tests
// and for logging.
func (d *Daemon) agentPlanQuotaDirs() map[string]string {
	d.planAgentMu.Lock()
	defer d.planAgentMu.Unlock()
	out := make(map[string]string, len(d.planAgentQuota))
	for agentID, entry := range d.planAgentQuota {
		out[agentID] = entry.dir
	}
	return out
}
