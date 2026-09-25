package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// mockRow implements pgx.Row, returning either a scanned task or pgx.ErrNoRows.
type mockRow struct {
	task *db.AgentTaskQueue
	err  error
}

func (r *mockRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	t := r.task
	ptrs := []any{
		&t.ID, &t.AgentID, &t.IssueID, &t.Status, &t.Priority,
		&t.DispatchedAt, &t.StartedAt, &t.CompletedAt, &t.Result,
		&t.Error, &t.CreatedAt, &t.Context, &t.RuntimeID,
		&t.SessionID, &t.WorkDir, &t.TriggerCommentID,
		&t.ChatSessionID, &t.AutopilotRunID,
	}
	for i, p := range ptrs {
		if i >= len(dest) {
			break
		}
		// Copy value from source to dest by assigning through the pointer.
		switch d := dest[i].(type) {
		case *pgtype.UUID:
			*d = *(p.(*pgtype.UUID))
		case *string:
			*d = *(p.(*string))
		case *int32:
			*d = *(p.(*int32))
		case *pgtype.Timestamptz:
			*d = *(p.(*pgtype.Timestamptz))
		case *[]byte:
			*d = *(p.(*[]byte))
		case *pgtype.Text:
			*d = *(p.(*pgtype.Text))
		}
	}
	return nil
}

// mockDBTX routes QueryRow calls: complete/fail queries return ErrNoRows,
// getAgentTask returns the stored task.
type mockDBTX struct {
	task db.AgentTaskQueue
}

func (m *mockDBTX) Exec(_ context.Context, _ string, _ ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag(""), nil
}

func (m *mockDBTX) Query(_ context.Context, _ string, _ ...interface{}) (pgx.Rows, error) {
	return nil, pgx.ErrNoRows
}

func (m *mockDBTX) QueryRow(_ context.Context, sql string, _ ...interface{}) pgx.Row {
	// CompleteAgentTask and FailAgentTask SQL contain "SET status ="
	if strings.Contains(sql, "SET status =") {
		return &mockRow{err: pgx.ErrNoRows}
	}
	// GetAgentTask — return the existing task
	return &mockRow{task: &m.task}
}

func testUUID(b byte) pgtype.UUID {
	var u pgtype.UUID
	u.Valid = true
	u.Bytes[0] = b
	return u
}

func TestCompleteTask_AlreadyFinalized(t *testing.T) {
	taskID := testUUID(1)
	agentID := testUUID(2)

	tests := []struct {
		name   string
		status string
	}{
		{"already completed", "completed"},
		{"already cancelled", "cancelled"},
		{"already failed", "failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockDBTX{task: db.AgentTaskQueue{
				ID:      taskID,
				AgentID: agentID,
				Status:  tt.status,
			}}
			svc := &TaskService{
				Queries: db.New(mock),
				Bus:     events.New(),
			}

			got, transitioned, err := svc.CompleteTaskWithTransition(context.Background(), taskID, nil, "", "", "", false, "", "")
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if transitioned {
				t.Fatal("already-finalized task reported a new completion transition")
			}
			if got == nil {
				t.Fatal("expected task, got nil")
			}
			if got.Status != tt.status {
				t.Errorf("expected status %q, got %q", tt.status, got.Status)
			}
			if got.ID != taskID {
				t.Error("returned task ID doesn't match")
			}
		})
	}
}

func TestFailTask_AlreadyFinalized(t *testing.T) {
	taskID := testUUID(1)
	agentID := testUUID(2)

	tests := []struct {
		name   string
		status string
	}{
		{"already completed", "completed"},
		{"already cancelled", "cancelled"},
		{"already failed", "failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockDBTX{task: db.AgentTaskQueue{
				ID:      taskID,
				AgentID: agentID,
				Status:  tt.status,
			}}
			svc := &TaskService{
				Queries: db.New(mock),
				Bus:     events.New(),
			}

			got, transitioned, err := svc.FailTaskWithTransition(context.Background(), taskID, "agent crashed", "", "", "", "", false, "", "")
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if transitioned {
				t.Fatal("already-finalized task reported a new failure transition")
			}
			if got == nil {
				t.Fatal("expected task, got nil")
			}
			if got.Status != tt.status {
				t.Errorf("expected status %q, got %q", tt.status, got.Status)
			}
			if got.ID != taskID {
				t.Error("returned task ID doesn't match")
			}
		})
	}
}

