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

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// enableChildDoneRule turns the child_done system rule on for the test
// workspace for one test. In this fork the rule is opt-in (the handler's
// child-done path is the default, see childDoneRuleOwnsParent), so the
// upstream rule's tests opt in.
func enableChildDoneRule(t *testing.T) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `UPDATE workspace SET settings = COALESCE(settings, '{}'::jsonb) || '{"system_wakeup_child_done": true}'::jsonb WHERE id = $1`, testWorkspaceID); err != nil {
		t.Fatalf("enable child_done rule: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `UPDATE workspace SET settings = settings - 'system_wakeup_child_done' WHERE id = $1`, testWorkspaceID)
	})
}

func cleanupChildDoneIssue(id string) {
	ctx := context.Background()
	for _, sql := range []string{
		`DELETE FROM agent_task_queue WHERE issue_id = $1`,
		`DELETE FROM issue_wakeup_receipt WHERE wakeup_id IN (SELECT id FROM issue_wakeup WHERE issue_id = $1)`,
		`DELETE FROM issue_wakeup WHERE issue_id = $1`,
		`DELETE FROM issue_child_event WHERE parent_id = $1 OR child_id = $1`,
		`DELETE FROM inbox_item WHERE issue_id = $1`,
		`DELETE FROM activity_log WHERE issue_id = $1`,
		`DELETE FROM issue WHERE id = $1`,
	} {
		testPool.Exec(ctx, sql, id)
	}
}

// childDoneEntry is one timeline entry the rule wrote on the parent.
type childDoneEntry struct {
	Stage      *int32 `json:"stage"`
	Total      int    `json:"total"`
	Outcome    string `json:"outcome"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	TaskID     string `json:"task_id"`
}

func childDoneEntries(t *testing.T, parentID string) []childDoneEntry {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT details FROM activity_log
		WHERE issue_id = $1 AND action = 'wakeup_triggered' AND details->>'rule' = 'child_done'
		ORDER BY created_at, id`, parentID)
	if err != nil {
		t.Fatalf("read child_done entries: %v", err)
	}
	defer rows.Close()
	entries := []childDoneEntry{}
	for rows.Next() {
		var raw []byte
		var entry childDoneEntry
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	return entries
}

type childDoneRunRow struct {
	ID, AgentID, SquadID, Note, Status, WakeupID string
	Leader                                       bool
}

// childDoneRuns lists the runs the parent's rule started, oldest first.
func childDoneRuns(t *testing.T, parentID string) []childDoneRunRow {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT id::text, agent_id::text, COALESCE(squad_id::text, ''),
		COALESCE(handoff_note, ''), status, context->>'wakeup_id', COALESCE(is_leader_task, false)
		FROM agent_task_queue WHERE issue_id = $1 AND context->>'wakeup_system' = 'child_done'
		ORDER BY created_at, id`, parentID)
	if err != nil {
		t.Fatalf("read child_done runs: %v", err)
	}
	defer rows.Close()
	runs := []childDoneRunRow{}
	for rows.Next() {
		var r childDoneRunRow
		if err := rows.Scan(&r.ID, &r.AgentID, &r.SquadID, &r.Note, &r.Status, &r.WakeupID, &r.Leader); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, r)
	}
	return runs
}

// runWakeupTick is one scheduler pass over this package's ready wakeups. It is
// scoped to the test workspace: other packages' tests share the database, and
// dispatching their rules would consume receipts they are asserting on.
func runWakeupTick(t *testing.T) {
	t.Helper()
	if err := (&service.IssueWakeupService{Tasks: testHandler.TaskService}).TickWorkspaces(context.Background(), parseUUID(testWorkspaceID)); err != nil {
		t.Logf("wakeup tick: %v", err)
	}
}

// The parent's agent is woken by an ordinary wakeup run: the platform's
// instruction and the stage facts ride in its [WAKEUP] block, and the
// timeline entry names the run.
func TestChildDoneWakesAgentAssignee(t *testing.T) {
	enableChildDoneRule(t)
	fx := newChildDoneFixture(t, "in_progress")
	agentID := handlerTestAgentID(t)
	setIssueAssigneeDirect(t, fx.parent.ID, "agent", agentID)

	updateChildStatus(t, fx.child.ID, "done")

	runs := childDoneRuns(t, fx.parent.ID)
	if len(runs) != 1 || runs[0].AgentID != agentID || runs[0].Leader || runs[0].WakeupID == "" {
		t.Fatalf("runs = %+v, want one run for the parent's agent", runs)
	}
	for _, want := range []string{service.ChildDoneDefaultInstruction, "condition.met", `"all":true`} {
		if !strings.Contains(runs[0].Note, want) {
			t.Errorf("wakeup note lacks %q:\n%s", want, runs[0].Note)
		}
	}
	entries := childDoneEntries(t, fx.parent.ID)
	if len(entries) != 1 || entries[0].Outcome != "woke" || entries[0].TargetType != "agent" || entries[0].TargetID != agentID ||
		entries[0].TaskID != runs[0].ID || entries[0].Stage != nil || entries[0].Total != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1 AND author_type = 'system'`, fx.parent.ID); n != 0 {
		t.Fatalf("the rule posted %d system comments; it records a timeline entry instead", n)
	}
}

