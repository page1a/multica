package workspacelink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/permission"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Managed access (DENE-1663). A source owner can switch a link to managed:
// the viewer's agents may then work on the source's issues and autopilots
// for their run's originator, while execution stays in the source. The task
// token stays bound to its own workspace (MUL-2600); Managed below is the one
// place a token's request may reach another workspace, and only when all of
// these hold on that very request:
//
//  1. the run names the source in LinkedWorkspaceHeader, and an active link
//     from that source to the token's workspace has managed switched on;
//  2. the run's originator (never the runtime owner) is a member of the
//     source whose tier allows OpManageRemote;
//  3. the route is on managedRoutes.
//
// The request then continues as the originator's own request in the source,
// so every rule the source applies to that person still applies. Switching
// managed off or revoking the link refuses the next call.

// LinkedWorkspaceHeader names the source workspace (slug or id) a run wants
// to act in. `multica ... --linked <slug>` sets it.
const LinkedWorkspaceHeader = "X-Linked-Workspace"

// ManagedActorSource is the X-Actor-Source a managed request carries once it
// continues as the originator's. It is a machine credential for every
// human-only gate.
const ManagedActorSource = "linked_task_token"

// ErrManagedDenied is the one refusal for a request the gate will not carry:
// no such workspace, no active link, managed off, no originator, or an
// originator without the right there. Like ErrNotFound it never says which.
var ErrManagedDenied = forbidden("that workspace is not open to this run: it needs an active link with managed access on, and the person who started this run must be able to do this there")

// ErrManagedRoute refuses a command outside the managed list.
var ErrManagedRoute = forbidden("this command is not available on a linked workspace: only issues (list, get, create, update, comment, labels, properties) and autopilots (list, get, create, update, triggers, run, delete) are")

// managedRoute is one endpoint a managed request may reach.
type managedRoute struct {
	name   string
	method string
	path   *regexp.Regexp
	// write routes leave a managed_write audit row (and an issue activity).
	write bool
}

func route(name, method, pattern string, write bool) managedRoute {
	return managedRoute{name: name, method: method, path: regexp.MustCompile("^" + pattern + "/?$"), write: write}
}

const seg = `[^/]+`

