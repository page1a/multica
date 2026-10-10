package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/spf13/cobra"
)

// workspaceRoutingCmd edits the analysis role's transport and the 接着做 and
// 负载分流 switches without requiring a browser. It deliberately writes the same
// workspace.settings.routing block consumed by the web settings page.
var workspaceRoutingCmd = &cobra.Command{
	Use:   "routing",
	Short: "Configure routing analysis, thresholds, and dispatch rules",
}

var workspaceRoutingGetCmd = &cobra.Command{Use: "get", Short: "Show analysis routing settings, thresholds, and switches", Args: cobra.NoArgs, RunE: runWorkspaceRoutingGet}
var workspaceRoutingSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Set routing analysis, thresholds, or switches",
	Long: `Set analysis source, runtime, model, thinking level, thresholds, or dispatch switches.

--continuation on|off is 接着做: a ticket continuing a previous stage, its
parent, or its batch goes back to that work's executor when the seat is strong
enough, online, not out of quota, and in the right direction. Off (the default)
is shadow mode: routing keeps its own pick and only says in the assignment
comment who the rule would have picked.

--load on|off is 负载分流: inside the rung and direction routing picked, the
seat with fewer unfinished runs (queued or running) takes the ticket; when all
are equally busy the usual order stands. Off (the default) is shadow mode, as
above. With both switches on, --continuation wins.

--learn on|off is 从结果里学: a class of tickets (direction × tier × scope,
clarity, risk) whose recent tickets were escalated by their executor or held
at acceptance often enough gets its next ticket one tier higher. Off (the
default) is shadow mode, as above. --learn-low-rate (0, 1] is the share that
triggers it (default 0.3), --learn-window-days [1, 365] how far back it looks
(default 30); a class needs at least 5 tickets. See the numbers with:
multica workspace routing learning --output json

--judged-review on|off is 按判断配验收. It is kept so old configs still read:
since DENE-1677 the rule table (multica workspace routing rules) decides
whether a ticket needs a check, so the switch has no effect.`,
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
	workspaceRoutingSetCmd.Flags().String("learn", "", "从结果里学 switch: on (raise a class judged low too often one tier) or off (shadow mode)")
	workspaceRoutingSetCmd.Flags().String("learn-low-rate", "", "从结果里学: share of a class judged low that raises it, (0, 1]")
	workspaceRoutingSetCmd.Flags().String("learn-window-days", "", "从结果里学: days of history the rate is taken over, [1, 365]")
	workspaceRoutingSetCmd.Flags().String("usage-priority", "", "用量优先 switch: on (ample seats first) or off (stable name order)")
	workspaceRoutingSetCmd.Flags().String("allow-upshift", "", "允许上调一档 switch: on (borrow an ample seat from the tier above) or off")
	workspaceRoutingSetCmd.Flags().String("judged-review", "", "按判断配验收 switch (no effect since the rule table decides the check; kept for old configs)")
	workspaceRoutingSetCmd.Flags().String("confidence-threshold", "", "Confidence floor for the routing model (0, 1]")
	workspaceRoutingSetCmd.Flags().String("stale-review-hours", "", "Hours before an inactive in-review ticket is checked (0, 8760]")
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
	learn, _ := block["learn_from_outcomes"].(bool)
	lowRate := routing.DefaultLearnLowRate
	if value, ok := numberFromRoutingBlock(block, "learn_low_rate"); ok && value > 0 && value <= 1 {
		lowRate = value
	}
	windowDays := float64(routing.DefaultLearnWindowDays)
	if value, ok := numberFromRoutingBlock(block, "learn_window_days"); ok && value >= 1 && value <= 365 {
		windowDays = float64(int(value))
	}
	judgedReview, _ := block["judged_review"].(bool)
	confidenceThreshold := routing.DefaultConfidenceThreshold
	if value, ok := numberFromRoutingBlock(block, "confidence_threshold"); ok && value > 0 && value <= 1 {
		confidenceThreshold = value
	}
	staleReviewHours := float64(routing.DefaultStaleReviewHours)
	if value, ok := numberFromRoutingBlock(block, "stale_review_hours"); ok && value > 0 && value <= 24*365 {
		staleReviewHours = value
	}
	return map[string]any{
		"source": analysis["source"], "runtime_id": analysis["runtime_id"], "model": analysis["model"], "thinking_level": analysis["thinking_level"],
		"prefer_continuation": continuation, "continuation_mode": switchMode(continuation),
		"prefer_idle": idle, "load_mode": switchMode(idle),
		"learn_from_outcomes": learn, "learn_mode": switchMode(learn), "learn_low_rate": lowRate, "learn_window_days": windowDays,
		"usage_priority": usagePriority, "allow_upshift": allowUpshift, "judged_review": judgedReview,
		"confidence_threshold": confidenceThreshold, "stale_review_hours": staleReviewHours,
	}
}

