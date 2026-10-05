package taskfailure

import "testing"

func TestTransientCheckout(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"DENE-1312 unwritable file", "execenv: git worktree add: error: unable to write file video/remotion/public/textures/a.png\nfatal: Could not reset index file to revision 'HEAD'.: exit status 128", true},
		{"index reset alone", "fatal: Could not reset index file to revision 'HEAD'.", true},
		{"held index lock", "fatal: Unable to create '/r/.git/worktrees/x/index.lock': File exists.", true},
		{"held ref lock", "fatal: cannot lock ref 'refs/heads/agent/x': Unable to create '/r/.git/refs/heads/agent/x.lock': File exists.", true},
		{"branch already exists", "fatal: a branch named 'agent/x' already exists", false},
		{"bad base", "fatal: invalid reference: deadbeef", false},
		{"path registered", "fatal: '/w/x' is a missing but already registered worktree", false},
		{"no repo", "fatal: not a git repository", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		if got := TransientCheckout(tc.text); got != tc.want {
			t.Errorf("%s: TransientCheckout = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRetryableEnvironmentPrepare(t *testing.T) {
	lock := "fatal: cannot lock ref 'refs/heads/x': File exists."
	if !RetryableEnvironmentPrepare(string(ReasonEnvironmentPrepareFailed), lock) {
		t.Error("passing checkout fault under environment_prepare_failed should be retryable")
	}
	if RetryableEnvironmentPrepare(string(ReasonEnvironmentPrepareFailed), "fatal: not a git repository") {
		t.Error("deterministic prepare failure must stay final")
	}
	if RetryableEnvironmentPrepare("agent_error", lock) {
		t.Error("only environment_prepare_failed is widened")
	}
}
