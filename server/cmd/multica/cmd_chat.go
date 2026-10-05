package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	Short: "Read a chat conversation, or send to one",
}

var chatListCmd = &cobra.Command{
	Use:   "list",
	Short: "List chats visible to the task initiator",
	Args:  cobra.NoArgs,
	RunE:  runChatDirectoryList,
}

var chatSearchCmd = &cobra.Command{
	Use:   "search <词>",
	Short: "Search visible chat titles and messages",
	Args:  cobra.ExactArgs(1),
	RunE:  runChatDirectorySearch,
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

var chatProgressCmd = &cobra.Command{
	Use:   "progress [text]",
	Short: "Report or read the progress line under a chat's title",
	Long: `Report where this chat stands — the second line under its title in the
chat list and header. Your own line wins over the automatic summary the
platform writes after each reply.

  multica chat progress "Fixed the redirect; waiting for you to retest"
  multica chat progress "Need the API key to continue" --tone waiting
  multica chat progress --history

--tone sets the dot colour: working (blue, default), waiting (yellow),
stuck (red), done (green). --history lists earlier lines, newest first.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runChatProgress,
}

var chatToGoalCmd = &cobra.Command{
	Use:   "to-goal",
	Short: "Turn the current chat into a goal task",
	Args:  cobra.NoArgs,
	RunE:  runChatToGoal,
}

var chatTitleCmd = &cobra.Command{
	Use:   "title <Project · topic>",
	Short: "Report the title for the current chat",
	Long: `Report a chat title from the agent runtime. The server validates the
Project · topic shape and refuses to overwrite a title a member has renamed.

  multica chat title "Billing · retry invoices"
`,
	Args: cobra.ExactArgs(1),
	RunE: runChatTitle,
}

var chatOpenCmd = &cobra.Command{
	Use:   "open",
	Short: "Open a new chat with another agent from the current chat",
	Long: `Open a new chat with an agent and send it a brief as the first message.
The server creates the chat, records which chat it came from, and posts a
card in the current chat linking to it, all in one step.

Only a chat run can open a chat (a task run gets chat_spawn_task_mode: use
"multica issue create" instead). A chat opened this way cannot open another
one, and its visibility and project can only be narrower than the current
chat. The workspace caps how many chats each chat and each run may open.

Refusals come back as JSON with a code:
  agent_spawn_disabled, agent_spawn_budget_exceeded, chat_spawn_depth_exceeded,
  chat_spawn_task_mode, chat_spawn_scope_exceeded, chat_spawn_agent_only

Retrying with the same --client-key (default: derived from agent, title and
brief) returns the chat already opened instead of a second one.

  multica chat open --agent 布尔玛 --brief-file ./brief.md --title "Billing · retry design"
`,
	Args: cobra.NoArgs,
	RunE: runChatOpen,
}

var chatSendCmd = &cobra.Command{
	Use:   "send [text]",
	Short: "Send a message to a chat, steering, queueing or restarting its reply",
	Long: `Send a message to a chat as if typed in its composer. --mode decides what
happens when the agent is still replying (ignored when it is not):

  steer    read it after the current step, keep working on the original ask
           (same process, same session on Claude, Codex, Grok, OpenCode 1.x
           and Pi; Cursor, Copilot, CodeArts, DevEco, Antigravity and OpenClaw
           restart the CLI on the same session to read it right away)
  queue    answer it after this reply finishes (new process, same session;
           default)
  restart  stop the reply now and start over from this message (new process,
           same session; the half-done step is dropped)

When the running CLI cannot steer, the send is refused with the reason and
the modes that work; nothing is sent.

  multica chat send --session <id|url> "Use the staging database instead" --mode steer
  multica chat send --session <id|url> --content-file ./msg.md --mode queue
`,
	Args: cobra.MaximumNArgs(1),
	RunE: runChatSend,
}

var chatHandoffCmd = &cobra.Command{
	Use:   "handoff",
	Short: "Hand a chat to another agent: a new chat that opens with this one's summary",
	Long: `Hand a chat to another agent. The server opens a new chat with --to whose
first message summarises this one (title, opening line, latest messages, and
how to read the full history), and starts that agent's reply. The old chat is
left as it is. Only the chat's owner can hand it over.

  multica chat handoff --to 孙悟饭
  multica chat handoff --session <id|url> --to <agent-name|id> --output table
`,
	Args: cobra.NoArgs,
	RunE: runChatHandoff,
}

func init() {
	for _, c := range []*cobra.Command{chatListCmd, chatSearchCmd} {
		c.Flags().String("project", "", "Filter to a project id (defaults to the current project)")
		c.Flags().Bool("all-projects", false, "Search across every project visible to the task initiator")
		c.Flags().String("since", "", "Only chats active since an RFC3339 timestamp")
		c.Flags().String("output", "json", "Output format: table or json")
	}
	chatCmd.AddCommand(chatListCmd)
	chatCmd.AddCommand(chatSearchCmd)
	for _, c := range []*cobra.Command{chatHistoryCmd, chatThreadCmd} {
		c.Flags().Int("limit", 0, "Maximum number of messages to return (the server clamps the range)")
		c.Flags().String("before", "", "Opaque cursor (a next_cursor from a prior page) to read older messages")
		c.Flags().String("session", "", "Read this chat instead of the current one: a session id or an internal URL (…/chat/<session-id> or ?session=<id>)")
		c.Flags().String("output", "json", "Output format: table or json")
	}
	chatCmd.AddCommand(chatHistoryCmd)
	chatCmd.AddCommand(chatThreadCmd)
	chatCmd.AddCommand(chatProgressCmd)
	chatCmd.AddCommand(chatToGoalCmd)
	chatToGoalCmd.Flags().String("session", "", "Chat session id or URL (defaults to MULTICA_CHAT_SESSION_ID)")
	chatToGoalCmd.Flags().String("output", "json", "Output format: table or json")
	chatCmd.AddCommand(chatTitleCmd)
	chatCmd.AddCommand(chatOpenCmd)
	chatCmd.AddCommand(chatSendCmd)
	chatCmd.AddCommand(chatHandoffCmd)
	chatHandoffCmd.Flags().String("session", "", "Chat session id or URL (defaults to MULTICA_CHAT_SESSION_ID)")
	chatHandoffCmd.Flags().String("to", "", "Agent to hand the chat to: name or id (required)")
	chatHandoffCmd.Flags().String("output", "json", "Output format: table or json")
	chatSendCmd.Flags().String("session", "", "Chat session id or URL (required)")
	chatSendCmd.Flags().String("content-file", "", "Read the message from this file instead of the argument")
	chatSendCmd.Flags().String("mode", "queue", "While the agent is replying: steer, queue or restart")
	chatSendCmd.Flags().String("output", "json", "Output format: table or json")
	chatOpenCmd.Flags().String("agent", "", "Agent to talk to: name or id (required)")
	chatOpenCmd.Flags().String("brief-file", "", "File holding the first message for the agent (required)")
	chatOpenCmd.Flags().String("title", "", "Chat title, Project · topic")
	chatOpenCmd.Flags().String("project", "", "Project id or prefix (must be the current chat's project, or narrower)")
	chatOpenCmd.Flags().String("visibility", "", "private or workspace (cannot be wider than the current chat)")
	chatOpenCmd.Flags().String("client-key", "", "Idempotency key; defaults to a hash of agent, title and brief")
	chatOpenCmd.Flags().String("output", "json", "Output format: table or json")
	chatProgressCmd.Flags().String("session", "", "Chat session id or URL (defaults to MULTICA_CHAT_SESSION_ID)")
	chatProgressCmd.Flags().String("output", "json", "Output format: table or json")
	chatProgressCmd.Flags().String("tone", "", "Dot colour: working, waiting, stuck, or done (default working)")
	chatProgressCmd.Flags().Bool("history", false, "List earlier progress lines instead of reporting one")
	chatTitleCmd.Flags().String("session", "", "Chat session id or URL (defaults to MULTICA_CHAT_SESSION_ID)")
	chatTitleCmd.Flags().String("output", "json", "Output format: table or json")
}

func runChatTitle(cmd *cobra.Command, args []string) error {
	session, _ := cmd.Flags().GetString("session")
	if strings.TrimSpace(session) == "" {
		session = os.Getenv("MULTICA_CHAT_SESSION_ID")
	}
	ref, err := parseChatSessionLinkRef(session)
	if err != nil {
		return fmt.Errorf("chat title: --session is required (or set MULTICA_CHAT_SESSION_ID): %w", err)
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var out map[string]any
	path := "/api/chat/sessions/" + url.PathEscape(ref.ID) + "/title"
	if err := client.PostJSON(ctx, path, map[string]any{"title": args[0]}, &out); err != nil {
		return fmt.Errorf("report chat title: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		fmt.Printf("Title: %v\n", out["title"])
		return nil
	}
	return cli.PrintJSON(os.Stdout, out)
}

func runChatToGoal(cmd *cobra.Command, _ []string) error {
	session := os.Getenv("MULTICA_CHAT_SESSION_ID")
	if raw, _ := cmd.Flags().GetString("session"); strings.TrimSpace(raw) != "" {
		session = raw
	}
	ref, err := parseChatSessionLinkRef(session)
	if err != nil {
		return fmt.Errorf("chat to-goal: session is required (or set MULTICA_CHAT_SESSION_ID): %w", err)
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var out map[string]any
	if err := client.PostJSON(ctx, "/api/chat/sessions/"+url.PathEscape(ref.ID)+"/to-goal", nil, &out); err != nil {
		return fmt.Errorf("convert chat to goal: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		issue, _ := out["issue"].(map[string]any)
		fmt.Printf("Goal task: %v\n", issue["identifier"])
		return nil
	}
	return cli.PrintJSON(os.Stdout, out)
}

func runChatProgress(cmd *cobra.Command, args []string) error {
	session, _ := cmd.Flags().GetString("session")
	if strings.TrimSpace(session) == "" {
		session = os.Getenv("MULTICA_CHAT_SESSION_ID")
	}
	ref, err := parseChatSessionLinkRef(session)
	if err != nil {
		return fmt.Errorf("chat progress: --session is required (or set MULTICA_CHAT_SESSION_ID): %w", err)
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	return reportOrListProgress(ctx, cmd, client, "/api/chat/sessions/"+url.PathEscape(ref.ID)+"/progress", args, "chat")
}

// reportOrListProgress is shared by `multica chat progress` and `multica
// issue progress`: POST a line, or GET the history with --history.
func reportOrListProgress(ctx context.Context, cmd *cobra.Command, client *cli.APIClient, path string, args []string, noun string) error {
	output, _ := cmd.Flags().GetString("output")
	if history, _ := cmd.Flags().GetBool("history"); history {
		if len(args) > 0 {
			return fmt.Errorf("%s progress: --history takes no text", noun)
		}
		var out struct {
			Progress []map[string]any `json:"progress"`
		}
		if err := client.GetJSON(ctx, path, &out); err != nil {
			return fmt.Errorf("list %s progress: %w", noun, err)
		}
		if output == "table" {
			for _, p := range out.Progress {
				fmt.Printf("%v  %-7v %-7v %v\n", p["updated_at"], p["tone"], p["source"], p["text"])
			}
			return nil
		}
		return cli.PrintJSON(os.Stdout, out)
	}
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return fmt.Errorf("%s progress: text is required (or pass --history to read)", noun)
	}
	body := map[string]any{"text": args[0]}
	if tone, _ := cmd.Flags().GetString("tone"); tone != "" {
		body["tone"] = tone
	}
	var out map[string]any
	if err := client.PostJSON(ctx, path, body, &out); err != nil {
		return fmt.Errorf("report %s progress: %w", noun, err)
	}
	if output == "table" {
		fmt.Printf("Progress: %v\n", out["progress"])
		return nil
	}
	return cli.PrintJSON(os.Stdout, out)
}

func runChatDirectoryList(cmd *cobra.Command, _ []string) error {
	return runChatDirectory(cmd, "")
}

func runChatDirectorySearch(cmd *cobra.Command, args []string) error {
	return runChatDirectory(cmd, args[0])
}

func runChatDirectory(cmd *cobra.Command, keyword string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	q := url.Values{}
	if project, _ := cmd.Flags().GetString("project"); strings.TrimSpace(project) != "" {
		q.Set("project", strings.TrimSpace(project))
	}
	if all, _ := cmd.Flags().GetBool("all-projects"); all {
		q.Set("all_projects", "true")
	}
	if since, _ := cmd.Flags().GetString("since"); strings.TrimSpace(since) != "" {
		q.Set("since", strings.TrimSpace(since))
	}
	if strings.TrimSpace(keyword) != "" {
		q.Set("q", keyword)
	}
	path := "/api/chat/directory"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var rows []map[string]any
	if err := client.GetJSON(ctx, path, &rows); err != nil {
		return fmt.Errorf("list chats: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output != "table" {
		return cli.PrintJSON(os.Stdout, rows)
	}
	headers := []string{"TITLE", "PROJECT", "AGENT", "ORIGINATOR", "LAST_ACTIVE", "MESSAGES", "SUMMARY"}
	tableRows := make([][]string, 0, len(rows))
	for _, row := range rows {
		tableRows = append(tableRows, []string{
			strVal(row, "title"), strVal(row, "project_title"), strVal(row, "agent_name"),
			strVal(row, "originator"), strVal(row, "last_active_at"), numVal(row, "message_count"), strVal(row, "summary"),
		})
	}
	cli.PrintTable(os.Stdout, headers, tableRows)
	return nil
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
	ref, err := parseChatSessionLinkRef(sessionRef)
	if err != nil {
		return err
	}
	path := "/api/chat/sessions/" + url.PathEscape(ref.ID) + "/handoff"
	if ref.Slug != "" {
		path = "/api/chat/links/" + url.PathEscape(ref.Slug) + "/sessions/" + url.PathEscape(ref.ID) + "/handoff"
	}
	resp, err := fetchChatRead(cmd, path, "")
	if err != nil {
		return err
	}
	return renderChatRead(cmd, resp, false)
}

// parseChatSessionRef accepts a bare session UUID or an internal chat URL
// (`…/chat/<session-id>` or `?session=<id>`).
type chatSessionRef struct{ ID, Slug string }

func parseChatSessionRef(raw string) (string, error) {
	ref, err := parseChatSessionLinkRef(raw)
	if err != nil {
		return "", err
	}
	return ref.ID, nil
}

func parseChatSessionLinkRef(raw string) (chatSessionRef, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return chatSessionRef{}, fmt.Errorf("missing session id or url")
	}
	if id, ok := canonicalSessionID(raw); ok {
		return chatSessionRef{ID: id}, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return chatSessionRef{}, fmt.Errorf("could not read a chat session id from %q", raw)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i, part := range parts {
		if part == "chat" && i+1 < len(parts) {
			if id, ok := canonicalSessionID(parts[i+1]); ok {
				slug := ""
				if i > 0 {
					slug = parts[i-1]
				}
				return chatSessionRef{ID: id, Slug: slug}, nil
			}
		}
	}
	if id, ok := canonicalSessionID(parsed.Query().Get("session")); ok {
		return chatSessionRef{ID: id}, nil
	}
	return chatSessionRef{}, fmt.Errorf("could not read a chat session id from %q", raw)
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

func runChatOpen(cmd *cobra.Command, _ []string) error {
	agentRef, _ := cmd.Flags().GetString("agent")
	briefPath, _ := cmd.Flags().GetString("brief-file")
	if strings.TrimSpace(agentRef) == "" || strings.TrimSpace(briefPath) == "" {
		return fmt.Errorf("chat open: --agent and --brief-file are required")
	}
	raw, err := os.ReadFile(briefPath)
	if err != nil {
		return fmt.Errorf("chat open: read brief: %w", err)
	}
	brief := strings.TrimSpace(string(raw))
	if brief == "" {
		return fmt.Errorf("chat open: brief file is empty")
	}
	title, _ := cmd.Flags().GetString("title")
	visibility, _ := cmd.Flags().GetString("visibility")
	project, _ := cmd.Flags().GetString("project")
	clientKey, _ := cmd.Flags().GetString("client-key")

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	agentID, err := resolveAgent(ctx, client, agentRef)
	if err != nil {
		return fmt.Errorf("chat open: %w", err)
	}
	if strings.TrimSpace(clientKey) == "" {
		sum := sha256.Sum256([]byte(agentID + "\x00" + title + "\x00" + brief))
		clientKey = hex.EncodeToString(sum[:16])
	}
	body := map[string]any{
		"agent_id":   agentID,
		"brief":      brief,
		"client_key": clientKey,
	}
	if title != "" {
		body["title"] = title
	}
	if visibility != "" {
		body["visibility"] = visibility
	}
	if project != "" {
		p, err := resolveProjectID(ctx, client, project)
		if err != nil {
			return fmt.Errorf("chat open: %w", err)
		}
		body["project_id"] = p.ID
	}

	output, _ := cmd.Flags().GetString("output")
	var out map[string]any
	if err := client.PostJSON(ctx, "/api/chat/sessions/spawn", body, &out); err != nil {
		var httpErr *cli.HTTPError
		var refusal map[string]any
		if errors.As(err, &httpErr) && json.Unmarshal([]byte(httpErr.Body), &refusal) == nil && refusal["code"] != nil {
			if output != "table" {
				_ = cli.PrintJSON(os.Stdout, refusal)
			}
			return fmt.Errorf("chat open refused (%v): %v", refusal["code"], refusal["error"])
		}
		return fmt.Errorf("open chat: %w", err)
	}
	if output == "table" {
		session, _ := out["session"].(map[string]any)
		fmt.Printf("Chat: %v (%v)\n", session["title"], session["id"])
		return nil
	}
	return cli.PrintJSON(os.Stdout, out)
}

func runChatSend(cmd *cobra.Command, args []string) error {
	// No MULTICA_CHAT_SESSION_ID fallback: a chat run sending to the chat it
	// is answering would queue a turn for itself.
	session, _ := cmd.Flags().GetString("session")
	ref, err := parseChatSessionLinkRef(session)
	if err != nil {
		return fmt.Errorf("chat send: --session is required: %w", err)
	}
	mode, _ := cmd.Flags().GetString("mode")
	switch mode {
	case "steer", "queue", "restart":
	default:
		return fmt.Errorf("chat send: --mode must be steer, queue or restart")
	}
	content := ""
	if len(args) == 1 {
		content = args[0]
	}
	if path, _ := cmd.Flags().GetString("content-file"); path != "" {
		if content != "" {
			return fmt.Errorf("chat send: pass the message as an argument or --content-file, not both")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("chat send: read content file: %w", err)
		}
		content = string(raw)
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("chat send: message is empty")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	output, _ := cmd.Flags().GetString("output")
	var out map[string]any
	path := "/api/chat/sessions/" + url.PathEscape(ref.ID) + "/messages"
	if err := client.PostJSON(ctx, path, map[string]any{"content": content, "mode": mode}, &out); err != nil {
		return sendModeRefusal(err, output, "chat send")
	}
	if output == "table" {
		fmt.Printf("Sent (%v): message %v\n", out["mode"], out["message_id"])
		return nil
	}
	return cli.PrintJSON(os.Stdout, out)
}

// sendModeRefusal turns a steer refusal into the reason plus the modes that
// still work, shared by chat send and issue comment add.
func sendModeRefusal(err error, output, verb string) error {
	var httpErr *cli.HTTPError
	var refusal struct {
		Error          string   `json:"error"`
		Code           string   `json:"code"`
		AvailableModes []string `json:"available_modes"`
	}
	if errors.As(err, &httpErr) && json.Unmarshal([]byte(httpErr.Body), &refusal) == nil && refusal.Code == "steer_unsupported" {
		if output != "table" {
			_ = cli.PrintJSON(os.Stdout, refusal)
		}
		return fmt.Errorf("%s: cannot steer: %s (use --mode %s)", verb, refusal.Error, strings.Join(refusal.AvailableModes, " or --mode "))
	}
	return fmt.Errorf("%s: %w", verb, err)
}

func runChatHandoff(cmd *cobra.Command, _ []string) error {
	session := os.Getenv("MULTICA_CHAT_SESSION_ID")
	if raw, _ := cmd.Flags().GetString("session"); strings.TrimSpace(raw) != "" {
		session = raw
	}
	ref, err := parseChatSessionLinkRef(session)
	if err != nil {
		return fmt.Errorf("chat handoff: session is required (or set MULTICA_CHAT_SESSION_ID): %w", err)
	}
	to, _ := cmd.Flags().GetString("to")
	if strings.TrimSpace(to) == "" {
		return fmt.Errorf("chat handoff: --to is required")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var out map[string]any
	if err := client.PostJSON(ctx, "/api/chat/sessions/"+url.PathEscape(ref.ID)+"/handoff", map[string]any{"to": to}, &out); err != nil {
		return fmt.Errorf("hand chat over: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		s, _ := out["session"].(map[string]any)
		fmt.Printf("New chat: %v (%v)\n", s["title"], s["id"])
		return nil
	}
	return cli.PrintJSON(os.Stdout, out)
}
