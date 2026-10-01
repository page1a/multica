package handler

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/githubapp"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func clearGitHubAppEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GITHUB_APP_SLUG", "")
	t.Setenv("GITHUB_WEBHOOK_SECRET", "")
	t.Setenv("GITHUB_APP_ID", "")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", "")
	t.Setenv("JWT_SECRET", "github-app-test-secret")
	githubapp.Reset()
	t.Cleanup(githubapp.Reset)
}

func withGitHubRole(req *http.Request, role, workspaceID, userID string) *http.Request {
	member := db.Member{
		Role:        role,
		UserID:      parseUUID(userID),
		WorkspaceID: parseUUID(workspaceID),
	}
	return req.WithContext(middleware.SetMemberContext(req.Context(), workspaceID, member))
}

func TestGitHubAppGuards(t *testing.T) {
	clearGitHubAppEnv(t)
	box, err := githubapp.NewSecretBox("github-app-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{
		cfg:              Config{PublicURL: "https://api.example.test", AppURL: "https://app.example.test"},
		GitHubAppSecrets: box,
	}
	ws := "11111111-1111-4111-8111-111111111111"
	user := "22222222-2222-4222-8222-222222222222"

	begin := func(role string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/github/app", strings.NewReader(`{"org":"acme"}`))
		req = withGitHubRole(req, role, ws, user)
		req = withURLParam(req, "id", ws)
		rec := httptest.NewRecorder()
		h.BeginGitHubApp(rec, req)
		return rec
	}

	if rec := begin("member"); rec.Code != http.StatusForbidden {
		t.Fatalf("non-owner begin = %d, want 403", rec.Code)
	}
	// The owner's success path mints a stored launch token; it is covered by
	// TestGitHubAppLaunchLinkIsSingleUse against a real database.

	t.Setenv("GITHUB_APP_SLUG", "from-env")
	t.Setenv("GITHUB_WEBHOOK_SECRET", "env-hook")
	if rec := begin("owner"); rec.Code != http.StatusConflict {
		t.Fatalf("env begin = %d, want 409", rec.Code)
	}
	get := httptest.NewRequest(http.MethodGet, "/github/app", nil)
	get = withGitHubRole(get, "member", ws, user)
	getRec := httptest.NewRecorder()
	h.GetGitHubApp(getRec, get)
	var status githubAppStatusResponse
	if err := json.Unmarshal(getRec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Source != githubapp.SourceEnv || !status.ReadOnly || status.CanCreate {
		t.Fatalf("env status = %#v", status)
	}

	t.Setenv("GITHUB_APP_SLUG", "")
	t.Setenv("GITHUB_WEBHOOK_SECRET", "")
	githubapp.Store(githubapp.Creds{Slug: "from-db", WebhookSecret: "db-hook", Name: "From DB"})
	if rec := begin("owner"); rec.Code != http.StatusConflict {
		t.Fatalf("database begin = %d, want 409", rec.Code)
	}

	cb := httptest.NewRequest(http.MethodGet, "/api/github/app/callback?state=not-a-token", nil)
	cbRec := httptest.NewRecorder()
	h.GitHubAppCallback(cbRec, cb)
	if cbRec.Code != http.StatusFound {
		t.Fatalf("bad state = %d", cbRec.Code)
	}
	loc := cbRec.Header().Get("Location")
	if loc != "https://app.example.test/settings?tab=git-connections&github_app_error=expired" {
		t.Fatalf("bad state redirect = %s", loc)
	}
}

func TestGitHubAppCallbackAndInstallationTaken(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("no database")
	}
	clearGitHubAppEnv(t)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `DELETE FROM github_app_credential`); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `DELETE FROM github_installation WHERE installation_id IN (900001, 900002)`); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `DELETE FROM workspace WHERE slug = 'gh-app-taken'`); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `DELETE FROM member WHERE user_id IN (SELECT id FROM "user" WHERE email = 'gh-app-member@multica.ai')`); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `DELETE FROM "user" WHERE email = 'gh-app-member@multica.ai'`); err != nil {
		t.Fatal(err)
	}

	box, err := githubapp.NewSecretBox("github-app-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	prevBox := testHandler.GitHubAppSecrets
	prevPublic := testHandler.cfg.PublicURL
	prevApp := testHandler.cfg.AppURL
	testHandler.GitHubAppSecrets = box
	testHandler.cfg.PublicURL = "https://api.example.test"
	testHandler.cfg.AppURL = "https://app.example.test"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app-manifests/good/conversions":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 42, "slug": "multica-test-app", "name": "Multica Test",
				"html_url":       "https://github.com/apps/multica-test-app",
				"client_id":      "cid",
				"client_secret":  "csecret",
				"webhook_secret": "hooksecret",
				"pem":            "-----BEGIN PRIVATE KEY-----\nTEST\n-----END PRIVATE KEY-----",
				"owner":          map[string]string{"login": "octocat", "type": "User"},
			})
		case "/app-manifests/used/conversions":
			w.WriteHeader(http.StatusNotFound)
		case "/app-manifests/bad/conversions":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	oldBase := githubAPIBase
	oldClient := githubAppHTTP
	githubAPIBase = srv.URL
	githubAppHTTP = srv.Client()
	t.Cleanup(func() {
		githubAPIBase = oldBase
		githubAppHTTP = oldClient
		srv.Close()
		testHandler.GitHubAppSecrets = prevBox
		testHandler.cfg.PublicURL = prevPublic
		testHandler.cfg.AppURL = prevApp
		githubapp.Reset()
		_, _ = testPool.Exec(context.Background(), `DELETE FROM github_app_credential`)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM github_installation WHERE installation_id IN (900001, 900002)`)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM member WHERE user_id IN (SELECT id FROM "user" WHERE email = 'gh-app-member@multica.ai')`)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE email = 'gh-app-member@multica.ai'`)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE slug = 'gh-app-taken'`)
	})

	var memberUser string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email) VALUES ('Not Owner', 'gh-app-member@multica.ai') RETURNING id
	`).Scan(&memberUser); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'member')
	`, testWorkspaceID, memberUser); err != nil {
		t.Fatal(err)
	}

	callback := func(userID, code string) string {
		t.Helper()
		state, err := githubapp.SignState("github-app-test-secret", testWorkspaceID, userID, "", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/github/app/callback?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(state), nil)
		rec := httptest.NewRecorder()
		testHandler.GitHubAppCallback(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("callback %s = %d", code, rec.Code)
		}
		return rec.Header().Get("Location")
	}

	if loc := callback(memberUser, "good"); !strings.Contains(loc, "github_app_error=not_owner") {
		t.Fatalf("non-owner callback = %s", loc)
	}
	if loc := callback(testUserID, "used"); !strings.Contains(loc, "github_app_error=expired") {
		t.Fatalf("expired callback = %s", loc)
	}
	if loc := callback(testUserID, "bad"); !strings.Contains(loc, "github_app_error=failed") {
		t.Fatalf("failed callback = %s", loc)
	}
	loc := callback(testUserID, "good")
	if !strings.Contains(loc, "https://github.com/apps/multica-test-app/installations/new?state=") {
		t.Fatalf("success callback = %s", loc)
	}
	if got := githubapp.Current(); got.Slug != "multica-test-app" || got.WebhookSecret != "hooksecret" || !got.InstallReady() {
		t.Fatalf("hot load = %#v", got)
	}
	dup := callback(testUserID, "good")
	if !strings.Contains(dup, "github_app_error=duplicate") {
		t.Fatalf("duplicate callback = %s", dup)
	}
	if !strings.Contains(dup, "/"+handlerTestWorkspaceSlug+"/settings?tab=git-connections") {
		t.Fatalf("duplicate landing = %s", dup)
	}
	githubapp.Reset()
	dup = callback(testUserID, "good")
	if !strings.Contains(dup, "github_app_error=duplicate") {
		t.Fatalf("stored duplicate callback = %s", dup)
	}
	if err := testHandler.LoadGitHubAppCredential(ctx); err != nil {
		t.Fatal(err)
	}
	if githubapp.Current().WebhookSecret != "hooksecret" {
		t.Fatal("reload did not restore the webhook secret")
	}

	t.Setenv("GITHUB_APP_SLUG", "from-env")
	t.Setenv("GITHUB_WEBHOOK_SECRET", "env-hook")
	req := httptest.NewRequest(http.MethodPost, "/github/app", strings.NewReader(`{}`))
	req = withGitHubRole(req, "owner", testWorkspaceID, testUserID)
	req = withURLParam(req, "id", testWorkspaceID)
	rec := httptest.NewRecorder()
	testHandler.BeginGitHubApp(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("env wins begin = %d body %s", rec.Code, rec.Body.String())
	}
	t.Setenv("GITHUB_APP_SLUG", "")
	t.Setenv("GITHUB_WEBHOOK_SECRET", "")

	if _, err := testHandler.Queries.CreateGitHubInstallation(ctx, db.CreateGitHubInstallationParams{
		WorkspaceID:    parseUUID(testWorkspaceID),
		InstallationID: 900001,
		AccountLogin:   "personal",
		AccountType:    "User",
	}); err != nil {
		t.Fatal(err)
	}
	var otherID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ('Other', 'gh-app-taken', '', 'GHT') RETURNING id
	`).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')
	`, otherID, testUserID); err != nil {
		t.Fatal(err)
	}
	installState, err := signState(otherID)
	if err != nil {
		t.Fatal(err)
	}
	takenReq := httptest.NewRequest(http.MethodGet, "/api/github/setup?installation_id=900001&state="+url.QueryEscape(installState), nil)
	takenRec := httptest.NewRecorder()
	testHandler.GitHubSetupCallback(takenRec, takenReq)
	takenLoc := takenRec.Header().Get("Location")
	if !strings.Contains(takenLoc, "/gh-app-taken/settings?tab=git-connections&github_error=installation_taken") {
		t.Fatalf("taken redirect = %s", takenLoc)
	}

	if _, err := testHandler.Queries.CreateGitHubInstallation(ctx, db.CreateGitHubInstallationParams{
		WorkspaceID:    parseUUID(testWorkspaceID),
		InstallationID: 900002,
		AccountLogin:   "acme-org",
		AccountType:    "Organization",
	}); err != nil {
		t.Fatal(err)
	}
	listReq := httptest.NewRequest(http.MethodGet, "/github/installations", nil)
	listReq = withGitHubRole(listReq, "owner", testWorkspaceID, testUserID)
	listReq = withURLParam(listReq, "id", testWorkspaceID)
	listRec := httptest.NewRecorder()
	testHandler.ListGitHubInstallations(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list = %d %s", listRec.Code, listRec.Body.String())
	}
	var listed struct {
		Installations []struct {
			AccountLogin string `json:"account_login"`
		} `json:"installations"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, row := range listed.Installations {
		seen[row.AccountLogin] = true
	}
	if !seen["personal"] || !seen["acme-org"] {
		t.Fatalf("installations = %#v", listed.Installations)
	}
}

func TestGitHubAppLaunchLinkIsSingleUse(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("no database")
	}
	clearGitHubAppEnv(t)
	ctx := context.Background()
	box, err := githubapp.NewSecretBox("github-app-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	prevBox := testHandler.GitHubAppSecrets
	prevPublic := testHandler.cfg.PublicURL
	prevApp := testHandler.cfg.AppURL
	testHandler.GitHubAppSecrets = box
	testHandler.cfg.PublicURL = "https://api.example.test"
	testHandler.cfg.AppURL = "https://app.example.test"
	t.Cleanup(func() {
		testHandler.GitHubAppSecrets = prevBox
		testHandler.cfg.PublicURL = prevPublic
		testHandler.cfg.AppURL = prevApp
		_, _ = testPool.Exec(context.Background(), `DELETE FROM github_app_launch_token`)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM member WHERE user_id IN (SELECT id FROM "user" WHERE email = 'gh-launch-member@multica.ai')`)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE email = 'gh-launch-member@multica.ai'`)
	})
	if _, err := testPool.Exec(ctx, `DELETE FROM github_app_credential`); err != nil {
		t.Fatal(err)
	}

	begin := func(role, userID string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/github/app", strings.NewReader(`{"org":"acme"}`))
		req = withGitHubRole(req, role, testWorkspaceID, userID)
		req = withURLParam(req, "id", testWorkspaceID)
		rec := httptest.NewRecorder()
		testHandler.BeginGitHubApp(rec, req)
		return rec
	}
	launchURL := func(rec *httptest.ResponseRecorder) string {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("begin = %d body %s", rec.Code, rec.Body.String())
		}
		var setup githubAppSetupResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &setup); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(setup.ActionURL, "https://github.com/organizations/acme/settings/apps/new?state=") {
			t.Fatalf("action = %s", setup.ActionURL)
		}
		if !strings.HasPrefix(setup.LaunchURL, "https://api.example.test/api/github/app/launch?token=") {
			t.Fatalf("launch = %s", setup.LaunchURL)
		}
		if strings.Contains(setup.LaunchURL, "state=") {
			t.Fatalf("launch link must not carry the signed state: %s", setup.LaunchURL)
		}
		return setup.LaunchURL
	}
	open := func(link string) *httptest.ResponseRecorder {
		t.Helper()
		u, err := url.Parse(link)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, u.RequestURI(), nil)
		rec := httptest.NewRecorder()
		// Through the site-wide CSP middleware, as the router mounts it.
		middleware.ContentSecurityPolicy(http.HandlerFunc(testHandler.LaunchGitHubApp)).ServeHTTP(rec, req)
		return rec
	}

	link := launchURL(begin("owner", testUserID))
	first := open(link)
	if first.Code != http.StatusOK {
		t.Fatalf("first open = %d body %s", first.Code, first.Body.String())
	}
	if !strings.Contains(first.Body.String(), `action="https://github.com/organizations/acme/settings/apps/new?state=`) {
		t.Fatalf("first open page = %s", first.Body.String())
	}
	// The success page replaces the site-wide CSP: one header, allowing the
	// auto-submit script by hash and the form post to this manifest's URL only.
	csp := first.Header().Values("Content-Security-Policy")
	if len(csp) != 1 {
		t.Fatalf("csp headers = %q", csp)
	}
	directives := map[string]string{}
	for _, d := range strings.Split(csp[0], ";") {
		name, value, _ := strings.Cut(strings.TrimSpace(d), " ")
		directives[name] = value
	}
	if got := directives["form-action"]; got != "https://github.com/organizations/acme/settings/apps/new" {
		t.Fatalf("form-action = %q (csp %s)", got, csp[0])
	}
	sum := sha256.Sum256([]byte(githubAppLaunchSubmitScript))
	scriptHash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	if got := directives["script-src"]; got != scriptHash {
		t.Fatalf("script-src = %q, want %s", got, scriptHash)
	}
	if !strings.Contains(first.Body.String(), "<script>"+githubAppLaunchSubmitScript+"</script>") {
		t.Fatalf("page script does not match the allowed hash: %s", first.Body.String())
	}
	if strings.Contains(csp[0], "unsafe-inline") {
		t.Fatalf("csp must not allow unsafe-inline: %s", csp[0])
	}
	second := open(link)
	if second.Code != http.StatusGone || !strings.Contains(second.Body.String(), "不能重复使用") {
		t.Fatalf("reuse = %d body %s", second.Code, second.Body.String())
	}
	if ct := second.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("reuse content type = %s", ct)
	}
	if got := second.Header().Get("Content-Security-Policy"); !strings.Contains(got, "form-action 'self'") {
		t.Fatalf("error page must keep the site-wide csp: %s", got)
	}

	expired := launchURL(begin("owner", testUserID))
	token := strings.TrimPrefix(expired, "https://api.example.test/api/github/app/launch?token=")
	if _, err := testPool.Exec(ctx, `UPDATE github_app_launch_token SET expires_at = now() - interval '1 minute' WHERE token_hash = $1`, githubAppLaunchTokenHash(token)); err != nil {
		t.Fatal(err)
	}
	if rec := open(expired); rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), "已过期") {
		t.Fatalf("expired = %d body %s", rec.Code, rec.Body.String())
	}
	if rec := open("https://api.example.test/api/github/app/launch?token=nope"); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "无效") {
		t.Fatalf("unknown = %d body %s", rec.Code, rec.Body.String())
	}

	// A state minted for someone who is not the owner (owner demoted after
	// minting, for example) is refused when the link is opened.
	var memberUser string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email) VALUES ('Launch Member', 'gh-launch-member@multica.ai') RETURNING id
	`).Scan(&memberUser); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'member')`, testWorkspaceID, memberUser); err != nil {
		t.Fatal(err)
	}
	if rec := begin("member", memberUser); rec.Code != http.StatusForbidden {
		t.Fatalf("member begin = %d, want 403", rec.Code)
	}
	state, err := githubapp.SignState("github-app-test-secret", testWorkspaceID, memberUser, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	memberToken, err := testHandler.mintGitHubAppLaunchToken(ctx, state, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rec := open("https://api.example.test/api/github/app/launch?token=" + memberToken); rec.Code != http.StatusForbidden {
		t.Fatalf("member launch = %d body %s", rec.Code, rec.Body.String())
	}
}
