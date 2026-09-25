package handler

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// The stale-review row's four store methods, against a real database.
//
// The routing package's own tests drive the decision logic through a fake
// store, so the one thing they cannot check is the half that is written in
// SQL: which tickets the sweep picks up, whose remarks it reads, and whether
// the completion write is really conditional on the status. Each of those is a
// guard, and a guard that only exists in a fake is not a guard.
func TestStaleReviewStoreQueries(t *testing.T) {
	ctx := context.Background()
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	store := testHandler.RoutingStore()
	long := time.Now().Add(-48 * time.Hour)

	stale := fx.Issue(t, "stalled in review", testutil.Cols{
		"status":           "in_review",
		"last_activity_at": long,
	})
	fresh := fx.Issue(t, "reviewed an hour ago", testutil.Cols{
		"status":           "in_review",
		"last_activity_at": time.Now().Add(-time.Hour),
	})
	working := fx.Issue(t, "still being worked on", testutil.Cols{
		"status":           "in_progress",
		"last_activity_at": long,
	})
	running := fx.Issue(t, "quiet but a run is active", testutil.Cols{
		"status":           "in_review",
		"last_activity_at": long,
	})
	child := fx.Issue(t, "child review must stay with parent", testutil.Cols{
		"status":           "in_review",
		"last_activity_at": long,
	})
	fx.Exec(t, `UPDATE issue SET parent_issue_id = $1 WHERE id = $2`, stale, child)
	runtimeID := fx.Runtime(t, "stale-sweep-runtime")
	agentID := fx.Agent(t, "stale-sweep-agent", runtimeID)
	fx.Task(t, agentID, testutil.Cols{
		"issue_id":   running,
		"status":     "running",
		"runtime_id": runtimeID,
	})

	t.Run("picks up only quiet in-review tickets with no run", func(t *testing.T) {
		ids, err := store.StaleReviews(ctx, testWorkspaceID, time.Now().Add(-24*time.Hour), 50)
		if err != nil {
			t.Fatalf("stale reviews: %v", err)
		}
		seen := map[string]bool{}
		for _, id := range ids {
			seen[id] = true
		}
		if !seen[stale] {
			t.Errorf("the stalled ticket was not picked up")
		}
		for label, id := range map[string]string{
			"a ticket touched an hour ago":  fresh,
			"a ticket being worked on":      working,
			"a ticket with an active run":   running,
			"a child issue awaiting review": child,
		} {
			if seen[id] {
				t.Errorf("%s was picked up by the sweep", label)
			}
		}
	})

	t.Run("reads only the reviewer's own remarks, and only this review round", func(t *testing.T) {
		reviewer := routing.ReviewerRef{Kind: routing.ReviewerMember, ID: testUserID}
		enteredReview := time.Now().Add(-36 * time.Hour)

		// Round one: the reviewer passed it, somebody sent it back, the
		// executor redid the work. Both remarks predate the ticket's CURRENT
		// entry into review.
		fx.Comment(t, stale, "第一轮：通过",
			testutil.Cols{"created_at": enteredReview.Add(-4 * time.Hour)})
		fx.Comment(t, stale, "返工说明",
			testutil.Cols{"created_at": enteredReview.Add(-2 * time.Hour),
				"author_type": "agent", "author_id": agentID})

		// With no record of when review began, nothing counts. A whole
		// history is not a verdict about the round being examined.
		remarks, err := store.ReviewRemarks(ctx, testWorkspaceID, stale, reviewer)
		if err != nil {
			t.Fatalf("review remarks with no entry record: %v", err)
		}
		if len(remarks) != 0 {
			t.Fatalf("remarks = %v, want none: the round boundary is unknown, so an "+
				"older round's verdict must not count as this round's acceptance", remarks)
		}

		fx.Insert(t, "activity_log", testutil.Cols{
			"workspace_id": testWorkspaceID,
			"issue_id":     stale,
			"actor_type":   "member",
			"actor_id":     testUserID,
			"action":       "status_changed",
			"details":      testutil.Raw(`'{"from": "in_progress", "to": "in_review"}'::jsonb`),
			"created_at":   enteredReview,
		})

		// Round one's pass is now out of scope, and so is everything written
		// by anyone who is not the reviewer.
		remarks, err = store.ReviewRemarks(ctx, testWorkspaceID, stale, reviewer)
		if err != nil {
			t.Fatalf("review remarks: %v", err)
		}
		if len(remarks) != 0 {
			t.Fatalf("remarks = %v, want none: every remark predates this round", remarks)
		}

		fx.Comment(t, stale, "从验收席的角度：通过",
			testutil.Cols{"created_at": enteredReview.Add(time.Hour)})
		fx.Comment(t, stale, "执行席的交付说明",
			testutil.Cols{"created_at": enteredReview.Add(2 * time.Hour),
				"author_type": "agent", "author_id": agentID})

		remarks, err = store.ReviewRemarks(ctx, testWorkspaceID, stale, reviewer)
		if err != nil {
			t.Fatalf("review remarks after this round's verdict: %v", err)
		}
		if len(remarks) != 1 || remarks[0] != "从验收席的角度：通过" {
			t.Fatalf("remarks = %v, want only the reviewer's own remark from this round", remarks)
		}

		// A slot holding "needs no acceptance pass" has no author at all, so
		// it can never produce the remark that unlocks a completion.
		none, err := store.ReviewRemarks(ctx, testWorkspaceID, stale,
			routing.ReviewerRef{Kind: routing.ReviewerNoReview})
		if err != nil {
			t.Fatalf("review remarks for none: %v", err)
		}
		if len(none) != 0 {
			t.Fatalf("a no-review slot produced remarks: %v", none)
		}
	})

	t.Run("the completion write is conditional on the status", func(t *testing.T) {
		written, err := store.CompleteFromReview(ctx, testWorkspaceID, stale)
		if err != nil {
			t.Fatalf("complete: %v", err)
		}
		if !written {
			t.Fatalf("the write lost against an in_review ticket")
		}
		var status string
		fx.QueryRow(t, `SELECT status FROM issue WHERE id = $1`, stale).Scan(&status)
		if status != "done" {
			t.Fatalf("status = %q, want done", status)
		}

		// Second pass: the ticket already left in_review, so the guard must
		// hold and the caller must be told it wrote nothing.
		written, err = store.CompleteFromReview(ctx, testWorkspaceID, stale)
		if err != nil {
			t.Fatalf("second complete: %v", err)
		}
		if written {
			t.Fatalf("a ticket outside in_review was moved anyway")
		}

		// And a ticket that was never in review is never eligible.
		written, err = store.CompleteFromReview(ctx, testWorkspaceID, working)
		if err != nil {
			t.Fatalf("complete in_progress: %v", err)
		}
		if written {
			t.Fatalf("an in_progress ticket was moved to done")
		}
	})

	t.Run("lists the workspace only while routing is on", func(t *testing.T) {
		ids, err := store.EnabledWorkspaces(ctx)
		if err != nil {
			t.Fatalf("enabled workspaces: %v", err)
		}
		for _, id := range ids {
			if id == testWorkspaceID {
				t.Fatalf("a workspace with routing off was listed for the sweep")
			}
		}

		fx.Exec(t, `UPDATE workspace SET settings = jsonb_set(COALESCE(settings, '{}'::jsonb),
			'{routing}', '{"enabled": true, "model": "m"}'::jsonb, true) WHERE id = $1`, testWorkspaceID)
		t.Cleanup(func() {
			fx.Exec(t, `UPDATE workspace SET settings = settings - 'routing' WHERE id = $1`, testWorkspaceID)
		})

		ids, err = store.EnabledWorkspaces(ctx)
		if err != nil {
			t.Fatalf("enabled workspaces after enabling: %v", err)
		}
		found := false
		for _, id := range ids {
			if id == testWorkspaceID {
				found = true
			}
		}
		if !found {
			t.Fatalf("an enabled workspace was not listed for the sweep")
		}
	})
}

