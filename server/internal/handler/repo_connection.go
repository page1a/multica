package handler

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/delivery"
	"github.com/multica-ai/multica/server/internal/integrations/vcs"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/repoident"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// repoConnectionCard is one workspace repository as the settings page will
// show it (DENE-967 reads this; the page itself is not in this change).
type repoConnectionCard struct {
	URL             string             `json:"url"`
	Provider        string             `json:"provider"`
	Mode            string             `json:"mode"`
	AccountLogin    string             `json:"account_login,omitempty"`
	Webhook         string             `json:"webhook"`
	LastLookupOK    *bool              `json:"last_lookup_ok"`
	LastLookupAt    string             `json:"last_lookup_at,omitempty"`
	LastLookupError string             `json:"last_lookup_error,omitempty"`
	ConnectionID    string             `json:"connection_id,omitempty"`
	CanConfigure    bool               `json:"can_configure"`
	CreatedBy       string             `json:"created_by,omitempty"`
	Projects        []RepoReachProject `json:"projects"`
	Reach           RepoReach          `json:"reach"`
	// AgentEligible is true when this request is an agent task and the
	// repository was registered by that task's initiator.
	AgentEligible bool `json:"agent_eligible,omitempty"`
}

type repoConnectionRequest struct {
	RepoURL     string `json:"repo_url"`
	Provider    string `json:"provider"`
	InstanceURL string `json:"instance_url"`
	AccessToken string `json:"access_token"`
	// AgentYes is a client hint. Authorization ignores it: an agent caller is
	// always limited to repositories the task initiator registered.
	AgentYes bool `json:"agent_yes"`
}

// ListRepoConnections (GET /workspaces/{id}/repos/connections) lists each
// repository the caller can see, and how the server can reach it.
func (h *Handler) ListRepoConnections(w http.ResponseWriter, r *http.Request) {
	ws, wsUUID, ok := h.workspaceForRepoConnection(w, r)
	if !ok {
		return
	}
	conns, err := h.Queries.ListVCSConnectionsByWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list connections")
		return
	}
	initiator, _ := h.agentTaskInitiator(r, wsUUID)
	viewer, viewErr := h.visibilityViewerFor(r, wsUUID)
	var viewerPtr *visibilityViewer
	if viewErr == nil {
		viewerPtr = &viewer
	}
	cards := make([]repoConnectionCard, 0)
	for _, repo := range h.visibleWorkspaceRepos(r, ws) {
		card := h.repoCard(r, wsUUID, repo, conns, viewerPtr)
		if initiator.Valid && repo.CreatedBy != "" && repo.CreatedBy == uuidToString(initiator) {
			card.AgentEligible = true
		}
		cards = append(cards, card)
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": cards})
}

// UpsertRepoConnection (POST /workspaces/{id}/repos/connections) stores an
// encrypted token for one repository the caller added, or that an owner/admin
// is configuring. GitHub and GitLab both use this path.
func (h *Handler) UpsertRepoConnection(w http.ResponseWriter, r *http.Request) {
	h.writeRepoConnection(w, r, true)
}

// TestRepoConnection (POST /workspaces/{id}/repos/connections/test) checks a
// token against the repository and stores nothing.
func (h *Handler) TestRepoConnection(w http.ResponseWriter, r *http.Request) {
	h.writeRepoConnection(w, r, false)
}

func (h *Handler) writeRepoConnection(w http.ResponseWriter, r *http.Request, save bool) {
	ws, wsUUID, ok := h.workspaceForRepoConnection(w, r)
	if !ok {
		return
	}
	if !h.isVCSAvailable() {
		writeError(w, http.StatusNotFound, "vcs integration is not available on this deployment")
		return
	}
	if !h.isVCSConfigured() {
		writeFeatureDisabled(w, "vcs_not_configured", "vcs integration not configured (MULTICA_VCS_SECRET_KEY unset)")
		return
	}
	var req repoConnectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	repo, found := findWorkspaceRepo(h.visibleWorkspaceRepos(r, ws), req.RepoURL)
	if !found {
		writeError(w, http.StatusNotFound, "这个仓库还没加到工作区")
		return
	}
	var connectedBy pgtype.UUID
	if h.callerIsAgent(r, wsUUID) {
		initiator, okInit := h.agentTaskInitiator(r, wsUUID)
		if !okInit {
			writeError(w, http.StatusBadRequest, "智能体登记连接需要这条任务的发起人")
			return
		}
		if repo.CreatedBy == "" || repo.CreatedBy != uuidToString(initiator) {
			writeError(w, http.StatusForbidden, "智能体只能给任务发起人自己添加的仓库登记连接")
			return
		}
		connectedBy = initiator
	} else if !h.callerCanConfigureRepo(r, repo) {
		writeError(w, http.StatusForbidden, "只有仓库的添加人或工作区管理员能配置令牌")
		return
	} else if member, ok := middleware.MemberFromContext(r.Context()); ok {
		connectedBy = member.UserID
	}
	key := string(repoident.NormalizeURL(repo.URL))
	host, owner, name, okKey := splitRepoKey(key)
	if !okKey {
		writeError(w, http.StatusBadRequest, "仓库地址无法识别")
		return
	}
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if provider == "" {
		provider = guessProvider(host, nil)
	}
	if provider != "github" && provider != "gitlab" && provider != "forgejo" && provider != "gitea" {
		writeError(w, http.StatusBadRequest, "provider 需要是 github、gitlab、forgejo 或 gitea")
		return
	}
	token := strings.TrimSpace(req.AccessToken)
	if token == "" {
		writeError(w, http.StatusBadRequest, "access_token is required")
		return
	}
	instance := repoInstanceURL(provider, repo.URL, req.InstanceURL)
	if instance == "" {
		writeError(w, http.StatusBadRequest, "instance_url must be an absolute http(s) URL")
		return
	}
	account, err := delivery.ValidateToken(r.Context(), h.deliveryClient(), provider, delivery.APIBase(provider, instance, ""), token, owner, name)
	if err != nil {
		if delivery.Unauthorized(err) {
			writeError(w, http.StatusBadRequest, "令牌被拒绝，或读不到这个仓库")
			return
		}
		writeError(w, http.StatusBadGateway, "连不上这个代码仓库")
		return
	}
	if !save {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "account_login": account, "provider": provider})
		return
	}
	webhookSecret, err := newVCSWebhookSecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mint webhook secret")
		return
	}
	tokenEnc, err := h.sealVCSSecret(token)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encrypt token")
		return
	}
	secretEnc, err := h.sealVCSSecret(webhookSecret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encrypt webhook secret")
		return
	}
	conn, err := h.Queries.UpsertVCSConnection(r.Context(), db.UpsertVCSConnectionParams{
		WorkspaceID:            wsUUID,
		Provider:               provider,
		InstanceUrl:            instance,
		RepoUrl:                key,
		AccountLogin:           account,
		AccessTokenEncrypted:   tokenEnc,
		WebhookSecretEncrypted: secretEnc,
		ConnectedByID:          connectedBy,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save connection")
		return
	}
	h.clearCoveredNudges(r.Context(), conn)
	h.publish(protocol.EventVCSConnectionCreated, uuidToString(wsUUID), "system", "", map[string]any{"id": uuidToString(conn.ID), "repo_url": repo.URL})
	viewer, viewErr := h.visibilityViewerFor(r, wsUUID)
	var viewerPtr *visibilityViewer
	if viewErr == nil {
		viewerPtr = &viewer
	}
	card := h.repoCard(r, wsUUID, repo, []db.VcsConnection{conn}, viewerPtr)
	writeJSON(w, http.StatusOK, map[string]any{
		"repo":           card,
		"webhook_secret": webhookSecret,
	})
}

