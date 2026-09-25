package handler

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/realtime"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Per-recipient visibility for realtime delivery (DENE-717).
//
// The HTTP read paths resolve sharing scope once per request; a broadcast has
// one frame and many recipients, so the same question has to be asked once per
// connection. This file is that ask. It does not own a rule: the answer is
// visibilityViewer.canSeeIssue / canSeeRepo — the same predicates the list,
// search, table, sub-issue, inbox, attachment and single-read endpoints use.
// Adding a second copy of the rule here would be the bug this file exists to
// prevent.
//
// Two frames are deliberately exempt:
//
//   - the id-only invalidation frame (protocol.EventIssueInvalidated), whose
//     whole purpose is to reach the recipients a content filter excludes;
//   - issue:deleted, which is id-only by contract and whose row is already
//     gone when the post-commit frame is published, so there is nothing left
//     to judge — and the people who need it are the ones holding a cached copy.
//
// Everything else that names an issue is judged, including the issue's
// satellite events (subscribers, pins, activity, reactions, labels,
// properties) — an id and a squad/action field is still "this issue exists".

// FilterRealtimeBroadcast is the BroadcastFilter wired into the hub at
// construction. It classifies one outbound frame once and returns a decision
// per recipient, or nil when no recipient-specific rule applies.
func (h *Handler) FilterRealtimeBroadcast(ctx context.Context, scopeType, scopeID string, frame []byte) realtime.BroadcastDecision {
	envelope, ok := decodeRealtimeEnvelope(frame)
	if !ok {
		// Not a frame this server produced. There is nothing to judge.
		return nil
	}
	eventType, _ := envelope["type"].(string)
	payload, _ := envelope["payload"].(map[string]any)

	switch eventType {
	case protocol.EventIssueInvalidated, protocol.EventIssueDeleted:
		return nil
	case protocol.EventWorkspaceUpdated:
		if scopeType != realtime.ScopeWorkspace {
			return nil
		}
		return h.narrowWorkspaceRepos(ctx, scopeID, envelope, payload)
	case protocol.EventInboxNew:
		// A personal frame: the recipient is the scope, and an inbox item that
		// points at an issue is content for that issue.
		if item, ok := payload["item"].(map[string]any); ok {
			return h.issueVisibilityDecision(ctx, stringField(item, "issue_id"), frame)
		}
		return nil
	}

	if realtimeChatEvent(eventType) {
		if eventType == protocol.EventChatSessionRead {
			return chatReaderDecision(payload, frame)
		}
		return h.chatVisibilityDecision(ctx, stringField(payload, "chat_session_id"), frame)
	}

	if !realtimeIssueEvent(eventType) {
		return nil
	}
	return h.issueVisibilityDecision(ctx, realtimeIssueID(payload), frame)
}

// realtimeChatEvent lists chat frames that carry conversation content.
// session_deleted and session_invalidated are absent on purpose: the row may
// already be gone or the recipient may have just lost it, and those frames
// exist so a client can drop what it cached.
func realtimeChatEvent(eventType string) bool {
	switch eventType {
	case protocol.EventChatMessage,
		protocol.EventChatDone,
		protocol.EventChatQuickActions,
		protocol.EventChatCancelFinalized,
		protocol.EventChatSessionCreated,
		protocol.EventChatSessionUpdated,
		protocol.EventChatSessionRead:
		return true
	default:
		return false
	}
}

// chatReaderDecision delivers a read-cursor frame only to the person who
// read. Someone else's cursor must not clear this client's unread.
func chatReaderDecision(payload map[string]any, frame []byte) realtime.BroadcastDecision {
	reader := stringField(payload, "reader_user_id")
	if reader == "" {
		return nil
	}
	return func(userID string) ([]byte, bool) {
		if userID == reader {
			return frame, true
		}
		return nil, false
	}
}

