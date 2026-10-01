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

var issueEscalateCmd = &cobra.Command{
	Use:   "escalate <id> --reason <text>",
	Short: "Tell routing this issue is too hard for its executor; routing re-judges",
	Long: `Say that the work on this issue is too hard for the seat holding it.

This is the ONLY thing an executor says about who should work on its own
issue. It takes a reason and nothing else: no person, no agent, no tier.
Routing reads the reason, judges again from scratch, and moves the issue to a
stronger seat if it finds one (one rung up if its own answer is not clear). If
the holder already sits on the strongest rung there is nowhere to move it, and
the reply says so — then say what you need in a comment instead.

Do not reassign the issue yourself or attach a tier label for this: an
executor's own pick is ignored.

Examples:
  multica issue escalate MUL-123 --reason "touches the auth flow and three services; I cannot hold it all"
  multica issue escalate MUL-123 --reason "..." --output json`,
	Args: cobra.ExactArgs(1),
	RunE: runIssueEscalate,
}

func init() {
	issueEscalateCmd.Flags().String("reason", "", "Why the work is too hard for the current executor (required)")
	issueEscalateCmd.Flags().String("output", "table", "Output format: table or json")
	issueCmd.AddCommand(issueEscalateCmd)
}

func runIssueEscalate(cmd *cobra.Command, args []string) error {
	reason, _ := cmd.Flags().GetString("reason")
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("--reason is required: say what makes the work too hard")
	}
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
		"/api/issues/"+url.PathEscape(issueRef.ID)+"/escalate", map[string]any{"reason": reason}, &out); err != nil {
		return fmt.Errorf("escalate issue: %w", err)
	}
	if format, _ := cmd.Flags().GetString("output"); format == "json" {
		return cli.PrintJSON(os.Stdout, out)
	}
	switch {
	case out["changed"] == true:
		fmt.Printf("Escalated: %v -> %v\n", out["from"], out["to"])
	case out["at_top"] == true:
		fmt.Printf("%v already sits on the strongest rung; nothing to move. Say what you need in a comment.\n", out["from"])
	default:
		fmt.Println("No change.")
	}
	return nil
}
