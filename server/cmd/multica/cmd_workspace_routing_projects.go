package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/routing"
)

// The project -> direction table is workspace data: it lives in
// workspace.settings.routing.projects, laid over the defaults shipped in
// ladder.json. These commands are the way to edit a row without a release.

var workspaceRoutingProjectsCmd = &cobra.Command{
	Use:   "routing-projects",
	Short: "Manage the project -> direction table used by automatic routing",
	Long: `Automatic routing picks a direction-specialised seat (孙悟空游戏, 布尔玛出海, …)
from the project an issue belongs to. This table says which project is which
direction. It is workspace data: a change applies to the next routed issue,
with no release and no restart.

A row maps a project name — exact, or a prefix ending in * (game-*) — to one
of the ladder's directions, or to 通用 for a project that is deliberately
general-purpose. Matching ignores case; an exact row beats a prefix row, and
the longest prefix wins. A project with no row routes to the generic seats and
the routing comment says the project is not in the table.

Examples:
  multica workspace routing-projects list
  multica workspace routing-projects set tarot 出海
  multica workspace routing-projects set "game-*" 游戏
  multica workspace routing-projects set "Multica 魔改" 通用
  multica workspace routing-projects unset tarot`,
}

var workspaceRoutingProjectsListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show the effective table: this workspace's rows over the shipped defaults",
	Args:  cobra.NoArgs,
	RunE:  runWorkspaceRoutingProjectsList,
}

var workspaceRoutingProjectsSetCmd = &cobra.Command{
	Use:   "set <project> <direction>",
	Short: "Add or change one row (admin/owner only)",
	Args:  cobra.ExactArgs(2),
	RunE:  runWorkspaceRoutingProjectsSet,
}

var workspaceRoutingProjectsUnsetCmd = &cobra.Command{
	Use:   "unset <project>",
	Short: "Remove one of this workspace's rows (admin/owner only)",
	Args:  cobra.ExactArgs(1),
	RunE:  runWorkspaceRoutingProjectsUnset,
}

func init() {
	workspaceRoutingProjectsListCmd.Flags().String("output", "table", "Output format: table or json")
	workspaceRoutingProjectsCmd.AddCommand(workspaceRoutingProjectsListCmd)
	workspaceRoutingProjectsCmd.AddCommand(workspaceRoutingProjectsSetCmd)
	workspaceRoutingProjectsCmd.AddCommand(workspaceRoutingProjectsUnsetCmd)
	workspaceCmd.AddCommand(workspaceRoutingProjectsCmd)
}

// routingProjectRows reads this workspace's own rows out of a settings payload.
func routingProjectRows(settings map[string]any) map[string]string {
	rows := map[string]string{}
	block, _ := settings[routing.SettingsKey].(map[string]any)
	raw, _ := block["projects"].(map[string]any)
	for k, v := range raw {
		if s, ok := v.(string); ok {
			rows[k] = s
		}
	}
	return rows
}

// routingProjectKey is how a project name is compared, here and in the
// router: trimmed and case-folded. Two rows that fold to the same key are the
// same row, so the table must never hold both — the router keeps one of them
// at random, and the direction would flip between two routed issues.
func routingProjectKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// findRoutingProjectRow returns the stored spelling of the row this project
// name addresses, whatever case it was typed in.
func findRoutingProjectRow(rows map[string]string, project string) (string, bool) {
	key := routingProjectKey(project)
	for stored := range rows {
		if routingProjectKey(stored) == key {
			return stored, true
		}
	}
	return "", false
}

// validRoutingDirection accepts a declared direction or the generic marker.
func validRoutingDirection(direction string) bool {
	if direction == routing.GenericDirection {
		return true
	}
	for _, d := range routing.DefaultLadder.Directions {
		if d == direction {
			return true
		}
	}
	return false
}

func loadWorkspaceSettings(ctx context.Context, client *cli.APIClient, wsID string) (map[string]any, error) {
	var ws map[string]any
	if err := client.GetJSON(ctx, "/api/workspaces/"+wsID, &ws); err != nil {
		return nil, fmt.Errorf("get workspace: %w", err)
	}
	settings, _ := ws["settings"].(map[string]any)
	if settings == nil {
		settings = map[string]any{}
	}
	return settings, nil
}

