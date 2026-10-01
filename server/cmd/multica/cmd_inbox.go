package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var inboxCmd = &cobra.Command{
	Use:   "inbox",
	Short: "Read the inbox",
}

var inboxBoardCmd = &cobra.Command{
	Use:   "board",
	Short: "The inbox in its six lanes: waiting on you, stalled, running, to do, new, done today",
	Long: "The same lanes the inbox page shows (DENE-975), from GET /api/inbox/board:\n\n" +
		"  waiting  — someone called this person, or a ticket stopped waiting on them\n" +
		"  stalled  — a ticket stopped without saying why\n" +
		"  running  — an agent is on it now\n" +
		"  todo     — assigned to this person, still in todo\n" +
		"  fresh    — unread activity no lane above took\n" +
		"  done     — finished today (in --tz, default this machine's zone)\n\n" +
		"Whose inbox: run by a person, their own. Run by an agent, the inbox of the\n" +
		"person who started this run by hand (a chat message, comment, @ or assignment); a run\n" +
		"an automation or another agent started is refused, since no person is behind it.\n\n" +
		"--project <id, id prefix or name> narrows the board to that project's tickets\n" +
		"(DENE-1019), the same set the page shows once a project is picked. A project the\n" +
		"person cannot see gives an empty board.\n\n" +
		"Read-only: it never marks anything read, so the page still shows what is new.\n" +
		"To act on a row, reply on that ticket with `multica issue comment add`.",
	Args: cobra.NoArgs,
	RunE: runInboxBoard,
}

func init() {
	inboxCmd.AddCommand(inboxBoardCmd)
	inboxBoardCmd.Flags().String("tz", "", "IANA time zone \"done today\" is counted in (default: this machine's)")
	inboxBoardCmd.Flags().String("project", "", "Only this project's tickets: project id, id prefix or exact name")
	inboxBoardCmd.Flags().String("output", "table", "Output format: table or json")
}

// localTZName is this machine's IANA zone name: $TZ, else the /etc/localtime
// link, else UTC. time.Local only knows itself as "Local".
func localTZName() string {
	if tz := strings.TrimPrefix(os.Getenv("TZ"), ":"); tz != "" {
		if _, err := time.LoadLocation(tz); err == nil {
			return tz
		}
	}
	if target, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if i := strings.Index(target, "zoneinfo/"); i >= 0 {
			name := target[i+len("zoneinfo/"):]
			if _, err := time.LoadLocation(name); err == nil {
				return name
			}
		}
	}
	return "UTC"
}

type inboxBoardOwner struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type inboxBoardRow struct {
	Identifier string           `json:"identifier"`
	Title      string           `json:"title"`
	Kind       string           `json:"kind"`
	Reason     string           `json:"reason"`
	Before     string           `json:"before"`
	FromName   string           `json:"from_name"`
	Next       *inboxBoardOwner `json:"next"`
	NextName   string           `json:"next_name"`
	At         string           `json:"at"`
	Unread     int64            `json:"unread"`
	Children   []inboxBoardRow  `json:"children"`
}

type inboxBoardView struct {
	Waiting  []inboxBoardRow `json:"waiting"`
	Stalled  []inboxBoardRow `json:"stalled"`
	Running  []inboxBoardRow `json:"running"`
	Todo     []inboxBoardRow `json:"todo"`
	Fresh    []inboxBoardRow `json:"fresh"`
	Done     []inboxBoardRow `json:"done"`
	ViewerID string          `json:"viewer_id"`
	TZ       string          `json:"tz"`
}