// The sweep's budget must not be spent on child rows.
//
// Acceptance is parent-scoped, so a child is not a candidate at all. That
// exclusion has to live in the candidate query, not only in the no-op the
// decision layer returns after a row has been selected: the query orders
// oldest-quiet-first and caps the round at `limit`, so children filtered only
// after selection spend every slot before the sweep ever reaches the stalled
// top-level parent. The parent then waits round after round — not a slow
// sweep, a sweep that never visits the ticket it exists for.
//
// The children here are quieter than the parent on purpose, so they sort ahead
// of it, and the limit is smaller than the child population.
func TestStaleReviewSweepIsNotStarvedByChildIssues(t *testing.T) {
	ctx := context.Background()
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	store := testHandler.RoutingStore()

	// A workspace of its own: the claim is about which rows fill the round,
	// and stale top-level rows another test left in the shared workspace
	// would be exactly the noise this assertion must not have.
	wsID := fx.Workspace(t, "child-starved stale sweep",
		fmt.Sprintf("child-starved-sweep-%d", time.Now().UnixNano()))

	parent := fx.Issue(t, "top-level ticket quietly awaiting acceptance", testutil.Cols{
		"workspace_id":     wsID,
		"status":           "in_review",
		"last_activity_at": time.Now().Add(-48 * time.Hour),
	})
	const children = 30
	for i := 0; i < children; i++ {
		fx.Issue(t, "quiet child awaiting acceptance", testutil.Cols{
			"workspace_id":     wsID,
			"status":           "in_review",
			"parent_issue_id":  parent,
			"last_activity_at": time.Now().Add(-72 * time.Hour),
		})
	}

	const limit = 5
	ids, err := store.StaleReviews(ctx, wsID, time.Now().Add(-24*time.Hour), limit)
	if err != nil {
		t.Fatalf("stale reviews: %v", err)
	}
	if len(ids) == 0 {
		t.Fatalf("the sweep returned nothing: the only candidate in this workspace is the " +
			"top-level parent, and a sweep that never reaches it is the stall it exists to catch")
	}
	for _, id := range ids {
		if id != parent {
			t.Fatalf("sweep = %v, want only the top-level parent: %d child rows quieter than it "+
				"sorted ahead of it, so a filter applied after selection spends the whole round "+
				"on children and the parent is never reached", ids, children)
		}
	}
}

