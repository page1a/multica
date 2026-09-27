package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var issueSummonCmd = &cobra.Command{
	Use:   "summon <id>",
	Short: "Call a person onto an issue: inbox, subscription and a visible @ in one step",
	Long: "One command for \"this ticket needs this person\" (DENE-880). The server writes\n" +
		"their inbox row (needs you, highest severity), subscribes them, leaves a\n" +
		"visible @ on the ticket, and records the open call: when they reply, the\n" +
		"executor is woken. Calling the same person again before they reply is a\n" +
		"no-op that reports duplicate.\n\n" +
		"  --to      the person: name, email or user id\n" +
		"  --reason  one sentence on what they need to decide\n\n" +
		"A close with --needs-human already calls that person; do not summon again.",
	Args: exactArgs(1),
	RunE: runIssueSummon,
}

func init() {
	issueCmd.AddCommand(issueSummonCmd)
	issueSummonCmd.Flags().String("to", "", "the person to call: name, email or user id (required)")
	issueSummonCmd.Flags().String("reason", "", "what they need to decide (required)")
	issueSummonCmd.Flags().String("output", "table", "Output format: table or json")
}

func runIssueSummon(cmd *cobra.Command, args []string) error {
	to, _ := cmd.Flags().GetString("to")
	reason, _ := cmd.Flags().GetString("reason")
	if strings.TrimSpace(to) == "" {
		return fmt.Errorf("--to is required: the person to call")
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("--reason is required: what they need to decide")
	}
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
	var out map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+url.PathEscape(ref.ID)+"/summon", map[string]any{"to": to, "reason": reason}, &out); err != nil {
		return fmt.Errorf("summon: %w", err)
	}
	if format, _ := cmd.Flags().GetString("output"); format == "json" {
		return cli.PrintJSON(os.Stdout, out)
	}
	fmt.Printf("Called: %v\nDuplicate: %v\n", out["recipient_name"], out["duplicate"])
	if lines, ok := out["result"].([]any); ok {
		for _, l := range lines {
			fmt.Printf("- %v\n", l)
		}
	}
	return nil
}
