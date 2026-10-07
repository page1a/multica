package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// DENE-1451: domains are one workspace list; projects carry several, an issue
// picks one of its project's, a specialisation is base role + one domain.

func createTestDomain(t *testing.T, name string) string {
	t.Helper()
	resp := testutil.Call(t, testHandler.CreateDomain,
		newRequest(http.MethodPost, "/api/domains", map[string]any{"name": name})).Want(http.StatusCreated).Map()
	id, _ := resp["id"].(string)
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM workspace_domain WHERE id = $1`, id) })
	return id
}

func createDomainProject(t *testing.T, title string, domains ...string) map[string]any {
	t.Helper()
	resp := testutil.Call(t, testHandler.CreateProject, newRequest(http.MethodPost, "/api/projects", map[string]any{
		"title": title, "domain_ids": domains,
	})).Want(http.StatusCreated).Map()
	id, _ := resp["id"].(string)
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, id) })
	return resp
}

func createDomainIssue(t *testing.T, body map[string]any, want int) map[string]any {
	t.Helper()
	req := newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, body)
	req = req.WithContext(withSkipIssueRouting(req.Context()))
	resp := testutil.Call(t, testHandler.CreateIssue, req).Want(want).Map()
	if id, _ := resp["id"].(string); id != "" {
		t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, id) })
	}
	return resp
}

func domainUsage(t *testing.T, id string) (projects, agents float64) {
	t.Helper()
	resp := testutil.Call(t, testHandler.ListDomains, newRequest(http.MethodGet, "/api/domains", nil)).Want(http.StatusOK).Map()
	list, _ := resp["domains"].([]any)
	for _, raw := range list {
		d, _ := raw.(map[string]any)
		if d["id"] == id {
			return d["project_count"].(float64), d["agent_count"].(float64)
		}
	}
	t.Fatalf("domain %s not listed", id)
	return
}

func TestDomainListCountsUsageAndRefusesToDeleteAUsedDomain(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	sea := createTestDomain(t, "T出海")
	createDomainProject(t, "domain usage project", sea)

	if p, a := domainUsage(t, sea); p != 1 || a != 0 {
		t.Fatalf("usage = %v projects %v agents, want 1/0", p, a)
	}
	testutil.Call(t, testHandler.DeleteDomain,
		withURLParam(newRequest(http.MethodDelete, "/api/domains/"+sea, nil), "id", sea)).Want(http.StatusConflict)

	// 通用 is the absence of a domain and can never be one.
	testutil.Call(t, testHandler.CreateDomain,
		newRequest(http.MethodPost, "/api/domains", map[string]any{"name": "通用"})).Want(http.StatusBadRequest)

	unused := createTestDomain(t, "T空闲")
	testutil.Call(t, testHandler.DeleteDomain,
		withURLParam(newRequest(http.MethodDelete, "/api/domains/"+unused, nil), "id", unused)).Want(http.StatusNoContent)
}

func TestIssueDomainFollowsItsProject(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	sea := createTestDomain(t, "T出海2")
	media := createTestDomain(t, "T自媒体2")
	game := createTestDomain(t, "T游戏2")
	single := createDomainProject(t, "domain single", media)
	multi := createDomainProject(t, "domain tarot", sea, media)

	// One project domain fills itself in.
	resp := createDomainIssue(t, map[string]any{"title": "auto", "project_id": single["id"]}, http.StatusCreated)
	if resp["domain_id"] != media {
		t.Fatalf("single-domain project: domain_id = %v, want %s", resp["domain_id"], media)
	}
	// Several: the issue stays generic until one is picked.
	resp = createDomainIssue(t, map[string]any{"title": "generic", "project_id": multi["id"]}, http.StatusCreated)
	if resp["domain_id"] != "" {
		t.Fatalf("multi-domain project: domain_id = %v, want generic", resp["domain_id"])
	}
	// Picked by name, one of the project's.
	resp = createDomainIssue(t, map[string]any{"title": "picked", "project_id": multi["id"], "domain_id": "T自媒体2"}, http.StatusCreated)
	if resp["domain_id"] != media {
		t.Fatalf("picked: domain_id = %v, want %s", resp["domain_id"], media)
	}
	// Not one of the project's: refused.
	createDomainIssue(t, map[string]any{"title": "outside", "project_id": multi["id"], "domain_id": game}, http.StatusBadRequest)

	// Update: pick, then clear back to generic.
	id := resp["id"].(string)
	upd := testutil.Call(t, testHandler.UpdateIssue, withURLParam(newRequest(http.MethodPut, "/api/issues/"+id,
		map[string]any{"domain_id": sea}), "id", id)).Want(http.StatusOK).Map()
	if upd["domain_id"] != sea {
		t.Fatalf("update: domain_id = %v, want %s", upd["domain_id"], sea)
	}
	upd = testutil.Call(t, testHandler.UpdateIssue, withURLParam(newRequest(http.MethodPut, "/api/issues/"+id,
		map[string]any{"domain_id": nil}), "id", id)).Want(http.StatusOK).Map()
	if upd["domain_id"] != "" {
		t.Fatalf("clear: domain_id = %v, want generic", upd["domain_id"])
	}
	testutil.Call(t, testHandler.UpdateIssue, withURLParam(newRequest(http.MethodPut, "/api/issues/"+id,
		map[string]any{"domain_id": game}), "id", id)).Want(http.StatusBadRequest)
}

func TestProjectRenameKeepsItsDomains(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	sea := createTestDomain(t, "T出海3")
	media := createTestDomain(t, "T自媒体3")
	p := createDomainProject(t, "tarot-before", sea, media)
	id := p["id"].(string)

	resp := testutil.Call(t, testHandler.UpdateProject, withURLParam(newRequest(http.MethodPut, "/api/projects/"+id,
		map[string]any{"title": "tarot-after"}), "id", id)).Want(http.StatusOK).Map()
	got, _ := resp["domain_ids"].([]any)
	if len(got) != 2 || got[0] != sea || got[1] != media {
		t.Fatalf("domain_ids after rename = %v, want [%s %s]", got, sea, media)
	}
}

func TestSpecialisationIsBaseRolePlusDomainOncePerDomain(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	createTestDomain(t, "T中转")
	base := createHandlerTestAgent(t, "域基础", []byte("[]"))

	req := specRequest(http.MethodPost, "/api/agents", map[string]any{"parent_agent_id": base, "domain_id": "T中转"})
	child := testutil.Decode[AgentResponse](t, testHandler.CreateAgent, req, http.StatusCreated)
	cleanupAPIAgent(t, child.ID)
	if child.Name != "域基础T中转" || child.DomainID == "" {
		t.Fatalf("child = %q domain %q, want the generated name and a recorded domain", child.Name, child.DomainID)
	}

	again := specRequest(http.MethodPost, "/api/agents", map[string]any{"parent_agent_id": base, "domain_id": "T中转", "name": "另一个"})
	testutil.Call(t, testHandler.CreateAgent, again).Want(http.StatusConflict)

	// A domain needs a base role.
	orphan := specRequest(http.MethodPost, "/api/agents", map[string]any{"name": "无基础", "domain_id": "T中转"})
	testutil.Call(t, testHandler.CreateAgent, orphan).Want(http.StatusBadRequest)
}

func TestQuoteNamingTheBaseRoleLandsOnTheIssueDomainSpecialisation(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	enableDraftSuggestRouting(t, tierJudge{tier: "medium", confidence: 0.95})
	sea := createTestDomain(t, "T出海5")
	project := createDomainProject(t, "domain quote project", sea)

	f := originRun(t, "domainquote", "交给 Dom Goku 做", "member", testUserID, testUserID)
	base := createHandlerTestAgent(t, "Dom Goku", []byte("[]"))
	spec := specialisation(t, "Dom GokuT出海5", base)
	dbfx.Exec(t, `UPDATE agent SET domain_id = $1 WHERE id = $2`, sea, spec)

	body := map[string]any{
		"title": "出海 work", "status": "todo", "project_id": project["id"],
		"assignee_type": "agent", "assignee_id": base, "assignee_quote": "交给 Dom Goku 做",
	}
	req := newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, body)
	req.Header.Set("X-Agent-ID", f.caller)
	req.Header.Set("X-Task-ID", f.task)
	req = req.WithContext(withSkipIssueRouting(req.Context()))
	resp := testutil.Call(t, testHandler.CreateIssue, req).Want(http.StatusCreated).Map()
	id := resp["id"].(string)
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, id) })

	assignee, source, _, _ := originOf(t, id)
	if assignee == nil || *assignee != spec || source == nil || *source != "quote" {
		t.Fatalf("assignee = %v source = %v, want the 出海 specialisation by quote", assignee, source)
	}

	// Naming the specialisation itself is kept as said, even off-domain.
	game := createTestDomain(t, "T游戏5")
	gameSpec := specialisation(t, "Dom GokuT游戏5", base)
	dbfx.Exec(t, `UPDATE agent SET domain_id = $1 WHERE id = $2`, game, gameSpec)
	f2 := originRun(t, "domainquote2", "交给 Dom GokuT游戏5 做", "member", testUserID, testUserID)
	body["assignee_id"], body["assignee_quote"] = gameSpec, "交给 Dom GokuT游戏5 做"
	body["title"] = "出海 work named"
	req = newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, body)
	req.Header.Set("X-Agent-ID", f2.caller)
	req.Header.Set("X-Task-ID", f2.task)
	req = req.WithContext(withSkipIssueRouting(req.Context()))
	resp = testutil.Call(t, testHandler.CreateIssue, req).Want(http.StatusCreated).Map()
	id2 := resp["id"].(string)
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, id2) })
	if assignee, _, _, _ := originOf(t, id2); assignee == nil || *assignee != gameSpec {
		t.Fatalf("assignee = %v, a named specialisation must stand", assignee)
	}
}

func TestAgentListForProjectGroupsByFit(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// DENE-1477: one judgement, read the same way dispatch reads it.
	sea := createTestDomain(t, "T出海6")
	game := createTestDomain(t, "T游戏6")
	project := createDomainProject(t, "fit list project", sea)
	generic := createDomainProject(t, "fit list generic project")

	base := createHandlerTestAgent(t, "Fit Goku", []byte("[]"))
	seaSpec := specialisation(t, "Fit GokuT出海6", base)
	dbfx.Exec(t, `UPDATE agent SET domain_id = $1 WHERE id = $2`, sea, seaSpec)
	gameSpec := specialisation(t, "Fit GokuT游戏6", base)
	dbfx.Exec(t, `UPDATE agent SET domain_id = $1 WHERE id = $2`, game, gameSpec)

	fits := func(query string) (map[string]string, []string) {
		t.Helper()
		list := testutil.Decode[[]AgentResponse](t, testHandler.ListAgents,
			newRequest(http.MethodGet, "/api/agents?workspace_id="+testWorkspaceID+"&"+query, nil), http.StatusOK)
		out := map[string]string{}
		order := []string{}
		for _, a := range list {
			if a.Fit == "" {
				t.Fatalf("%s: agent %s has no fit", query, a.Name)
			}
			out[a.ID] = a.Fit
			order = append(order, a.Fit)
		}
		return out, order
	}
	ranked := func(order []string) bool {
		rank := map[string]int{"match": 0, "generic": 1, "other": 2}
		for i := 1; i < len(order); i++ {
			if rank[order[i]] < rank[order[i-1]] {
				return false
			}
		}
		return true
	}

	got, order := fits("for_project=" + project["id"].(string))
	if got[seaSpec] != "match" || got[base] != "generic" || got[gameSpec] != "other" || !ranked(order) {
		t.Fatalf("出海 project: base %s 出海 %s 游戏 %s order %v", got[base], got[seaSpec], got[gameSpec], order)
	}
	got, order = fits("for_project=" + generic["id"].(string))
	if got[base] != "match" || got[seaSpec] != "other" || !ranked(order) {
		t.Fatalf("generic project: base %s 出海 %s order %v", got[base], got[seaSpec], order)
	}

	issue := createDomainIssue(t, map[string]any{"title": "fit list issue", "project_id": project["id"]}, http.StatusCreated)
	if got, _ = fits("for_issue=" + issue["id"].(string)); got[seaSpec] != "match" {
		t.Fatalf("issue in the 出海 project: 出海 fit %s", got[seaSpec])
	}
	testutil.Call(t, testHandler.ListAgents, newRequest(http.MethodGet,
		"/api/agents?workspace_id="+testWorkspaceID+"&for_issue="+issue["id"].(string)+"&for_project="+project["id"].(string), nil)).Want(http.StatusBadRequest)
}

func TestQuoteNamingTheBaseRoleInAMultiDomainProjectLandsOnAFittingSpecialisation(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// Rule 1 of DENE-1477: no issue domain picked, so the project's domains,
	// all of them, are the scene — the base role's 游戏 seat fits.
	enableDraftSuggestRouting(t, tierJudge{tier: "medium", confidence: 0.95})
	sea := createTestDomain(t, "T出海7")
	game := createTestDomain(t, "T游戏7")
	project := createDomainProject(t, "multi domain quote project", sea, game)

	f := originRun(t, "multiquote", "交给 Multi Goku 做", "member", testUserID, testUserID)
	base := createHandlerTestAgent(t, "Multi Goku", []byte("[]"))
	spec := specialisation(t, "Multi GokuT游戏7", base)
	dbfx.Exec(t, `UPDATE agent SET domain_id = $1 WHERE id = $2`, game, spec)

	req := newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title": "multi domain work", "status": "todo", "project_id": project["id"],
		"assignee_type": "agent", "assignee_id": base, "assignee_quote": "交给 Multi Goku 做",
	})
	req.Header.Set("X-Agent-ID", f.caller)
	req.Header.Set("X-Task-ID", f.task)
	req = req.WithContext(withSkipIssueRouting(req.Context()))
	resp := testutil.Call(t, testHandler.CreateIssue, req).Want(http.StatusCreated).Map()
	id := resp["id"].(string)
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, id) })

	if assignee, _, _, _ := originOf(t, id); assignee == nil || *assignee != spec {
		t.Fatalf("assignee = %v, want the 游戏 specialisation", assignee)
	}
}