// writeRoutingProjectRows replaces the rows and leaves everything else in the
// settings column as it was read. The routing key is never in that read — the
// server strips it — and a write that omits `api_key` carries the stored key
// forward, so this cannot clear it.
func writeRoutingProjectRows(ctx context.Context, client *cli.APIClient, wsID string, settings map[string]any, rows map[string]string) error {
	block, _ := settings[routing.SettingsKey].(map[string]any)
	next := make(map[string]any, len(block)+1)
	for k, v := range block {
		next[k] = v
	}
	if len(rows) == 0 {
		delete(next, "projects")
	} else {
		projects := make(map[string]any, len(rows))
		for k, v := range rows {
			projects[k] = v
		}
		next["projects"] = projects
	}
	out := make(map[string]any, len(settings)+1)
	for k, v := range settings {
		out[k] = v
	}
	out[routing.SettingsKey] = next

	var ws map[string]any
	if err := client.PatchJSON(ctx, "/api/workspaces/"+wsID, map[string]any{"settings": out}, &ws); err != nil {
		return fmt.Errorf("update workspace: %w", err)
	}
	return nil
}

func routingProjectsSession(cmd *cobra.Command) (*cli.APIClient, string, error) {
	wsID := resolveWorkspaceID(cmd)
	if wsID == "" {
		return nil, "", fmt.Errorf("workspace ID is required: set --workspace-id or MULTICA_WORKSPACE_ID")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, "", err
	}
	return client, wsID, nil
}

func runWorkspaceRoutingProjectsList(cmd *cobra.Command, _ []string) error {
	client, wsID, err := routingProjectsSession(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	settings, err := loadWorkspaceSettings(ctx, client, wsID)
	if err != nil {
		return err
	}
	own := routingProjectRows(settings)

	type row struct {
		Project   string `json:"project"`
		Direction string `json:"direction"`
		Source    string `json:"source"`
	}
	rows := make([]row, 0, len(own)+len(routing.DefaultLadder.Projects))
	seen := map[string]bool{}
	for k, v := range own {
		seen[routingProjectKey(k)] = true
		rows = append(rows, row{Project: k, Direction: v, Source: "workspace"})
	}
	for k, v := range routing.DefaultLadder.Projects {
		if !seen[routingProjectKey(k)] {
			rows = append(rows, row{Project: k, Direction: v, Source: "default"})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Project < rows[j].Project })

	if format, _ := cmd.Flags().GetString("output"); format == "json" {
		return cli.PrintJSON(os.Stdout, map[string]any{
			"directions": append([]string{routing.GenericDirection}, routing.DefaultLadder.Directions...),
			"projects":   rows,
		})
	}
	for _, r := range rows {
		fmt.Printf("%-28s %-8s %s\n", r.Project, r.Direction, r.Source)
	}
	return nil
}

func runWorkspaceRoutingProjectsSet(cmd *cobra.Command, args []string) error {
	project, direction := strings.TrimSpace(args[0]), strings.TrimSpace(args[1])
	if project == "" {
		return fmt.Errorf("project name cannot be empty")
	}
	if !validRoutingDirection(direction) {
		return fmt.Errorf("unknown direction %q; use one of: %s",
			direction, strings.Join(append([]string{routing.GenericDirection}, routing.DefaultLadder.Directions...), ", "))
	}
	client, wsID, err := routingProjectsSession(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	settings, err := loadWorkspaceSettings(ctx, client, wsID)
	if err != nil {
		return err
	}
	rows := routingProjectRows(settings)
	// Re-typing an existing project in another case replaces that row rather
	// than adding a second one the router would pick between at random.
	if stored, ok := findRoutingProjectRow(rows, project); ok {
		delete(rows, stored)
	}
	rows[project] = direction
	if err := writeRoutingProjectRows(ctx, client, wsID, settings, rows); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "project %q -> %s\n", project, direction)
	return nil
}

func runWorkspaceRoutingProjectsUnset(cmd *cobra.Command, args []string) error {
	project := strings.TrimSpace(args[0])
	client, wsID, err := routingProjectsSession(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	settings, err := loadWorkspaceSettings(ctx, client, wsID)
	if err != nil {
		return err
	}
	rows := routingProjectRows(settings)
	stored, ok := findRoutingProjectRow(rows, project)
	if !ok {
		return fmt.Errorf("this workspace has no row for %q (shipped defaults cannot be removed; override one with `set`)", project)
	}
	delete(rows, stored)
	if err := writeRoutingProjectRows(ctx, client, wsID, settings, rows); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "removed row %q\n", project)
	return nil
}