// TestProviderNetworkRetrySchedule locks in the three-tier schedule for a
// transient provider stream cut (MUL-4910): first run + immediate retry + one
// retry deferred ~5s. runtime_offline uses a separate deferred marker so its
// retry waits for a healthy runtime. Capacity has its own deferred schedule
// (TestProviderCapacityRetrySchedule); remaining retryable reasons keep their
// generic max_attempts=2 (single, immediate retry).
func TestProviderNetworkRetrySchedule(t *testing.T) {
	const provNet = "agent_error.provider_network"

	// Attempt ceiling: provider_network is raised to 3, but only ever WIDENS the
	// budget and never overrides the max_attempts<=1 "retry disabled" contract.
	ceilingCases := []struct {
		reason string
		max    int32
		want   int32
	}{
		{provNet, 2, providerNetworkMaxAttempts}, // default budget → raised to 3
		{provNet, 1, 1},                          // disabled → stays disabled, not revived
		{provNet, 5, 5},                          // higher configured budget → kept (widen-only)
		{"timeout", 2, 2},                        // unrelated reason → column value untouched
		{"timeout", 1, 1},                        // unrelated + disabled → untouched
	}
	for _, tc := range ceilingCases {
		if got := retryAttemptCeiling(tc.reason, tc.max); got != tc.want {
			t.Errorf("ceiling(%q, %d) = %d, want %d", tc.reason, tc.max, got, tc.want)
		}
	}

	// Backoff / deferral: runtime_offline always waits for health-gated
	// promotion; provider_network only defers its final tier.
	delayCases := []struct {
		reason        string
		failedAttempt int32
		want          time.Duration
	}{
		{provNet, 1, 0}, // first failure → immediate retry
		{provNet, 2, providerNetworkFinalRetryWait}, // second failure → 5s-deferred retry
		{"runtime_offline", 1, runtimeOfflineRetryDeferral},
		{"timeout", 2, 0}, // unrelated reason → never deferred
	}
	for _, tc := range delayCases {
		if got := retryDelayForAttempt(tc.reason, tc.failedAttempt); got != tc.want {
			t.Errorf("retryDelayForAttempt(%q, %d) = %s, want %s", tc.reason, tc.failedAttempt, got, tc.want)
		}
	}

	// Eligibility across the whole chain. mkTask has an issue link and no
	// autopilot run so only the reason/attempt/ceiling gate is exercised.
	mkTask := func(attempt, max int32) db.AgentTaskQueue {
		return db.AgentTaskQueue{
			Attempt:     attempt,
			MaxAttempts: max,
			IssueID:     pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		}
	}
	eligCases := []struct {
		name    string
		reason  string
		attempt int32
		max     int32
		want    bool
	}{
		{"provider_network first run retries", provNet, 1, 2, true},
		{"provider_network second run still retries (deferred tier)", provNet, 2, 2, true},
		{"provider_network third run is the ceiling", provNet, 3, 2, false},
		{"provider_network with retry disabled (max_attempts=1) never retries", provNet, 1, 1, false},
		{"timeout keeps single immediate retry", "timeout", 1, 2, true},
		{"timeout exhausts at attempt 2", "timeout", 2, 2, false},
		{"non-retryable reason never retries", "agent_error.unknown", 1, 2, false},
	}
	for _, tc := range eligCases {
		if got := retryEligible(tc.reason, mkTask(tc.attempt, tc.max), retryEnabledAgent()); got != tc.want {
			t.Errorf("%s: retryEligible(%q, attempt=%d/max=%d) = %v, want %v", tc.name, tc.reason, tc.attempt, tc.max, got, tc.want)
		}
	}
}

func retryEnabledAgent() db.Agent {
	return db.Agent{AutoRetryEnabled: true}
}

func retryDisabledAgent() db.Agent {
	return db.Agent{AutoRetryEnabled: false}
}

