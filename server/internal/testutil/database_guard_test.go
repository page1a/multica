package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDBBackedTestsResolveTheDatabaseThroughTestutil walks every _test.go in
// the server module and rejects the two shapes that used to let a DB-backed
// suite go green without a database: reading DATABASE_URL for itself, and
// falling back to a hardcoded localhost URL. Both bypass the skip-or-fail
// decision in this package, so a run that set MULTICA_REQUIRE_TEST_DB=1 could
// still skip — exactly the hollow pass scripts/test-go.sh exists to prevent.
//
// New DB-backed tests go through OpenTestDatabase, OpenTestDatabaseConfig,
// MustTestDatabaseURL, or (in a TestMain) ConnectTestDatabase +
// ExitIfDatabaseRequired.
func TestDBBackedTestsResolveTheDatabaseThroughTestutil(t *testing.T) {
	root := moduleRoot(t)
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "testdata", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if strings.HasPrefix(rel, filepath.Join("internal", "testutil")+string(filepath.Separator)) {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, needle := range []string{
			`Getenv("DATABASE_URL")`,
			`Getenv("TEST_DATABASE_URL")`,
			`"postgres://multica:multica@localhost:5432/multica`,
		} {
			if strings.Contains(string(body), needle) {
				offenders = append(offenders, rel+": "+needle)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("DB-backed tests must resolve their database through internal/testutil (OpenTestDatabase / MustTestDatabaseURL / ConnectTestDatabase), never DATABASE_URL or a localhost fallback of their own:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

// moduleRoot finds the directory holding the server module's go.mod, walking
// up from this package so the walk covers cmd/, internal/ and pkg/ alike.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above " + dir)
		}
		dir = parent
	}
}
