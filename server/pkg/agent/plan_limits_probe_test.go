package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestParseClaudeUsageJSONCapturesFiveHourAndSevenDay(t *testing.T) {
	t.Parallel()

	observed := time.Unix(1_800_000_000, 0)
	got, err := ParseClaudeUsageJSON([]byte(`{
		"five_hour": {"utilization": 12.4, "resets_at": "2027-01-15T04:00:00Z"},
		"seven_day": {"utilization": 41, "resets_at": "2027-01-20T12:00:00Z"},
		"seven_day_opus": {"utilization": 90, "resets_at": "2027-01-20T12:00:00Z"}
	}`), observed)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Provider != "claude" || got.Status != protocol.PlanLimitsStatusAvailable {
		t.Fatalf("snapshot = %+v", got)
	}
	if got.ObservedAt != observed.Unix() {
		t.Fatalf("observed_at = %d", got.ObservedAt)
	}
	if len(got.Windows) != 2 {
		t.Fatalf("windows = %+v", got.Windows)
	}
	if got.Windows[0].Name != "five_hour" || got.Windows[0].UsedPercent == nil || *got.Windows[0].UsedPercent != 12.4 {
		t.Fatalf("5h window = %+v", got.Windows[0])
	}
	if got.Windows[0].WindowMinutes == nil || *got.Windows[0].WindowMinutes != 300 {
		t.Fatalf("5h minutes = %+v", got.Windows[0].WindowMinutes)
	}
	if got.Windows[1].Name != "seven_day" || got.Windows[1].UsedPercent == nil || *got.Windows[1].UsedPercent != 41 {
		t.Fatalf("7d window = %+v", got.Windows[1])
	}
	if got.Windows[1].WindowMinutes == nil || *got.Windows[1].WindowMinutes != 10_080 {
		t.Fatalf("7d minutes = %+v", got.Windows[1].WindowMinutes)
	}
}