// A sub-issue completed by the sweep must reach its parent.
//
// This is the routing row's main customer, not an edge case: an agent cannot
// set done itself, so "the reviewer passed it and the status never moved" is
// the commonest way a sub-issue stalls. Closing one without the parent
// notification would trade a visible stall for an invisible one — the stage
// barrier never evaluates and the next stage never wakes.
func TestCompleteFromReviewNotifiesParent(t *testing.T) {
	ctx := context.Background()
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	store := testHandler.RoutingStore()

	// An unassigned parent: a member-assigned one reads its own timeline and
	// is skipped by design, which would make this test prove nothing.
	parent := fx.Issue(t, "parent holding one stage", testutil.Cols{"status": "in_progress"})
	child := fx.Issue(t, "the only child, awaiting acceptance", testutil.Cols{
		"status":          "in_review",
		"parent_issue_id": parent,
	})

	before := fx.Count(t, `SELECT COUNT(*) FROM comment WHERE issue_id = $1`, parent)

	written, err := store.CompleteFromReview(ctx, testWorkspaceID, child)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if !written {
		t.Fatalf("the completion write lost against an in_review child")
	}

	var status string
	fx.QueryRow(t, `SELECT status FROM issue WHERE id = $1`, child).Scan(&status)
	if status != "done" {
		t.Fatalf("child status = %q, want done", status)
	}

	after := fx.Count(t, `SELECT COUNT(*) FROM comment WHERE issue_id = $1`, parent)
	if after <= before {
		t.Fatalf("the parent got no comment (%d -> %d): the last child of the stage "+
			"finished and nothing told the parent, so the next stage never wakes",
			before, after)
	}
}