// TestProviderCapacityRetrySchedule locks in the backoff schedule for a
// provider capacity miss (DENE-210): first run + two deferred retries. Unlike
// provider_network, every retry waits — a limit that just fired will not
// clear on the next millisecond. max_attempts<=1 still disables retry.
func TestProviderCapacityRetrySchedule(t *testing.T) {
	const capReason = "agent_error.provider_capacity_or_rate_limit"

	ceilingCases := []struct {
		reason string
		max    int32
		want   int32
	}{
		{capReason, 2, providerCapacityMaxAttempts},
		{capReason, 1, 1},
		{capReason, 5, 5},
	}
	for _, tc := range ceilingCases {
		if got := retryAttemptCeiling(tc.reason, tc.max); got != tc.want {
			t.Errorf("ceiling(%q, %d) = %d, want %d", tc.reason, tc.max, got, tc.want)
		}
	}

	delayCases := []struct {
		failedAttempt int32
		want          time.Duration
	}{
		{1, providerCapacityRetryWait},
		{2, providerCapacityRetryWait},
	}
	for _, tc := range delayCases {
		if got := retryDelayForAttempt(capReason, tc.failedAttempt); got != tc.want {
			t.Errorf("retryDelayForAttempt(%q, %d) = %s, want %s", capReason, tc.failedAttempt, got, tc.want)
		}
		if got := retryDelayForAttempt(capReason, tc.failedAttempt); got <= 0 {
			t.Errorf("retryDelayForAttempt(%q, %d) must be a positive backoff, got %s", capReason, tc.failedAttempt, got)
		}
	}

	mkTask := func(attempt, max int32) db.AgentTaskQueue {
		return db.AgentTaskQueue{
			Attempt:     attempt,
			MaxAttempts: max,
			IssueID:     pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		}
	}
	eligCases := []struct {
		name    string
		attempt int32
		max     int32
		want    bool
	}{
		{"capacity first run retries", 1, 2, true},
		{"capacity second run still retries", 2, 2, true},
		{"capacity third run is the ceiling", 3, 2, false},
		{"capacity with retry disabled (max_attempts=1) never retries", 1, 1, false},
	}
	for _, tc := range eligCases {
		if got := retryEligible(capReason, mkTask(tc.attempt, tc.max), retryEnabledAgent()); got != tc.want {
			t.Errorf("%s: retryEligible(%q, attempt=%d/max=%d) = %v, want %v", tc.name, capReason, tc.attempt, tc.max, got, tc.want)
		}
	}
}

// TestProviderServerErrorRetrySchedule locks in DENE-596's schedule for the
// provider-fault bucket: first run + two deferred retries, same shape as the
// capacity schedule and for the same reason — an upstream that just answered
// 5xx, or just refused a credential it had accepted seconds earlier, is not
// fixed by resending in the same instant. max_attempts<=1 still disables retry.
//
// Before this, provider_server_error was off the allowlist entirely: the Grok
// 401 shape terminated the run on its first attempt and showed the member
// "sign in again" copy. Neither of those was true.
func TestProviderServerErrorRetrySchedule(t *testing.T) {
	const srvReason = "agent_error.provider_server_error"

	ceilingCases := []struct {
		reason string
		max    int32
		want   int32
	}{
		{srvReason, 2, providerServerErrorMaxAttempts},
		{srvReason, 1, 1}, // disabled → stays disabled, not revived
		{srvReason, 5, 5}, // higher configured budget → kept (widen-only)
	}
	for _, tc := range ceilingCases {
		if got := retryAttemptCeiling(tc.reason, tc.max); got != tc.want {
			t.Errorf("ceiling(%q, %d) = %d, want %d", tc.reason, tc.max, got, tc.want)
		}
	}

	for _, failedAttempt := range []int32{1, 2} {
		if got := retryDelayForAttempt(srvReason, failedAttempt); got != providerServerErrorRetryWait {
			t.Errorf("retryDelayForAttempt(%q, %d) = %s, want %s", srvReason, failedAttempt, got, providerServerErrorRetryWait)
		}
	}

	mkTask := func(attempt, max int32) db.AgentTaskQueue {
		return db.AgentTaskQueue{
			Attempt:     attempt,
			MaxAttempts: max,
			IssueID:     pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		}
	}
	eligCases := []struct {
		name    string
		attempt int32
		max     int32
		want    bool
	}{
		{"provider fault first run retries", 1, 2, true},
		{"provider fault second run still retries", 2, 2, true},
		{"provider fault third run is the ceiling", 3, 2, false},
		{"provider fault with retry disabled (max_attempts=1) never retries", 1, 1, false},
	}
	for _, tc := range eligCases {
		if got := retryEligible(srvReason, mkTask(tc.attempt, tc.max), retryEnabledAgent()); got != tc.want {
			t.Errorf("%s: retryEligible(%q, attempt=%d/max=%d) = %v, want %v", tc.name, srvReason, tc.attempt, tc.max, got, tc.want)
		}
	}
}

