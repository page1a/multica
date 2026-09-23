package logexport

import "testing"

// TestParseSettingsTolerantOfGarbage is the fail-closed contract: anything the
// parser cannot read yields the zero Settings, which resolves to no repo, so a
// corrupt settings column can never make the server push somewhere.
func TestParseSettingsTolerantOfGarbage(t *testing.T) {
	for _, raw := range [][]byte{
		nil,
		{},
		[]byte("not json"),
		[]byte("null"),
		[]byte(`{"logExport": null}`),
		[]byte(`{"other": {"gitRepo": {"enabled": true, "url": "https://example.com/o/r.git"}}}`),
	} {
		got := ParseSettings(raw)
		if _, ok := got.Repo(); ok {
			t.Fatalf("ParseSettings(%q) produced a usable repo", raw)
		}
	}
}

func TestParseSettingsReadsTheBlock(t *testing.T) {
	raw := []byte(`{
		"routing": {"enabled": true},
		"logExport": {"gitRepo": {"enabled": true, "url": "https://github.com/org/repo.git", "branch": "kun", "dir": "artifacts"}}
	}`)
	repo, ok := ParseSettings(raw).Repo()
	if !ok {
		t.Fatal("a complete block did not resolve to a repo")
	}
	if repo.URL != "https://github.com/org/repo.git" || repo.Branch != "kun" || repo.Dir != "artifacts" {
		t.Fatalf("repo = %+v", repo)
	}
}

// TestRepoRequiresTheSwitchAndURL pins the two required halves. A URL with the
// switch off is inert config, and a switched-on row with no URL is unfinished;
// neither may push.
func TestRepoRequiresTheSwitchAndURL(t *testing.T) {
	cases := []struct {
		name string
		in   Settings
	}{
		{"empty", Settings{}},
		{"nil block", Settings{GitRepo: nil}},
		{"disabled", Settings{GitRepo: &GitRepo{URL: "https://github.com/o/r.git"}}},
		{"enabled without url", Settings{GitRepo: &GitRepo{Enabled: true}}},
		{"enabled with blank url", Settings{GitRepo: &GitRepo{Enabled: true, URL: "   "}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := tc.in.Repo(); ok {
				t.Fatalf("Repo() accepted an unusable config: %+v", tc.in)
			}
		})
	}
}

// TestRepoDefaultsDirAndKeepsBranchOptional — the two halves that are allowed
// to be absent, and what each absence means.
func TestRepoDefaultsDirAndKeepsBranchOptional(t *testing.T) {
	repo, ok := Settings{GitRepo: &GitRepo{Enabled: true, URL: " https://github.com/o/r.git "}}.Repo()
	if !ok {
		t.Fatal("an enabled repo with a URL was rejected")
	}
	if repo.Dir != DefaultDir {
		t.Fatalf("dir = %q, want %q", repo.Dir, DefaultDir)
	}
	if repo.URL != "https://github.com/o/r.git" {
		t.Fatalf("url = %q, want it trimmed", repo.URL)
	}
	if repo.Branch != "" {
		t.Fatalf("branch = %q, want empty meaning the remote default", repo.Branch)
	}

	// A blank dir after trimming to nothing defaults too, and a "/logs/" is
	// normalized so the artifact path never starts with a slash.
	repo, _ = Settings{GitRepo: &GitRepo{Enabled: true, URL: "u", Dir: " /logs/ "}}.Repo()
	if repo.Dir != "logs" {
		t.Fatalf("dir = %q, want logs", repo.Dir)
	}
}

func TestGitRepoPathUsesForwardSlashes(t *testing.T) {
	repo := GitRepo{Dir: "logs"}
	if got := repo.Path("log-export-x.json"); got != "logs/log-export-x.json" {
		t.Fatalf("Path = %q", got)
	}
	nested := GitRepo{Dir: "artifacts/logs"}
	if got := nested.Path("x.json"); got != "artifacts/logs/x.json" {
		t.Fatalf("Path = %q", got)
	}
}

// TestNormalizeRepoDirRejectsEscapes is the security boundary: the directory is
// the prefix of a path joined under a temporary clone, so a `..` segment writes
// the bundle outside that clone and past the RemoveAll that only covers it.
// These values are refused rather than repaired.
func TestNormalizeRepoDirRejectsEscapes(t *testing.T) {
	for _, dir := range []string{
		"..",
		"../escaped",
		"foo/../../tmp/escaped",
		"logs/../../etc",
		"/../escaped",
		"logs/..",
		`..\..\escaped`,
		`logs\..\escaped`,
	} {
		if got, ok := normalizeRepoDir(dir); ok {
			t.Fatalf("normalizeRepoDir(%q) = %q, want refusal", dir, got)
		}
	}
}

func TestNormalizeRepoDirKeepsOrdinaryPaths(t *testing.T) {
	cases := map[string]string{
		"":               DefaultDir,
		"   ":            DefaultDir,
		"/":              DefaultDir,
		"logs":           "logs",
		" /logs/ ":       "logs",
		"artifacts/logs": "artifacts/logs",
		"foo//bar":       "foo/bar",
		"foo/./bar":      "foo/bar",
	}
	for raw, want := range cases {
		got, ok := normalizeRepoDir(raw)
		if !ok {
			t.Fatalf("normalizeRepoDir(%q) refused an ordinary path", raw)
		}
		if got != want {
			t.Fatalf("normalizeRepoDir(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestRepoRejectsEscapingDir pins the handler-facing outcome: a row whose
// directory would escape the clone resolves to no repo, so the push endpoint
// answers "not configured" and never reaches the filesystem.
func TestRepoRejectsEscapingDir(t *testing.T) {
	for _, dir := range []string{"../escaped", "foo/../../tmp/escaped", `logs\..\escaped`} {
		settings := Settings{GitRepo: &GitRepo{
			Enabled: true,
			URL:     "https://github.com/o/r.git",
			Dir:     dir,
		}}
		if _, ok := settings.Repo(); ok {
			t.Fatalf("Repo() accepted an escaping dir %q", dir)
		}
	}
}
