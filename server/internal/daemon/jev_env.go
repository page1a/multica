package daemon

import (
	"os"
	"path/filepath"
	"strings"
)

// The JEV fast-judgement layer keeps its credentials in one file per machine,
// `~/.config/jev/runtime.env`, written by `jev configure`. The CLI reads it
// itself, so an agent that shells out to `jev` has always worked. Anything
// that talks to TypeSafe directly — the documented curl, the Python and
// JavaScript SDKs — reads TYPESAFE_API_KEY from the environment instead, and
// that variable is set by nobody: the daemon may be launched by launchd or by
// make, neither of which sources a login shell.
//
// The result reads as "JEV is not configured on this machine" when in fact it
// is, and the failure is a 403 from the API rather than anything the agent can
// see the cause of. Exporting the same file into every task environment closes
// that gap once, instead of once per agent's custom_env.
const (
	jevConfigDirName    = "jev"
	jevRuntimeEnvName   = "runtime.env"
	jevAPIKeyEnv        = "JEV_API_KEY"
	jevBaseURLEnv       = "JEV_BASE_URL"
	jevModelEnv         = "JEV_MODEL"
	typesafeAPIKeyEnv   = "TYPESAFE_API_KEY"
	typesafeBaseURLEnv  = "TYPESAFE_BASE_URL"
	typesafeDefaultBase = "https://api.typesafe.ai"
)

// jevConfigKeys is the exact set the jev CLI honours in runtime.env. Reading
// only these means a stray line in that file cannot inject an arbitrary
// variable into every agent process on the machine.
var jevConfigKeys = map[string]bool{
	jevAPIKeyEnv:  true,
	jevBaseURLEnv: true,
	jevModelEnv:   true,
}

// jevRuntimeEnvPath resolves the config file the same way the jev CLI does:
// `$XDG_CONFIG_HOME/jev/runtime.env`, else `~/.config/jev/runtime.env`.
func (d *Daemon) jevRuntimeEnvPath() string {
	if d.jevConfigPathOverride != "" {
		return d.jevConfigPathOverride
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(expandHomePrefix(xdg, home), jevConfigDirName, jevRuntimeEnvName)
	}
	return filepath.Join(home, ".config", jevConfigDirName, jevRuntimeEnvName)
}

// jevTaskEnv reads runtime.env and returns the variables an agent task should
// inherit, or nil when the machine has no JEV configured. A missing or
// unreadable file is the normal case on a machine that never ran
// `jev configure`; it is not an error and must not fail the task.
//
// Returning nil rather than a partly-filled map matters: a TYPESAFE_API_KEY
// set to the empty string is worse than an absent one, because an SDK reads it
// as configured and sends `Bearer ` to the API.
func (d *Daemon) jevTaskEnv() map[string]string {
	data, err := os.ReadFile(d.jevRuntimeEnvPath())
	if err != nil {
		return nil
	}
	return jevTaskEnvFromRuntimeFile(string(data))
}

// jevTaskEnvFromRuntimeFile is the pure core: file contents in, task env out.
func jevTaskEnvFromRuntimeFile(contents string) map[string]string {
	config := map[string]string{}
	for _, line := range strings.Split(contents, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		if jevConfigKeys[name] {
			config[name] = strings.TrimSpace(value)
		}
	}
	// The key is the whole point. Without it the CLI falls back to its own
	// defaults anyway, and the SDK variables would be actively misleading.
	if config[jevAPIKeyEnv] == "" {
		return nil
	}

	env := map[string]string{
		jevAPIKeyEnv: config[jevAPIKeyEnv],
		// Same credential under the name the TypeSafe docs and SDKs read.
		// One file stays the source of truth for both spellings, so the two
		// cannot drift into pointing at different accounts.
		typesafeAPIKeyEnv: config[jevAPIKeyEnv],
	}
	baseURL := config[jevBaseURLEnv]
	if baseURL == "" {
		baseURL = typesafeDefaultBase
	}
	env[jevBaseURLEnv] = baseURL
	env[typesafeBaseURLEnv] = baseURL
	if model := config[jevModelEnv]; model != "" {
		env[jevModelEnv] = model
	}
	return env
}
