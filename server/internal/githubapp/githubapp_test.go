package githubapp

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/util/secretbox"
)

func TestBuildManifestMatchesOnboarding(t *testing.T) {
	got, err := Build("https://ai.example/", "https://app.example", "state-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.ActionURL != "https://github.com/settings/apps/new" {
		t.Fatalf("action = %s", got.ActionURL)
	}
	if got.Fields["public"] != true {
		t.Fatalf("public = %#v", got.Fields["public"])
	}
	hook, _ := got.Fields["hook_attributes"].(map[string]any)
	if hook["url"] != "https://ai.example/api/webhooks/github" {
		t.Fatalf("webhook = %#v", hook)
	}
	if got.Fields["setup_url"] != "https://ai.example/api/github/setup" || got.Fields["setup_on_update"] != true {
		t.Fatalf("setup = %#v %#v", got.Fields["setup_url"], got.Fields["setup_on_update"])
	}
	redirect, _ := got.Fields["redirect_url"].(string)
	if !strings.Contains(redirect, "https://ai.example/api/github/app/callback?state=") {
		t.Fatalf("redirect = %s", redirect)
	}
	perms, _ := got.Fields["default_permissions"].(map[string]string)
	for _, key := range []string{"metadata", "contents", "pull_requests", "checks", "statuses"} {
		if perms[key] != "read" {
			t.Fatalf("permission %s = %q", key, perms[key])
		}
	}
	events, _ := got.Fields["default_events"].([]string)
	if strings.Join(events, ",") != "pull_request,check_suite,check_run,status" {
		t.Fatalf("events = %#v", events)
	}
}

func TestBuildOrgActionAndRejectsBadOrg(t *testing.T) {
	got, err := Build("https://ai.example", "", "s", "Acme-Org")
	if err != nil {
		t.Fatal(err)
	}
	if got.ActionURL != "https://github.com/organizations/Acme-Org/settings/apps/new" {
		t.Fatalf("action = %s", got.ActionURL)
	}
	if _, err := Build("https://ai.example", "", "s", "bad org"); !errors.Is(err, ErrInvalidOrg) {
		t.Fatalf("bad org err = %v", err)
	}
	if _, err := Build("", "", "s", ""); err == nil {
		t.Fatal("empty public url should fail")
	}
}

func TestStateRoundTripAndExpiry(t *testing.T) {
	now := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	token, err := SignState("jwt-secret", "ws-1", "user-1", "acme", now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyState("jwt-secret", token, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkspaceID != "ws-1" || got.UserID != "user-1" || got.Org != "acme" {
		t.Fatalf("state = %#v", got)
	}
	if _, err := VerifyState("other-secret", token, now); !errors.Is(err, ErrBadState) {
		t.Fatalf("wrong secret err = %v", err)
	}
	if _, err := VerifyState("jwt-secret", token, now.Add(stateTTL+time.Second)); !errors.Is(err, ErrBadState) {
		t.Fatalf("expired err = %v", err)
	}
}

func TestExchangeSuccessFailureExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.Contains(r.URL.Path, "/conversions") {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		switch {
		case strings.Contains(r.URL.Path, "/app-manifests/good/"):
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 42, "slug": "multica-app", "name": "Multica",
				"html_url":  "https://github.com/apps/multica-app",
				"client_id": "cid", "client_secret": "csecret",
				"webhook_secret": "wsecret", "pem": "-----BEGIN KEY-----\nabc\n-----END KEY-----",
				"owner": map[string]string{"login": "acme", "type": "Organization"},
			})
		case strings.Contains(r.URL.Path, "/app-manifests/old/"):
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)

	got, err := Exchange(context.Background(), srv.Client(), srv.URL, "good")
	if err != nil {
		t.Fatal(err)
	}
	if got.AppID != 42 || got.Slug != "multica-app" || got.WebhookSecret != "wsecret" || got.PEM == "" {
		t.Fatalf("conversion = %#v", got)
	}
	if got.ManageURL != "https://github.com/organizations/acme/settings/apps/multica-app" {
		t.Fatalf("manage = %s", got.ManageURL)
	}

	if _, err := Exchange(context.Background(), srv.Client(), srv.URL, "old"); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("expired err = %v", err)
	}
	if _, err := Exchange(context.Background(), srv.Client(), srv.URL, "bad"); !errors.Is(err, ErrExchangeFailed) {
		t.Fatalf("failure err = %v", err)
	}
}

func TestEnvWinsOverDatabase(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	Store(Creds{Slug: "from-db", WebhookSecret: "db-secret", AppID: "9", PrivateKeyPEM: "db-pem", Name: "DB App"})
	if got := Current(); got.Source != SourceDatabase || got.Slug != "from-db" || got.ReadOnly {
		t.Fatalf("database current = %#v", got)
	}
	t.Setenv("GITHUB_APP_SLUG", "from-env")
	t.Setenv("GITHUB_WEBHOOK_SECRET", "env-secret")
	got := Current()
	if got.Source != SourceEnv || got.Slug != "from-env" || got.WebhookSecret != "env-secret" || !got.ReadOnly {
		t.Fatalf("env should win, got %#v", got)
	}
	if got.PrivateKeyPEM != "" {
		t.Fatal("partial env must not borrow the database private key")
	}
}

func TestSealRoundTrip(t *testing.T) {
	key := make([]byte, secretbox.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	box, err := secretbox.New(key)
	if err != nil {
		t.Fatal(err)
	}
	priv, hook, client, err := SealSecrets(box, "pem", "hook", "client")
	if err != nil {
		t.Fatal(err)
	}
	gotPEM, gotHook, gotClient, err := OpenSecrets(box, priv, hook, client)
	if err != nil {
		t.Fatal(err)
	}
	if gotPEM != "pem" || gotHook != "hook" || gotClient != "client" {
		t.Fatalf("opened %q %q %q", gotPEM, gotHook, gotClient)
	}
	other, err := NewSecretBox("different-deployment")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := OpenSecrets(other, priv, hook, client); err == nil {
		t.Fatal("another deployment key opened the ciphertext")
	}
}
