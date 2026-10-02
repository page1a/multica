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

// goalCmd is deliberately a small command family around one server-side
// contract. The server owns validation and locking; the CLI only resolves the
// issue reference and presents the resulting goal.
var goalCmd = &cobra.Command{
	Use:   "goal",
	Short: "Draft and track a task goal",
}

var goalDraftCmd = &cobra.Command{
	Use:   "draft <issue>",
	Short: "Attach a completion line waiting for human confirmation",
	Long: "Attach a draft completion line to an issue. Repeat --check for each " +
		"criterion. A check may name how it will be verified (command, test, " +
		"screenshot, or acceptance). The server rejects changes after confirmation.",
	Args: exactArgs(1),
	RunE: runGoalDraft,
}

var goalGetCmd = &cobra.Command{
	Use:   "get <issue>",
	Short: "Show an issue's goal",
	Args:  exactArgs(1),
	RunE:  runGoalGet,
}

var goalConfirmCmd = &cobra.Command{
	Use:   "confirm <issue>",
	Short: "Confirm and lock a draft goal",
	Args:  exactArgs(1),
	RunE:  runGoalConfirm,
}

var goalBudgetCmd = &cobra.Command{
	Use:   "budget <issue>",
	Short: "Append budget to a goal",
	Args:  exactArgs(1),
	RunE:  runGoalBudget,
}

var goalFinishCmd = &cobra.Command{
	Use:   "finish <issue>",
	Short: "Stop or mark a goal achieved",
	Args:  exactArgs(1),
	RunE:  runGoalFinish,
}

var goalCheckCmd = &cobra.Command{
	Use:   "check <issue> <check>",
	Short: "Record evidence for a completion-line check",
	Long:  "Record a check by id or position. The server still owns the continuation and review decision.",
	Args:  exactArgs(2),
	RunE:  runGoalCheck,
}

func init() {
	for _, c := range []*cobra.Command{goalDraftCmd, goalGetCmd, goalConfirmCmd, goalBudgetCmd, goalFinishCmd, goalCheckCmd} {
		c.Flags().String("output", "table", "Output format: table or json")
	}
	goalDraftCmd.Flags().StringSlice("check", nil, "Completion criterion (repeatable; required)")
	goalDraftCmd.Flags().StringSlice("method", nil, "Verification method for checks, in order: command, test, screenshot, acceptance")
	goalDraftCmd.Flags().Int64("token-budget", 0, "Token budget to reserve")
	goalDraftCmd.Flags().Int64("run-budget", 0, "Run budget to reserve")
	goalDraftCmd.Flags().Int64("duration-budget", 0, "Total duration budget in seconds to reserve")
	goalBudgetCmd.Flags().Int64("tokens", 0, "Additional token budget")
	goalBudgetCmd.Flags().Int64("runs", 0, "Additional run budget")
	goalBudgetCmd.Flags().Int64("duration", 0, "Additional duration budget in seconds")
	goalFinishCmd.Flags().String("status", "achieved", "End state: achieved or stopped")
	goalCheckCmd.Flags().String("status", "passed", "Check state: pending, passed, or failed")
	goalCheckCmd.Flags().String("evidence", "", "Evidence detail to attach")

	goalCmd.AddCommand(goalDraftCmd, goalGetCmd, goalConfirmCmd, goalBudgetCmd, goalFinishCmd, goalCheckCmd)
	rootCmd.AddCommand(goalCmd)
}

type goalCheckInput struct {
	Description string `json:"description"`
	Method      string `json:"method,omitempty"`
}

func goalOutput(cmd *cobra.Command, out any, table func(any)) error {
	format, _ := cmd.Flags().GetString("output")
	if format == "json" {
		return cli.PrintJSON(os.Stdout, out)
	}
	table(out)
	return nil
}

func goalClient(cmd *cobra.Command, arg string) (*cli.APIClient, context.Context, string, func(), error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, nil, "", nil, err
	}
	ctx, cancel := cli.APIContext(context.Background())
	ref, err := resolveIssueRef(ctx, client, arg)
	if err != nil {
		cancel()
		return nil, nil, "", nil, fmt.Errorf("resolve issue: %w", err)
	}
	return client, ctx, url.PathEscape(ref.ID), cancel, nil
}

func runGoalDraft(cmd *cobra.Command, args []string) error {
	checks, _ := cmd.Flags().GetStringSlice("check")
	if len(checks) == 0 {
		return fmt.Errorf("--check is required at least once")
	}
	methods, _ := cmd.Flags().GetStringSlice("method")
	if len(methods) > len(checks) {
		return fmt.Errorf("--method has %d values but --check has %d", len(methods), len(checks))
	}
	items := make([]goalCheckInput, len(checks))
	for i, description := range checks {
		method := ""
		if i < len(methods) {
			method = strings.ToLower(strings.TrimSpace(methods[i]))
		}
		items[i] = goalCheckInput{Description: description, Method: method}
	}
	tokens, _ := cmd.Flags().GetInt64("token-budget")
	runs, _ := cmd.Flags().GetInt64("run-budget")
	duration, _ := cmd.Flags().GetInt64("duration-budget")
	if tokens < 0 || runs < 0 || duration < 0 {
		return fmt.Errorf("budget values cannot be negative")
	}
	client, ctx, id, cancel, err := goalClient(cmd, args[0])
	if err != nil {
		return err
	}
	defer cancel()
	body := map[string]any{
		"checks": items,
		"budget": map[string]any{"token_limit": tokens, "run_limit": runs, "duration_seconds": duration},
	}
	var out map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+id+"/goal", body, &out); err != nil {
		return fmt.Errorf("draft goal: %w", err)
	}
	return goalOutput(cmd, out, printGoal)
}