func runInboxBoard(cmd *cobra.Command, _ []string) error {
	tz, _ := cmd.Flags().GetString("tz")
	if strings.TrimSpace(tz) == "" {
		tz = localTZName()
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	path := "/api/inbox/board?tz=" + url.QueryEscape(tz)
	if ref, _ := cmd.Flags().GetString("project"); strings.TrimSpace(ref) != "" {
		projectID, err := resolveInboxProject(ctx, client, ref)
		if err != nil {
			return fmt.Errorf("inbox board: %w", err)
		}
		path += "&project_id=" + url.QueryEscape(projectID)
	}
	if format, _ := cmd.Flags().GetString("output"); format == "json" {
		var out map[string]any
		if err := client.GetJSON(ctx, path, &out); err != nil {
			return fmt.Errorf("inbox board: %w", err)
		}
		return cli.PrintJSON(os.Stdout, out)
	}
	var board inboxBoardView
	if err := client.GetJSON(ctx, path, &board); err != nil {
		return fmt.Errorf("inbox board: %w", err)
	}
	printInboxBoard(os.Stdout, board)
	return nil
}

// resolveInboxProject turns --project into a project id: a full id as is,
// else an exact (case-insensitive) name, else an id prefix.
func resolveInboxProject(ctx context.Context, client *cli.APIClient, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if uuidRegexp.MatchString(ref) {
		return ref, nil
	}
	candidates, err := fetchProjectCandidates(ctx, client)
	if err != nil {
		return "", fmt.Errorf("resolve project: %w", err)
	}
	var named []idCandidate
	for _, c := range candidates {
		if strings.EqualFold(strings.TrimSpace(c.Display), ref) {
			named = append(named, c)
		}
	}
	switch len(named) {
	case 1:
		return named[0].ID, nil
	case 0:
		found, err := resolveIDByPrefix(ctx, client, "project", ref, func(context.Context, *cli.APIClient) ([]idCandidate, error) {
			return candidates, nil
		})
		if err != nil {
			if strings.Contains(err.Error(), "ambiguous") {
				return "", err
			}
			return "", fmt.Errorf("no project named %q, and it is not an id prefix of one", ref)
		}
		return found.ID, nil
	default:
		return "", ambiguousIDPrefixError("project name", ref, named)
	}
}

func printInboxBoard(w io.Writer, b inboxBoardView) {
	who := func(r inboxBoardRow) string {
		if r.NextName != "" {
			return r.NextName
		}
		if r.Next != nil {
			return r.Next.Type + " " + r.Next.ID
		}
		return "nobody named"
	}
	line := func(indent string, r inboxBoardRow) {
		fmt.Fprintf(w, "%s%s  %s", indent, r.Identifier, r.Title)
		if r.Unread > 0 {
			fmt.Fprintf(w, "  [%d unread]", r.Unread)
		}
		fmt.Fprintln(w)
	}
	detail := func(indent, label, text string) {
		if text = strings.TrimSpace(text); text != "" {
			fmt.Fprintf(w, "%s  %s: %s\n", indent, label, text)
		}
	}
	var section func(indent string, rows []inboxBoardRow, body func(string, inboxBoardRow))
	section = func(indent string, rows []inboxBoardRow, body func(string, inboxBoardRow)) {
		for _, r := range rows {
			line(indent, r)
			body(indent, r)
			section(indent+"    ", r.Children, body)
		}
	}
	lane := func(title string, rows []inboxBoardRow) {
		fmt.Fprintf(w, "\n%s (%d)\n", title, len(rows))
		if len(rows) == 0 {
			fmt.Fprintln(w, "  —")
		}
	}

	fmt.Fprintf(w, "Inbox of %s (done today counted in %s)\n", b.ViewerID, b.TZ)

	lane("Waiting on you", b.Waiting)
	section("  ", b.Waiting, func(indent string, r inboxBoardRow) {
		detail(indent, "asks", r.Reason)
		detail(indent, "called by", r.FromName)
		detail(indent, "before", r.Before)
	})
	lane("Stalled, no explanation", b.Stalled)
	section("  ", b.Stalled, func(indent string, r inboxBoardRow) {
		detail(indent, "stopped as", r.Kind)
		detail(indent, "before", r.Before)
		detail(indent, "next", who(r))
	})
	lane("Running", b.Running)
	section("  ", b.Running, func(indent string, r inboxBoardRow) { detail(indent, "on it", who(r)) })
	lane("To do", b.Todo)
	section("  ", b.Todo, func(string, inboxBoardRow) {})
	lane("New activity", b.Fresh)
	section("  ", b.Fresh, func(string, inboxBoardRow) {})
	lane("Done today", b.Done)
	section("  ", b.Done, func(string, inboxBoardRow) {})
}
