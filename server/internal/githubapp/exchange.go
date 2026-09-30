package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/multica-ai/multica/server/internal/util/secretbox"
)

// ErrCodeExpired is a manifest code GitHub rejected as missing, used, or older
// than one hour. GitHub answers that with 404.
var ErrCodeExpired = errors.New("github app manifest code expired")

// ErrExchangeFailed is any other failure talking to GitHub.
var ErrExchangeFailed = errors.New("github app manifest exchange failed")

// Conversion is the App GitHub returns for a manifest code.
// PEM, WebhookSecret and ClientSecret are secrets.
type Conversion struct {
	AppID         int64
	Slug          string
	Name          string
	HTMLURL       string
	ManageURL     string
	ClientID      string
	ClientSecret  string
	WebhookSecret string
	PEM           string
}

type conversionBody struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	HTMLURL       string `json:"html_url"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	WebhookSecret string `json:"webhook_secret"`
	PEM           string `json:"pem"`
	Owner         struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"owner"`
}

// Exchange trades a one-hour manifest code for the App credentials.
// apiBase is GitHub's API root, overridable in tests.
func Exchange(ctx context.Context, client *http.Client, apiBase, code string) (Conversion, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return Conversion{}, ErrCodeExpired
	}
	if client == nil {
		client = http.DefaultClient
	}
	endpoint := strings.TrimRight(apiBase, "/") + "/app-manifests/" + url.PathEscape(code) + "/conversions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return Conversion{}, ErrExchangeFailed
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return Conversion{}, fmt.Errorf("%w: %s", ErrExchangeFailed, "request failed")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Conversion{}, ErrExchangeFailed
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
	case http.StatusNotFound, http.StatusGone:
		return Conversion{}, ErrCodeExpired
	default:
		return Conversion{}, ErrExchangeFailed
	}
	var parsed conversionBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Conversion{}, ErrExchangeFailed
	}
	if parsed.ID == 0 || parsed.Slug == "" || parsed.PEM == "" || parsed.WebhookSecret == "" || parsed.ClientSecret == "" {
		return Conversion{}, ErrExchangeFailed
	}
	htmlURL := parsed.HTMLURL
	if htmlURL == "" {
		htmlURL = "https://github.com/apps/" + parsed.Slug
	}
	return Conversion{
		AppID:         parsed.ID,
		Slug:          parsed.Slug,
		Name:          parsed.Name,
		HTMLURL:       htmlURL,
		ManageURL:     ManageURL(parsed.Owner.Login, parsed.Owner.Type, parsed.Slug),
		ClientID:      parsed.ClientID,
		ClientSecret:  parsed.ClientSecret,
		WebhookSecret: parsed.WebhookSecret,
		PEM:           parsed.PEM,
	}, nil
}

// SealSecrets encrypts the three App secrets with the deployment box.
func SealSecrets(box *secretbox.Box, pem, webhook, clientSecret string) (priv, hook, client []byte, err error) {
	if box == nil {
		return nil, nil, nil, errors.New("github app secretbox is not configured")
	}
	priv, err = box.Seal([]byte(pem))
	if err != nil {
		return nil, nil, nil, err
	}
	hook, err = box.Seal([]byte(webhook))
	if err != nil {
		return nil, nil, nil, err
	}
	client, err = box.Seal([]byte(clientSecret))
	if err != nil {
		return nil, nil, nil, err
	}
	return priv, hook, client, nil
}

// OpenSecrets reverses SealSecrets. A tampered or wrong-key ciphertext errors.
func OpenSecrets(box *secretbox.Box, priv, hook, client []byte) (pem, webhook, clientSecret string, err error) {
	if box == nil {
		return "", "", "", errors.New("github app secretbox is not configured")
	}
	raw, err := box.Open(priv)
	if err != nil {
		return "", "", "", err
	}
	pem = string(raw)
	raw, err = box.Open(hook)
	if err != nil {
		return "", "", "", err
	}
	webhook = string(raw)
	raw, err = box.Open(client)
	if err != nil {
		return "", "", "", err
	}
	return pem, webhook, string(raw), nil
}
