package testutil

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// helperProcessEnv marks a re-exec of this test binary whose only job is to
// run one helper under a chosen environment. The parent asserts on the exit
// status and output, which is the only way to observe what a helper does to
// the test that called it: SkipDatabase ends the goroutine, and
// ExitIfDatabaseRequired ends the process.
const helperProcessEnv = "MULTICA_TESTUTIL_HELPER_PROCESS"

// runHelper re-runs the named test in a subprocess with the database
// environment cleared and `extra` applied on top, returning combined output
// and whether the process exited 0.
func runHelper(t *testing.T, name string, extra ...string) (string, bool) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$", "-test.v", "-test.count=1")
	env := []string{helperProcessEnv + "=1"}
	for _, kv := range os.Environ() {
		key := strings.SplitN(kv, "=", 2)[0]
		switch key {
		case "DATABASE_URL", "TEST_DATABASE_URL", "MULTICA_REQUIRE_TEST_DB", helperProcessEnv:
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env, extra...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run helper %s: %v\n%s", name, err, out)
		}
	}
	return string(out), err == nil
}

// unreachableURL names a port nothing listens on, so a connect attempt fails
// fast and deterministically instead of depending on a database being absent.
const unreachableURL = "postgres://multica:multica@127.0.0.1:1/multica?sslmode=disable&connect_timeout=1"

func TestHelperOpenTestDatabase(t *testing.T) {
	if os.Getenv(helperProcessEnv) != "1" {
		t.Skip("helper body; driven by the subprocess cases below")
	}
	OpenTestDatabase(context.Background(), t)
	t.Log("helper: opened")
}

func TestHelperSkipUnmigrated(t *testing.T) {
	if os.Getenv(helperProcessEnv) != "1" {
		t.Skip("helper body; driven by the subprocess cases below")
	}
	SkipUnmigrated(t, "table widget missing")
}

func TestHelperExitIfDatabaseRequired(t *testing.T) {
	if os.Getenv(helperProcessEnv) != "1" {
		t.Skip("helper body; driven by the subprocess cases below")
	}
	ExitIfDatabaseRequired(errors.New("no database for this helper"))
	t.Log("helper: returned")
}

// TestOpenTestDatabaseWithoutRequirementSkips is the laptop case: nothing
// configured, nothing promised, so the test skips and the package stays
// runnable.
func TestOpenTestDatabaseWithoutRequirementSkips(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string
	}{
		{"unconfigured", nil},
		{"unreachable", []string{"DATABASE_URL=" + unreachableURL}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, ok := runHelper(t, "TestHelperOpenTestDatabase", tc.env...)
			if !ok || !strings.Contains(out, "--- SKIP") || strings.Contains(out, "helper: opened") {
				t.Fatalf("want a skip and exit 0, got ok=%v:\n%s", ok, out)
			}
		})
	}
}

// TestOpenTestDatabaseWhenRequiredFails is the CI / scripts/test-go.sh case:
// the run promised a database, so a helper that cannot reach one must fail
// the test, never skip it — whether the URL is missing or points nowhere.
func TestOpenTestDatabaseWhenRequiredFails(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string
	}{
		{"unconfigured", []string{"MULTICA_REQUIRE_TEST_DB=1"}},
		{"unreachable", []string{"MULTICA_REQUIRE_TEST_DB=1", "DATABASE_URL=" + unreachableURL}},
		{"unreachable via TEST_DATABASE_URL", []string{"MULTICA_REQUIRE_TEST_DB=1", "TEST_DATABASE_URL=" + unreachableURL}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, ok := runHelper(t, "TestHelperOpenTestDatabase", tc.env...)
			if ok || !strings.Contains(out, "--- FAIL") || !strings.Contains(out, "database required (MULTICA_REQUIRE_TEST_DB=1)") {
				t.Fatalf("want a failure naming MULTICA_REQUIRE_TEST_DB, got ok=%v:\n%s", ok, out)
			}
			if strings.Contains(out, "--- SKIP") {
				t.Fatalf("required run skipped instead of failing:\n%s", out)
			}
		})
	}
}

// TestSkipUnmigratedFollowsTheRequirement pins the schema-presence checks to
// the same decision: a missing table is a skip on a laptop and a broken
// migration on a run that provisioned and migrated its own database.
func TestSkipUnmigratedFollowsTheRequirement(t *testing.T) {
	out, ok := runHelper(t, "TestHelperSkipUnmigrated")
	if !ok || !strings.Contains(out, "--- SKIP") {
		t.Fatalf("without a requirement, want skip and exit 0, got ok=%v:\n%s", ok, out)
	}
	out, ok = runHelper(t, "TestHelperSkipUnmigrated", "MULTICA_REQUIRE_TEST_DB=1")
	if ok || !strings.Contains(out, "--- FAIL") || !strings.Contains(out, "table widget missing") {
		t.Fatalf("with a requirement, want failure naming the object, got ok=%v:\n%s", ok, out)
	}
}

// TestExitIfDatabaseRequiredEndsTheProcessOnlyWhenRequired is the TestMain
// contract: a promised database that is missing exits 1 before any test runs;
// otherwise the helper returns and the caller decides what still runs.
func TestExitIfDatabaseRequiredEndsTheProcessOnlyWhenRequired(t *testing.T) {
	out, ok := runHelper(t, "TestHelperExitIfDatabaseRequired")
	if !ok || !strings.Contains(out, "helper: returned") || !strings.Contains(out, "Skipping DB-backed tests") {
		t.Fatalf("without a requirement, want the helper to return, got ok=%v:\n%s", ok, out)
	}
	out, ok = runHelper(t, "TestHelperExitIfDatabaseRequired", "MULTICA_REQUIRE_TEST_DB=1")
	if ok || strings.Contains(out, "helper: returned") || !strings.Contains(out, "database required (MULTICA_REQUIRE_TEST_DB=1)") {
		t.Fatalf("with a requirement, want exit 1 before the helper returns, got ok=%v:\n%s", ok, out)
	}
}
