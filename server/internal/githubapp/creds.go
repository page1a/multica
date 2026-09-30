// Package githubapp is the deployment's GitHub App identity.
//
// Environment variables win over the database row. Callers read Current on
// every use, so a row written by the manifest callback is visible without a
// restart. Secrets never leave this package as part of a status value.
package githubapp

import (
	"crypto/sha256"
	"os"
	"strings"
	"sync"

	"github.com/multica-ai/multica/server/internal/util/secretbox"
)

const (
	SourceNone     = "none"
	SourceEnv      = "env"
	SourceDatabase = "database"
)

// Creds is the App identity the process is using right now.
// PrivateKeyPEM, WebhookSecret and ClientSecret are plaintext in memory
// and must not be copied into an API response.
type Creds struct {
	Source        string
	AppID         string
	Slug          string
	Name          string
	HTMLURL       string
	ManageURL     string
	ClientID      string
	PrivateKeyPEM string
	WebhookSecret string
	ClientSecret  string
	ReadOnly      bool
}

// InstallReady is the slug + webhook pair the connect flow needs.
func (c Creds) InstallReady() bool {
	return strings.TrimSpace(c.Slug) != "" && strings.TrimSpace(c.WebhookSecret) != ""
}

// BrowseReady is the App id + private key pair repository listing needs.
func (c Creds) BrowseReady() bool {
	return strings.TrimSpace(c.AppID) != "" && strings.TrimSpace(c.PrivateKeyPEM) != ""
}

// NewSecretBox derives the GitHub App secretbox from a deployment secret.
// A dedicated MULTICA_GITHUB_APP_SECRET_KEY still wins at the call site.
// Deriving from JWT_SECRET means creating an App from Settings does not
// require a second env var and an SSH step.
func NewSecretBox(deploymentSecret string) (*secretbox.Box, error) {
	trimmed := strings.TrimSpace(deploymentSecret)
	if trimmed == "" {
		return nil, secretbox.ErrInvalidKey
	}
	sum := sha256.Sum256([]byte("multica/github-app-credential/v1:" + trimmed))
	return secretbox.New(sum[:])
}

var (
	mu     sync.RWMutex
	stored *Creds
)

// Store keeps the database identity in memory. Later Current calls see it
// until an environment variable is set, which always wins.
func Store(c Creds) {
	c.Source = SourceDatabase
	c.ReadOnly = false
	mu.Lock()
	stored = &c
	mu.Unlock()
}

// Reset drops the in-memory database identity. Tests use it.
func Reset() {
	mu.Lock()
	stored = nil
	mu.Unlock()
}

// FromEnv reports the environment identity when any of the four App
// variables is set. A partial set still counts: mixing it with the database
// row would sign with one App's key and verify webhooks with another's.
func FromEnv() (Creds, bool) {
	slug := strings.TrimSpace(os.Getenv("GITHUB_APP_SLUG"))
	webhook := strings.TrimSpace(os.Getenv("GITHUB_WEBHOOK_SECRET"))
	appID := strings.TrimSpace(os.Getenv("GITHUB_APP_ID"))
	pem := strings.TrimSpace(os.Getenv("GITHUB_APP_PRIVATE_KEY"))
	if slug == "" && webhook == "" && appID == "" && pem == "" {
		return Creds{}, false
	}
	c := Creds{
		Source:        SourceEnv,
		AppID:         appID,
		Slug:          slug,
		Name:          slug,
		PrivateKeyPEM: pem,
		WebhookSecret: webhook,
		ReadOnly:      true,
	}
	if slug != "" {
		c.HTMLURL = "https://github.com/apps/" + slug
		c.ManageURL = "https://github.com/settings/apps/" + slug
	}
	return c, true
}

// Current is the identity to use. Environment variables win over the row
// loaded by Store.
func Current() Creds {
	if c, ok := FromEnv(); ok {
		return c
	}
	mu.RLock()
	defer mu.RUnlock()
	if stored == nil {
		return Creds{Source: SourceNone}
	}
	c := *stored
	c.Source = SourceDatabase
	c.ReadOnly = false
	return c
}

// SnapshotIdentity is the App id and PEM the pull-request snapshot client
// reads on each token mint, so a manifest callback enables that client
// without a restart.
func SnapshotIdentity() (appID, pem string, ok bool) {
	c := Current()
	if !c.BrowseReady() {
		return "", "", false
	}
	return c.AppID, c.PrivateKeyPEM, true
}
