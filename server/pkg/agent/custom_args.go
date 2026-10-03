package agent

import (
	"fmt"
	"strings"
)

// ValidateCustomArgsForProvider rejects Codex's -c key=value syntax on
// providers where -c is reserved for session continuation. Keeping this
// validation next to the launch argument policy makes the API and daemon use
// the same provider contract.
func ValidateCustomArgsForProvider(provider string, args []string) error {
	if !providerUsesContinueFlag(provider) {
		return nil
	}
	for i, raw := range args {
		arg := unshellQuoteArg(raw)
		if arg == "-c" {
			if i+1 < len(args) && isCodexConfigAssignment(unshellQuoteArg(args[i+1])) {
				return fmt.Errorf("custom_args contains Codex config syntax (-c %s), but runtime %q uses -c for session continuation; remove it and set model/reasoning fields in the agent configuration", unshellQuoteArg(args[i+1]), provider)
			}
			continue
		}
		if strings.HasPrefix(arg, "-c=") && isCodexConfigAssignment(strings.TrimPrefix(arg, "-c=")) {
			return fmt.Errorf("custom_args contains Codex config syntax (%s), but runtime %q uses -c for session continuation; remove it and set model/reasoning fields in the agent configuration", arg, provider)
		}
	}
	return nil
}

func providerUsesContinueFlag(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude", "codebuddy", "antigravity", "grok", "qwen", "opencode", "deveco", "codearts", "pi":
		return true
	default:
		return false
	}
}

func isCodexConfigAssignment(arg string) bool {
	key, _, ok := strings.Cut(arg, "=")
	return ok && strings.TrimSpace(key) != "" && !strings.HasPrefix(strings.TrimSpace(key), "-")
}