// Without an assignee there is nobody to wake; the milestone is still on the
// parent's timeline.
func TestChildDoneRecordsWithoutAssignee(t *testing.T) {
	enableChildDoneRule(t)
	fx := newChildDoneFixture(t, "in_progress")
	updateChildStatus(t, fx.child.ID, "done")
	entries := childDoneEntries(t, fx.parent.ID)
	if len(entries) != 1 || entries[0].Outcome != "none" || entries[0].TargetType != "" {
		t.Fatalf("entries = %+v", entries)
	}
	if runs := childDoneRuns(t, fx.parent.ID); len(runs) != 0 {
		t.Fatalf("runs = %+v, want none", runs)
	}
}

// Re-saving an already-done child is not a new fact.
func TestChildDoneRuleNotificationIsIdempotent(t *testing.T) {
	enableChildDoneRule(t)
	fx := newChildDoneFixture(t, "in_progress")
	updateChildStatus(t, fx.child.ID, "done")
	updateChildStatus(t, fx.child.ID, "done")
	if got := len(childDoneEntries(t, fx.parent.ID)); got != 1 {
		t.Fatalf("entries = %d, want 1", got)
	}
}

// done → in_progress → done is a new completion (MUL-2538).
func TestChildDoneRuleReopenAndDoneFiresAgain(t *testing.T) {
	enableChildDoneRule(t)
	fx := newChildDoneFixture(t, "in_progress")
	updateChildStatus(t, fx.child.ID, "done")
	updateChildStatus(t, fx.child.ID, "in_progress")
	updateChildStatus(t, fx.child.ID, "done")
	if got := len(childDoneEntries(t, fx.parent.ID)); got != 2 {
		t.Fatalf("entries = %d, want 2 after reopen and done", got)
	}
}

// A closed parent has nothing left to drive.
func TestChildDoneSkippedWhenParentClosed(t *testing.T) {
	enableChildDoneRule(t)
	for _, status := range []string{"done", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			fx := newChildDoneFixture(t, status)
			updateChildStatus(t, fx.child.ID, "done")
			if got := len(childDoneEntries(t, fx.parent.ID)); got != 0 {
				t.Errorf("parent %s received %d entries", status, got)
			}
		})
	}
}

// A parent parked in backlog is not woken (#4320 / MUL-3497), and nothing is
// marked as seen: leaving backlog wakes the assignee once for what closed
// meanwhile, and parking again does not repeat it.
func TestChildDoneHeldWhileParentInBacklog(t *testing.T) {
	enableChildDoneRule(t)
	fx := newChildDoneFixture(t, "backlog")
	agentID := handlerTestAgentID(t)
	setIssueAssigneeDirect(t, fx.parent.ID, "agent", agentID)

	updateChildStatus(t, fx.child.ID, "done")
	if got := len(childDoneEntries(t, fx.parent.ID)); got != 0 {
		t.Fatalf("parked parent received %d entries", got)
	}

	dbfx.Exec(t, `UPDATE issue SET status = 'todo' WHERE id = $1`, fx.parent.ID)
	runWakeupTick(t)
	entries := childDoneEntries(t, fx.parent.ID)
	if len(entries) != 1 || entries[0].Outcome != "woke" {
		t.Fatalf("leaving backlog: entries = %+v, want one wake", entries)
	}

	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE issue_id = $1`, fx.parent.ID)
	dbfx.Exec(t, `UPDATE issue SET status = 'backlog' WHERE id = $1`, fx.parent.ID)
	runWakeupTick(t)
	dbfx.Exec(t, `UPDATE issue SET status = 'todo' WHERE id = $1`, fx.parent.ID)
	runWakeupTick(t)
	if got := len(childDoneEntries(t, fx.parent.ID)); got != 1 {
		t.Fatalf("parking again repeated the wake: %d entries", got)
	}
}

// An issue without a parent creates no rule anywhere.
func TestChildDoneRuleSkippedWhenNoParent(t *testing.T) {
	enableChildDoneRule(t)
	orphan := dbfx.Issue(t, "orphan child-done", testutil.Cols{"status": "in_progress"})
	t.Cleanup(func() { cleanupChildDoneIssue(orphan) })
	updateChildStatus(t, orphan, "done")
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_wakeup WHERE issue_id = $1`, orphan); n != 0 {
		t.Fatalf("orphan got %d rules", n)
	}
}

