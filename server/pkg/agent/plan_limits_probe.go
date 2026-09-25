package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"
	codexUsageURL  = "https://chatgpt.com/backend-api/wham/usage"

	claudeKeychainService = "Claude Code-credentials"
	codexKeychainService  = "Codex Auth"
	geminiKeychainService = "gemini-cli-oauth"

	fiveHourMinutes  int64 = 300
	sevenDayMinutes  int64 = 10_080
	planQuotaBodyCap       = 1 << 20
)

// PlanQuotaProbe reads local Claude Code / Codex / Gemini CLI / Grok
// credentials and queries the same unofficial usage endpoints other desktop
// tools (e.g. cc-switch) use. The snapshot is credential-free: percentages,
// window length, and reset time only.
type PlanQuotaProbe struct {
	Home   string
	Client *http.Client
	Now    func() time.Time
	// ClaudeConfigDir is the Claude Code account directory this probe reads
	// `.credentials.json` from when the caller knows which one the CLI actually
	// runs against. An agent bound to a numbered account is served by the same
	// `CLAUDE_CONFIG_DIR` its task environment is assembled from, so probing the
	// daemon process's own account instead would report the wrong seat's quota
	// (DENE-715).
	//
	// Empty keeps the previous resolution, which is also what an agent with no
	// binding configured keeps using: the CLI's default account — the daemon
	// process's own CLAUDE_CONFIG_DIR, else <Home>/.claude, and on macOS the
	// login Keychain for the token. A non-empty value names a numbered account,
	// whose credentials live in that directory's `.credentials.json`; the
	// machine-wide Keychain never speaks for it.
	ClaudeConfigDir string
	// Optional URL overrides so tests can serve fixtures without the network.
	ClaudeUsageURL     string
	CodexUsageURL      string
	GeminiLoadURL      string
	GeminiQuotaURL     string
	GeminiTokenURL     string
	GrokBillingURL     string
	GrokCreditsURL     string
	KimiUsagesURL      string
	GLMQuotaURL        string
	MiniMaxRemainsURL  string
	DeepSeekBalanceURL string
	// LookupKeychain, when set, replaces the macOS Keychain read. Tests inject
	// a no-op so they never shell out to `security`.
	LookupKeychain func(service string) (string, bool)
	// LookupAPIKey, when set, replaces env/file API-key discovery for coding
	// plan and prepaid-balance probes. Tests inject a stub so ambient
	// DEEPSEEK_API_KEY / ZHIPUAI_API_KEY cannot leak into the suite.
	LookupAPIKey func(provider string) (string, bool)
}

func (p PlanQuotaProbe) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p PlanQuotaProbe) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Timeout: 12 * time.Second}
}

func (p PlanQuotaProbe) lookupKeychain(service string) (string, bool) {
	if p.LookupKeychain != nil {
		return p.LookupKeychain(service)
	}
	return readMacKeychainPassword(service)
}

// ProbeClaude returns the live 5h/7d Claude Code subscription windows, or
// (nil, nil) when no OAuth credentials are present.
func (p PlanQuotaProbe) ProbeClaude(ctx context.Context) (*protocol.PlanLimitsSnapshot, error) {
	token := readClaudeAccessToken(p.Home, p.ClaudeConfigDir, p.lookupKeychain)
	if token == "" {
		return nil, nil
	}
	url := p.ClaudeUsageURL
	if url == "" {
		url = claudeUsageURL
	}
	body, err := p.getJSON(ctx, url, token, map[string]string{
		"anthropic-beta": "oauth-2025-04-20",
	})
	if err != nil {
		return nil, err
	}
	return ParseClaudeUsageJSON(body, p.now())
}

// ProbeCodex returns the live 5h/7d Codex subscription windows, or (nil, nil)
// when the CLI is not using a ChatGPT OAuth login.
func (p PlanQuotaProbe) ProbeCodex(ctx context.Context) (*protocol.PlanLimitsSnapshot, error) {
	token, accountID := readCodexAccessToken(p.Home, p.lookupKeychain)
	if token == "" {
		return nil, nil
	}
	url := p.CodexUsageURL
	if url == "" {
		url = codexUsageURL
	}
	headers := map[string]string{"User-Agent": "codex-cli"}
	if accountID != "" {
		headers["ChatGPT-Account-Id"] = accountID
	}
	body, err := p.getJSON(ctx, url, token, headers)
	if err != nil {
		return nil, err
	}
	return ParseCodexUsageJSON(body, p.now())
}

func (p PlanQuotaProbe) getJSON(ctx context.Context, url, bearer string, extra map[string]string) ([]byte, error) {
	return p.doHTTP(ctx, http.MethodGet, url, bearer, extra, nil)
}

func (p PlanQuotaProbe) postJSON(ctx context.Context, url, bearer string, extra map[string]string, body []byte) ([]byte, error) {
	headers := map[string]string{"Content-Type": "application/json"}
	for k, v := range extra {
		headers[k] = v
	}
	return p.doHTTP(ctx, http.MethodPost, url, bearer, headers, body)
}

