package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/projectmemory"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func memoryProgressReady(t *testing.T) {
	t.Helper()
	if testHandler == nil {
		t.Skip("database not available")
	}
}

func mustPgUUID(t *testing.T, id string) pgtype.UUID {
	t.Helper()
	var u pgtype.UUID
	if err := u.Scan(id); err != nil {
		t.Fatal(err)
	}
	return u
}

func loadTestProject(t *testing.T, id string) db.Project {
	t.Helper()
	project, err := testHandler.Queries.GetProjectInWorkspace(context.Background(), db.GetProjectInWorkspaceParams{
		ID: mustPgUUID(t, id), WorkspaceID: mustPgUUID(t, testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("load project: %v", err)
	}
	return project
}

func loadTestIssue(t *testing.T, id string) db.Issue {
	t.Helper()
	issue, err := testHandler.Queries.GetIssue(context.Background(), mustPgUUID(t, id))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	return issue
}

func countSediment(t *testing.T, projectID string) int {
	t.Helper()
	return dbfx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id = $1 AND metadata @> jsonb_build_object('sediment_project', $2::text)`, testWorkspaceID, projectID)
}

func sedimentIDs(t *testing.T, projectID string) []string {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT id::text FROM issue WHERE workspace_id = $1 AND metadata @> jsonb_build_object('sediment_project', $2::text) ORDER BY created_at ASC, id ASC`, testWorkspaceID, projectID)
	if err != nil {
		t.Fatalf("list sediment: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func forgetSediment(t *testing.T, projectID string) {
	t.Helper()
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE workspace_id = $1 AND metadata @> jsonb_build_object('sediment_project', $2::text)`, testWorkspaceID, projectID)
	})
}

func pinSedimentSeat(t *testing.T, agentID string) {
	t.Helper()
	snapshotHandlerTestWorkspaceSettings(t)
	if agentID == "" {
		dbfx.Exec(t, `UPDATE workspace SET settings = COALESCE(settings, '{}'::jsonb) - 'memory' WHERE id = $1`, testWorkspaceID)
		return
	}
	dbfx.Exec(t, `UPDATE agent SET status = 'idle', work_enabled = true WHERE id = $1`, agentID)
	// jsonb_set leaves the row unchanged when an intermediate key is missing,
	// so the memory object is written in one step.
	dbfx.Exec(t, `
		UPDATE workspace
		SET settings = COALESCE(settings, '{}'::jsonb) || jsonb_build_object(
			'memory',
			COALESCE(settings->'memory', '{}'::jsonb) || jsonb_build_object('sediment_agent', $2::text)
		)
		WHERE id = $1::uuid
	`, testWorkspaceID, agentID)
	workspace, err := testHandler.Queries.GetWorkspace(context.Background(), mustPgUUID(t, testWorkspaceID))
	if err != nil {
		t.Fatal(err)
	}
	if _, cfgErr := configuredSedimentAgent(workspace.Settings); cfgErr != nil {
		t.Fatalf("sediment seat was not visible to the server: %v settings=%s", cfgErr, workspace.Settings)
	}
}

func openSedimentSeat(t *testing.T) string {
	t.Helper()
	agentID := createHandlerTestAgent(t, "sediment-"+t.Name(), []byte("[]"))
	pinSedimentSeat(t, agentID)
	return agentID
}

func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func putProjectStatus(t *testing.T, projectID, status string) *httptest.ResponseRecorder {
	t.Helper()
	syncIssueCounter(t)
	req := withURLParam(newRequest(http.MethodPut, "/api/projects/"+projectID, map[string]any{"status": status}), "id", projectID)
	rec := httptest.NewRecorder()
	testHandler.UpdateProject(rec, req)
	return rec
}

// Fixture rows take the next issue number directly, while a sediment ticket
// goes through the workspace counter. Leave the counter at least as high as
// the highest number already stored, or the create collides.
func syncIssueCounter(t *testing.T) {
	t.Helper()
	dbfx.Exec(t, `
		UPDATE workspace
		SET issue_counter = GREATEST(
			issue_counter,
			(SELECT COALESCE(MAX(number), 0) FROM issue WHERE workspace_id = $1)
		)
		WHERE id = $1
	`, testWorkspaceID)
}

func finishChild(t *testing.T, childID, from string) {
	t.Helper()
	syncIssueCounter(t)
	dbfx.Exec(t, `UPDATE issue SET status = 'done' WHERE id = $1`, childID)
	now := loadTestIssue(t, childID)
	prev := now
	prev.Status = from
	testHandler.notifyParentOfChildDone(context.Background(), prev, now)
}

func issueDescription(t *testing.T, id string) string {
	t.Helper()
	var desc *string
	dbfx.QueryRow(t, `SELECT description FROM issue WHERE id = $1`, id).Scan(&desc)
	if desc == nil {
		return ""
	}
	return *desc
}

func TestMemoryRoundStageAdvanceAndParentBoundary(t *testing.T) {
	memoryProgressReady(t)
	seat := openSedimentSeat(t)
	executor := createHandlerTestAgent(t, "stage-exec-"+t.Name(), []byte("[]"))
	projectID := dbfx.Project(t, "memory stage project")
	forgetSediment(t, projectID)
	parent := dbfx.Issue(t, "memory stage parent", testutil.Cols{
		"status": "in_progress", "project_id": projectID,
		"assignee_type": "member", "assignee_id": testUserID,
	})
	s1 := dbfx.Issue(t, "memory stage 1", testutil.Cols{
		"status": "in_progress", "parent_issue_id": parent, "project_id": projectID, "stage": 1,
	})
	s2 := dbfx.Issue(t, "memory stage 2", testutil.Cols{
		"status": "backlog", "parent_issue_id": parent, "project_id": projectID, "stage": 2,
		"assignee_type": "agent", "assignee_id": executor,
	})
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, s2)
	})

	// One open sibling does not close the stage.
	finishChild(t, s1, "in_progress")
	// s1 was the only stage-1 child, so the stage did close. Stage 2 is still open.
	if n := countSediment(t, projectID); n != 1 {
		t.Fatalf("stage close created %d sediment tickets, want 1", n)
	}
	first := sedimentIDs(t, projectID)[0]
	if !strings.Contains(issueDescription(t, first), "第 1 阶段已终态，下一阶段是 2") {
		t.Fatalf("description = %q", issueDescription(t, first))
	}
	if strings.Contains(issueDescription(t, first), "全部终态") {
		t.Fatalf("intermediate stage was recorded as the whole tree: %s", issueDescription(t, first))
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1 AND author_type = 'system'`, parent); n != 0 {
		t.Fatalf("member parent got %d system comments; the wake should stay skipped", n)
	}

	code, advanced := callStageAdvance(t, parent)
	if code != http.StatusOK || !advanced.Advanced || advanced.SedimentError != "" {
		t.Fatalf("advance = %d %+v", code, advanced)
	}
	if n := countSediment(t, projectID); n != 1 {
		t.Fatalf("advance opened %d tickets, want the same round", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1 AND content LIKE '%进入第 2 阶段%'`, first); n != 1 {
		t.Fatalf("advance reason comments = %d, want 1", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, first, seat); n < 1 {
		t.Fatalf("sediment seat was not dispatched, tasks = %d", n)
	}

	code, _ = callStageAdvance(t, parent)
	if code != http.StatusConflict {
		t.Fatalf("repeat advance = %d, want 409", code)
	}
	if n := countSediment(t, projectID); n != 1 {
		t.Fatalf("repeat advance created %d tickets", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1 AND content LIKE '%进入第 2 阶段%'`, first); n != 1 {
		t.Fatalf("repeat advance appended the reason again (%d)", n)
	}

	finishChild(t, s2, "backlog")
	if n := countSediment(t, projectID); n != 1 {
		t.Fatalf("last stage opened %d tickets, want the same round", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1 AND content LIKE '%父票子票全部终态%'`, first); n != 1 {
		t.Fatalf("all-terminal comments = %d, want 1", n)
	}

	// Replaying the same transition appends; it does not open another ticket.
	prev := loadTestIssue(t, s2)
	prev.Status = "backlog"
	testHandler.notifyParentOfChildDone(context.Background(), prev, loadTestIssue(t, s2))
	if n := countSediment(t, projectID); n != 1 {
		t.Fatalf("replay opened %d tickets", n)
	}
}

func TestMemoryRoundOrdinaryCloseDoesNotTrigger(t *testing.T) {
	memoryProgressReady(t)
	openSedimentSeat(t)
	projectID := dbfx.Project(t, "memory ordinary project")
	forgetSediment(t, projectID)
	alone := dbfx.Issue(t, "memory ordinary alone", testutil.Cols{"status": "in_progress", "project_id": projectID})
	w := closeIssueHTTP(t, alone, "", "", map[string]any{
		"outcome": "done", "evidence": "普通关单，没有大进展。",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("close = %d %s", w.Code, w.Body.String())
	}
	if n := countSediment(t, projectID); n != 0 {
		t.Fatalf("ordinary close created %d sediment tickets", n)
	}

	parent := dbfx.Issue(t, "memory ordinary parent", testutil.Cols{"status": "in_progress", "project_id": projectID})
	child := dbfx.Issue(t, "memory ordinary child", testutil.Cols{
		"status": "in_progress", "parent_issue_id": parent, "project_id": projectID,
	})
	dbfx.Issue(t, "memory ordinary sibling", testutil.Cols{
		"status": "in_progress", "parent_issue_id": parent, "project_id": projectID,
	})
	w = closeIssueHTTP(t, child, "", "", map[string]any{
		"outcome": "done", "evidence": "还有兄弟票，阶段没关。",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("child close = %d %s", w.Code, w.Body.String())
	}
	if n := countSediment(t, projectID); n != 0 {
		t.Fatalf("non-last child close created %d sediment tickets", n)
	}
}

func TestMemoryRoundProjectCompletedAndNewRound(t *testing.T) {
	memoryProgressReady(t)
	openSedimentSeat(t)
	projectID := dbfx.Project(t, "memory completed project")
	forgetSediment(t, projectID)

	w := putProjectStatus(t, projectID, "paused")
	if w.Code != http.StatusOK {
		t.Fatalf("pause = %d %s", w.Code, w.Body.String())
	}
	if n := countSediment(t, projectID); n != 0 {
		t.Fatalf("pause created %d sediment tickets", n)
	}

	w = putProjectStatus(t, projectID, "completed")
	if w.Code != http.StatusOK {
		t.Fatalf("complete = %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Status        string `json:"status"`
		SedimentError string `json:"sediment_error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "completed" || body.SedimentError != "" {
		t.Fatalf("complete response = %+v", body)
	}
	if n := countSediment(t, projectID); n != 1 {
		t.Fatalf("complete created %d tickets", n)
	}
	first := sedimentIDs(t, projectID)[0]
	var round1 string
	dbfx.QueryRow(t, `SELECT metadata->>'sediment_round' FROM issue WHERE id = $1`, first).Scan(&round1)
	if round1 == "" || !strings.Contains(issueDescription(t, first), "项目置为 completed") {
		t.Fatalf("round = %q description = %q", round1, issueDescription(t, first))
	}

	w = putProjectStatus(t, projectID, "completed")
	if w.Code != http.StatusOK {
		t.Fatalf("repeat complete = %d %s", w.Code, w.Body.String())
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1`, first); n != 0 {
		t.Fatalf("repeat complete appended %d comments", n)
	}

	w = putProjectStatus(t, projectID, "in_progress")
	if w.Code != http.StatusOK {
		t.Fatalf("reopen = %d %s", w.Code, w.Body.String())
	}
	w = putProjectStatus(t, projectID, "completed")
	if w.Code != http.StatusOK {
		t.Fatalf("second complete = %d %s", w.Code, w.Body.String())
	}
	if n := countSediment(t, projectID); n != 1 {
		t.Fatalf("second complete created %d tickets", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1 AND content LIKE '%项目置为 completed%'`, first); n != 1 {
		t.Fatalf("second complete comments = %d", n)
	}

	dbfx.Exec(t, `UPDATE issue SET status = 'done' WHERE id = $1`, first)
	w = putProjectStatus(t, projectID, "in_progress")
	if w.Code != http.StatusOK {
		t.Fatalf("reopen after round = %d %s", w.Code, w.Body.String())
	}
	w = putProjectStatus(t, projectID, "completed")
	if w.Code != http.StatusOK {
		t.Fatalf("new round = %d %s", w.Code, w.Body.String())
	}
	ids := sedimentIDs(t, projectID)
	if len(ids) != 2 {
		t.Fatalf("tickets after the round closed = %v", ids)
	}
	var round2 string
	dbfx.QueryRow(t, `SELECT metadata->>'sediment_round' FROM issue WHERE id = $1`, ids[1]).Scan(&round2)
	if round2 == "" || round2 == round1 {
		t.Fatalf("new round = %q, previous = %q", round2, round1)
	}
}

func TestMemoryRoundNoSeatDoesNotBlock(t *testing.T) {
	memoryProgressReady(t)
	pinSedimentSeat(t, "")
	logs := captureSlog(t)
	projectID := dbfx.Project(t, "memory noseat project")
	forgetSediment(t, projectID)
	parent := dbfx.Issue(t, "memory noseat parent", testutil.Cols{"status": "in_progress", "project_id": projectID})
	dbfx.Issue(t, "memory noseat s1", testutil.Cols{"status": "done", "parent_issue_id": parent, "project_id": projectID, "stage": 1})
	s2 := dbfx.Issue(t, "memory noseat s2", testutil.Cols{
		"status": "backlog", "parent_issue_id": parent, "project_id": projectID, "stage": 2,
	})

	code, advanced := callStageAdvance(t, parent)
	if code != http.StatusOK || !advanced.Advanced {
		t.Fatalf("advance without a seat = %d %+v", code, advanced)
	}
	if advanced.SedimentError == "" || !strings.Contains(logs.String(), "memory round: sediment skipped") {
		t.Fatalf("sediment error = %q logs = %s", advanced.SedimentError, logs.String())
	}
	var status string
	dbfx.QueryRow(t, `SELECT status FROM issue WHERE id = $1`, s2).Scan(&status)
	if status != "todo" {
		t.Fatalf("stage 2 status = %q, want todo; the business event was blocked", status)
	}
	if n := countSediment(t, projectID); n != 0 {
		t.Fatalf("missing seat created %d tickets", n)
	}

	w := putProjectStatus(t, projectID, "completed")
	if w.Code != http.StatusOK {
		t.Fatalf("complete without a seat = %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Status        string `json:"status"`
		SedimentError string `json:"sediment_error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "completed" || body.SedimentError == "" {
		t.Fatalf("complete response = %+v", body)
	}
	if n := countSediment(t, projectID); n != 0 {
		t.Fatalf("missing seat created %d tickets on complete", n)
	}
}

func TestMemoryRoundCrossProjectAndConcurrency(t *testing.T) {
	memoryProgressReady(t)
	openSedimentSeat(t)
	projectA := dbfx.Project(t, "memory project A")
	projectB := dbfx.Project(t, "memory project B")
	forgetSediment(t, projectA)
	forgetSediment(t, projectB)
	a := loadTestProject(t, projectA)
	b := loadTestProject(t, projectB)
	syncIssueCounter(t)

	if _, err := testHandler.EnsureMemoryRound(context.Background(), a, "项目 A 的原因"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 8)
	start := make(chan struct{})
	for i := 0; i < len(errs); i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = testHandler.EnsureMemoryRound(context.Background(), b, "项目 B 的并发原因")
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent call %d: %v", i, err)
		}
	}
	if n := countSediment(t, projectA); n != 1 {
		t.Fatalf("project A tickets = %d", n)
	}
	if n := countSediment(t, projectB); n != 1 {
		t.Fatalf("project B tickets = %d", n)
	}
	aID := sedimentIDs(t, projectA)[0]
	if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1 AND content LIKE '%项目 B%'`, aID); n != 0 {
		t.Fatalf("project B reason landed on project A (%d comments)", n)
	}
	if strings.Contains(issueDescription(t, aID), "项目 B") {
		t.Fatalf("project A description absorbed project B: %s", issueDescription(t, aID))
	}

	// A second sequential callback on B appends and stays on B.
	if _, err := testHandler.EnsureMemoryRound(context.Background(), b, "项目 B 的重复回调"); err != nil {
		t.Fatal(err)
	}
	if n := countSediment(t, projectB); n != 1 {
		t.Fatalf("repeat callback created %d tickets on B", n)
	}
	bID := sedimentIDs(t, projectB)[0]
	if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1 AND content = '项目 B 的重复回调'`, bID); n != 1 {
		t.Fatalf("repeat reason comments = %d", n)
	}

	// DENE-1154: the daemon re-reports an unchanged gap on every run. The
	// same reason right after itself is not appended again; a different
	// reason is, and the old reason returning after it is news again.
	for _, reason := range []string{"项目 B 的重复回调", "项目 B 的重复回调", "项目 B 的新缺口", "项目 B 的重复回调"} {
		if _, err := testHandler.EnsureMemoryRound(context.Background(), b, reason); err != nil {
			t.Fatal(err)
		}
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1 AND content = '项目 B 的重复回调'`, bID); n != 2 {
		t.Fatalf("unchanged reason stacked: %d copies, want 2", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1 AND content = '项目 B 的新缺口'`, bID); n != 1 {
		t.Fatalf("changed reason comments = %d, want 1", n)
	}
}

func TestMemoryRoundSkipsParkedParentAndOpenPull(t *testing.T) {
	memoryProgressReady(t)
	openSedimentSeat(t)
	projectID := dbfx.Project(t, "memory parked project")
	forgetSediment(t, projectID)
	parked := dbfx.Issue(t, "memory parked parent", testutil.Cols{"status": "backlog", "project_id": projectID})
	child := dbfx.Issue(t, "memory parked child", testutil.Cols{
		"status": "in_progress", "parent_issue_id": parked, "project_id": projectID,
	})
	finishChild(t, child, "in_progress")
	if n := countSediment(t, projectID); n != 0 {
		t.Fatalf("backlog parent created %d tickets", n)
	}

	parent := dbfx.Issue(t, "memory pull parent", testutil.Cols{"status": "in_progress", "project_id": projectID})
	waiting := dbfx.Issue(t, "memory pull child", testutil.Cols{
		"status": "in_progress", "parent_issue_id": parent, "project_id": projectID,
	})
	seedOpenPullForIssue(t, waiting, int(time.Now().UnixNano()%1_000_000_000))
	finishChild(t, waiting, "in_progress")
	if n := countSediment(t, projectID); n != 0 {
		t.Fatalf("unmerged pull request counted as terminal and created %d tickets", n)
	}
}

func TestProjectMemoryBriefLineIsPointerOnly(t *testing.T) {
	memoryProgressReady(t)
	projectID := dbfx.Project(t, "memory brief project")
	dbfx.Exec(t, `
		INSERT INTO project_memory_status (project_id, workspace_id, location_key, path, exists_on_disk, is_directory, observed_at, error)
		VALUES
			($1, $2, 'agents', 'AGENTS.md', true, false, now(), NULL),
			($1, $2, 'context', 'CONTEXT.md', false, false, now(), 'do not inject this file body')
	`, projectID, testWorkspaceID)
	project := loadTestProject(t, projectID)
	line := testHandler.projectMemoryBriefLine(context.Background(), project)
	if strings.Contains(line, "\n") {
		t.Fatalf("brief line is not one line: %q", line)
	}
	if strings.Contains(line, "do not inject this file body") {
		t.Fatalf("brief injected a status error / file body: %q", line)
	}
	for _, want := range []string{"map is `AGENTS.md`", "missing: `CONTEXT.md`", "`docs/adr/`", "`docs/README.md`", "`docs/evidence/INDEX.md`"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line %q missing %q", line, want)
		}
	}
	if strings.Contains(line, "missing: `AGENTS.md`") {
		t.Fatalf("present map was listed as missing: %q", line)
	}

	loaded, err := testHandler.resolveClaimProjectContext(context.Background(), project.ID, project.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	var resp AgentTaskResponse
	loaded.applyTo(&resp)
	if len(resp.Projects) != 1 || resp.Projects[0].MemoryLine != line {
		t.Fatalf("claim memory line = %#v, want %q", resp.Projects, line)
	}
}

func TestProjectMemoryCheck_SedimentSeatLifecycle(t *testing.T) {
	memoryProgressReady(t)
	projectID := dbfx.Project(t, "memory check seat lifecycle project")
	forgetSediment(t, projectID)

	// 1. Initial state: seat not configured
	pinSedimentSeat(t, "")

	localDir := bindMemoryLocalDirectory(t, projectID)
	callCheck := func() ProjectMemoryResponse {
		req := withURLParam(newRequest(http.MethodPost, "/api/projects/"+projectID+"/memory/check", map[string]any{
			"path": localDir,
			"locations": []map[string]any{
				{"key": "agents", "path": "AGENTS.md", "exists": false},
			},
		}), "id", projectID)
		rec := httptest.NewRecorder()
		testHandler.PostProjectMemoryCheck(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("check code = %d, want 200, body: %s", rec.Code, rec.Body.String())
		}
		var resp ProjectMemoryResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal resp: %v", err)
		}
		return resp
	}

	resp := callCheck()
	if resp.SedimentAgentConfigured {
		t.Fatalf("want SedimentAgentConfigured=false, got true")
	}
	if resp.SedimentError == nil || *resp.SedimentError != "memory.sediment_agent is not configured" {
		t.Fatalf("want 'memory.sediment_agent is not configured', got %q", *resp.SedimentError)
	}
	if n := countSediment(t, projectID); n != 0 {
		t.Fatalf("expected 0 sediment tickets without seat, got %d", n)
	}

	// 2. Configure sediment seat to an agent
	agentID := createHandlerTestAgent(t, "sediment-checker-"+t.Name(), []byte("[]"))
	pinSedimentSeat(t, agentID)

	resp = callCheck()
	if !resp.SedimentAgentConfigured {
		t.Fatalf("want SedimentAgentConfigured=true, got false")
	}
	if resp.SedimentIssue == nil {
		t.Fatalf("expected sediment issue to be created, got nil (sediment error: %v)", resp.SedimentError)
	}
	if n := countSediment(t, projectID); n != 1 {
		t.Fatalf("expected 1 sediment ticket after check, got %d", n)
	}
	issue := loadTestIssue(t, resp.SedimentIssue.ID)
	if issue.AssigneeID.String() != agentID {
		t.Fatalf("expected issue assignee to be %s, got %s", agentID, issue.AssigneeID.String())
	}

	// 3. Clear sediment seat again
	pinSedimentSeat(t, "")
	resp = callCheck()
	if resp.SedimentAgentConfigured {
		t.Fatalf("want SedimentAgentConfigured=false after clear, got true")
	}
	if resp.SedimentError == nil || *resp.SedimentError != "memory.sediment_agent is not configured" {
		t.Fatalf("want 'memory.sediment_agent is not configured' after clear, got %v", resp.SedimentError)
	}
}

func bindMemoryLocalDirectory(t *testing.T, projectID string) string {
	t.Helper()
	dir := "/tmp/memory-local-" + strings.ReplaceAll(t.Name(), "/", "-")
	dbfx.Exec(t, `
		INSERT INTO project_resource (project_id, workspace_id, resource_type, resource_ref, label, position)
		VALUES ($1, $2, 'local_directory', jsonb_build_object('local_path', $3::text), 'local', 0)
	`, projectID, testWorkspaceID, dir)
	return dir
}

func memoryObservation(present map[string]bool, mainline map[string]string) []projectmemory.LocationResult {
	results := make([]projectmemory.LocationResult, 0, len(projectmemory.Locations()))
	for _, location := range projectmemory.Locations() {
		results = append(results, projectmemory.LocationResult{
			Location: location, Exists: present[location.Key], MainlineRef: mainline[location.Key],
		})
	}
	return results
}

func allMemoryPresentExcept(keys ...string) map[string]bool {
	present := map[string]bool{}
	for _, key := range projectmemory.LocationKeys() {
		present[key] = true
	}
	for _, key := range keys {
		present[key] = false
	}
	return present
}

// DENE-1660: the local directory keeps lacking two slots the executor wrote
// in its own worktree. Once that round closes, the same gap reported again
// must not open another ticket; a new missing slot must.
func TestProjectMemoryClosedRoundHoldsSameGap(t *testing.T) {
	memoryProgressReady(t)
	openSedimentSeat(t)
	projectID := dbfx.Project(t, "memory gap cooldown project")
	forgetSediment(t, projectID)
	syncIssueCounter(t)
	project := loadTestProject(t, projectID)
	ctx := context.Background()

	gap := allMemoryPresentExcept(projectmemory.LocationDocs, projectmemory.LocationEvidence)
	if _, err := testHandler.observeLocalMemory(ctx, project, memoryObservation(gap, nil)); err != nil {
		t.Fatal(err)
	}
	ids := sedimentIDs(t, projectID)
	if len(ids) != 1 {
		t.Fatalf("first gap opened %d rounds, want 1", len(ids))
	}
	dbfx.Exec(t, `UPDATE issue SET status = 'done' WHERE id = $1`, ids[0])

	// Same gap, and a smaller one, after the round closed: held back.
	for _, observed := range []map[string]bool{gap, allMemoryPresentExcept(projectmemory.LocationDocs)} {
		resp, err := testHandler.observeLocalMemory(ctx, project, memoryObservation(observed, nil))
		if err != nil {
			t.Fatal(err)
		}
		if n := countSediment(t, projectID); n != 1 {
			t.Fatalf("closed round with the same gap reopened: %d rounds", n)
		}
		if resp.SedimentError != nil {
			t.Fatalf("held-back gap reported an error: %s", *resp.SedimentError)
		}
	}

	// A new missing slot opens a fresh round.
	syncIssueCounter(t)
	wider := allMemoryPresentExcept(projectmemory.LocationDocs, projectmemory.LocationEvidence, projectmemory.LocationContext)
	if _, err := testHandler.observeLocalMemory(ctx, project, memoryObservation(wider, nil)); err != nil {
		t.Fatal(err)
	}
	if n := countSediment(t, projectID); n != 2 {
		t.Fatalf("changed gap opened %d rounds in total, want 2", n)
	}
}

func TestProjectMemoryGapReopensAfterCooldown(t *testing.T) {
	memoryProgressReady(t)
	openSedimentSeat(t)
	projectID := dbfx.Project(t, "memory gap cooldown expiry project")
	forgetSediment(t, projectID)
	syncIssueCounter(t)
	project := loadTestProject(t, projectID)
	ctx := context.Background()

	gap := allMemoryPresentExcept(projectmemory.LocationDocs)
	if _, err := testHandler.observeLocalMemory(ctx, project, memoryObservation(gap, nil)); err != nil {
		t.Fatal(err)
	}
	ids := sedimentIDs(t, projectID)
	if len(ids) != 1 {
		t.Fatalf("first gap opened %d rounds, want 1", len(ids))
	}
	dbfx.Exec(t, `UPDATE issue SET status = 'done', updated_at = now() - interval '8 days' WHERE id = $1`, ids[0])
	syncIssueCounter(t)
	if _, err := testHandler.observeLocalMemory(ctx, project, memoryObservation(gap, nil)); err != nil {
		t.Fatal(err)
	}
	if n := countSediment(t, projectID); n != 2 {
		t.Fatalf("gap after cooldown opened %d rounds in total, want 2", n)
	}
}

// A worktree self-check never overwrites the local directory's observation
// and never opens a round; the local directory itself still counts.
func TestProjectMemoryWorktreeCheckIsNotAnObservation(t *testing.T) {
	memoryProgressReady(t)
	openSedimentSeat(t)
	projectID := dbfx.Project(t, "memory worktree check project")
	forgetSediment(t, projectID)
	syncIssueCounter(t)
	localDir := bindMemoryLocalDirectory(t, projectID)
	project := loadTestProject(t, projectID)

	if _, err := testHandler.observeLocalMemory(context.Background(), project,
		memoryObservation(allMemoryPresentExcept(projectmemory.LocationDocs), nil)); err != nil {
		t.Fatal(err)
	}
	post := func(path string, observed map[string]bool) ProjectMemoryResponse {
		results := memoryObservation(observed, nil)
		req := withURLParam(newRequest(http.MethodPost, "/api/projects/"+projectID+"/memory/check", map[string]any{
			"path": path, "locations": results,
		}), "id", projectID)
		rec := httptest.NewRecorder()
		testHandler.PostProjectMemoryCheck(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("check code = %d: %s", rec.Code, rec.Body.String())
		}
		var resp ProjectMemoryResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := post("/tmp/some-agent-worktree", allMemoryPresentExcept())
	if resp.WorktreeCheck == nil || len(resp.WorktreeCheck.Missing) != 0 || resp.WorktreeCheck.Path != "/tmp/some-agent-worktree" {
		t.Fatalf("worktree check = %#v, want a complete self-check echoed back", resp.WorktreeCheck)
	}
	if len(resp.Missing) != 1 || resp.Missing[0] != projectmemory.LocationDocs || resp.Source != "local_directory" {
		t.Fatalf("local observation = %v from %q, want docs_index still missing locally", resp.Missing, resp.Source)
	}

	resp = post(localDir+"/", allMemoryPresentExcept())
	if resp.WorktreeCheck != nil || len(resp.Missing) != 0 {
		t.Fatalf("local directory check = %#v missing %v, want an observation with nothing missing", resp.WorktreeCheck, resp.Missing)
	}
}

// A slot the remote mainline already has is reported as "sync the local
// directory", not as something to write again.
func TestProjectMemoryBehindMainlineSaysSync(t *testing.T) {
	memoryProgressReady(t)
	openSedimentSeat(t)
	projectID := dbfx.Project(t, "memory behind mainline project")
	forgetSediment(t, projectID)
	syncIssueCounter(t)
	project := loadTestProject(t, projectID)

	resp, err := testHandler.observeLocalMemory(context.Background(), project, memoryObservation(
		allMemoryPresentExcept(projectmemory.LocationDocs, projectmemory.LocationEvidence),
		map[string]string{projectmemory.LocationDocs: "origin/dev"},
	))
	if err != nil {
		t.Fatal(err)
	}
	var docs ProjectMemoryLocation
	for _, location := range resp.Locations {
		if location.Key == projectmemory.LocationDocs {
			docs = location
		}
	}
	if docs.MainlineRef == nil || *docs.MainlineRef != "origin/dev" {
		t.Fatalf("docs_index mainline_ref = %v, want origin/dev", docs.MainlineRef)
	}
	ids := sedimentIDs(t, projectID)
	if len(ids) != 1 {
		t.Fatalf("rounds = %d, want 1", len(ids))
	}
	desc := issueDescription(t, ids[0])
	if !strings.Contains(desc, "本地目录落后，需要同步") || !strings.Contains(desc, "origin/dev 已有 docs_index") ||
		!strings.Contains(desc, "缺少项目记忆位置：evidence_index") {
		t.Fatalf("description does not separate behind from missing:\n%s", desc)
	}
}
