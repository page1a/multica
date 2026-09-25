package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestReconcileAgentCLIUpgradesWhenIdle(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "lib", "node_modules", "@anthropic-ai", "claude-code", "bin", "claude")
	d, bodies := newAgentCLITestDaemon(t, bin, "1.0.0")

	var mu sync.Mutex
	var ran [][]string
	d.agentCLIRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		mu.Lock()
		ran = append(ran, append([]string{name}, args...))
		mu.Unlock()
		return []byte("updated"), nil
	}
	d.agentCLIAfterUpgrade = func(_ context.Context, provider string) {
		d.versionsMu.Lock()
		d.agentVersions[provider] = "1.2.0"
		d.versionsMu.Unlock()
	}

	d.reconcileAgentCLIs(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if len(ran) != 1 || ran[0][0] != bin || ran[0][1] != "update" {
		t.Fatalf("ran = %#v, want native update of %s", ran, bin)
	}
	var status agentCLIStatus
	if err := json.Unmarshal(lastCLIUpdate(t, bodies), &status); err != nil {
		t.Fatal(err)
	}
	if status.Phase != agentCLIPhaseCurrent || status.CurrentVersion != "1.2.0" {
		t.Fatalf("status = %#v", status)
	}
	if status.BinaryPath != bin {
		t.Fatalf("binary = %q", status.BinaryPath)
	}
}

func TestReconcileAgentCLIWaitsWhileATaskIsRunning(t *testing.T) {
	bin := "/usr/local/bin/claude"
	d, bodies := newAgentCLITestDaemon(t, bin, "1.0.0")
	d.activeTasks.Store(1)
	called := false
	d.agentCLIRun = func(context.Context, string, ...string) ([]byte, error) {
		called = true
		return nil, nil
	}

	d.reconcileAgentCLIs(context.Background())

	if called {
		t.Fatal("upgraded while a task was running")
	}
	var status agentCLIStatus
	if err := json.Unmarshal(lastCLIUpdate(t, bodies), &status); err != nil {
		t.Fatal(err)
	}
	if status.Phase != agentCLIPhaseWaiting {
		t.Fatalf("phase = %q, want waiting", status.Phase)
	}
	if d.updating.Load() {
		t.Fatal("left the update barrier held")
	}
}

func TestReconcileAgentCLIFollowOffDoesNotUpgrade(t *testing.T) {
	bin := "/usr/local/bin/claude"
	d, bodies := newAgentCLITestDaemon(t, bin, "1.0.0")
	d.agentCLIFollowFile = filepath.Join(t.TempDir(), "follow.json")
	d.agentCLIMu.Lock()
	d.agentCLIFollowLoaded = true
	d.agentCLIFollow = map[string]bool{"claude": false}
	d.agentCLIMu.Unlock()
	called := false
	d.agentCLIRun = func(context.Context, string, ...string) ([]byte, error) {
		called = true
		return nil, nil
	}

	d.reconcileAgentCLIs(context.Background())

	if called {
		t.Fatal("upgraded with auto-follow off")
	}
	var status agentCLIStatus
	if err := json.Unmarshal(lastCLIUpdate(t, bodies), &status); err != nil {
		t.Fatal(err)
	}
	if status.Phase != agentCLIPhaseAvailable || status.AutoFollow {
		t.Fatalf("status = %#v", status)
	}
}

