package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/permission"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Cross-workspace read-by-link (DENE-897, design in DENE-891).
//
// A member of workspace A pastes an A issue link to the agent they run in
// workspace B. The agent's mat_ token stays bound to B (MUL-2600 — the three
// bindings in middleware/workspace.go are untouched), so it reaches A only
// through the routes mounted here, which sit in the Auth group OUTSIDE
// RequireWorkspaceMember and resolve the workspace from the link, never from
// the request headers. Only GET is served; every write method answers 405
// with a stable code so the CLI can explain why.
//
// Who is judged is not the agent and not the token's user (that is the
// runtime OWNER, not the person who asked — handler/daemon.go stamps
// runtime.OwnerID into the token). It is the human at the top of the run's
// chain, agent_task_queue.originator_user_id, and only when that chain is a
// direct human action (originator_source = direct_human). A scheduled
// autopilot, an automatic routing dispatch or an agent-to-agent delegation
// has no person who just pasted a link, so it is refused. That human is then
// held to exactly what they could see in A themselves: membership, the
// Issues module gate, and resource visibility through
// visibilityViewerForUser — NOT visibilityViewerFor, whose agent bypass would
// hand out A's private issues.
//
// What comes back is the issue's own record: fields, comments, timeline and
// run metadata. Not the run transcripts, logs, result / error payloads,
// source_context or any attachment URL — redact.Text does not recognise our
// own mat_ / mul_ / mdt_ tokens, so a transcript is not safe to hand across a
// workspace boundary. Every payload carries a provenance block naming where
// it came from, and every cross-workspace read writes a
// cross_workspace_read row into A's activity_log, so the people in A can see
// on the issue's own timeline that it was read from outside.
//
// The same routes also accept a link into the caller's OWN workspace (a
// human or agent pasting a URL instead of an issue key). That path is the
// ordinary read — same viewer the /api/issues routes use, no audit row — so
// the CLI can accept a URL wherever it accepts an issue key.

// Stable error codes the CLI maps to human sentences (internal/cli).
const (
	linkErrNoOriginator   = "link_no_originator"
	linkErrNotMember      = "link_not_member"
	linkErrWriteForbidden = "link_write_forbidden"
)

// linkReadAction is the activity_log action written into the linked
// workspace on every cross-workspace read.
const linkReadAction = "cross_workspace_read"

// issueLink is a parsed issue URL: which workspace, which issue.
type issueLink struct {
	Slug string
	Ref  string // issue key (DENE-12) or UUID
}

// parseIssueLink accepts the web / desktop issue URL shapes:
//
//	https://host/<slug>/issues/<ref>
//	https://host/<slug>/issues/<ref>?...#...
//	/<slug>/issues/<ref>            (path only)
//	<slug>/issues/<ref>
//
// The host is not checked: a deployment may be reached under several names
// and the workspace slug is what identifies the target. A link that does not
// match this shape is refused rather than guessed at.
func parseIssueLink(raw string) (issueLink, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return issueLink{}, errors.New("link is required")
	}
	path := raw
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return issueLink{}, fmt.Errorf("invalid link: %w", err)
		}
		path = u.Path
	} else if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	// Expect [..., slug, "issues", ref]; a locale or basePath prefix before the
	// slug is tolerated because the anchor is the "issues" segment.
	for i := 1; i+1 < len(parts); i++ {
		if parts[i] == "issues" && parts[i-1] != "" && parts[i+1] != "" {
			ref, err := url.PathUnescape(parts[i+1])
			if err != nil {
				return issueLink{}, fmt.Errorf("invalid link: %w", err)
			}
			return issueLink{Slug: parts[i-1], Ref: ref}, nil
		}
	}
	return issueLink{}, errors.New("link is not an issue link; expected https://<host>/<workspace>/issues/<issue-key>")
}

// linkProvenance tells the reader where the payload came from. It is meant
// for the model as much as for the person: the CLI prints Notice before the
// content so a cross-workspace read is never mistaken for local data.
type linkProvenance struct {
	WorkspaceID     string `json:"workspace_id"`
	WorkspaceSlug   string `json:"workspace_slug"`
	WorkspaceName   string `json:"workspace_name"`
	IssueID         string `json:"issue_id"`
	IssueIdentifier string `json:"issue_identifier"`
	// CrossWorkspace is false when the link points into the caller's own
	// (bound) workspace; that read is the ordinary one and leaves no audit.
	CrossWorkspace bool `json:"cross_workspace"`
	// ReadAs is the A-side member the read was authorised as (the run's
	// originator). Empty on a same-workspace read.
	ReadAs string `json:"read_as,omitempty"`
	ReadAt string `json:"read_at"`
	Notice string `json:"notice"`
}

