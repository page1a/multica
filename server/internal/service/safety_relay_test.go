package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

const safetyRefusalText = "API Error: Claude Opus's safeguards flagged this message. Our intentionally broad safeguards may flag some legitimate work."

// seedSafetyWorld renames the quota world's seats to ladder names and gives
// each the model that seat really runs, so the provider of each seat is
// known: the failed seat is a Claude seat, the same-tier seat a GPT one. The
// configured model decides the family, so name and model must agree.
func seedSafetyWorld(t *testing.T, errorText string) quotaWorld {
	t.Helper()
	w := seedQuotaWorld(t, string(taskfailure.ReasonAgentUnknown), errorText, true)
	ctx := context.Background()
	for id, seat := range map[string][2]string{
		w.failedID: {"孙悟空", "claude-opus-5-5"},
		w.sameID:   {"特兰克斯", "gpt-6.1-sol"},
		w.mediumID: {"贝吉塔", "command-code/deepseek%2Fdeepseek-v4.1-flash"},
	} {
		if _, err := w.pool.Exec(ctx, `UPDATE agent SET name = $2, model = $3 WHERE id = $1`, id, seat[0], seat[1]); err != nil {
			t.Fatalf("rename seat: %v", err)
		}
	}
	return w
}

func TestSafetyRefusalHandsTheTicketToAnotherHouse(t *testing.T) {
	w := seedSafetyWorld(t, safetyRefusalText)
	ctx := context.Background()

	if !w.service().relaySafetyRefusal(ctx, w.task(t), safetyRefusalText) {
		t.Fatal("safety refusal was not relayed")
	}
	var assignee, status string
	if err := w.pool.QueryRow(ctx, `SELECT assignee_id::text, status FROM issue WHERE id = $1`, w.issueID).Scan(&assignee, &status); err != nil {
		t.Fatalf("issue: %v", err)
	}
	if assignee != w.sameID || status != "in_progress" {
		t.Fatalf("issue assignee/status = %s/%s, want the GPT seat %s in progress", assignee, status, w.sameID)
	}
	var queued int
	if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'`,
		w.issueID, w.sameID).Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("queued replacement = %d err=%v, want 1", queued, err)
	}
	var enabled bool
	var breakers int
	if err := w.pool.QueryRow(ctx, `SELECT work_enabled, (SELECT count(*) FROM agent_quota_breaker WHERE agent_id = $1) FROM agent WHERE id = $1`,
		w.failedID).Scan(&enabled, &breakers); err != nil {
		t.Fatalf("failed seat: %v", err)
	}
	if !enabled || breakers != 0 {
		t.Fatalf("failed seat enabled=%v breakers=%d, a content refusal must not close the seat", enabled, breakers)
	}
	var notice string
	if err := w.pool.QueryRow(ctx, `SELECT content FROM comment WHERE issue_id = $1 AND author_type = 'system' ORDER BY created_at DESC LIMIT 1`,
		w.issueID).Scan(&notice); err != nil {
		t.Fatalf("notice: %v", err)
	}
	if !strings.Contains(notice, "安全审查") || !strings.Contains(notice, "特兰克斯") {
		t.Fatalf("notice = %s", notice)
	}

	// A second failure report of the same run must not move the ticket again.
	if w.service().relaySafetyRefusal(ctx, w.task(t), safetyRefusalText) {
		t.Fatal("second report relayed again")
	}
}

func TestSafetyRefusalStaysPutWithoutAnotherHouse(t *testing.T) {
	w := seedSafetyWorld(t, safetyRefusalText)
	ctx := context.Background()
	// Only Claude seats left on reachable rungs.
	if _, err := w.pool.Exec(ctx, `UPDATE agent SET work_enabled = false WHERE id = ANY($1::uuid[])`,
		[]string{w.sameID, w.mediumID}); err != nil {
		t.Fatalf("disable seats: %v", err)
	}
	if w.service().relaySafetyRefusal(ctx, w.task(t), safetyRefusalText) {
		t.Fatal("relayed with no seat of another house")
	}
	assertAssignee(t, w, w.failedID)
}

func TestOtherFailuresDoNotTakeTheSafetyRelay(t *testing.T) {
	text := fmt.Sprintf("tool crashed at %s", time.Now())
	w := seedSafetyWorld(t, text)
	if w.service().relaySafetyRefusal(context.Background(), w.task(t), text) {
		t.Fatal("ordinary failure was relayed")
	}
	assertAssignee(t, w, w.failedID)
}

func assertAssignee(t *testing.T, w quotaWorld, want string) {
	t.Helper()
	var assignee string
	if err := w.pool.QueryRow(context.Background(), `SELECT assignee_id::text FROM issue WHERE id = $1`, w.issueID).Scan(&assignee); err != nil {
		t.Fatalf("issue: %v", err)
	}
	if assignee != want {
		t.Fatalf("assignee = %s, want %s", assignee, util.UUIDToString(util.MustParseUUID(want)))
	}
}
