package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"
)

// newAgentCLIGateDaemon has a codex CLI one release behind and a claude CLI
// already current, with one runtime each, so only codex wants an upgrade.
func newAgentCLIGateDaemon(t *testing.T) (*Daemon, func() agentCLIStatus, *[][]string) {
	t.Helper()
	var mu sync.Mutex
	codexStatus := []agentCLIStatus{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var envelope struct {
			CLIUpdate agentCLIStatus `json:"cli_update"`
		}
		_ = json.Unmarshal(raw, &envelope)
		if r.URL.Path == "/api/daemon/runtimes/rt-codex/agent-cli/status" {
			mu.Lock()
			codexStatus = append(codexStatus, envelope.CLIUpdate)
			mu.Unlock()
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	codexBin := "/home/k/.codex/packages/standalone/bin/codex"
	agents := map[string]AgentEntry{
		"codex":  {Path: codexBin, Command: "codex"},
		"claude": {Path: "/usr/local/bin/claude", Command: "claude"},
	}
	d := &Daemon{
		cfg:    Config{Agents: agents},
		client: NewClient(srv.URL),
		logger: slog.Default(),
		runtimeIndex: map[string]Runtime{
			"rt-codex":   {ID: "rt-codex", Provider: "codex"},
			"rt-codex-p": {ID: "rt-codex-p", Provider: "codex", ProfileID: "prof-1"},
			"rt-claude":  {ID: "rt-claude", Provider: "claude"},
		},
		agentVersions:      map[string]string{"codex": "1.0.0", "claude": "1.2.0"},
		agentCLIFollow:     map[string]bool{"codex": false},
		agentCLIUpdateKick: make(chan struct{}, 1),
		claimGateWakeup:    make(chan struct{}, 1),
		agentCLIFetch: func(context.Context, string) ([]byte, error) {
			return []byte(`{"version":"1.2.0"}`), nil
		},
	}
	d.agentsAvailable.Store(&agents)
	d.agentCLIFollowLoaded = true
	var ran [][]string
	d.agentCLIRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		mu.Lock()
		ran = append(ran, append([]string{name}, args...))
		mu.Unlock()
		return []byte("ok"), nil
	}
	d.agentCLIAfterUpgrade = func(_ context.Context, provider string) {
		d.versionsMu.Lock()
		d.agentVersions[provider] = "1.2.0"
		d.versionsMu.Unlock()
	}
	last := func() agentCLIStatus {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if len(codexStatus) == 0 {
			t.Fatal("no codex status report")
		}
		return codexStatus[len(codexStatus)-1]
	}
	return d, last, &ran
}

// dispatch mimics the batch poller: claim, count the task, release the claim.
func dispatchForTest(t *testing.T, d *Daemon, runtimeID string) string {
	t.Helper()
	claim, ok := d.tryEnterClaimFor([]string{runtimeID})
	if !ok || !slices.Contains(claim.runtimeIDs, runtimeID) {
		t.Fatalf("could not claim for %s", runtimeID)
	}
	d.activeTasks.Add(1)
	provider := d.beginProviderTask(runtimeID)
	d.exitClaimFor(claim)
	return provider
}

func claimable(t *testing.T, d *Daemon) []string {
	t.Helper()
	claim, ok := d.tryEnterClaimFor([]string{"rt-codex", "rt-codex-p", "rt-claude"})
	if !ok {
		t.Fatal("claims are paused for the whole machine")
	}
	d.exitClaimFor(claim)
	return claim.runtimeIDs
}

func requestCodexUpdate(d *Daemon) {
	d.handleAgentCLICommand("rt-codex", &PendingAgentCLI{UpdateNow: true, RequestID: "req-1"})
	select {
	case <-d.agentCLIUpdateKick:
	default:
	}
}

func TestAgentCLIUpdateNowHoldsOnlyItsProvider(t *testing.T) {
	d, last, ran := newAgentCLIGateDaemon(t)
	codexTask := dispatchForTest(t, d, "rt-codex")
	claudeTask := dispatchForTest(t, d, "rt-claude")

	requestCodexUpdate(d)
	d.reconcileAgentCLIs(context.Background())

	if len(*ran) != 0 {
		t.Fatalf("upgraded while a codex task was running: %v", *ran)
	}
	st := last()
	if st.Phase != agentCLIPhaseWaiting || st.WaitingTasks != 1 || !st.ClaimsPaused || st.WaitReason != agentCLIWaitTasks || st.Error != "" {
		t.Fatalf("status = %#v", st)
	}
	// Claude keeps taking tasks; codex, profile runtimes included, does not.
	if got := claimable(t, d); !slices.Equal(got, []string{"rt-claude"}) {
		t.Fatalf("claimable = %v, want only rt-claude", got)
	}
	// A running claude task does not hold up the codex upgrade.
	d.finishActiveTask(codexTask)
	select {
	case <-d.agentCLIUpdateKick:
	default:
		t.Fatal("last codex task did not wake the updater")
	}
	d.reconcileAgentCLIs(context.Background())

	if len(*ran) != 1 || (*ran)[0][1] != "update" {
		t.Fatalf("ran = %v, want one codex update", *ran)
	}
	if st := last(); st.Phase != agentCLIPhaseCurrent || st.UpdatedAt == "" || st.CurrentVersion != "1.2.0" {
		t.Fatalf("status = %#v", st)
	}
	if got := claimable(t, d); len(got) != 3 {
		t.Fatalf("claimable after upgrade = %v, want all runtimes back", got)
	}
	select {
	case <-d.claimGateWakeup:
	default:
		t.Fatal("releasing codex did not nudge the poller")
	}
	d.finishActiveTask(claudeTask)
	if d.activeTasks.Load() != 0 {
		t.Fatalf("activeTasks = %d", d.activeTasks.Load())
	}
}

func TestAgentCLIUpdateNowFailureReleasesProvider(t *testing.T) {
	d, last, _ := newAgentCLIGateDaemon(t)
	d.agentCLIRun = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("boom"), errString("exit status 1")
	}
	requestCodexUpdate(d)
	d.reconcileAgentCLIs(context.Background())

	if st := last(); st.Phase != agentCLIPhaseFailed || st.Error == "" {
		t.Fatalf("status = %#v", st)
	}
	if got := claimable(t, d); len(got) != 3 {
		t.Fatalf("claimable after failure = %v", got)
	}
}

