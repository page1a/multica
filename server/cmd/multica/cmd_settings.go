package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

// settingsCmd is the long-tail escape hatch. A key maps to one existing
// server route; the method is part of that mapping, not a second guess
// from the key prefix. The server still authorises and validates.
var settingsCmd = &cobra.Command{
	Use:   "settings",
	Short: "Read and change long-tail workspace settings",
	Long: `Maps a stable key onto an existing server route and prints JSON.

High-frequency objects keep their own commands. This command does not
decide permissions; the server does.

  multica settings get repo.visibility --value-json '{"url":"https://github.com/acme/app.git"}'
  multica settings set repo.visibility --value-json '{"url":"https://github.com/acme/app.git","visibility":"workspace"}'
  multica settings get repo.shares --value-json '{"url":"https://github.com/acme/app.git"}'
  multica settings set repo.shares --value-json '{"url":"https://github.com/acme/app.git","member_id":"..."}'
  multica settings set repo.shares --value-json '{"url":"https://github.com/acme/app.git","member_id":"...","revoke":true}'
  multica settings get agent.spawn
  multica settings set agent.spawn --value-json '{"chat_chat":{"enabled":true,"per_chat":5,"per_run":3}}'

Keys, methods, and which page each one belongs to: docs/kun/settings-cli-coverage.md`,
}

var settingsGetCmd = &cobra.Command{
	Use:   "get <key>",
	Short: "Read a setting",
	Args:  exactArgs(1),
	RunE:  runSettingsGet,
}

var settingsSetCmd = &cobra.Command{
	Use:   "set <key>",
	Short: "Change a setting",
	Args:  exactArgs(1),
	RunE:  runSettingsSet,
}

func init() {
	settingsGetCmd.Flags().String("output", "json", "Output format: json")
	settingsGetCmd.Flags().String("value-json", "", "JSON object a read needs, such as {\"url\":\"...\"} for a repository")
	settingsSetCmd.Flags().String("value-json", "", "JSON value (use --value-file or --value-stdin for secrets)")
	settingsSetCmd.Flags().String("value-file", "", "Read the JSON value from a file")
	settingsSetCmd.Flags().Bool("value-stdin", false, "Read the JSON value from stdin")
	settingsSetCmd.Flags().String("output", "json", "Output format: json")
	settingsCmd.AddCommand(settingsGetCmd, settingsSetCmd)
}

// settingsCall is one request the key table already decided: path and method
// together. Project names how a read reduces a larger existing payload when
// the write route has no GET of its own. Hint is the id or url that
// projection looks for. Ack is what to print when the server accepts the
// write and returns no JSON body.
type settingsCall struct {
	Method  string
	Path    string
	Body    any
	Project string
	Hint    any
	Ack     any
}

type settingsKind struct {
	handlers []string
	read     func(id string, query map[string]any, workspaceID string) (settingsCall, error)
	write    func(id string, value any) (settingsCall, error)
}

