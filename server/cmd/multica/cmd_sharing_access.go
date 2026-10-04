package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// Sharing access reads (DENE-1214): the same answer the share button shows —
// who can see the resource and whether the caller may change that.

const sharingAccessLong = "Show who can see this %[1]s and whether you may change its sharing scope.\n\n" +
	"visibility is private (only the creator), project (specific people) or workspace " +
	"(every member except guests); audience_size is how many people it reaches. " +
	"can_change is the same rule the scope change enforces; when it is false, reason is " +
	"\"guest\" (guests are read-only) or \"not_creator\" (only the creator, workspace " +
	"admins and the owner may change it%[2]s)."

var issueAccessCmd = &cobra.Command{
	Use:   "access <id>",
	Short: "Show who can see an issue and whether you can change it",
	Long:  fmt.Sprintf(sharingAccessLong, "issue", ""),
	Args:  exactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSharingAccess(cmd, args[0], "issue")
	},
}

var projectAccessCmd = &cobra.Command{
	Use:   "access <id>",
	Short: "Show who can see a project and whether you can change it",
	Long:  fmt.Sprintf(sharingAccessLong, "project", "; a project led by an agent you own counts as yours"),
	Args:  exactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSharingAccess(cmd, args[0], "project")
	},
}

func init() {
	issueCmd.AddCommand(issueAccessCmd)
	projectCmd.AddCommand(projectAccessCmd)
	issueAccessCmd.Flags().String("output", "json", "Output format: table or json")
	projectAccessCmd.Flags().String("output", "json", "Output format: table or json")
}

func runSharingAccess(cmd *cobra.Command, ref, kind string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var id string
	if kind == "issue" {
		resolved, err := resolveIssueRef(ctx, client, ref)
		if err != nil {
			return fmt.Errorf("resolve issue: %w", err)
		}
		id = resolved.ID
	} else {
		resolved, err := resolveProjectID(ctx, client, ref)
		if err != nil {
			return fmt.Errorf("resolve project: %w", err)
		}
		id = resolved.ID
	}

	var access map[string]any
	if err := client.GetJSON(ctx, "/api/"+kind+"s/"+id+"/access", &access); err != nil {
		return fmt.Errorf("get %s access: %w", kind, err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		audience := ""
		if n, ok := access["audience_size"].(float64); ok {
			audience = strconv.Itoa(int(n))
		}
		canChange, _ := access["can_change"].(bool)
		headers := []string{"VISIBILITY", "AUDIENCE", "CAN CHANGE", "REASON"}
		rows := [][]string{{
			strVal(access, "visibility"),
			audience,
			strconv.FormatBool(canChange),
			strVal(access, "reason"),
		}}
		cli.PrintTable(os.Stdout, headers, rows)
		return nil
	}
	return cli.PrintJSON(os.Stdout, access)
}
