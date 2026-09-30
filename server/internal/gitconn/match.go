// Package gitconn decides which stored connection covers a repository.
// A connection covers an account or an organization, not one repository.
// An empty cover list means the whole instance (legacy GitLab and Forgejo
// rows). A personal connection only covers repositories that its owner
// registered.
package gitconn

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/multica-ai/multica/server/internal/repoident"
)

// Conn is the part of a stored connection the matcher needs. No secrets.
type Conn struct {
	ID           string
	Provider     string
	InstanceURL  string
	AccountLogin string
	Covers       []string
	Personal     bool
	OwnerID      string
}

// Repo is one repository the workspace or a project knows about.
type Repo struct {
	Key        string // host/owner/name, lowercased
	URL        string
	Registrant string // user id of the person who registered it
}

// HostOf returns the lowercased host of an instance URL.
func HostOf(instanceURL string) string {
	u, err := url.Parse(strings.TrimSpace(instanceURL))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// Parts splits a repo key into host, owner and name.
func Parts(key string) (host, owner, name string, ok bool) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(key)), "/")
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

func sameHost(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	if a == "www.github.com" {
		a = "github.com"
	}
	if b == "www.github.com" {
		b = "github.com"
	}
	return a != "" && a == b
}

// Matches reports whether conn covers repo. A personal connection matches
// only when repo.Registrant is that connection's owner.
func Matches(conn Conn, repo Repo) bool {
	host, owner, _, ok := Parts(repo.Key)
	if !ok || !sameHost(host, HostOf(conn.InstanceURL)) {
		return false
	}
	if conn.Personal {
		if repo.Registrant == "" || !strings.EqualFold(repo.Registrant, conn.OwnerID) {
			return false
		}
	}
	if len(conn.Covers) == 0 {
		return true
	}
	for _, cover := range conn.Covers {
		if strings.EqualFold(cover, owner) || strings.EqualFold(cover, conn.AccountLogin) && strings.EqualFold(conn.AccountLogin, owner) {
			return true
		}
	}
	return false
}

// AppCovers reports whether a GitHub App installation on account covers the
// repository. Installations are workspace-wide.
func AppCovers(accountLogin, repoKey string) bool {
	_, owner, _, ok := Parts(repoKey)
	if !ok || accountLogin == "" {
		return false
	}
	return strings.EqualFold(accountLogin, owner)
}

// AskMessage is the inbox body: a settings link and a command the person can paste.
func AskMessage(repoKey, settingsURL, command string) string {
	var b strings.Builder
	b.WriteString(repoKey)
	b.WriteString(" 还没有连接，关单时查不到 PR。\n添加连接：")
	b.WriteString(settingsURL)
	b.WriteString("\n或在本机运行：")
	b.WriteString(command)
	return b.String()
}

// AddCommand is the copy-paste command for this host.
func AddCommand(host string) string {
	if strings.Contains(strings.ToLower(host), "gitlab") {
		return "multica connection add --from-glab"
	}
	return "multica connection add --from-gh"
}

// SettingsURL opens the settings page on the connection tab.
func SettingsURL(publicBase, slug, host string) string {
	_ = host
	base := strings.TrimRight(strings.TrimSpace(publicBase), "/")
	path := "/" + strings.Trim(slug, "/") + "/settings?tab=git-connections"
	if base == "" {
		return path
	}
	return base + path
}

// FromResource reads a project resource into a Repo. ok is false when the
// resource does not name a repository.
func FromResource(resourceType string, ref []byte) (Repo, bool) {
	switch resourceType {
	case "github_repo":
		var payload struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(ref, &payload) != nil {
			return Repo{}, false
		}
		key := string(repoident.NormalizeURL(payload.URL))
		if key == "" {
			return Repo{}, false
		}
		return Repo{Key: key, URL: payload.URL}, true
	case "local_directory":
		var payload struct {
			RepoKey string `json:"repo_key"`
		}
		if json.Unmarshal(ref, &payload) != nil {
			return Repo{}, false
		}
		key := strings.ToLower(strings.TrimSpace(payload.RepoKey))
		if _, _, _, ok := Parts(key); !ok {
			return Repo{}, false
		}
		return Repo{Key: key}, true
	default:
		return Repo{}, false
	}
}