// managedRoutes is the whole whitelist. Lookups (labels, properties,
// statuses, projects, members, agents, squads) are read-only and exist so
// the CLI can resolve names; members, workspace settings, MCP, agent config
// and the link itself stay closed.
var managedRoutes = []managedRoute{
	route("issue.list", "GET", `/api/issues`, false),
	route("issue.search", "GET", `/api/issues/search`, false),
	route("issue.children", "GET", `/api/issues/children`, false),
	route("issue.create", "POST", `/api/issues`, true),
	route("issue.get", "GET", `/api/issues/(?P<issue>`+seg+`)`, false),
	route("issue.update", "PUT", `/api/issues/(?P<issue>`+seg+`)`, true),
	route("issue.child_list", "GET", `/api/issues/(?P<issue>`+seg+`)/children`, false),
	route("issue.comments", "GET", `/api/issues/(?P<issue>`+seg+`)/comments`, false),
	route("issue.comment", "POST", `/api/issues/(?P<issue>`+seg+`)/comments`, true),
	route("issue.timeline", "GET", `/api/issues/(?P<issue>`+seg+`)/timeline`, false),
	route("issue.labels", "GET", `/api/issues/(?P<issue>`+seg+`)/labels`, false),
	route("issue.label_add", "POST", `/api/issues/(?P<issue>`+seg+`)/labels`, true),
	route("issue.label_remove", "DELETE", `/api/issues/(?P<issue>`+seg+`)/labels/`+seg, true),
	route("issue.property_set", "PUT", `/api/issues/(?P<issue>`+seg+`)/properties/`+seg, true),
	route("issue.property_clear", "DELETE", `/api/issues/(?P<issue>`+seg+`)/properties/`+seg, true),

	route("autopilot.list", "GET", `/api/autopilots`, false),
	route("autopilot.cron_preview", "GET", `/api/autopilots/cron-preview`, false),
	route("autopilot.create", "POST", `/api/autopilots`, true),
	route("autopilot.get", "GET", `/api/autopilots/(?P<autopilot>`+seg+`)`, false),
	route("autopilot.update", "PATCH", `/api/autopilots/(?P<autopilot>`+seg+`)`, true),
	route("autopilot.delete", "DELETE", `/api/autopilots/(?P<autopilot>`+seg+`)`, true),
	route("autopilot.run", "POST", `/api/autopilots/(?P<autopilot>`+seg+`)/trigger`, true),
	route("autopilot.runs", "GET", `/api/autopilots/(?P<autopilot>`+seg+`)/runs`, false),
	route("autopilot.linked_changes", "GET", `/api/autopilots/(?P<autopilot>`+seg+`)/linked-changes`, false),
	route("autopilot.run_get", "GET", `/api/autopilots/(?P<autopilot>`+seg+`)/runs/`+seg, false),
	route("autopilot.trigger_add", "POST", `/api/autopilots/(?P<autopilot>`+seg+`)/triggers`, true),
	route("autopilot.trigger_update", "PATCH", `/api/autopilots/(?P<autopilot>`+seg+`)/triggers/`+seg, true),
	route("autopilot.trigger_delete", "DELETE", `/api/autopilots/(?P<autopilot>`+seg+`)/triggers/`+seg, true),

	route("lookup.labels", "GET", `/api/labels`, false),
	route("lookup.properties", "GET", `/api/properties`, false),
	route("lookup.statuses", "GET", `/api/issue-statuses`, false),
	route("lookup.projects", "GET", `/api/projects`, false),
	route("lookup.squads", "GET", `/api/squads`, false),
	route("lookup.agents", "GET", `/api/agents`, false),
	route("lookup.members", "GET", `/api/workspaces/(?P<workspace>`+seg+`)/members`, false),
}

// matchManagedRoute finds the whitelisted route and its path parameters.
func matchManagedRoute(method, path string) (managedRoute, map[string]string, bool) {
	for _, rt := range managedRoutes {
		if rt.method != method {
			continue
		}
		m := rt.path.FindStringSubmatch(path)
		if m == nil {
			continue
		}
		params := map[string]string{}
		for i, name := range rt.path.SubexpNames() {
			if name != "" {
				params[name] = m[i]
			}
		}
		return rt, params, true
	}
	return managedRoute{}, nil, false
}

// ManagedCall is what the token's request says about itself. Every field is
// server-set by the auth middleware except Source, which only names a target.
type ManagedCall struct {
	ViewerWS pgtype.UUID
	AgentID  pgtype.UUID
	TaskID   pgtype.UUID
	Source   string
}

// ManagedGrant is a request the gate carries into the source.
type ManagedGrant struct {
	Link       db.WorkspaceLink
	Source     db.Workspace
	Viewer     db.Workspace
	Originator pgtype.UUID
	Agent      db.Agent
	TaskID     pgtype.UUID
}