// TestAgentAutoRetrySwitchGatesRetryEligible is the DENE-217 unit gate: the
// per-agent switch sits inside retryEligible so FailTask and
// MaybeRetryFailedTask cannot disagree. Default-on (column DEFAULT TRUE)
// keeps the historical table-driven outcomes above; explicit false blocks
// every retryable reason, including capacity and provider_network.
func TestAgentAutoRetrySwitchGatesRetryEligible(t *testing.T) {
	mkTask := func() db.AgentTaskQueue {
		return db.AgentTaskQueue{
			Attempt:     1,
			MaxAttempts: 2,
			IssueID:     pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		}
	}
	reasons := []string{
		"agent_error.provider_network",
		"agent_error.provider_capacity_or_rate_limit",
		"agent_error.provider_server_error",
		"runtime_offline",
		"timeout",
		"codex_semantic_inactivity",
		"skill_bundle_unavailable",
		"cancelled",
	}
	for _, reason := range reasons {
		if !retryEligible(reason, mkTask(), retryEnabledAgent()) {
			t.Errorf("default-on agent must still retry %q", reason)
		}
		if retryEligible(reason, mkTask(), retryDisabledAgent()) {
			t.Errorf("auto_retry_enabled=false must not retry %q", reason)
		}
	}
}

func TestTaskFailureClassifiers(t *testing.T) {
	cases := []struct {
		reason       string
		wantType     string
		wantResumeOK bool
		wantRetry    bool
	}{
		{reason: "timeout", wantType: "timeout", wantResumeOK: true, wantRetry: true},
		{reason: "codex_semantic_inactivity", wantType: "timeout", wantResumeOK: false, wantRetry: true},
		// Transient mid-stream provider disconnect (MUL-4910): retryable, and
		// resume-safe so the retry continues the truncated conversation.
		{reason: "agent_error.provider_network", wantType: "agent_error", wantResumeOK: true, wantRetry: true},
		// Capacity/rate-limit is retryable with backoff (DENE-210): the
		// request is well-formed and the model exists, the provider just has
		// no spare compute. Resume stays safe so the retry continues the
		// session; the delay (not a fresh session) is what avoids colliding
		// with the same limit.
		{reason: "agent_error.provider_capacity_or_rate_limit", wantType: "agent_error", wantResumeOK: true, wantRetry: true},
		// Provider fault (DENE-596): a 5xx, or an upstream that refuses a
		// credential the runtime has just authenticated. Retryable for the
		// same reason provider_network is — the request was fine — and
		// resume-safe because nothing about the conversation was rejected.
		{reason: "agent_error.provider_server_error", wantType: "agent_error", wantResumeOK: true, wantRetry: true},
		// A provider quota / weekly usage limit is deterministic: do not spend
		// another run against the same exhausted account. The failure comment
		// tells the operator to switch the agent's account/seat instead.
		{reason: "agent_error.provider_quota_limit", wantType: "agent_error", wantResumeOK: true, wantRetry: false},
		{reason: "runtime_recovery", wantType: "runtime", wantResumeOK: true, wantRetry: true},
		// DENE-813: the daemon reports this when its process context dies
		// while the row is still running. Resume-safe so the retry keeps the
		// session and workdir. A person's cancel-task/halt does not use this
		// reason.
		{reason: "cancelled", wantType: "cancelled", wantResumeOK: true, wantRetry: true},
		// DENE-857: the workspace wall clock stopped a healthy run. Resume-safe
		// so the next attempt keeps the session, and retryable until the
		// attempt budget is spent.
		{reason: "task_time_limit", wantType: "timeout", wantResumeOK: true, wantRetry: true},
		{reason: "iteration_limit", wantType: "agent_output", wantResumeOK: false, wantRetry: false},
		{reason: "api_invalid_request", wantType: "agent_error", wantResumeOK: false, wantRetry: false},
		{reason: "agent_error.context_overflow", wantType: "agent_error", wantResumeOK: false, wantRetry: false},
		{reason: "agent_error", wantType: "agent_error", wantResumeOK: true, wantRetry: false},
		// Missing terminal result errors classify to agent_error.unknown. Keep
		// that deterministic upstream failure outside the auto-retry allowlist.
		{reason: "agent_error.unknown", wantType: "agent_error", wantResumeOK: true, wantRetry: false},
	}

	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			if got := taskErrorType(tc.reason); got != tc.wantType {
				t.Fatalf("taskErrorType(%q) = %q, want %q", tc.reason, got, tc.wantType)
			}
			if got := !resumeUnsafeFailureReason(tc.reason); got != tc.wantResumeOK {
				t.Fatalf("resume-safe(%q) = %v, want %v", tc.reason, got, tc.wantResumeOK)
			}
			if got := retryableReasons[tc.reason]; got != tc.wantRetry {
				t.Fatalf("retryableReasons[%q] = %v, want %v", tc.reason, got, tc.wantRetry)
			}
		})
	}
}