// Acceptance is per stay in review, not per issue. DENE-617 had a handoff
// comment and a reviewer task from an earlier stay; the executor held the
// ticket again and nothing was running. That must read as "not yet handed
// off", while a live run and a notice from THIS stay must read as already
// covered.
func TestAcceptanceFollowsTheCurrentReviewStay(t *testing.T) {
	ctx := context.Background()
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	store := testHandler.RoutingStore()

	runtimeID := fx.Runtime(t, "acceptance-stay-runtime")
	executor := fx.Agent(t, "acceptance-stay-executor", runtimeID)
	reviewer := fx.Agent(t, "acceptance-stay-reviewer", runtimeID)
	entered := time.Now().Add(-2 * time.Hour)

	waiting := fx.Issue(t, "executor finished, reviewer not started", testutil.Cols{
		"status":        "in_review",
		"assignee_type": "agent",
		"assignee_id":   executor,
		"reviewer_type": "agent",
		"reviewer_id":   reviewer,
	})
	fx.Insert(t, "activity_log", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"issue_id":     waiting,
		"actor_type":   "agent",
		"actor_id":     executor,
		"action":       "status_changed",
		"details":      testutil.Raw(`'{"from": "in_progress", "to": "in_review"}'::jsonb`),
		"created_at":   entered,
	})
	fx.Comment(t, waiting, "上一轮交接", testutil.Cols{
		"author_type":  "system",
		"type":         "system",
		"routing_kind": "handoff",
		"created_at":   entered.Add(-48 * time.Hour),
	})
	fx.Task(t, reviewer, testutil.Cols{
		"issue_id":   waiting,
		"status":     "completed",
		"runtime_id": runtimeID,
		"created_at": entered.Add(-48 * time.Hour),
	})

	open := routing.Issue{
		ID: waiting, Status: "in_review",
		AssigneeType: "agent", AssigneeID: executor,
		Reviewer: routing.ReviewerRef{Kind: routing.ReviewerAgent, ID: reviewer},
	}
	state, err := store.Acceptance(ctx, testWorkspaceID, open)
	if err != nil {
		t.Fatalf("acceptance: %v", err)
	}
	if state.ActiveRun || state.AgentEngaged {
		t.Fatalf("state = %+v, want neither an active run nor an engaged reviewer — an earlier stay must not count", state)
	}

	fx.Task(t, executor, testutil.Cols{
		"issue_id":   waiting,
		"status":     "running",
		"runtime_id": runtimeID,
	})
	state, err = store.Acceptance(ctx, testWorkspaceID, open)
	if err != nil {
		t.Fatalf("acceptance with a live run: %v", err)
	}
	if !state.ActiveRun || state.AgentEngaged {
		t.Fatalf("state = %+v, want an active run and the reviewer still not engaged", state)
	}

	held := fx.Issue(t, "reviewer already holds this stay", testutil.Cols{
		"status":        "in_review",
		"assignee_type": "agent",
		"assignee_id":   reviewer,
		"reviewer_type": "agent",
		"reviewer_id":   reviewer,
	})
	fx.Insert(t, "activity_log", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"issue_id":     held,
		"actor_type":   "agent",
		"actor_id":     executor,
		"action":       "status_changed",
		"details":      testutil.Raw(`'{"from": "in_progress", "to": "in_review"}'::jsonb`),
		"created_at":   entered,
	})
	fx.Task(t, reviewer, testutil.Cols{
		"issue_id":   held,
		"status":     "completed",
		"runtime_id": runtimeID,
		"created_at": entered.Add(time.Minute),
	})
	state, err = store.Acceptance(ctx, testWorkspaceID, routing.Issue{
		ID: held, Status: "in_review",
		AssigneeType: "agent", AssigneeID: reviewer,
		Reviewer: routing.ReviewerRef{Kind: routing.ReviewerAgent, ID: reviewer},
	})
	if err != nil {
		t.Fatalf("acceptance for a held stay: %v", err)
	}
	if state.ActiveRun || !state.AgentEngaged {
		t.Fatalf("state = %+v, want the reviewer engaged for this stay and no live run", state)
	}

	person := fx.Issue(t, "a person accepts this", testutil.Cols{
		"status":        "in_review",
		"assignee_type": "agent",
		"assignee_id":   executor,
		"reviewer_type": "member",
		"reviewer_id":   testUserID,
	})
	fx.Insert(t, "activity_log", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"issue_id":     person,
		"actor_type":   "member",
		"actor_id":     testUserID,
		"action":       "status_changed",
		"details":      testutil.Raw(`'{"from": "in_progress", "to": "in_review"}'::jsonb`),
		"created_at":   entered,
	})
	fx.Insert(t, "inbox_item", testutil.Cols{
		"workspace_id":   testWorkspaceID,
		"recipient_type": "member",
		"recipient_id":   testUserID,
		"type":           "routing_needs_you",
		"severity":       "action_required",
		"issue_id":       person,
		"title":          "上一轮",
		"created_at":     entered.Add(-48 * time.Hour),
	})
	member := routing.Issue{
		ID: person, Status: "in_review",
		AssigneeType: "agent", AssigneeID: executor,
		Reviewer: routing.ReviewerRef{Kind: routing.ReviewerMember, ID: testUserID},
	}
	state, err = store.Acceptance(ctx, testWorkspaceID, member)
	if err != nil {
		t.Fatalf("acceptance for a person: %v", err)
	}
	if state.MemberNotified {
		t.Fatal("an inbox row from the previous stay counted as this stay's notice")
	}
	fx.Insert(t, "inbox_item", testutil.Cols{
		"workspace_id":   testWorkspaceID,
		"recipient_type": "member",
		"recipient_id":   testUserID,
		"type":           "routing_needs_you",
		"severity":       "action_required",
		"issue_id":       person,
		"title":          "这一轮",
		"created_at":     entered.Add(time.Minute),
	})
	state, err = store.Acceptance(ctx, testWorkspaceID, member)
	if err != nil {
		t.Fatalf("acceptance after this stay's notice: %v", err)
	}
	if !state.MemberNotified {
		t.Fatal("this stay's notice was not seen")
	}
}

