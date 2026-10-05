package agent

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// Provider-side halves of supplementInbox. Each runs inside the provider
// process, which is the only place OpenCode and Pi accept in-turn input.

//go:embed supplementext/opencode.js
var opencodeSupplementPlugin string

//go:embed supplementext/pi.js
var piSupplementExtension string

// bindSupplement exposes the inbox through the provider-neutral Session hooks.
func (i *supplementInbox) bindSupplement(s *Session) *Session {
	if i != nil {
		s.Supplement = i.deliver
		s.SupplementReady = i.ready
	}
	return s
}

// opencodeConfigWithPlugin adds the plugin to an OPENCODE_CONFIG_CONTENT
// document. OpenCode concatenates plugin lists across config layers, so the
// user's own plugins still load.
func opencodeConfigWithPlugin(content, pluginPath string) (string, error) {
	doc := map[string]any{}
	if strings.TrimSpace(content) != "" {
		if err := json.Unmarshal([]byte(content), &doc); err != nil {
			return "", fmt.Errorf("opencode config content is not a JSON object: %w", err)
		}
	}
	var plugins []any
	switch existing := doc["plugin"].(type) {
	case nil:
	case []any:
		plugins = existing
	default:
		return "", fmt.Errorf("opencode config content has a non-list plugin field")
	}
	doc["plugin"] = append(plugins, fileURL(pluginPath))
	out, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func fileURL(path string) string {
	slashed := filepath.ToSlash(path)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed // Windows drive paths: file:///C:/...
	}
	return "file://" + slashed
}