// linkRunResponse is the run metadata a linked read exposes: who ran, when,
// how it ended. No prompt, result, error or failure reason.
type linkRunResponse struct {
	ID               string  `json:"id"`
	AgentID          string  `json:"agent_id"`
	AgentName        string  `json:"agent_name,omitempty"`
	Status           string  `json:"status"`
	OriginatorSource string  `json:"originator_source,omitempty"`
	CreatedAt        string  `json:"created_at"`
	StartedAt        *string `json:"started_at"`
	CompletedAt      *string `json:"completed_at"`
}

// linkIssueResponse is GET /api/links/issue.
type linkIssueResponse struct {
	Issue      IssueResponse     `json:"issue"`
	Runs       []linkRunResponse `json:"runs"`
	Provenance linkProvenance    `json:"provenance"`
}

// linkReadTarget is the outcome of the gate: the issue, its workspace, and
// how the read was authorised.
type linkReadTarget struct {
	issue     db.Issue
	workspace db.Workspace
	// cross is true when the caller's bound workspace differs from the
	// link's; only then is originator / readAs set and an audit row written.
	cross      bool
	originator pgtype.UUID
	task       db.AgentTaskQueue
}

// MountLinkReadRoutes registers the read-by-link routes. Called from the
// router inside the Auth group, deliberately outside RequireWorkspaceMember
// and RequireModule — the handlers resolve the workspace from the link and
// run those gates themselves against the right workspace. Kept here so the
// handler tests mount exactly what production mounts.
func (h *Handler) MountLinkReadRoutes(r chi.Router) {
	patterns := []string{"/api/links/issue", "/api/links/issue/comments", "/api/links/issue/timeline"}
	r.Get(patterns[0], h.LinkReadIssue)
	r.Get(patterns[1], h.LinkReadIssueComments)
	r.Get(patterns[2], h.LinkReadIssueTimeline)
	// Writes are refused with a stable code rather than chi's bare 405 so the
	// CLI can say "a linked issue is read-only" instead of "method not
	// allowed". Registered per pattern: chi resolves the exact node first and
	// does not fall back to a wildcard for a missing method.
	for _, p := range patterns {
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			r.Method(m, p, http.HandlerFunc(linkReadWriteForbidden))
		}
	}
}

func linkReadWriteForbidden(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Allow", http.MethodGet)
	writeErrorCode(w, http.StatusMethodNotAllowed, linkErrWriteForbidden,
		"an issue reached through a link is read-only; write to it from inside its own workspace")
}

// LinkReadIssue — GET /api/links/issue?url=<issue link>
func (h *Handler) LinkReadIssue(w http.ResponseWriter, r *http.Request) {
	target, ok := h.resolveLinkReadTarget(w, r, "issue")
	if !ok {
		return
	}
	ctx := r.Context()
	issue := target.issue
	prefix := h.getIssuePrefix(ctx, issue.WorkspaceID)
	resp := issueToResponse(issue, prefix)
	h.fillStatusCategory(ctx, issue.WorkspaceID, &resp)
	labels := h.labelsByIssue(ctx, issue.WorkspaceID, []pgtype.UUID{issue.ID})[uuidToString(issue.ID)]
	if labels == nil {
		labels = []LabelResponse{}
	}
	resp.Labels = &labels
	// Attachments: name, type and size only. URLs stay behind the workspace
	// boundary (the download route binds task tokens to their workspace,
	// DENE-896, so a URL would be a dead link at best and a leak at worst).
	if attachments, err := h.Queries.ListAttachmentsByIssue(ctx, db.ListAttachmentsByIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
	}); err == nil && len(attachments) > 0 {
		resp.Attachments = make([]AttachmentResponse, len(attachments))
		for i, a := range attachments {
			resp.Attachments[i] = h.attachmentToResponse(a, attachmentURLModeStable)
		}
		resp.Attachments = redactAttachmentURLs(resp.Attachments)
	}
	// SourceContext is the frozen execution contract with whatever the
	// creator pasted into it; it never leaves the workspace. Reactions are
	// noise for a reader outside it.
	resp.SourceContext = nil

	runs, err := h.linkRuns(ctx, issue)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runs")
		return
	}
	writeJSON(w, http.StatusOK, linkIssueResponse{
		Issue:      resp,
		Runs:       runs,
		Provenance: h.linkProvenance(target, prefix),
	})
}