// Two callbacks for the same stay — the status change and the run finishing —
// both used to read "not yet notified" and each insert a routing_needs_you.
// The person is not reassigned. A later stay still gets its own notice.
func TestNotifyMemberOncePerStayUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	store := testHandler.RoutingStore()

	runtimeID := fx.Runtime(t, "notice-race-runtime")
	executor := fx.Agent(t, "notice-race-executor", runtimeID)
	issueID := fx.Issue(t, "person accepts, two callbacks at once", testutil.Cols{
		"status":        "in_review",
		"assignee_type": "agent",
		"assignee_id":   executor,
		"reviewer_type": "member",
		"reviewer_id":   testUserID,
	})
	fx.Insert(t, "activity_log", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"issue_id":     issueID,
		"actor_type":   "member",
		"actor_id":     testUserID,
		"action":       "status_changed",
		"details":      testutil.Raw(`'{"from": "in_progress", "to": "in_review"}'::jsonb`),
		"created_at":   testutil.Raw("now() - interval '2 hours'"),
	})

	const callers = 8
	var wrote atomic.Int32
	var failed atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := store.NotifyMember(ctx, testWorkspaceID, issueID, routing.Member{UserID: testUserID, Name: "Kun"})
			if err != nil {
				failed.Add(1)
				t.Errorf("notify: %v", err)
				return
			}
			if ok {
				wrote.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if failed.Load() != 0 {
		t.Fatalf("%d notify calls failed", failed.Load())
	}
	if got := wrote.Load(); got != 1 {
		t.Fatalf("writers = %d, want 1", got)
	}
	if got := fx.Count(t, `SELECT COUNT(*) FROM inbox_item WHERE issue_id = $1 AND type = 'routing_needs_you'`, issueID); got != 1 {
		t.Fatalf("notices = %d, want 1", got)
	}
	assertAssignee(t, issueID, "agent", executor)

	fx.Insert(t, "activity_log", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"issue_id":     issueID,
		"actor_type":   "member",
		"actor_id":     testUserID,
		"action":       "status_changed",
		"details":      testutil.Raw(`'{"from": "in_progress", "to": "in_review"}'::jsonb`),
		"created_at":   testutil.Raw("now() + interval '2 minutes'"),
	})
	again, err := store.NotifyMember(ctx, testWorkspaceID, issueID, routing.Member{UserID: testUserID, Name: "Kun"})
	if err != nil {
		t.Fatalf("next stay: %v", err)
	}
	if !again {
		t.Fatal("the next stay was treated as already notified")
	}
	if got := fx.Count(t, `SELECT COUNT(*) FROM inbox_item WHERE issue_id = $1 AND type = 'routing_needs_you'`, issueID); got != 2 {
		t.Fatalf("notices after the next stay = %d, want 2", got)
	}
	assertAssignee(t, issueID, "agent", executor)
}

