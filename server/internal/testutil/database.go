package testutil

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestDatabaseURL returns the database DB-backed tests connect to.
//
// TEST_DATABASE_URL wins over DATABASE_URL so a runner can hand the suite a
// database of its own without changing what the application under test
// resolves. `scripts/test-db.sh` sets both to a database it created for this
// run and drops again when the run ends, which is what keeps one run's rows out
// of the next run's assertions.
//
// There is deliberately no localhost fallback. A hardcoded default cannot be
// right — this checkout's database name is allocated per worktree, never the
// `multica` a constant would guess — and it hides the difference between "the
// suite ran against the configured database" and "the suite found nothing and
// skipped". Callers turn an empty result into a skip through SkipDatabase.
func TestDatabaseURL() string {
	if url := os.Getenv("TEST_DATABASE_URL"); url != "" {
		return url
	}
	return os.Getenv("DATABASE_URL")
}

// RequireTestDatabase reports whether this run promised a database.
//
// `scripts/test-db.sh` sets MULTICA_REQUIRE_TEST_DB=1 because it created the
// database the suite is about to use: anything that cannot reach it now is a
// real problem, not a machine that happens to lack Postgres.
func RequireTestDatabase() bool {
	return os.Getenv("MULTICA_REQUIRE_TEST_DB") == "1"
}

// SkipDatabase reports that the test database is unreachable and stops the
// calling test — as a failure when this run required a database, otherwise as
// a skip, which is what lets a laptop without Postgres still run the rest of
// the tree.
//
// Every DB-backed package needs exactly this decision, and each one used to
// spell it out (and the URL above) for itself. Keep them here so a run cannot
// be green in one package and loud in another.
func SkipDatabase(t testing.TB, err error) {
	t.Helper()
	if RequireTestDatabase() {
		t.Fatalf("database required (MULTICA_REQUIRE_TEST_DB=1) but unreachable: %v", err)
	}
	t.Skipf("skipping: database not reachable: %v", err)
}

// SkipUnmigrated reports that the test database is reachable but its schema
// lacks something the test needs — a table or index a migration would have
// created — and stops the calling test the same way SkipDatabase does: a
// failure when this run promised a migrated database, a skip otherwise.
//
// A run that provisioned its own database (scripts/test-db.sh) migrated it
// before the first test started, so a missing object there is a broken
// migration, which is exactly what the suite exists to catch.
func SkipUnmigrated(t testing.TB, what string) {
	t.Helper()
	if RequireTestDatabase() {
		t.Fatalf("database required (MULTICA_REQUIRE_TEST_DB=1) but its schema is incomplete: %s", what)
	}
	t.Skipf("skipping: test database schema is incomplete: %s", what)
}

// MustTestDatabaseURL returns the URL DB-backed tests connect to, stopping the
// calling test through SkipDatabase when none is configured.
//
// Prefer OpenTestDatabase. This is for the suites that need the URL itself —
// to parse it into a config with a private search_path, or to hand it to a
// binary under test — and it carries the same skip-or-fail decision, so a
// suite that builds its own pool still cannot turn "no database" into green.
func MustTestDatabaseURL(t testing.TB) string {
	t.Helper()
	url := TestDatabaseURL()
	if url == "" {
		SkipDatabase(t, errNoTestDatabase)
	}
	return url
}

// OpenTestDatabase connects to the test database and stops the calling test if
// it is unreachable. The pool is closed with the test.
//
// It is the one place a DB-backed suite should get its pool from: the URL, the
// connect, and the skip-or-fail decision are all shared state, and a suite that
// resolved them differently from its neighbours is how a leftover-row failure
// becomes a package-shaped mystery.
func OpenTestDatabase(ctx context.Context, t testing.TB) *pgxpool.Pool {
	t.Helper()
	return OpenTestDatabaseConfig(ctx, t, nil)
}

// OpenTestDatabaseConfig is OpenTestDatabase for suites that need to shape the
// pool before it connects — a private search_path, a connection cap. configure
// receives the parsed config for the test database URL and may be nil.
func OpenTestDatabaseConfig(ctx context.Context, t testing.TB, configure func(*pgxpool.Config)) *pgxpool.Pool {
	t.Helper()

	pool, err := connectTestDatabase(ctx, configure)
	if err != nil {
		SkipDatabase(t, err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// ConnectTestDatabase connects to the test database for callers that have no
// *testing.T — a TestMain building package-wide fixtures. The error is the one
// OpenTestDatabase would have handed to SkipDatabase; pass it to
// ExitIfDatabaseRequired to make the same skip-or-fail decision.
func ConnectTestDatabase(ctx context.Context) (*pgxpool.Pool, error) {
	return connectTestDatabase(ctx, nil)
}

// ExitIfDatabaseRequired is SkipDatabase for TestMain. When this run promised
// a database it prints why the suite could not use it and exits the binary
// with status 1; otherwise it prints the skip notice and returns, leaving the
// caller to exit 0 or run whatever tests need no database.
//
// It is never right to `os.Exit(0)` on a connect error without going through
// here: that is the hollow pass scripts/test-db.sh exists to prevent, and a
// TestMain is the one place `go test` cannot see a skip.
func ExitIfDatabaseRequired(err error) {
	if RequireTestDatabase() {
		fmt.Printf("database required (MULTICA_REQUIRE_TEST_DB=1) but unreachable: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Skipping DB-backed tests: %v\n", err)
}

func connectTestDatabase(ctx context.Context, configure func(*pgxpool.Config)) (*pgxpool.Pool, error) {
	url := TestDatabaseURL()
	if url == "" {
		return nil, errNoTestDatabase
	}
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse test database URL: %w", err)
	}
	if configure != nil {
		if config.ConnConfig.RuntimeParams == nil {
			config.ConnConfig.RuntimeParams = map[string]string{}
		}
		configure(config)
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// errNoTestDatabase reports the one failure OpenTestDatabase invents rather
// than observes: no database was configured at all.
var errNoTestDatabase = errTestDatabaseNotConfigured{}

type errTestDatabaseNotConfigured struct{}

func (errTestDatabaseNotConfigured) Error() string {
	return "neither TEST_DATABASE_URL nor DATABASE_URL is set; run tests through `make test` (scripts/test-go.sh) or set one of them"
}
