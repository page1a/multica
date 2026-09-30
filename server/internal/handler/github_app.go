package handler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/githubapp"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// githubAppHTTP is the client that exchanges a manifest code. Tests replace it.
var githubAppHTTP = http.DefaultClient

// githubAppLaunchTTL bounds the single-use browser link. The signed state
// inside it lives longer so GitHub's callback still verifies after the
// person spends a while on GitHub's form.
const githubAppLaunchTTL = 10 * time.Minute

type githubAppStatusResponse struct {
	Source      string `json:"source"`
	Configured  bool   `json:"configured"`
	ReadOnly    bool   `json:"read_only"`
	CanCreate   bool   `json:"can_create"`
	BlockReason string `json:"block_reason,omitempty"`
	AppName     string `json:"app_name,omitempty"`
	Slug        string `json:"slug,omitempty"`
	HTMLURL     string `json:"html_url,omitempty"`
	ManageURL   string `json:"manage_url,omitempty"`
}

type githubAppSetupResponse struct {
	ActionURL string         `json:"action_url"`
	Manifest  map[string]any `json:"manifest"`
	LaunchURL string         `json:"launch_url"`
}

// LoadGitHubAppCredential reads the stored App into memory. An empty table
// is the unconfigured state, not an error. Called once at startup; the
// manifest callback updates the same memory on success.
func (h *Handler) LoadGitHubAppCredential(ctx context.Context) error {
	if h == nil || h.Queries == nil || h.GitHubAppSecrets == nil {
		githubapp.Reset()
		return nil
	}
	row, err := h.Queries.GetGitHubAppCredential(ctx)
	if err != nil {
		if isNotFound(err) {
			githubapp.Reset()
			return nil
		}
		return err
	}
	pem, webhook, clientSecret, err := githubapp.OpenSecrets(h.GitHubAppSecrets, row.PrivateKey, row.WebhookSecret, row.ClientSecret)
	if err != nil {
		githubapp.Reset()
		return err
	}
	githubapp.Store(credsFromRow(row, pem, webhook, clientSecret))
	return nil
}

func credsFromRow(row db.GithubAppCredential, pem, webhook, clientSecret string) githubapp.Creds {
	return githubapp.Creds{
		Source:        githubapp.SourceDatabase,
		AppID:         strconv.FormatInt(row.AppID, 10),
		Slug:          row.Slug,
		Name:          row.Name,
		HTMLURL:       row.HtmlUrl,
		ManageURL:     row.ManageUrl,
		ClientID:      row.ClientID,
		PrivateKeyPEM: pem,
		WebhookSecret: webhook,
		ClientSecret:  clientSecret,
	}
}

func (h *Handler) githubAppView(ctx context.Context, role string) githubAppStatusResponse {
	creds := githubapp.Current()
	resp := githubAppStatusResponse{
		Source:     creds.Source,
		Configured: creds.InstallReady(),
		ReadOnly:   creds.ReadOnly,
		AppName:    creds.Name,
		Slug:       creds.Slug,
		HTMLURL:    creds.HTMLURL,
		ManageURL:  creds.ManageURL,
	}
	if creds.Source != githubapp.SourceNone {
		return resp
	}
	switch {
	case role != "owner":
		resp.BlockReason = "not_owner"
	case h.GitHubAppSecrets == nil || strings.TrimSpace(h.deploymentSecret()) == "":
		resp.BlockReason = "secret_unavailable"
	case strings.TrimSpace(h.cfg.PublicURL) == "":
		resp.BlockReason = "public_url_missing"
	default:
		resp.CanCreate = true
	}
	_ = ctx
	return resp
}

func (h *Handler) deploymentSecret() string {
	return strings.TrimSpace(os.Getenv("JWT_SECRET"))
}

// GetGitHubApp (GET /api/workspaces/{id}/github/app) reports which identity
// the server is using. Any member can read it. Secrets stay on the server.
func (h *Handler) GetGitHubApp(w http.ResponseWriter, r *http.Request) {
	member, _ := middleware.MemberFromContext(r.Context())
	writeJSON(w, http.StatusOK, h.githubAppView(r.Context(), member.Role))
}

