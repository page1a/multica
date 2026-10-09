package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

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
		"offer a link; the viewer's owner or admin accepts it; either side can revoke it.\n" +
		"Someone who owns both workspaces can pull from the viewer side instead:\n" +
		"`create --from <workspace> --project <name>` links that workspace's projects in, active at once.\n" +
		"Sharing a project shares its context too: description, local directories, repositories\n" +
		"and project memory. A chat can attach it as a read-only reference.\n\n" +
		"Agents use `list` to find the active links of their workspace and `view` to read one;\n" +
		"`list --pending` shows the offers waiting for this workspace's owner or admin to accept.\n\n" +
		"Managed: when the source owner turns it on (`update <link-id> --managed on`), an\n" +
		"agent of the viewer may work on the source's issues and autopilots on behalf of the\n" +
		"person who started its run, with that person's rights there:\n" +
		"`multica issue ... --linked <source-slug>` and `multica autopilot ... --linked <source-slug>`.",
}

var workspaceLinkListCmd = &cobra.Command{
	Use:   "list",
	Short: "List links this workspace is on (members see only active incoming links; --pending: offers waiting for this workspace)",
	Args:  cobra.NoArgs,
	RunE:  runWorkspaceLinkList,
}

var workspaceLinkLookupCmd = &cobra.Command{
	Use:   "lookup <workspace-link-or-slug>",
	Short: "Show which workspace a pasted link or slug names and whether you can pull its projects (owner/admin, exact match)",
	Args:  cobra.ExactArgs(1),
	RunE:  runWorkspaceLinkLookup,
}

var workspaceLinkCreateCmd = &cobra.Command{
	Use:   "create (--to | --from) <workspace-link-or-slug> --project <project>...",
	Short: "Share this workspace's projects (--to, owner) or pull another workspace's projects in (--from, owner of both)",
	Args:  cobra.NoArgs,
	RunE:  runWorkspaceLinkCreate,
}

var workspaceLinkUpdateCmd = &cobra.Command{
	Use:   "update <link-id> [--project <project>...] [--accept] [--managed on|off]",
	Short: "Change the projects a link shares (source owner), accept it (viewer owner/admin), or turn managed on/off (source owner)",
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
	Short: "Read the linked workspace's projects — description, directories, repos, project memory — and task status (read-only)",
	Args:  cobra.ExactArgs(1),
	RunE:  runWorkspaceLinkView,
}

func init() {
	for _, c := range []*cobra.Command{workspaceLinkListCmd, workspaceLinkLookupCmd, workspaceLinkCreateCmd, workspaceLinkUpdateCmd, workspaceLinkRevokeCmd, workspaceLinkViewCmd} {
		c.Flags().String("output", "table", "Output format: table or json")
		workspaceLinkCmd.AddCommand(c)
	}
	workspaceLinkCreateCmd.Flags().String("to", "", "The workspace that will see the projects: its link (https://host/<slug>/...) or slug")
	workspaceLinkCreateCmd.Flags().String("from", "", "The workspace whose projects this workspace will see (you must own it): its link or slug")
	workspaceLinkCreateCmd.Flags().StringArray("project", nil, "Project to share (id, id prefix or exact name); with --from, a project of that workspace; repeatable")
	workspaceLinkListCmd.Flags().Bool("pending", false, "Only offers waiting for this workspace to accept (owner/admin, or their agents)")
	workspaceLinkUpdateCmd.Flags().StringArray("project", nil, "Replace the shared projects with these; repeatable")
	workspaceLinkUpdateCmd.Flags().Bool("accept", false, "Accept a pending link offered to this workspace")
	workspaceLinkUpdateCmd.Flags().String("managed", "", "on: the viewer's agents may work on this workspace's issues and autopilots for the person who started their run; off: read-only again (source owner, or owner of both from the viewer side)")
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
	if pending, _ := cmd.Flags().GetBool("pending"); pending {
		resp["links"] = waitingLinks(resp["links"])
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
		rows = append(rows, []string{strVal(l, "id"), strVal(l, "side"), strVal(l, "status"), managedLabel(l), strVal(peer, "name") + " (" + strVal(peer, "slug") + ")"})
	}
	cli.PrintTable(os.Stdout, []string{"ID", "SIDE", "STATUS", "ACCESS", "OTHER WORKSPACE"}, rows)
	return nil
}

// managedLabel names what the viewer's agents may do through the link.
func managedLabel(link map[string]any) string {
	if on, _ := link["managed"].(bool); on {
		return "managed"
	}
	return "read-only"
}

