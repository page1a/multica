package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
)

const (
	canaryEnv    = "CANARY_ENV_9f3a"
	canaryMCP    = "CANARY_MCP_7b1c"
	canaryGW     = "CANARY_GW_2e8d"
	canarySig    = "CANARY_SIG_4c6f"
	canaryMCPLib = "CANARY_MCPLIB_5d2a"
)

func configReq(method, path, wsID string, body any) *http.Request {
	return testutil.WithURLParams(
		testutil.WithHeaders(testutil.JSONRequest(method, path, body), "X-User-ID", testUserID),
		"id", wsID,
	)
}

// configWorkspace creates a workspace the way the real create path does: the
// 7 built-in issue statuses are seeded inside the same transaction
// (handler/workspace.go), so every workspace a user can import into already
// carries them.
func configWorkspace(t *testing.T, name, slug, prefix string) string {
	t.Helper()
	id := dbfx.Workspace(t, name, slug, testutil.Cols{"issue_prefix": prefix})
	dbfx.Member(t, id, testUserID, "owner")
	if err := testHandler.Queries.SeedIssueStatusEntries(context.Background(), util.MustParseUUID(id)); err != nil {
		t.Fatalf("seed statuses for %s: %v", slug, err)
	}
	return id
}

func setupConfigWorkspaces(t *testing.T) (src, dst string) {
	t.Helper()
	suf := uuid.NewString()[:8]
	return configWorkspace(t, "CfgSrc "+suf, "cfgsrc-"+suf, ""),
		configWorkspace(t, "CfgDst "+suf, "cfgdst-"+suf, "")
}

func exportBundle(t *testing.T, wsID string) service.ConfigBundle {
	t.Helper()
	var bundle service.ConfigBundle
	testutil.Call(t, testHandler.ExportWorkspaceConfig, configReq("GET", "/api/workspaces/"+wsID+"/config/export", wsID, nil)).
		Want(http.StatusOK).JSON(&bundle)
	return bundle
}

func importReport(t *testing.T, wsID string, body any, want int) service.ConfigImportReport {
	t.Helper()
	resp := testutil.Call(t, testHandler.ImportWorkspaceConfig, configReq("POST", "/api/workspaces/"+wsID+"/config/import", wsID, body))
	if resp.Code != want {
		var wrapped struct {
			Code   string                     `json:"code"`
			Error  string                     `json:"error"`
			Report service.ConfigImportReport `json:"report"`
		}
		_ = json.Unmarshal(resp.Body.Bytes(), &wrapped)
		if wrapped.Report.BundleID != "" {
			if want == http.StatusConflict || want == http.StatusUnprocessableEntity {
				return wrapped.Report
			}
		}
	}
	resp.Want(want)
	if want != http.StatusOK {
		var wrapped struct {
			Report service.ConfigImportReport `json:"report"`
		}
		resp.JSON(&wrapped)
		return wrapped.Report
	}
	var report service.ConfigImportReport
	resp.JSON(&report)
	return report
}

