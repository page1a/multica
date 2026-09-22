package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/dispatch"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestClaimSkipsDisabledAgentAndResumesWhenEnabled(t *testing.T) {
	fixture := newRuntimeClaimAccessFixture(t, "public", true, true, "queued")
	ctx := context.Background()
	q := db.New(fixture.pool)

	if _, err := fixture.pool.Exec(ctx, `UPDATE agent SET work_enabled = false WHERE id = $1`, fixture.agentID); err != nil {
		t.Fatalf("disable seat: %v", err)
	}

	candidates, err := q.ListQueuedClaimCandidatesByRuntime(ctx, fixture.runtimeID)
	if err != nil {
		t.Fatalf("list candidates while disabled: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("disabled seat still listed %d claim candidates", len(candidates))
	}

	_, err = q.ClaimAgentTask(ctx, db.ClaimAgentTaskParams{
		AgentID:          fixture.agentID,
		RuntimeID:        fixture.runtimeID,
		PrepareLeaseSecs: 60,
		RuntimeStaleSecs: RuntimeClaimFreshnessSeconds,
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("claim while disabled: %v, want no rows", err)
	}

	var status string
	if err := fixture.pool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, fixture.taskID).Scan(&status); err != nil {
		t.Fatalf("read task status: %v", err)
	}
	if status != "queued" {
		t.Fatalf("task status = %q, want queued (disable must not cancel)", status)
	}

	if _, err := fixture.pool.Exec(ctx, `UPDATE agent SET work_enabled = true WHERE id = $1`, fixture.agentID); err != nil {
		t.Fatalf("re-enable seat: %v", err)
	}

	claimed, err := q.ClaimAgentTask(ctx, db.ClaimAgentTaskParams{
		AgentID:          fixture.agentID,
		RuntimeID:        fixture.runtimeID,
		PrepareLeaseSecs: 60,
		RuntimeStaleSecs: RuntimeClaimFreshnessSeconds,
	})
	if err != nil {
		t.Fatalf("claim after re-enable: %v", err)
	}
	if util.UUIDToString(claimed.ID) != fixture.taskID {
		t.Fatalf("claimed %s, want %s", util.UUIDToString(claimed.ID), fixture.taskID)
	}
}

func TestEnqueueIssueTaskSkipsDisabledAgentAndResumesWhenEnabled(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	q := db.New(pool)
	_, _, agentID, issueID := seedAttributionFixture(t, pool)

	if _, err := pool.Exec(ctx, `UPDATE issue SET assignee_type = 'agent', assignee_id = $1 WHERE id = $2`, agentID, issueID); err != nil {
		t.Fatalf("assign issue: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent SET work_enabled = false WHERE id = $1`, agentID); err != nil {
		t.Fatalf("disable seat: %v", err)
	}

	svc := NewTaskService(q, pool, nil, events.New())
	issue, err := q.GetIssue(ctx, util.MustParseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	if _, err := svc.EnqueueTaskForIssue(ctx, issue); err == nil {
		t.Fatal("enqueue while disabled succeeded")
	} else if err.Error() != "agent is not accepting work" {
		t.Fatalf("enqueue while disabled: %v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE agent SET work_enabled = true WHERE id = $1`, agentID); err != nil {
		t.Fatalf("re-enable seat: %v", err)
	}
	if _, err := svc.EnqueueTaskForIssue(ctx, issue); err != nil {
		t.Fatalf("enqueue after re-enable: %v", err)
	}
}

func TestAgentReadinessDisabledDoesNotUseRuntimeLookup(t *testing.T) {
	validRuntime := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	got, err := AgentReadiness(t.Context(), RuntimeLookup{}, db.Agent{
		RuntimeID:   validRuntime,
		WorkEnabled: false,
	})
	if err != nil {
		t.Fatalf("AgentReadiness: %v", err)
	}
	if !got.Blocked() || got.Reason != dispatch.ReasonTargetUnavailable {
		t.Fatalf("got %+v, want blocked/target_unavailable", got)
	}
}

func TestDisabledAgentStaysOnList(t *testing.T) {
	// The list query must still return a disabled seat — disable is not archive.
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	q := db.New(pool)
	fx := testutil.New(pool, "", "")
	suffix := t.Name()
	userID := fx.User(t, "work-enabled-owner-"+suffix, "work-enabled-owner-"+suffix+"@example.com")
	wsID := fx.Workspace(t, "work-enabled-ws-"+suffix, "work-enabled-ws-"+suffix)
	fx = testutil.New(pool, wsID, userID)
	fx.Member(t, wsID, userID, "owner")
	runtimeID := fx.Runtime(t, "work-enabled-rt", testutil.Cols{"owner_id": userID, "visibility": "public"})
	agentID := fx.Agent(t, "work-enabled-listed", runtimeID, testutil.Cols{"owner_id": userID})
	if _, err := pool.Exec(ctx, `UPDATE agent SET work_enabled = false WHERE id = $1`, agentID); err != nil {
		t.Fatalf("disable: %v", err)
	}
	agents, err := q.ListAgents(ctx, util.MustParseUUID(wsID))
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	found := false
	for _, a := range agents {
		if util.UUIDToString(a.ID) == agentID {
			found = true
			if a.WorkEnabled {
				t.Fatal("listed agent still work_enabled=true")
			}
			if a.ArchivedAt.Valid {
				t.Fatal("disable must not archive")
			}
		}
	}
	if !found {
		t.Fatal("disabled agent missing from ListAgents")
	}
}
