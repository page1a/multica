package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// TestAgentBoundAccountDir pins the "same source as the task environment" rule
// (DENE-715): the directory the plan-quota probe reads is the one the child
// process is actually launched with, and only when the AGENT itself chose it.
// An inherited daemon default is not a binding, so it must not be tracked —
// otherwise every agent on the machine would report the daemon's own seat.
func TestAgentBoundAccountDir(t *testing.T) {
	home := "/hosts/kk"
	cases := []struct {
		name      string
		cli       string
		customEnv map[string]string
		taskEnv   map[string]string
		want      string
	}{
		{
			name:      "an explicit binding is read back out of the task environment",
			cli:       "claude",
			customEnv: map[string]string{"CLAUDE_CONFIG_DIR": "~/.claude-account2"},
			taskEnv:   map[string]string{"CLAUDE_CONFIG_DIR": "/hosts/kk/.claude-account2"},
			want:      "/hosts/kk/.claude-account2",
		},
		{
			name:      "a ~ binding the task environment left verbatim is expanded against the host home",
			cli:       "claude",
			customEnv: map[string]string{"CLAUDE_CONFIG_DIR": "~/.claude-account3"},
			taskEnv:   map[string]string{"CLAUDE_CONFIG_DIR": "~/.claude-account3"},
			want:      filepath.Join(home, ".claude-account3"),
		},
		{
			name:    "a daemon default inherited by an unbound agent is not a binding",
			cli:     "claude",
			taskEnv: map[string]string{"CLAUDE_CONFIG_DIR": "/hosts/kk/.claude-account9"},
			want:    "",
		},
		{
			name:      "a binding the task environment dropped records nothing",
			cli:       "claude",
			customEnv: map[string]string{"CLAUDE_CONFIG_DIR": "/hosts/kk/.claude-account2"},
			taskEnv:   map[string]string{},
			want:      "",
		},
		{
			name:      "a relative binding is refused rather than guessed",
			cli:       "claude",
			customEnv: map[string]string{"CLAUDE_CONFIG_DIR": "relative/dir"},
			taskEnv:   map[string]string{"CLAUDE_CONFIG_DIR": "relative/dir"},
			want:      "",
		},
		{
			name:      "another CLI's lever does not bind Claude",
			cli:       "claude",
			customEnv: map[string]string{"DSH_HOME": "/hosts/kk/.dsh-account2"},
			taskEnv:   map[string]string{"DSH_HOME": "/hosts/kk/.dsh-account2"},
			want:      "",
		},
		{
			// Codex cannot be rebound: the task environment rewrites CODEX_HOME,
			// so tracking a directory for it would report a switch that does not
			// exist. It never reaches here through recordAgentAccountBinding, and
			// it must not answer here either.
			name:      "a CLI with no lever binds nothing",
			cli:       "codex",
			customEnv: map[string]string{"CODEX_HOME": "/hosts/kk/.codex-account2"},
			taskEnv:   map[string]string{"CODEX_HOME": "/hosts/kk/.codex-account2"},
			want:      "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := agentBoundAccountDir(tc.cli, tc.customEnv, tc.taskEnv, home)
			if got != tc.want {
				t.Fatalf("agentBoundAccountDir() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRecordAgentAccountBindingFollowsTaskEnvironment covers the daemon's
// bookkeeping: an agent that binds an account is remembered, and one that drops
// its binding forgets the entry instead of keeping a stale seat.
func TestRecordAgentAccountBindingFollowsTaskEnvironment(t *testing.T) {
	home := t.TempDir()
	pinAccountQuotaHost(t, home)
	d := &Daemon{}
	account2 := filepath.Join(home, ".claude-account2")

	d.recordAgentAccountBinding("rt-1", "agent-1", "claude",
		map[string]string{"CLAUDE_CONFIG_DIR": "~/.claude-account2"},
		map[string]string{"CLAUDE_CONFIG_DIR": account2})
	if got := d.agentPlanQuotaDirs(); got["agent-1"] != account2 {
		t.Fatalf("tracked dirs = %#v, want agent-1 -> %q", got, account2)
	}

	// An agent the daemon has never seen bound is not tracked at all, so its
	// panel keeps reading the runtime row.
	d.recordAgentAccountBinding("rt-1", "agent-2", "claude", map[string]string{}, map[string]string{})
	if got := d.agentPlanQuotaDirs(); len(got) != 1 {
		t.Fatalf("tracked dirs = %#v, want only the bound agent", got)
	}

	// Same agent, binding removed: it stays tracked, now on the runtime's own
	// account (the empty directory). Forgetting the entry would freeze the
	// numbered account's last numbers on its panel forever.
	d.recordAgentAccountBinding("rt-1", "agent-1", "claude", map[string]string{}, map[string]string{})
	got := d.agentPlanQuotaDirs()
	dir, tracked := got["agent-1"]
	if !tracked || dir != "" {
		t.Fatalf("tracked dirs = %#v, want agent-1 tracked on the runtime account", got)
	}
	if d.planAgentQuota["agent-1"].snapshot != nil {
		t.Fatal("the dropped seat's snapshot must not be carried over to the runtime account")
	}
}

// writeClaudeProbeCredentials seeds <dir>/.credentials.json with token, which
// is the file pkg/agent reads for a Claude account.
func writeClaudeProbeCredentials(t *testing.T, dir, token string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"claudeAiOauth":{"accessToken":"` + token + `","expiresAt":1999999999}}`
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRefreshAgentPlanQuotaGroupsByDirectory probes each bound account once and
// publishes the result per agent on that agent's own runtime (DENE-715). Two
// agents sharing a seat cost one request; an agent on another runtime is not
// handed this runtime's windows.
func TestRefreshAgentPlanQuotaGroupsByDirectory(t *testing.T) {
	home := t.TempDir()
	pinAccountQuotaHost(t, home)
	account2 := filepath.Join(home, ".claude-account2")
	account3 := filepath.Join(home, ".claude-account3")
	writeClaudeProbeCredentials(t, account2, "account2-token")
	writeClaudeProbeCredentials(t, account3, "account3-token")

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		utilization := 10
		if r.Header.Get("Authorization") == "Bearer account3-token" {
			utilization = 90
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"five_hour": map[string]any{"utilization": utilization, "resets_at": "2027-01-15T04:00:00Z"},
		})
	}))
	t.Cleanup(server.Close)

	probe := agent.PlanQuotaProbe{
		Home:           home,
		Client:         server.Client(),
		ClaudeUsageURL: server.URL,
		LookupKeychain: func(string) (string, bool) { return "", false },
		Now:            func() time.Time { return time.Unix(100, 0) },
	}

	d := &Daemon{}
	d.planAgentQuota = map[string]*agentPlanQuota{
		"agent-1": {runtimeID: "rt-1", cli: "claude", dir: account2},
		"agent-2": {runtimeID: "rt-1", cli: "claude", dir: account2},
		"agent-3": {runtimeID: "rt-1", cli: "claude", dir: account3},
		"agent-4": {runtimeID: "rt-2", cli: "claude", dir: account2},
	}

	d.refreshAgentPlanQuota(context.Background(), probe)

	if requests != 2 {
		t.Fatalf("usage requests = %d, want one per distinct account directory", requests)
	}

	got := d.agentPlanLimitsForRuntime("rt-1")
	if len(got) != 3 {
		t.Fatalf("rt-1 snapshots = %#v, want agent-1..3", got)
	}
	if percent := firstWindowPercent(t, got["agent-1"]); percent != 10 {
		t.Fatalf("agent-1 percent = %v, want the account2 seat (10)", percent)
	}
	if percent := firstWindowPercent(t, got["agent-3"]); percent != 90 {
		t.Fatalf("agent-3 percent = %v, want the account3 seat (90)", percent)
	}
	if other := d.agentPlanLimitsForRuntime("rt-2"); len(other) != 1 {
		t.Fatalf("rt-2 snapshots = %#v, want only agent-4", other)
	}
	if other := d.agentPlanLimitsForRuntime("rt-missing"); other != nil {
		t.Fatalf("unknown runtime snapshots = %#v, want nil", other)
	}
}

// TestRefreshAgentPlanQuotaFollowsRuntimeAccountAfterUnbind covers the
// transition the panel must survive: once the agent's binding is gone it is
// probed against the runtime's own account, so its row agrees with the runtime
// row instead of freezing the numbered seat's last observation.
func TestRefreshAgentPlanQuotaFollowsRuntimeAccountAfterUnbind(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	pinAccountQuotaHost(t, home)
	account2 := filepath.Join(home, ".claude-account2")
	writeClaudeProbeCredentials(t, account2, "account2-token")
	writeClaudeProbeCredentials(t, filepath.Join(home, ".claude"), "default-token")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		utilization := 5
		if r.Header.Get("Authorization") == "Bearer account2-token" {
			utilization = 20
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"five_hour": map[string]any{"utilization": utilization, "resets_at": "2027-01-15T04:00:00Z"},
		})
	}))
	t.Cleanup(server.Close)

	probe := agent.PlanQuotaProbe{
		Home:           home,
		Client:         server.Client(),
		ClaudeUsageURL: server.URL,
		LookupKeychain: func(string) (string, bool) { return "", false },
		Now:            func() time.Time { return time.Unix(100, 0) },
	}

	d := &Daemon{}
	d.recordAgentAccountBinding("rt-1", "agent-1", "claude",
		map[string]string{"CLAUDE_CONFIG_DIR": "~/.claude-account2"},
		map[string]string{"CLAUDE_CONFIG_DIR": account2})
	d.refreshAgentPlanQuota(context.Background(), probe)
	if percent := firstWindowPercent(t, d.agentPlanLimitsForRuntime("rt-1")["agent-1"]); percent != 20 {
		t.Fatalf("bound percent = %v, want the account2 seat (20)", percent)
	}

	d.recordAgentAccountBinding("rt-1", "agent-1", "claude", map[string]string{}, map[string]string{})
	d.refreshAgentPlanQuota(context.Background(), probe)
	if percent := firstWindowPercent(t, d.agentPlanLimitsForRuntime("rt-1")["agent-1"]); percent != 5 {
		t.Fatalf("unbound percent = %v, want the runtime's own account (5)", percent)
	}
}

// TestRefreshAgentPlanQuotaClearsUnreadableSeat covers the "directory exists
// but holds no credentials" case end to end: the seat publishes nothing, and a
// previous cycle's number is never re-served as if it were this cycle's.
func TestRefreshAgentPlanQuotaClearsUnreadableSeat(t *testing.T) {
	home := t.TempDir()
	pinAccountQuotaHost(t, home)
	present := filepath.Join(home, ".claude-account2")
	missing := filepath.Join(home, ".claude-account9")
	writeClaudeProbeCredentials(t, present, "account2-token")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"five_hour": map[string]any{"utilization": 10, "resets_at": "2027-01-15T04:00:00Z"},
		})
	}))
	t.Cleanup(server.Close)

	probe := agent.PlanQuotaProbe{
		Home:           home,
		Client:         server.Client(),
		ClaudeUsageURL: server.URL,
		LookupKeychain: func(string) (string, bool) { return "", false },
		Now:            func() time.Time { return time.Unix(100, 0) },
	}

	d := &Daemon{}
	d.planAgentQuota = map[string]*agentPlanQuota{
		"agent-1": {runtimeID: "rt-1", cli: "claude", dir: present},
		"agent-2": {runtimeID: "rt-1", cli: "claude", dir: missing},
	}

	d.refreshAgentPlanQuota(context.Background(), probe)

	got := d.agentPlanLimitsForRuntime("rt-1")
	if _, ok := got["agent-1"]; !ok {
		t.Fatalf("rt-1 snapshots = %#v, want agent-1", got)
	}
	if _, ok := got["agent-2"]; ok {
		t.Fatalf("rt-1 snapshots = %#v, want agent-2 absent for an unseeded seat", got)
	}
}

func firstWindowPercent(t *testing.T, snapshot protocol.PlanLimitsSnapshot) float64 {
	t.Helper()
	if len(snapshot.Windows) == 0 || snapshot.Windows[0].UsedPercent == nil {
		t.Fatalf("snapshot has no percent window: %#v", snapshot)
	}
	return *snapshot.Windows[0].UsedPercent
}