// chatVisibilityDecision resolves one chat and answers, per recipient,
// whether that person may see it. A chat that cannot be loaded is suppressed
// for everyone — the same fail-closed answer the HTTP read gives.
func (h *Handler) chatVisibilityDecision(ctx context.Context, sessionID string, frame []byte) realtime.BroadcastDecision {
	if sessionID == "" {
		return nil
	}
	sessionUUID, err := parseUUIDSafe(sessionID)
	if err != nil {
		return nil
	}
	session, err := h.Queries.GetChatSession(ctx, sessionUUID)
	if err != nil {
		return suppressEveryRecipient
	}
	projectRows, err := h.Queries.ListChatSessionProjectIDs(ctx, session.ID)
	if err != nil {
		return suppressEveryRecipient
	}
	shares, err := h.Queries.ListChatShareMemberAccess(ctx, db.ListChatShareMemberAccessParams{
		WorkspaceID: session.WorkspaceID,
		ResourceID:  uuidToString(session.ID),
	})
	if err != nil {
		return suppressEveryRecipient
	}
	shareAccess := make(map[string]string, len(shares))
	for _, share := range shares {
		shareAccess[uuidToString(share.MemberID)] = share.Access
	}
	viewers := newRecipientViewers(h, ctx, session.WorkspaceID)
	creatorID := uuidToString(session.CreatorID)
	return func(userID string) ([]byte, bool) {
		if userID == creatorID {
			return frame, true
		}
		if session.Visibility != "project" {
			return nil, false
		}
		if _, ok := shareAccess[userID]; ok {
			return frame, true
		}
		viewer, ok := viewers.get(userID)
		if !ok {
			return nil, false
		}
		for _, projectID := range projectRows {
			if viewer.inProject(projectID) {
				return frame, true
			}
		}
		if viewer.inProject(session.ProjectID) {
			return frame, true
		}
		return nil, false
	}
}

// issueVisibilityDecision resolves the issue once and then answers per
// recipient. A frame with no resolvable issue id carries no issue content, so
// it is passed through; a frame whose issue cannot be loaded is suppressed for
// everyone, which is the same fail-closed answer the HTTP reads give.
func (h *Handler) issueVisibilityDecision(ctx context.Context, issueID string, frame []byte) realtime.BroadcastDecision {
	if issueID == "" {
		return nil
	}
	issueUUID, err := parseUUIDSafe(issueID)
	if err != nil {
		return nil
	}
	issue, err := h.Queries.GetIssue(ctx, issueUUID)
	if err != nil {
		// The row is unreadable: a delete that raced this frame, or a lookup
		// failure. Either way there is no viewer to place, and the HTTP reads
		// answer an unanswerable question with "not found".
		return suppressEveryRecipient
	}
	viewers := newRecipientViewers(h, ctx, issue.WorkspaceID)
	return func(userID string) ([]byte, bool) {
		viewer, ok := viewers.get(userID)
		if !ok || !viewer.canSeeIssue(issue) {
			return nil, false
		}
		return frame, true
	}
}

// narrowWorkspaceRepos rewrites the workspace snapshot's repo registry to the
// entries one viewer may see. It runs once per broadcast to load the workspace
// and each repo's projects, then once per recipient to build that recipient's
// slice.
func (h *Handler) narrowWorkspaceRepos(ctx context.Context, workspaceID string, envelope, payload map[string]any) realtime.BroadcastDecision {
	wsUUID, err := parseUUIDSafe(workspaceID)
	if err != nil {
		return suppressEveryRecipient
	}
	ws, err := h.Queries.GetWorkspace(ctx, wsUUID)
	if err != nil {
		return suppressEveryRecipient
	}
	repos := decodeWorkspaceRepos(ws.Repos)
	if len(repos) == 0 {
		// Nothing repository-shaped in the frame; no per-recipient view of it.
		return nil
	}
	workspace, _ := payload["workspace"].(map[string]any)
	if workspace == nil {
		return nil
	}
	repoProjects := make(map[string][]pgtype.UUID, len(repos))
	for _, entry := range repos {
		repoProjects[entry.URL] = h.repoProjectIDs(ctx, wsUUID, entry.URL)
	}
	viewers := newRecipientViewers(h, ctx, wsUUID)
	return func(userID string) ([]byte, bool) {
		viewer, ok := viewers.get(userID)
		if !ok {
			return nil, false
		}
		visible := make([]workspaceRepoRef, 0, len(repos))
		for _, entry := range repos {
			if viewer.canSeeRepo(entry, repoProjects[entry.URL]) {
				visible = append(visible, entry)
			}
		}
		out, ok := cloneEnvelopeWithRepos(envelope, payload, workspace, visible)
		if !ok {
			return nil, false
		}
		return out, true
	}
}

