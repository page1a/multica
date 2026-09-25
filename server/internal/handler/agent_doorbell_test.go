package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// DENE-808 acceptance: doorbell rings, approval / decline, timed passes.

func doorbellMentionContent(agentID string) string {
	return fmt.Sprintf("[@Private](mention://agent/%s) please help with this", agentID)
}

// memberMentionsAgent posts a comment as memberID mentioning agentID and
// returns the saved comment's outcomes.
func memberMentionsAgent(t *testing.T, memberID, issueID, agentID string) CommentResponse {
	t.Helper()
	w := httptest.NewRecorder()
	r := withURLParam(newRequestAs(memberID, http.MethodPost, "/api/issues/"+issueID+"/comments", map[string]any{"content": doorbellMentionContent(agentID)}), "id", issueID)
	testHandler.CreateComment(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp CommentResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode comment: %v", err)
	}
	return resp
}

// doorbellIssue creates a workspace-visible issue: new issues are private to
// their creator (DENE-698), and the ringing member must be able to see it.
func doorbellIssue(t *testing.T, title string) string {
	t.Helper()
	issueID := createCommentTriggerPreviewIssue(t, title, "", "")
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET visibility = 'workspace' WHERE id = $1`, issueID); err != nil {
		t.Fatalf("make issue workspace-visible: %v", err)
	}
	return issueID
}

func pendingDoorbellRequestID(t *testing.T, agentID, memberID, issueID string) string {
	t.Helper()
	var id string
	err := testPool.QueryRow(context.Background(), `
		SELECT id FROM agent_access_request
		WHERE agent_id = $1 AND requester_id = $2 AND issue_id = $3 AND status = 'pending'
	`, agentID, memberID, issueID).Scan(&id)
	if err != nil {
		return ""
	}
	return id
}

func countDoorbellInbox(t *testing.T, recipientID, itemType string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM inbox_item WHERE recipient_id = $1 AND type = $2
	`, recipientID, itemType).Scan(&n); err != nil {
		t.Fatalf("count inbox: %v", err)
	}
	return n
}

// armDoorbell turns the fixture agent's doorbell on (default off) and
// registers cleanup for everything a ring can leave behind.
func armDoorbell(t *testing.T, agentID string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `UPDATE agent SET doorbell_enabled = true WHERE id = $1`, agentID); err != nil {
		t.Fatalf("enable doorbell: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		testPool.Exec(ctx, `DELETE FROM inbox_item WHERE type LIKE 'agent_access_%' AND details->>'agent_id' = $1`, agentID)
		testPool.Exec(ctx, `DELETE FROM agent_access_pass WHERE agent_id = $1`, agentID)
		testPool.Exec(ctx, `DELETE FROM agent_access_request WHERE agent_id = $1`, agentID)
	})
}

