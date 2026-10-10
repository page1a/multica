package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/spf13/cobra"
)

// workspaceRoutingLearningCmd prints 从结果里学 (DENE-1722): per class of
// ticket, how many were tiered in the window, how many were later escalated
// or held, and whether the next ticket of that class is raised one tier.
var workspaceRoutingLearningCmd = &cobra.Command{
	Use:   "learning",
	Short: "Show which classes of tickets were judged too low and get raised a tier",
	Long: `Show 从结果里学: every class of ticket routing tiered in the window
(direction × tier × scope, clarity, risk), how many of them were later
escalated by their executor (multica issue escalate) or held at acceptance
(verdict: hold), and whether the next ticket of that class is raised one tier.

A class is raised when it has at least min_sample tickets and its share of
judged-low tickets reaches low_rate. With the switch off (mode "shadow") the
raise is only written into the assignment comment; turn it on with:
multica workspace routing set --learn on

Filter with --direction / --tier / --scope / --clarity / --risk.`,
	Args: cobra.NoArgs,
	RunE: runWorkspaceRoutingLearning,
}

func init() {
	workspaceRoutingLearningCmd.Flags().String("output", "table", "Output format: table or json")
	for _, f := range []string{"direction", "tier", "scope", "clarity", "risk"} {
		workspaceRoutingLearningCmd.Flags().String(f, "", "Only classes with this "+f)
	}
	workspaceRoutingCmd.AddCommand(workspaceRoutingLearningCmd)
}

func runWorkspaceRoutingLearning(cmd *cobra.Command, _ []string) error {
	client, wsID, err := routingProjectsSession(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	var rep routing.LearningReport
	if err := client.GetJSON(ctx, "/api/workspaces/"+wsID+"/routing/learning", &rep); err != nil {
		return fmt.Errorf("read routing learning: %w", err)
	}
	rep.Classes = filterLearnClasses(cmd, rep.Classes)
	if format, _ := cmd.Flags().GetString("output"); format == "json" {
		return cli.PrintJSON(os.Stdout, rep)
	}
	fmt.Printf("从结果里学: %s · raise at %.0f%% of ≥%d tickets · last %d days\n", rep.Mode, rep.LowRate*100, rep.MinSample, rep.WindowDays)
	if len(rep.Classes) == 0 {
		fmt.Println("No ticket tiered in the window.")
		return nil
	}
	fmt.Printf("%-10s %-6s %-14s %-8s %-8s %5s %4s %4s %4s  %s\n", "DIRECTION", "TIER", "SCOPE", "CLARITY", "RISK", "TOTAL", "LOW", "ESC", "HOLD", "NEXT")
	for _, c := range rep.Classes {
		next := "—"
		if c.Raise {
			next = "→ " + c.To
		}
		k := c.Stats.Class
		fmt.Printf("%-10s %-6s %-14s %-8s %-8s %5d %4d %4d %4d  %s\n", k.Direction, k.Tier, k.Scope, k.Clarity, k.Risk,
			c.Stats.Total, c.Stats.Low, c.Stats.Escalated, c.Stats.Held, next)
	}
	return nil
}

func filterLearnClasses(cmd *cobra.Command, in []routing.LearnPick) []routing.LearnPick {
	want := map[string]string{}
	for _, f := range []string{"direction", "tier", "scope", "clarity", "risk"} {
		if v, _ := cmd.Flags().GetString(f); strings.TrimSpace(v) != "" {
			want[f] = strings.TrimSpace(v)
		}
	}
	out := make([]routing.LearnPick, 0, len(in))
	for _, c := range in {
		k := c.Stats.Class
		have := map[string]string{"direction": k.Direction, "tier": k.Tier, "scope": k.Scope, "clarity": k.Clarity, "risk": k.Risk}
		keep := true
		for f, v := range want {
			if !strings.EqualFold(have[f], v) {
				keep = false
			}
		}
		if keep {
			out = append(out, c)
		}
	}
	return out
}