func TestAgentCLIUpdateNowHoldExpires(t *testing.T) {
	orig := agentCLIHoldLimit
	agentCLIHoldLimit = time.Minute
	t.Cleanup(func() { agentCLIHoldLimit = orig })

	d, last, ran := newAgentCLIGateDaemon(t)
	dispatchForTest(t, d, "rt-codex")
	requestCodexUpdate(d)
	d.reconcileAgentCLIs(context.Background())
	if got := claimable(t, d); slices.Contains(got, "rt-codex") {
		t.Fatalf("codex claimable during hold: %v", got)
	}

	// Pretend the hold started past the limit.
	d.claimMu.Lock()
	h := d.cliGate.held["codex"]
	h.since = time.Now().Add(-2 * time.Minute)
	d.cliGate.held["codex"] = h
	d.claimMu.Unlock()
	if !d.agentCLIHoldOverdue(time.Now()) {
		t.Fatal("hold not reported overdue")
	}
	d.reconcileAgentCLIs(context.Background())

	if len(*ran) != 0 {
		t.Fatal("upgraded while the codex task was still running")
	}
	st := last()
	if st.Phase != agentCLIPhaseWaiting || st.ClaimsPaused || st.WaitReason != agentCLIWaitHoldExpired || st.WaitingTasks != 1 {
		t.Fatalf("status = %#v", st)
	}
	if got := claimable(t, d); !slices.Contains(got, "rt-codex") {
		t.Fatalf("codex still held after the limit: %v", got)
	}
	// Later reconciles do not take the hold back for the same click.
	d.reconcileAgentCLIs(context.Background())
	if got := claimable(t, d); !slices.Contains(got, "rt-codex") {
		t.Fatalf("expired click re-held codex: %v", got)
	}
}