// Concurrent handoffs of one stay must enqueue one reviewer run. The pending
// task unique index is what collapses the second insert; this is the regression
// that it still does.
func TestConcurrentHandoffStartsOneReviewerRun(t *testing.T) {
	ctx := context.Background()
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	store := testHandler.RoutingStore()

	runtimeID := fx.Runtime(t, "handoff-race-runtime")
	executor := fx.Agent(t, "handoff-race-executor", runtimeID)
	reviewer := fx.Agent(t, "handoff-race-reviewer", runtimeID)
	issueID := fx.Issue(t, "two callbacks hand the ticket to the reviewer", testutil.Cols{
		"status":        "in_review",
		"assignee_type": "agent",
		"assignee_id":   executor,
		"reviewer_type": "agent",
		"reviewer_id":   reviewer,
	})

	const callers = 8
	var failed atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := store.Handoff(ctx, testWorkspaceID, issueID, "agent", reviewer); err != nil {
				failed.Add(1)
				t.Errorf("handoff: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if failed.Load() != 0 {
		t.Fatalf("%d handoffs failed", failed.Load())
	}
	if got := fx.Count(t, `SELECT COUNT(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND status <> 'cancelled'`, issueID, reviewer); got != 1 {
		t.Fatalf("reviewer runs = %d, want 1", got)
	}
	assertAssignee(t, issueID, "agent", reviewer)
}

// A finished stay leaves its handoff comment and its inbox row behind. The
// next time the ticket enters in_review those rows are history: the reviewer
// seat is started again, and a person in the slot is notified again. Eight
// callbacks in the new stay still produce one notice, and the person is not
// given the ticket.
func TestNextReviewRoundWakesDespiteOldHandoffAndNotice(t *testing.T) {
	ctx := context.Background()
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	store := testHandler.RoutingStore()

	runtimeID := fx.Runtime(t, "next-round-runtime")
	executor := fx.Agent(t, "next-round-executor", runtimeID)
	reviewer := fx.Agent(t, "next-round-reviewer", runtimeID)
	entered := time.Now().Add(-2 * time.Hour)
	previous := entered.Add(-48 * time.Hour)

	agentIssue := fx.Issue(t, "rework came back to review", testutil.Cols{
		"status":        "in_review",
		"assignee_type": "agent",
		"assignee_id":   executor,
		"reviewer_type": "agent",
		"reviewer_id":   reviewer,
	})
	fx.Insert(t, "activity_log", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"issue_id":     agentIssue,
		"actor_type":   "agent",
		"actor_id":     executor,
		"action":       "status_changed",
		"details":      testutil.Raw(`'{"from": "in_progress", "to": "in_review"}'::jsonb`),
		"created_at":   entered,
	})
	fx.Comment(t, agentIssue, "上一轮交接", testutil.Cols{
		"author_type":  "system",
		"type":         "system",
		"routing_kind": "handoff",
		"created_at":   previous,
	})
	fx.Task(t, reviewer, testutil.Cols{
		"issue_id":   agentIssue,
		"status":     "completed",
		"runtime_id": runtimeID,
		"created_at": previous,
	})

	state, err := store.Acceptance(ctx, testWorkspaceID, routing.Issue{
		ID: agentIssue, Status: "in_review",
		AssigneeType: "agent", AssigneeID: executor,
		Reviewer: routing.ReviewerRef{Kind: routing.ReviewerAgent, ID: reviewer},
	})
	if err != nil {
		t.Fatalf("acceptance before the next handoff: %v", err)
	}
	if state.ActiveRun || state.AgentEngaged {
		t.Fatalf("state = %+v, the previous stay still counts as this one", state)
	}
	if err := store.Handoff(ctx, testWorkspaceID, agentIssue, "agent", reviewer); err != nil {
		t.Fatalf("handoff: %v", err)
	}
	assertAssignee(t, agentIssue, "agent", reviewer)
	pending := `SELECT COUNT(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND status IN ('queued', 'dispatched')`
	if got := fx.Count(t, pending, agentIssue, reviewer); got != 1 {
		t.Fatalf("new reviewer runs = %d, want 1 — the old completed run and the old handoff comment must not block this stay", got)
	}
	if err := store.Handoff(ctx, testWorkspaceID, agentIssue, "agent", reviewer); err != nil {
		t.Fatalf("duplicate handoff: %v", err)
	}
	if got := fx.Count(t, pending, agentIssue, reviewer); got != 1 {
		t.Fatalf("reviewer runs after the duplicate callback = %d, want 1", got)
	}

	personIssue := fx.Issue(t, "a person accepts the next round", testutil.Cols{
		"status":        "in_review",
		"assignee_type": "agent",
		"assignee_id":   executor,
		"reviewer_type": "member",
		"reviewer_id":   testUserID,
	})
	fx.Insert(t, "activity_log", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"issue_id":     personIssue,
		"actor_type":   "member",
		"actor_id":     testUserID,
		"action":       "status_changed",
		"details":      testutil.Raw(`'{"from": "in_progress", "to": "in_review"}'::jsonb`),
		"created_at":   entered,
	})
	fx.Comment(t, personIssue, "上一轮交接", testutil.Cols{
		"author_type":  "system",
		"type":         "system",
		"routing_kind": "handoff",
		"created_at":   previous,
	})
	fx.Insert(t, "inbox_item", testutil.Cols{
		"workspace_id":   testWorkspaceID,
		"recipient_type": "member",
		"recipient_id":   testUserID,
		"type":           "routing_needs_you",
		"severity":       "action_required",
		"issue_id":       personIssue,
		"title":          "上一轮",
		"created_at":     previous,
	})

	const callers = 8
	var wrote atomic.Int32
	var failed atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := store.NotifyMember(ctx, testWorkspaceID, personIssue, routing.Member{UserID: testUserID, Name: "Kun"})
			if err != nil {
				failed.Add(1)
				t.Errorf("notify: %v", err)
				return
			}
			if ok {
				wrote.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if failed.Load() != 0 {
		t.Fatalf("%d notify calls failed", failed.Load())
	}
	if got := wrote.Load(); got != 1 {
		t.Fatalf("writers = %d, want 1 for this stay", got)
	}
	if got := fx.Count(t, `SELECT COUNT(*) FROM inbox_item WHERE issue_id = $1 AND type = 'routing_needs_you' AND archived = false`, personIssue); got != 2 {
		t.Fatalf("notices = %d, want the previous stay's notice plus this one", got)
	}
	assertAssignee(t, personIssue, "agent", executor)
}

func assertAssignee(t *testing.T, issueID, wantType, wantID string) {
	t.Helper()
	var gotType, gotID string
	if err := testPool.QueryRow(context.Background(),
		`SELECT assignee_type, assignee_id::text FROM issue WHERE id = $1`, issueID,
	).Scan(&gotType, &gotID); err != nil {
		t.Fatalf("assignee: %v", err)
	}
	if gotType != wantType || gotID != wantID {
		t.Fatalf("assignee = %s/%s, want %s/%s", gotType, gotID, wantType, wantID)
	}
}
