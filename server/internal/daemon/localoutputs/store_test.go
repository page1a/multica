package localoutputs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testID = "01a11579-ea96-76ba-8a38-13dba24d52ce"

func newStore(t *testing.T) *Store {
	t.Helper()
	return &Store{Root: filepath.Join(t.TempDir(), "outputs")}
}

func TestPutThenLookup(t *testing.T) {
	s := newStore(t)
	put, err := s.Put(testID, "report.html", strings.NewReader("<p>hi</p>"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := s.Lookup(testID)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.Path != put.Path || got.Filename != "report.html" || got.SizeBytes != 9 {
		t.Fatalf("lookup = %+v, put = %+v", got, put)
	}
	if filepath.Dir(got.Path) != filepath.Join(s.Root, testID) {
		t.Fatalf("copy outside its entry dir: %s", got.Path)
	}
}

func TestLookupRejectsNonIDs(t *testing.T) {
	s := newStore(t)
	for _, id := range []string{"", "..", "../etc/passwd", "/etc/passwd", testID + "/../x", "not-a-uuid", strings.ToUpper(testID) + " "} {
		if _, err := s.Lookup(id); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Lookup(%q) err = %v, want ErrInvalidID", id, err)
		}
		if _, err := s.Put(id, "a.txt", strings.NewReader("x")); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Put(%q) err = %v, want ErrInvalidID", id, err)
		}
	}
}

func TestPutKeepsFilenameInsideEntry(t *testing.T) {
	s := newStore(t)
	entry, err := s.Put(testID, "../../escape.txt", strings.NewReader("x"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if entry.Filename != "escape.txt" || filepath.Dir(entry.Path) != filepath.Join(s.Root, testID) {
		t.Fatalf("entry = %+v", entry)
	}
	for _, name := range []string{"", "..", "/", "meta.json"} {
		if _, err := s.Put(testID, name, strings.NewReader("x")); err == nil {
			t.Errorf("Put filename %q accepted", name)
		}
	}
}

func TestLookupMissesDeletedOrChangedCopy(t *testing.T) {
	s := newStore(t)
	entry, err := s.Put(testID, "a.txt", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	// Same size, different bytes, new mtime: re-hashed and rejected.
	if err := os.WriteFile(entry.Path, []byte("HELLO"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(entry.Path, future, future)
	if _, err := s.Lookup(testID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("changed copy: err = %v, want ErrNotFound", err)
	}
	// Touched but unchanged: still served.
	if err := os.WriteFile(entry.Path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lookup(testID); err != nil {
		t.Fatalf("touched copy: %v", err)
	}
	if err := os.Remove(entry.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lookup(testID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted copy: err = %v, want ErrNotFound", err)
	}
}

func TestLookupMissesSymlinkSwap(t *testing.T) {
	s := newStore(t)
	entry, err := s.Put(testID, "a.txt", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other.txt")
	if err := os.WriteFile(other, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(entry.Path)
	if err := os.Symlink(other, entry.Path); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, err := s.Lookup(testID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("symlinked copy: err = %v, want ErrNotFound", err)
	}
}

func TestPrune(t *testing.T) {
	now := time.Now()
	s := newStore(t)
	s.Now = func() time.Time { return now.Add(-48 * time.Hour) }
	if _, err := s.Put(testID, "old.txt", strings.NewReader("old")); err != nil {
		t.Fatal(err)
	}
	fresh := "11111111-2222-3333-4444-555555555555"
	s.Now = func() time.Time { return now }
	if _, err := s.Put(fresh, "new.txt", strings.NewReader("new")); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(s.Root, "keep-me")
	if err := os.MkdirAll(stray, 0o700); err != nil {
		t.Fatal(err)
	}

	if removed, _ := s.Prune(0); removed != 0 {
		t.Fatalf("ttl 0 removed %d", removed)
	}
	removed, bytes := s.Prune(24 * time.Hour)
	if removed != 1 || bytes == 0 {
		t.Fatalf("Prune = %d, %d; want 1 entry", removed, bytes)
	}
	if _, err := s.Lookup(testID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old copy survived prune")
	}
	if _, err := s.Lookup(fresh); err != nil {
		t.Fatalf("fresh copy pruned: %v", err)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Fatalf("prune touched a directory it did not create")
	}
}
