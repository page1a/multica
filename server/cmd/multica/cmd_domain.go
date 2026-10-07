package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// ---------------------------------------------------------------------------
// Domain commands — the workspace domain list (DENE-1451). Projects carry
// domains, an issue picks one of its project's, and specialisations are
// keyed by base role + domain; all of them pick from this list.
// ---------------------------------------------------------------------------

var domainCmd = &cobra.Command{
	Use:   "domain",
	Short: "Work with the workspace domain list",
	Long: `Projects carry domains (multica project update --domain), an issue picks one
of its project's domains (multica issue update --domain), and a specialisation
is a base role plus one domain (multica agent create --base-role --domain).
Routing sends an issue to the specialisation for its domain; an issue with no
domain is generic and goes to the base role.`,
}

var domainListCmd = &cobra.Command{
	Use:   "list",
	Short: "List domains with how many projects and specialisations use each",
	RunE:  runDomainList,
}

var domainAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add a domain",
	Args:  exactArgs(1),
	RunE:  runDomainAdd,
}

var domainRenameCmd = &cobra.Command{
	Use:   "rename <name-or-id> <new-name>",
	Short: "Rename a domain (projects and specialisations keep it)",
	Args:  exactArgs(2),
	RunE:  runDomainRename,
}

var domainDeleteCmd = &cobra.Command{
	Use:   "delete <name-or-id>",
	Short: "Delete a domain no project or specialisation uses",
	Args:  exactArgs(1),
	RunE:  runDomainDelete,
}

func init() {
	domainCmd.AddCommand(domainListCmd)
	domainCmd.AddCommand(domainAddCmd)
	domainCmd.AddCommand(domainRenameCmd)
	domainCmd.AddCommand(domainDeleteCmd)

	domainListCmd.Flags().String("output", "table", "Output format: table or json")
	domainListCmd.Flags().Bool("full-id", false, "Show full UUIDs in table output")
	domainAddCmd.Flags().String("output", "json", "Output format: table or json")
	domainRenameCmd.Flags().String("output", "json", "Output format: table or json")
}

func domainPath(client *cli.APIClient, suffix string) string {
	path := "/api/domains" + suffix
	if client.WorkspaceID != "" {
		path += "?" + url.Values{"workspace_id": {client.WorkspaceID}}.Encode()
	}
	return path
}

func listDomains(ctx context.Context, client *cli.APIClient) ([]map[string]any, error) {
	var result struct {
		Domains []map[string]any `json:"domains"`
	}
	if err := client.GetJSON(ctx, domainPath(client, ""), &result); err != nil {
		return nil, fmt.Errorf("list domains: %w", err)
	}
	return result.Domains, nil
}

// resolveDomainID accepts a domain name or id.
func resolveDomainID(ctx context.Context, client *cli.APIClient, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	domains, err := listDomains(ctx, client)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(domains))
	for _, d := range domains {
		if strVal(d, "id") == ref || strVal(d, "name") == ref {
			return strVal(d, "id"), nil
		}
		names = append(names, strVal(d, "name"))
	}
	return "", fmt.Errorf("no domain %q (domains: %s)", ref, strings.Join(names, ", "))
}

func countVal(m map[string]any, key string) int {
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	return 0
}

func runDomainList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	domains, err := listDomains(ctx, client)
	if err != nil {
		return err
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, domains)
	}
	fullID, _ := cmd.Flags().GetBool("full-id")
	rows := make([][]string, 0, len(domains))
	for _, d := range domains {
		rows = append(rows, []string{
			displayID(strVal(d, "id"), fullID),
			strVal(d, "name"),
			strconv.Itoa(countVal(d, "project_count")),
			strconv.Itoa(countVal(d, "agent_count")),
		})
	}
	cli.PrintTable(os.Stdout, []string{"ID", "NAME", "PROJECTS", "SPECIALISATIONS"}, rows)
	return nil
}

func printDomain(cmd *cobra.Command, d map[string]any) error {
	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		cli.PrintTable(os.Stdout, []string{"ID", "NAME"}, [][]string{{strVal(d, "id"), strVal(d, "name")}})
		return nil
	}
	return cli.PrintJSON(os.Stdout, d)
}

func runDomainAdd(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var created map[string]any
	if err := client.PostJSON(ctx, domainPath(client, ""), map[string]any{"name": args[0]}, &created); err != nil {
		return fmt.Errorf("add domain: %w", err)
	}
	return printDomain(cmd, created)
}

func runDomainRename(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	id, err := resolveDomainID(ctx, client, args[0])
	if err != nil {
		return err
	}
	var renamed map[string]any
	if err := client.PatchJSON(ctx, domainPath(client, "/"+id), map[string]any{"name": args[1]}, &renamed); err != nil {
		return fmt.Errorf("rename domain: %w", err)
	}
	return printDomain(cmd, renamed)
}

func runDomainDelete(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	id, err := resolveDomainID(ctx, client, args[0])
	if err != nil {
		return err
	}
	if err := client.DeleteJSON(ctx, domainPath(client, "/"+id)); err != nil {
		return fmt.Errorf("delete domain: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Domain %s deleted.\n", args[0])
	return nil
}
