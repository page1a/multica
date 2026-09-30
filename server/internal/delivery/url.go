package delivery

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Ref is a pull or merge request named by its URL.
type Ref struct {
	Provider string
	Host     string
	Owner    string
	Repo     string
	Number   int32
	URL      string
	// Key is the repoident key, host/owner/repo, used to find the connection.
	Key string
}

// ParsePullURL accepts the links a person pastes into `issue close --pr`:
//
//	https://github.com/owner/repo/pull/392
//	https://gitlab.com/group/sub/repo/-/merge_requests/12
//	https://git.example.com/owner/repo/pulls/3
func ParsePullURL(raw string) (Ref, error) {
	text := strings.TrimSpace(raw)
	parsed, err := url.Parse(text)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return Ref{}, fmt.Errorf("pr url must be an http(s) link to a pull or merge request")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	host := strings.ToLower(parsed.Hostname())
	ref := Ref{Host: host, URL: parsed.Scheme + "://" + parsed.Host + parsed.Path}
	mrIdx := indexOf(parts, "merge_requests")

	switch {
	case len(parts) >= 4 && parts[len(parts)-2] == "pull":
		ref.Provider = "github"
		ref.Owner = parts[0]
		ref.Repo = strings.TrimSuffix(parts[1], ".git")
		// github.com/owner/repo/pull/N — owner is a single segment.
		// Anything longer before "pull" is not the GitHub shape.
		if len(parts) != 4 {
			return Ref{}, fmt.Errorf("pr url is not a GitHub pull request link")
		}
		n, err := strconv.Atoi(parts[3])
		if err != nil || n <= 0 {
			return Ref{}, fmt.Errorf("pr url has no pull request number")
		}
		ref.Number = int32(n)
	case mrIdx > 0 && mrIdx == len(parts)-2:
		ref.Provider = "gitlab"
		project := parts[:mrIdx]
		// Drop the GitLab separator segment "-" when it is the last project piece.
		if len(project) > 0 && project[len(project)-1] == "-" {
			project = project[:len(project)-1]
		}
		if len(project) < 2 {
			return Ref{}, fmt.Errorf("pr url is not a GitLab merge request link")
		}
		ref.Repo = strings.TrimSuffix(project[len(project)-1], ".git")
		ref.Owner = strings.Join(project[:len(project)-1], "/")
		n, err := strconv.Atoi(parts[mrIdx+1])
		if err != nil || n <= 0 {
			return Ref{}, fmt.Errorf("pr url has no merge request number")
		}
		ref.Number = int32(n)
	case len(parts) >= 4 && parts[len(parts)-2] == "pulls":
		ref.Provider = "forgejo"
		ref.Owner = strings.Join(parts[:len(parts)-3], "/")
		ref.Repo = strings.TrimSuffix(parts[len(parts)-3], ".git")
		if ref.Owner == "" || ref.Repo == "" {
			return Ref{}, fmt.Errorf("pr url is not a Forgejo pull request link")
		}
		n, err := strconv.Atoi(parts[len(parts)-1])
		if err != nil || n <= 0 {
			return Ref{}, fmt.Errorf("pr url has no pull request number")
		}
		ref.Number = int32(n)
	default:
		return Ref{}, fmt.Errorf("pr url is not a pull or merge request link")
	}
	// The pull URL's path continues past the repository (pull/N, merge_requests/N).
	// The connection is bound to the repository, so the key stops at owner/repo.
	ref.Key = strings.ToLower(ref.Host + "/" + ref.Owner + "/" + ref.Repo)
	return ref, nil
}

func indexOf(parts []string, want string) int {
	for i, p := range parts {
		if p == want {
			return i
		}
	}
	return -1
}

// ProjectPath is owner/repo the way the provider's API addresses the project.
// GitLab keeps subgroups inside owner ("group/sub").
func (r Ref) ProjectPath() string {
	return r.Owner + "/" + r.Repo
}
