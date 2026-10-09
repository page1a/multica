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

The tier comes from the routing rule table (multica workspace routing rules):
the analysis model answers numbered questions, the first matching row names the
tier, and the judge model may only raise it one rung. The "trace" field of the
JSON output carries each answer, the matched row, the judge's effect, the final
tier and the seat.

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
	if v := str("tier"); v != "" {
		if from := str("judged_tier"); from != "" {
			fmt.Printf("档位:    %s（判断模型给的 %s，按规则下限抬档）\n", v, from)
		} else {
			fmt.Printf("档位:    %s\n", v)
		}
	}
	if v := str("reviewer"); v != "" {
		fmt.Printf("验收席:   %s\n", v)
	}
	printRouteTrace(out["trace"])
	if boolean("commented") {
		fmt.Println("Comment: posted")
	}
	if boolean("mentioned") {
		fmt.Println("Mention: notified (comment + subscriber + inbox)")
	}
}

// printRouteTrace prints how the tier was decided: each question's answer,
// the rule-table row it matched, and what the judge model did (DENE-1677).
func printRouteTrace(raw any) {
	t, _ := raw.(map[string]any)
	if t == nil {
		return
	}
	str := func(m map[string]any, k string) string {
		v, _ := m[k].(string)
		return v
	}
	fmt.Println("定档过程:")
	if e := str(t, "analysis_error"); e != "" {
		fmt.Printf("  分析模型没答上: %s\n", e)
	}
	questions, _ := t["questions"].([]any)
	for _, q := range questions {
		a, _ := q.(map[string]any)
		if a == nil {
			continue
		}
		line := "  " + str(a, "label") + ": " + str(a, "value_label")
		if rawAnswer := str(a, "raw"); rawAnswer != "" {
			line += "（答 " + rawAnswer + "）"
		}
		fmt.Println(line)
	}
	if rule, _ := t["rule"].(map[string]any); rule != nil {
		index, _ := rule["index"].(float64)
		review := "要验收"
		if str(rule, "reviewer") == "none" {
			review = "不需要验收"
		}
		fmt.Printf("  规则: 第 %d 行「%s」→ %s，%s\n", int(index), str(rule, "label"), str(rule, "tier"), review)
	}
	if j, _ := t["judge"].(map[string]any); j != nil {
		parts := []string{str(j, "effect")}
		if tier := str(j, "tier"); tier != "" {
			parts = append(parts, "答 "+tier)
		}
		for _, k := range []string{"reason", "why"} {
			if v := str(j, k); v != "" {
				parts = append(parts, v)
			}
		}
		fmt.Printf("  判断模型: %s\n", strings.Join(parts, " · "))
	}
	if tier := str(t, "tier"); tier != "" {
		fmt.Printf("  定档: %s\n", tier)
	}
}