func (p PlanQuotaProbe) doHTTP(ctx context.Context, method, url, bearer string, extra map[string]string, body []byte) ([]byte, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, planQuotaBodyCap))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("plan quota auth failed (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("plan quota HTTP %d", resp.StatusCode)
	}
	return raw, nil
}

// ParseClaudeUsageJSON maps Anthropic's OAuth usage payload onto the
// credential-free PlanLimitsSnapshot wire shape. Only the 5-hour and 7-day
// windows are kept so the hover card matches the subscription meters users
// already know.
func ParseClaudeUsageJSON(body []byte, observedAt time.Time) (*protocol.PlanLimitsSnapshot, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	windows := make([]protocol.PlanLimitWindow, 0, 2)
	for _, item := range []struct {
		key     string
		name    string
		minutes int64
	}{
		{key: "five_hour", name: "five_hour", minutes: fiveHourMinutes},
		{key: "seven_day", name: "seven_day", minutes: sevenDayMinutes},
	} {
		msg, ok := raw[item.key]
		if !ok {
			continue
		}
		var window struct {
			Utilization *float64 `json:"utilization"`
			ResetsAt    string   `json:"resets_at"`
		}
		if err := json.Unmarshal(msg, &window); err != nil || window.Utilization == nil {
			continue
		}
		used := clampPercent(*window.Utilization)
		out := protocol.PlanLimitWindow{Name: item.name, UsedPercent: &used}
		minutes := item.minutes
		out.WindowMinutes = &minutes
		if resetsAt, ok := parseRFC3339Unix(window.ResetsAt); ok {
			out.ResetsAt = &resetsAt
		}
		windows = append(windows, out)
	}
	return snapshotFromWindows("claude", windows, observedAt), nil
}

