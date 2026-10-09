package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// `multica project memory monitor` reads the same response as the project
// page's memory card (DENE-1681): writes and deletions, finished tickets
// that wrote nothing, idle sediment rounds, and chat-dispatched tickets'
// receipts.
var projectMemoryMonitorCmd = &cobra.Command{
	Use:   "monitor <id>",
	Short: "Show what project memory gained and lost, unsettled closes, idle rounds, and chat receipts",
	Long: `Reads one window (default 14 days, up to 90) of a project's memory activity:
  writes     sediments with their source ticket or chat, entries marked superseded, and deleted lines
  unsettled  tickets that finished without writing memory (none = declared nothing, unaudited = no audit)
  rounds     sediment rounds; idle = ended without a sediment
  chats      tickets each chat dispatched: reported, no_conclusion (finished without issue close), open
Rows the caller cannot see are left out; a private chat's title is withheld.`,
	Args: exactArgs(1),
	RunE: runProjectMemoryMonitor,
}

func init() {
	projectMemoryCmd.AddCommand(projectMemoryMonitorCmd)
	projectMemoryMonitorCmd.Flags().Int("days", 0, "Window in days, 1-90 (default 14)")
	projectMemoryMonitorCmd.Flags().String("output", "json", "Output format: table or json")
}

func runProjectMemoryMonitor(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	projectRef, err := resolveProjectID(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve project: %w", err)
	}
	path := "/api/projects/" + projectRef.ID + "/memory/monitor"
	if days, _ := cmd.Flags().GetInt("days"); days != 0 {
		path += "?" + url.Values{"days": {fmt.Sprint(days)}}.Encode()
	}
	var result map[string]any
	if err := client.GetJSON(ctx, path, &result); err != nil {
		return fmt.Errorf("get project memory monitor: %w", err)
	}
	if output, _ := cmd.Flags().GetString("output"); output != "table" {
		return cli.PrintJSON(os.Stdout, result)
	}
	printMemoryMonitor(os.Stdout, result)
	return nil
}

func printMemoryMonitor(out io.Writer, result map[string]any) {
	fmt.Fprintf(out, "Last %s days (since %s)\n", strVal(result, "days"), strVal(result, "since"))

	writes, _ := result["writes"].([]any)
	fmt.Fprintf(out, "\nWrites (%d):\n", len(writes))
	for _, raw := range writes {
		write, _ := raw.(map[string]any)
		line := "  " + sedimentSummaryLine(write)
		if superseded, _ := write["superseded"].([]any); len(superseded) > 0 {
			names := make([]string, 0, len(superseded))
			for _, s := range superseded {
				names = append(names, fmt.Sprint(s))
			}
			line += "  superseded: " + strings.Join(names, ", ")
		}
		if deleted := strVal(write, "deleted_lines"); deleted != "" && deleted != "0" {
			line += "  deleted lines: " + deleted
		}
		fmt.Fprintln(out, line)
	}

	unsettled, _ := result["unsettled"].([]any)
	fmt.Fprintf(out, "\nFinished without sediment (%d):\n", len(unsettled))
	for _, raw := range unsettled {
		item, _ := raw.(map[string]any)
		fmt.Fprintf(out, "  %s  %-9s  %s %s\n", strVal(item, "closed_at"), strVal(item, "reason"), strVal(item, "identifier"), strVal(item, "title"))
	}

	rounds, _ := result["rounds"].(map[string]any)
	items, _ := rounds["items"].([]any)
	fmt.Fprintf(out, "\nSediment rounds: %s opened, %s open, %s idle\n", strVal(rounds, "opened"), strVal(rounds, "open"), strVal(rounds, "idle"))
	for _, raw := range items {
		round, _ := raw.(map[string]any)
		state := strVal(round, "status")
		if round["idle"] == true {
			state += " (idle)"
		}
		fmt.Fprintf(out, "  %s  %-6s  %s  %s\n", strVal(round, "identifier"), strVal(round, "layer"), state, strVal(round, "title"))
	}

	chats, _ := result["chats"].([]any)
	fmt.Fprintf(out, "\nChat receipts (%d chats):\n", len(chats))
	for _, raw := range chats {
		chat, _ := raw.(map[string]any)
		title := strVal(chat, "title")
		if chat["accessible"] != true {
			title = "(chat you cannot open)"
		}
		fmt.Fprintf(out, "  %s %s: %s dispatched, %s reported, %s no conclusion, %s open\n",
			strVal(chat, "chat_session_id"), title, strVal(chat, "dispatched"), strVal(chat, "reported"), strVal(chat, "no_conclusion"), strVal(chat, "open"))
		tickets, _ := chat["tickets"].([]any)
		for _, rawTicket := range tickets {
			ticket, _ := rawTicket.(map[string]any)
			fmt.Fprintf(out, "    %s  %-13s  %-11s  %s\n", strVal(ticket, "identifier"), strVal(ticket, "flow"), strVal(ticket, "status"), strVal(ticket, "title"))
		}
	}
}
