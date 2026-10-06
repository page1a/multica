package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

// Cross-workspace read-only links (DENE-1225). Every rule lives on the
// server (internal/workspacelink); these commands only call it.

var workspaceLinkCmd = &cobra.Command{
	Use:   "link",
	Short: "Read-only links that let another workspace see chosen projects",
	Long: "A link lets the members of another workspace (the viewer) read the status of\n" +
		"this workspace's chosen projects without becoming members. Only the owner can\n" +
		"offer a link; the viewer's owner or admin accepts it; either side can revoke it.\n\n" +
		"Agents use `list` to find the active links of their workspace and `view` to read one.",
}

var workspaceLinkListCmd = &cobra.Command{
	Use:   "list",
	Short: "List links this workspace is on (members and agents see only active incoming links)",
	Args:  cobra.NoArgs,
	RunE:  runWorkspaceLinkList,
}

var workspaceLinkLookupCmd = &cobra.Command{
	Use:   "lookup <workspace-link-or-slug>",
	Short: "Show which workspace a pasted link or slug names (owner only, exact match)",
	Args:  cobra.ExactArgs(1),
	RunE:  runWorkspaceLinkLookup,
}

var workspaceLinkCreateCmd = &cobra.Command{
	Use:   "create --to <workspace-link-or-slug> --project <project>...",
	Short: "Offer a read-only link to another workspace (owner only)",
	Args:  cobra.NoArgs,
	RunE:  runWorkspaceLinkCreate,
}

var workspaceLinkUpdateCmd = &cobra.Command{
	Use:   "update <link-id> [--project <project>...] [--accept]",
	Short: "Change the projects a link shares (source owner) or accept it (viewer owner/admin)",
	Args:  cobra.ExactArgs(1),
	RunE:  runWorkspaceLinkUpdate,
}

var workspaceLinkRevokeCmd = &cobra.Command{
	Use:   "revoke <link-id>",
	Short: "Disconnect a link from either side",
	Args:  cobra.ExactArgs(1),
	RunE:  runWorkspaceLinkRevoke,
}

var workspaceLinkViewCmd = &cobra.Command{
	Use:   "view <link-id>",
	Short: "Read the linked workspace's projects and task status (read-only)",
	Args:  cobra.ExactArgs(1),
	RunE:  runWorkspaceLinkView,
}

func init() {
	for _, c := range []*cobra.Command{workspaceLinkListCmd, workspaceLinkLookupCmd, workspaceLinkCreateCmd, workspaceLinkUpdateCmd, workspaceLinkRevokeCmd, workspaceLinkViewCmd} {
		c.Flags().String("output", "table", "Output format: table or json")
		workspaceLinkCmd.AddCommand(c)
	}
	workspaceLinkCreateCmd.Flags().String("to", "", "The workspace that will see the projects: its link (https://host/<slug>/...) or slug")
	workspaceLinkCreateCmd.Flags().StringArray("project", nil, "Project to share (id, id prefix or exact name); repeatable")
	workspaceLinkUpdateCmd.Flags().StringArray("project", nil, "Replace the shared projects with these; repeatable")
	workspaceLinkUpdateCmd.Flags().Bool("accept", false, "Accept a pending link offered to this workspace")
	workspaceLinkViewCmd.Flags().String("project-id", "", "Only this shared project (an id from the view's projects)")
	workspaceLinkViewCmd.Flags().String("cursor", "", "next_cursor from the previous page")
	workspaceLinkViewCmd.Flags().Int("limit", 0, "Tasks per page (max 100)")
	workspaceCmd.AddCommand(workspaceLinkCmd)
}

func runWorkspaceLinkList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var resp map[string]any
	if err := client.GetJSON(ctx, "/api/workspace-links", &resp); err != nil {
		return fmt.Errorf("list workspace links: %w", err)
	}
	if out, _ := cmd.Flags().GetString("output"); out == "json" {
		return cli.PrintJSON(os.Stdout, resp)
	}
	links, _ := resp["links"].([]any)
	rows := make([][]string, 0, len(links))
	for _, raw := range links {
		l, _ := raw.(map[string]any)
		other := "source"
		if strVal(l, "side") == "source" {
			other = "target"
		}
		peer, _ := l[other].(map[string]any)
		rows = append(rows, []string{strVal(l, "id"), strVal(l, "side"), strVal(l, "status"), strVal(peer, "name") + " (" + strVal(peer, "slug") + ")"})
	}
	cli.PrintTable(os.Stdout, []string{"ID", "SIDE", "STATUS", "OTHER WORKSPACE"}, rows)
	return nil
}

func resolveLinkProjects(ctx context.Context, client *cli.APIClient, refs []string) ([]string, error) {
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		p, err := resolveProjectID(ctx, client, ref)
		if err != nil {
			return nil, fmt.Errorf("resolve project %q: %w", ref, err)
		}
		ids = append(ids, p.ID)
	}
	return ids, nil
}