func TestProviderQuotaFailureIsExplicitlyNonRetryable(t *testing.T) {
	const raw = "Weekly usage limit reached"
	reason := taskfailure.Classify(raw)
	if reason != taskfailure.ReasonAgentProviderQuotaLimit {
		t.Fatalf("Classify(%q) = %q, want %q", raw, reason, taskfailure.ReasonAgentProviderQuotaLimit)
	}
	if got := taskfailure.NormalizeDaemonReason("agent_error", raw); got != taskfailure.ReasonAgentProviderQuotaLimit {
		t.Fatalf("NormalizeDaemonReason(agent_error, %q) = %q, want %q", raw, got, taskfailure.ReasonAgentProviderQuotaLimit)
	}
	if retryableReasons[reason.String()] {
		t.Fatalf("retryableReasons[%q] = true, want false", reason)
	}
	capacity := taskfailure.Classify("API Error: 429 Too Many Requests")
	if capacity != taskfailure.ReasonAgentProviderCapacityOrRateLimit || !retryableReasons[capacity.String()] {
		t.Fatalf("429 classification = %q, retryable = %v; want provider capacity/rate-limit and retryable", capacity, retryableReasons[capacity.String()])
	}
	comment := failureCommentBody(reason.String(), raw)
	for _, want := range []string{"quota exhausted", "no automatic retry", "another available account or seat"} {
		if !strings.Contains(strings.ToLower(comment), want) {
			t.Errorf("quota failure comment %q does not contain %q", comment, want)
		}
	}
}

// TestDSHStreamClosedRetriesViaExistingProviderNetworkBucket pins DENE-235:
// STREAM_CLOSED classifies as provider_network, and that bucket is already
// on retryableReasons. Do not add a new agent_error.* bucket — unknown stays
// off the allowlist, which is why the misclassification was terminal.
func TestDSHStreamClosedRetriesViaExistingProviderNetworkBucket(t *testing.T) {
	reason := taskfailure.Classify("STREAM_CLOSED: SSE stream ended without [DONE]")
	if reason != taskfailure.ReasonAgentProviderNetwork {
		t.Fatalf("Classify(STREAM_CLOSED) = %q, want %q", reason, taskfailure.ReasonAgentProviderNetwork)
	}
	if !retryableReasons[string(reason)] {
		t.Fatal("agent_error.provider_network must stay on retryableReasons; STREAM_CLOSED retries through the existing bucket, not a new one")
	}
	if retryableReasons[string(taskfailure.ReasonAgentUnknown)] {
		t.Fatal("agent_error.unknown must stay off retryableReasons: that is the misclassification this fix removes")
	}
}

