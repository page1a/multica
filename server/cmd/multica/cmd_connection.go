package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/repoident"
	"github.com/spf13/cobra"
)

var connectionCmd = &cobra.Command{Use: "connection", Short: "Manage Git connections"}
var connectionRepoCmd = &cobra.Command{Use: "repo", Short: "Inspect repository connections"}
var connectionListCmd = &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: runConnectionList}
var connectionAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Register a GitHub or GitLab token connection",
	Long: `The token is read from a file, from stdin (--token-file -), or from the local gh/glab login.
It is never accepted as a command-line flag.

  multica connection add --from-gh --yes
  multica connection add --provider gitlab --token-file ./token.txt --repo https://gitlab.example/group/app

--from-gh and --from-glab register the login on repositories this person added.
Inside an agent task the server only accepts repositories the task initiator
registered, with or without --yes. --yes only skips the confirmation prompt.
Without --repo, a GitLab or Forgejo token is saved as an admin-owned connection
for the whole instance.`,
	Args: cobra.NoArgs,
	RunE: runConnectionAdd,
}
var connectionTestCmd = &cobra.Command{Use: "test <connection-id>", Args: exactArgs(1), RunE: runConnectionTest}
var connectionRemoveCmd = &cobra.Command{Use: "remove <connection-id>", Args: exactArgs(1), RunE: runConnectionRemove}
var connectionRepoStatusCmd = &cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: runConnectionRepoStatus}

// connectionTokenCommand runs a local credential helper. Tests replace it.
// The arguments never include the secret; it stays on stdout.
var connectionTokenCommand = func(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

func init() {
	for _, c := range []*cobra.Command{connectionListCmd, connectionAddCmd, connectionTestCmd, connectionRemoveCmd, connectionRepoStatusCmd} {
		c.Flags().String("output", "table", "Output format: table or json")
	}
	connectionAddCmd.Flags().String("provider", "", "Provider: github or gitlab")
	connectionAddCmd.Flags().String("instance-url", "", "Provider instance URL (default is the public host)")
	connectionAddCmd.Flags().String("repo", "", "Repository URL. Required for a GitHub token that is not --from-gh")
	connectionAddCmd.Flags().String("token-file", "", "Read the token from a file, or '-' for stdin. Never pass the token as a flag")
	connectionAddCmd.Flags().Bool("from-gh", false, "Read the token from `gh auth token` and register it on your GitHub repositories")
	connectionAddCmd.Flags().Bool("from-glab", false, "Read the token from `glab auth token` and register it on your GitLab repositories")
	connectionAddCmd.Flags().Bool("yes", false, "Skip the confirmation prompt. For an agent this only covers the task initiator's repositories")
	connectionAddCmd.Flags().Bool("workspace", false, "Register an admin-owned connection for the whole instance instead of one repository")
	connectionCmd.AddCommand(connectionListCmd, connectionAddCmd, connectionTestCmd, connectionRemoveCmd, connectionRepoCmd)
	connectionRepoCmd.AddCommand(connectionRepoStatusCmd)
	rootCmd.AddCommand(connectionCmd)
}

func connectionClient(cmd *cobra.Command) (*cli.APIClient, string, error) {
	ws, err := requireWorkspaceID(cmd)
	if err != nil {
		return nil, "", err
	}
	c, err := newAPIClient(cmd)
	if err != nil {
		return nil, "", err
	}
	return c, ws, nil
}

func connectionWantsJSON(cmd *cobra.Command) bool {
	o, _ := cmd.Flags().GetString("output")
	return o == "json"
}

func runConnectionList(cmd *cobra.Command, _ []string) error {
	c, ws, err := connectionClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var out map[string]any
	if err = c.GetJSON(ctx, "/api/workspaces/"+ws+"/vcs/connections", &out); err != nil {
		return fmt.Errorf("list connections: %w", err)
	}
	redactConnectionPayload(out)
	if connectionWantsJSON(cmd) {
		return cli.PrintJSON(cmd.OutOrStdout(), out)
	}
	rows, _ := out["connections"].([]any)
	if len(rows) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No connections")
		return nil
	}
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s\n", row["provider"], row["account_login"], row["instance_url"])
		if repo := strings.TrimSpace(fmt.Sprint(row["repo_url"])); repo != "" && repo != "<nil>" {
			fmt.Fprintf(cmd.OutOrStdout(), "  covers: %s\n", repo)
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), "  covers: whole instance")
		}
	}
	return nil
}

