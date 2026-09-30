package handler

import (
	"context"
	"net/http"
	"net/url"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/gitconn"
	"github.com/multica-ai/multica/server/internal/repoident"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// RepoReach is the one answer for "can the server use this repository, and
// what should happen next". Settings, project pages, the CLI and delivery
// lookup all read it instead of deciding on their own.
type RepoReach struct {
	RepoURL      string             `json:"repo_url"`
	Key          string             `json:"key"`
	Provider     string             `json:"provider"`
	Mode         string             `json:"mode"`
	State        string             `json:"state"`
	AccountLogin string             `json:"account_login"`
	LinkID       *string            `json:"link_id"`
	LastLookup   RepoLastLookup     `json:"last_lookup"`
	Webhook      string             `json:"webhook"`
	Projects     []RepoReachProject `json:"projects"`
	CanConfigure bool               `json:"can_configure"`
	Hint         string             `json:"hint"`
	NextAction   *RepoNextAction    `json:"next_action"`
}

// RepoReachProject is a project the caller is allowed to see that lists this repository.
type RepoReachProject struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// RepoReachContact is an owner or admin the caller can ask when they cannot configure the repository.
type RepoReachContact struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// RepoLastLookup is the most recent token lookup. App mode leaves it empty.
type RepoLastLookup struct {
	OK    *bool  `json:"ok"`
	At    string `json:"at"`
	Error string `json:"error"`
}

// RepoNextAction is the single next step. Nil means the repository is usable and nothing is waiting.
type RepoNextAction struct {
	Kind     string             `json:"kind"`
	For      string             `json:"for,omitempty"`
	URL      string             `json:"url,omitempty"`
	Command  string             `json:"command,omitempty"`
	Optional bool               `json:"optional,omitempty"`
	Contacts []RepoReachContact `json:"contacts,omitempty"`
}

// reachFacts is the input to the pure reach decision. Callers resolve coverage
// before this runs, so one installation cannot be mistaken for every account.
type reachFacts struct {
	Provider     string
	Owner        string
	HasToken     bool
	TokenBroken  bool
	AppCovers    bool
	AppReady     bool
	HasCLI       bool
	CanConfigure bool
	InstallURL   string
	CreateURL    string
	Command      string
	AccountLogin string
}

// DecideRepoReach picks the strongest route and the next step.
// Order: stored token, GitHub App installed on this repository's owner,
// a CLI trace, then nothing. A caller who cannot configure the repository
// gets ask_owner wrapped around the real step.
func DecideRepoReach(f reachFacts) RepoReach {
	out := RepoReach{
		Provider:     f.Provider,
		AccountLogin: f.AccountLogin,
		CanConfigure: f.CanConfigure,
		Projects:     []RepoReachProject{},
	}
	var action *RepoNextAction
	switch {
	case f.HasToken:
		out.Mode = "token"
		out.State = "connected"
		if f.TokenBroken {
			action = &RepoNextAction{Kind: "replace_token", Command: f.Command}
		}
	case f.Provider == "github" && f.AppCovers:
		out.Mode = "app"
		out.State = "connected"
	case f.HasCLI:
		out.Mode = "cli"
		out.State = "cli"
		action = &RepoNextAction{Kind: "add_token", Command: f.Command, Optional: true}
	default:
		out.Mode = "none"
		out.State = "disconnected"
		switch {
		case f.Provider == "github" && f.AppReady:
			out.State = "pending_install"
			action = &RepoNextAction{Kind: "install_app", URL: f.InstallURL}
		case f.Provider == "github":
			action = &RepoNextAction{Kind: "create_app", URL: f.CreateURL}
		default:
			action = &RepoNextAction{Kind: "add_token", Command: f.Command}
		}
	}
	kind := ""
	if action != nil {
		kind = action.Kind
	}
	out.Hint = reachHint(out.Mode, kind, f.Owner)
	if action != nil && !f.CanConfigure {
		action = &RepoNextAction{
			Kind:     "ask_owner",
			For:      action.Kind,
			URL:      action.URL,
			Command:  action.Command,
			Optional: action.Optional,
		}
	}
	out.NextAction = action
	return out
}

func reachHint(mode, kind, owner string) string {
	switch {
	case mode == "token" && kind == "replace_token":
		return "最近一次查询失败，令牌需要更换。"
	case mode == "token":
		return "已用令牌接通。"
	case mode == "app":
		return "已用 GitHub App 接通。"
	case mode == "cli":
		return "服务器到不了这个仓库，本机命令行可以查。"
	case kind == "install_app":
		if owner != "" {
			return "服务器已有 GitHub App，但没装到账号 " + owner + " 上。"
		}
		return "服务器已有 GitHub App，但没装到这个仓库的账号上。"
	case kind == "create_app":
		return "服务器还没有 GitHub App。"
	case kind == "add_token":
		return "这个仓库还没有令牌。"
	default:
		return "这个仓库还没接通。"
	}
}

func (h *Handler) buildRepoReach(r *http.Request, ws pgtype.UUID, repo workspaceRepoRef, conns []db.VcsConnection, viewer *visibilityViewer) RepoReach {
	key := string(repoident.NormalizeURL(repo.URL))
	host, owner, name, _ := splitRepoKey(key)
	provider := guessProvider(host, conns)
	if provider == "" {
		provider = guessProvider(host, nil)
	}
	conn := matchConnection(conns, key, host)
	appCovers, account := h.coveringInstallation(r.Context(), ws, key)
	cli := h.repoHasCLITrace(r, ws, owner, name)
	tokenBroken := conn != nil && conn.LastLookupOk.Valid && !conn.LastLookupOk.Bool
	accountLogin := account
	if conn != nil && conn.AccountLogin != "" {
		accountLogin = conn.AccountLogin
	}
	can := h.callerCanConfigureRepo(r, repo)
	facts := reachFacts{
		Provider:     provider,
		Owner:        owner,
		HasToken:     conn != nil,
		TokenBroken:  tokenBroken,
		AppCovers:    appCovers,
		AppReady:     isGitHubConfigured(),
		HasCLI:       cli,
		CanConfigure: can,
		Command:      gitconn.AddCommand(host),
		AccountLogin: accountLogin,
	}
	if provider == "github" && isGitHubConfigured() {
		if state, err := signState(uuidToString(ws)); err == nil {
			facts.InstallURL = "https://github.com/apps/" + url.PathEscape(githubAppSlug()) + "/installations/new?state=" + url.QueryEscape(state)
		}
	}
	if workspace, err := h.Queries.GetWorkspace(r.Context(), ws); err == nil {
		facts.CreateURL = gitconn.SettingsURL(h.cfg.PublicURL, workspace.Slug, host)
	}
	reach := DecideRepoReach(facts)
	reach.RepoURL = repo.URL
	reach.Key = key
	reach.Projects = h.reachProjects(r, ws, repo.URL, viewer)
	reach.Webhook = webhookMode(provider, reach.Mode, conn)
	if conn != nil {
		id := uuidToString(conn.ID)
		reach.LinkID = &id
		reach.LastLookup.Error = conn.LastLookupError
		if conn.LastLookupOk.Valid {
			ok := conn.LastLookupOk.Bool
			reach.LastLookup.OK = &ok
		}
		if conn.LastLookupAt.Valid {
			reach.LastLookup.At = timestampToString(conn.LastLookupAt)
		}
	}
	if reach.NextAction != nil && reach.NextAction.Kind == "ask_owner" {
		reach.NextAction.Contacts = h.reachContacts(r.Context(), ws)
	}
	return reach
}

func (h *Handler) repoHasCLITrace(r *http.Request, ws pgtype.UUID, owner, name string) bool {
	if owner == "" || name == "" {
		return false
	}
	if ok, err := h.Queries.WorkspaceHasDaemonPullRequest(r.Context(), db.WorkspaceHasDaemonPullRequestParams{
		WorkspaceID: ws, RepoOwner: owner, RepoName: name,
	}); err == nil && ok {
		return true
	}
	ok, err := h.Queries.WorkspaceHasCLIPullRequest(r.Context(), db.WorkspaceHasCLIPullRequestParams{
		WorkspaceID: ws, RepoOwner: owner, RepoName: name,
	})
	return err == nil && ok
}

func (h *Handler) coveringInstallation(ctx context.Context, ws pgtype.UUID, repoKey string) (bool, string) {
	rows, err := h.Queries.ListGitHubInstallationsByWorkspace(ctx, ws)
	if err != nil {
		return false, ""
	}
	for _, row := range rows {
		if gitconn.AppCovers(row.AccountLogin, repoKey) {
			return true, row.AccountLogin
		}
	}
	return false, ""
}

func (h *Handler) reachProjects(r *http.Request, ws pgtype.UUID, repoURL string, viewer *visibilityViewer) []RepoReachProject {
	out := []RepoReachProject{}
	if viewer == nil {
		return out
	}
	for _, id := range h.repoProjectIDs(r.Context(), ws, repoURL) {
		project, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
			ID: id, WorkspaceID: ws,
		})
		if err != nil || !viewer.canSeeProject(project) {
			continue
		}
		out = append(out, RepoReachProject{ID: uuidToString(project.ID), Title: project.Title})
	}
	return out
}

func (h *Handler) reachContacts(ctx context.Context, ws pgtype.UUID) []RepoReachContact {
	rows, err := h.Queries.ListMembersWithUser(ctx, ws)
	if err != nil {
		return nil
	}
	out := make([]RepoReachContact, 0)
	for _, row := range rows {
		if row.Role != "owner" && row.Role != "admin" {
			continue
		}
		out = append(out, RepoReachContact{
			ID:   uuidToString(row.UserID),
			Name: row.UserName,
			Role: row.Role,
		})
	}
	return out
}
