package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

const jevTestKey = "apikey_test_0123456789"

// TestJevTaskEnvExportsBothSpellings is the whole point of the file: one
// credential on disk has to arrive under the name the CLI reads and the name
// the SDKs read, pointing at the same account.
func TestJevTaskEnvExportsBothSpellings(t *testing.T) {
	env := jevTaskEnvFromRuntimeFile(
		"JEV_API_KEY=" + jevTestKey + "\n" +
			"JEV_BASE_URL=https://api.typesafe.ai\n" +
			"JEV_MODEL=jev-latest\n",
	)
	for name, want := range map[string]string{
		jevAPIKeyEnv:       jevTestKey,
		typesafeAPIKeyEnv:  jevTestKey,
		jevBaseURLEnv:      "https://api.typesafe.ai",
		typesafeBaseURLEnv: "https://api.typesafe.ai",
		jevModelEnv:        "jev-latest",
	} {
		if env[name] != want {
			t.Errorf("%s = %q, want %q", name, env[name], want)
		}
	}
}

// TestJevTaskEnvWithoutAKeyIsAbsentEntirely. A TYPESAFE_API_KEY set to the
// empty string is worse than no variable: an SDK reads it as configured and
// sends `Bearer ` upstream, turning a clear "not configured" into a 403.
func TestJevTaskEnvWithoutAKeyIsAbsentEntirely(t *testing.T) {
	for _, contents := range []string{
		"",
		"JEV_BASE_URL=https://api.typesafe.ai\nJEV_MODEL=jev-latest\n",
		"JEV_API_KEY=\n",
		"JEV_API_KEY=   \n",
		"# JEV_API_KEY=" + jevTestKey + "\n",
	} {
		if env := jevTaskEnvFromRuntimeFile(contents); env != nil {
			t.Errorf("contents %q produced %v, want nil", contents, env)
		}
	}
}

// TestJevTaskEnvDefaultsTheBaseURL so a runtime.env holding only the key still
// points the SDKs at the same host the CLI would have used.
func TestJevTaskEnvDefaultsTheBaseURL(t *testing.T) {
	env := jevTaskEnvFromRuntimeFile("JEV_API_KEY=" + jevTestKey + "\n")
	if env[typesafeBaseURLEnv] != typesafeDefaultBase {
		t.Errorf("%s = %q, want %q", typesafeBaseURLEnv, env[typesafeBaseURLEnv], typesafeDefaultBase)
	}
	if _, ok := env[jevModelEnv]; ok {
		// An unset model means "the CLI's own default", which is not this
		// package's to guess.
		t.Errorf("%s was invented: %q", jevModelEnv, env[jevModelEnv])
	}
}

// TestJevTaskEnvIgnoresUnknownNames. This file lands in the environment of
// every agent process on the machine, so a line nobody vetted must not become
// a variable those processes see.
func TestJevTaskEnvIgnoresUnknownNames(t *testing.T) {
	env := jevTaskEnvFromRuntimeFile(
		"JEV_API_KEY=" + jevTestKey + "\n" +
			"PATH=/tmp/evil\n" +
			"ANTHROPIC_API_KEY=sk-someone-elses\n" +
			"a line with no equals sign\n",
	)
	for _, name := range []string{"PATH", "ANTHROPIC_API_KEY"} {
		if _, ok := env[name]; ok {
			t.Errorf("%s leaked out of runtime.env", name)
		}
	}
	if env[jevAPIKeyEnv] != jevTestKey {
		t.Errorf("the valid line was dropped along with the junk: %q", env[jevAPIKeyEnv])
	}
}

// TestJevTaskEnvOnAnUnconfiguredMachine covers the common case end to end: no
// runtime.env at all has to be silent and nil, never an error that fails a task.
func TestJevTaskEnvOnAnUnconfiguredMachine(t *testing.T) {
	d := &Daemon{jevConfigPathOverride: filepath.Join(t.TempDir(), "runtime.env")}
	if env := d.jevTaskEnv(); env != nil {
		t.Fatalf("jevTaskEnv on a machine with no config = %v, want nil", env)
	}
}

// TestJevTaskEnvReadsTheOverriddenPath proves the override is wired, so the
// rest of the suite never reads the developer's own credentials.
func TestJevTaskEnvReadsTheOverriddenPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.env")
	if err := os.WriteFile(path, []byte("JEV_API_KEY="+jevTestKey+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	d := &Daemon{jevConfigPathOverride: path}
	if got := d.jevTaskEnv()[typesafeAPIKeyEnv]; got != jevTestKey {
		t.Fatalf("%s = %q, want the key from the fixture", typesafeAPIKeyEnv, got)
	}
}