func readConnectionToken(cmd *cobra.Command) (string, error) {
	file, _ := cmd.Flags().GetString("token-file")
	gh, _ := cmd.Flags().GetBool("from-gh")
	glab, _ := cmd.Flags().GetBool("from-glab")
	n := 0
	if gh {
		n++
	}
	if glab {
		n++
	}
	if file != "" {
		n++
	}
	if n != 1 {
		return "", fmt.Errorf("pass exactly one of --token-file, --from-gh, or --from-glab")
	}
	if gh || glab {
		name := "gh"
		if glab {
			name = "glab"
		}
		b, err := connectionTokenCommand(name, "auth", "token")
		if err != nil {
			return "", fmt.Errorf("read %s login failed", name)
		}
		token := strings.TrimSpace(string(b))
		if token == "" {
			return "", fmt.Errorf("%s login is empty", name)
		}
		return token, nil
	}
	var r io.Reader = cmd.InOrStdin()
	if file != "-" {
		f, err := os.Open(file)
		if err != nil {
			return "", err
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return "", fmt.Errorf("token is empty")
	}
	return token, nil
}

func confirmConnection(cmd *cobra.Command, scope []string, personal bool) error {
	label := "将登记为你的个人连接，覆盖："
	if !personal {
		label = "将登记为工作区连接，覆盖："
	}
	fmt.Fprintln(cmd.ErrOrStderr(), label)
	for _, line := range scope {
		fmt.Fprintf(cmd.ErrOrStderr(), "- %s\n", line)
	}
	yes, _ := cmd.Flags().GetBool("yes")
	if yes {
		return nil
	}
	fmt.Fprint(cmd.ErrOrStderr(), "继续？输入 y 确认：")
	answer, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if strings.ToLower(strings.TrimSpace(answer)) != "y" {
		return fmt.Errorf("cancelled")
	}
	return nil
}

type connectionRepoCard struct {
	URL           string
	Provider      string
	Mode          string
	CanConfigure  bool
	AgentEligible bool
}

func runConnectionAdd(cmd *cobra.Command, _ []string) error {
	c, ws, err := connectionClient(cmd)
	if err != nil {
		return err
	}
	provider, _ := cmd.Flags().GetString("provider")
	gh, _ := cmd.Flags().GetBool("from-gh")
	glab, _ := cmd.Flags().GetBool("from-glab")
	if provider == "" {
		switch {
		case gh:
			provider = "github"
		case glab:
			provider = "gitlab"
		}
	}
	if provider == "" {
		return fmt.Errorf("--provider is required")
	}
	workspaceWide, _ := cmd.Flags().GetBool("workspace")
	repoFlag, _ := cmd.Flags().GetString("repo")
	// An agent task is recognized by the process environment. --yes only
	// skips the prompt; the server still refuses any other repository.
	agentTask := strings.TrimSpace(os.Getenv("MULTICA_AGENT_ID")) != ""
	if agentTask && workspaceWide {
		return fmt.Errorf("--workspace cannot be used from an agent task")
	}
	personal := !workspaceWide && (gh || glab || repoFlag != "")
	if provider == "github" && !personal {
		return fmt.Errorf("a GitHub token is saved per repository; pass --from-gh or --repo")
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var targets []connectionRepoCard
	if personal {
		targets, err = connectionRepoTargets(ctx, c, ws, provider, repoFlag, agentTask)
		if err != nil {
			return err
		}
		if len(targets) == 0 {
			return fmt.Errorf("没有可以接上的仓库")
		}
	}

	token, err := readConnectionToken(cmd)
	if err != nil {
		return err
	}
	scope := make([]string, 0, len(targets))
	for _, target := range targets {
		scope = append(scope, target.URL)
	}
	instance, _ := cmd.Flags().GetString("instance-url")
	if !personal {
		if instance == "" {
			if provider == "gitlab" {
				instance = "https://gitlab.com"
			} else {
				return fmt.Errorf("--instance-url is required")
			}
		}
		scope = []string{instance + " 整个实例"}
	}
	if err = confirmConnection(cmd, scope, personal); err != nil {
		return err
	}
	if personal {
		return postRepoConnections(cmd, ctx, c, ws, provider, instance, token, targets, agentTask)
	}
	body := map[string]any{
		"provider":     provider,
		"instance_url": instance,
		"access_token": token,
	}
	var out map[string]any
	if err = c.PostJSON(ctx, "/api/workspaces/"+ws+"/vcs/connections", body, &out); err != nil {
		return redactToken(fmt.Errorf("add connection: %w", err), token)
	}
	redactConnectionPayload(out)
	if connectionWantsJSON(cmd) {
		return cli.PrintJSON(cmd.OutOrStdout(), out)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "connected %s (%s)\n", out["account_login"], out["id"])
	return nil
}

func connectionRepoTargets(ctx context.Context, c *cli.APIClient, ws, provider, repoFlag string, agentOnly bool) ([]connectionRepoCard, error) {
	var out map[string]any
	if err := c.GetJSON(ctx, "/api/workspaces/"+ws+"/repos/connections", &out); err != nil {
		return nil, fmt.Errorf("list repositories: %w", err)
	}
	rows, _ := out["repos"].([]any)
	want := strings.TrimSpace(strings.TrimRight(repoFlag, "/"))
	var targets []connectionRepoCard
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		card := connectionRepoCard{
			URL:           fmt.Sprint(row["url"]),
			Provider:      fmt.Sprint(row["provider"]),
			Mode:          fmt.Sprint(row["mode"]),
			CanConfigure:  row["can_configure"] == true,
			AgentEligible: row["agent_eligible"] == true,
		}
		if card.Provider != provider && card.Provider != "" && provider != "" {
			if !(provider == "github" && strings.Contains(card.URL, "github.com")) && !(provider == "gitlab" && strings.Contains(card.URL, "gitlab")) {
				continue
			}
		}
		if !connectionRepoWanted(card.URL, want) {
			continue
		}
		if want == "" && (card.Mode == "token" || card.Mode == "app") {
			continue
		}
		if agentOnly {
			if !card.AgentEligible {
				continue
			}
		} else if !card.CanConfigure {
			continue
		}
		targets = append(targets, card)
	}
	return targets, nil
}

// connectionRepoWanted matches --repo to one card. A full URL compares as the
// same repository; owner/name compares those two segments exactly, so
// "acme/app" does not select "acme/app-private".
func connectionRepoWanted(cardURL, want string) bool {
	want = strings.TrimSpace(want)
	if want == "" {
		return true
	}
	cardTrim := strings.TrimRight(strings.TrimSpace(cardURL), "/")
	wantTrim := strings.TrimRight(want, "/")
	if strings.EqualFold(cardTrim, wantTrim) {
		return true
	}
	cardKey := string(repoident.NormalizeURL(cardURL))
	wantKey := string(repoident.NormalizeURL(want))
	if cardKey != "" && wantKey != "" && strings.EqualFold(cardKey, wantKey) {
		return true
	}
	wantPath := strings.ToLower(strings.Trim(strings.TrimSuffix(wantTrim, ".git"), "/"))
	if cardKey != "" && !strings.Contains(want, "://") && strings.Count(wantPath, "/") == 1 {
		parts := strings.Split(cardKey, "/")
		if len(parts) >= 3 {
			return parts[len(parts)-2]+"/"+parts[len(parts)-1] == wantPath
		}
	}
	return false
}

func postRepoConnections(cmd *cobra.Command, ctx context.Context, c *cli.APIClient, ws, provider, instance, token string, targets []connectionRepoCard, agentYes bool) error {
	saved := make([]any, 0, len(targets))
	for _, target := range targets {
		body := map[string]any{
			"repo_url":     target.URL,
			"provider":     provider,
			"access_token": token,
			"agent_yes":    agentYes,
		}
		if instance != "" {
			body["instance_url"] = instance
		}
		var out map[string]any
		if err := c.PostJSON(ctx, "/api/workspaces/"+ws+"/repos/connections", body, &out); err != nil {
			return redactToken(fmt.Errorf("add connection: %w", err), token)
		}
		redactConnectionPayload(out)
		saved = append(saved, out)
	}
	if connectionWantsJSON(cmd) {
		return cli.PrintJSON(cmd.OutOrStdout(), map[string]any{"repos": saved})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "connected %d repositories\n", len(saved))
	return nil
}

func runConnectionTest(cmd *cobra.Command, args []string) error {
	c, ws, err := connectionClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var out map[string]any
	if err = c.PostJSON(ctx, "/api/workspaces/"+ws+"/vcs/connections/"+args[0]+"/test", map[string]any{}, &out); err != nil {
		return fmt.Errorf("test connection: %w", err)
	}
	redactConnectionPayload(out)
	if connectionWantsJSON(cmd) {
		return cli.PrintJSON(cmd.OutOrStdout(), out)
	}
	if out["ok"] == true {
		fmt.Fprintf(cmd.OutOrStdout(), "ok  %s\n", out["account_login"])
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "failed  %s\n", out["error"])
	return nil
}

func runConnectionRemove(cmd *cobra.Command, args []string) error {
	c, ws, err := connectionClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	if err = c.DeleteJSON(ctx, "/api/workspaces/"+ws+"/vcs/connections/"+args[0]); err != nil {
		return fmt.Errorf("remove connection: %w", err)
	}
	if connectionWantsJSON(cmd) {
		return cli.PrintJSON(cmd.OutOrStdout(), map[string]any{"removed": args[0]})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", args[0])
	return nil
}

func runConnectionRepoStatus(cmd *cobra.Command, _ []string) error {
	c, ws, err := connectionClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var out map[string]any
	if err = c.GetJSON(ctx, "/api/workspaces/"+ws+"/repos/connections", &out); err != nil {
		return fmt.Errorf("repo status: %w", err)
	}
	if connectionWantsJSON(cmd) {
		return cli.PrintJSON(cmd.OutOrStdout(), out)
	}
	rows, _ := out["repos"].([]any)
	if len(rows) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No repositories")
		return nil
	}
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s  %s\n", row["url"], row["mode"], row["account_login"], row["connection_id"])
	}
	return nil
}

func redactToken(err error, token string) error {
	if err == nil || token == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), token, "[redacted]")
	return errors.New(msg)
}

func redactConnectionPayload(v any) {
	switch val := v.(type) {
	case map[string]any:
		delete(val, "access_token")
		for _, child := range val {
			redactConnectionPayload(child)
		}
	case []any:
		for _, child := range val {
			redactConnectionPayload(child)
		}
	}
}