// suppressEveryRecipient is the decision for a frame whose sharing facts could
// not be established. The visibility layer's rule is that an unanswerable
// question is "sees nothing", never "sees everything".
func suppressEveryRecipient(string) ([]byte, bool) { return nil, false }

// recipientViewers memoizes one workspace's viewer per recipient for the
// lifetime of a single broadcast, so several connections belonging to the same
// user cost one member lookup and one project list.
type recipientViewers struct {
	handler *Handler
	ctx     context.Context
	wsUUID  pgtype.UUID

	mu     sync.Mutex
	loaded map[string]visibilityViewer
	failed map[string]struct{}
}

func newRecipientViewers(h *Handler, ctx context.Context, wsUUID pgtype.UUID) *recipientViewers {
	return &recipientViewers{
		handler: h,
		ctx:     ctx,
		wsUUID:  wsUUID,
		loaded:  map[string]visibilityViewer{},
		failed:  map[string]struct{}{},
	}
}

// get reports whether the recipient's sharing facts could be established. A
// failure — unknown user, not a member, database error — is reported as false,
// and every caller must read that as "cannot see it".
func (r *recipientViewers) get(userID string) (visibilityViewer, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if viewer, ok := r.loaded[userID]; ok {
		return viewer, true
	}
	if _, ok := r.failed[userID]; ok {
		return visibilityViewer{}, false
	}
	uid, err := parseUUIDSafe(userID)
	if err != nil {
		r.failed[userID] = struct{}{}
		return visibilityViewer{}, false
	}
	viewer, err := r.handler.visibilityViewerForUser(r.ctx, r.wsUUID, uid)
	if err != nil {
		r.failed[userID] = struct{}{}
		return visibilityViewer{}, false
	}
	r.loaded[userID] = viewer
	return viewer, true
}

// decodeRealtimeEnvelope parses the frame the event listener marshals:
// {"type": ..., "payload": {...}, "actor_id": ..., "actor_type": ...}.
func decodeRealtimeEnvelope(frame []byte) (map[string]any, bool) {
	if len(frame) == 0 {
		return nil, false
	}
	var envelope map[string]any
	if err := json.Unmarshal(frame, &envelope); err != nil {
		return nil, false
	}
	if _, ok := envelope["type"].(string); !ok {
		return nil, false
	}
	return envelope, true
}

// cloneEnvelopeWithRepos copies the three levels this rewrite touches and
// swaps in the recipient's repo slice. The parsed envelope is shared by every
// recipient of the broadcast, so mutating it in place would hand the first
// recipient's answer to the rest.
func cloneEnvelopeWithRepos(envelope, payload, workspace map[string]any, visible []workspaceRepoRef) ([]byte, bool) {
	envelopeCopy := make(map[string]any, len(envelope))
	for k, v := range envelope {
		envelopeCopy[k] = v
	}
	payloadCopy := make(map[string]any, len(payload))
	for k, v := range payload {
		payloadCopy[k] = v
	}
	workspaceCopy := make(map[string]any, len(workspace))
	for k, v := range workspace {
		workspaceCopy[k] = v
	}
	workspaceCopy["repos"] = visible
	payloadCopy["workspace"] = workspaceCopy
	envelopeCopy["payload"] = payloadCopy

	out, err := json.Marshal(envelopeCopy)
	if err != nil {
		return nil, false
	}
	return out, true
}

// realtimeIssueEvent is the list of event types that name one issue. It is the
// delivery-time twin of the producers in server/pkg/protocol and of the client
// handler table in packages/core/realtime/use-realtime-sync.ts; an event that
// carries an issue's title, body, comment or id belongs here.
func realtimeIssueEvent(eventType string) bool {
	switch eventType {
	case protocol.EventIssueCreated,
		protocol.EventIssueUpdated,
		protocol.EventIssueDeleted,
		protocol.EventIssueMetadataChanged,
		protocol.EventIssueAttachmentsChanged,
		protocol.EventCommentCreated,
		protocol.EventCommentUpdated,
		protocol.EventCommentDeleted,
		protocol.EventCommentResolved,
		protocol.EventCommentUnresolved,
		protocol.EventReactionAdded,
		protocol.EventReactionRemoved,
		protocol.EventIssueReactionAdded,
		protocol.EventIssueReactionRemoved,
		protocol.EventIssueLabelsChanged,
		protocol.EventIssuePropertiesChanged,
		protocol.EventSubscriberAdded,
		protocol.EventSubscriberRemoved,
		protocol.EventPinCreated,
		protocol.EventPinDeleted,
		protocol.EventActivityCreated:
		return true
	default:
		return false
	}
}