// LinkReadIssueComments — GET /api/links/issue/comments?url=<issue link>&<ListComments params>
//
// Same query language as GET /api/issues/{id}/comments (since / thread /
// tail / recent / roots_only / summary / fold / cursor); the body is the same
// flat comment array, minus attachment URLs, with the provenance in a header
// so the array shape the CLI already parses is unchanged.
func (h *Handler) LinkReadIssueComments(w http.ResponseWriter, r *http.Request) {
	target, ok := h.resolveLinkReadTarget(w, r, "comments")
	if !ok {
		return
	}
	h.setLinkProvenanceHeader(w, target)
	h.writeCommentList(w, r, target.issue, true)
}

// LinkReadIssueTimeline — GET /api/links/issue/timeline?url=<issue link>
func (h *Handler) LinkReadIssueTimeline(w http.ResponseWriter, r *http.Request) {
	target, ok := h.resolveLinkReadTarget(w, r, "timeline")
	if !ok {
		return
	}
	h.setLinkProvenanceHeader(w, target)
	h.writeTimeline(w, r, target.issue, true)
}

// HeaderLinkProvenance carries the provenance block on the list-shaped
// responses (comments, timeline), whose bodies are arrays.
const HeaderLinkProvenance = "X-Multica-Link-Provenance"

func (h *Handler) setLinkProvenanceHeader(w http.ResponseWriter, target linkReadTarget) {
	prefix := h.getIssuePrefix(context.Background(), target.issue.WorkspaceID)
	if b, err := json.Marshal(h.linkProvenance(target, prefix)); err == nil {
		w.Header().Set(HeaderLinkProvenance, string(b))
	}
}

func (h *Handler) linkProvenance(target linkReadTarget, prefix string) linkProvenance {
	identifier := issueIdentifier(prefix, target.issue.Number)
	p := linkProvenance{
		WorkspaceID:     uuidToString(target.workspace.ID),
		WorkspaceSlug:   target.workspace.Slug,
		WorkspaceName:   target.workspace.Name,
		IssueID:         uuidToString(target.issue.ID),
		IssueIdentifier: identifier,
		CrossWorkspace:  target.cross,
		ReadAt:          time.Now().UTC().Format(time.RFC3339),
	}
	if target.cross {
		p.ReadAs = uuidToString(target.originator)
		p.Notice = fmt.Sprintf(
			"以下内容来自另一个工作区「%s」的 %s，按发起人在该工作区的权限只读取得；请勿把它原样转贴到当前工作区，也不能对它做任何修改。",
			target.workspace.Slug, identifier)
	} else {
		p.Notice = fmt.Sprintf("链接指向当前工作区的 %s。", identifier)
	}
	return p
}

