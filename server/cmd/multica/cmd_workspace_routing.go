package main

import (
	"fmt"
	"os"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

// workspaceRoutingCmd edits the analysis role's transport and the 接着做 and
// 负载分流 switches without requiring a browser. It deliberately writes the same
// workspace.settings.routing block consumed by the web settings page.
var workspaceRoutingCmd = &cobra.Command{
	Use:   "routing",
	Short: "Configure the routing analysis model and the continuation and load rules",
}

var workspaceRoutingGetCmd = &cobra.Command{Use: "get", Short: "Show analysis routing settings and all routing switches", Args: cobra.NoArgs, RunE: runWorkspaceRoutingGet}
var workspaceRoutingSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Set analysis source, runtime, model, thinking level, or routing switches",
	Long: `Set analysis source, runtime, model, thinking level, or the continuation and load switches.

--continuation on|off is 接着做: a ticket continuing a previous stage, its
parent, or its batch goes back to that work's executor when the seat is strong
enough, online, not out of quota, and in the right direction. Off (the default)
is shadow mode: routing keeps its own pick and only says in the assignment
comment who the rule would have picked.

--load on|off is 负载分流: inside the rung and direction routing picked, the
seat with fewer unfinished runs (queued or running) takes the ticket; when all
are equally busy the usual order stands. Off (the default) is shadow mode, as
above. With both switches on, --continuation wins.`,
	Args: cobra.NoArgs,
	RunE: runWorkspaceRoutingSet,
}

func init() {
	workspaceRoutingGetCmd.Flags().String("output", "json", "Output format: json")
	workspaceRoutingSetCmd.Flags().String("source", "", "Analysis source: api_gateway or runtime_subscription")
	workspaceRoutingSetCmd.Flags().String("runtime", "", "Runtime ID for runtime_subscription")
	workspaceRoutingSetCmd.Flags().String("model", "", "Analysis model ID")
	workspaceRoutingSetCmd.Flags().String("thinking", "", "Thinking level: low, medium, high")
	workspaceRoutingSetCmd.Flags().String("continuation", "", "接着做 switch: on (prefer the previous executor) or off (shadow mode)")
	workspaceRoutingSetCmd.Flags().String("load", "", "负载分流 switch: on (prefer a less busy seat of the same tier) or off (shadow mode)")
	workspaceRoutingSetCmd.Flags().String("usage-priority", "", "用量优先 switch: on (ample seats first) or off (stable name order)")
	workspaceRoutingSetCmd.Flags().String("allow-upshift", "", "允许上调一档 switch: on (borrow an ample seat from the tier above) or off")
	workspaceRoutingCmd.AddCommand(workspaceRoutingGetCmd, workspaceRoutingSetCmd)
	workspaceCmd.AddCommand(workspaceRoutingCmd)
}

func runWorkspaceRoutingGet(cmd *cobra.Command, _ []string) error {
	client, wsID, err := routingProjectsSession(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	settings, err := loadWorkspaceSettings(ctx, client, wsID)
	if err != nil {
		return err
	}
	block, _ := settings["routing"].(map[string]any)
	return cli.PrintJSON(os.Stdout, routingView(block))
}

// routingView is what get and set print: the analysis transport, and the
// 接着做 and 负载分流 switches with the mode each puts routing in.
func routingView(block map[string]any) map[string]any {
	analysis, _ := block["analysis"].(map[string]any)
	continuation, _ := block["prefer_continuation"].(bool)
	idle, _ := block["prefer_idle"].(bool)
	usagePriority, ok := block["usage_priority"].(bool)
	if !ok {
		// The web and server default this omitted legacy field to on.
		usagePriority = true
	}
	allowUpshift, _ := block["allow_upshift"].(bool)
	return map[string]any{
		"source": analysis["source"], "runtime_id": analysis["runtime_id"], "model": analysis["model"], "thinking_level": analysis["thinking_level"],
		"prefer_continuation": continuation, "continuation_mode": switchMode(continuation),
		"prefer_idle": idle, "load_mode": switchMode(idle),
		"usage_priority": usagePriority, "allow_upshift": allowUpshift,
	}
}

// switchMode names a shadow-run switch's state: on, or shadow while off.
func switchMode(on bool) string {
	if on {
		return "on"
	}
	return "shadow"
}

func runWorkspaceRoutingSet(cmd *cobra.Command, _ []string) error {
	source, _ := cmd.Flags().GetString("source")
	runtimeID, _ := cmd.Flags().GetString("runtime")
	model, _ := cmd.Flags().GetString("model")
	thinking, _ := cmd.Flags().GetString("thinking")
	continuation, _ := cmd.Flags().GetString("continuation")
	if continuation != "" && continuation != "on" && continuation != "off" {
		return fmt.Errorf("--continuation must be on or off")
	}
	load, _ := cmd.Flags().GetString("load")
	if load != "" && load != "on" && load != "off" {
		return fmt.Errorf("--load must be on or off")
	}
	usagePriority, _ := cmd.Flags().GetString("usage-priority")
	if usagePriority != "" && usagePriority != "on" && usagePriority != "off" {
		return fmt.Errorf("--usage-priority must be on or off")
	}
	allowUpshift, _ := cmd.Flags().GetString("allow-upshift")
	if allowUpshift != "" && allowUpshift != "on" && allowUpshift != "off" {
		return fmt.Errorf("--allow-upshift must be on or off")
	}
	if source != "" && source != "api_gateway" && source != "runtime_subscription" {
		return fmt.Errorf("--source must be api_gateway or runtime_subscription")
	}
	if thinking != "" && thinking != "low" && thinking != "medium" && thinking != "high" {
		return fmt.Errorf("--thinking must be low, medium or high")
	}
	client, wsID, err := routingProjectsSession(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	settings, err := loadWorkspaceSettings(ctx, client, wsID)
	if err != nil {
		return err
	}
	block, _ := settings["routing"].(map[string]any)
	if block == nil {
		block = map[string]any{}
	}
	if continuation != "" {
		block["prefer_continuation"] = continuation == "on"
	}
	if load != "" {
		block["prefer_idle"] = load == "on"
	}
	if usagePriority != "" {
		block["usage_priority"] = usagePriority == "on"
	}
	if allowUpshift != "" {
		block["allow_upshift"] = allowUpshift == "on"
	}
	analysis, _ := block["analysis"].(map[string]any)
	if analysis == nil {
		analysis = map[string]any{}
	}
	if source != "" {
		analysis["source"] = source
	}
	if runtimeID != "" {
		analysis["runtime_id"] = runtimeID
	}
	if model != "" {
		analysis["model"] = model
	}
	if thinking != "" {
		analysis["thinking_level"] = thinking
	}
	if len(analysis) > 0 {
		block["analysis"] = analysis
	}
	settings["routing"] = block
	var out map[string]any
	if err := client.PatchJSON(ctx, "/api/workspaces/"+wsID, map[string]any{"settings": settings}, &out); err != nil {
		return err
	}
	return cli.PrintJSON(os.Stdout, routingView(block))
}
