package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

const connectionTestToken = "ghp_TESTTOKEN_SHOULD_NOT_LEAK"

func TestConnectionAddFromGHDoesNotLeakToken(t *testing.T) {
	restore := chdirConnectionTest(t)
	defer restore()
	resetConnectionAddFlags(t)

	var argv []string
	origCmd := connectionTokenCommand
	t.Cleanup(func() { connectionTokenCommand = origCmd })
	connectionTokenCommand = func(name string, args ...string) ([]byte, error) {
		argv = append([]string{name}, args...)
		return []byte(connectionTestToken + "\n"), nil
	}

	var posted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"repos":[{"url":"https://github.com/octocat/hello","provider":"github","mode":"none","can_configure":true}]}`))
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
			t.Errorf("decode body: %v", err)
		}
		_, _ = w.Write([]byte(`{"repo":{"url":"https://github.com/octocat/hello","account_login":"octocat"},"webhook_secret":"whsec"}`))
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "user-token")
	t.Setenv("MULTICA_AGENT_ID", "")

	mustSetFlag(t, "from-gh", "true")
	mustSetFlag(t, "yes", "true")
	mustSetFlag(t, "output", "json")
	var stdout, stderr bytes.Buffer
	connectionAddCmd.SetOut(&stdout)
	connectionAddCmd.SetErr(&stderr)
	connectionAddCmd.SetIn(strings.NewReader(""))

	if err := runConnectionAdd(connectionAddCmd, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Join(argv, " ") != "gh auth token" {
		t.Fatalf("argv = %q", argv)
	}
	if posted["repo_url"] != "https://github.com/octocat/hello" {
		t.Fatalf("repo_url = %#v", posted["repo_url"])
	}
	if posted["agent_yes"] != false {
		t.Fatalf("agent_yes = %#v", posted["agent_yes"])
	}
	if posted["access_token"] != connectionTestToken {
		t.Fatal("token was not posted to the server")
	}
	blob := stdout.String() + stderr.String()
	if strings.Contains(blob, connectionTestToken) {
		t.Fatalf("token leaked into output:\n%s", blob)
	}
	if !strings.Contains(stderr.String(), "https://github.com/octocat/hello") {
		t.Fatalf("scope was not printed: %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "octocat") {
		t.Fatalf("stdout = %s", stdout.String())
	}
}

func TestConnectionAddFromGlabCancelSkipsPost(t *testing.T) {
	restore := chdirConnectionTest(t)
	defer restore()
	resetConnectionAddFlags(t)

	origCmd := connectionTokenCommand
	t.Cleanup(func() { connectionTokenCommand = origCmd })
	connectionTokenCommand = func(name string, args ...string) ([]byte, error) {
		if name != "glab" || strings.Join(args, " ") != "auth token" {
			t.Fatalf("argv = %s %v", name, args)
		}
		return []byte(connectionTestToken), nil
	}
	posted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			posted = true
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusCreated)
			return
		}
		_, _ = w.Write([]byte(`{"repos":[{"url":"https://gitlab.com/octocat/app","provider":"gitlab","mode":"none","can_configure":true}]}`))
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "user-token")
	t.Setenv("MULTICA_AGENT_ID", "")

	mustSetFlag(t, "from-glab", "true")
	mustSetFlag(t, "yes", "false")
	var stdout, stderr bytes.Buffer
	connectionAddCmd.SetOut(&stdout)
	connectionAddCmd.SetErr(&stderr)
	connectionAddCmd.SetIn(strings.NewReader("n\n"))
	err := runConnectionAdd(connectionAddCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("err = %v", err)
	}
	if posted {
		t.Fatal("cancel still posted the token")
	}
	blob := stdout.String() + stderr.String() + err.Error()
	if strings.Contains(blob, connectionTestToken) {
		t.Fatalf("token leaked: %s", blob)
	}
	if !strings.Contains(stderr.String(), "https://gitlab.com/octocat/app") {
		t.Fatalf("scope missing: %s", stderr.String())
	}
}

func TestConnectionAddHelperFailureOmitsToken(t *testing.T) {
	restore := chdirConnectionTest(t)
	defer restore()
	resetConnectionAddFlags(t)
	orig := connectionTokenCommand
	t.Cleanup(func() { connectionTokenCommand = orig })
	connectionTokenCommand = func(string, ...string) ([]byte, error) {
		return []byte(connectionTestToken), os.ErrPermission
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"repos":[{"url":"https://github.com/octocat/hello","provider":"github","mode":"none","can_configure":true}]}`))
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "user-token")
	mustSetFlag(t, "from-gh", "true")
	mustSetFlag(t, "yes", "true")
	var stderr bytes.Buffer
	connectionAddCmd.SetOut(&stderr)
	connectionAddCmd.SetErr(&stderr)
	err := runConnectionAdd(connectionAddCmd, nil)
	if err == nil {
		t.Fatal("expected helper failure")
	}
	if strings.Contains(err.Error(), connectionTestToken) {
		t.Fatalf("error contains token: %v", err)
	}
}

