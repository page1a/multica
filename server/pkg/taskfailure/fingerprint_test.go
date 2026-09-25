package taskfailure

import "testing"

func TestFingerprintNormalisesVolatileParts(t *testing.T) {
	a := Fingerprint("runtime_recovery", "permission denied: /work/01a0d281-c0a1-7af1-b9b4-ba7190ce21b0/wt-1234 (attempt 1)")
	b := Fingerprint("runtime_recovery", "Permission   denied: /work/7f2c1c34-9d0e-7a11-8b1a-0e4f5a6b7c8d/wt-99 (attempt 2)")
	if a != b {
		t.Fatalf("fingerprint should ignore uuid/number/whitespace: %s != %s", a, b)
	}
	if len(a) != 24 {
		t.Fatalf("fingerprint length = %d, want 24", len(a))
	}
	if Fingerprint("timeout", "permission denied: /work") == a {
		t.Fatal("fingerprint must include the failure reason")
	}
	if Fingerprint("runtime_recovery", "no space left on device") == a {
		t.Fatal("different error shapes must not collide")
	}
}

func TestIsDeterministic(t *testing.T) {
	for _, text := range []string{
		"replay conflict on branch agent/agent/dene-814",
		"EACCES: permission denied, open '/x'",
		"write /tmp/x: no space left on device",
		"Read-only file system",
		"ENOSPC",
	} {
		if !IsDeterministic(text) {
			t.Errorf("%q should be deterministic", text)
		}
	}
	for _, text := range []string{
		"connection reset by peer",
		"API Error: Connection closed mid-response",
		"Selected model is at capacity",
		"",
	} {
		if IsDeterministic(text) {
			t.Errorf("%q should stay transient", text)
		}
	}
}
