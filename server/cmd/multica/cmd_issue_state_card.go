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

// The issue state card (DENE-1328): a few hundred characters the server
// derives from what the issue already records. The CLI only reads it and
// edits the "已拍板" list; every rule lives on the server.

var issueContextCmd = &cobra.Command{
	Use:   "context <id>",
	Short: "Read the issue's state card: goal, decisions, where it stands, last baton, what changed",
	Long: `Print the issue's state card, derived by the server from what the issue
already records:

  目标              the title, and the goal's finish line when it has one
  已拍板            decisions written by close/handoff --decision or by people
  现在在哪          status, the latest close conclusion and what it waits on
  上一棒交代        the summary of the latest handoff or close
  你上次之后的变化  threads with comments new since you were last here

"You" is the caller: an agent counts from its previous run on this issue, a
person from their last comment. --since <RFC3339> overrides the anchor.
Threads are titles and ids only; expand one with
` + "`multica issue comment list <id> --thread <thread_id> --tail 30`" + `.

  multica issue context DENE-12
  multica issue context DENE-12 --output json`,
	Args: exactArgs(1),
	RunE: runIssueContext,
}

var issueDecisionCmd = &cobra.Command{
	Use:   "decision",
	Short: "Add, edit or remove the issue's settled decisions (已拍板)",
	Long: `The state card's 已拍板 list. A close or handoff adds to it with --decision;
these commands change it directly. A person may edit any decision; an agent
only the ones it wrote. Each decision is one line, 300 characters at most.

  multica issue decision add DENE-12 "拍板单独建表"
  multica issue decision edit DENE-12 <decision-id> "拍板改用新表"
  multica issue decision rm DENE-12 <decision-id>

Decision ids are in ` + "`multica issue context <id> --output json`" + `.`,
}

var issueDecisionAddCmd = &cobra.Command{
	Use:   "add <issue-id> <text>",
	Short: "Add a decision",
	Args:  exactArgs(2),
	RunE:  runIssueDecisionAdd,
}

var issueDecisionEditCmd = &cobra.Command{
	Use:   "edit <issue-id> <decision-id> <text>",
	Short: "Rewrite a decision",
	Args:  exactArgs(3),
	RunE:  runIssueDecisionEdit,
}

var issueDecisionRmCmd = &cobra.Command{
	Use:   "rm <issue-id> <decision-id>",
	Short: "Remove a decision",
	Args:  exactArgs(2),
	RunE:  runIssueDecisionRm,
}

func init() {
	issueCmd.AddCommand(issueContextCmd)
	issueCmd.AddCommand(issueDecisionCmd)
	issueDecisionCmd.AddCommand(issueDecisionAddCmd)
	issueDecisionCmd.AddCommand(issueDecisionEditCmd)
	issueDecisionCmd.AddCommand(issueDecisionRmCmd)

	issueContextCmd.Flags().String("since", "", "RFC3339 time to count changes from, instead of your last run or comment")
	issueContextCmd.Flags().String("output", "table", "Output format: table (the card as text) or json")
	for _, c := range []*cobra.Command{issueDecisionAddCmd, issueDecisionEditCmd, issueDecisionRmCmd} {
		c.Flags().String("output", "table", "Output format: table or json")
	}
}

func issueAPIPath(ctx context.Context, cmd *cobra.Command, raw string) (*cli.APIClient, string, error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, "", err
	}
	ref, err := resolveIssueRef(ctx, client, raw)
	if err != nil {
		return nil, "", fmt.Errorf("resolve issue: %w", err)
	}
	return client, "/api/issues/" + url.PathEscape(ref.ID), nil
}

func runIssueContext(cmd *cobra.Command, args []string) error {
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	client, base, err := issueAPIPath(ctx, cmd, args[0])
	if err != nil {
		return err
	}
	path := base + "/context"
	if since, _ := cmd.Flags().GetString("since"); strings.TrimSpace(since) != "" {
		path += "?since=" + url.QueryEscape(strings.TrimSpace(since))
	}
	var out map[string]any
	if err := client.GetJSON(ctx, path, &out); err != nil {
		return fmt.Errorf("read state card: %w", err)
	}
	if format, _ := cmd.Flags().GetString("output"); format == "json" {
		return cli.PrintJSON(os.Stdout, out)
	}
	text, _ := out["text"].(string)
	fmt.Print(text)
	if !strings.HasSuffix(text, "\n") {
		fmt.Println()
	}
	return nil
}

func printDecision(cmd *cobra.Command, out map[string]any, verb string) error {
	if format, _ := cmd.Flags().GetString("output"); format == "json" {
		return cli.PrintJSON(os.Stdout, out)
	}
	fmt.Printf("%s %v: %v\n", verb, out["id"], out["text"])
	return nil
}

func runIssueDecisionAdd(cmd *cobra.Command, args []string) error {
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	client, base, err := issueAPIPath(ctx, cmd, args[0])
	if err != nil {
		return err
	}
	var out map[string]any
	if err := client.PostJSON(ctx, base+"/decisions", map[string]any{"text": args[1]}, &out); err != nil {
		return fmt.Errorf("add decision: %w", err)
	}
	return printDecision(cmd, out, "Added")
}

func runIssueDecisionEdit(cmd *cobra.Command, args []string) error {
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	client, base, err := issueAPIPath(ctx, cmd, args[0])
	if err != nil {
		return err
	}
	var out map[string]any
	if err := client.PatchJSON(ctx, base+"/decisions/"+url.PathEscape(args[1]), map[string]any{"text": args[2]}, &out); err != nil {
		return fmt.Errorf("edit decision: %w", err)
	}
	return printDecision(cmd, out, "Updated")
}

func runIssueDecisionRm(cmd *cobra.Command, args []string) error {
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	client, base, err := issueAPIPath(ctx, cmd, args[0])
	if err != nil {
		return err
	}
	if err := client.DeleteJSON(ctx, base+"/decisions/"+url.PathEscape(args[1])); err != nil {
		return fmt.Errorf("remove decision: %w", err)
	}
	if format, _ := cmd.Flags().GetString("output"); format == "json" {
		return cli.PrintJSON(os.Stdout, map[string]any{"id": args[1], "deleted": true})
	}
	fmt.Printf("Removed %s\n", args[1])
	return nil
}
