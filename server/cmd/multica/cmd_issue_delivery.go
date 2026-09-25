package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/service"
)

// multica issue delivery — the issue's canonical delivery line and every
// other branch a task has produced for it (DENE-820). One issue has one
// canonical branch/PR; the rest must be classified rescue/experiment, and
// after the merge the cleanup plan lists what may be removed locally.
var issueDeliveryCmd = &cobra.Command{
	Use:   "delivery <issue-id>",
	Short: "Show the issue's canonical delivery line and other branches",
	Long: `Show which branch/PR is the issue's single delivery truth, every other
branch a task has produced for it, and the local cleanup plan once merged.

Subcommands:
  set-canonical  make a branch the canonical line (the old one becomes rescue)
  classify       mark a non-canonical branch rescue/experiment, optionally resolved
  cleanup        list (or --apply) removal of non-canonical worktrees/branches`,
	Args: cobra.ExactArgs(1),
	RunE: runIssueDeliveryShow,
}

var issueDeliverySetCanonicalCmd = &cobra.Command{
	Use:   "set-canonical <issue-id> <branch>",
	Short: "Make a branch the issue's canonical delivery line",
	Args:  cobra.ExactArgs(2),
	RunE:  runIssueDeliverySetCanonical,
}

var issueDeliveryClassifyCmd = &cobra.Command{
	Use:   "classify <issue-id> <branch>",
	Short: "Classify a non-canonical branch as rescue or experiment",
	Args:  cobra.ExactArgs(2),
	RunE:  runIssueDeliveryClassify,
}

var issueDeliveryCleanupCmd = &cobra.Command{
	Use:   "cleanup <issue-id>",
	Short: "List, or with --apply remove, non-canonical worktrees and branches after merge",
	Long: `Without --apply, prints the server's cleanup plan: which branches may be
removed and why the others are kept. With --apply, removes each allowed branch
from the local repository (worktrees, branch, Multica state refs) when git
shows it merged into --trunk (a discarded rescue is removed regardless), and
records the outcome on the issue.`,
	Args: cobra.ExactArgs(1),
	RunE: runIssueDeliveryCleanup,
}

func init() {
	issueDeliveryCmd.Flags().String("output", "table", "Output format: table or json")
	issueDeliverySetCanonicalCmd.Flags().String("output", "table", "Output format: table or json")
	issueDeliveryClassifyCmd.Flags().String("output", "table", "Output format: table or json")
	issueDeliveryClassifyCmd.Flags().String("role", "", "rescue or experiment (required)")
	issueDeliveryClassifyCmd.Flags().String("resolution", "", "absorbed or discarded once the line is settled")
	_ = issueDeliveryClassifyCmd.MarkFlagRequired("role")
	issueDeliveryCleanupCmd.Flags().String("output", "table", "Output format: table or json")
	issueDeliveryCleanupCmd.Flags().Bool("apply", false, "Remove allowed branches locally and record the outcome")
	issueDeliveryCleanupCmd.Flags().String("git-root", ".", "Local repository to clean")
	issueDeliveryCleanupCmd.Flags().String("trunk", "", "Trunk branch to check merge evidence against (default: origin/kun, then origin/main)")

	issueDeliveryCmd.AddCommand(issueDeliverySetCanonicalCmd)
	issueDeliveryCmd.AddCommand(issueDeliveryClassifyCmd)
	issueDeliveryCmd.AddCommand(issueDeliveryCleanupCmd)
	issueCmd.AddCommand(issueDeliveryCmd)
}

func deliveryPath(issueID, suffix string) string {
	return "/api/issues/" + url.PathEscape(issueID) + "/delivery" + suffix
}

func deliveryContext(cmd *cobra.Command, ref string) (*cli.APIClient, context.Context, context.CancelFunc, string, error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, nil, nil, "", err
	}
	ctx, cancel := cli.APIContext(context.Background())
	issueRef, err := resolveIssueRef(ctx, client, ref)
	if err != nil {
		cancel()
		return nil, nil, nil, "", fmt.Errorf("resolve issue: %w", err)
	}
	return client, ctx, cancel, issueRef.ID, nil
}

