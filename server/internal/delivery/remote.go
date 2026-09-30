package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTP is the client the lookup uses. Tests substitute their own.
var HTTP = &http.Client{Timeout: 12 * time.Second}

// APIBase is the REST root for a connection. instanceURL is the site
// (https://github.com, https://gitlab.example.com). apiOverride, when set by
// a test, replaces it entirely.
func APIBase(provider, instanceURL, apiOverride string) string {
	if strings.TrimSpace(apiOverride) != "" {
		return strings.TrimRight(apiOverride, "/")
	}
	instanceURL = strings.TrimRight(strings.TrimSpace(instanceURL), "/")
	host := ""
	if u, err := url.Parse(instanceURL); err == nil {
		host = strings.ToLower(u.Hostname())
	}
	switch provider {
	case "github":
		if host == "" || host == "github.com" || host == "www.github.com" {
			return "https://api.github.com"
		}
		return instanceURL + "/api/v3"
	case "gitlab":
		return instanceURL + "/api/v4"
	default:
		return instanceURL + "/api/v1"
	}
}

// Search asks the provider for pull requests whose title contains identifier.
func Search(ctx context.Context, client *http.Client, provider, apiBase, token, owner, repo, identifier string) ([]Pull, error) {
	if client == nil {
		client = HTTP
	}
	switch provider {
	case "github":
		return searchGitHub(ctx, client, apiBase, token, owner, repo, identifier)
	case "gitlab":
		return searchGitLab(ctx, client, apiBase, token, owner, repo, identifier)
	default:
		return searchForgejo(ctx, client, apiBase, token, owner, repo, identifier)
	}
}

// Fetch reads one pull request the caller already named.
func Fetch(ctx context.Context, client *http.Client, provider, apiBase, token string, ref Ref) (Pull, error) {
	if client == nil {
		client = HTTP
	}
	switch provider {
	case "github":
		return fetchGitHub(ctx, client, apiBase, token, ref.Owner, ref.Repo, ref.Number)
	case "gitlab":
		return fetchGitLab(ctx, client, apiBase, token, ref.ProjectPath(), ref.Number)
	default:
		return fetchForgejo(ctx, client, apiBase, token, ref.Owner, ref.Repo, ref.Number)
	}
}

// Merge squash-merges one pull request with the stored token.
func Merge(ctx context.Context, client *http.Client, provider, apiBase, token string, ref Ref) error {
	if client == nil {
		client = HTTP
	}
	switch provider {
	case "github":
		endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/merge", apiBase, url.PathEscape(ref.Owner), url.PathEscape(ref.Repo), ref.Number)
		return writeJSON(ctx, client, http.MethodPut, endpoint, token, "github", map[string]string{"merge_method": "squash"})
	case "gitlab":
		endpoint := fmt.Sprintf("%s/projects/%s/merge_requests/%d/merge", apiBase, url.PathEscape(ref.ProjectPath()), ref.Number)
		return writeJSON(ctx, client, http.MethodPut, endpoint, token, "gitlab", map[string]any{"squash": true})
	default:
		endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/merge", apiBase, url.PathEscape(ref.Owner), url.PathEscape(ref.Repo), ref.Number)
		return writeJSON(ctx, client, http.MethodPost, endpoint, token, "forgejo", map[string]string{"Do": "squash"})
	}
}