// TestAdapterTransportConnectionErrorRetriesViaExistingProviderNetworkBucket
// pins DENE-727: "TRANSPORT: Connection error." classifies as
// provider_network, and that bucket is already on retryableReasons. Do not
// add a new agent_error.* bucket — unknown stays off the allowlist, which is
// why the misclassification was terminal.
func TestAdapterTransportConnectionErrorRetriesViaExistingProviderNetworkBucket(t *testing.T) {
	reason := taskfailure.Classify("TRANSPORT: Connection error.")
	if reason != taskfailure.ReasonAgentProviderNetwork {
		t.Fatalf("Classify(TRANSPORT: Connection error.) = %q, want %q", reason, taskfailure.ReasonAgentProviderNetwork)
	}
	if !retryableReasons[string(reason)] {
		t.Fatal("agent_error.provider_network must stay on retryableReasons; TRANSPORT wraps retry through the existing bucket, not a new one")
	}
	if retryableReasons[string(taskfailure.ReasonAgentUnknown)] {
		t.Fatal("agent_error.unknown must stay off retryableReasons: that is the misclassification this fix removes")
	}
}

// TestRuntimeCLITimeoutIsNotAutoRetried pins the retry posture for #7112. A
// local runtime CLI that missed its preparation deadline is not transient: the
// same host runs the same CLI on the next attempt and takes the same 8-11s to
// fail again, so every retry is pure cost with a guaranteed outcome. That is
// the difference from agent_error.provider_network, which the old text-based
// classification lumped it in with — and which IS on the allowlist.
//
// Resume stays safe: the agent process never started, so the session the retry
// (or the user's next message) resumes is untouched.
func TestRuntimeCLITimeoutIsNotAutoRetried(t *testing.T) {
	const reason = "runtime_cli_timeout"

	if retryableReasons[reason] {
		t.Errorf("retryableReasons[%q] = true, want false: the stall is local and deterministic", reason)
	}
	if resumeUnsafeFailureReason(reason) {
		t.Errorf("resumeUnsafeFailureReason(%q) = true, want false: the agent never started", reason)
	}
	if !retryableReasons["agent_error.provider_network"] {
		t.Error("agent_error.provider_network must stay retryable: real provider stalls are transient")
	}
}

// TestEnvironmentPrepareFailureIsNotAutoRetried pins the retry posture for
// #7913. The reason moved out of agent_error.* purely so the label is honest —
// it must not quietly buy the failure a retry it never had. Its causes are the
// host's: a full volume, a denied permission, a directory another process
// holds. None of them changes because Multica asked a second time, and
// preparation already waits out the one transient case it knows about (a prior
// run still holding the directory) before it fails.
//
// Resume stays safe: the agent process never started, so the session the user's
// next message resumes is untouched.
func TestEnvironmentPrepareFailureIsNotAutoRetried(t *testing.T) {
	const reason = "environment_prepare_failed"

	if retryableReasons[reason] {
		t.Errorf("retryableReasons[%q] = true, want false: the disk or permission problem is the same on the next attempt", reason)
	}
	if resumeUnsafeFailureReason(reason) {
		t.Errorf("resumeUnsafeFailureReason(%q) = true, want false: the agent never started", reason)
	}
}