func TestDoorbell_MentionRingsInsteadOfRefusing(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	agentID, ownerID, memberID := privateAgentTestFixture(t)
	armDoorbell(t, agentID)
	issueID := doorbellIssue(t, "doorbell mention")

	resp := memberMentionsAgent(t, memberID, issueID, agentID)
	out := findCommentOutcome(t, resp.TriggerOutcomes, agentID)
	if out.Status != DispatchBlocked || out.ReasonCode != ReasonAccessRequested {
		t.Fatalf("outcome = %+v, want blocked/access_requested", out)
	}
	if got := countQueuedCommentTriggerTasks(t, issueID, agentID); got != 0 {
		t.Fatalf("queued tasks = %d, want 0 before approval", got)
	}
	reqID := pendingDoorbellRequestID(t, agentID, memberID, issueID)
	if reqID == "" {
		t.Fatal("no pending access request recorded")
	}
	if got := countDoorbellInbox(t, ownerID, inboxTypeAgentAccessRequest); got != 1 {
		t.Fatalf("owner inbox rings = %d, want 1", got)
	}

	// Ringing again on the same issue refreshes, never stacks.
	memberMentionsAgent(t, memberID, issueID, agentID)
	if got := countDoorbellInbox(t, ownerID, inboxTypeAgentAccessRequest); got != 1 {
		t.Fatalf("owner inbox rings after re-ring = %d, want 1", got)
	}

	// A stranger cannot resolve it.
	w := httptest.NewRecorder()
	testHandler.ApproveAgentAccessRequest(w, withURLParam(newRequestAs(memberID, http.MethodPost, "/api/agent-access-requests/"+reqID+"/approve", nil), "id", reqID))
	if w.Code != http.StatusForbidden {
		t.Fatalf("requester approving own ring: got %d, want 403", w.Code)
	}

	// Owner approves once (no pass): the mention replays into a task.
	w = httptest.NewRecorder()
	testHandler.ApproveAgentAccessRequest(w, withURLParam(newRequestAs(ownerID, http.MethodPost, "/api/agent-access-requests/"+reqID+"/approve", map[string]any{}), "id", reqID))
	if w.Code != http.StatusOK {
		t.Fatalf("approve: got %d: %s", w.Code, w.Body.String())
	}
	var approved struct {
		Request AgentAccessRequestResponse `json:"request"`
		Pass    *AgentAccessPassResponse   `json:"pass"`
		Replay  DispatchOutcome            `json:"replay"`
	}
	if err := json.NewDecoder(w.Body).Decode(&approved); err != nil {
		t.Fatalf("decode approve: %v", err)
	}
	if approved.Request.Status != "approved" || approved.Pass != nil || approved.Replay.Status != DispatchQueued {
		t.Fatalf("approve response = %+v", approved)
	}
	if got := countQueuedCommentTriggerTasks(t, issueID, agentID); got != 1 {
		t.Fatalf("queued tasks after approval = %d, want 1", got)
	}
	if got := countDoorbellInbox(t, memberID, inboxTypeAgentAccessApproved); got != 1 {
		t.Fatalf("requester approved receipts = %d, want 1", got)
	}
	// Second approve is a no-op conflict.
	w = httptest.NewRecorder()
	testHandler.ApproveAgentAccessRequest(w, withURLParam(newRequestAs(ownerID, http.MethodPost, "/api/agent-access-requests/"+reqID+"/approve", nil), "id", reqID))
	if w.Code != http.StatusConflict {
		t.Fatalf("re-approve: got %d, want 409", w.Code)
	}
	// Without a pass the next mention rings again.
	issue2 := doorbellIssue(t, "doorbell mention 2")
	resp2 := memberMentionsAgent(t, memberID, issue2, agentID)
	if out := findCommentOutcome(t, resp2.TriggerOutcomes, agentID); out.ReasonCode != ReasonAccessRequested {
		t.Fatalf("second issue outcome = %+v, want access_requested (one-off approval must not persist)", out)
	}
}

func TestDoorbell_DeclineSendsReceipt(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	agentID, ownerID, memberID := privateAgentTestFixture(t)
	armDoorbell(t, agentID)
	issueID := doorbellIssue(t, "doorbell decline")
	memberMentionsAgent(t, memberID, issueID, agentID)
	reqID := pendingDoorbellRequestID(t, agentID, memberID, issueID)
	if reqID == "" {
		t.Fatal("no pending access request recorded")
	}
	w := httptest.NewRecorder()
	testHandler.DeclineAgentAccessRequest(w, withURLParam(newRequestAs(ownerID, http.MethodPost, "/api/agent-access-requests/"+reqID+"/decline", nil), "id", reqID))
	if w.Code != http.StatusOK {
		t.Fatalf("decline: got %d: %s", w.Code, w.Body.String())
	}
	if got := countQueuedCommentTriggerTasks(t, issueID, agentID); got != 0 {
		t.Fatalf("queued tasks after decline = %d, want 0", got)
	}
	if got := countDoorbellInbox(t, memberID, inboxTypeAgentAccessDeclined); got != 1 {
		t.Fatalf("requester declined receipts = %d, want 1", got)
	}
	var status string
	testPool.QueryRow(context.Background(), `SELECT status FROM agent_access_request WHERE id = $1`, reqID).Scan(&status)
	if status != "declined" {
		t.Fatalf("request status = %q, want declined", status)
	}
}