// waitingLinks keeps the offers this workspace has not answered yet.
func waitingLinks(raw any) []any {
	links, _ := raw.([]any)
	out := make([]any, 0, len(links))
	for _, l := range links {
		m, _ := l.(map[string]any)
		if strVal(m, "side") == "viewer" && strVal(m, "status") == "pending" {
			out = append(out, l)
		}
	}
	return out
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
	fmt.Fprintf(os.Stdout, "%s  %s  %s  %s\n", strVal(link, "id"), strVal(link, "side"), strVal(link, "status"), managedLabel(link))
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
	pull, _ := resp["pull"].(map[string]any)
	if allowed, _ := pull["allowed"].(bool); allowed {
		fmt.Fprintln(os.Stdout, "Projects you can pull in with `create --from`:")
		projects, _ := pull["projects"].([]any)
		for _, raw := range projects {
			p, _ := raw.(map[string]any)
			fmt.Fprintf(os.Stdout, "  %s  %s\n", strVal(p, "id"), strVal(p, "title"))
		}
	}
	return nil
}

// resolvePullProjects matches --project refs (id, id prefix or exact title)
// against the projects the lookup says can be pulled from that workspace.
func resolvePullProjects(ctx context.Context, client *cli.APIClient, from string, refs []string) ([]string, error) {
	var resp struct {
		Pull struct {
			Allowed  bool `json:"allowed"`
			Projects []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"projects"`
		} `json:"pull"`
	}
	if err := client.GetJSON(ctx, "/api/workspace-links/lookup?target="+url.QueryEscape(from), &resp); err != nil {
		return nil, fmt.Errorf("look up workspace: %w", err)
	}
	if !resp.Pull.Allowed {
		return nil, fmt.Errorf("only an owner of that workspace can pick its projects; ask its owner to run `multica workspace link create --to` from there")
	}
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		var match []string
		for _, p := range resp.Pull.Projects {
			if p.ID == ref || p.Title == ref || (len(ref) >= 4 && strings.HasPrefix(p.ID, ref)) {
				match = append(match, p.ID)
			}
		}
		if len(match) != 1 {
			return nil, fmt.Errorf("project %q matches %d shareable projects of that workspace; run `multica workspace link lookup %s` to list them", ref, len(match), from)
		}
		ids = append(ids, match[0])
	}
	return ids, nil
}

func runWorkspaceLinkCreate(cmd *cobra.Command, _ []string) error {
	to, _ := cmd.Flags().GetString("to")
	from, _ := cmd.Flags().GetString("from")
	refs, _ := cmd.Flags().GetStringArray("project")
	if (to == "") == (from == "") || len(refs) == 0 {
		return fmt.Errorf("pass exactly one of --to or --from, and at least one --project")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	body := map[string]any{"target_slug": to, "direction": "offer"}
	var ids []string
	if from != "" {
		body["target_slug"], body["direction"] = from, "pull"
		ids, err = resolvePullProjects(ctx, client, from, refs)
	} else {
		ids, err = resolveLinkProjects(ctx, client, refs)
	}
	if err != nil {
		return err
	}
	body["project_ids"] = ids
	var link map[string]any
	if err := client.PostJSON(ctx, "/api/workspace-links", body, &link); err != nil {
		return fmt.Errorf("create workspace link: %w", err)
	}
	return printLink(cmd, link)
}

func runWorkspaceLinkUpdate(cmd *cobra.Command, args []string) error {
	refs, _ := cmd.Flags().GetStringArray("project")
	accept, _ := cmd.Flags().GetBool("accept")
	managed, _ := cmd.Flags().GetString("managed")
	given := 0
	for _, set := range []bool{len(refs) > 0, accept, managed != ""} {
		if set {
			given++
		}
	}
	if given != 1 {
		return fmt.Errorf("pass exactly one of --project (source side), --accept (viewer side) or --managed on|off")
	}
	if managed != "" && managed != "on" && managed != "off" {
		return fmt.Errorf("--managed takes on or off")
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
	if managed != "" {
		body["managed"] = managed == "on"
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

// printLinkedProjectContext prints a shared project's context, indented under
// it: description, resources (read-only pointers) and the memory line.
func printLinkedProjectContext(w io.Writer, p map[string]any) {
	if desc := strings.TrimSpace(strVal(p, "description")); desc != "" {
		for _, line := range strings.Split(desc, "\n") {
			fmt.Fprintf(w, "    %s\n", line)
		}
	}
	resources, _ := p["resources"].([]any)
	for _, raw := range resources {
		r, _ := raw.(map[string]any)
		target := strVal(r, "path")
		if target == "" {
			target = strVal(r, "url")
		}
		fmt.Fprintf(w, "    - %s: %s\n", strVal(r, "type"), target)
	}
	if memory := strVal(p, "memory_line"); memory != "" {
		fmt.Fprintf(w, "    %s\n", memory)
	}
}

func printLinkedView(w io.Writer, view map[string]any) {
	source, _ := view["source"].(map[string]any)
	fmt.Fprintf(w, "%s (read-only)\n\nProjects\n", strVal(source, "name"))
	projects, _ := view["projects"].([]any)
	for _, raw := range projects {
		p, _ := raw.(map[string]any)
		fmt.Fprintf(w, "  %s  %s  %v/%v done  [%s]\n", strVal(p, "title"), strVal(p, "status"), p["done"], p["total"], strVal(p, "id"))
		printLinkedProjectContext(w, p)
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