// resolveLinkReadTarget runs the gate. On failure the response has been
// written and ok is false. surface names which read this is for the audit row.
//
// Order matters for what a refused caller learns. The originator and
// membership gates run BEFORE the issue is looked up, so a run with no human
// behind it, or a human who is not in A, learns nothing about whether the
// issue exists. Module and visibility failures then answer with the same 404
// an absent issue gets.
func (h *Handler) resolveLinkReadTarget(w http.ResponseWriter, r *http.Request, surface string) (linkReadTarget, bool) {
	ctx := r.Context()
	link, err := parseIssueLink(r.URL.Query().Get("url"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return linkReadTarget{}, false
	}
	ws, err := h.Queries.GetWorkspaceBySlug(ctx, link.Slug)
	if err != nil {
		// An unknown workspace and a hidden issue must read the same.
		hiddenIssueNotFound(w)
		return linkReadTarget{}, false
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return linkReadTarget{}, false
	}

	target := linkReadTarget{workspace: ws}
	var viewer visibilityViewer
	isTaskToken := r.Header.Get("X-Actor-Source") == "task_token"
	boundWorkspace := r.Header.Get("X-Workspace-ID")

	switch {
	case isTaskToken && boundWorkspace != uuidToString(ws.ID):
		// Cross-workspace: judge the human behind the run.
		target.cross = true
		task, originator, ok := h.linkOriginator(w, r)
		if !ok {
			return linkReadTarget{}, false
		}
		target.task, target.originator = task, originator
		if _, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
			UserID:      originator,
			WorkspaceID: ws.ID,
		}); err != nil {
			writeErrorCode(w, http.StatusForbidden, linkErrNotMember,
				"the person this run acts for is not a member of the workspace this link points to")
			return linkReadTarget{}, false
		}
		viewer, err = h.visibilityViewerForUser(ctx, ws.ID, originator)
		if err != nil {
			hiddenIssueNotFound(w)
			return linkReadTarget{}, false
		}
	case isTaskToken:
		// The link points into the token's own workspace: the ordinary agent
		// read, same viewer /api/issues uses.
		viewer, err = h.visibilityViewerFor(r, ws.ID)
		if err != nil {
			hiddenIssueNotFound(w)
			return linkReadTarget{}, false
		}
	default:
		// A person. They can already open any workspace they belong to by
		// switching; this is that read with the workspace taken from the link.
		userUUID, err := parseUUIDSafe(userID)
		if err != nil {
			hiddenIssueNotFound(w)
			return linkReadTarget{}, false
		}
		if _, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
			UserID:      userUUID,
			WorkspaceID: ws.ID,
		}); err != nil {
			writeErrorCode(w, http.StatusForbidden, linkErrNotMember,
				"you are not a member of the workspace this link points to")
			return linkReadTarget{}, false
		}
		viewer, err = h.visibilityViewerForUser(ctx, ws.ID, userUUID)
		if err != nil {
			hiddenIssueNotFound(w)
			return linkReadTarget{}, false
		}
	}

	// Module gate: RequireModule is a route-group middleware bound to the
	// request's workspace, so it is applied by hand against the link's.
	moduleRow, err := h.loadModuleVisibility(ctx, ws.ID, permission.ModuleIssues)
	if err != nil || !viewer.canSeeModule(moduleRow) {
		hiddenIssueNotFound(w)
		return linkReadTarget{}, false
	}

	issue, ok := h.resolveLinkedIssue(ctx, ws, link.Ref)
	if !ok || !viewer.canSeeIssue(issue) {
		hiddenIssueNotFound(w)
		return linkReadTarget{}, false
	}
	target.issue = issue

	if target.cross {
		h.recordLinkRead(r, target, surface)
	}
	return target, true
}

// linkOriginator returns the live task behind this request and its
// direct-human originator, or writes the link_no_originator refusal. Mirrors
// invokeOriginatorFromRequest (a terminal task lends nothing) and then
// narrows to originator_source = direct_human: the owner's first-version
// decision is that only a run a person started by hand carries that person
// across a workspace boundary.
func (h *Handler) linkOriginator(w http.ResponseWriter, r *http.Request) (db.AgentTaskQueue, pgtype.UUID, bool) {
	refuse := func(msg string) (db.AgentTaskQueue, pgtype.UUID, bool) {
		writeErrorCode(w, http.StatusForbidden, linkErrNoOriginator, msg)
		return db.AgentTaskQueue{}, pgtype.UUID{}, false
	}
	taskUUID, err := util.ParseUUID(r.Header.Get("X-Task-ID"))
	if err != nil {
		return refuse("this request carries no run, so there is no person to read the link as")
	}
	task, err := h.Queries.GetAgentTask(r.Context(), taskUUID)
	if err != nil {
		return refuse("this request carries no run, so there is no person to read the link as")
	}
	if isTerminalTaskStatus(task.Status) {
		return refuse("this run has finished; a finished run no longer acts for the person who started it")
	}
	if !task.OriginatorUserID.Valid {
		return refuse("this run has no originating human, so it cannot read another workspace on anyone's behalf")
	}
	if task.OriginatorSource.String != "direct_human" {
		return refuse(fmt.Sprintf(
			"this run was started by %s, not directly by a person; only a run a person started by hand may read another workspace",
			describeOriginatorSource(task.OriginatorSource.String)))
	}
	return task, task.OriginatorUserID, true
}

func describeOriginatorSource(source string) string {
	switch source {
	case "trigger_owner", "rule_owner":
		return "an automation trigger"
	case "delegation", "comment_source":
		return "another agent's run"
	case "owner_fallback", "backfill":
		return "an ownership fallback"
	case "unattributed", "":
		return "an unattributed dispatch"
	}
	return source
}

// resolveLinkedIssue finds the issue a link names inside the link's
// workspace: by key (DENE-12, case-insensitive) or by UUID.
func (h *Handler) resolveLinkedIssue(ctx context.Context, ws db.Workspace, ref string) (db.Issue, bool) {
	if issue, ok := h.resolveIssueByIdentifier(ctx, ref, uuidToString(ws.ID)); ok {
		return issue, true
	}
	issueUUID, err := util.ParseUUID(ref)
	if err != nil {
		return db.Issue{}, false
	}
	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issueUUID, WorkspaceID: ws.ID})
	if err != nil {
		return db.Issue{}, false
	}
	return issue, true
}