func runIssueDeliveryShow(cmd *cobra.Command, args []string) error {
	client, ctx, cancel, issueID, err := deliveryContext(cmd, args[0])
	if err != nil {
		return err
	}
	defer cancel()
	var d service.IssueDelivery
	if err := client.GetJSON(ctx, deliveryPath(issueID, ""), &d); err != nil {
		return fmt.Errorf("get issue delivery: %w", err)
	}
	return printIssueDelivery(cmd, &d)
}

func runIssueDeliverySetCanonical(cmd *cobra.Command, args []string) error {
	client, ctx, cancel, issueID, err := deliveryContext(cmd, args[0])
	if err != nil {
		return err
	}
	defer cancel()
	var d service.IssueDelivery
	body := map[string]string{"branch": strings.TrimSpace(args[1])}
	if err := client.PutJSON(ctx, deliveryPath(issueID, "/canonical"), body, &d); err != nil {
		return fmt.Errorf("set canonical delivery branch: %w", err)
	}
	fmt.Fprintf(os.Stderr, "canonical delivery line is now %s\n", args[1])
	return printIssueDelivery(cmd, &d)
}

func runIssueDeliveryClassify(cmd *cobra.Command, args []string) error {
	role, _ := cmd.Flags().GetString("role")
	resolution, _ := cmd.Flags().GetString("resolution")
	client, ctx, cancel, issueID, err := deliveryContext(cmd, args[0])
	if err != nil {
		return err
	}
	defer cancel()
	var d service.IssueDelivery
	body := map[string]string{
		"branch":     strings.TrimSpace(args[1]),
		"role":       strings.TrimSpace(role),
		"resolution": strings.TrimSpace(resolution),
	}
	if err := client.PostJSON(ctx, deliveryPath(issueID, "/classify"), body, &d); err != nil {
		return fmt.Errorf("classify delivery branch: %w", err)
	}
	return printIssueDelivery(cmd, &d)
}

func runIssueDeliveryCleanup(cmd *cobra.Command, args []string) error {
	apply, _ := cmd.Flags().GetBool("apply")
	gitRoot, _ := cmd.Flags().GetString("git-root")
	trunk, _ := cmd.Flags().GetString("trunk")
	output, _ := cmd.Flags().GetString("output")

	client, ctx, cancel, issueID, err := deliveryContext(cmd, args[0])
	if err != nil {
		return err
	}
	defer cancel()
	var d service.IssueDelivery
	if err := client.GetJSON(ctx, deliveryPath(issueID, ""), &d); err != nil {
		return fmt.Errorf("get issue delivery: %w", err)
	}
	if !apply {
		if output == "json" {
			return cli.PrintJSON(os.Stdout, d.CleanupPlan)
		}
		printDeliveryCleanupPlan(&d)
		return nil
	}

	trunk, err = resolveCleanupTrunk(gitRoot, trunk)
	if err != nil {
		return err
	}
	type applied struct {
		Branch  string   `json:"branch"`
		Status  string   `json:"status"`
		Note    string   `json:"note,omitempty"`
		Removed []string `json:"removed,omitempty"`
	}
	var results []applied
	for _, item := range d.CleanupPlan {
		if !item.Allowed {
			results = append(results, applied{Branch: item.Branch, Status: "skipped", Note: item.Reason})
			continue
		}
		force := false
		for _, b := range d.Branches {
			if b.Branch == item.Branch && b.Resolution == service.DeliveryResolutionDiscarded {
				force = true
			}
		}
		res, err := execenv.CleanupDeliveryBranch(gitRoot, item.Branch, trunk, force, nil)
		if err != nil {
			return fmt.Errorf("cleanup %s: %w", item.Branch, err)
		}
		status, note := service.DeliveryCleanupCleaned, ""
		if !res.Cleaned() {
			status, note = service.DeliveryCleanupKept, res.Kept
		} else if len(res.Removed) > 0 {
			note = "removed " + strings.Join(res.Removed, ", ")
		}
		body := map[string]string{"branch": item.Branch, "status": status, "note": note}
		if err := client.PostJSON(ctx, deliveryPath(issueID, "/cleanup"), body, &d); err != nil {
			return fmt.Errorf("record cleanup of %s: %w", item.Branch, err)
		}
		results = append(results, applied{Branch: item.Branch, Status: status, Note: note, Removed: res.Removed})
	}
	if output == "json" {
		return cli.PrintJSON(os.Stdout, results)
	}
	if len(results) == 0 {
		fmt.Println("nothing to clean up")
		return nil
	}
	rows := make([][]string, 0, len(results))
	for _, r := range results {
		rows = append(rows, []string{r.Branch, r.Status, r.Note})
	}
	cli.PrintTable(os.Stdout, []string{"BRANCH", "RESULT", "NOTE"}, rows)
	return nil
}

