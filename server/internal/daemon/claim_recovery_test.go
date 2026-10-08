package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// TestClaimTasksWSFirst_RecoveryRequestFollowsUncertainClaim pins the DENE-1611
// daemon half: after an uncertain claim the next claim asks the server to
// re-send undelivered dispatches and names what the daemon already holds; once
// a claim gets an answer the request goes back to plain claims.
func TestClaimTasksWSFirst_RecoveryRequestFollowsUncertainClaim(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/api/daemon/tasks/claim") {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			bodies = append(bodies, body)
			mu.Unlock()
		}
		w.Write([]byte(`{"tasks":[]}`))
	}))
	defer srv.Close()

	d := New(Config{ServerBaseURL: srv.URL, MaxConcurrentTasks: 4}, slog.New(slog.NewTextHandler(noopWriter{}, nil)))
	d.claimRecoveryPending.Store(false)
	d.holdTask("held-1")

	claim := func() map[string]any {
		t.Helper()
		if _, err := d.ClaimTasksWSFirst(context.Background(), "daemon-x", []string{"rt1"}, 2); err != nil {
			t.Fatalf("claim: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		return bodies[len(bodies)-1]
	}

	if got := claim(); got["recover_undelivered"] != nil {
		t.Fatalf("plain claim carried recovery: %v", got)
	}

	d.claimRecoveryPending.Store(true)
	got := claim()
	if got["recover_undelivered"] != true {
		t.Fatalf("claim after uncertain outcome lacks recover_undelivered: %v", got)
	}
	held, _ := got["held_task_ids"].([]any)
	if len(held) != 1 || held[0] != "held-1" {
		t.Fatalf("held_task_ids = %v, want [held-1]", got["held_task_ids"])
	}

	if got := claim(); got["recover_undelivered"] != nil {
		t.Fatalf("recovery stayed on after an answered claim: %v", got)
	}
}

// A failed HTTP claim (the request may have reached the server before the
// response was lost) arms recovery for the next claim.
func TestClaimTasksWSFirst_HTTPFailureArmsRecovery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()
	d := New(Config{ServerBaseURL: srv.URL, MaxConcurrentTasks: 4}, slog.New(slog.NewTextHandler(noopWriter{}, nil)))
	d.claimRecoveryPending.Store(false)

	if _, err := d.ClaimTasksWSFirst(context.Background(), "daemon-x", []string{"rt1"}, 2); err == nil {
		t.Fatal("expected claim error")
	}
	if !d.claimRecoveryPending.Load() {
		t.Fatal("failed claim did not arm recovery")
	}
}

// The response wait must cover transfer time on a slow link, not the 7s that
// lost multi-task claims in DENE-1611, while the post-uncertainty pause stays
// short so recovery is not delayed by it.
func TestClaimTimeoutsCoverSlowLinks(t *testing.T) {
	if got := batchClaimRequestTimeout + wsRPCResponseGrace; got < 30*time.Second {
		t.Fatalf("ws claim response wait = %v, want >= 30s", got)
	}
	if batchClaimResponseWait < 30*time.Second {
		t.Fatalf("http claim response wait = %v, want >= 30s", batchClaimResponseWait)
	}
	if wsClaimUncertainFallbackDelay > 10*time.Second {
		t.Fatalf("uncertain-claim pause = %v; recovery would wait on it", wsClaimUncertainFallbackDelay)
	}
}