func TestWorkspaceConfigExport_CanarySecretsOmitted(t *testing.T) {
	src, _ := setupConfigWorkspaces(t)
	agentName := "CanaryBot-" + uuid.NewString()[:8]
	agentID := dbfx.Agent(t, agentName, "", testutil.Cols{
		"workspace_id":   src,
		"runtime_config": testutil.Raw(fmt.Sprintf(`'{"mode":"gateway","gateway":{"url":"http://127.0.0.1:18789","token":"%s"}}'::jsonb`, canaryGW)),
		"custom_env":     testutil.Raw(fmt.Sprintf(`'{"%s":"secret-value"}'::jsonb`, canaryEnv)),
		"mcp_config":     testutil.Raw(fmt.Sprintf(`'{"env":{"KEY":"%s"}}'::jsonb`, canaryMCP)),
		"visibility":     "workspace",
	})
	mcpName := "canary-mcp-" + uuid.NewString()[:8]
	dbfx.Insert(t, "workspace_mcp_server", testutil.Cols{
		"workspace_id": src,
		"name":         mcpName,
		"config":       testutil.Raw(fmt.Sprintf(`'{"type":"stdio","command":"npx","env":{"KEY":"%s"}}'::jsonb`, canaryMCPLib)),
		"created_by":   testUserID,
	})
	apID := dbfx.Insert(t, "autopilot", testutil.Cols{
		"workspace_id":    src,
		"title":           "Canary AP " + uuid.NewString()[:6],
		"assignee_type":   "agent",
		"assignee_id":     agentID,
		"status":          "paused",
		"execution_mode":  "create_issue",
		"created_by_type": "member",
		"created_by_id":   testUserID,
	})
	dbfx.Insert(t, "autopilot_trigger", testutil.Cols{
		"autopilot_id":    apID,
		"kind":            "webhook",
		"enabled":         true,
		"label":           "CI",
		"webhook_token":   "awt_canarytokenvalue",
		"signing_secret":  canarySig,
		"created_by_type": "member",
		"created_by_id":   testUserID,
	})

	resp := testutil.Call(t, testHandler.ExportWorkspaceConfig, configReq("GET", "/api/workspaces/"+src+"/config/export", src, nil)).Want(http.StatusOK)
	body := resp.Text()
	for _, needle := range []string{canaryEnv, canaryMCP, canaryGW, canarySig, canaryMCPLib, "***"} {
		if strings.Contains(body, needle) {
			t.Errorf("export leaked %q", needle)
		}
	}

	var bundle service.ConfigBundle
	resp.JSON(&bundle)
	wantFields := map[string]bool{
		"custom_env": false, "mcp_config": false, "runtime_config.gateway.token": false,
		"webhook_token": false, "signing_secret": false, "config": false,
	}
	for _, s := range bundle.SecretsOmitted {
		wantFields[s.Field] = true
	}
	for field, ok := range wantFields {
		if !ok {
			t.Errorf("secrets_omitted missing field %s", field)
		}
	}
	if bundle.Format != service.ConfigBundleFormat {
		t.Errorf("format = %q", bundle.Format)
	}
	if cd := resp.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("missing Content-Disposition attachment, got %q", cd)
	}
	if resp.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", resp.Header().Get("Cache-Control"))
	}
}