// resolveCleanupTrunk picks the trunk to compare against: the flag when given,
// else the first of origin/kun, origin/main that exists locally.
func resolveCleanupTrunk(gitRoot, trunk string) (string, error) {
	if strings.TrimSpace(trunk) != "" {
		return trunk, nil
	}
	for _, candidate := range []string{"origin/kun", "origin/main", "kun", "main"} {
		if _, err := execenv.RevParseQuiet(gitRoot, candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no trunk branch found in %s; pass --trunk", gitRoot)
}

func printIssueDelivery(cmd *cobra.Command, d *service.IssueDelivery) error {
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, d)
	}
	canonical := "(none)"
	if d.Canonical != nil {
		canonical = d.Canonical.Branch
	}
	fmt.Printf("Issue: %s (%s)  Canonical: %s  Merged: %v\n", d.IssueID, d.IssueStatus, canonical, d.Merged)
	rows := make([][]string, 0, len(d.Branches))
	for _, b := range d.Branches {
		pr := ""
		if b.PullRequest != nil {
			pr = fmt.Sprintf("#%d %s", b.PullRequest.Number, b.PullRequest.State)
		}
		agents := make([]string, 0, len(b.Tasks))
		seen := map[string]bool{}
		for _, t := range b.Tasks {
			name := t.AgentName
			if name == "" {
				name = t.AgentID
			}
			if !seen[name] {
				seen[name] = true
				agents = append(agents, name)
			}
		}
		rows = append(rows, []string{b.Branch, b.Role, b.Resolution, b.CleanupStatus, pr, fmt.Sprintf("%d", len(b.Tasks)), strings.Join(agents, ",")})
	}
	cli.PrintTable(os.Stdout, []string{"BRANCH", "ROLE", "RESOLUTION", "CLEANUP", "PR", "TASKS", "AGENTS"}, rows)
	if len(d.Problems) > 0 {
		fmt.Println("\nProblems:")
		for _, p := range d.Problems {
			fmt.Println("  - " + p)
		}
	}
	return nil
}

func printDeliveryCleanupPlan(d *service.IssueDelivery) {
	if !d.Merged {
		fmt.Println("issue is not merged yet; nothing is cleanable")
	}
	if len(d.CleanupPlan) == 0 {
		fmt.Println("no non-canonical branches recorded")
		return
	}
	rows := make([][]string, 0, len(d.CleanupPlan))
	for _, item := range d.CleanupPlan {
		verdict := "keep: " + item.Reason
		if item.Allowed {
			verdict = "removable"
		}
		rows = append(rows, []string{item.Branch, item.Role, item.CleanupStatus, verdict, strings.Join(item.WorkDirs, ",")})
	}
	cli.PrintTable(os.Stdout, []string{"BRANCH", "ROLE", "CLEANUP", "VERDICT", "WORKDIRS"}, rows)
}