func TestDoorbell_OffMeansPlainRefusal(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	agentID, ownerID, memberID := privateAgentTestFixture(t)
	armDoorbell(t, agentID)
	if _, err := testPool.Exec(context.Background(), `UPDATE agent SET doorbell_enabled = false WHERE id = $1`, agentID); err != nil {
		t.Fatal(err)
	}
	issueID := doorbellIssue(t, "doorbell off")
	resp := memberMentionsAgent(t, memberID, issueID, agentID)
	if out := findCommentOutcome(t, resp.TriggerOutcomes, agentID); out.ReasonCode != ReasonInvocationNotAllowed {
		t.Fatalf("outcome = %+v, want invocation_not_allowed", out)
	}
	if pendingDoorbellRequestID(t, agentID, memberID, issueID) != "" {
		t.Fatal("doorbell off must not record a request")
	}
	if got := countDoorbellInbox(t, ownerID, inboxTypeAgentAccessRequest); got != 0 {
		t.Fatalf("owner inbox rings = %d, want 0", got)
	}
}

func TestDoorbell_PassAdmitsUntilExpiryOrRevoke(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	agentID, ownerID, memberID := privateAgentTestFixture(t)
	armDoorbell(t, agentID)

	// Only the owner can issue passes.
	w := httptest.NewRecorder()
	testHandler.CreateAgentAccessPass(w, withURLParam(newRequestAs(memberID, http.MethodPost, "/api/agents/"+agentID+"/access-passes", map[string]any{"user_id": memberID, "duration_minutes": 120}), "id", agentID))
	if w.Code != http.StatusForbidden {
		t.Fatalf("member issuing pass: got %d, want 403", w.Code)
	}
	w = httptest.NewRecorder()
	testHandler.CreateAgentAccessPass(w, withURLParam(newRequestAs(ownerID, http.MethodPost, "/api/agents/"+agentID+"/access-passes", map[string]any{"user_id": memberID, "duration_minutes": 120}), "id", agentID))
	if w.Code != http.StatusCreated {
		t.Fatalf("issue pass: got %d: %s", w.Code, w.Body.String())
	}
	var pass AgentAccessPassResponse
	if err := json.NewDecoder(w.Body).Decode(&pass); err != nil {
		t.Fatal(err)
	}
	if !pass.Active || pass.UserID != memberID {
		t.Fatalf("pass = %+v", pass)
	}

	// Valid pass: the mention goes straight through, no ring.
	issueID := doorbellIssue(t, "doorbell pass")
	resp := memberMentionsAgent(t, memberID, issueID, agentID)
	if out := findCommentOutcome(t, resp.TriggerOutcomes, agentID); out.Status != DispatchQueued {
		t.Fatalf("outcome with pass = %+v, want queued", out)
	}
	if got := countQueuedCommentTriggerTasks(t, issueID, agentID); got != 1 {
		t.Fatalf("queued tasks with pass = %d, want 1", got)
	}
	if pendingDoorbellRequestID(t, agentID, memberID, issueID) != "" {
		t.Fatal("a valid pass must not ring")
	}
	// The pass holder can also open the agent and sees it listed.
	w = httptest.NewRecorder()
	testHandler.GetAgent(w, withURLParam(newRequestAs(memberID, http.MethodGet, "/api/agents/"+agentID, nil), "id", agentID))
	if w.Code != http.StatusOK {
		t.Fatalf("pass holder GetAgent: got %d", w.Code)
	}
	w = httptest.NewRecorder()
	testHandler.ListAgents(w, newRequestAs(memberID, http.MethodGet, "/api/agents", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), agentID) {
		t.Fatalf("pass holder ListAgents: got %d, agent listed=%v", w.Code, strings.Contains(w.Body.String(), agentID))
	}

	// Owner sees it in the list.
	w = httptest.NewRecorder()
	testHandler.ListAgentAccessPasses(w, withURLParam(newRequestAs(ownerID, http.MethodGet, "/api/agents/"+agentID+"/access-passes", nil), "id", agentID))
	if w.Code != http.StatusOK {
		t.Fatalf("list passes: got %d", w.Code)
	}
	var listed []AgentAccessPassResponse
	if err := json.NewDecoder(w.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != pass.ID || !listed[0].Active {
		t.Fatalf("listed passes = %+v", listed)
	}

	// Expired pass: back to the doorbell.
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_access_pass SET expires_at = now() - interval '1 minute' WHERE id = $1`, pass.ID); err != nil {
		t.Fatal(err)
	}
	issue2 := doorbellIssue(t, "doorbell pass expired")
	resp2 := memberMentionsAgent(t, memberID, issue2, agentID)
	if out := findCommentOutcome(t, resp2.TriggerOutcomes, agentID); out.ReasonCode != ReasonAccessRequested {
		t.Fatalf("outcome with expired pass = %+v, want access_requested", out)
	}

	// Fresh pass, then revoke: immediate.
	w = httptest.NewRecorder()
	testHandler.CreateAgentAccessPass(w, withURLParam(newRequestAs(ownerID, http.MethodPost, "/api/agents/"+agentID+"/access-passes", map[string]any{"user_id": memberID, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}), "id", agentID))
	if w.Code != http.StatusCreated {
		t.Fatalf("issue pass 2: got %d: %s", w.Code, w.Body.String())
	}
	var pass2 AgentAccessPassResponse
	json.NewDecoder(w.Body).Decode(&pass2)
	w = httptest.NewRecorder()
	testHandler.RevokeAgentAccessPass(w, withURLParams(newRequestAs(ownerID, http.MethodDelete, "/api/agents/"+agentID+"/access-passes/"+pass2.ID, nil), "id", agentID, "passId", pass2.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("revoke: got %d: %s", w.Code, w.Body.String())
	}
	issue3 := doorbellIssue(t, "doorbell pass revoked")
	resp3 := memberMentionsAgent(t, memberID, issue3, agentID)
	if out := findCommentOutcome(t, resp3.TriggerOutcomes, agentID); out.ReasonCode != ReasonAccessRequested {
		t.Fatalf("outcome after revoke = %+v, want access_requested", out)
	}
	if got := countQueuedCommentTriggerTasks(t, issue3, agentID); got != 0 {
		t.Fatalf("queued tasks after revoke = %d, want 0", got)
	}
}

func TestDoorbell_ApproveWithPassGrantsIt(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	agentID, ownerID, memberID := privateAgentTestFixture(t)
	armDoorbell(t, agentID)
	issueID := doorbellIssue(t, "doorbell approve with pass")
	memberMentionsAgent(t, memberID, issueID, agentID)
	reqID := pendingDoorbellRequestID(t, agentID, memberID, issueID)
	w := httptest.NewRecorder()
	testHandler.ApproveAgentAccessRequest(w, withURLParam(newRequestAs(ownerID, http.MethodPost, "/api/agent-access-requests/"+reqID+"/approve", map[string]any{"pass_duration_minutes": 120}), "id", reqID))
	if w.Code != http.StatusOK {
		t.Fatalf("approve: got %d: %s", w.Code, w.Body.String())
	}
	var approved struct {
		Pass *AgentAccessPassResponse `json:"pass"`
	}
	json.NewDecoder(w.Body).Decode(&approved)
	if approved.Pass == nil || !approved.Pass.Active {
		t.Fatalf("approve with pass returned %+v", approved.Pass)
	}
	issue2 := doorbellIssue(t, "doorbell after pass")
	resp := memberMentionsAgent(t, memberID, issue2, agentID)
	if out := findCommentOutcome(t, resp.TriggerOutcomes, agentID); out.Status != DispatchQueued {
		t.Fatalf("outcome after pass = %+v, want queued", out)
	}
}

func TestDoorbell_AssignRingsAndApprovalAssigns(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	agentID, ownerID, memberID := privateAgentTestFixture(t)
	armDoorbell(t, agentID)
	issueID := doorbellIssue(t, "doorbell assign")

	w := httptest.NewRecorder()
	testHandler.UpdateIssue(w, withURLParam(newRequestAs(memberID, http.MethodPut, "/api/issues/"+issueID, map[string]any{"assignee_type": "agent", "assignee_id": agentID}), "id", issueID))
	if w.Code != http.StatusForbidden {
		t.Fatalf("member assign: got %d: %s", w.Code, w.Body.String())
	}
	var blocked dispatchBlockedResponse
	json.NewDecoder(w.Body).Decode(&blocked)
	if blocked.ReasonCode != ReasonAccessRequested {
		t.Fatalf("assign reason = %q, want access_requested", blocked.ReasonCode)
	}
	var assignee *string
	testPool.QueryRow(context.Background(), `SELECT assignee_id::text FROM issue WHERE id = $1`, issueID).Scan(&assignee)
	if assignee != nil {
		t.Fatalf("issue must stay unassigned until approval, got %v", *assignee)
	}
	reqID := pendingDoorbellRequestID(t, agentID, memberID, issueID)
	if reqID == "" {
		t.Fatal("no pending assign request")
	}
	var kind string
	testPool.QueryRow(context.Background(), `SELECT trigger_kind FROM agent_access_request WHERE id = $1`, reqID).Scan(&kind)
	if kind != "assign" {
		t.Fatalf("trigger_kind = %q, want assign", kind)
	}

	w = httptest.NewRecorder()
	testHandler.ApproveAgentAccessRequest(w, withURLParam(newRequestAs(ownerID, http.MethodPost, "/api/agent-access-requests/"+reqID+"/approve", nil), "id", reqID))
	if w.Code != http.StatusOK {
		t.Fatalf("approve assign: got %d: %s", w.Code, w.Body.String())
	}
	testPool.QueryRow(context.Background(), `SELECT assignee_id::text FROM issue WHERE id = $1`, issueID).Scan(&assignee)
	if assignee == nil || *assignee != agentID {
		t.Fatalf("issue assignee after approval = %v, want %s", assignee, agentID)
	}
	if got := countQueuedCommentTriggerTasks(t, issueID, agentID); got != 1 {
		t.Fatalf("queued tasks after assign approval = %d, want 1", got)
	}
}

func TestDoorbell_ListRequestsAndOwnerOnlyToggle(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	agentID, ownerID, memberID := privateAgentTestFixture(t)
	armDoorbell(t, agentID)
	issueID := doorbellIssue(t, "doorbell list")
	memberMentionsAgent(t, memberID, issueID, agentID)

	w := httptest.NewRecorder()
	testHandler.ListAgentAccessRequests(w, newRequestAs(ownerID, http.MethodGet, "/api/agent-access-requests", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list: got %d: %s", w.Code, w.Body.String())
	}
	var lists struct {
		Incoming []AgentAccessRequestResponse `json:"incoming"`
		Outgoing []AgentAccessRequestResponse `json:"outgoing"`
	}
	json.NewDecoder(w.Body).Decode(&lists)
	if len(lists.Incoming) != 1 || lists.Incoming[0].Status != "pending" || lists.Incoming[0].RequesterID != memberID || len(lists.Outgoing) != 0 {
		t.Fatalf("owner lists = %+v", lists)
	}
	w = httptest.NewRecorder()
	testHandler.ListAgentAccessRequests(w, newRequestAs(memberID, http.MethodGet, "/api/agent-access-requests", nil))
	json.NewDecoder(w.Body).Decode(&lists)
	if len(lists.Outgoing) != 1 || len(lists.Incoming) != 0 {
		t.Fatalf("requester lists = %+v", lists)
	}

	// Expired rings flip lazily on read.
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_access_request SET expires_at = now() - interval '1 minute' WHERE agent_id = $1`, agentID); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	testHandler.ListAgentAccessRequests(w, newRequestAs(ownerID, http.MethodGet, "/api/agent-access-requests", nil))
	json.NewDecoder(w.Body).Decode(&lists)
	if len(lists.Incoming) != 1 || lists.Incoming[0].Status != "expired" {
		t.Fatalf("after expiry lists = %+v", lists)
	}

	// A doorbell-enabled agent is listed to the member (so they can @ it)
	// but its detail page stays closed until they hold a pass.
	w = httptest.NewRecorder()
	testHandler.ListAgents(w, newRequestAs(memberID, http.MethodGet, "/api/agents", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), agentID) {
		t.Fatalf("doorbell agent listed to member: got %d, listed=%v", w.Code, strings.Contains(w.Body.String(), agentID))
	}
	w = httptest.NewRecorder()
	testHandler.GetAgent(w, withURLParam(newRequestAs(memberID, http.MethodGet, "/api/agents/"+agentID, nil), "id", agentID))
	if w.Code != http.StatusForbidden {
		t.Fatalf("doorbell agent detail for member: got %d, want 403", w.Code)
	}

	// doorbell_enabled is owner-only; the workspace owner (testUserID, admin
	// of this agent but not its owner) cannot flip it.
	w = httptest.NewRecorder()
	testHandler.UpdateAgent(w, withURLParam(newRequest(http.MethodPut, "/api/agents/"+agentID, map[string]any{"doorbell_enabled": false}), "id", agentID))
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-owner toggling doorbell: got %d: %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	testHandler.UpdateAgent(w, withURLParam(newRequestAs(ownerID, http.MethodPut, "/api/agents/"+agentID, map[string]any{"doorbell_enabled": false}), "id", agentID))
	if w.Code != http.StatusOK {
		t.Fatalf("owner toggling doorbell: got %d: %s", w.Code, w.Body.String())
	}
	var updated AgentResponse
	json.NewDecoder(w.Body).Decode(&updated)
	if updated.DoorbellEnabled {
		t.Fatal("doorbell_enabled should be false after owner update")
	}
}