// A member assignee reads an inbox notification instead of a run.
func TestChildDoneNotifiesMemberAssignee(t *testing.T) {
	enableChildDoneRule(t)
	fx := newChildDoneFixture(t, "in_progress")
	var userID string
	dbfx.QueryRow(t, `SELECT user_id FROM member WHERE workspace_id = $1 LIMIT 1`, testWorkspaceID).Scan(&userID)
	setIssueAssigneeDirect(t, fx.parent.ID, "member", userID)

	updateChildStatus(t, fx.child.ID, "done")

	if n := dbfx.Count(t, `SELECT count(*) FROM inbox_item WHERE recipient_id = $1 AND issue_id = $2 AND type = 'children_done'`, userID, fx.parent.ID); n != 1 {
		t.Fatalf("children_done inbox items = %d, want 1", n)
	}
	if runs := childDoneRuns(t, fx.parent.ID); len(runs) != 0 {
		t.Fatalf("member parent started runs: %+v", runs)
	}
	entries := childDoneEntries(t, fx.parent.ID)
	if len(entries) != 1 || entries[0].Outcome != "notified" || entries[0].TargetType != "member" || entries[0].TargetID != userID {
		t.Fatalf("entries = %+v", entries)
	}
}

// A squad parent wakes its leader in the leader role; it does not fan out to
// members.
func TestChildDoneWakesSquadLeader(t *testing.T) {
	enableChildDoneRule(t)
	fx := newChildDoneFixture(t, "in_progress")
	sq := newSquadCommentTriggerFixture(t)
	setIssueAssigneeDirect(t, fx.parent.ID, "squad", sq.SquadID)

	updateChildStatus(t, fx.child.ID, "done")

	runs := childDoneRuns(t, fx.parent.ID)
	if len(runs) != 1 || runs[0].AgentID != sq.LeaderID || !runs[0].Leader || runs[0].SquadID != sq.SquadID {
		t.Fatalf("runs = %+v, want one leader run", runs)
	}
}

// Waking the parent's assignee is a hand-off between two different issues,
// never a self-loop, whoever owned the finished child (MUL-2808, MUL-3969).
func TestChildDoneWakesAssigneeWhoeverOwnedTheChild(t *testing.T) {
	enableChildDoneRule(t)
	cases := map[string]func(t *testing.T, fx childDoneFixture) string{
		"same agent owns the child": func(t *testing.T, fx childDoneFixture) string {
			agentID := handlerTestAgentID(t)
			setIssueAssigneeDirect(t, fx.parent.ID, "agent", agentID)
			setIssueAssigneeDirect(t, fx.child.ID, "agent", agentID)
			return agentID
		},
		"child squad led by the parent's agent": func(t *testing.T, fx childDoneFixture) string {
			sq := newSquadCommentTriggerFixture(t)
			setIssueAssigneeDirect(t, fx.parent.ID, "agent", sq.LeaderID)
			setIssueAssigneeDirect(t, fx.child.ID, "squad", sq.SquadID)
			return sq.LeaderID
		},
		"two squads share a leader": func(t *testing.T, fx childDoneFixture) string {
			sq := newSquadCommentTriggerFixture(t)
			other := dbfx.Squad(t, "Child Done Shared Leader Squad", sq.LeaderID)
			setIssueAssigneeDirect(t, fx.parent.ID, "squad", sq.SquadID)
			setIssueAssigneeDirect(t, fx.child.ID, "squad", other)
			return sq.LeaderID
		},
		"same squad owns the child": func(t *testing.T, fx childDoneFixture) string {
			sq := newSquadCommentTriggerFixture(t)
			setIssueAssigneeDirect(t, fx.parent.ID, "squad", sq.SquadID)
			setIssueAssigneeDirect(t, fx.child.ID, "squad", sq.SquadID)
			return sq.LeaderID
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			fx := newChildDoneFixture(t, "in_progress")
			agentID := setup(t, fx)
			updateChildStatus(t, fx.child.ID, "done")
			if got := countPendingTasksForAgent(t, fx.parent.ID, agentID); got != 1 {
				t.Fatalf("pending parent runs for the assignee = %d, want 1", got)
			}
		})
	}
}

