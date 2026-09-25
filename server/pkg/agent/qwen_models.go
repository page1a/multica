package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// qwenConfigHome resolves the Qwen Code home whose .env and settings.json are
// in effect for this machine. QWEN_HOME in the overlay (an agent custom_env)
// wins, then QWEN_HOME in the daemon process, then ~/.qwen. Tests replace it.
var qwenConfigHome = defaultQwenConfigHome

func defaultQwenConfigHome(overlay map[string]string) string {
	if home := overlayValue(overlay, "QWEN_HOME"); home != "" {
		return home
	}
	if home := strings.TrimSpace(os.Getenv("QWEN_HOME")); home != "" {
		return home
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(userHome, ".qwen")
}

// qwenModelEndpoint is the chain's endpoint step for Qwen Code. The machine
// home (.env and settings.json) is the base. An agent custom_env attached
// with WithModelEnvOverlay wins over those files, the same way a value
// already in the process environment wins over dotenv. The API key is used
// only for the local probe.
func qwenModelEndpoint(ctx context.Context) (string, string, string, error) {
	return qwenEndpointFields(ModelEnvOverlay(ctx))
}

func qwenEndpointFields(overlay map[string]string) (string, string, string, error) {
	endpoint, err := resolveQwenEndpoint(qwenConfigHome(overlay), overlay)
	if err != nil {
		return "", "", "", err
	}
	return endpoint.BaseURL, endpoint.APIKey, endpoint.Model, nil
}

func discoverQwenModels(ctx context.Context, overlay map[string]string) ([]Model, error) {
	baseURL, apiKey, model, err := qwenEndpointFields(overlay)
	if err != nil {
		return nil, err
	}
	return discoverCompatibleEndpointModels(ctx, baseURL, apiKey, model)
}

// qwenEndpoint is the OpenAI-compatible route one Qwen config resolves to.
// APIKey is a secret and must not be logged or placed in an error string.
type qwenEndpoint struct {
	BaseURL string
	APIKey  string
	Model   string
}

type qwenSettings struct {
	Env            map[string]any               `json:"env"`
	ModelProviders map[string][]qwenProviderRow `json:"modelProviders"`
	Model          struct {
		Name string `json:"name"`
	} `json:"model"`
	Security struct {
		Auth struct {
			SelectedType string `json:"selectedType"`
			APIKey       string `json:"apiKey"`
			BaseURL      string `json:"baseUrl"`
		} `json:"auth"`
	} `json:"security"`
}

type qwenProviderRow struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"baseUrl"`
	EnvKey  string `json:"envKey"`
}

// resolveQwenEndpoint merges the Qwen home with an optional env overlay.
//
// Later layers win, and a layer only fills a field it actually sets:
//
//  1. settings.json security.auth and model.name
//  2. settings.json env
//  3. ~/.qwen/.env
//  4. overlay (agent custom_env)
//  5. the selected modelProviders row, only for fields still empty
//
// OPENAI_BASE_URL therefore beats a modelProviders baseUrl, because that
// variable is what Qwen Code calls when it is set. modelProviders supplies the
// route only when the variable is absent. Missing files are fine. A directory
// that cannot be read, or a config that names no endpoint, is an error — not
// an empty catalog.
func resolveQwenEndpoint(dir string, overlay map[string]string) (qwenEndpoint, error) {
	if strings.TrimSpace(dir) == "" {
		return qwenEndpoint{}, errModelsListUnavailable("没有读到 OpenAI 兼容端点")
	}
	settings, settingsErr := readQwenSettings(filepath.Join(dir, "settings.json"))
	fileEnv, fileErr := readDotEnvFile(filepath.Join(dir, ".env"))
	if settingsErr != nil && fileErr != nil && len(overlay) == 0 {
		return qwenEndpoint{}, errModelsListUnavailable("没有读到 Qwen 配置")
	}
	settingsEnv := stringMap(settings.Env)

	endpoint := qwenEndpoint{
		BaseURL: strings.TrimSpace(settings.Security.Auth.BaseURL),
		APIKey:  strings.TrimSpace(settings.Security.Auth.APIKey),
		Model:   strings.TrimSpace(settings.Model.Name),
	}
	// OPENAI_* from the env layers wins over modelProviders. The provider row
	// only fills a field that is still empty afterwards, so a key named by
	// envKey cannot be clobbered by a lower layer applied later.
	applyQwenEnvLayer(&endpoint, settingsEnv)
	applyQwenEnvLayer(&endpoint, fileEnv)
	applyQwenEnvLayer(&endpoint, overlay)
	if row, ok := selectedQwenProvider(settings, endpoint.Model); ok {
		if endpoint.BaseURL == "" {
			endpoint.BaseURL = strings.TrimSpace(row.BaseURL)
		}
		if endpoint.APIKey == "" {
			endpoint.APIKey = lookupQwenKey(overlay, fileEnv, settingsEnv, strings.TrimSpace(row.EnvKey))
		}
		if endpoint.Model == "" {
			endpoint.Model = strings.TrimSpace(row.ID)
		}
	}

	if endpoint.BaseURL == "" {
		return qwenEndpoint{}, errModelsListUnavailable("没有读到 OpenAI 兼容端点")
	}
	return endpoint, nil
}

// lookupQwenKey reads the provider's envKey from the highest layer that sets it.
func lookupQwenKey(overlay, fileEnv, settingsEnv map[string]string, envKey string) string {
	if envKey == "" {
		return ""
	}
	for _, layer := range []map[string]string{overlay, fileEnv, settingsEnv} {
		if value := overlayValue(layer, envKey); value != "" {
			return value
		}
	}
	return ""
}

func applyQwenEnvLayer(endpoint *qwenEndpoint, env map[string]string) {
	if base := overlayValue(env, "OPENAI_BASE_URL"); base != "" {
		endpoint.BaseURL = base
	}
	if key := overlayValue(env, "OPENAI_API_KEY"); key != "" {
		endpoint.APIKey = key
	}
	if model := overlayValue(env, "OPENAI_MODEL"); model != "" {
		endpoint.Model = model
		return
	}
	if model := overlayValue(env, "QWEN_MODEL"); model != "" {
		endpoint.Model = model
	}
}

func selectedQwenProvider(settings qwenSettings, modelName string) (qwenProviderRow, bool) {
	selected := strings.TrimSpace(settings.Security.Auth.SelectedType)
	if selected == "" {
		selected = "openai"
	}
	// Only the OpenAI-compatible protocol has a /models list we can ask for.
	if selected != "openai" {
		return qwenProviderRow{}, false
	}
	rows := settings.ModelProviders[selected]
	if len(rows) == 0 {
		return qwenProviderRow{}, false
	}
	if modelName != "" {
		for _, row := range rows {
			if strings.TrimSpace(row.ID) == modelName {
				return row, true
			}
		}
	}
	for _, row := range rows {
		if strings.TrimSpace(row.BaseURL) != "" {
			return row, true
		}
	}
	return rows[0], true
}

func readQwenSettings(path string) (qwenSettings, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return qwenSettings{}, nil
		}
		return qwenSettings{}, err
	}
	var settings qwenSettings
	if err := json.Unmarshal(body, &settings); err != nil {
		// A settings file we cannot parse is ignored when .env still names an
		// endpoint. The parse error itself can quote file contents, so it is
		// not wrapped into the caller's error.
		return qwenSettings{}, err
	}
	return settings, nil
}

func readDotEnvFile(path string) (map[string]string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return parseDotEnv(string(body)), nil
}

// parseDotEnv reads KEY=VALUE lines. Blank lines and # comments are ignored.
// A matching pair of quotes around a value is stripped. Values are not
// expanded, so a secret cannot be pulled in from another variable and then
// echoed by accident.
func parseDotEnv(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = unquoteEnvValue(strings.TrimSpace(value))
	}
	return out
}

func unquoteEnvValue(value string) string {
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
			return value[1 : len(value)-1]
		}
	}
	return value
}

func stringMap(raw map[string]any) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for key, value := range raw {
		text, ok := value.(string)
		if !ok {
			continue
		}
		out[key] = strings.TrimSpace(text)
	}
	return out
}

func overlayValue(env map[string]string, key string) string {
	if key == "" || env == nil {
		return ""
	}
	return strings.TrimSpace(env[key])
}