// settingsKinds is the only method table. Adding a server route under the
// settings surface without a handler listed here fails
// TestSettingsSurfaceRoutesAreClaimed.
var settingsKinds = map[string]settingsKind{
	"modules.visibility": {
		handlers: []string{"ListModuleVisibility"},
		read: func(string, map[string]any, string) (settingsCall, error) {
			return settingsCall{Method: http.MethodGet, Path: "/api/modules"}, nil
		},
	},
	"module.visibility": {
		handlers: []string{"SetModuleVisibility"},
		read: func(id string, _ map[string]any, _ string) (settingsCall, error) {
			return settingsCall{Method: http.MethodGet, Path: "/api/modules", Project: "module", Hint: id}, nil
		},
		write: func(id string, value any) (settingsCall, error) {
			return settingsCall{Method: http.MethodPut, Path: "/api/modules/" + url.PathEscape(id) + "/visibility", Body: value}, nil
		},
	},
	"repo.visibility": {
		handlers: []string{"SetRepoVisibility"},
		read: func(_ string, query map[string]any, workspaceID string) (settingsCall, error) {
			if workspaceID == "" {
				return settingsCall{}, fmt.Errorf("workspace id is required to read repo.visibility")
			}
			return settingsCall{
				Method:  http.MethodGet,
				Path:    "/api/workspaces/" + url.PathEscape(workspaceID),
				Project: "repo-visibility",
				Hint:    queryString(query, "url"),
			}, nil
		},
		write: func(_ string, value any) (settingsCall, error) {
			return settingsCall{Method: http.MethodPut, Path: "/api/repos/visibility", Body: value}, nil
		},
	},
	"repo.shares": {
		handlers: []string{"ListRepoShares", "AddRepoShare", "RemoveRepoShare"},
		read: func(_ string, query map[string]any, _ string) (settingsCall, error) {
			repoURL := queryString(query, "url")
			if repoURL == "" {
				return settingsCall{}, fmt.Errorf("repo.shares requires url")
			}
			return settingsCall{
				Method: http.MethodGet,
				Path:   "/api/repos/shares?url=" + url.QueryEscape(repoURL),
			}, nil
		},
		write: planRepoSharesWrite,
	},
	"runtime.visibility": {
		handlers: []string{"ListAgentRuntimes", "UpdateAgentRuntime"},
		read: func(id string, _ map[string]any, _ string) (settingsCall, error) {
			return settingsCall{Method: http.MethodGet, Path: "/api/runtimes", Project: "runtime-visibility", Hint: id}, nil
		},
		write: func(id string, value any) (settingsCall, error) {
			return settingsCall{Method: http.MethodPatch, Path: "/api/runtimes/" + url.PathEscape(id), Body: value}, nil
		},
	},
	"agent.access-passes": {
		handlers: []string{"ListAgentAccessPasses", "CreateAgentAccessPass", "RevokeAgentAccessPass"},
		read: func(id string, _ map[string]any, _ string) (settingsCall, error) {
			return settingsCall{Method: http.MethodGet, Path: "/api/agents/" + url.PathEscape(id) + "/access-passes"}, nil
		},
		write: planAccessPassWrite,
	},
	"agent.spawn": {
		handlers: []string{"GetWorkspaceAgentSpawn", "UpdateWorkspaceAgentSpawn"},
		read: func(_ string, _ map[string]any, workspaceID string) (settingsCall, error) {
			return settingsCall{Method: http.MethodGet, Path: "/api/workspaces/" + url.PathEscape(workspaceID) + "/agent-spawn"}, nil
		},
		// id is the workspace: planSettings fills it in for workspace-wide keys.
		write: func(workspaceID string, value any) (settingsCall, error) {
			return settingsCall{Method: http.MethodPut, Path: "/api/workspaces/" + url.PathEscape(workspaceID) + "/agent-spawn", Body: value}, nil
		},
	},
	"agent.runtime-skill": {
		handlers: []string{"GetAgent", "SetAgentRuntimeSkillEnabled"},
		read: func(id string, _ map[string]any, _ string) (settingsCall, error) {
			return settingsCall{Method: http.MethodGet, Path: "/api/agents/" + url.PathEscape(id), Project: "runtime-skill", Hint: id}, nil
		},
		write: func(id string, value any) (settingsCall, error) {
			return settingsCall{
				Method: http.MethodPut,
				Path:   "/api/agents/" + url.PathEscape(id) + "/runtime-skills/enabled",
				Body:   value,
				Ack:    map[string]any{"ok": true},
			}, nil
		},
	},
}

func planRepoSharesWrite(_ string, value any) (settingsCall, error) {
	body, _ := value.(map[string]any)
	if body != nil && boolField(body, "revoke") {
		repoURL := queryString(body, "url")
		memberID := queryString(body, "member_id")
		if repoURL == "" || memberID == "" {
			return settingsCall{}, fmt.Errorf("repo.shares revoke requires url and member_id")
		}
		q := url.Values{}
		q.Set("url", repoURL)
		q.Set("member_id", memberID)
		return settingsCall{
			Method: http.MethodDelete,
			Path:   "/api/repos/shares?" + q.Encode(),
			Ack:    map[string]any{"url": repoURL, "member_id": memberID, "revoked": true},
		}, nil
	}
	return settingsCall{Method: http.MethodPost, Path: "/api/repos/shares", Body: value}, nil
}

func planAccessPassWrite(id string, value any) (settingsCall, error) {
	path := "/api/agents/" + url.PathEscape(id) + "/access-passes"
	if body, ok := value.(map[string]any); ok && body["revoke_id"] != nil {
		revokeID := strings.TrimSpace(fmt.Sprint(body["revoke_id"]))
		if revokeID == "" || revokeID == "<nil>" {
			return settingsCall{}, fmt.Errorf("access-passes revoke_id is empty")
		}
		return settingsCall{Method: http.MethodDelete, Path: path + "/" + url.PathEscape(revokeID)}, nil
	}
	return settingsCall{Method: http.MethodPost, Path: path, Body: value}, nil
}