// TestRunBatchPollerIgnoresResentHeldTask drives the whole poller: a recovery
// claim re-sends t1, which is still running, alongside a genuinely new t2. The
// held copy must be dropped (one run of t1), t2 must run in its own slot, and
// the request must have told the server t1 is held.
func TestRunBatchPollerIgnoresResentHeldTask(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var recoveryBodies []map[string]any
	var claimCalls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasSuffix(r.URL.Path, "/api/daemon/tasks/claim") {
			w.Write([]byte(`{}`))
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch {
		case claimCalls.Add(1) == 1:
			w.Write([]byte(`{"tasks":[{"id":"t1","runtime_id":"rt-1","issue_id":"i1","agent":{"name":"a"}}]}`))
		case body["recover_undelivered"] == true:
			mu.Lock()
			recoveryBodies = append(recoveryBodies, body)
			mu.Unlock()
			w.Write([]byte(`{"tasks":[
				{"id":"t1","runtime_id":"rt-1","issue_id":"i1","agent":{"name":"a"}},
				{"id":"t2","runtime_id":"rt-1","issue_id":"i2","agent":{"name":"a"}}
			]}`))
		default:
			w.Write([]byte(`{"tasks":[]}`))
		}
	}))
	defer srv.Close()

	d := New(Config{
		ServerBaseURL:      srv.URL,
		HeartbeatInterval:  time.Hour,
		PollInterval:       20 * time.Millisecond,
		MaxConcurrentTasks: 4,
	}, slog.New(slog.NewTextHandler(noopWriter{}, nil)))
	d.workspaces["ws-1"] = &workspaceState{workspaceID: "ws-1", runtimeIDs: []string{"rt-1"}}
	d.runtimeIndex["rt-1"] = Runtime{ID: "rt-1", Provider: "codex"}
	d.cancelPollInterval = time.Hour
	d.claimRecoveryPending.Store(false)

	var runs sync.Map // task id -> *atomic.Int64
	count := func(id string) *atomic.Int64 {
		v, _ := runs.LoadOrStore(id, new(atomic.Int64))
		return v.(*atomic.Int64)
	}
	releaseT1 := make(chan struct{})
	d.runner = taskRunnerFunc(func(ctx context.Context, task Task, provider string, slot int, log *slog.Logger) (TaskResult, error) {
		count(task.ID).Add(1)
		if task.ID == "t1" {
			<-releaseT1
		}
		return TaskResult{Status: "completed"}, nil
	})

	sem := newTaskSlotSemaphore(d.cfg.MaxConcurrentTasks)
	var taskWG sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.runBatchPoller(ctx, ctx, sem, make(chan struct{}, 1), &taskWG)

	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.After(3 * time.Second)
		for !cond() {
			select {
			case <-deadline:
				t.Fatalf("timed out waiting for %s", what)
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	waitFor("t1 to start", func() bool { return count("t1").Load() == 1 })

	// t1 is running; an earlier claim's outcome is now unknown.
	d.claimRecoveryPending.Store(true)
	waitFor("t2 to start", func() bool { return count("t2").Load() == 1 })
	// Give a duplicate t1 the chance to (wrongly) start.
	time.Sleep(100 * time.Millisecond)
	close(releaseT1)
	cancel()
	taskWG.Wait()

	if n := count("t1").Load(); n != 1 {
		t.Fatalf("t1 ran %d times, want exactly 1", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(recoveryBodies) == 0 {
		t.Fatal("recovery claim never sent")
	}
	held, _ := recoveryBodies[0]["held_task_ids"].([]any)
	if len(held) != 1 || held[0] != "t1" {
		t.Fatalf("held_task_ids = %v, want [t1]", recoveryBodies[0]["held_task_ids"])
	}
}

// An answer that says recovery is unfinished keeps it armed on both transports;
// an empty answer without that signal is not proof anything was recovered.
func TestClaimTasksWSFirst_RecoveryStaysArmedWhileServerSaysPending(t *testing.T) {
	var pending atomic.Bool
	pending.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if pending.Load() {
			w.Write([]byte(`{"tasks":[],"recovery_pending":true}`))
			return
		}
		w.Write([]byte(`{"tasks":[]}`))
	}))
	defer srv.Close()
	d := New(Config{ServerBaseURL: srv.URL, MaxConcurrentTasks: 4}, slog.New(slog.NewTextHandler(noopWriter{}, nil)))
	claim := func() {
		t.Helper()
		if _, err := d.ClaimTasksWSFirst(context.Background(), "daemon-x", []string{"rt1"}, 2); err != nil {
			t.Fatalf("claim: %v", err)
		}
	}

	// Startup starts armed; HTTP answer says unfinished.
	claim()
	if !d.claimRecoveryPending.Load() {
		t.Fatal("HTTP empty answer with recovery_pending cleared recovery")
	}

	// WS answer, same contract.
	generation := d.wsRPC.attach(func(frame []byte) (*wsOutbound, error) {
		var msg protocol.Message
		_ = json.Unmarshal(frame, &msg)
		var req protocol.RPCRequestPayload
		_ = json.Unmarshal(msg.Payload, &req)
		go d.wsRPC.deliver(protocol.RPCResponsePayload{RequestID: req.RequestID, Status: 200, Body: json.RawMessage(`{"tasks":[],"recovery_pending":true}`)})
		return &wsOutbound{data: frame}, nil
	})
	d.wsRPC.markRPCV1Supported(generation)
	claim()
	if !d.claimRecoveryPending.Load() {
		t.Fatal("WS empty answer with recovery_pending cleared recovery")
	}

	// Server finished: HTTP answer without the signal ends it.
	d.wsRPC.attach(nil)
	pending.Store(false)
	claim()
	if d.claimRecoveryPending.Load() {
		t.Fatal("recovery stayed armed after the server reported it finished")
	}
}