func TestConnectionAddAgentPostsOnlyInitiatorRepo(t *testing.T) {
	restore := chdirConnectionTest(t)
	defer restore()
	resetConnectionAddFlags(t)

	origCmd := connectionTokenCommand
	t.Cleanup(func() { connectionTokenCommand = origCmd })
	connectionTokenCommand = func(name string, args ...string) ([]byte, error) {
		if name != "gh" || strings.Join(args, " ") != "auth token" {
			t.Fatalf("argv = %s %v", name, args)
		}
		return []byte(connectionTestToken + "\n"), nil
	}
	var posted []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"repos":[
				{"url":"https://github.com/acme/app-private","provider":"github","mode":"none","can_configure":true,"agent_eligible":false},
				{"url":"https://github.com/acme/app","provider":"github","mode":"none","can_configure":true,"agent_eligible":true}
			]}`))
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		posted = append(posted, body)
		_, _ = w.Write([]byte(`{"repo":{"url":"https://github.com/acme/app","account_login":"octocat"}}`))
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_AGENT_ID", "agent-1")

	mustSetFlag(t, "from-gh", "true")
	mustSetFlag(t, "yes", "false")
	var stdout, stderr bytes.Buffer
	connectionAddCmd.SetOut(&stdout)
	connectionAddCmd.SetErr(&stderr)
	connectionAddCmd.SetIn(strings.NewReader("y\n"))
	if err := runConnectionAdd(connectionAddCmd, nil); err != nil {
		t.Fatal(err)
	}
	if len(posted) != 1 || posted[0]["repo_url"] != "https://github.com/acme/app" {
		t.Fatalf("posted = %#v", posted)
	}
	blob := stdout.String() + stderr.String()
	if strings.Contains(blob, connectionTestToken) || strings.Contains(blob, "app-private") {
		t.Fatalf("output leaked a token or the other repo:\n%s", blob)
	}
}

func TestConnectionRepoWantedMatchesExactly(t *testing.T) {
	card := "https://github.com/acme/app-private"
	if connectionRepoWanted(card, "acme/app") {
		t.Fatal("owner/name matched a longer repository name")
	}
	if connectionRepoWanted(card, "https://github.com/acme/app") {
		t.Fatal("full URL matched a longer repository name")
	}
	own := "https://github.com/acme/app.git"
	for _, want := range []string{"acme/app", "https://github.com/acme/app", "https://github.com/acme/app.git"} {
		if !connectionRepoWanted(own, want) {
			t.Fatalf("want %q to match %s", want, own)
		}
	}
}

func chdirConnectionTest(t *testing.T) func() {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	return func() { _ = os.Chdir(wd) }
}

func resetConnectionAddFlags(t *testing.T) {
	t.Helper()
	for _, pair := range [][2]string{
		{"from-gh", "false"},
		{"from-glab", "false"},
		{"yes", "false"},
		{"workspace", "false"},
		{"token-file", ""},
		{"provider", ""},
		{"instance-url", ""},
		{"repo", ""},
		{"output", "table"},
	} {
		mustSetFlag(t, pair[0], pair[1])
	}
}

func mustSetFlag(t *testing.T, name, value string) {
	t.Helper()
	if err := connectionAddCmd.Flags().Set(name, value); err != nil {
		t.Fatal(err)
	}
}