func TestReconcileAgentCLIManualUpdateWhileFollowOff(t *testing.T) {
	bin := "/usr/local/bin/claude"
	d, bodies := newAgentCLITestDaemon(t, bin, "1.0.0")
	d.agentCLIMu.Lock()
	d.agentCLIFollowLoaded = true
	d.agentCLIFollow = map[string]bool{"claude": false}
	d.agentCLIManual = agentCLIManual{RuntimeID: "rt-claude", Provider: "claude", RequestID: "req-1"}
	d.agentCLIMu.Unlock()
	d.agentCLIRun = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("ok"), nil
	}
	d.agentCLIAfterUpgrade = func(_ context.Context, provider string) {
		d.versionsMu.Lock()
		d.agentVersions[provider] = "1.2.0"
		d.versionsMu.Unlock()
	}

	d.reconcileAgentCLIs(context.Background())

	raw := lastStatusBody(t, bodies)
	var envelope struct {
		CLIUpdate        agentCLIStatus `json:"cli_update"`
		AppliedRequestID string         `json:"applied_request_id"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.AppliedRequestID != "req-1" || envelope.CLIUpdate.Phase != agentCLIPhaseCurrent {
		t.Fatalf("envelope = %#v", envelope)
	}
}

func TestReconcileAgentCLIReportsCheckFailure(t *testing.T) {
	bin := "/usr/local/bin/claude"
	d, bodies := newAgentCLITestDaemon(t, bin, "1.0.0")
	d.agentCLIFetch = func(context.Context, string) ([]byte, error) {
		return nil, errString("Get \"https://registry.npmjs.org\": dial tcp: connection refused")
	}
	called := false
	d.agentCLIRun = func(context.Context, string, ...string) ([]byte, error) {
		called = true
		return nil, nil
	}

	d.reconcileAgentCLIs(context.Background())

	if called {
		t.Fatal("ran an upgrade after the version check failed")
	}
	var status agentCLIStatus
	if err := json.Unmarshal(lastCLIUpdate(t, bodies), &status); err != nil {
		t.Fatal(err)
	}
	if status.Phase != agentCLIPhaseCheckFailed || status.Error == "" {
		t.Fatalf("status = %#v", status)
	}
}

func TestAgentCLIFollowFromTwoWorkspacesAppliesOnce(t *testing.T) {
	bin := "/usr/local/bin/claude"
	d, bodies := newAgentCLITestDaemon(t, bin, "1.2.0")
	d.runtimeIndex = map[string]Runtime{
		"rt-a": {ID: "rt-a", Provider: "claude"},
		"rt-b": {ID: "rt-b", Provider: "claude"},
	}
	d.agentCLIFollowFile = filepath.Join(t.TempDir(), "follow.json")

	off := false
	on := true
	d.handleAgentCLICommand("rt-a", &PendingAgentCLI{Follow: &off, FollowID: "fa"})
	d.handleAgentCLICommand("rt-b", &PendingAgentCLI{Follow: &on, FollowID: "fb"})
	settled, err := os.ReadFile(d.agentCLIFollowFile)
	if err != nil {
		t.Fatal(err)
	}
	// The same clicks come back on the next heartbeats. They must not flip
	// the shared switch or rewrite the preference file.
	d.handleAgentCLICommand("rt-a", &PendingAgentCLI{Follow: &off, FollowID: "fa"})
	d.handleAgentCLICommand("rt-b", &PendingAgentCLI{Follow: &on, FollowID: "fb"})
	retried, err := os.ReadFile(d.agentCLIFollowFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(settled) != string(retried) {
		t.Fatalf("retry rewrote follow preference:\n%s\n%s", settled, retried)
	}
	if !d.agentCLIWantsFollow("claude") {
		t.Fatal("the later workspace click did not stick")
	}

	d.reconcileAgentCLIs(context.Background())
	got := map[string]bool{}
	for _, raw := range *bodies {
		var envelope struct {
			AppliedFollowID string `json:"applied_follow_id"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.AppliedFollowID != "" {
			got[envelope.AppliedFollowID] = true
		}
	}
	if !got["fa"] || !got["fb"] {
		t.Fatalf("acks = %#v", got)
	}
}

func TestFinishActiveTaskKicksADeferredUpgrade(t *testing.T) {
	d := &Daemon{agentCLIUpdateKick: make(chan struct{}, 1)}
	d.activeTasks.Store(1)
	d.finishActiveTask()
	select {
	case <-d.agentCLIUpdateKick:
	default:
		t.Fatal("idle machine did not wake the updater")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func newAgentCLITestDaemon(t *testing.T, bin, version string) (*Daemon, *[][]byte) {
	t.Helper()
	var mu sync.Mutex
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, raw)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	agents := map[string]AgentEntry{"claude": {Path: bin, Command: "claude"}}
	d := &Daemon{
		cfg:            Config{Agents: agents},
		client:         NewClient(srv.URL),
		logger:         slog.Default(),
		runtimeIndex:   map[string]Runtime{"rt-claude": {ID: "rt-claude", Provider: "claude"}},
		agentVersions:  map[string]string{"claude": version},
		agentCLIFollow: map[string]bool{},
		agentCLIFetch: func(context.Context, string) ([]byte, error) {
			return []byte(`{"version":"1.2.0"}`), nil
		},
	}
	d.agentsAvailable.Store(&agents)
	d.agentCLIFollowLoaded = true
	return d, &bodies
}

func lastStatusBody(t *testing.T, bodies *[][]byte) []byte {
	t.Helper()
	if bodies == nil || len(*bodies) == 0 {
		t.Fatal("no status report")
	}
	return (*bodies)[len(*bodies)-1]
}

func lastCLIUpdate(t *testing.T, bodies *[][]byte) []byte {
	t.Helper()
	var envelope struct {
		CLIUpdate json.RawMessage `json:"cli_update"`
	}
	if err := json.Unmarshal(lastStatusBody(t, bodies), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.CLIUpdate
}