func (h *Handler) workspaceForRepoConnection(w http.ResponseWriter, r *http.Request) (db.Workspace, pgtype.UUID, bool) {
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return db.Workspace{}, pgtype.UUID{}, false
	}
	ws, err := h.Queries.GetWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return db.Workspace{}, pgtype.UUID{}, false
	}
	return ws, wsUUID, true
}

func (h *Handler) repoCard(r *http.Request, ws pgtype.UUID, repo workspaceRepoRef, conns []db.VcsConnection, viewer *visibilityViewer) repoConnectionCard {
	reach := h.buildRepoReach(r, ws, repo, conns, viewer)
	card := repoConnectionCard{
		URL:          repo.URL,
		Provider:     reach.Provider,
		Mode:         reach.Mode,
		Webhook:      reach.Webhook,
		CanConfigure: reach.CanConfigure,
		CreatedBy:    repo.CreatedBy,
		Projects:     reach.Projects,
		Reach:        reach,
		AccountLogin: reach.AccountLogin,
	}
	if reach.LinkID != nil {
		card.ConnectionID = *reach.LinkID
	}
	card.LastLookupOK = reach.LastLookup.OK
	card.LastLookupAt = reach.LastLookup.At
	card.LastLookupError = reach.LastLookup.Error
	return card
}

func webhookMode(provider, mode string, conn *db.VcsConnection) string {
	switch mode {
	case "token":
		if provider == "github" {
			return "missing"
		}
		if conn != nil && conn.LastWebhookAt.Valid {
			return "ok"
		}
		return "pending"
	default:
		return "unknown"
	}
}

// callerIsAgent reports whether this request is an agent task. The decision
// comes from resolveActor (the task token, or the agent/task pair tests
// stamp), never from a flag in the body.
func (h *Handler) callerIsAgent(r *http.Request, ws pgtype.UUID) bool {
	userID := requestUserID(r)
	if member, ok := middleware.MemberFromContext(r.Context()); ok && member.UserID.Valid {
		userID = uuidToString(member.UserID)
	}
	actorType, _ := h.resolveActor(r, userID, uuidToString(ws))
	return actorType == "agent"
}

func (h *Handler) callerCanConfigureRepo(r *http.Request, repo workspaceRepoRef) bool {
	member, ok := middleware.MemberFromContext(r.Context())
	if !ok {
		return false
	}
	if roleAllowed(member.Role, "owner", "admin") {
		return true
	}
	return repo.CreatedBy != "" && repo.CreatedBy == uuidToString(member.UserID)
}

func findWorkspaceRepo(repos []workspaceRepoRef, raw string) (workspaceRepoRef, bool) {
	want := string(repoident.NormalizeURL(raw))
	if want == "" {
		return workspaceRepoRef{}, false
	}
	for _, repo := range repos {
		if string(repoident.NormalizeURL(repo.URL)) == want {
			return repo, true
		}
	}
	return workspaceRepoRef{}, false
}

func repoInstanceURL(provider, repoURL, override string) string {
	if strings.TrimSpace(override) != "" {
		return vcs.NormalizeInstanceURL(override)
	}
	parsed, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ""
	}
	instance := vcs.NormalizeInstanceURL(parsed.Scheme + "://" + parsed.Host)
	if provider == "github" && (strings.EqualFold(parsed.Hostname(), "www.github.com") || strings.EqualFold(parsed.Hostname(), "github.com")) {
		return "https://github.com"
	}
	return instance
}