// TestClaimRecoverySurvivesPrematureEmptyRetry is the DENE-1611 review scenario
// end to end through the daemon: claim 1 commits server-side and its response is
// destroyed (connection closed); claim 2 retries at once, before the dispatch is
// old enough to take back, and gets an honest "nothing yet, still pending"; claim
// 3, after the safety window, still carries the recovery request and gets the
// original task. The stub server models the real contract's minimum age.
func TestClaimRecoverySurvivesPrematureEmptyRetry(t *testing.T) {
	const minAge = 150 * time.Millisecond
	var mu sync.Mutex
	var dispatchedAt time.Time
	var recoverFlags []bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		recover := body["recover_undelivered"] == true
		mu.Lock()
		defer mu.Unlock()
		recoverFlags = append(recoverFlags, recover)
		switch {
		case dispatchedAt.IsZero():
			// Commit the dispatch, then lose the response.
			dispatchedAt = time.Now()
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
		case recover && time.Since(dispatchedAt) >= minAge:
			w.Write([]byte(`{"tasks":[{"id":"lost-1","runtime_id":"rt1","agent":{"name":"a"}}]}`))
		case recover:
			w.Write([]byte(`{"tasks":[],"recovery_pending":true}`))
		default:
			w.Write([]byte(`{"tasks":[]}`))
		}
	}))
	defer srv.Close()

	d := New(Config{ServerBaseURL: srv.URL, MaxConcurrentTasks: 4}, slog.New(slog.NewTextHandler(noopWriter{}, nil)))
	d.claimRecoveryPending.Store(false)
	claim := func() ([]*Task, error) {
		return d.ClaimTasksWSFirst(context.Background(), "daemon-x", []string{"rt1"}, 2)
	}

	if _, err := claim(); err == nil {
		t.Fatal("claim 1: expected the destroyed response to surface as an error")
	}
	tasks, err := claim()
	if err != nil || len(tasks) != 0 {
		t.Fatalf("claim 2 = %v, %v; want an empty answer", tasks, err)
	}
	if !d.claimRecoveryPending.Load() {
		t.Fatal("premature empty answer ended recovery; the lost task would wait for stale reclaim")
	}
	time.Sleep(minAge + 50*time.Millisecond)
	tasks, err = claim()
	if err != nil || len(tasks) != 1 || tasks[0].ID != "lost-1" {
		t.Fatalf("claim 3 = %v, %v; want the original task back", tasks, err)
	}
	if d.claimRecoveryPending.Load() {
		t.Fatal("recovery stayed armed after the task came back")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(recoverFlags) != 3 || recoverFlags[0] || !recoverFlags[1] || !recoverFlags[2] {
		t.Fatalf("recover flags per claim = %v, want [false true true]", recoverFlags)
	}
}

// While recovery is pending the poller must retry within seconds, not wait out
// the 30s poll or the multi-minute WS safety poll.
func TestTaskClaimPollIntervalIsShortWhileRecoveryPending(t *testing.T) {
	d := New(Config{PollInterval: 30 * time.Second, WSClaimPollInterval: 3 * time.Minute}, slog.Default())
	d.claimRecoveryPending.Store(false)
	if got := d.taskClaimPollInterval(claimTasksResult{}); got != 30*time.Second {
		t.Fatalf("interval without recovery = %v, want 30s", got)
	}
	d.claimRecoveryPending.Store(true)
	if got := d.taskClaimPollInterval(claimTasksResult{}); got != claimRecoveryRetryInterval {
		t.Fatalf("interval with recovery pending = %v, want %v", got, claimRecoveryRetryInterval)
	}
	if got := d.capForClaimRecovery(2 * time.Second); got != 2*time.Second {
		t.Fatalf("a shorter wait must not be stretched, got %v", got)
	}
}