// ParseCodexUsageJSON maps ChatGPT's /wham/usage payload onto the same
// primary/secondary window names the Codex JSONL observer already uses.
func ParseCodexUsageJSON(body []byte, observedAt time.Time) (*protocol.PlanLimitsSnapshot, error) {
	var raw struct {
		RateLimit *struct {
			PrimaryWindow   *codexAPIWindow `json:"primary_window"`
			SecondaryWindow *codexAPIWindow `json:"secondary_window"`
		} `json:"rate_limit"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	if raw.RateLimit == nil {
		return nil, nil
	}
	windows := make([]protocol.PlanLimitWindow, 0, 2)
	for _, item := range []struct {
		name   string
		window *codexAPIWindow
	}{
		{name: "primary", window: raw.RateLimit.PrimaryWindow},
		{name: "secondary", window: raw.RateLimit.SecondaryWindow},
	} {
		if item.window == nil || item.window.UsedPercent == nil {
			continue
		}
		used := clampPercent(*item.window.UsedPercent)
		out := protocol.PlanLimitWindow{Name: item.name, UsedPercent: &used}
		if item.window.LimitWindowSeconds != nil && *item.window.LimitWindowSeconds > 0 {
			minutes := *item.window.LimitWindowSeconds / 60
			if minutes > 0 {
				out.WindowMinutes = &minutes
			}
		}
		if item.window.ResetAt != nil && *item.window.ResetAt > 0 {
			resetsAt := *item.window.ResetAt
			out.ResetsAt = &resetsAt
		}
		windows = append(windows, out)
	}
	return snapshotFromWindows("codex", windows, observedAt), nil
}

type codexAPIWindow struct {
	UsedPercent        *float64 `json:"used_percent"`
	LimitWindowSeconds *int64   `json:"limit_window_seconds"`
	ResetAt            *int64   `json:"reset_at"`
}

func snapshotFromWindows(provider string, windows []protocol.PlanLimitWindow, observedAt time.Time) *protocol.PlanLimitsSnapshot {
	if len(windows) == 0 {
		return nil
	}
	status := protocol.PlanLimitsStatusAvailable
	for _, window := range windows {
		if window.UsedPercent != nil && *window.UsedPercent >= 100 {
			status = protocol.PlanLimitsStatusExhausted
			break
		}
		if window.Remaining != nil && *window.Remaining <= 0 && window.UsedPercent == nil {
			status = protocol.PlanLimitsStatusExhausted
			break
		}
	}
	snapshot := &protocol.PlanLimitsSnapshot{
		Provider: provider,
		Status:   status,
		Windows:  windows,
	}
	if !observedAt.IsZero() {
		snapshot.ObservedAt = observedAt.Unix()
	}
	return snapshot
}

func clampPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func parseRFC3339Unix(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if ts, err := time.Parse(time.RFC3339, value); err == nil {
		return ts.Unix(), true
	}
	if ts, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return ts.Unix(), true
	}
	return 0, false
}

func jsonFloat(msg json.RawMessage) (float64, bool) {
	var value float64
	if json.Unmarshal(msg, &value) == nil {
		return value, true
	}
	var text string
	if json.Unmarshal(msg, &text) != nil {
		return 0, false
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func parseFlexibleReset(msg json.RawMessage) (int64, bool) {
	if ts, ok := jsonFloat(msg); ok && ts > 0 {
		value := int64(ts)
		if value > 1_000_000_000_000 {
			return value / 1000, true
		}
		return value, true
	}
	var text string
	if json.Unmarshal(msg, &text) != nil {
		return 0, false
	}
	if ts, ok := parseRFC3339Unix(text); ok {
		return ts, true
	}
	return 0, false
}

func (p PlanQuotaProbe) apiKey(provider string, envNames []string, relFiles []string) string {
	if p.LookupAPIKey != nil {
		key, _ := p.LookupAPIKey(provider)
		return strings.TrimSpace(key)
	}
	for _, name := range envNames {
		if key := strings.TrimSpace(os.Getenv(name)); key != "" {
			return key
		}
	}
	home := homeDir(p.Home)
	for _, rel := range relFiles {
		if key := readAPIKeyFile(filepath.Join(home, rel)); key != "" {
			return key
		}
	}
	return ""
}

func readAPIKeyFile(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return ""
	}
	if !strings.HasPrefix(text, "{") && !strings.HasPrefix(text, "[") {
		if strings.ContainsAny(text, "\r\n") || len(text) > 512 {
			return ""
		}
		return text
	}
	var parsed map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &parsed) != nil {
		return ""
	}
	for _, key := range []string{"api_key", "apiKey", "access_token", "token", "key"} {
		msg, ok := parsed[key]
		if !ok {
			continue
		}
		var value string
		if json.Unmarshal(msg, &value) == nil {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// readClaudeAccessToken reads the Claude Code OAuth token. configDir is the
// account directory the caller knows this run uses (an agent's bound
// `CLAUDE_CONFIG_DIR`); empty falls back to the daemon process's own account,
// which is the pre-DENE-715 behavior.
func readClaudeAccessToken(home, configDir string, lookupKeychain func(string) (string, bool)) string {
	// The login Keychain holds the CLI's *default* account; it is machine-wide
	// and cannot name a numbered one. Claude Code itself only consults it when
	// CLAUDE_CONFIG_DIR is unset, and reads `<dir>/.credentials.json` when it
	// is set (its credential resolution guards the Keychain read with
	// `if (!CLAUDE_CONFIG_DIR)`). Mirroring that split is what keeps a bound
	// agent from being handed the default seat's token, which is the exact
	// wrong-seat reading DENE-715 exists to remove.
	bound := strings.TrimSpace(configDir) != "" || strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")) != ""
	if !bound && lookupKeychain != nil {
		if raw, ok := lookupKeychain(claudeKeychainService); ok {
			if token := parseClaudeCredentialsJSON(raw); token != "" {
				return token
			}
		}
	}
	path := filepath.Join(claudeConfigDir(home, configDir), ".credentials.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return parseClaudeCredentialsJSON(string(raw))
}

func parseClaudeCredentialsJSON(content string) string {
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return ""
	}
	entry, ok := parsed["claudeAiOauth"]
	if !ok {
		entry, ok = parsed["claude.ai_oauth"]
	}
	if !ok {
		return ""
	}
	var oauth struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(entry, &oauth); err != nil {
		return ""
	}
	return strings.TrimSpace(oauth.AccessToken)
}

func readCodexAccessToken(home string, lookupKeychain func(string) (string, bool)) (token, accountID string) {
	if lookupKeychain != nil {
		if raw, ok := lookupKeychain(codexKeychainService); ok {
			token, accountID = parseCodexCredentialsJSON(raw)
			if token != "" {
				return token, accountID
			}
		}
	}
	path := filepath.Join(codexHomeDir(home), "auth.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	return parseCodexCredentialsJSON(string(raw))
}

func parseCodexCredentialsJSON(content string) (token, accountID string) {
	var parsed struct {
		AuthMode string `json:"auth_mode"`
		Tokens   *struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return "", ""
	}
	if parsed.AuthMode != "" && parsed.AuthMode != "chatgpt" {
		return "", ""
	}
	if parsed.Tokens == nil {
		return "", ""
	}
	return strings.TrimSpace(parsed.Tokens.AccessToken), strings.TrimSpace(parsed.Tokens.AccountID)
}

// claudeConfigDir resolves the directory Claude Code keeps this account's
// credentials in. configDir is the account the caller knows the CLI will run
// against — the value its task environment carries — and it wins over the
// daemon process's own environment, which says nothing about which seat a
// given agent was switched to (DENE-715). Both are empty for a machine with
// no binding at all, where the CLI's own directory is the answer.
func claudeConfigDir(home, configDir string) string {
	if dir := strings.TrimSpace(configDir); dir != "" {
		return dir
	}
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return dir
	}
	return filepath.Join(homeDir(home), ".claude")
}

func codexHomeDir(home string) string {
	if dir := strings.TrimSpace(os.Getenv("CODEX_HOME")); dir != "" {
		return dir
	}
	return filepath.Join(homeDir(home), ".codex")
}

func homeDir(home string) string {
	if strings.TrimSpace(home) != "" {
		return home
	}
	if dir, err := os.UserHomeDir(); err == nil {
		return dir
	}
	return ""
}

func readMacKeychainPassword(service string) (string, bool) {
	if runtime.GOOS != "darwin" || service == "" {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "security", "find-generic-password", "-s", service, "-w")
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return "", false
	}
	return raw, true
}
