package main

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

func runIssueHandoff(cmd *cobra.Command, args []string) error {
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
	target, _ := cmd.Flags().GetString("to")
	if target == "" {
		return fmt.Errorf("--to is required")
	}
	var out map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+url.PathEscape(ref.ID)+"/handoff", map[string]any{"to": target}, &out); err != nil {
		return fmt.Errorf("handoff issue: %w", err)
	}
	if format, _ := cmd.Flags().GetString("output"); format == "json" {
		return cli.PrintJSON(os.Stdout, out)
	}
	fmt.Printf("Target: %v\nRun created: %v\nDuplicate: %v\n", out["target_name"], out["run_created"], out["duplicate"])
	if reason, ok := out["reason"].(string); ok && reason != "" {
		fmt.Printf("Reason: %s\n", reason)
	}
	return nil
}
