package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var runtimeAgentCLICmd = &cobra.Command{
	Use:   "agent-cli <runtime-id>",
	Short: "Show or update the agent CLI (Claude, Codex, ...) behind a local runtime",
	Long: "Show the agent CLI update state the daemon last reported for a built-in local runtime:\n" +
		"phase (current, available, waiting, updating, failed, ...), current and latest version,\n" +
		"how many of this CLI's tasks an upgrade is waiting for, and whether new tasks of this CLI\n" +
		"are held back meanwhile.\n\n" +
		"--update-now asks the daemon to upgrade now: it stops taking new tasks for this CLI only,\n" +
		"waits for the running ones to finish (at most 30 minutes), upgrades, then takes tasks again.\n" +
		"--follow on|off sets whether the daemon keeps this CLI on the latest release by itself.\n" +
		"Both return right away; run the command again to see the daemon's next report.",
	Args: exactArgs(1),
	RunE: runRuntimeAgentCLI,
}

func init() {
	runtimeCmd.AddCommand(runtimeAgentCLICmd)
	runtimeAgentCLICmd.Flags().Bool("update-now", false, "Ask the daemon to upgrade this CLI as soon as its running tasks finish")
	runtimeAgentCLICmd.Flags().String("follow", "", "Keep this CLI on the latest release automatically: on or off")
	runtimeAgentCLICmd.Flags().String("output", "table", "Output format: table or json")
}

func runRuntimeAgentCLI(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	runtimeID := args[0]
	updateNow, _ := cmd.Flags().GetBool("update-now")
	followFlag, _ := cmd.Flags().GetString("follow")

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	result := map[string]any{"runtime_id": runtimeID}
	if followFlag != "" {
		var follow bool
		switch strings.ToLower(strings.TrimSpace(followFlag)) {
		case "on", "true":
			follow = true
		case "off", "false":
			follow = false
		default:
			return fmt.Errorf("--follow must be on or off")
		}
		if err := client.PostJSON(ctx, "/api/runtimes/"+runtimeID+"/agent-cli/follow", map[string]any{"follow": follow}, nil); err != nil {
			return fmt.Errorf("set auto-follow: %w", err)
		}
		result["follow_set"] = follow
	}
	if updateNow {
		var resp map[string]any
		if err := client.PostJSON(ctx, "/api/runtimes/"+runtimeID+"/agent-cli/update", map[string]any{}, &resp); err != nil {
			return fmt.Errorf("request update: %w", err)
		}
		result["update_requested"] = true
		result["request_id"] = strVal(resp, "request_id")
	}

	var runtimes []map[string]any
	if err := client.GetJSON(ctx, "/api/runtimes", &runtimes); err != nil {
		return fmt.Errorf("list runtimes: %w", err)
	}
	var rt map[string]any
	for _, candidate := range runtimes {
		if strVal(candidate, "id") == runtimeID {
			rt = candidate
			break
		}
	}
	if rt == nil {
		return fmt.Errorf("runtime %s not found in this workspace", runtimeID)
	}
	result["provider"] = strVal(rt, "provider")
	status, _ := nestedMap(rt, "metadata")["cli_update"].(map[string]any)
	result["cli_update"] = status

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	if updateNow {
		fmt.Fprintln(os.Stderr, "Update requested; the daemon picks it up on its next heartbeat.")
	}
	if status == nil {
		fmt.Fprintln(os.Stderr, "The daemon has not reported this CLI yet (only built-in local runtimes report one).")
		return nil
	}
	detail := strVal(status, "error")
	if detail == "" {
		detail = strVal(status, "note")
	}
	headers := []string{"PROVIDER", "PHASE", "CURRENT", "LATEST", "AUTO_FOLLOW", "WAITING_TASKS", "CLAIMS_PAUSED", "DETAIL"}
	cli.PrintTable(os.Stdout, headers, [][]string{{
		strVal(rt, "provider"),
		strVal(status, "phase"),
		strVal(status, "current_version"),
		strVal(status, "latest_version"),
		strVal(status, "auto_follow"),
		strVal(status, "waiting_tasks"),
		strVal(status, "claims_paused"),
		detail,
	}})
	return nil
}