func settingsHandlerClaims() map[string]struct{} {
	out := make(map[string]struct{})
	for _, kind := range settingsKinds {
		for _, handler := range kind.handlers {
			out[handler] = struct{}{}
		}
	}
	return out
}

func parseSettingsKey(key string) (kind, id string, err error) {
	switch key {
	case "modules.visibility", "repo.visibility", "repo.shares", "agent.spawn":
		return key, "", nil
	}
	parts := strings.Split(key, ".")
	if len(parts) != 3 || parts[1] == "" {
		return "", "", unknownSettingsKey(key)
	}
	switch {
	case parts[0] == "module" && parts[2] == "visibility":
		return "module.visibility", parts[1], nil
	case parts[0] == "runtime" && parts[2] == "visibility":
		return "runtime.visibility", parts[1], nil
	case parts[0] == "agent" && parts[2] == "access-passes":
		return "agent.access-passes", parts[1], nil
	case parts[0] == "agent" && parts[2] == "runtime-skill":
		return "agent.runtime-skill", parts[1], nil
	default:
		return "", "", unknownSettingsKey(key)
	}
}

func unknownSettingsKey(key string) error {
	return fmt.Errorf("unknown settings key %q; run 'multica settings --help'", key)
}

func planSettings(key string, write bool, value any, query map[string]any, workspaceID string) (settingsCall, error) {
	kindName, id, err := parseSettingsKey(key)
	if err != nil {
		return settingsCall{}, err
	}
	kind, ok := settingsKinds[kindName]
	if !ok {
		return settingsCall{}, unknownSettingsKey(key)
	}
	if write {
		if kind.write == nil {
			return settingsCall{}, fmt.Errorf("setting %s is read-only; set a more specific key", key)
		}
		if kindName == "agent.spawn" {
			if workspaceID == "" {
				return settingsCall{}, fmt.Errorf("workspace id is required to set agent.spawn")
			}
			id = workspaceID
		}
		return kind.write(id, value)
	}
	if kind.read == nil {
		return settingsCall{}, fmt.Errorf("setting %s cannot be read", key)
	}
	return kind.read(id, query, workspaceID)
}

func settingsClient(cmd *cobra.Command) (*cli.APIClient, error) {
	if _, err := requireWorkspaceID(cmd); err != nil {
		return nil, err
	}
	return newAPIClient(cmd)
}

func rejectSettingsTable(cmd *cobra.Command) error {
	if cmd.Flags().Lookup("output") == nil {
		return nil
	}
	format, _ := cmd.Flags().GetString("output")
	if format == "" || format == "json" {
		return nil
	}
	return fmt.Errorf("settings only writes JSON; --output %s is not supported", format)
}

func runSettingsGet(cmd *cobra.Command, args []string) error {
	if err := rejectSettingsTable(cmd); err != nil {
		return err
	}
	client, err := settingsClient(cmd)
	if err != nil {
		return err
	}
	query, err := readOptionalObject(cmd)
	if err != nil {
		return err
	}
	call, err := planSettings(args[0], false, nil, query, client.WorkspaceID)
	if err != nil {
		return err
	}
	return executeSettings(cmd.Context(), client, args[0], call)
}

func runSettingsSet(cmd *cobra.Command, args []string) error {
	if err := rejectSettingsTable(cmd); err != nil {
		return err
	}
	client, err := settingsClient(cmd)
	if err != nil {
		return err
	}
	value, err := readSettingValue(cmd)
	if err != nil {
		return err
	}
	call, err := planSettings(args[0], true, value, nil, client.WorkspaceID)
	if err != nil {
		return err
	}
	return executeSettings(cmd.Context(), client, args[0], call)
}

func executeSettings(ctx context.Context, client *cli.APIClient, key string, call settingsCall) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := cli.APIContext(ctx)
	defer cancel()

	var raw any
	var err error
	switch call.Method {
	case http.MethodGet:
		err = client.GetJSON(ctx, call.Path, &raw)
	case http.MethodPost:
		err = client.PostJSON(ctx, call.Path, call.Body, &raw)
	case http.MethodPut:
		err = client.PutJSON(ctx, call.Path, call.Body, &raw)
	case http.MethodPatch:
		err = client.PatchJSON(ctx, call.Path, call.Body, &raw)
	case http.MethodDelete:
		err = client.DeleteJSON(ctx, call.Path)
	default:
		return fmt.Errorf("setting %s has no method", key)
	}
	if err != nil {
		if errors.Is(err, io.EOF) {
			raw = nil
		} else {
			return fmt.Errorf("setting %s: %w", key, err)
		}
	}
	if call.Project != "" {
		raw, err = projectSettings(call.Project, raw, call.Hint)
		if err != nil {
			return err
		}
	} else if raw == nil {
		raw = call.Ack
	}
	if raw == nil {
		return nil
	}
	return cli.PrintJSON(os.Stdout, raw)
}

