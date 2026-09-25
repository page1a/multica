package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var chatCmd = &cobra.Command{
	Use:   "chat",
	Short: "Read a chat conversation",
}

var chatHistoryCmd = &cobra.Command{
	Use:   "history",
	Short: "Overview of the channel this conversation is in (messages + thread list)",
	Long: `Show the overview of the chat channel (e.g. Slack) this conversation is in: the
recent top-level messages, and for each thread its thread_id, reply_count, and
latest_reply. It does NOT expand thread contents — it is the table of contents.

To read a specific thread's messages, take a thread_id from here and run
"multica chat thread <thread_id>".

It is the SAME command regardless of which channel the conversation came from.

Pass --session <url|id> to read a different chat in this workspace instead of
the one you are running in. The link looks like …/chat/<session-id> (an older
?session=<id> link works too). The default is a short summary plus the latest
messages, not the full transcript. Page older messages with --before
<next_cursor>.

When the user says "接管这个：<link>", run:
  multica chat history --session <link> --output json
You can only read a session in this workspace that this person is allowed to
open. Anyone outside the workspace gets nothing.`,
	Args: cobra.NoArgs,
	RunE: runChatHistory,
}

var chatThreadCmd = &cobra.Command{
	Use:   "thread [id]",
	Short: "Read one thread's messages (the current thread, or a specific id)",
	Long: `Read the messages of a single thread.

With no id, read the thread you are currently in (the one you were @mentioned in).
With an id — a thread_id from "multica chat history" — read that specific thread.
Either way the thread is within the channel you are in.

Pass --session <url|id> (and no thread id) to read another chat session's
transcript — the same summary-plus-latest-page read as "chat history
--session". A web chat is a single thread, so there is no separate thread id
to pass.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runChatThread,
}

func init() {
	for _, c := range []*cobra.Command{chatHistoryCmd, chatThreadCmd} {
		c.Flags().Int("limit", 0, "Maximum number of messages to return (the server clamps the range)")
		c.Flags().String("before", "", "Opaque cursor (a next_cursor from a prior page) to read older messages")
		c.Flags().String("session", "", "Read this chat instead of the current one: a session id or an internal URL (…/chat/<session-id> or ?session=<id>)")
		c.Flags().String("output", "json", "Output format: table or json")
	}
	chatCmd.AddCommand(chatHistoryCmd)
	chatCmd.AddCommand(chatThreadCmd)
}

func runChatHistory(cmd *cobra.Command, _ []string) error {
	if session, _ := cmd.Flags().GetString("session"); strings.TrimSpace(session) != "" {
		return runChatSessionHandoff(cmd, session)
	}
	resp, err := fetchChatRead(cmd, "/api/chat/history", "")
	if err != nil {
		return err
	}
	return renderChatRead(cmd, resp, true)
}

func runChatThread(cmd *cobra.Command, args []string) error {
	if session, _ := cmd.Flags().GetString("session"); strings.TrimSpace(session) != "" {
		if len(args) == 1 {
			return fmt.Errorf("--session reads that chat's transcript; a thread id only applies to the current channel. Use `multica chat history --session %s`", session)
		}
		return runChatSessionHandoff(cmd, session)
	}
	threadID := ""
	if len(args) == 1 {
		threadID = args[0]
	}
	resp, err := fetchChatRead(cmd, "/api/chat/thread", threadID)
	if err != nil {
		return err
	}
	return renderChatRead(cmd, resp, false)
}

func runChatSessionHandoff(cmd *cobra.Command, sessionRef string) error {
	sessionID, err := parseChatSessionRef(sessionRef)
	if err != nil {
		return err
	}
	resp, err := fetchChatRead(cmd, "/api/chat/sessions/"+url.PathEscape(sessionID)+"/handoff", "")
	if err != nil {
		return err
	}
	return renderChatRead(cmd, resp, false)
}

// parseChatSessionRef accepts a bare session UUID or an internal chat URL
// (`…/chat/<session-id>` or `?session=<id>`).
func parseChatSessionRef(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("missing session id or url")
	}
	if id, ok := canonicalSessionID(raw); ok {
		return id, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("could not read a chat session id from %q", raw)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i, part := range parts {
		if part == "chat" && i+1 < len(parts) {
			if id, ok := canonicalSessionID(parts[i+1]); ok {
				return id, nil
			}
		}
	}
	if id, ok := canonicalSessionID(parsed.Query().Get("session")); ok {
		return id, nil
	}
	return "", fmt.Errorf("could not read a chat session id from %q", raw)
}

func canonicalSessionID(raw string) (string, bool) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", false
	}
	return id.String(), true
}

// fetchChatRead builds the request (shared --limit/--before paging, plus the
// optional thread id) and decodes the response.
func fetchChatRead(cmd *cobra.Command, basePath, threadID string) (map[string]any, error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	limit, _ := cmd.Flags().GetInt("limit")
	before, _ := cmd.Flags().GetString("before")

	q := url.Values{}
	if threadID != "" {
		q.Set("id", threadID)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if before != "" {
		q.Set("before", before)
	}
	path := basePath
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}

	var resp map[string]any
	if err := client.GetJSON(ctx, path, &resp); err != nil {
		return nil, fmt.Errorf("read chat: %w", err)
	}
	return resp, nil
}

// renderChatRead prints the response as JSON (default) or a table. The overview
// table adds the thread columns so the agent can pick a thread_id to drill into.
func renderChatRead(cmd *cobra.Command, resp map[string]any, overview bool) error {
	output, _ := cmd.Flags().GetString("output")
	if output != "table" {
		return cli.PrintJSON(os.Stdout, resp)
	}
	if summary := strVal(resp, "summary"); summary != "" {
		fmt.Fprintln(os.Stdout, summary)
		fmt.Fprintln(os.Stdout)
	}
	if note := strVal(resp, "note"); note != "" {
		fmt.Fprintln(os.Stdout, note)
		return nil
	}
	msgs, _ := resp["messages"].([]any)
	var headers []string
	if overview {
		headers = []string{"TS", "ROLE", "AUTHOR", "THREAD_ID", "REPLIES", "TEXT"}
	} else {
		headers = []string{"TS", "ROLE", "AUTHOR", "TEXT"}
	}
	rows := make([][]string, 0, len(msgs))
	for _, mi := range msgs {
		m, ok := mi.(map[string]any)
		if !ok {
			continue
		}
		if overview {
			rows = append(rows, []string{strVal(m, "ts"), strVal(m, "role"), strVal(m, "author"), strVal(m, "thread_id"), numVal(m, "reply_count"), strVal(m, "text")})
		} else {
			rows = append(rows, []string{strVal(m, "ts"), strVal(m, "role"), strVal(m, "author"), strVal(m, "text")})
		}
	}
	cli.PrintTable(os.Stdout, headers, rows)
	if cursor := strVal(resp, "next_cursor"); cursor != "" && strVal(resp, "summary") != "" {
		fmt.Fprintf(os.Stdout, "next_cursor: %s\n", cursor)
	}
	return nil
}

// numVal renders a numeric JSON field as a string, blank when zero/absent.
func numVal(m map[string]any, key string) string {
	if v, ok := m[key].(float64); ok && v != 0 {
		return strconv.Itoa(int(v))
	}
	return ""
}
