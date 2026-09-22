package main

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

var issueRouteCmd = &cobra.Command{
	Use:   "route <id>",
	Short: "Re-run the routing pass on one issue and print what it did",
	Long: `Ask the routing layer, right now, who should be holding this issue.

This is the manual entry point to the SAME module the server runs when an issue
is created and whenever its status changes — not a second implementation. Run
it on an issue the hooks already handled and it will report that there was
nothing left to fill, because routing only ever writes a slot that is still
empty.

What it can change:

  - the assignee, but only while that slot is empty
  - the issue's 验收席 (reviewer), but only while that slot is empty
  - one routing comment per kind, and the subscriber needed for its @ to notify

What it never changes: the status. Status is a fact about the work, and only
whoever is doing the work knows it.

When routing is switched off, has no model chosen, or its model is cooling down
after repeated failures, this reports that state and writes nothing at all —
the same path the hooks take.

Examples:
  # Why did MUL-123 not get an assignee?
  multica issue route MUL-123

  # Machine-readable outcome
  multica issue route MUL-123 --output json`,
	Args: cobra.ExactArgs(1),
	RunE: runIssueRoute,
}

func init() {
	issueRouteCmd.Flags().String("output", "table", "Output format: table or json")
	issueCmd.AddCommand(issueRouteCmd)
}

func runIssueRoute(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	var out map[string]any
	if err := client.PostJSON(ctx,
		"/api/issues/"+url.PathEscape(issueRef.ID)+"/route", map[string]any{}, &out); err != nil {
		return fmt.Errorf("route issue: %w", err)
	}

	if format, _ := cmd.Flags().GetString("output"); format == "json" {
		return cli.PrintJSON(os.Stdout, out)
	}
	printIssueRoute(out)
	return nil
}

func printIssueRoute(out map[string]any) {
	str := func(k string) string {
		v, _ := out[k].(string)
		return v
	}
	boolean := func(k string) bool {
		v, _ := out[k].(bool)
		return v
	}

	fmt.Printf("State:   %s\n", str("state"))
	fmt.Printf("Action:  %s\n", str("action"))
	if r := str("reason"); r != "" {
		fmt.Printf("Reason:  %s\n", r)
	}
	if v := str("executor"); v != "" {
		fmt.Printf("执行席:   %s\n", v)
	}
	if v := str("reviewer"); v != "" {
		fmt.Printf("验收席:   %s\n", v)
	}
	if boolean("commented") {
		fmt.Println("Comment: posted")
	}
	if boolean("mentioned") {
		fmt.Println("Mention: notified (comment + subscriber + inbox)")
	}
}