// ValidateToken checks the token and returns the account login.
func ValidateToken(ctx context.Context, client *http.Client, provider, apiBase, token, owner, repo string) (string, error) {
	if client == nil {
		client = HTTP
	}
	switch provider {
	case "github":
		var user struct {
			Login string `json:"login"`
		}
		if err := readJSON(ctx, client, apiBase+"/user", token, "github", &user); err != nil {
			return "", err
		}
		if owner != "" && repo != "" {
			if err := readJSON(ctx, client, fmt.Sprintf("%s/repos/%s/%s", apiBase, url.PathEscape(owner), url.PathEscape(repo)), token, "github", &struct{}{}); err != nil {
				return "", err
			}
		}
		if user.Login == "" {
			return "", fmt.Errorf("github: user response missing login")
		}
		return user.Login, nil
	case "gitlab":
		var user struct {
			Username string `json:"username"`
		}
		if err := readJSON(ctx, client, apiBase+"/user", token, "gitlab", &user); err != nil {
			return "", err
		}
		if owner != "" && repo != "" {
			if err := readJSON(ctx, client, apiBase+"/projects/"+url.PathEscape(owner+"/"+repo), token, "gitlab", &struct{}{}); err != nil {
				return "", err
			}
		}
		if user.Username == "" {
			return "", fmt.Errorf("gitlab: user response missing username")
		}
		return user.Username, nil
	default:
		var user struct {
			Login    string `json:"login"`
			UserName string `json:"username"`
		}
		if err := readJSON(ctx, client, apiBase+"/user", token, "forgejo", &user); err != nil {
			return "", err
		}
		login := user.Login
		if login == "" {
			login = user.UserName
		}
		if login == "" {
			return "", fmt.Errorf("forgejo: user response missing login")
		}
		return login, nil
	}
}

func searchGitHub(ctx context.Context, client *http.Client, apiBase, token, owner, repo, identifier string) ([]Pull, error) {
	q := url.Values{}
	q.Set("q", fmt.Sprintf("repo:%s/%s is:pr %s in:title", owner, repo, identifier))
	q.Set("per_page", "10")
	var body struct {
		Items []struct {
			Number      int32     `json:"number"`
			PullRequest *struct{} `json:"pull_request"`
		} `json:"items"`
	}
	if err := readJSON(ctx, client, apiBase+"/search/issues?"+q.Encode(), token, "github", &body); err != nil {
		return nil, err
	}
	var out []Pull
	for _, item := range body.Items {
		if item.PullRequest == nil || item.Number == 0 {
			continue
		}
		pull, err := fetchGitHub(ctx, client, apiBase, token, owner, repo, item.Number)
		if err != nil {
			return nil, err
		}
		out = append(out, pull)
	}
	return out, nil
}

