package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

var projectBoardCmd = &cobra.Command{
	Use:   "board [<project>...]",
	Short: "Read the open task board for projects or the whole workspace",
	Long: "Read every open ticket visible to you, grouped as waiting, stalled, running, todo, or stale.\n\n" +
		"Pass one or more project ids, id prefixes, or exact names to narrow the board.\n" +
		"With no project arguments the board covers the whole workspace. This is read-only.",
	Args: cobra.MaximumNArgs(32),
	RunE: runProjectBoard,
}

func init() {
	projectBoardCmd.Flags().String("output", "table", "Output format: table or json")
	projectCmd.AddCommand(projectBoardCmd)
}

func runProjectBoard(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	params := url.Values{}
	ids := make([]string, 0, len(args))
	for _, arg := range args {
		// Accept "a,b" as well as "a b": prompts and agents write both.
		for _, ref := range strings.Split(arg, ",") {
			ref = strings.TrimSpace(ref)
			if ref == "" {
				continue
			}
			resolved, err := resolveProjectID(ctx, client, ref)
			if err != nil {
				return fmt.Errorf("resolve project %q: %w", ref, err)
			}
			ids = append(ids, resolved.ID)
		}
	}
	if len(ids) > 0 {
		params.Set("project_ids", strings.Join(ids, ","))
	}
	path := "/api/projects/board"
	if query := params.Encode(); query != "" {
		path += "?" + query
	}
	var board map[string]any
	if err := client.GetJSON(ctx, path, &board); err != nil {
		return fmt.Errorf("project board: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, board)
	}
	printProjectBoard(os.Stdout, board)
	return nil
}

func printProjectBoard(w interface{ Write([]byte) (int, error) }, board map[string]any) {
	lanes := []struct{ key, title string }{
		{"waiting", "Waiting"}, {"stalled", "Stalled"}, {"running", "Running"}, {"todo", "Todo"}, {"stale", "Stale"},
	}
	for _, lane := range lanes {
		rows, _ := board[lane.key].([]any)
		fmt.Fprintf(w, "\n%s (%d)\n", lane.title, len(rows))
		if len(rows) == 0 {
			fmt.Fprintln(w, "  —")
			continue
		}
		for _, raw := range rows {
			row, _ := raw.(map[string]any)
			fmt.Fprintf(w, "  %s  %s\n", strVal(row, "identifier"), strVal(row, "title"))
			if reason := strVal(row, "stalled_reason"); reason != "" {
				fmt.Fprintf(w, "    reason: %s\n", reason)
			}
			if at := strVal(row, "last_activity_at"); at != "" {
				fmt.Fprintf(w, "    last activity: %s\n", at)
			}
			if next := strVal(row, "next_name"); next != "" {
				fmt.Fprintf(w, "    next: %s\n", next)
			}
			if paused, _ := row["paused"].(bool); paused {
				fmt.Fprintf(w, "    paused: %s\n", strVal(row, "pause_reason"))
			}
		}
	}
}