func TestAgentCLIFollowWaitsWithoutHolding(t *testing.T) {
	d, last, ran := newAgentCLIGateDaemon(t)
	d.agentCLIFollow = map[string]bool{"codex": true}
	codexTask := dispatchForTest(t, d, "rt-codex")
	dispatchForTest(t, d, "rt-claude")

	d.reconcileAgentCLIs(context.Background())
	if len(*ran) != 0 {
		t.Fatal("follow upgraded under a running codex task")
	}
	if st := last(); st.Phase != agentCLIPhaseWaiting || st.ClaimsPaused || st.WaitingTasks != 1 {
		t.Fatalf("status = %#v", st)
	}
	if got := claimable(t, d); len(got) != 3 {
		t.Fatalf("follow held claims: %v", got)
	}

	// Only codex needs to be idle, not the whole machine.
	d.finishActiveTask(codexTask)
	d.reconcileAgentCLIs(context.Background())
	if len(*ran) != 1 {
		t.Fatalf("ran = %v, want the codex upgrade while claude still runs", *ran)
	}
}

func TestAgentCLIUpgradeWaitsForAnInFlightClaim(t *testing.T) {
	d, _, _ := newAgentCLIGateDaemon(t)
	claim, ok := d.tryEnterClaimFor([]string{"rt-codex"})
	if !ok {
		t.Fatal("claim refused")
	}
	if ok, _, _ := d.tryBeginAgentCLIUpgrade("codex", time.Now()); ok {
		t.Fatal("upgrade began while a codex claim was in flight")
	}
	if ok, _, _ := d.tryBeginAgentCLIUpgrade("claude", time.Now()); !ok {
		t.Fatal("a codex claim blocked the claude upgrade")
	}
	d.endAgentCLIUpgrade("claude")
	d.exitClaimFor(claim)
	if ok, _, _ := d.tryBeginAgentCLIUpgrade("codex", time.Now()); !ok {
		t.Fatal("upgrade still refused after the claim finished")
	}
	d.endAgentCLIUpgrade("codex")
}

func TestAgentCLIUpgradeAndSelfUpdateExcludeEachOther(t *testing.T) {
	d, _, _ := newAgentCLIGateDaemon(t)
	if ok, _, _ := d.tryBeginAgentCLIUpgrade("codex", time.Now()); !ok {
		t.Fatal("idle upgrade refused")
	}
	if d.trySetClaimBarrier() {
		t.Fatal("self-update barrier taken during a CLI upgrade")
	}
	if got := d.tryBeginServerUpdate(context.Background()); got != serverUpdateRuntimeBusy {
		t.Fatalf("server update = %v, want busy", got)
	}
	d.endAgentCLIUpgrade("codex")

	if !d.trySetClaimBarrier() {
		t.Fatal("self-update barrier refused on an idle machine")
	}
	if ok, running, self := d.tryBeginAgentCLIUpgrade("codex", time.Now()); ok || running != 0 || !self {
		t.Fatalf("CLI upgrade began under the self-update barrier (%v/%d/%v)", ok, running, self)
	}
	d.releaseClaimBarrier()
}

// An "update now" that lands while a claim is in flight, and that claim
// brings back no codex task, must start the upgrade as soon as the claim
// ends instead of holding codex until the next periodic check.
func TestAgentCLIEmptyClaimExitStartsWaitingUpgrade(t *testing.T) {
	d, last, ran := newAgentCLIGateDaemon(t)
	claim, ok := d.tryEnterClaimFor([]string{"rt-codex", "rt-claude"})
	if !ok {
		t.Fatal("claim refused")
	}

	requestCodexUpdate(d)
	d.reconcileAgentCLIs(context.Background())
	if len(*ran) != 0 {
		t.Fatalf("upgraded while a codex claim was in flight: %v", *ran)
	}
	st := last()
	if st.Phase != agentCLIPhaseWaiting || st.WaitReason == agentCLIWaitDaemonUpdate || !st.ClaimsPaused || st.Error != "" {
		t.Fatalf("status = %#v", st)
	}
	if got := claimable(t, d); !slices.Equal(got, []string{"rt-claude"}) {
		t.Fatalf("claimable = %v, want only rt-claude until the upgrade", got)
	}

	d.exitClaimFor(claim)
	select {
	case <-d.agentCLIUpdateKick:
	default:
		t.Fatal("empty claim exit did not wake the updater")
	}
	d.reconcileAgentCLIs(context.Background())
	if len(*ran) != 1 {
		t.Fatalf("ran = %v, want the codex upgrade", *ran)
	}
	if got := claimable(t, d); len(got) != 3 {
		t.Fatalf("claimable after upgrade = %v", got)
	}
}
