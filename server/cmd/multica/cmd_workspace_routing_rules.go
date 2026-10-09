package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/spf13/cobra"
)

// workspaceRoutingRulesCmd prints the rule table routing decides the tier
// with (DENE-1677). It reads the same /routing/health payload the settings
// page shows, so the CLI and the page cannot disagree about the table.
var workspaceRoutingRulesCmd = &cobra.Command{
	Use:   "rules",
	Short: "Show the rule table that decides an issue's tier",
	Long: `Show the questions the analysis model answers and the rule table that turns
the answers into a tier.

Rows are read top to bottom and the first match wins. A question nobody could
answer is 答不出 (unknown), and the table never sends an unknown or a
cross-module answer to the weakest tier. The judge model, when it is on, may
only raise the table's tier by one rung, with a reason.

The table ships with the server; this command only reads it. To see how one
issue was decided, run: multica issue route <id> --output json (field "trace").`,
	Args: cobra.NoArgs,
	RunE: runWorkspaceRoutingRules,
}

func init() {
	workspaceRoutingRulesCmd.Flags().String("output", "table", "Output format: table or json")
	workspaceRoutingCmd.AddCommand(workspaceRoutingRulesCmd)
}

func runWorkspaceRoutingRules(cmd *cobra.Command, _ []string) error {
	client, wsID, err := routingProjectsSession(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	var health struct {
		Rules routing.RuleTableView `json:"rules"`
	}
	if err := client.GetJSON(ctx, "/api/workspaces/"+wsID+"/routing/health", &health); err != nil {
		return fmt.Errorf("read routing rules: %w", err)
	}
	if len(health.Rules.Rules) == 0 {
		return fmt.Errorf("this server does not report a rule table; upgrade it")
	}
	if format, _ := cmd.Flags().GetString("output"); format == "json" {
		return cli.PrintJSON(os.Stdout, health.Rules)
	}
	printRuleTable(health.Rules)
	return nil
}

func printRuleTable(t routing.RuleTableView) {
	labels := map[string]map[string]string{}
	fmt.Println("Questions:")
	for _, q := range t.Questions {
		labels[q.Key] = map[string]string{"": q.Label}
		opts := make([]string, 0, len(q.Options))
		for i, o := range q.Options {
			labels[q.Key][o.Value] = o.Label
			opts = append(opts, fmt.Sprintf("%d %s", i+1, o.Label))
		}
		fmt.Printf("  %s: %s\n", q.Label, strings.Join(opts, " · "))
	}
	fmt.Println("Rules (first match wins):")
	for _, r := range t.Rules {
		review := "要验收"
		if r.Reviewer == routing.ReviewerNone {
			review = "不需要验收"
		}
		fmt.Printf("  %d. %s → %s档，%s%s\n", r.Index, r.Label, r.TierLabel, review, ruleCondition(r, labels))
	}
}

// ruleCondition spells a row's condition for a reader, e.g. （改动范围=跨模块）.
func ruleCondition(r routing.RuleView, labels map[string]map[string]string) string {
	if r.AnyUnknown {
		return "（任一题答不出）"
	}
	if len(r.When) == 0 {
		return ""
	}
	keys := make([]string, 0, len(r.When))
	for k := range r.When {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		values := make([]string, 0, len(r.When[k]))
		for _, v := range r.When[k] {
			if l := labels[k][v]; l != "" {
				v = l
			}
			values = append(values, v)
		}
		name := k
		if l := labels[k][""]; l != "" {
			name = l
		}
		parts = append(parts, name+"="+strings.Join(values, "/"))
	}
	return "（" + strings.Join(parts, "，") + "）"
}
