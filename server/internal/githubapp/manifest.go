package githubapp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	stateVersion = "v1"
	stateTTL     = time.Hour
)

// ErrInvalidOrg is an organization login GitHub would not accept.
var ErrInvalidOrg = errors.New("invalid github organization login")

// ErrBadState is a missing, tampered, or expired manifest state.
var ErrBadState = errors.New("invalid github app manifest state")

// Manifest is the GitHub App manifest and the form action that creates it.
type Manifest struct {
	ActionURL string
	Fields    map[string]any
}

// Build matches the self-host manifest: public install, read-only permissions,
// setup and webhook URLs on this instance. state rides on the form action, not
// redirect_url: GitHub echoes it back to the callback, while a redirect_url
// carrying the signed state is too long for GitHub to accept.
func Build(publicAPI, appURL, state, org string) (Manifest, error) {
	api := strings.TrimRight(strings.TrimSpace(publicAPI), "/")
	if api == "" {
		return Manifest{}, errors.New("public api url is not configured")
	}
	home := strings.TrimRight(strings.TrimSpace(appURL), "/")
	if home == "" {
		home = api
	}
	org = strings.TrimSpace(org)
	if org != "" && !validOrgLogin(org) {
		return Manifest{}, ErrInvalidOrg
	}
	fields := map[string]any{
		"name":        appName(api),
		"url":         home,
		"description": "Connect GitHub repositories to Multica.",
		"public":      true,
		"hook_attributes": map[string]any{
			"url":    api + "/api/webhooks/github",
			"active": true,
		},
		"redirect_url":    api + "/api/github/app/callback",
		"setup_url":       api + "/api/github/setup",
		"setup_on_update": true,
		"default_permissions": map[string]string{
			"metadata":      "read",
			"contents":      "read",
			"pull_requests": "read",
			"checks":        "read",
			"statuses":      "read",
		},
		"default_events": []string{"pull_request", "pull_request_review", "check_suite", "check_run", "status"},
	}
	action := "https://github.com/settings/apps/new"
	if org != "" {
		action = "https://github.com/organizations/" + url.PathEscape(org) + "/settings/apps/new"
	}
	return Manifest{ActionURL: action + "?state=" + url.QueryEscape(state), Fields: fields}, nil
}

func appName(publicAPI string) string {
	host := publicAPI
	if u, err := url.Parse(publicAPI); err == nil && u.Host != "" {
		host = u.Host
	}
	name := "Multica (" + host + ")"
	if len(name) > 100 {
		return name[:100]
	}
	return name
}

func validOrgLogin(org string) bool {
	if len(org) < 1 || len(org) > 39 {
		return false
	}
	for i, r := range org {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i > 0 && i < len(org)-1:
		default:
			return false
		}
	}
	return true
}

// SignState binds a workspace owner to a manifest for stateTTL.
// secret is the deployment JWT secret, not the webhook secret: the App
// does not exist yet when the manifest is signed.
func SignState(secret, workspaceID, userID, org string, now time.Time) (string, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return "", errors.New("jwt secret is not configured")
	}
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(userID) == "" {
		return "", errors.New("workspace and user are required")
	}
	org = strings.TrimSpace(org)
	if org != "" && !validOrgLogin(org) {
		return "", ErrInvalidOrg
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	payload := strings.Join([]string{
		stateVersion,
		workspaceID,
		userID,
		org,
		strconv.FormatInt(now.Add(stateTTL).Unix(), 10),
		hex.EncodeToString(nonce),
	}, "|")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("github-app-manifest\n" + payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sig, nil
}

// State is what VerifyState recovered.
type State struct {
	WorkspaceID string
	UserID      string
	Org         string
}

// VerifyState checks the signature and expiry. now is injected for tests.
func VerifyState(secret, token string, now time.Time) (State, error) {
	secret = strings.TrimSpace(secret)
	token = strings.TrimSpace(token)
	if secret == "" || token == "" {
		return State{}, ErrBadState
	}
	encoded, sig, ok := strings.Cut(token, ".")
	if !ok || encoded == "" || sig == "" {
		return State{}, ErrBadState
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return State{}, ErrBadState
	}
	payload := string(raw)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("github-app-manifest\n" + payload))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return State{}, ErrBadState
	}
	parts := strings.Split(payload, "|")
	if len(parts) != 6 || parts[0] != stateVersion {
		return State{}, ErrBadState
	}
	exp, err := strconv.ParseInt(parts[4], 10, 64)
	if err != nil || now.Unix() > exp {
		return State{}, ErrBadState
	}
	return State{WorkspaceID: parts[1], UserID: parts[2], Org: parts[3]}, nil
}

// ManageURL is the GitHub page where the App owner edits it.
func ManageURL(ownerLogin, ownerType, slug string) string {
	slug = strings.TrimSpace(slug)
	ownerLogin = strings.TrimSpace(ownerLogin)
	if strings.EqualFold(ownerType, "Organization") && ownerLogin != "" && slug != "" {
		return fmt.Sprintf("https://github.com/organizations/%s/settings/apps/%s", url.PathEscape(ownerLogin), url.PathEscape(slug))
	}
	if slug == "" {
		return ""
	}
	return "https://github.com/settings/apps/" + url.PathEscape(slug)
}