func numberFromRoutingBlock(block map[string]any, key string) (float64, bool) {
	value, ok := block[key]
	if !ok {
		return 0, false
	}
	switch value := value.(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case json.Number:
		parsed, err := strconv.ParseFloat(string(value), 64)
		return parsed, err == nil
	default:
		return 0, false
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
	learn, _ := cmd.Flags().GetString("learn")
	if learn != "" && learn != "on" && learn != "off" {
		return fmt.Errorf("--learn must be on or off")
	}
	learnLowRate, _ := cmd.Flags().GetString("learn-low-rate")
	lowRateValue, err := parseRoutingRange(learnLowRate, "--learn-low-rate", 0, 1, false)
	if err != nil {
		return err
	}
	learnWindowDays, _ := cmd.Flags().GetString("learn-window-days")
	windowValue, err := parseRoutingRange(learnWindowDays, "--learn-window-days", 1, 365, true)
	if err != nil {
		return err
	}
	usagePriority, _ := cmd.Flags().GetString("usage-priority")
	if usagePriority != "" && usagePriority != "on" && usagePriority != "off" {
		return fmt.Errorf("--usage-priority must be on or off")
	}
	allowUpshift, _ := cmd.Flags().GetString("allow-upshift")
	if allowUpshift != "" && allowUpshift != "on" && allowUpshift != "off" {
		return fmt.Errorf("--allow-upshift must be on or off")
	}
	judgedReview, _ := cmd.Flags().GetString("judged-review")
	if judgedReview != "" && judgedReview != "on" && judgedReview != "off" {
		return fmt.Errorf("--judged-review must be on or off")
	}
	confidenceThreshold, _ := cmd.Flags().GetString("confidence-threshold")
	confidenceValue, err := parseRoutingConfidenceThreshold(confidenceThreshold)
	if err != nil {
		return err
	}
	staleReviewHours, _ := cmd.Flags().GetString("stale-review-hours")
	staleHoursValue, err := parseRoutingStaleReviewHours(staleReviewHours)
	if err != nil {
		return err
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
	if learn != "" {
		block["learn_from_outcomes"] = learn == "on"
	}
	if learnLowRate != "" {
		block["learn_low_rate"] = lowRateValue
	}
	if learnWindowDays != "" {
		block["learn_window_days"] = windowValue
	}
	if usagePriority != "" {
		block["usage_priority"] = usagePriority == "on"
	}
	if allowUpshift != "" {
		block["allow_upshift"] = allowUpshift == "on"
	}
	if judgedReview != "" {
		block["judged_review"] = judgedReview == "on"
	}
	if confidenceThreshold != "" {
		block["confidence_threshold"] = confidenceValue
	}
	if staleReviewHours != "" {
		block["stale_review_hours"] = staleHoursValue
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

func parseRoutingConfidenceThreshold(value string) (float64, error) {
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || !(parsed > 0 && parsed <= 1) {
		return 0, fmt.Errorf("--confidence-threshold must be a number in (0, 1]")
	}
	return parsed, nil
}

func parseRoutingStaleReviewHours(value string) (float64, error) {
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || !(parsed > 0 && parsed <= 24*365) {
		return 0, fmt.Errorf("--stale-review-hours must be a number in (0, 8760]")
	}
	return parsed, nil
}

// parseRoutingRange reads an optional number flag. The lower bound is
// inclusive only when lowInclusive is set; the upper bound always is.
func parseRoutingRange(value, flag string, low, high float64, lowInclusive bool) (float64, error) {
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	inRange := parsed <= high && (parsed > low || (lowInclusive && parsed == low))
	if err != nil || !inRange {
		open := "("
		if lowInclusive {
			open = "["
		}
		return 0, fmt.Errorf("%s must be a number in %s%v, %v]", flag, open, low, high)
	}
	return parsed, nil
}