func printLink(cmd *cobra.Command, link map[string]any) error {
	if out, _ := cmd.Flags().GetString("output"); out == "json" {
		return cli.PrintJSON(os.Stdout, link)
	}
	fmt.Fprintf(os.Stdout, "%s  %s  %s\n", strVal(link, "id"), strVal(link, "side"), strVal(link, "status"))
	return nil
}

func runWorkspaceLinkLookup(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var resp map[string]any
	if err := client.GetJSON(ctx, "/api/workspace-links/lookup?target="+url.QueryEscape(args[0]), &resp); err != nil {
		return fmt.Errorf("look up workspace: %w", err)
	}
	if out, _ := cmd.Flags().GetString("output"); out == "json" {
		return cli.PrintJSON(os.Stdout, resp)
	}
	ws, _ := resp["workspace"].(map[string]any)
	fmt.Fprintf(os.Stdout, "%s (%s)\n", strVal(ws, "name"), strVal(ws, "slug"))
	return nil
}

func runWorkspaceLinkCreate(cmd *cobra.Command, _ []string) error {
	to, _ := cmd.Flags().GetString("to")
	refs, _ := cmd.Flags().GetStringArray("project")
	if to == "" || len(refs) == 0 {
		return fmt.Errorf("--to and at least one --project are required")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	ids, err := resolveLinkProjects(ctx, client, refs)
	if err != nil {
		return err
	}
	var link map[string]any
	if err := client.PostJSON(ctx, "/api/workspace-links", map[string]any{"target_slug": to, "project_ids": ids}, &link); err != nil {
		return fmt.Errorf("create workspace link: %w", err)
	}
	return printLink(cmd, link)
}

func runWorkspaceLinkUpdate(cmd *cobra.Command, args []string) error {
	refs, _ := cmd.Flags().GetStringArray("project")
	accept, _ := cmd.Flags().GetBool("accept")
	if (len(refs) > 0) == accept {
		return fmt.Errorf("pass either --project (source side) or --accept (viewer side)")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	body := map[string]any{"accept": accept}
	if len(refs) > 0 {
		ids, err := resolveLinkProjects(ctx, client, refs)
		if err != nil {
			return err
		}
		body["project_ids"] = ids
	}
	var link map[string]any
	if err := client.PatchJSON(ctx, "/api/workspace-links/"+url.PathEscape(args[0]), body, &link); err != nil {
		return fmt.Errorf("update workspace link: %w", err)
	}
	return printLink(cmd, link)
}

func runWorkspaceLinkRevoke(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	if err := client.DeleteJSON(ctx, "/api/workspace-links/"+url.PathEscape(args[0])); err != nil {
		return fmt.Errorf("revoke workspace link: %w", err)
	}
	if out, _ := cmd.Flags().GetString("output"); out == "json" {
		return cli.PrintJSON(os.Stdout, map[string]any{"id": args[0], "revoked": true})
	}
	fmt.Fprintf(os.Stdout, "Revoked link %s\n", args[0])
	return nil
}

func runWorkspaceLinkView(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	params := url.Values{}
	if v, _ := cmd.Flags().GetString("project-id"); v != "" {
		params.Set("project_id", v)
	}
	if v, _ := cmd.Flags().GetString("cursor"); v != "" {
		params.Set("cursor", v)
	}
	if v, _ := cmd.Flags().GetInt("limit"); v > 0 {
		params.Set("limit", fmt.Sprint(v))
	}
	path := "/api/workspace-links/" + url.PathEscape(args[0]) + "/view"
	if q := params.Encode(); q != "" {
		path += "?" + q
	}
	var view map[string]any
	if err := client.GetJSON(ctx, path, &view); err != nil {
		return fmt.Errorf("view workspace link: %w", err)
	}
	if out, _ := cmd.Flags().GetString("output"); out == "json" {
		return cli.PrintJSON(os.Stdout, view)
	}
	printLinkedView(os.Stdout, view)
	return nil
}

func printLinkedView(w io.Writer, view map[string]any) {
	source, _ := view["source"].(map[string]any)
	fmt.Fprintf(w, "%s (read-only)\n\nProjects\n", strVal(source, "name"))
	projects, _ := view["projects"].([]any)
	for _, raw := range projects {
		p, _ := raw.(map[string]any)
		fmt.Fprintf(w, "  %s  %s  %v/%v done  [%s]\n", strVal(p, "title"), strVal(p, "status"), p["done"], p["total"], strVal(p, "id"))
	}
	issues, _ := view["issues"].([]any)
	rows := make([][]string, 0, len(issues))
	for _, raw := range issues {
		i, _ := raw.(map[string]any)
		rows = append(rows, []string{strVal(i, "identifier"), strVal(i, "status"), strVal(i, "priority"), strVal(i, "assignee_name"), strVal(i, "title")})
	}
	fmt.Fprintln(w, "\nTasks")
	cli.PrintTable(w, []string{"ID", "STATUS", "PRIORITY", "ASSIGNEE", "TITLE"}, rows)
	if next := strVal(view, "next_cursor"); next != "" {
		fmt.Fprintf(w, "\nMore: --cursor %s\n", next)
	}
}