// AuthorizeManaged answers whether this run may act in the named source.
// Every refusal is ErrManagedDenied.
func (s *Service) AuthorizeManaged(ctx context.Context, call ManagedCall) (ManagedGrant, error) {
	var g ManagedGrant
	source, err := s.workspaceByRef(ctx, call.Source)
	if err != nil {
		return g, err
	}
	if source.ID == call.ViewerWS {
		return g, ErrManagedDenied
	}
	link, err := s.q.GetActiveWorkspaceLinkBetween(ctx, db.GetActiveWorkspaceLinkBetweenParams{
		SourceWorkspaceID: source.ID, TargetWorkspaceID: call.ViewerWS,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return g, ErrManagedDenied
	}
	if err != nil {
		return g, err
	}
	if !link.Managed {
		return g, ErrManagedDenied
	}
	task, err := s.q.GetAgentTask(ctx, call.TaskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return g, ErrManagedDenied
	}
	if err != nil {
		return g, err
	}
	// A finished run lends nothing (MUL-6951), and only the originator —
	// the person who started the run — is ever judged.
	if task.AgentID != call.AgentID || !task.OriginatorUserID.Valid || terminalTask(task.Status) {
		return g, ErrManagedDenied
	}
	agent, err := s.q.GetAgent(ctx, call.AgentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return g, ErrManagedDenied
	}
	if err != nil {
		return g, err
	}
	if agent.WorkspaceID != call.ViewerWS {
		return g, ErrManagedDenied
	}
	member, err := s.q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID: task.OriginatorUserID, WorkspaceID: source.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return g, ErrManagedDenied
	}
	if err != nil {
		return g, err
	}
	if !Decide(OpManageRemote, SideViewer, Actor{Role: permission.Role(member.Role), IsAgent: true}) {
		return g, ErrManagedDenied
	}
	viewer, err := s.q.GetWorkspace(ctx, call.ViewerWS)
	if err != nil {
		return g, err
	}
	return ManagedGrant{Link: link, Source: source, Viewer: viewer, Originator: task.OriginatorUserID, Agent: agent, TaskID: call.TaskID}, nil
}

func terminalTask(status string) bool {
	switch status {
	case "completed", "failed", "cancelled":
		return true
	}
	return false
}

// workspaceByRef reads a workspace by id or slug; a miss is ErrManagedDenied.
func (s *Service) workspaceByRef(ctx context.Context, ref string) (db.Workspace, error) {
	ref = strings.TrimSpace(ref)
	var ws db.Workspace
	var err error
	if id, perr := util.ParseUUID(ref); perr == nil {
		ws, err = s.q.GetWorkspace(ctx, id)
	} else {
		ws, err = s.q.GetWorkspaceBySlug(ctx, ref)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Workspace{}, ErrManagedDenied
	}
	return ws, err
}

// Managed is the HTTP gate. Mounted right after Auth and before the guest
// interceptor, so every later gate judges the originator in the source.
// A request without LinkedWorkspaceHeader passes untouched; a person's
// request drops the header and goes on as itself (people reach another
// workspace through their own membership).
func (s *Service) Managed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ref := strings.TrimSpace(r.Header.Get(LinkedWorkspaceHeader))
		r.Header.Del(LinkedWorkspaceHeader)
		if ref == "" || r.Header.Get("X-Actor-Source") != "task_token" {
			next.ServeHTTP(w, r)
			return
		}
		rt, params, ok := matchManagedRoute(r.Method, r.URL.Path)
		if !ok {
			writeManagedError(w, ErrManagedRoute)
			return
		}
		call := ManagedCall{Source: ref}
		var err error
		if call.ViewerWS, err = util.ParseUUID(r.Header.Get("X-Workspace-ID")); err != nil {
			writeManagedError(w, ErrManagedDenied)
			return
		}
		if call.AgentID, err = util.ParseUUID(r.Header.Get("X-Agent-ID")); err != nil {
			writeManagedError(w, ErrManagedDenied)
			return
		}
		if call.TaskID, err = util.ParseUUID(r.Header.Get("X-Task-ID")); err != nil {
			writeManagedError(w, ErrManagedDenied)
			return
		}
		grant, err := s.AuthorizeManaged(r.Context(), call)
		if err != nil {
			writeManagedError(w, err)
			return
		}
		sourceID := util.UUIDToString(grant.Source.ID)
		// A workspace in the path must be the source itself: the originator
		// may belong to other workspaces the link says nothing about.
		if ws, ok := params["workspace"]; ok && ws != sourceID {
			writeManagedError(w, ErrManagedDenied)
			return
		}

		// From here on the request is the originator's own, in the source.
		r.Header.Set("X-User-ID", util.UUIDToString(grant.Originator))
		r.Header.Set("X-Workspace-ID", sourceID)
		r.Header.Set("X-Actor-Source", ManagedActorSource)
		r.Header.Del("X-Workspace-Slug")
		r.Header.Del("X-Agent-ID")
		r.Header.Del("X-Task-ID")
		q := r.URL.Query()
		q.Del("workspace_slug")
		if q.Has("workspace_id") {
			q.Set("workspace_id", sourceID)
		}
		r.URL.RawQuery = q.Encode()

		rec := &managedRecorder{ResponseWriter: w, status: http.StatusOK, keepBody: rt.name == "issue.create" || rt.name == "autopilot.create"}
		next.ServeHTTP(rec, r)
		if rt.write && rec.status < 300 {
			s.recordManagedWrite(context.WithoutCancel(r.Context()), grant, rt, params, rec.body.Bytes())
		}
	})
}

