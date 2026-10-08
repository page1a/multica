package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/statecard"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// DENE-1331: the cold-and-thick gate in its four quadrants, plus the missing
// size that must never trip it.
func TestSessionColdAndThick(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	thin, thick := int64(40_000), int64(180_000)
	hot, cold := now.Add(-10*time.Minute), now.Add(-3*time.Hour)
	cases := []struct {
		name   string
		ended  time.Time
		tokens *int64
		want   bool
	}{
		{"hot thin", hot, &thin, false},
		{"hot thick", hot, &thick, false},
		{"cold thin", cold, &thin, false},
		{"cold thick", cold, &thick, true},
		{"cold, size unknown", cold, nil, false},
		{"end unknown", time.Time{}, &thick, false},
	}
	for _, tc := range cases {
		if got := sessionColdAndThick(tc.ended, tc.tokens, now); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The gate at the claim: a cold, thick session is set aside — no session id,
// the working directory kept, the card in the opening message — and every
// other quadrant resumes as before.
func TestClaimTask_ColdThickSessionStartsFresh(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cases := []struct {
		name      string
		endedAgo  string
		tokens    any // nil: no usage row carries the size
		wantFresh bool
	}{
		{"hot thin", "10 minutes", int64(40_000), false},
		{"hot thick", "10 minutes", int64(180_000), false},
		{"cold thin", "3 hours", int64(40_000), false},
		{"cold thick", "3 hours", int64(180_000), true},
		{"cold, size missing", "3 hours", nil, false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			agentID, runtimeID, daemonID := createRuntimeGuardAgent(t, ctx)
			issueID := dbfx.Issue(t, "cold thick "+tc.name, testutil.Cols{"status": "in_progress", "number": 91331 + i})
			var priorID string
			dbfx.QueryRow(t, `
				INSERT INTO agent_task_queue (
					agent_id, runtime_id, issue_id, status, priority,
					started_at, completed_at, session_id, work_dir
				)
				VALUES ($1, $2, $3, 'completed', 0,
				        now() - interval '`+tc.endedAgo+`' - interval '5 minutes', now() - interval '`+tc.endedAgo+`',
				        'gate-session', '/tmp/gate-workdir')
				RETURNING id
			`, agentID, runtimeID, issueID).Scan(&priorID)
			if tc.tokens != nil {
				dbfx.Exec(t, `INSERT INTO task_usage (task_id, provider, model, last_context_tokens) VALUES ($1, 'claude', 'claude-opus-5', $2)`, priorID, tc.tokens)
			} else {
				dbfx.Exec(t, `INSERT INTO task_usage (task_id, provider, model) VALUES ($1, 'claude', 'claude-opus-5')`, priorID)
			}
			dbfx.Exec(t, `INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority) VALUES ($1, $2, $3, 'queued', 0)`, agentID, runtimeID, issueID)

			task := claimTaskForRuntimeGuard(t, runtimeID, daemonID)
			if task.PriorWorkDir != "/tmp/gate-workdir" {
				t.Fatalf("PriorWorkDir = %q, want the old directory kept", task.PriorWorkDir)
			}
			if tc.wantFresh {
				if task.PriorSessionID != "" {
					t.Fatalf("PriorSessionID = %q, want a new session", task.PriorSessionID)
				}
				if task.IssueStateCardReason != stateCardReasonFreshSession || !strings.Contains(task.IssueHandoffCard, "## 状态卡") {
					t.Fatalf("fresh session opens without the card: reason %q card %q", task.IssueStateCardReason, task.IssueHandoffCard)
				}
				return
			}
			if task.PriorSessionID != "gate-session" {
				t.Fatalf("PriorSessionID = %q, want gate-session resumed", task.PriorSessionID)
			}
			if task.IssueStateCardReason == stateCardReasonFreshSession {
				t.Fatal("a resumed session must not be labelled fresh")
			}
		})
	}
}

// A run after someone else handed off opens with the card; a run after the
// agent's own handoff does not.
func TestClaimTask_BatonOpensWithStateCard(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	for i, own := range []bool{false, true} {
		name := "someone else's baton"
		if own {
			name = "own baton"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			agentID, runtimeID, daemonID := createRuntimeGuardAgent(t, ctx)
			issueID := dbfx.Issue(t, "baton "+name, testutil.Cols{"status": "in_progress", "number": 81331 + i})
			dbfx.Exec(t, `
				INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, started_at, completed_at)
				VALUES ($1, $2, $3, 'completed', 0, now() - interval '2 hours', now() - interval '110 minutes')
			`, agentID, runtimeID, issueID)
			by := "00000000-0000-0000-0000-0000000000aa"
			if own {
				by = agentID
			}
			meta, _ := json.Marshal(map[string]string{
				statecard.KeyHandoffAt:      time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
				statecard.KeyHandoffSummary: "审过了，缓存键要带 wsId",
				statecard.KeyHandoffTo:      "reviewer",
				statecard.KeyHandoffByType:  "agent",
				statecard.KeyHandoffByID:    by,
			})
			dbfx.Exec(t, `UPDATE issue SET metadata = $2 WHERE id = $1`, issueID, meta)
			dbfx.Exec(t, `INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority) VALUES ($1, $2, $3, 'queued', 0)`, agentID, runtimeID, issueID)

			task := claimTaskForRuntimeGuard(t, runtimeID, daemonID)
			if own {
				if task.IssueHandoffCard != "" {
					t.Fatalf("the agent's own baton must not open the run: %q", task.IssueHandoffCard)
				}
				return
			}
			if task.IssueStateCardReason != stateCardReasonBaton || !strings.Contains(task.IssueHandoffCard, "缓存键要带 wsId") {
				t.Fatalf("baton run opens without the previous owner's words: reason %q card %q", task.IssueStateCardReason, task.IssueHandoffCard)
			}
		})
	}
}