// TestOpencodeStreamEndedFailureRetries walks the full chain for #6522: the
// error string pkg/agent/opencode.go's terminal-signal guard produces, through
// taskfailure.Classify, into the retry gate.
//
// The trap this guards: a run that ends on an empty step now goes red instead
// of false-green, but that alone delivers nothing. The reason it classifies to
// has to be on retryableReasons, or the task simply dies with a better label
// and max_attempts never applies — which is exactly where these three errors
// used to land (process_failure for the two "terminal signal" variants, via
// the word "signal", and unknown for the empty-step one).
//
// The second trap, one layer out: FailTask only classifies when the daemon sent
// no reason. An installed daemon predating the rule-7 entry sends a non-empty
// one, so the fix reaches it through NormalizeDaemonReason or not at all — the
// installed-daemon cadence problem that shim exists for. Hence the matrix below
// walks both wire shapes, not just the current-daemon one.
func TestOpencodeStreamEndedFailureRetries(t *testing.T) {
	guardErrors := []struct {
		name   string
		errMsg string
	}{
		{"empty final step", "opencode stream ended on an empty step (no text, no tool call, no reported usage) — the provider produced nothing"},
		{"step open at EOF", "opencode stream ended without a terminal signal (step still open at EOF)"},
		{"continuation never started", "opencode stream ended without a terminal signal (last step required a continuation that never started)"},
	}

	mkTask := func(attempt, max int32) db.AgentTaskQueue {
		return db.AgentTaskQueue{
			Attempt:     attempt,
			MaxAttempts: max,
			IssueID:     pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		}
	}

	// The reason a daemon puts on the wire. "" is a current daemon, which sends
	// nothing and lets the server classify. The rest are what an installed
	// daemon predating rule 7 reports: a NON-EMPTY reason, which skips
	// FailTask's classify-when-empty branch entirely and only NormalizeDaemonReason
	// can still fix. Testing Classify alone would miss that boundary — and did.
	daemonReasons := []struct {
		name     string
		reported string
	}{
		{"current daemon (server classifies)", ""},
		{"legacy daemon reporting process_failure", string(taskfailure.ReasonAgentProcessFailure)},
		{"legacy daemon reporting unknown", string(taskfailure.ReasonAgentUnknown)},
		{"pre-MUL-1949 daemon reporting coarse agent_error", "agent_error"},
	}

	// resolveFailureReason mirrors the two steps FailTask runs in order before
	// it decides retry eligibility.
	resolveFailureReason := func(reported, errMsg string) string {
		if reported == "" {
			reported = taskfailure.Classify(errMsg).String()
		}
		return taskfailure.NormalizeDaemonReason(reported, errMsg).String()
	}

	for _, tc := range guardErrors {
		for _, dr := range daemonReasons {
			t.Run(tc.name+"/"+dr.name, func(t *testing.T) {
				reason := resolveFailureReason(dr.reported, tc.errMsg)
				if reason != string(taskfailure.ReasonAgentProviderNetwork) {
					t.Fatalf("resolveFailureReason(%q, %q) = %q, want %q",
						dr.reported, tc.errMsg, reason, taskfailure.ReasonAgentProviderNetwork)
				}
				// Resume-safe, so the retry child inherits the session and
				// continues rather than redoing the work already paid for.
				if resumeUnsafeFailureReason(reason) {
					t.Errorf("resumeUnsafeFailureReason(%q) = true, want false", reason)
				}
				// With an attempt left, the failure produces a retry task.
				if !retryEligible(reason, mkTask(1, 2), retryEnabledAgent()) {
					t.Errorf("retryEligible(%q, attempt=1/max=2) = false, want true", reason)
				}
				// And still terminates once the ceiling is reached, so a
				// deterministically broken provider cannot loop forever.
				if retryEligible(reason, mkTask(providerNetworkMaxAttempts, 2), retryEnabledAgent()) {
					t.Errorf("retryEligible(%q, attempt=%d/max=2) = true, want false at the ceiling",
						reason, providerNetworkMaxAttempts)
				}
			})
		}
	}

	// The prefix is what identifies the guard's own message. An unrelated
	// failure that merely mentions opencode must keep the daemon's label —
	// upgrading on a loose substring would relabel real crashes as transient
	// stream cuts and retry them.
	t.Run("unrelated opencode failure keeps its reason", func(t *testing.T) {
		const crash = "opencode exited with error: exit status 2 (opencode stream ended is not why)"
		got := taskfailure.NormalizeDaemonReason(string(taskfailure.ReasonAgentProcessFailure), crash).String()
		if got != string(taskfailure.ReasonAgentProcessFailure) {
			t.Errorf("NormalizeDaemonReason(process_failure, %q) = %q, want it left alone", crash, got)
		}
	})
}

