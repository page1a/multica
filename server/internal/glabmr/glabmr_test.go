package glabmr

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestViewUsesExplicitGitLabRepository(t *testing.T) {
	dir := t.TempDir()
	bin := t.TempDir()
	argsFile := filepath.Join(bin, "args")
	outFile := filepath.Join(bin, "output")
	if err := os.WriteFile(filepath.Join(bin, "glab"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$GLAB_ARGS_FILE\"\ncat \"$GLAB_OUTPUT_FILE\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outFile, []byte(`{"iid":490,"state":"merged","web_url":"http://189.1.240.110:8929/front/game_web_all/-/merge_requests/490","title":"Fix DENE-1156","sha":"27b88db00"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GLAB_ARGS_FILE", argsFile)
	t.Setenv("GLAB_OUTPUT_FILE", outFile)

	pr, err := View(context.Background(), dir, "http://189.1.240.110:8929/front/game_web_all/-/merge_requests/490")
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if pr.State != "merged" || pr.Number != 490 || pr.Provider != "gitlab" {
		t.Fatalf("pr = %+v, want merged GitLab MR", pr)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(raw)), "\n")
	want := []string{"mr", "view", "490", "-R", "http://189.1.240.110:8929/front/game_web_all", "--output", "json"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("glab args = %#v, want %#v", got, want)
	}
}

func TestListRequestsAllMergeRequests(t *testing.T) {
	dir := t.TempDir()
	bin := t.TempDir()
	argsFile := filepath.Join(bin, "args")
	outFile := filepath.Join(bin, "output")
	if err := os.WriteFile(filepath.Join(bin, "glab"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$GLAB_ARGS_FILE\"\ncat \"$GLAB_OUTPUT_FILE\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outFile, []byte(`[{"iid":490,"state":"merged","web_url":"http://gitlab.example/acme/game/-/merge_requests/490"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GLAB_ARGS_FILE", argsFile)
	t.Setenv("GLAB_OUTPUT_FILE", outFile)
	if _, err := List(context.Background(), dir); err != nil {
		t.Fatalf("List: %v", err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(got) < 3 || got[0] != "mr" || got[1] != "list" || got[2] != "--all" {
		t.Fatalf("glab args = %#v, want mr list --all", got)
	}
}