func TestParseClaudeUsageJSONMarksExhaustedAt100(t *testing.T) {
	t.Parallel()

	got, err := ParseClaudeUsageJSON([]byte(`{
		"five_hour": {"utilization": 100, "resets_at": "2027-01-15T04:00:00Z"}
	}`), time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Status != protocol.PlanLimitsStatusExhausted {
		t.Fatalf("status = %+v", got)
	}
}

func TestParseCodexUsageJSONMapsWindowSeconds(t *testing.T) {
	t.Parallel()

	got, err := ParseCodexUsageJSON([]byte(`{
		"rate_limit": {
			"primary_window": {"used_percent": 8, "limit_window_seconds": 18000, "reset_at": 1800000900},
			"secondary_window": {"used_percent": 3.5, "limit_window_seconds": 604800, "reset_at": 1800001800}
		}
	}`), time.Unix(20, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Provider != "codex" || len(got.Windows) != 2 {
		t.Fatalf("snapshot = %+v", got)
	}
	if got.Windows[0].Name != "primary" || got.Windows[0].WindowMinutes == nil || *got.Windows[0].WindowMinutes != 300 {
		t.Fatalf("primary = %+v", got.Windows[0])
	}
	if got.Windows[1].Name != "secondary" || got.Windows[1].WindowMinutes == nil || *got.Windows[1].WindowMinutes != 10_080 {
		t.Fatalf("secondary = %+v", got.Windows[1])
	}
	if got.Windows[0].ResetsAt == nil || *got.Windows[0].ResetsAt != 1_800_000_900 {
		t.Fatalf("primary reset = %+v", got.Windows[0].ResetsAt)
	}
}

func TestParseCodexUsageJSONEmptyRateLimit(t *testing.T) {
	t.Parallel()

	got, err := ParseCodexUsageJSON([]byte(`{"rate_limit":{}}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil snapshot, got %+v", got)
	}
}

func TestProbeClaudeReadsCredentialsAndQueriesUsage(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	credDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credDir, ".credentials.json"), []byte(`{
		"claudeAiOauth": {"accessToken": "claude-token", "expiresAt": 1999999999}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.Header.Get("anthropic-beta") != "oauth-2025-04-20" {
			t.Errorf("missing anthropic-beta header")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"five_hour": map[string]any{"utilization": 5, "resets_at": "2027-01-15T04:00:00Z"},
			"seven_day": map[string]any{"utilization": 9, "resets_at": "2027-01-20T12:00:00Z"},
		})
	}))
	t.Cleanup(server.Close)

	probe := PlanQuotaProbe{
		Home:           home,
		Client:         server.Client(),
		ClaudeUsageURL: server.URL,
		LookupKeychain: func(string) (string, bool) { return "", false },
		Now:            func() time.Time { return time.Unix(30, 0) },
	}
	got, err := probe.ProbeClaude(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer claude-token" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if got == nil || len(got.Windows) != 2 || got.Windows[0].UsedPercent == nil || *got.Windows[0].UsedPercent != 5 {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestProbeCodexReadsAuthJSONAndSendsAccountHeader(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	home := t.TempDir()
	credDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "auth.json"), []byte(`{
		"auth_mode": "chatgpt",
		"tokens": {"access_token": "codex-token", "account_id": "acct-1"}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotAuth, gotAccount string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccount = r.Header.Get("ChatGPT-Account-Id")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rate_limit": map[string]any{
				"primary_window":   map[string]any{"used_percent": 22, "limit_window_seconds": 18000, "reset_at": 99},
				"secondary_window": map[string]any{"used_percent": 7, "limit_window_seconds": 604800, "reset_at": 100},
			},
		})
	}))
	t.Cleanup(server.Close)

	probe := PlanQuotaProbe{
		Home:           home,
		Client:         server.Client(),
		CodexUsageURL:  server.URL,
		LookupKeychain: func(string) (string, bool) { return "", false },
		Now:            func() time.Time { return time.Unix(40, 0) },
	}
	got, err := probe.ProbeCodex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer codex-token" || gotAccount != "acct-1" {
		t.Fatalf("headers auth=%q account=%q", gotAuth, gotAccount)
	}
	if got == nil || got.Provider != "codex" || len(got.Windows) != 2 {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestProbeClaudeSkipsMissingCredentials(t *testing.T) {
	t.Parallel()

	probe := PlanQuotaProbe{
		Home:           t.TempDir(),
		LookupKeychain: func(string) (string, bool) { return "", false },
	}
	got, err := probe.ProbeClaude(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil snapshot, got %+v", got)
	}
}

// writeClaudeCredentials creates <dir>/.credentials.json carrying token.
func writeClaudeCredentials(t *testing.T, dir, token string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"claudeAiOauth":{"accessToken":"` + token + `","expiresAt":1999999999}}`
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// claudeKeychainFixture is what the macOS login Keychain holds on a machine
// whose default Claude Code account is signed in. It is machine-wide, so it
// always names the default seat — never a numbered one.
const claudeKeychainFixture = `{"claudeAiOauth":{"accessToken":"keychain-default-token","expiresAt":1999999999}}`

// TestProbeClaudeUsesBoundAccountDir is the DENE-715 regression: an agent bound
// to a numbered account is served by that account's `CLAUDE_CONFIG_DIR`, so the
// probe must read its credentials instead of whatever else the machine has.
func TestProbeClaudeUsesBoundAccountDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	// The daemon's own (default) account is fully signed in, in both places a
	// macOS install could keep it. Neither may win for a bound agent.
	writeClaudeCredentials(t, filepath.Join(home, ".claude"), "default-account-token")
	bound := filepath.Join(home, ".claude-account2")
	writeClaudeCredentials(t, bound, "account2-token")

	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"five_hour": map[string]any{"utilization": 12, "resets_at": "2027-01-15T04:00:00Z"},
		})
	}))
	t.Cleanup(server.Close)

	probe := PlanQuotaProbe{
		Home:            home,
		ClaudeConfigDir: bound,
		Client:          server.Client(),
		ClaudeUsageURL:  server.URL,
		LookupKeychain:  keychainReturning(claudeKeychainFixture),
		Now:             func() time.Time { return time.Unix(50, 0) },
	}
	got, err := probe.ProbeClaude(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer account2-token" {
		t.Fatalf("authorization = %q, want the bound account's token", gotAuth)
	}
	if got == nil || len(got.Windows) != 1 {
		t.Fatalf("snapshot = %+v", got)
	}
}

// keychainReturning is a LookupKeychain stub for the macOS shape of the
// problem: the login Keychain is populated with the daemon user's default
// account.
func keychainReturning(raw string) func(string) (string, bool) {
	return func(string) (string, bool) { return raw, true }
}