// BeginGitHubApp (POST /api/workspaces/{id}/github/app) mints a manifest for
// the workspace owner. The browser posts it to GitHub; the launch_url is the
// same form for someone who only has a terminal.
func (h *Handler) BeginGitHubApp(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	if _, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id"); !ok {
		return
	}
	member, _ := middleware.MemberFromContext(r.Context())
	if member.Role != "owner" {
		writeError(w, http.StatusForbidden, "only the workspace owner can create the GitHub App")
		return
	}
	view := h.githubAppView(r.Context(), member.Role)
	if view.Source != githubapp.SourceNone {
		writeError(w, http.StatusConflict, "GitHub App is already configured")
		return
	}
	if !view.CanCreate {
		writeError(w, http.StatusServiceUnavailable, githubAppBlockMessage(view.BlockReason))
		return
	}
	var body struct {
		Org string `json:"org"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
	}
	userID := uuidToString(member.UserID)
	state, err := githubapp.SignState(h.deploymentSecret(), workspaceID, userID, body.Org, time.Now())
	if err != nil {
		if errors.Is(err, githubapp.ErrInvalidOrg) {
			writeError(w, http.StatusBadRequest, "invalid github organization login")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to sign github app state")
		return
	}
	manifest, err := githubapp.Build(h.cfg.PublicURL, h.appOrigin(), state, strings.TrimSpace(body.Org))
	if err != nil {
		if errors.Is(err, githubapp.ErrInvalidOrg) {
			writeError(w, http.StatusBadRequest, "invalid github organization login")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "server public url is not configured")
		return
	}
	token, err := h.mintGitHubAppLaunchToken(r.Context(), state, time.Now())
	if err != nil {
		slog.Error("github app: mint launch token failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to create github app setup link")
		return
	}
	launch := strings.TrimRight(h.cfg.PublicURL, "/") + "/api/github/app/launch?token=" + url.QueryEscape(token)
	writeJSON(w, http.StatusOK, githubAppSetupResponse{
		ActionURL: manifest.ActionURL,
		Manifest:  manifest.Fields,
		LaunchURL: launch,
	})
}

func githubAppBlockMessage(reason string) string {
	switch reason {
	case "secret_unavailable":
		return "server cannot store GitHub App credentials"
	case "public_url_missing":
		return "server public url is not configured"
	default:
		return "only the workspace owner can create the GitHub App"
	}
}

// mintGitHubAppLaunchToken stores state behind a random single-use token.
// Only the token's hash is kept, so the table alone cannot open the page.
func (h *Handler) mintGitHubAppLaunchToken(ctx context.Context, state string, now time.Time) (string, error) {
	if h.Queries == nil {
		return "", errors.New("database unavailable")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	if err := h.Queries.DeleteStaleGitHubAppLaunchTokens(ctx, pgtype.Timestamptz{Time: now.Add(-24 * time.Hour), Valid: true}); err != nil {
		slog.Warn("github app: prune launch tokens failed", "err", err)
	}
	err := h.Queries.InsertGitHubAppLaunchToken(ctx, db.InsertGitHubAppLaunchTokenParams{
		TokenHash: githubAppLaunchTokenHash(token),
		State:     state,
		ExpiresAt: pgtype.Timestamptz{Time: now.Add(githubAppLaunchTTL), Valid: true},
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

func githubAppLaunchTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// consumeGitHubAppLaunchToken spends the token once. The error message is
// what the person sees, so it says why and what to do next.
func (h *Handler) consumeGitHubAppLaunchToken(ctx context.Context, token string, now time.Time) (string, int, string) {
	const retry = "回到 Multica 设置页，重新点「先创建 GitHub App」获取新链接。"
	token = strings.TrimSpace(token)
	if token == "" || h.Queries == nil {
		return "", http.StatusBadRequest, "这个链接无效。" + retry
	}
	hash := githubAppLaunchTokenHash(token)
	state, err := h.Queries.ConsumeGitHubAppLaunchToken(ctx, db.ConsumeGitHubAppLaunchTokenParams{
		TokenHash: hash,
		ExpiresAt: pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err == nil {
		return state, http.StatusOK, ""
	}
	if !isNotFound(err) {
		slog.Error("github app: consume launch token failed", "err", err)
		return "", http.StatusInternalServerError, "暂时无法打开这个链接，请稍后再试。"
	}
	row, err := h.Queries.GetGitHubAppLaunchToken(ctx, hash)
	switch {
	case err != nil:
		return "", http.StatusNotFound, "这个链接无效。" + retry
	case row.UsedAt.Valid:
		return "", http.StatusGone, "这个链接已经打开过一次，不能重复使用。" + retry
	default:
		return "", http.StatusGone, "这个链接已过期（有效期 10 分钟）。" + retry
	}
}

func writeGitHubAppLaunchPage(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`<!DOCTYPE html>
<html lang="zh-Hans"><head><meta charset="utf-8"><title>创建 GitHub App</title></head>
<body>
` + body + `
</body></html>`))
}

// githubAppLaunchSubmitScript posts the manifest form as soon as the page
// loads. The site-wide CSP blocks inline scripts and cross-origin form posts,
// so the success page swaps in a policy that allows exactly this script (by
// hash) and exactly this manifest's GitHub address.
const githubAppLaunchSubmitScript = `document.getElementById("gh").submit()`

var githubAppLaunchSubmitScriptHash = func() string {
	sum := sha256.Sum256([]byte(githubAppLaunchSubmitScript))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}()

func githubAppLaunchCSP(actionURL string) string {
	return "default-src 'none'; " +
		"script-src " + githubAppLaunchSubmitScriptHash + "; " +
		"frame-ancestors 'none'; " +
		"base-uri 'none'; " +
		"form-action " + actionURL
}

func writeGitHubAppLaunchError(w http.ResponseWriter, status int, msg string) {
	writeGitHubAppLaunchPage(w, status, "<p>"+html.EscapeString(msg)+"</p>")
}

// LaunchGitHubApp (GET /api/github/app/launch?token=) is the page the system
// browser opens, from the desktop app or a CLI link. The token works once and
// for githubAppLaunchTTL; the page posts the manifest to GitHub.
func (h *Handler) LaunchGitHubApp(w http.ResponseWriter, r *http.Request) {
	state, status, msg := h.consumeGitHubAppLaunchToken(r.Context(), r.URL.Query().Get("token"), time.Now())
	if msg != "" {
		writeGitHubAppLaunchError(w, status, msg)
		return
	}
	st, err := githubapp.VerifyState(h.deploymentSecret(), state, time.Now())
	if err != nil {
		writeGitHubAppLaunchError(w, http.StatusGone, "这个链接已过期。回到 Multica 设置页，重新点「先创建 GitHub App」获取新链接。")
		return
	}
	if !h.userIsWorkspaceOwner(r.Context(), st.WorkspaceID, st.UserID) {
		writeGitHubAppLaunchError(w, http.StatusForbidden, "只有工作区所有者可以创建 GitHub App。")
		return
	}
	if githubapp.Current().Source != githubapp.SourceNone {
		http.Redirect(w, r, h.githubAppSettingsURL(r.Context(), st.WorkspaceID, "duplicate"), http.StatusFound)
		return
	}
	manifest, err := githubapp.Build(h.cfg.PublicURL, h.appOrigin(), state, st.Org)
	if err != nil {
		writeGitHubAppLaunchError(w, http.StatusServiceUnavailable, "服务器没有配置公网地址，暂时不能创建 GitHub App。")
		return
	}
	raw, err := json.Marshal(manifest.Fields)
	if err != nil {
		writeGitHubAppLaunchError(w, http.StatusInternalServerError, "生成 GitHub App 配置失败，请稍后再试。")
		return
	}
	action, err := url.Parse(manifest.ActionURL)
	if err != nil || action.Scheme != "https" || action.Host == "" {
		writeGitHubAppLaunchError(w, http.StatusInternalServerError, "生成 GitHub App 配置失败，请稍后再试。")
		return
	}
	action.RawQuery, action.Fragment = "", ""
	w.Header().Set("Content-Security-Policy", githubAppLaunchCSP(action.String()))
	writeGitHubAppLaunchPage(w, http.StatusOK, `<p>正在前往 GitHub。若浏览器没有跳转，点下面的按钮，然后在 GitHub 上输入一次密码并创建。</p>
<form id="gh" method="post" action="`+html.EscapeString(manifest.ActionURL)+`">
<input type="hidden" name="manifest" value="`+html.EscapeString(string(raw))+`">
<button type="submit">前往 GitHub</button>
</form>
<script>`+githubAppLaunchSubmitScript+`</script>`)
}

// GitHubAppCallback (GET /api/github/app/callback) exchanges GitHub's code,
// stores the encrypted credentials, and continues into the install page.
func (h *Handler) GitHubAppCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	st, err := githubapp.VerifyState(h.deploymentSecret(), q.Get("state"), time.Now())
	if err != nil {
		h.redirectGitHubAppError(w, r, "", "expired")
		return
	}
	settings := func(code string) {
		h.redirectGitHubAppError(w, r, st.WorkspaceID, code)
	}
	if !h.userIsWorkspaceOwner(r.Context(), st.WorkspaceID, st.UserID) {
		settings("not_owner")
		return
	}
	if githubapp.Current().Source != githubapp.SourceNone {
		settings("duplicate")
		return
	}
	if h.GitHubAppSecrets == nil {
		settings("failed")
		return
	}
	converted, err := githubapp.Exchange(r.Context(), githubAppHTTP, githubAPIBase, q.Get("code"))
	if err != nil {
		if errors.Is(err, githubapp.ErrCodeExpired) {
			settings("expired")
			return
		}
		slog.Warn("github app: manifest exchange failed", "err", err.Error())
		settings("failed")
		return
	}
	priv, hook, clientSecret, err := githubapp.SealSecrets(h.GitHubAppSecrets, converted.PEM, converted.WebhookSecret, converted.ClientSecret)
	if err != nil {
		settings("failed")
		return
	}
	wsID, err := parseStrictUUID(st.WorkspaceID)
	if err != nil {
		settings("failed")
		return
	}
	userID, err := parseStrictUUID(st.UserID)
	if err != nil {
		userID = pgtype.UUID{}
	}
	row, err := h.Queries.InsertGitHubAppCredential(r.Context(), db.InsertGitHubAppCredentialParams{
		AppID:         converted.AppID,
		Slug:          converted.Slug,
		Name:          converted.Name,
		HtmlUrl:       converted.HTMLURL,
		ManageUrl:     converted.ManageURL,
		ClientID:      converted.ClientID,
		PrivateKey:    priv,
		WebhookSecret: hook,
		ClientSecret:  clientSecret,
		CreatedBy:     userID,
		WorkspaceID:   wsID,
	})
	if err != nil {
		if isUniqueViolation(err) {
			settings("duplicate")
			return
		}
		slog.Error("github app: store credential failed", "err", err)
		settings("failed")
		return
	}
	githubapp.Store(credsFromRow(row, converted.PEM, converted.WebhookSecret, converted.ClientSecret))
	installState, err := signStateForReturn(st.WorkspaceID, githubReturnToGitHub)
	if err != nil {
		settings("failed")
		return
	}
	installURL := "https://github.com/apps/" + url.PathEscape(converted.Slug) + "/installations/new?state=" + url.QueryEscape(installState)
	http.Redirect(w, r, installURL, http.StatusFound)
}

func (h *Handler) redirectGitHubAppError(w http.ResponseWriter, r *http.Request, workspaceID, code string) {
	http.Redirect(w, r, h.githubAppSettingsURL(r.Context(), workspaceID, code), http.StatusFound)
}

func (h *Handler) githubAppSettingsURL(ctx context.Context, workspaceID, errorCode string) string {
	base := h.githubSettingsURL(ctx, h.appOrigin(), workspaceID, githubReturnToGitHub)
	if errorCode == "" {
		return base
	}
	return base + "&github_app_error=" + url.QueryEscape(errorCode)
}

func (h *Handler) appOrigin() string {
	if v := strings.TrimSpace(h.cfg.AppURL); v != "" {
		return strings.TrimRight(v, "/")
	}
	if v := strings.TrimSpace(os.Getenv("FRONTEND_ORIGIN")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://localhost:3000"
}

func (h *Handler) userIsWorkspaceOwner(ctx context.Context, workspaceID, userID string) bool {
	if h == nil || h.Queries == nil {
		return false
	}
	ws, err := parseStrictUUID(workspaceID)
	if err != nil {
		return false
	}
	user, err := parseStrictUUID(userID)
	if err != nil {
		return false
	}
	member, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      user,
		WorkspaceID: ws,
	})
	if err != nil {
		return false
	}
	return member.Role == "owner"
}

// githubSettingsURL lands the browser on the connections page. The workspace
// slug is included when we can resolve it; a handler without a database
// (unit tests) keeps the historical /settings path.
func (h *Handler) githubSettingsURL(ctx context.Context, frontend, workspaceID, returnTo string) string {
	tab := returnTo
	if returnTo == githubReturnToGitHub || !isAllowedGitHubReturnTo(returnTo) {
		tab = "git-connections"
	}
	base := strings.TrimRight(frontend, "/")
	path := "/settings"
	if h != nil && h.Queries != nil && workspaceID != "" {
		if id, err := parseStrictUUID(workspaceID); err == nil {
			if ws, err := h.Queries.GetWorkspace(ctx, id); err == nil && ws.Slug != "" {
				path = "/" + url.PathEscape(ws.Slug) + "/settings"
			}
		}
	}
	return base + path + "?tab=" + url.QueryEscape(tab)
}