// recordLinkRead writes the audit row into the LINKED workspace's activity
// log, attributed to the A-side member the read was authorised as, with the
// reader's side (workspace, agent, run) in details. It lands on the issue's
// own timeline, which is where the people in A look. Best-effort: a failed
// audit write is logged, not turned into a refusal — the read was already
// authorised and the log is not the authorisation.
func (h *Handler) recordLinkRead(r *http.Request, target linkReadTarget, surface string) {
	ctx := r.Context()
	details := map[string]any{
		"surface":              surface,
		"reader_workspace_id":  r.Header.Get("X-Workspace-ID"),
		"reader_agent_id":      r.Header.Get("X-Agent-ID"),
		"reader_task_id":       uuidToString(target.task.ID),
		"originator_user_id":   uuidToString(target.originator),
		"originator_source":    target.task.OriginatorSource.String,
		"reader_task_issue_id": uuidToString(target.task.IssueID),
	}
	if readerWS, err := util.ParseUUID(r.Header.Get("X-Workspace-ID")); err == nil {
		if ws, err := h.Queries.GetWorkspace(ctx, readerWS); err == nil {
			details["reader_workspace_slug"] = ws.Slug
			details["reader_workspace_name"] = ws.Name
		}
	}
	if agentUUID, err := util.ParseUUID(r.Header.Get("X-Agent-ID")); err == nil {
		if agent, err := h.Queries.GetAgent(ctx, agentUUID); err == nil {
			details["reader_agent_name"] = agent.Name
		}
	}
	raw, _ := json.Marshal(details)
	activity, err := h.Queries.CreateActivity(ctx, db.CreateActivityParams{
		ID:          dbid.NewV7(),
		WorkspaceID: target.workspace.ID,
		IssueID:     target.issue.ID,
		ActorType:   pgtype.Text{String: "member", Valid: true},
		ActorID:     target.originator,
		Action:      linkReadAction,
		Details:     raw,
	})
	if err != nil {
		slog.Error("cross-workspace read: audit row not written",
			append(logger.RequestAttrs(r), "issue_id", uuidToString(target.issue.ID), "error", err)...)
		return
	}
	h.publish(protocol.EventActivityCreated, uuidToString(target.workspace.ID), "member", uuidToString(target.originator), map[string]any{
		"issue_id": uuidToString(target.issue.ID),
		"entry": map[string]any{
			"type":       "activity",
			"id":         uuidToString(activity.ID),
			"actor_type": "member",
			"actor_id":   uuidToString(target.originator),
			"action":     activity.Action,
			"details":    json.RawMessage(raw),
			"created_at": timestampToString(activity.CreatedAt),
		},
	})
}

// linkRuns is the run history trimmed to metadata.
func (h *Handler) linkRuns(ctx context.Context, issue db.Issue) ([]linkRunResponse, error) {
	tasks, err := h.Queries.ListTasksByIssue(ctx, issue.ID)
	if err != nil {
		return nil, err
	}
	tasks = visibleTaskHistory(tasks)
	names := map[string]string{}
	runs := make([]linkRunResponse, 0, len(tasks))
	for _, t := range tasks {
		agentID := uuidToString(t.AgentID)
		name, seen := names[agentID]
		if !seen {
			if agent, err := h.Queries.GetAgent(ctx, t.AgentID); err == nil {
				name = agent.Name
			}
			names[agentID] = name
		}
		runs = append(runs, linkRunResponse{
			ID:               uuidToString(t.ID),
			AgentID:          agentID,
			AgentName:        name,
			Status:           t.Status,
			OriginatorSource: t.OriginatorSource.String,
			CreatedAt:        timestampToString(t.CreatedAt),
			StartedAt:        timestampToPtr(t.StartedAt),
			CompletedAt:      timestampToPtr(t.CompletedAt),
		})
	}
	return runs, nil
}

// redactAttachmentURLs keeps what an attachment IS (name, type, size, who
// uploaded it, where it hangs) and drops every way to fetch it.
func redactAttachmentURLs(in []AttachmentResponse) []AttachmentResponse {
	for i := range in {
		in[i].URL = ""
		in[i].DownloadURL = ""
		in[i].AttachmentDownloadURL = ""
		in[i].MarkdownURL = ""
	}
	return in
}