func createUnstagedChild(t *testing.T, parentID, status string) IssueResponse {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.CreateIssue(w, newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title": "unstaged child " + time.Now().Format(time.RFC3339Nano), "status": status, "parent_issue_id": parentID,
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("create unstaged child: %d %s", w.Code, w.Body.String())
	}
	var child IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&child); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupChildDoneIssue(child.ID) })
	return child
}

// Each stage that closes while a later one waits wakes the assignee with the
// next stage named; the last stage alone does not, because the wrap-up also
// waits for unstaged sub-issues.
func TestChildDoneStagesThenWrapUp(t *testing.T) {
	enableChildDoneRule(t)
	fx := newChildDoneFixture(t, "in_progress")
	agentID := handlerTestAgentID(t)
	setIssueAssigneeDirect(t, fx.parent.ID, "agent", agentID)
	dbfx.Exec(t, `UPDATE issue SET stage = 1 WHERE id = $1`, fx.child.ID)
	second := createStagedChild(t, fx.parent.ID, 1, "in_progress")
	last := createStagedChild(t, fx.parent.ID, 2, "backlog")
	loose := createUnstagedChild(t, fx.parent.ID, "todo")
	completeRuns := func() {
		dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE issue_id = $1`, fx.parent.ID)
	}

	updateChildStatus(t, fx.child.ID, "done")
	if got := len(childDoneEntries(t, fx.parent.ID)); got != 0 {
		t.Fatalf("half a stage fired %d entries", got)
	}
	updateChildStatus(t, second.ID, "cancelled")
	entries := childDoneEntries(t, fx.parent.ID)
	if len(entries) != 1 || entries[0].Stage == nil || *entries[0].Stage != 1 || entries[0].Total != 2 || entries[0].Outcome != "woke" {
		t.Fatalf("stage 1: entries = %+v", entries)
	}
	runs := childDoneRuns(t, fx.parent.ID)
	if len(runs) != 1 || !strings.Contains(runs[0].Note, `"next_stage":2`) || !strings.Contains(runs[0].Note, `"cancelled":1`) {
		t.Fatalf("stage 1 run does not name stage 2 and the cancellation: %+v", runs)
	}
	completeRuns()

	updateChildStatus(t, last.ID, "done")
	if got := len(childDoneEntries(t, fx.parent.ID)); got != 1 {
		t.Fatalf("the last stage fired while an unstaged sub-issue was open: %d entries", got)
	}
	updateChildStatus(t, loose.ID, "done")
	entries = childDoneEntries(t, fx.parent.ID)
	if len(entries) != 2 || entries[1].Stage != nil || entries[1].Total != 4 {
		t.Fatalf("wrap-up: entries = %+v", entries)
	}
}

// Another run of the same agent that has not started yet reads the current
// sub-issues when it does, so the rule hands it the stage facts when it is
// claimed instead of queuing a second run. Claimed by a daemon that cannot
// render them, the rule wakes the agent with its own run.
func TestChildDoneJoinsPendingRun(t *testing.T) {
	enableChildDoneRule(t)
	for _, rendersJoined := range []bool{true, false} {
		t.Run(fmt.Sprintf("renders joined wakeups %t", rendersJoined), func(t *testing.T) {
			fx := newChildDoneFixture(t, "in_progress")
			agentID := handlerTestAgentID(t)
			setIssueAssigneeDirect(t, fx.parent.ID, "agent", agentID)
			waiting := dbfx.Task(t, agentID, testutil.Cols{"issue_id": fx.parent.ID, "runtime_id": testRuntimeID, "status": "queued", "originator_user_id": testUserID, "accountable_user_id": testUserID})

			updateChildStatus(t, fx.child.ID, "done")
			if entries := childDoneEntries(t, fx.parent.ID); len(entries) != 0 {
				t.Fatalf("entries before the claim = %+v", entries)
			}
			if got := countPendingTasksForAgent(t, fx.parent.ID, agentID); got != 1 {
				t.Fatalf("pending runs = %d, want the existing one only", got)
			}

			notes := claimRun(t, waiting, rendersJoined)
			startRun(t, waiting)
			runWakeupTick(t)
			entries := childDoneEntries(t, fx.parent.ID)
			if !rendersJoined {
				if notes != "" || len(entries) != 1 || entries[0].Outcome != "woke" || len(childDoneRuns(t, fx.parent.ID)) != 1 {
					t.Fatalf("notes = %q entries = %+v", notes, entries)
				}
				return
			}
			if !strings.Contains(notes, service.ChildDoneDefaultInstruction) || !strings.Contains(notes, `"all":true`) {
				t.Fatalf("joined notes = %q", notes)
			}
			if len(entries) != 1 || entries[0].Outcome != "merged" || entries[0].TaskID != waiting || len(childDoneRuns(t, fx.parent.ID)) != 0 {
				t.Fatalf("entries = %+v", entries)
			}
		})
	}
}

// A person's sub-issue condition and the system rule describe the same fact:
// the assignee runs once.
func TestChildDoneAndConditionRuleWakeOnce(t *testing.T) {
	enableChildDoneRule(t)
	fx := newChildDoneFixture(t, "in_progress")
	agentID := handlerTestAgentID(t)
	setIssueAssigneeDirect(t, fx.parent.ID, "agent", agentID)
	svc := service.IssueWakeupService{Tasks: testHandler.TaskService}
	if _, err := svc.Create(context.Background(), parseUUID(fx.parent.ID), parseUUID(testUserID), pgtype.UUID{}, service.WakeupInput{
		AgentID: agentID, Instruction: "Summarize the children", Kind: "event", Condition: json.RawMessage(`{"type":"children_done"}`),
	}); err != nil {
		t.Fatal(err)
	}

	updateChildStatus(t, fx.child.ID, "done")

	if got := countPendingTasksForAgent(t, fx.parent.ID, agentID); got != 1 {
		t.Fatalf("pending runs = %d, want 1", got)
	}
	var run string
	dbfx.QueryRow(t, `SELECT id FROM agent_task_queue WHERE issue_id = $1 AND context->>'wakeup_system' IS NULL AND context->>'wakeup_id' IS NOT NULL`, fx.parent.ID).Scan(&run)
	if notes := claimRun(t, run, true); !strings.Contains(notes, service.ChildDoneDefaultInstruction) {
		t.Fatalf("the person's run lacks the system rule: %q", notes)
	}
	startRun(t, run)
	runWakeupTick(t)
	entries := childDoneEntries(t, fx.parent.ID)
	if len(entries) != 1 || entries[0].Outcome != "merged" || entries[0].TaskID != run {
		t.Fatalf("system entries = %+v", entries)
	}
}

// The system rule shares runaway protection: past the hourly limit it pauses
// with a visible reason instead of starting another run.
func TestChildDoneRateLimitPausesRule(t *testing.T) {
	enableChildDoneRule(t)
	fx := newChildDoneFixture(t, "in_progress")
	agentID := handlerTestAgentID(t)
	setIssueAssigneeDirect(t, fx.parent.ID, "agent", agentID)
	updateChildStatus(t, fx.child.ID, "done")
	runs := childDoneRuns(t, fx.parent.ID)
	if len(runs) != 1 {
		t.Fatalf("first close: runs = %+v", runs)
	}
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE issue_id = $1`, fx.parent.ID)
	for range 12 {
		dbfx.Task(t, agentID, testutil.Cols{"issue_id": fx.parent.ID, "runtime_id": testRuntimeID, "status": "completed",
			"context": fmt.Sprintf(`{"wakeup_id":%q}`, runs[0].WakeupID)})
	}

	updateChildStatus(t, fx.child.ID, "in_progress")
	updateChildStatus(t, fx.child.ID, "done")

	var paused pgtype.Text
	var enabled bool
	dbfx.QueryRow(t, `SELECT paused_reason, enabled FROM issue_wakeup WHERE issue_id = $1 AND system_rule = 'child_done'`, fx.parent.ID).Scan(&paused, &enabled)
	if paused.String != "rate" || enabled {
		t.Fatalf("rule paused=%q enabled=%v, want paused for rate", paused.String, enabled)
	}
	if got := countPendingTasksForAgent(t, fx.parent.ID, agentID); got != 0 {
		t.Fatalf("a paused rule started %d runs", got)
	}
}
