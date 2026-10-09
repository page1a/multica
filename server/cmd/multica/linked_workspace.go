package main

import (
	"context"
	"fmt"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

// linkedFlag sends an issue or autopilot command to a linked source
// workspace through a managed link (DENE-1663). The server checks the link,
// the switch, the run's originator and the route on every call.
const linkedFlag = "linked"

const linkedFlagHelp = "Run this in a linked workspace (its slug or id) whose link has managed on; " +
	"agents only, acting for the person who started the run with that person's rights there (see 'multica workspace link --help')"

// addLinkedFlag offers --linked on the commands whose routes the managed
// link opens; the others would only be refused by the server.
func addLinkedFlag(cmds ...*cobra.Command) {
	for _, c := range cmds {
		c.Flags().String(linkedFlag, "", linkedFlagHelp)
	}
}

// useLinkedWorkspace points client at the source of the active, managed link
// named by ref. The lookup only turns a slug into an id and gives a readable
// error early; the server re-checks every request.
func useLinkedWorkspace(client *cli.APIClient, ref string) error {
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var resp struct {
		Links []struct {
			ID      string `json:"id"`
			Side    string `json:"side"`
			Status  string `json:"status"`
			Managed bool   `json:"managed"`
			Source  struct {
				ID   string `json:"id"`
				Slug string `json:"slug"`
			} `json:"source"`
		} `json:"links"`
	}
	if err := client.GetJSON(ctx, "/api/workspace-links", &resp); err != nil {
		return fmt.Errorf("--linked: list workspace links: %w", err)
	}
	for _, l := range resp.Links {
		if l.Side != "viewer" || l.Status != "active" || (l.Source.Slug != ref && l.Source.ID != ref) {
			continue
		}
		if !l.Managed {
			return fmt.Errorf("--linked %s: the link is read-only; its owner can turn managed on with `multica workspace link update %s --managed on`", ref, l.ID)
		}
		client.WorkspaceID = l.Source.ID
		client.LinkedWorkspace = l.Source.ID
		return nil
	}
	return fmt.Errorf("--linked %s: no active link from that workspace to this one (see `multica workspace link list`)", ref)
}