// TestProbeClaudeUnboundKeepsDaemonProcessAccount pins the other half of the
// acceptance: an agent with no binding configured must keep reading exactly the
// directory the pre-DENE-715 probe read.
func TestProbeClaudeUnboundKeepsDaemonProcessAccount(t *testing.T) {
	home := t.TempDir()
	writeClaudeCredentials(t, filepath.Join(home, ".claude"), "home-token")
	// The daemon process's own environment is still the fallback when no agent
	// binding is in play.
	daemonDir := filepath.Join(home, "daemon-claude")
	writeClaudeCredentials(t, daemonDir, "daemon-env-token")
	t.Setenv("CLAUDE_CONFIG_DIR", daemonDir)

	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"five_hour": map[string]any{"utilization": 3, "resets_at": "2027-01-15T04:00:00Z"},
		})
	}))
	t.Cleanup(server.Close)

	probe := PlanQuotaProbe{
		Home:           home,
		Client:         server.Client(),
		ClaudeUsageURL: server.URL,
		LookupKeychain: func(string) (string, bool) { return "", false },
		Now:            func() time.Time { return time.Unix(51, 0) },
	}
	if _, err := probe.ProbeClaude(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer daemon-env-token" {
		t.Fatalf("authorization = %q, want the daemon process account", gotAuth)
	}

	// With neither a binding nor a process-level CLAUDE_CONFIG_DIR, the CLI's
	// own directory under home is the account.
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if _, err := probe.ProbeClaude(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer home-token" {
		t.Fatalf("authorization = %q, want <home>/.claude", gotAuth)
	}
}

// TestProbeClaudeDefaultAccountUsesKeychain pins the macOS default path: with
// no CLAUDE_CONFIG_DIR anywhere the CLI runs as its own account, and that
// token lives in the login Keychain on an install that keeps no
// `.credentials.json` on disk. Unbound agents must keep reading it.
func TestProbeClaudeDefaultAccountUsesKeychain(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir() // deliberately no <home>/.claude/.credentials.json

	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"five_hour": map[string]any{"utilization": 7, "resets_at": "2027-01-15T04:00:00Z"},
		})
	}))
	t.Cleanup(server.Close)

	probe := PlanQuotaProbe{
		Home:           home,
		Client:         server.Client(),
		ClaudeUsageURL: server.URL,
		LookupKeychain: keychainReturning(claudeKeychainFixture),
		Now:            func() time.Time { return time.Unix(52, 0) },
	}
	if _, err := probe.ProbeClaude(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer keychain-default-token" {
		t.Fatalf("authorization = %q, want the login Keychain's account", gotAuth)
	}
}

// TestProbeClaudeBoundDirWithoutCredentialsSkipsProbe covers the third case: a
// bound directory that exists but holds no credentials reports no snapshot
// rather than falling back to the default account's windows.
func TestProbeClaudeBoundDirWithoutCredentialsSkipsProbe(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	writeClaudeCredentials(t, filepath.Join(home, ".claude"), "default-account-token")
	bound := filepath.Join(home, ".claude-account2")
	if err := os.MkdirAll(bound, 0o700); err != nil {
		t.Fatal(err)
	}

	// The Keychain is populated with the machine's default account: a bound but
	// unseeded seat must not fall back to it and name the wrong seat's windows.
	probe := PlanQuotaProbe{
		Home:            home,
		ClaudeConfigDir: bound,
		LookupKeychain:  keychainReturning(claudeKeychainFixture),
	}
	got, err := probe.ProbeClaude(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected no snapshot for an unseeded account, got %+v", got)
	}

	// A directory that does not exist at all behaves the same way.
	probe.ClaudeConfigDir = filepath.Join(home, ".claude-account9")
	got, err = probe.ProbeClaude(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected no snapshot for a missing account dir, got %+v", got)
	}

	// A CLAUDE_CONFIG_DIR in the daemon's own environment names an account the
	// same way, so the Keychain does not speak for it either.
	probe.ClaudeConfigDir = ""
	t.Setenv("CLAUDE_CONFIG_DIR", bound)
	got, err = probe.ProbeClaude(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected no snapshot for an unseeded process account, got %+v", got)
	}
}

func TestParseClaudeCredentialsPrefersClaudeAiOauthKey(t *testing.T) {
	t.Parallel()

	token := parseClaudeCredentialsJSON(`{"claude.ai_oauth":{"accessToken":"alt-token"}}`)
	if token != "alt-token" {
		t.Fatalf("token = %q", token)
	}
}

func TestParseCodexCredentialsRejectsAPIKeyMode(t *testing.T) {
	t.Parallel()

	token, account := parseCodexCredentialsJSON(`{"auth_mode":"apikey","tokens":{"access_token":"nope"}}`)
	if token != "" || account != "" {
		t.Fatalf("token=%q account=%q", token, account)
	}
}
