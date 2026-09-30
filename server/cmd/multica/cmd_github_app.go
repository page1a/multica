package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var githubAppCmd = &cobra.Command{
	Use:   "github-app",
	Short: "GitHub App identity for this deployment",
	Long: `Show whether this server has a GitHub App, and mint a link a workspace
owner opens to create one.

The link opens a page that posts GitHub's manifest form. It works once and
expires after 10 minutes; mint a new one rather than resending an old one. Creating the App
asks for the GitHub password once; the CLI cannot do that step. When the
server already has the App in environment variables, that configuration
wins and this command will not replace it.`,
}

var githubAppStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether a GitHub App is configured",
	RunE:  runGitHubAppStatus,
}

var githubAppSetupLinkCmd = &cobra.Command{
	Use:   "setup-link",
	Short: "Mint a link a workspace owner opens to create the GitHub App",
	RunE:  runGitHubAppSetupLink,
}

func init() {
	githubAppCmd.AddCommand(githubAppStatusCmd)
	githubAppCmd.AddCommand(githubAppSetupLinkCmd)
	githubAppStatusCmd.Flags().String("output", "json", "Output format: table or json")
	githubAppSetupLinkCmd.Flags().String("output", "json", "Output format: table or json")
	githubAppSetupLinkCmd.Flags().String("org", "", "GitHub organization login; empty creates the App on the signed-in user")
}

type githubAppStatus struct {
	Source      string `json:"source"`
	Configured  bool   `json:"configured"`
	ReadOnly    bool   `json:"read_only"`
	CanCreate   bool   `json:"can_create"`
	BlockReason string `json:"block_reason,omitempty"`
	AppName     string `json:"app_name,omitempty"`
	Slug        string `json:"slug,omitempty"`
	HTMLURL     string `json:"html_url,omitempty"`
	ManageURL   string `json:"manage_url,omitempty"`
}

type githubAppSetup struct {
	ActionURL string         `json:"action_url"`
	Manifest  map[string]any `json:"manifest"`
	LaunchURL string         `json:"launch_url"`
}

func runGitHubAppStatus(cmd *cobra.Command, _ []string) error {
	client, wsID, err := githubAppClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var status githubAppStatus
	if err := client.GetJSON(ctx, "/api/workspaces/"+wsID+"/github/app", &status); err != nil {
		return err
	}
	format, _ := cmd.Flags().GetString("output")
	if format == "json" {
		return cli.PrintJSON(os.Stdout, status)
	}
	fmt.Printf("Source: %s\nConfigured: %t\nRead-only: %t\n", status.Source, status.Configured, status.ReadOnly)
	if status.AppName != "" {
		fmt.Printf("App: %s\n", status.AppName)
	}
	if status.ManageURL != "" {
		fmt.Printf("Manage: %s\n", status.ManageURL)
	}
	if status.BlockReason != "" {
		fmt.Printf("Blocked: %s\n", status.BlockReason)
	}
	return nil
}

func runGitHubAppSetupLink(cmd *cobra.Command, _ []string) error {
	client, wsID, err := githubAppClient(cmd)
	if err != nil {
		return err
	}
	org, _ := cmd.Flags().GetString("org")
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var setup githubAppSetup
	body := map[string]string{"org": strings.TrimSpace(org)}
	if err := client.PostJSON(ctx, "/api/workspaces/"+wsID+"/github/app", body, &setup); err != nil {
		return err
	}
	format, _ := cmd.Flags().GetString("output")
	if format == "json" {
		return cli.PrintJSON(os.Stdout, setup)
	}
	fmt.Printf("Open this link in a browser signed into GitHub, then confirm with your password (single use, expires in 10 minutes):\n%s\n", setup.LaunchURL)
	return nil
}

func githubAppClient(cmd *cobra.Command) (*cli.APIClient, string, error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, "", err
	}
	wsID, err := requireWorkspaceID(cmd)
	if err != nil {
		return nil, "", err
	}
	return client, wsID, nil
}