func runGoalGet(cmd *cobra.Command, args []string) error {
	client, ctx, id, cancel, err := goalClient(cmd, args[0])
	if err != nil {
		return err
	}
	defer cancel()
	var out map[string]any
	if err := client.GetJSON(ctx, "/api/issues/"+id+"/goal", &out); err != nil {
		return fmt.Errorf("get goal: %w", err)
	}
	return goalOutput(cmd, out, printGoal)
}

func runGoalConfirm(cmd *cobra.Command, args []string) error {
	return runGoalAction(cmd, args[0], "confirm", nil)
}

func runGoalBudget(cmd *cobra.Command, args []string) error {
	tokens, _ := cmd.Flags().GetInt64("tokens")
	runs, _ := cmd.Flags().GetInt64("runs")
	duration, _ := cmd.Flags().GetInt64("duration")
	if tokens < 0 || runs < 0 || duration < 0 || (tokens == 0 && runs == 0 && duration == 0) {
		return fmt.Errorf("provide at least one non-zero, non-negative budget value")
	}
	return runGoalAction(cmd, args[0], "budget", map[string]any{"token_limit": tokens, "run_limit": runs, "duration_seconds": duration})
}

func runGoalFinish(cmd *cobra.Command, args []string) error {
	status, _ := cmd.Flags().GetString("status")
	if status != "achieved" && status != "stopped" {
		return fmt.Errorf("--status must be achieved or stopped")
	}
	return runGoalAction(cmd, args[0], "finish", map[string]any{"status": status})
}

func runGoalCheck(cmd *cobra.Command, args []string) error {
	status, _ := cmd.Flags().GetString("status")
	if status != "pending" && status != "passed" && status != "failed" {
		return fmt.Errorf("--status must be pending, passed, or failed")
	}
	evidence, _ := cmd.Flags().GetString("evidence")
	client, ctx, id, cancel, err := goalClient(cmd, args[0])
	if err != nil {
		return err
	}
	defer cancel()
	body := map[string]any{"status": status}
	if strings.TrimSpace(evidence) != "" {
		body["evidence"] = []any{map[string]any{"kind": "cli", "detail": evidence}}
	}
	var out map[string]any
	checkID := url.PathEscape(args[1])
	if err := client.PostJSON(ctx, "/api/issues/"+id+"/goal/check/"+checkID, body, &out); err != nil {
		return fmt.Errorf("update goal check: %w", err)
	}
	return goalOutput(cmd, out, printGoal)
}

func runGoalAction(cmd *cobra.Command, arg, action string, body any) error {
	client, ctx, id, cancel, err := goalClient(cmd, arg)
	if err != nil {
		return err
	}
	defer cancel()
	var out map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+id+"/goal/"+action, body, &out); err != nil {
		return fmt.Errorf("%s goal: %w", action, err)
	}
	return goalOutput(cmd, out, printGoal)
}

func printGoal(raw any) {
	out, ok := raw.(map[string]any)
	if !ok {
		fmt.Printf("%v\n", raw)
		return
	}
	fmt.Printf("Goal: %v\n", out["status"])
	if next, ok := out["next_action"]; ok {
		fmt.Printf("Next: %v\n", next)
	}
	if round, ok := out["round"]; ok {
		fmt.Printf("Round: %v\n", round)
	}
	if checks, ok := out["checks"].([]any); ok {
		for _, item := range checks {
			if check, ok := item.(map[string]any); ok {
				mark := "pending"
				if check["passed"] == true || check["status"] == "passed" {
					mark = "passed"
				}
				fmt.Printf("- [%s] %v", mark, check["description"])
				if method := check["method"]; method != nil && method != "" {
					fmt.Printf(" (%v)", method)
				}
				fmt.Println()
			}
		}
	}
	if budget, ok := out["budget"].(map[string]any); ok {
		fmt.Printf("Budget: tokens %v, runs %v, duration %v seconds\n", budget["token_limit"], budget["run_limit"], budget["duration_seconds"])
	}
	if usage, ok := out["usage"].(map[string]any); ok {
		fmt.Printf("Used: tokens %v, runs %v, duration %v seconds\n", usage["tokens"], usage["runs"], usage["duration_seconds"])
	}
	if rounds, ok := out["no_progress_rounds"]; ok {
		fmt.Printf("No progress rounds: %v/%v\n", rounds, out["max_no_progress_rounds"])
	}
}