func projectSettings(project string, raw any, hint any) (any, error) {
	switch project {
	case "repo-visibility":
		return projectRepoVisibility(raw, hint)
	case "runtime-visibility":
		return projectRuntimeVisibility(raw, hint)
	case "runtime-skill":
		return projectRuntimeSkill(raw, hint)
	case "module":
		return projectModule(raw, hint)
	default:
		return nil, fmt.Errorf("unknown settings projection %q", project)
	}
}

func projectRepoVisibility(raw any, hint any) (any, error) {
	body, _ := raw.(map[string]any)
	if body == nil {
		return nil, fmt.Errorf("workspace response has no repositories")
	}
	repos := objectList(body["repos"])
	wantURL, _ := hint.(string)
	if wantURL == "" {
		out := make([]map[string]any, 0, len(repos))
		for _, repo := range repos {
			out = append(out, map[string]any{"url": repo["url"], "visibility": repo["visibility"]})
		}
		return out, nil
	}
	for _, repo := range repos {
		if queryString(repo, "url") == wantURL {
			return map[string]any{"url": repo["url"], "visibility": repo["visibility"]}, nil
		}
	}
	return nil, fmt.Errorf("repository not found in workspace")
}

func projectRuntimeVisibility(raw any, hint any) (any, error) {
	wantID, _ := hint.(string)
	for _, runtime := range objectList(raw) {
		if queryString(runtime, "id") == wantID {
			return map[string]any{"id": runtime["id"], "visibility": runtime["visibility"]}, nil
		}
	}
	return nil, fmt.Errorf("runtime not found")
}

func projectRuntimeSkill(raw any, hint any) (any, error) {
	body, _ := raw.(map[string]any)
	if body == nil {
		return nil, fmt.Errorf("agent response has no runtime skill switches")
	}
	skills := body["disabled_runtime_skills"]
	if skills == nil {
		skills = []any{}
	}
	agentID, _ := hint.(string)
	return map[string]any{"agent_id": agentID, "disabled_runtime_skills": skills}, nil
}

func projectModule(raw any, hint any) (any, error) {
	body, _ := raw.(map[string]any)
	if body == nil {
		return nil, fmt.Errorf("module list is missing")
	}
	want, _ := hint.(string)
	for _, module := range objectList(body["modules"]) {
		if queryString(module, "key") == want {
			return module, nil
		}
	}
	return nil, fmt.Errorf("module not found")
}

func objectList(v any) []map[string]any {
	items, _ := v.([]any)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if row, ok := item.(map[string]any); ok {
			out = append(out, row)
		}
	}
	return out
}

func queryString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}

func boolField(m map[string]any, key string) bool {
	v, ok := m[key].(bool)
	return ok && v
}

func readOptionalObject(cmd *cobra.Command) (map[string]any, error) {
	if flagString(cmd, "value-json") == "" && flagString(cmd, "value-file") == "" && !settingsFlagBool(cmd, "value-stdin") {
		return nil, nil
	}
	value, err := readSettingValue(cmd)
	if err != nil {
		return nil, err
	}
	body, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("value must be a JSON object")
	}
	return body, nil
}

func readSettingValue(cmd *cobra.Command) (any, error) {
	jsonValue := flagString(cmd, "value-json")
	file := flagString(cmd, "value-file")
	stdin := settingsFlagBool(cmd, "value-stdin")
	if boolToInt(jsonValue != "")+boolToInt(file != "")+boolToInt(stdin) != 1 {
		return nil, fmt.Errorf("provide exactly one of --value-json, --value-file, or --value-stdin")
	}
	var data []byte
	var err error
	if file != "" {
		data, err = os.ReadFile(file)
	} else if stdin {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data = []byte(jsonValue)
	}
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("value must be valid JSON: %w", err)
	}
	return value, nil
}

func settingsFlagBool(cmd *cobra.Command, name string) bool {
	if cmd.Flags().Lookup(name) == nil {
		return false
	}
	v, _ := cmd.Flags().GetBool(name)
	return v
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