func TestWorkspaceConfigImport_RejectsSecret(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	dry := true
	body := map[string]any{
		"dry_run":     dry,
		"on_conflict": "skip",
		"bundle": map[string]any{
			"format":         service.ConfigBundleFormat,
			"schema_version": 1,
			"bundle_id":      uuid.NewString(),
			"exported_at":    "2026-09-15T00:00:00Z",
			"source":         map[string]any{"workspace_id": uuid.NewString(), "slug": "x", "name": "x", "exported_by": testUserID},
			"entities": map[string]any{
				"agents": []map[string]any{{
					"source_id":  uuid.NewString(),
					"name":       "leaky",
					"custom_env": map[string]string{"K": "v"},
				}},
			},
		},
	}
	resp := testutil.Call(t, testHandler.ImportWorkspaceConfig, configReq("POST", "/api/workspaces/"+dst+"/config/import", dst, body))
	resp.Want(http.StatusBadRequest)
	got := resp.Map()
	if got["code"] != "config_bundle_contains_secret" {
		t.Fatalf("code = %v body=%s", got["code"], resp.Text())
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent WHERE workspace_id = $1 AND name = 'leaky'`, dst); n != 0 {
		t.Fatalf("reject-secret wrote an agent")
	}
}

func TestWorkspaceConfigImport_OverwritePreservesCustomEnv(t *testing.T) {
	src, dst := setupConfigWorkspaces(t)
	name := "KeepEnv-" + uuid.NewString()[:8]
	dbfx.Agent(t, name, "", testutil.Cols{
		"workspace_id": src,
		"custom_env":   testutil.Raw(`'{"SRC":"1"}'::jsonb`),
		"visibility":   "workspace",
	})
	dstAgent := dbfx.Agent(t, name, "", testutil.Cols{
		"workspace_id": dst,
		"custom_env":   testutil.Raw(`'{"DST_A":"1","DST_B":"2"}'::jsonb`),
		"visibility":   "workspace",
	})
	bundle := exportBundle(t, src)
	dry := false
	_ = importReport(t, dst, map[string]any{
		"bundle": bundle, "dry_run": dry, "on_conflict": "overwrite", "include": []string{"agents"},
	}, http.StatusOK)
	var keyCount int
	dbfx.QueryRow(t, `SELECT (SELECT count(*) FROM jsonb_object_keys(COALESCE(custom_env, '{}'::jsonb))) FROM agent WHERE id = $1`, dstAgent).Scan(&keyCount)
	if keyCount != 2 {
		t.Fatalf("overwrite clobbered custom_env; key_count=%d want 2", keyCount)
	}
}

// Regression: overwrite used to replace runtime_config wholesale (dropping the
// target gateway token) and recreate webhook triggers without their
// signing_secret, which silently disabled signature verification.
func TestWorkspaceConfigImport_OverwritePreservesGatewayTokenAndSigningSecret(t *testing.T) {
	src, dst := setupConfigWorkspaces(t)
	suf := uuid.NewString()[:8]
	agentName := "GW-" + suf
	apTitle := "Hook-" + suf
	srcAgent := dbfx.Agent(t, agentName, "", testutil.Cols{
		"workspace_id":   src,
		"runtime_config": testutil.Raw(`'{"mode":"gateway","gateway":{"url":"http://new","token":"SRC_GW"}}'::jsonb`),
		"visibility":     "workspace",
	})
	dstAgent := dbfx.Agent(t, agentName, "", testutil.Cols{
		"workspace_id":   dst,
		"runtime_config": testutil.Raw(`'{"mode":"gateway","gateway":{"url":"http://old","token":"DST_GW"}}'::jsonb`),
		"visibility":     "workspace",
	})
	for ws, agent := range map[string]string{src: srcAgent, dst: dstAgent} {
		apID := dbfx.Insert(t, "autopilot", testutil.Cols{
			"workspace_id": ws, "title": apTitle, "assignee_type": "agent", "assignee_id": agent,
			"status": "paused", "execution_mode": "create_issue", "created_by_type": "member", "created_by_id": testUserID,
		})
		dbfx.Insert(t, "autopilot_trigger", testutil.Cols{
			"autopilot_id": apID, "kind": "webhook", "enabled": true, "label": "CI",
			"webhook_token": "awt_" + uuid.NewString(), "signing_secret": "SIG_" + ws,
			"created_by_type": "member", "created_by_id": testUserID,
		})
	}

	bundle := exportBundle(t, src)
	_ = importReport(t, dst, map[string]any{
		"bundle": bundle, "dry_run": false, "on_conflict": "overwrite", "include": []string{"agents", "autopilots"},
	}, http.StatusOK)

	var token, url string
	dbfx.QueryRow(t, `SELECT runtime_config #>> '{gateway,token}', runtime_config #>> '{gateway,url}' FROM agent WHERE id = $1`, dstAgent).Scan(&token, &url)
	if token != "DST_GW" || url != "http://new" {
		t.Fatalf("overwrite gateway: token=%q url=%q, want DST_GW / http://new", token, url)
	}
	var secret string
	dbfx.QueryRow(t, `SELECT COALESCE(tr.signing_secret, '') FROM autopilot_trigger tr JOIN autopilot a ON a.id = tr.autopilot_id
		WHERE a.workspace_id = $1 AND a.title = $2 AND tr.kind = 'webhook'`, dst, apTitle).Scan(&secret)
	if secret != "SIG_"+dst {
		t.Fatalf("overwrite dropped target signing_secret: got %q", secret)
	}
}

// Regression: a squad leader that is also listed as a regular agent member
// (leader reassigned to an existing member) used to hit squad_member's unique
// key, abort the batch transaction and roll back every squad.
func TestWorkspaceConfigImport_SquadLeaderAlsoMember(t *testing.T) {
	src, dst := setupConfigWorkspaces(t)
	suf := uuid.NewString()[:8]
	leader := dbfx.Agent(t, "Leader-"+suf, "", testutil.Cols{"workspace_id": src, "visibility": "workspace"})
	squadName := "LeadDup-" + suf
	squadID := dbfx.Squad(t, squadName, leader, testutil.Cols{"workspace_id": src})
	dbfx.SquadMember(t, squadID, "agent", leader, testutil.Cols{"role": "worker"})

	bundle := exportBundle(t, src)
	_ = importReport(t, dst, map[string]any{
		"bundle": bundle, "dry_run": false, "on_conflict": "skip", "include": []string{"agents", "squads"},
	}, http.StatusOK)
	if n := dbfx.Count(t, `SELECT count(*) FROM squad WHERE workspace_id = $1 AND name = $2`, dst, squadName); n != 1 {
		t.Fatalf("expected squad imported once, got %d", n)
	}
}

func TestWorkspaceConfigRoundTripAndConflicts(t *testing.T) {
	src, dst := setupConfigWorkspaces(t)
	suf := uuid.NewString()[:6]
	labelName := "builder-" + suf
	skillName := "review-" + suf
	agentName := "Builder-" + suf
	squadName := "Squad-" + suf
	projectTitle := "Proj-" + suf
	apTitle := "Nightly-" + suf
	qaName := "Please review-" + suf
	propName := "Tier-" + suf
	statusKey := "verifying_" + suf

	dbfx.Insert(t, "issue_label", testutil.Cols{
		"workspace_id": src, "resource_type": "agent", "name": labelName, "color": "#3b82f6", "description": "",
	})
	skillID := dbfx.Insert(t, "skill", testutil.Cols{
		"workspace_id": src, "name": skillName, "description": "review", "content": "# Review\n", "created_by": testUserID,
	})
	dbfx.Insert(t, "skill_file", testutil.Cols{
		"skill_id": skillID, "path": "references/a.md", "content": "# A\n",
	})
	agentID := dbfx.Agent(t, agentName, "", testutil.Cols{
		"workspace_id": src, "instructions": "build things", "visibility": "workspace",
	})
	dbfx.InsertNoID(t, "agent_skill", testutil.Cols{"agent_id": agentID, "skill_id": skillID, "enabled": true},
		"agent_id = $1 AND skill_id = $2", agentID, skillID)
	dbfx.Squad(t, squadName, agentID, testutil.Cols{"workspace_id": src})
	dbfx.Project(t, projectTitle, testutil.Cols{"workspace_id": src, "status": "in_progress"})
	dbfx.Insert(t, "autopilot", testutil.Cols{
		"workspace_id": src, "title": apTitle, "assignee_type": "agent", "assignee_id": agentID,
		"status": "active", "execution_mode": "create_issue", "created_by_type": "member", "created_by_id": testUserID,
	})
	dbfx.Insert(t, "quick_action", testutil.Cols{
		"workspace_id": src, "name": qaName, "assignee_type": "agent", "assignee_id": agentID,
		"prompt": "review", "visibility": "public", "created_by_type": "member", "created_by_id": testUserID,
	})
	dbfx.Insert(t, "issue_property", testutil.Cols{
		"workspace_id": src, "name": propName, "type": "select", "description": "", "icon": "layers",
		"config":   testutil.Raw(`'{"options":[{"id":"11111111-1111-1111-1111-111111111111","name":"T3","color":"#ef4444"}]}'::jsonb`),
		"position": 1,
	})
	dbfx.Insert(t, "issue_status", testutil.Cols{
		"workspace_id": src, "key": statusKey, "name": "Verifying", "description": "",
		"category": "started", "color": "#a855f7", "is_system": false, "position": 4.5,
	})

	bundle := exportBundle(t, src)
	if bundle.Stats["agents"] < 1 || bundle.Stats["skills"] < 1 || bundle.Stats["labels"] < 1 {
		t.Fatalf("export stats too small: %+v", bundle.Stats)
	}

	dryTrue := true
	preview := importReport(t, dst, map[string]any{"bundle": bundle, "dry_run": dryTrue, "on_conflict": "skip"}, http.StatusOK)
	if preview.Applied {
		t.Fatal("dry_run report should have applied=false")
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent WHERE workspace_id = $1 AND name = $2`, dst, agentName); n != 0 {
		t.Fatal("dry_run wrote an agent")
	}

	dryFalse := false
	applied := importReport(t, dst, map[string]any{"bundle": bundle, "dry_run": dryFalse, "on_conflict": "skip"}, http.StatusOK)
	if !applied.Applied {
		t.Fatal("apply report should have applied=true")
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent WHERE workspace_id = $1 AND name = $2`, dst, agentName); n != 1 {
		t.Fatalf("expected 1 imported agent, got %d", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM skill WHERE workspace_id = $1 AND name = $2`, dst, skillName); n != 1 {
		t.Fatalf("expected 1 imported skill, got %d", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_label WHERE workspace_id = $1 AND name = $2`, dst, labelName); n != 1 {
		t.Fatalf("expected 1 imported label, got %d", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM squad WHERE workspace_id = $1 AND name = $2`, dst, squadName); n != 1 {
		t.Fatalf("expected 1 imported squad, got %d", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM project WHERE workspace_id = $1 AND title = $2`, dst, projectTitle); n != 1 {
		t.Fatalf("expected 1 imported project, got %d", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM autopilot WHERE workspace_id = $1 AND title = $2`, dst, apTitle); n != 1 {
		t.Fatalf("expected 1 imported autopilot, got %d", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM quick_action WHERE workspace_id = $1 AND name = $2`, dst, qaName); n != 1 {
		t.Fatalf("expected 1 imported quick action, got %d", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_property WHERE workspace_id = $1 AND name = $2`, dst, propName); n != 1 {
		t.Fatalf("expected 1 imported property, got %d", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_status WHERE workspace_id = $1 AND key = $2`, dst, statusKey); n != 1 {
		t.Fatalf("expected 1 imported custom status, got %d", n)
	}

	again := importReport(t, dst, map[string]any{"bundle": bundle, "dry_run": dryFalse, "on_conflict": "skip"}, http.StatusOK)
	if again.Stats.Created != 0 {
		t.Fatalf("second skip import created %d entities", again.Stats.Created)
	}
	if again.Stats.Skipped == 0 {
		t.Fatalf("second skip import expected skipped > 0, stats=%+v", again.Stats)
	}

	rename := importReport(t, dst, map[string]any{"bundle": bundle, "dry_run": dryFalse, "on_conflict": "rename", "include": []string{"agents"}}, http.StatusOK)
	if n := dbfx.Count(t, `SELECT count(*) FROM agent WHERE workspace_id = $1 AND name LIKE $2`, dst, agentName+"%"); n < 2 {
		t.Fatalf("rename should create a copy, agent count=%d report=%+v", n, rename.Stats)
	}
}

// DENE-408: the source's own export carries the 7 platform-seeded built-in
// statuses, and so does every workspace a user can import into — creating one
// seeds them. Read as a conflict, the `issue_statuses` batch failed on its first
// row under `fail`, the CLI's default, and the import stopped before the user
// saw a single preview row. The built-ins are the platform's catalog on both
// sides, not user data.
func TestWorkspaceConfigImport_BuiltinStatusesAreNotConflictsUnderFail(t *testing.T) {
	src, dst := setupConfigWorkspaces(t)
	bundle := exportBundle(t, src)
	if len(bundle.Entities.IssueStatuses) != 7 {
		t.Fatalf("bundle carries %d issue statuses, want the 7 seeded built-ins", len(bundle.Entities.IssueStatuses))
	}
	for _, stt := range bundle.Entities.IssueStatuses {
		if !stt.IsSystem {
			t.Fatalf("exported status %q is not marked is_system, so it is not the built-in this test is about", stt.Key)
		}
	}

	// The preview is the user's first sight of the import, so it must survive
	// the default policy too, not only the apply.
	for _, dryRun := range []bool{true, false} {
		report := importReport(t, dst, map[string]any{
			"bundle": bundle, "dry_run": dryRun, "on_conflict": "fail", "include": []string{"issue_statuses"},
		}, http.StatusOK)
		if report.Stats.Created != 0 || report.Stats.Skipped != 7 {
			t.Fatalf("dry_run=%v stats=%+v, want the 7 built-ins skipped and none created", dryRun, report.Stats)
		}
		for _, batch := range report.Batches {
			for _, item := range batch.Items {
				if item.Action != service.ActionSkipped {
					t.Fatalf("dry_run=%v status %q action=%q, want skipped (reason=%q)", dryRun, item.Name, item.Action, item.Reason)
				}
			}
		}
	}

	if n := dbfx.Count(t, `SELECT count(*) FROM issue_status WHERE workspace_id = $1`, dst); n != 7 {
		t.Fatalf("target holds %d statuses after the import, want its own 7 untouched", n)
	}
}

// The other half of DENE-408: sparing the built-ins must not disarm the `fail`
// guard. A same-name label / agent / skill is real user data on both sides and
// still has to stop the import with the entity named.
func TestWorkspaceConfigImport_UserDataConflictsStillFail(t *testing.T) {
	src, dst := setupConfigWorkspaces(t)
	suf := uuid.NewString()[:8]
	labelName := "conflict-label-" + suf
	skillName := "conflict-skill-" + suf
	agentName := "ConflictAgent-" + suf
	for _, ws := range []string{src, dst} {
		dbfx.Insert(t, "issue_label", testutil.Cols{
			"workspace_id": ws, "resource_type": "agent", "name": labelName, "color": "#3b82f6", "description": "",
		})
		dbfx.Insert(t, "skill", testutil.Cols{
			"workspace_id": ws, "name": skillName, "description": "d", "content": "# c\n", "created_by": testUserID,
		})
		dbfx.Agent(t, agentName, "", testutil.Cols{"workspace_id": ws, "visibility": "workspace"})
	}

	bundle := exportBundle(t, src)
	for _, tt := range []struct{ include, name string }{
		{"labels", labelName},
		{"agents", agentName},
		{"skills", skillName},
	} {
		t.Run(tt.include, func(t *testing.T) {
			resp := testutil.Call(t, testHandler.ImportWorkspaceConfig, configReq("POST", "/api/workspaces/"+dst+"/config/import", dst, map[string]any{
				"bundle": bundle, "dry_run": false, "on_conflict": "fail", "include": []string{tt.include},
			}))
			resp.Want(http.StatusConflict)
			got := resp.Map()
			if got["code"] != "config_import_conflict" {
				t.Fatalf("code=%v body=%s", got["code"], resp.Text())
			}
			if msg, _ := got["error"].(string); !strings.Contains(msg, tt.name) {
				t.Fatalf("error=%q, want the conflicting %s named", msg, tt.include)
			}
		})
	}
}

func TestWorkspaceConfigImport_UnmappedMemberDropped(t *testing.T) {
	src, dst := setupConfigWorkspaces(t)
	outsider := dbfx.User(t, "Outsider", "out-"+uuid.NewString()[:8]+"@example.com")
	dbfx.Member(t, src, outsider, "member")
	agentID := dbfx.Agent(t, "Lead-"+uuid.NewString()[:6], "", testutil.Cols{"workspace_id": src, "visibility": "workspace"})
	squadID := dbfx.Squad(t, "WithOutsider", agentID, testutil.Cols{"workspace_id": src})
	dbfx.SquadMember(t, squadID, "member", outsider, testutil.Cols{"role": "决策人"})

	bundle := exportBundle(t, src)
	dry := false
	report := importReport(t, dst, map[string]any{"bundle": bundle, "dry_run": dry, "on_conflict": "skip", "include": []string{"agents", "squads"}}, http.StatusOK)
	found := false
	for _, u := range report.UnmappedRefs {
		if u.RefType == "member" && u.RefID == outsider {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected unmapped member ref, got %+v", report.UnmappedRefs)
	}
}

func TestWorkspaceConfigImport_InvalidVersion(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	dry := true
	resp := testutil.Call(t, testHandler.ImportWorkspaceConfig, configReq("POST", "/api/workspaces/"+dst+"/config/import", dst, map[string]any{
		"dry_run": dry,
		"bundle": map[string]any{
			"format": service.ConfigBundleFormat, "schema_version": 99,
			"source":   map[string]any{"workspace_id": uuid.NewString()},
			"entities": map[string]any{},
		},
	}))
	resp.Want(http.StatusBadRequest)
	if resp.Map()["code"] != "config_bundle_version_unsupported" {
		t.Fatalf("code=%v", resp.Map()["code"])
	}
}

func TestWorkspaceConfigExport_InvalidInclude(t *testing.T) {
	src, _ := setupConfigWorkspaces(t)
	req := configReq("GET", "/api/workspaces/"+src+"/config/export?include=not_a_type", src, nil)
	testutil.Call(t, testHandler.ExportWorkspaceConfig, req).Want(http.StatusBadRequest)
}

func TestWorkspaceConfigExport_AgentActorForbidden(t *testing.T) {
	src, _ := setupConfigWorkspaces(t)
	agentID := dbfx.Agent(t, "Actor-"+uuid.NewString()[:6], "", testutil.Cols{"workspace_id": src})
	req := configReq("GET", "/api/workspaces/"+src+"/config/export", src, nil)
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Actor-Source", "task_token")
	testutil.Call(t, testHandler.ExportWorkspaceConfig, req).Want(http.StatusForbidden)
}

// Guards the single-apply lock: while another session holds the workspace's
// import lock, apply is rejected before any batch runs. The lock is held for
// the whole apply, not per batch, so a second apply cannot interleave.
func TestWorkspaceConfigImport_ConcurrentApplyRejected(t *testing.T) {
	src, dst := setupConfigWorkspaces(t)
	bundle := exportBundle(t, src)
	ctx := context.Background()
	holder, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock holder: %v", err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	var locked bool
	if err := holder.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('config_import:' || $1::text, 0))`, dst).Scan(&locked); err != nil || !locked {
		t.Fatalf("hold import lock: locked=%v err=%v", locked, err)
	}
	resp := testutil.Call(t, testHandler.ImportWorkspaceConfig, configReq("POST", "/api/workspaces/"+dst+"/config/import", dst, map[string]any{
		"bundle": bundle, "dry_run": false, "on_conflict": "skip",
	}))
	resp.Want(http.StatusConflict)
	if resp.Map()["code"] != "config_import_in_progress" {
		t.Fatalf("code=%v body=%s", resp.Map()["code"], resp.Text())
	}
}

func TestWorkspaceConfigImport_SameWorkspaceRejected(t *testing.T) {
	src, _ := setupConfigWorkspaces(t)
	bundle := exportBundle(t, src)
	dry := true
	resp := testutil.Call(t, testHandler.ImportWorkspaceConfig, configReq("POST", "/api/workspaces/"+src+"/config/import", src, map[string]any{
		"bundle": bundle, "dry_run": dry, "on_conflict": "skip",
	}))
	resp.Want(http.StatusBadRequest)
	if resp.Map()["code"] != "config_import_same_workspace" {
		t.Fatalf("code=%v body=%s", resp.Map()["code"], resp.Text())
	}
}

// Regression: secrets passed as custom_args values ("--api-key VALUE") were
// exported in plaintext, and overwrite replaced the target's args wholesale.
func TestWorkspaceConfigImport_CustomArgsSecretsMaskedAndPreserved(t *testing.T) {
	src, dst := setupConfigWorkspaces(t)
	name := "Args-" + uuid.NewString()[:8]
	canaryArg := "CANARY_ARG_" + uuid.NewString()[:8]
	dbfx.Agent(t, name, "", testutil.Cols{
		"workspace_id": src,
		"custom_args":  testutil.Raw(fmt.Sprintf(`'["--api-key","%s","--model","opus"]'::jsonb`, canaryArg)),
		"visibility":   "workspace",
	})
	dstAgent := dbfx.Agent(t, name, "", testutil.Cols{
		"workspace_id": dst,
		"custom_args":  testutil.Raw(`'["--api-key","DST_KEY"]'::jsonb`),
		"visibility":   "workspace",
	})

	bundle := exportBundle(t, src)
	raw, _ := json.Marshal(bundle)
	if strings.Contains(string(raw), canaryArg) {
		t.Fatalf("custom_args secret leaked into the bundle")
	}
	omitted := false
	for _, s := range bundle.SecretsOmitted {
		if s.Entity == "agent" && s.Field == "custom_args" && s.Name == name {
			omitted = true
		}
	}
	if !omitted {
		t.Fatalf("expected a custom_args secrets_omitted entry, got %+v", bundle.SecretsOmitted)
	}

	_ = importReport(t, dst, map[string]any{
		"bundle": bundle, "dry_run": false, "on_conflict": "overwrite", "include": []string{"agents"},
	}, http.StatusOK)
	var args string
	dbfx.QueryRow(t, `SELECT custom_args::text FROM agent WHERE id = $1`, dstAgent).Scan(&args)
	if args != `["--api-key", "DST_KEY", "--model", "opus"]` {
		t.Fatalf("overwrite custom_args = %s, want target key kept and model imported", args)
	}
}

// Regression: kept webhook secrets were keyed by label only, so two unlabeled
// webhook triggers reused one token and broke the unique index on overwrite.
func TestWorkspaceConfigImport_OverwriteUnlabeledWebhooks(t *testing.T) {
	src, dst := setupConfigWorkspaces(t)
	suf := uuid.NewString()[:8]
	agentName := "Hooks-" + suf
	apTitle := "TwoHooks-" + suf
	dstTokens := map[string]bool{}
	for _, ws := range []string{src, dst} {
		agent := dbfx.Agent(t, agentName, "", testutil.Cols{"workspace_id": ws, "visibility": "workspace"})
		apID := dbfx.Insert(t, "autopilot", testutil.Cols{
			"workspace_id": ws, "title": apTitle, "assignee_type": "agent", "assignee_id": agent,
			"status": "paused", "execution_mode": "create_issue", "created_by_type": "member", "created_by_id": testUserID,
		})
		for i := 0; i < 2; i++ {
			token := "awt_" + uuid.NewString()
			if ws == dst {
				dstTokens[token] = true
			}
			dbfx.Insert(t, "autopilot_trigger", testutil.Cols{
				"autopilot_id": apID, "kind": "webhook", "enabled": true,
				"webhook_token": token, "created_by_type": "member", "created_by_id": testUserID,
			})
		}
	}

	bundle := exportBundle(t, src)
	_ = importReport(t, dst, map[string]any{
		"bundle": bundle, "dry_run": false, "on_conflict": "overwrite", "include": []string{"agents", "autopilots"},
	}, http.StatusOK)

	rows := dbfx.Count(t, `SELECT count(DISTINCT tr.webhook_token) FROM autopilot_trigger tr JOIN autopilot a ON a.id = tr.autopilot_id
		WHERE a.workspace_id = $1 AND a.title = $2 AND tr.kind = 'webhook'`, dst, apTitle)
	if rows != 2 {
		t.Fatalf("expected 2 distinct webhook tokens after overwrite, got %d", rows)
	}
	for token := range dstTokens {
		if n := dbfx.Count(t, `SELECT count(*) FROM autopilot_trigger WHERE webhook_token = $1`, token); n != 1 {
			t.Fatalf("overwrite did not keep target webhook token %s", token)
		}
	}
}

// Regression: squad actor filters in saved views kept the source squad UUID,
// and member filters were dropped unless another entity had cached the member.
func TestWorkspaceConfigImport_ViewActorFiltersRemapped(t *testing.T) {
	src, dst := setupConfigWorkspaces(t)
	suf := uuid.NewString()[:8]
	leader := dbfx.Agent(t, "ViewLead-"+suf, "", testutil.Cols{"workspace_id": src, "visibility": "workspace"})
	squadName := "ViewSquad-" + suf
	srcSquad := dbfx.Squad(t, squadName, leader, testutil.Cols{"workspace_id": src})
	viewName := "BySquad-" + suf
	dbfx.Insert(t, "issue_view", testutil.Cols{
		"workspace_id": src, "owner_id": testUserID, "name": viewName, "scope_type": "workspace",
		"visibility": "workspace",
		"query":      testutil.Raw(fmt.Sprintf(`'{"assigneeFilters":[{"type":"squad","id":"%s"},{"type":"member","id":"%s"}]}'::jsonb`, srcSquad, testUserID)),
	})

	bundle := exportBundle(t, src)
	_ = importReport(t, dst, map[string]any{
		"bundle": bundle, "dry_run": false, "on_conflict": "skip", "include": []string{"agents", "squads", "issue_views"},
	}, http.StatusOK)

	var dstSquad, query string
	dbfx.QueryRow(t, `SELECT id::text FROM squad WHERE workspace_id = $1 AND name = $2`, dst, squadName).Scan(&dstSquad)
	dbfx.QueryRow(t, `SELECT query::text FROM issue_view WHERE workspace_id = $1 AND name = $2`, dst, viewName).Scan(&query)
	if strings.Contains(query, srcSquad) {
		t.Fatalf("view kept the source squad id: %s", query)
	}
	if !strings.Contains(query, dstSquad) || !strings.Contains(query, testUserID) {
		t.Fatalf("view filters not remapped: %s (want squad %s and member %s)", query, dstSquad, testUserID)
	}
}
