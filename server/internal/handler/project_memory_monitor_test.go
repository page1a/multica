package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestMonitorDays(t *testing.T) {
	for raw, want := range map[string]int{"": monitorDefaultDays, "1": 1, " 30 ": 30, "90": 90} {
		if got, ok := monitorDays(raw); !ok || got != want {
			t.Errorf("monitorDays(%q) = %d, %v", raw, got, ok)
		}
	}
	for _, raw := range []string{"0", "91", "-3", "two"} {
		if _, ok := monitorDays(raw); ok {
			t.Errorf("monitorDays(%q) accepted", raw)
		}
	}
}

func getMemoryMonitor(t *testing.T, projectID, query string) (int, ProjectMemoryMonitorResponse) {
	t.Helper()
	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodGet, "/api/projects/"+projectID+"/memory/monitor"+query, nil), "id", projectID)
	testHandler.GetProjectMemoryMonitor(w, req)
	var resp ProjectMemoryMonitorResponse
	if w.Code == http.StatusOK {
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return w.Code, resp
}

// DENE-1681: one read answers the four monitor questions for a project, each
// row naming its source, and leaves out what the person cannot see.
func TestProjectMemoryMonitor(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID := handlerTestAgentID(t)
	projectID := dbfx.Project(t, "memory monitor project")
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM knowledge_sediment WHERE project_id = $1`, projectID)
	})
	inProject := func(title, status string, extra testutil.Cols) string {
		cols := testutil.Cols{"status": status, "project_id": projectID}
		for k, v := range extra {
			cols[k] = v
		}
		return dbfx.Issue(t, title, cols)
	}
	setMeta := func(issueID string, meta map[string]any) {
		raw, _ := json.Marshal(meta)
		dbfx.Exec(t, `UPDATE issue SET metadata = COALESCE(metadata, '{}'::jsonb) || $2::jsonb WHERE id = $1`, issueID, string(raw))
	}

	// Writes: a close that superseded one entry and deleted two lines.
	wrote := inProject("monitor wrote", "done", nil)
	dbfx.Exec(t, `INSERT INTO knowledge_sediment (workspace_id, project_id, issue_id, changes, verified, author_type, memory_files)
		VALUES ($1, $2, $3, $4::jsonb, true, 'agent', $5::jsonb)`, testWorkspaceID, projectID, wrote,
		`[{"location":"agents","action":"supersede","entry":"旧派单","summary":"被取代","files":["AGENTS.md"]}]`,
		`[{"path":"AGENTS.md","bytes":900,"deleted":2,"supersede_marks":1}]`)
	// A chat sediment from a private chat someone else owns.
	otherUser := dbfx.User(t, "monitor other", "monitor-other@multica.test")
	dbfx.Member(t, testWorkspaceID, otherUser, "member")
	privateChat := dbfx.ChatSession(t, agentID, testutil.Cols{"creator_id": otherUser, "title": "别人的私聊", "visibility": "private"})
	// Its round rolled up a ticket the caller can read and one they cannot.
	privateSource := inProject("monitor private source", "done", testutil.Cols{
		"visibility": "private", "creator_type": "member", "creator_id": otherUser,
	})
	sources, _ := json.Marshal([]map[string]string{{"kind": "issue", "id": wrote}, {"kind": "issue", "id": privateSource}})
	dbfx.Exec(t, `INSERT INTO knowledge_sediment (workspace_id, project_id, chat_session_id, changes, verified, author_type, layer, sources)
		VALUES ($1, $2, $3, '[{"location":"context","action":"new","summary":"词条","files":["CONTEXT.md"]}]'::jsonb, true, 'member', 'boss', $4::jsonb)`,
		testWorkspaceID, projectID, privateChat, string(sources))

	// Unsettled: one declared nothing, one finished without an audit, one
	// finished before the window.
	declaredNone := inProject("monitor none", "done", nil)
	setMeta(declaredNone, map[string]any{closeprotocol.KeyKnowledgeAudit: `{"none":true}`})
	unaudited := inProject("monitor unaudited", "done", nil)
	old := inProject("monitor old", "done", nil)
	dbfx.Exec(t, `UPDATE issue SET updated_at = now() - interval '30 days' WHERE id = $1`, old)

	// Rounds: one ended without writing, one still open.
	idle := inProject("monitor idle round", "done", nil)
	setMeta(idle, map[string]any{"sediment_project": projectID, sedimentGapKey: []string{"context"}})
	openRound := inProject("monitor open round", "todo", nil)
	setMeta(openRound, map[string]any{"sediment_project": projectID})

	// Chat flow: three tickets from one chat — reported, no conclusion, open.
	chat := dbfx.ChatSession(t, agentID, testutil.Cols{"title": "派单聊天"})
	reported := inProject("monitor reported", "done", testutil.Cols{"origin_chat_session_id": chat})
	closeMeta := map[string]any{}
	for _, key := range closeprotocol.Keys {
		closeMeta[key] = ""
	}
	closeMeta[closeprotocol.KeyConclusion] = closeprotocol.ConclusionDelivered
	closeMeta[closeprotocol.KeyStatus] = "done"
	closeMeta[closeprotocol.KeyAt] = "2026-10-08T00:00:00Z"
	setMeta(reported, closeMeta)
	inProject("monitor no conclusion", "done", testutil.Cols{"origin_chat_session_id": chat})
	inProject("monitor open", "in_progress", testutil.Cols{"origin_chat_session_id": chat})
	// A sub-task from the same chat reports through its parent.
	inProject("monitor child", "done", testutil.Cols{"origin_chat_session_id": chat, "parent_issue_id": reported})

	if code, _ := getMemoryMonitor(t, projectID, "?days=0"); code != http.StatusBadRequest {
		t.Fatalf("days=0: status = %d", code)
	}
	code, resp := getMemoryMonitor(t, projectID, "")
	if code != http.StatusOK || resp.Days != monitorDefaultDays {
		t.Fatalf("status = %d days = %d", code, resp.Days)
	}

	if len(resp.Writes) != 2 {
		t.Fatalf("writes = %+v", resp.Writes)
	}
	for _, w := range resp.Writes {
		switch w.SourceKind {
		case "issue":
			if w.IssueID == nil || *w.IssueID != wrote || w.IssueIdentifier == nil || !w.SourceAccessible ||
				len(w.Superseded) != 1 || w.Superseded[0] != "旧派单" || w.DeletedLines == nil || *w.DeletedLines != 2 {
				t.Fatalf("issue write = %+v", w)
			}
		case "chat":
			if w.SourceAccessible || w.SourceTitle != "" || w.Layer != "boss" || w.DeletedLines != nil {
				t.Fatalf("a private chat's write leaked or misreported: %+v", w)
			}
			if len(w.Sources) != 2 || w.Sources[0].Identifier == nil || w.Sources[0].Title != "monitor wrote" ||
				w.Sources[1].ID != privateSource || w.Sources[1].Identifier != nil || w.Sources[1].Title != "" {
				t.Fatalf("a private source ticket leaked or misreported: %+v", w.Sources)
			}
		}
	}

	unsettled := map[string]string{}
	for _, u := range resp.Unsettled {
		unsettled[u.IssueID] = u.Reason
	}
	if unsettled[declaredNone] != "none" || unsettled[unaudited] != "unaudited" {
		t.Fatalf("unsettled = %+v", resp.Unsettled)
	}
	for _, id := range []string{wrote, old, idle} {
		if _, ok := unsettled[id]; ok {
			t.Fatalf("unsettled lists %s: %+v", id, resp.Unsettled)
		}
	}

	if resp.Rounds.Opened != 2 || resp.Rounds.Open != 1 || resp.Rounds.Idle != 1 {
		t.Fatalf("rounds = %+v", resp.Rounds)
	}
	for _, r := range resp.Rounds.Items {
		if r.IssueID == idle && (!r.Idle || len(r.Gap) != 1 || r.Gap[0] != "context") {
			t.Fatalf("idle round = %+v", r)
		}
	}

	if len(resp.Chats) != 1 {
		t.Fatalf("chats = %+v", resp.Chats)
	}
	c := resp.Chats[0]
	if c.ChatSessionID != chat || !c.Accessible || c.Title != "派单聊天" ||
		c.Dispatched != 3 || c.Reported != 1 || c.NoConclusion != 1 || c.Open != 1 || len(c.Tickets) != 3 {
		t.Fatalf("chat flow = %+v", c)
	}
	for _, ticket := range c.Tickets {
		if ticket.IssueID == reported && ticket.Flow != monitorFlowReported {
			t.Fatalf("reported ticket = %+v", ticket)
		}
	}
}
