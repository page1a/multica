package localdir

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProbeReportsIdentityForAnExistingDirectory(t *testing.T) {
	dir := t.TempDir()
	got := Probe(dir)
	if !got.Exists {
		t.Fatal("Probe of a real directory reported Exists=false")
	}
	if got.RealPath == "" {
		t.Fatal("Probe of a real directory reported no real_path")
	}
	if got.IsGitRepo {
		t.Fatal("a fresh temp dir is not a git repository")
	}
}

func TestProbeOfAMissingPathReportsNothing(t *testing.T) {
	got := Probe(filepath.Join(t.TempDir(), "does-not-exist"))
	if got.Exists || got.RealPath != "" || got.IsGitRepo || got.RepoKey != "" {
		t.Fatalf("missing path produced identity: %+v", got)
	}
}

func TestWritableLocationAcceptsAnExistingDirAndAMissingChild(t *testing.T) {
	dir := t.TempDir()
	if !WritableLocation(dir) {
		t.Fatal("temp dir was not writable")
	}
	if !WritableLocation(filepath.Join(dir, "not-created-yet")) {
		t.Fatal("a missing child of a writable dir should be creatable")
	}
}

func TestWritableLocationRejectsAFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if WritableLocation(file) {
		t.Fatal("a file is not a writable location for working copies")
	}
}