func writeManagedError(w http.ResponseWriter, err error) {
	var linkErr *Error
	status, msg := http.StatusInternalServerError, "linked workspace request failed"
	if errors.As(err, &linkErr) {
		status, msg = linkErr.Status, linkErr.Message
	} else {
		slog.Error("workspace link managed gate", "error", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// managedRecorder keeps the status, and the body of an issue or autopilot
// create, which is the only way to learn the new id.
type managedRecorder struct {
	http.ResponseWriter
	status   int
	keepBody bool
	body     bytes.Buffer
}

func (m *managedRecorder) WriteHeader(code int) {
	m.status = code
	m.ResponseWriter.WriteHeader(code)
}

func (m *managedRecorder) Write(b []byte) (int, error) {
	if m.keepBody && m.body.Len() < 1<<20 {
		m.body.Write(b)
	}
	return m.ResponseWriter.Write(b)
}

func (m *managedRecorder) Flush() {
	if f, ok := m.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// recordManagedWrite leaves the trail of one managed write: a managed_write
// row in the link audit (both owners read it) and, for an issue, a
// linked_write activity in the source issue's timeline naming the viewer
// workspace and agent. A failure here is logged, never undone: the write
// itself already happened as the originator.
func (s *Service) recordManagedWrite(ctx context.Context, g ManagedGrant, rt managedRoute, params map[string]string, body []byte) {
	detail := map[string]any{
		"route":         rt.name,
		"via_workspace": g.Viewer.Name,
		"via_slug":      g.Viewer.Slug,
		"agent_id":      util.UUIDToString(g.Agent.ID),
		"agent_name":    g.Agent.Name,
		"task_id":       util.UUIDToString(g.TaskID),
		"autopilot_id":  params["autopilot"],
		"issue_ref":     params["issue"],
	}
	var issueID pgtype.UUID
	if strings.HasPrefix(rt.name, "issue.") {
		issueID = s.managedIssueID(ctx, g.Source, params["issue"], body)
		if issueID.Valid {
			detail["issue_id"] = util.UUIDToString(issueID)
		}
	}
	var autopilotID pgtype.UUID
	if strings.HasPrefix(rt.name, "autopilot.") {
		autopilotID = s.managedAutopilotID(ctx, g.Source, params["autopilot"], body)
		if autopilotID.Valid {
			detail["autopilot_id"] = util.UUIDToString(autopilotID)
		}
	}
	if err := audit(ctx, s.q, g.Link, g.Viewer.ID, g.Originator, "managed_write", detail); err != nil {
		slog.Error("workspace link managed audit", "error", err, "route", rt.name)
	}
	if autopilotID.Valid {
		if err := s.q.InsertAutopilotLinkedChange(ctx, db.InsertAutopilotLinkedChangeParams{
			WorkspaceID:      g.Source.ID,
			AutopilotID:      autopilotID,
			LinkID:           g.Link.ID,
			Route:            rt.name,
			ActorID:          g.Originator,
			ViaWorkspaceID:   g.Viewer.ID,
			ViaWorkspaceName: g.Viewer.Name,
			ViaSlug:          g.Viewer.Slug,
			AgentID:          g.Agent.ID,
			AgentName:        g.Agent.Name,
			TaskID:           g.TaskID,
		}); err != nil {
			slog.Error("workspace link managed autopilot change", "error", err, "route", rt.name)
		}
	}
	if !issueID.Valid {
		return
	}
	raw, err := json.Marshal(map[string]any{
		"route":         rt.name,
		"via_workspace": g.Viewer.Name,
		"via_slug":      g.Viewer.Slug,
		"agent_id":      util.UUIDToString(g.Agent.ID),
		"agent_name":    g.Agent.Name,
	})
	if err != nil {
		return
	}
	if _, err := s.q.CreateActivity(ctx, db.CreateActivityParams{
		WorkspaceID: g.Source.ID,
		IssueID:     issueID,
		ActorType:   pgtype.Text{String: "member", Valid: true},
		ActorID:     g.Originator,
		Action:      "linked_write",
		Details:     raw,
	}); err != nil {
		slog.Error("workspace link managed activity", "error", err, "route", rt.name)
	}
}

// managedAutopilotID resolves the autopilot a managed write touched: the
// path's id, or the id in an autopilot create's response. A deleted
// autopilot resolves to nothing; its trail is the link audit row.
func (s *Service) managedAutopilotID(ctx context.Context, source db.Workspace, ref string, body []byte) pgtype.UUID {
	if ref == "" {
		var created struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(body, &created) != nil {
			return pgtype.UUID{}
		}
		ref = created.ID
	}
	id, err := util.ParseUUID(ref)
	if err != nil {
		return pgtype.UUID{}
	}
	ap, err := s.q.GetAutopilotInWorkspace(ctx, db.GetAutopilotInWorkspaceParams{ID: id, WorkspaceID: source.ID})
	if err != nil {
		return pgtype.UUID{}
	}
	return ap.ID
}

// managedIssueID resolves the issue a managed write touched: the path's id
// or identifier, or the id in an issue create's response.
func (s *Service) managedIssueID(ctx context.Context, source db.Workspace, ref string, body []byte) pgtype.UUID {
	if ref == "" {
		var created struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(body, &created) != nil {
			return pgtype.UUID{}
		}
		ref = created.ID
	}
	if id, err := util.ParseUUID(ref); err == nil {
		if issue, err := s.q.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: id, WorkspaceID: source.ID}); err == nil {
			return issue.ID
		}
		return pgtype.UUID{}
	}
	i := strings.LastIndex(ref, "-")
	n, err := strconv.Atoi(ref[i+1:])
	if err != nil {
		return pgtype.UUID{}
	}
	issue, err := s.q.GetIssueByNumber(ctx, db.GetIssueByNumberParams{WorkspaceID: source.ID, Number: int32(n)})
	if err != nil {
		return pgtype.UUID{}
	}
	return issue.ID
}

// setManaged switches managed access. The source owner decides; someone who
// owns the source and manages the viewer may switch it from the viewer side
// too (the pull rule, DENE-1582), and then each side gets its audit row.
func (s *Service) setManaged(ctx context.Context, q *db.Queries, link db.WorkspaceLink, side Side, actorWS, actorUser pgtype.UUID, actor Actor, on bool) error {
	fromViewer := false
	switch side {
	case SideSource:
		if !Decide(OpSetManaged, SideSource, actor) {
			return forbidden("only the source workspace owner can switch managed access")
		}
	case SideViewer:
		ok, err := s.canPull(ctx, q, actorUser, link.SourceWorkspaceID, actor)
		if err != nil {
			return err
		}
		if !ok {
			return forbidden("only an owner of the source workspace can switch managed access; ask its owner")
		}
		fromViewer = true
	default:
		return ErrNotFound
	}
	updated, err := q.SetWorkspaceLinkManaged(ctx, db.SetWorkspaceLinkManagedParams{ID: link.ID, Managed: on})
	if err != nil {
		return err
	}
	detail := map[string]any{"managed": on}
	if fromViewer {
		detail["from_viewer"] = true
	}
	if err := audit(ctx, q, updated, updated.SourceWorkspaceID, actorUser, "set_managed", detail); err != nil {
		return err
	}
	if fromViewer {
		return audit(ctx, q, updated, actorWS, actorUser, "set_managed", detail)
	}
	return nil
}