// realtimeIssueID pulls the issue a payload refers to. The payload shapes are
// the producers' — a full issue object, a comment carrying its issue, or the
// bare id every satellite event publishes.
func realtimeIssueID(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if id := stringField(payload, "issue_id"); id != "" {
		return id
	}
	if comment, ok := payload["comment"].(map[string]any); ok {
		if id := stringField(comment, "issue_id"); id != "" {
			return id
		}
	}
	if issue, ok := payload["issue"].(map[string]any); ok {
		if id := stringField(issue, "id"); id != "" {
			return id
		}
	}
	// A pin names its target as item_type/item_id, both nested (pin:created)
	// and flat (pin:deleted).
	if pin, ok := payload["pin"].(map[string]any); ok && stringField(pin, "item_type") == "issue" {
		if id := stringField(pin, "item_id"); id != "" {
			return id
		}
	}
	if stringField(payload, "item_type") == "issue" {
		if id := stringField(payload, "item_id"); id != "" {
			return id
		}
	}
	return ""
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	value, _ := m[key].(string)
	return value
}

// publishIssueInvalidated emits the id-only invalidation frame. Nothing but
// ids ever travels in it — that is what makes it safe to deliver without the
// content filter — and a recipient_id turns it into a personal frame for the
// one person whose access changed.
//
// The listener in server/cmd/server/listeners.go routes the two shapes.
func (h *Handler) publishIssueInvalidated(workspaceID, actorType, actorID, issueID, projectID, recipientID string) {
	payload := map[string]any{}
	if issueID != "" {
		payload["issue_id"] = issueID
	}
	if projectID != "" {
		payload["project_id"] = projectID
	}
	if recipientID != "" {
		payload["recipient_id"] = recipientID
	}
	if len(payload) == 0 {
		return
	}
	h.publish(protocol.EventIssueInvalidated, workspaceID, actorType, actorID, payload)
}

// publishWorkspaceSnapshot republishes the workspace object after a write that
// changed repositories. The repos are embedded JSON rather than rows and each
// recipient's copy is narrowed at delivery time by narrowWorkspaceRepos, so the
// frame is what tells a client to refetch a snapshot whose repo list may have
// just become longer (or shorter) for them.
func (h *Handler) publishWorkspaceSnapshot(ctx context.Context, wsUUID pgtype.UUID, workspaceID, actorType, actorID string) {
	ws, err := h.Queries.GetWorkspace(ctx, wsUUID)
	if err != nil {
		return
	}
	h.publish(protocol.EventWorkspaceUpdated, workspaceID, actorType, actorID, map[string]any{
		"workspace": h.workspaceToResponse(ws),
	})
}

// invalidateFormerAssignee tells a member who was just moved off an issue that
// their cached copy may no longer be theirs to see. It is a targeted frame,
// not a workspace broadcast, because nobody else's access moved.
//
// The predicate compares the stored rows rather than the request: the API
// expresses "unassign" as explicit nulls, so the assignee_changed flag the
// client reads — gated on a non-nil request field — is false for exactly the
// write this covers.
//
// The ownership question is then asked about the NEW row. When the facts
// cannot be established the frame is still sent — it carries no content and
// the recipient previously had access, so over-sending is safe.
func (h *Handler) invalidateFormerAssignee(ctx context.Context, prev, updated db.Issue, actorType, actorID string) {
	if prev.AssigneeType.String != "member" || !prev.AssigneeID.Valid {
		return
	}
	if updated.AssigneeType.String == "member" && uuidToString(updated.AssigneeID) == uuidToString(prev.AssigneeID) {
		return
	}
	recipientID := uuidToString(prev.AssigneeID)
	if recipientID == "" || recipientID == actorID {
		return
	}
	wsID := uuidToString(updated.WorkspaceID)
	if uid, err := parseUUIDSafe(recipientID); err == nil {
		if viewer, viewerErr := h.visibilityViewerForUser(ctx, updated.WorkspaceID, uid); viewerErr == nil && viewer.canSeeIssue(updated) {
			return
		}
	}
	h.publishIssueInvalidated(wsID, actorType, actorID, uuidToString(updated.ID), "", recipientID)
}
