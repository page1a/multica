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

func registerIssueDisposeFlags(cmd *cobra.Command) {
	cmd.Flags().String("action", "", "rerun, reroute, split, or cancel (required)")
	cmd.Flags().String("reason", "", "Why; required with --action cancel")
	cmd.Flags().StringArray("into", nil, "Sub-issue title for --action split (repeatable)")
	cmd.Flags().String("output", "table", "Output format: table or json")
}

func runIssueDispose(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	ref, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}
	action, _ := cmd.Flags().GetString("action")
	if strings.TrimSpace(action) == "" {
		return fmt.Errorf("--action is required: rerun, reroute, split, or cancel")
	}
	reason, _ := cmd.Flags().GetString("reason")
	into, _ := cmd.Flags().GetStringArray("into")
	body := map[string]any{"action": action}
	if reason != "" {
		body["reason"] = reason
	}
	if len(into) > 0 {
		body["into"] = into
	}
	var out map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+url.PathEscape(ref.ID)+"/dispose", body, &out); err != nil {
		return fmt.Errorf("dispose issue: %w", err)
	}
	if format, _ := cmd.Flags().GetString("output"); format == "json" {
		return cli.PrintJSON(os.Stdout, out)
	}
	fmt.Printf("Action: %v\nStatus: %v\nDriver: %s\n", out["action"], out["status"], driverCell(out))
	if created, ok := out["created"].([]any); ok && len(created) > 0 {
		names := make([]string, 0, len(created))
		for _, c := range created {
			names = append(names, fmt.Sprint(c))
		}
		fmt.Printf("Created: %s\n", strings.Join(names, ", "))
	}
	return nil
}

// driverCell renders an issue's driver (DENE-1342) for a table: the kind, and
// the reason when nobody drives it.
func driverCell(issue map[string]any) string {
	d, ok := issue["driver"].(map[string]any)
	if !ok {
		return "-"
	}
	kind, _ := d["kind"].(string)
	if kind != "none" {
		return kind
	}
	reason, _ := d["reason"].(string)
	return strings.TrimSpace("none " + reason)
}