// TestSkillBundleFailureFromLegacyDaemonRetries is the mixed-version
// regression for MUL-5370. It walks the exact chain FailTask runs for a task
// an un-upgraded daemon just failed, and asserts the user-visible outcome:
// the run is retried instead of dying.
//
// The trap this guards: the daemon-side fix labels the failure structurally,
// but an old daemon reports a NON-EMPTY catchall, so FailTask's "classify only
// when the caller gave us nothing" branch leaves it alone. Without
// NormalizeDaemonReason the reason stays agent_error.unknown, which is not on
// retryableReasons — meaning the fix would reach only hosts that happened to
// update, while the un-upgraded hosts most likely to be hitting the bug keep
// failing terminally.
func TestSkillBundleFailureFromLegacyDaemonRetries(t *testing.T) {
	const legacyErr = "resolve skill bundles: context deadline exceeded"
	task := db.AgentTaskQueue{
		Attempt:     1,
		MaxAttempts: 2,
		IssueID:     pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
	}

	// What an old daemon puts on the wire, and what FailTask does with it.
	legacyReason := taskfailure.ReasonAgentUnknown.String()
	if retryEligible(legacyReason, task, retryEnabledAgent()) {
		t.Fatal("precondition: the raw catchall must not be retryable, or this test proves nothing")
	}

	normalized := taskfailure.NormalizeDaemonReason(legacyReason, legacyErr).String()
	if normalized != taskfailure.ReasonSkillBundleUnavailable.String() {
		t.Fatalf("normalized reason = %q, want %q", normalized, taskfailure.ReasonSkillBundleUnavailable)
	}
	if !retryEligible(normalized, task, retryEnabledAgent()) {
		t.Errorf("a skill-bundle failure reported by an old daemon must still be retried; got reason %q", normalized)
	}

	// A current daemon supplies the reason itself and must reach the same
	// outcome — the two versions converge rather than diverging by client.
	current := taskfailure.NormalizeDaemonReason(
		taskfailure.ReasonSkillBundleUnavailable.String(),
		`skill bundle unavailable: skill "x" (id=1, 10 bytes) after 30s: context deadline exceeded`,
	).String()
	if !retryEligible(current, task, retryEnabledAgent()) {
		t.Errorf("a skill-bundle failure reported by a current daemon must be retried; got reason %q", current)
	}
}

// TestContextOverflowFromLegacyDaemonRetiresSession is the mixed-version
// regression for GH #6360. It walks the chain FailTask runs for a task an
// un-upgraded daemon just failed on a response-side context overflow, and
// asserts the user-visible outcome: the conversation is retired, so the next
// comment on that issue starts from a fresh session.
//
// Higher stakes than the skill-bundle case above. There, a stale label costs
// one missed retry; here the catchall is on no resume blacklist, so the
// over-full session stays pinned as the (agent, issue) resume pointer and
// EVERY later comment replays the same overflow — one un-upgraded host means a
// permanently stuck issue until it updates.
func TestContextOverflowFromLegacyDaemonRetiresSession(t *testing.T) {
	// Verbatim from Claude Code 2.1.x: the turn is not rejected with a 400,
	// the response comes back with stop_reason model_context_window_exceeded.
	const overflowErr = "API Error: The model has reached its context window limit."

	legacyReason := taskfailure.ReasonAgentUnknown.String()
	if ResumeUnsafeFailure(legacyReason, overflowErr) {
		t.Fatal("precondition: the raw catchall must be resume-safe, or this test proves nothing")
	}

	normalized := taskfailure.NormalizeDaemonReason(legacyReason, overflowErr).String()
	if normalized != taskfailure.ReasonAgentContextOverflow.String() {
		t.Fatalf("normalized reason = %q, want %q", normalized, taskfailure.ReasonAgentContextOverflow)
	}
	if !ResumeUnsafeFailure(normalized, overflowErr) {
		t.Errorf("an overflow reported by an old daemon must retire the session; got reason %q", normalized)
	}

	// A current daemon supplies the reason itself and must reach the same
	// outcome — the two versions converge rather than diverging by client.
	current := taskfailure.NormalizeDaemonReason(taskfailure.ReasonAgentContextOverflow.String(), overflowErr).String()
	if !ResumeUnsafeFailure(current, overflowErr) {
		t.Errorf("an overflow reported by a current daemon must retire the session; got reason %q", current)
	}
}