// TestDoorbell_RingSurvivesIssueInvisibleToOwner: the doorbell is a personal
// notice. The owner must hear it — list and unread badge — even when the
// ticket it rang on is private to the requester and not shared with them.
func TestDoorbell_RingSurvivesIssueInvisibleToOwner(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	agentID, ownerID, memberID := privateAgentTestFixture(t)
	armDoorbell(t, agentID)
	issueID := createCommentTriggerPreviewIssue(t, "doorbell on a private ticket", "", "")
	// Private to the ringing member: the owner is neither creator, assignee,
	// nor a share target, so ordinary issue notifications would be filtered.
	if _, err := testPool.Exec(context.Background(),
		`UPDATE issue SET visibility = 'private', creator_type = 'member', creator_id = $2 WHERE id = $1`, issueID, memberID); err != nil {
		t.Fatalf("make issue private to member: %v", err)
	}

	resp := memberMentionsAgent(t, memberID, issueID, agentID)
	if out := findCommentOutcome(t, resp.TriggerOutcomes, agentID); out.ReasonCode != ReasonAccessRequested {
		t.Fatalf("outcome = %+v, want access_requested", out)
	}

	ownerInbox := func(method, path string) *http.Request {
		r := newRequestAs(ownerID, method, path, nil)
		r.Header.Set("X-Workspace-ID", testWorkspaceID)
		return r
	}
	w := httptest.NewRecorder()
	inboxWorkspaceHandler(testHandler.ListInbox)(w, ownerInbox(http.MethodGet, "/api/inbox"))
	if w.Code != http.StatusOK {
		t.Fatalf("ListInbox: got %d: %s", w.Code, w.Body.String())
	}
	var items []InboxItemResponse
	if err := json.NewDecoder(w.Body).Decode(&items); err != nil {
		t.Fatalf("decode inbox: %v", err)
	}
	rings := 0
	for _, it := range items {
		if it.Type == inboxTypeAgentAccessRequest && it.IssueID != nil && *it.IssueID == issueID {
			rings++
		}
	}
	if rings != 1 {
		t.Fatalf("owner sees %d doorbell rings for the private ticket, want 1 (items=%d)", rings, len(items))
	}

	w = httptest.NewRecorder()
	inboxWorkspaceHandler(testHandler.CountUnreadInbox)(w, ownerInbox(http.MethodGet, "/api/inbox/unread-count"))
	if w.Code != http.StatusOK {
		t.Fatalf("CountUnreadInbox: got %d: %s", w.Code, w.Body.String())
	}
	var count struct {
		Count int64 `json:"count"`
	}
	if err := json.NewDecoder(w.Body).Decode(&count); err != nil {
		t.Fatalf("decode count: %v", err)
	}
	if count.Count < 1 {
		t.Fatalf("owner unread count = %d, want >= 1 (the ring must light the badge)", count.Count)
	}
}