func fetchGitHub(ctx context.Context, client *http.Client, apiBase, token, owner, repo string, number int32) (Pull, error) {
	var body struct {
		Number         int32  `json:"number"`
		Title          string `json:"title"`
		State          string `json:"state"`
		HTMLURL        string `json:"html_url"`
		Draft          bool   `json:"draft"`
		Merged         bool   `json:"merged"`
		MergeableState string `json:"mergeable_state"`
		User           struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", apiBase, url.PathEscape(owner), url.PathEscape(repo), number)
	if err := readJSON(ctx, client, endpoint, token, "github", &body); err != nil {
		return Pull{}, err
	}
	state := strings.ToLower(body.State)
	if body.Merged {
		state = "merged"
	} else if body.Draft && state == "open" {
		state = "draft"
	}
	pull := Pull{
		Provider: "github", Owner: owner, Repo: repo, Number: body.Number,
		Title: body.Title, State: state, URL: body.HTMLURL, Branch: body.Head.Ref,
		SHA: body.Head.SHA, Mergeable: strings.ToLower(body.MergeableState), Author: body.User.Login,
	}
	pull.Checks = githubChecks(ctx, client, apiBase, token, owner, repo, body.Head.SHA)
	return pull, nil
}

func githubChecks(ctx context.Context, client *http.Client, apiBase, token, owner, repo, sha string) string {
	if sha == "" {
		return ""
	}
	var body struct {
		State string `json:"state"`
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/commits/%s/status", apiBase, url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(sha))
	if err := readJSON(ctx, client, endpoint, token, "github", &body); err != nil {
		return ""
	}
	switch strings.ToLower(body.State) {
	case "success":
		return "success"
	case "failure", "error":
		return "failure"
	case "pending":
		return "pending"
	default:
		return ""
	}
}

func searchGitLab(ctx context.Context, client *http.Client, apiBase, token, owner, repo, identifier string) ([]Pull, error) {
	q := url.Values{}
	q.Set("search", identifier)
	q.Set("in", "title")
	q.Set("scope", "all")
	q.Set("per_page", "20")
	endpoint := fmt.Sprintf("%s/projects/%s/merge_requests?%s", apiBase, url.PathEscape(owner+"/"+repo), q.Encode())
	return readGitLabMRs(ctx, client, endpoint, token, owner, repo)
}

func fetchGitLab(ctx context.Context, client *http.Client, apiBase, token, project string, number int32) (Pull, error) {
	endpoint := fmt.Sprintf("%s/projects/%s/merge_requests/%d", apiBase, url.PathEscape(project), number)
	var item glMR
	if err := readJSON(ctx, client, endpoint, token, "gitlab", &item); err != nil {
		return Pull{}, err
	}
	owner, repo := splitProject(project)
	return item.pull(owner, repo), nil
}

type glMR struct {
	IID          int32  `json:"iid"`
	Title        string `json:"title"`
	State        string `json:"state"`
	WebURL       string `json:"web_url"`
	SourceBranch string `json:"source_branch"`
	SHA          string `json:"sha"`
	Draft        bool   `json:"draft"`
	MergeStatus  string `json:"merge_status"`
	Detailed     string `json:"detailed_merge_status"`
	Author       struct {
		Username string `json:"username"`
	} `json:"author"`
	HeadPipeline *struct {
		Status string `json:"status"`
	} `json:"head_pipeline"`
}

func (m glMR) pull(owner, repo string) Pull {
	state := "open"
	switch m.State {
	case "merged":
		state = "merged"
	case "closed":
		state = "closed"
	default:
		if m.Draft {
			state = "draft"
		}
	}
	mergeable := "unknown"
	switch m.Detailed {
	case "mergeable", "ci_still_running", "ci_must_pass", "approvals_syncing":
		mergeable = "clean"
	case "conflict", "need_rebase", "discussions_not_resolved", "not_approved", "draft_status":
		if m.Detailed == "conflict" || m.Detailed == "need_rebase" {
			mergeable = "dirty"
		} else if m.Detailed == "draft_status" {
			mergeable = "clean"
		} else {
			mergeable = "blocked"
		}
	default:
		switch m.MergeStatus {
		case "can_be_merged":
			mergeable = "clean"
		case "cannot_be_merged":
			mergeable = "dirty"
		}
	}
	checks := ""
	if m.HeadPipeline != nil {
		switch m.HeadPipeline.Status {
		case "success", "skipped":
			checks = "success"
		case "failed", "canceled":
			checks = "failure"
		case "running", "pending", "created", "preparing", "waiting_for_resource":
			checks = "pending"
		}
	}
	return Pull{
		Provider: "gitlab", Owner: owner, Repo: repo, Number: m.IID,
		Title: m.Title, State: state, URL: m.WebURL, Branch: m.SourceBranch,
		SHA: m.SHA, Mergeable: mergeable, Checks: checks, Author: m.Author.Username,
	}
}

func readGitLabMRs(ctx context.Context, client *http.Client, endpoint, token, owner, repo string) ([]Pull, error) {
	var items []glMR
	if err := readJSON(ctx, client, endpoint, token, "gitlab", &items); err != nil {
		return nil, err
	}
	out := make([]Pull, 0, len(items))
	for _, item := range items {
		if item.IID == 0 {
			continue
		}
		out = append(out, item.pull(owner, repo))
	}
	return out, nil
}

func searchForgejo(ctx context.Context, client *http.Client, apiBase, token, owner, repo, identifier string) ([]Pull, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls?state=all&limit=50", apiBase, url.PathEscape(owner), url.PathEscape(repo))
	pulls, err := readForgejo(ctx, client, endpoint, token, owner, repo)
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(identifier)
	var out []Pull
	for _, p := range pulls {
		if strings.Contains(strings.ToLower(p.Title), needle) || strings.Contains(strings.ToLower(p.Branch), needle) {
			out = append(out, p)
		}
	}
	return out, nil
}

func fetchForgejo(ctx context.Context, client *http.Client, apiBase, token, owner, repo string, number int32) (Pull, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", apiBase, url.PathEscape(owner), url.PathEscape(repo), number)
	var item fjPR
	if err := readJSON(ctx, client, endpoint, token, "forgejo", &item); err != nil {
		return Pull{}, err
	}
	return item.pull(owner, repo), nil
}

type fjPR struct {
	Number int32  `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	HTML   string `json:"html_url"`
	Merged bool   `json:"merged"`
	Draft  bool   `json:"draft"`
	Head   struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	User struct {
		Login    string `json:"login"`
		UserName string `json:"username"`
	} `json:"user"`
	Mergeable bool `json:"mergeable"`
}

func (p fjPR) pull(owner, repo string) Pull {
	state := strings.ToLower(p.State)
	if p.Merged || state == "merged" {
		state = "merged"
	} else if p.Draft && (state == "" || state == "open") {
		state = "draft"
	} else if state == "" {
		state = "open"
	}
	mergeable := "unknown"
	if p.Mergeable {
		mergeable = "clean"
	} else if state == "open" || state == "draft" {
		mergeable = "dirty"
	}
	author := p.User.Login
	if author == "" {
		author = p.User.UserName
	}
	return Pull{
		Provider: "forgejo", Owner: owner, Repo: repo, Number: p.Number,
		Title: p.Title, State: state, URL: p.HTML, Branch: p.Head.Ref,
		SHA: p.Head.SHA, Mergeable: mergeable, Author: author,
	}
}

func readForgejo(ctx context.Context, client *http.Client, endpoint, token, owner, repo string) ([]Pull, error) {
	var items []fjPR
	if err := readJSON(ctx, client, endpoint, token, "forgejo", &items); err != nil {
		return nil, err
	}
	out := make([]Pull, 0, len(items))
	for _, item := range items {
		if item.Number == 0 {
			continue
		}
		out = append(out, item.pull(owner, repo))
	}
	return out, nil
}

func splitProject(project string) (string, string) {
	project = strings.Trim(project, "/")
	i := strings.LastIndex(project, "/")
	if i <= 0 {
		return project, ""
	}
	return project[:i], project[i+1:]
}

type apiError struct {
	Status int
	Body   string
}

func (e *apiError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("provider status %d", e.Status)
	}
	return fmt.Sprintf("provider status %d: %s", e.Status, e.Body)
}

// Unauthorized reports a token the provider rejected.
func Unauthorized(err error) bool {
	api, ok := err.(*apiError)
	return ok && (api.Status == http.StatusUnauthorized || api.Status == http.StatusForbidden)
}

// NotFound reports a missing pull request or project.
func NotFound(err error) bool {
	api, ok := err.(*apiError)
	return ok && api.Status == http.StatusNotFound
}

func readJSON(ctx context.Context, client *http.Client, endpoint, token, provider string, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	authorize(req, provider, token)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		text := strings.TrimSpace(string(body))
		if len(text) > 180 {
			text = text[:180]
		}
		return &apiError{Status: resp.StatusCode, Body: text}
	}
	if dest == nil {
		return nil
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	return json.Unmarshal(body, dest)
}

func writeJSON(ctx context.Context, client *http.Client, method, endpoint, token, provider string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	authorize(req, provider, token)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		text := strings.TrimSpace(string(body))
		if len(text) > 180 {
			text = text[:180]
		}
		return &apiError{Status: resp.StatusCode, Body: text}
	}
	return nil
}

func authorize(req *http.Request, provider, token string) {
	req.Header.Set("Accept", "application/json")
	switch provider {
	case "gitlab":
		req.Header.Set("PRIVATE-TOKEN", token)
	case "github":
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	default:
		req.Header.Set("Authorization", "token "+token)
	}
}
