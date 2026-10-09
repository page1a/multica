package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/service"
)

// Knowledge sediment rides the delivery (DENE-1661): the CLI reads from git
// which files a close or a chat delivers, and the server holds the knowledge
// audit against them.

// closeDeliveredFiles is the delivered_files a close sends: the paths this
// checkout's branch changes against the parent's delivery branch (a
// sub-issue) or the nearest main line. nil — sent as no list — when git
// cannot say; the server then keeps the audit marked unverified. The second
// value is the memory_files account of the same delivery (DENE-1680).
func closeDeliveredFiles(ctx context.Context, client *cli.APIClient, issueID string) (*[]string, *[]closeprotocol.MemoryFile) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, nil
	}
	var bases []string
	var d service.IssueDelivery
	if err := client.GetJSON(ctx, deliveryPath(issueID, ""), &d); err == nil && d.Line != nil {
		bases = append(bases, d.Line.Branch)
	}
	files, base, err := execenv.DeliveredFiles(dir, bases...)
	if err != nil || base == "" {
		return nil, nil
	}
	memory, err := execenv.DeliveredMemoryFiles(dir, bases...)
	if err != nil || memory == nil {
		return &files, nil
	}
	return &files, &memory
}

var chatSedimentCmd = &cobra.Command{
	Use:   "sediment",
	Short: "Record what this chat settled into the project memory, after it reached the main line",
	Long: `Record the project-memory files this chat wrote (AGENTS.md, CONTEXT.md,
docs/adr, the docs and evidence indexes) once they are on the main line.
Commit the edits on the chat's branch first, then:

  multica chat sediment --knowledge context:new=加了「沉淀记录」词条
  multica chat sediment --knowledge agents:update:工作单=... --pr https://github.com/o/r/pull/12
  multica chat sediment --knowledge agents:supersede:旧派单规则=已被「自动派票」取代
  multica chat sediment --history

A chat settling its work is the boss layer: look for the existing entry
before writing, and give each change an action — update (rewrote an entry),
merge (new entry marked to merge into an existing one), supersede (the old
entry is marked 「已被 X 取代」, not deleted), or new. Rewriting or deleting
existing lines without update/supersede is refused, and so is a map file
(AGENTS.md, CONTEXT.md, the indexes) over 32 KiB; the refusal names the
entries to merge or drop.

The main line is the project's, never the branch the project directory
happens to be on: git config multica.mainline, else the remote's default
branch, else the only one of main/master. Nothing is recorded when it cannot
be determined.

A repository with a remote counts only what the remote's main line holds:
merge a PR into it and pass --pr (the PR title should carry "Chat <id>"), or
push to it. A repository that only lives on this machine is merged into its
main line here, as a merge commit named after the chat; work already on the
main line (a shared directory) is recorded as it is. Each --knowledge
location must be written by a delivered file, or nothing is recorded.

Skip it for a chat that settled nothing: idle talk records no sediment.
`,
	Args: cobra.NoArgs,
	RunE: runChatSediment,
}

func init() {
	chatCmd.AddCommand(chatSedimentCmd)
	chatSedimentCmd.Flags().String("session", "", "Chat session id or URL (defaults to MULTICA_CHAT_SESSION_ID)")
	chatSedimentCmd.Flags().StringArray("knowledge", nil, "Memory change as location:action[:entry]=summary; action is new, update, merge or supersede; repeatable (locations: agents, context, adr, docs_index, evidence_index)")
	chatSedimentCmd.Flags().String("pr", "", "The merged PR that carried the edits (repositories with a remote)")
	chatSedimentCmd.Flags().Bool("history", false, "List this chat's earlier sediments instead of recording one")
	chatSedimentCmd.Flags().String("output", "json", "Output format: table or json")
}

func runChatSediment(cmd *cobra.Command, _ []string) error {
	session, _ := cmd.Flags().GetString("session")
	if strings.TrimSpace(session) == "" {
		session = os.Getenv("MULTICA_CHAT_SESSION_ID")
	}
	ref, err := parseChatSessionLinkRef(session)
	if err != nil {
		return fmt.Errorf("chat sediment: --session is required (or set MULTICA_CHAT_SESSION_ID): %w", err)
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	path := "/api/chat/sessions/" + url.PathEscape(ref.ID) + "/sediment"
	output, _ := cmd.Flags().GetString("output")

	if history, _ := cmd.Flags().GetBool("history"); history {
		var out struct {
			Sediments []map[string]any `json:"sediments"`
		}
		if err := client.GetJSON(ctx, path, &out); err != nil {
			return fmt.Errorf("list chat sediments: %w", err)
		}
		if output == "table" {
			for _, s := range out.Sediments {
				fmt.Printf("%v  %v  %s\n", s["created_at"], s["mainline"], sedimentFilesLine(s["changes"]))
			}
			return nil
		}
		return cli.PrintJSON(os.Stdout, out)
	}

	items, _ := cmd.Flags().GetStringArray("knowledge")
	if len(items) == 0 {
		return fmt.Errorf("chat sediment: --knowledge location=summary is required; a chat that settled nothing records no sediment")
	}
	changes := make([]closeprotocol.KnowledgeChange, 0, len(items))
	for _, item := range items {
		changes = append(changes, parseKnowledgeItem(item))
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	shortID := ref.ID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}

	body := map[string]any{"changes": changes}
	prURL, _ := cmd.Flags().GetString("pr")
	prURL = strings.TrimSpace(prURL)
	mainline := execenv.Mainline(dir)
	if mainline == "" {
		return fmt.Errorf("chat sediment: 确定不了项目主线，什么都没记：先 `git config %s <主线分支>` 再执行一次", execenv.MainlineConfigKey)
	}
	landing := closeprotocol.SedimentLanding{Remote: closeprotocol.RemoteIdentity(execenv.RemoteURL(dir))}
	switch {
	case prURL != "":
		pr, err := viewMergedPR(ctx, dir, prURL)
		if err != nil {
			return fmt.Errorf("chat sediment: %w", err)
		}
		if !strings.Contains(pr.Title, shortID) {
			fmt.Fprintf(os.Stderr, "  ! PR 标题没带聊天 id（Chat %s），以后从主线追来源会难找\n", shortID)
		}
		landing.Via, landing.PRURL, landing.PRBase, landing.PRMerged = closeprotocol.LandingPR, prURL, pr.Base, true
		body["delivered_files"] = pr.Files
		body["commits"] = []string{pr.MergeCommit}
	case landing.Remote != "":
		res, err := execenv.CheckRemoteLanding(dir, mainline, chatStartedAt(ctx, client, ref.ID))
		if err != nil {
			return fmt.Errorf("chat sediment: 核实不了远端的 %s，什么都没记：%w；推到远端 %s 后重试，或开 PR（标题带 Chat %s）合入后带 --pr 再执行一次", mainline, err, mainline, shortID)
		}
		landing.Via, landing.RemoteHasCommits = closeprotocol.LandingPush, res.Landed
		commits := make([]string, 0, len(res.Commits))
		for _, c := range res.Commits {
			commits = append(commits, c.SHA)
		}
		body["delivered_files"] = res.Files
		body["commits"] = commits
	default:
		landing.Via = closeprotocol.LandingLocal
	}
	// The server runs the same rule; checking here first keeps a local merge
	// from happening for a sediment that would be refused.
	if reason := closeprotocol.CheckSedimentLanding(landing, mainline); reason != "" {
		return fmt.Errorf("chat sediment: 没有记，%s（PR 标题带 Chat %s）", reason, shortID)
	}
	if landing.Via == closeprotocol.LandingLocal {
		// The hygiene rules run before the merge too: a merge cannot be
		// taken back once the server refuses the record.
		if pending, err := execenv.DeliveredMemoryFiles(dir, mainline); err == nil && pending != nil {
			audit, _, auditErr := closeprotocol.CanonicalKnowledgeAudit(closeprotocol.KnowledgeAudit{Changes: changes})
			if auditErr != nil {
				return fmt.Errorf("chat sediment: 没有记，%w", auditErr)
			}
			if err := closeprotocol.CheckMemoryHygiene(audit, &pending, true); err != nil {
				return fmt.Errorf("chat sediment: 没有合也没有记，%w", err)
			}
		}
		res, err := execenv.MergeIntoMainline(dir, fmt.Sprintf("Chat %s: 沉淀 %s", shortID, sedimentLocations(changes)), chatStartedAt(ctx, client, ref.ID))
		if err != nil {
			return fmt.Errorf("chat sediment: 沉淀没能合进主线，什么都没记：%w", err)
		}
		commits := make([]string, 0, len(res.Commits))
		traced := false
		for _, c := range res.Commits {
			commits = append(commits, c.SHA)
			traced = traced || strings.Contains(c.Subject, shortID)
		}
		if !traced {
			fmt.Fprintf(os.Stderr, "  ! 这些提交的说明里都没有聊天 id（Chat %s），以后从主线追来源会难找\n", shortID)
		}
		body["delivered_files"] = res.Files
		body["commits"] = commits
		mainline = res.Mainline
		if res.Source != res.Mainline {
			fmt.Fprintf(os.Stderr, "已把 %s 合进 %s\n", res.Source, res.Mainline)
		}
	}
	body["mainline"] = mainline
	body["landing"] = landing
	// What the delivered memory files look like now, for the hygiene check
	// (DENE-1680): read after any local merge, from the commits recorded.
	delivered, _ := body["delivered_files"].([]string)
	recorded, _ := body["commits"].([]string)
	memory, err := execenv.CommitMemoryFiles(dir, recorded, delivered)
	if err != nil {
		return fmt.Errorf("chat sediment: 读不出记忆文件的改动，什么都没记：%w；先 git fetch 让这些提交在本地可见再执行一次", err)
	}
	body["memory_files"] = memory

	var out map[string]any
	if err := client.PostJSON(ctx, path, body, &out); err != nil {
		return fmt.Errorf("record chat sediment: %w", err)
	}
	if output == "table" {
		fmt.Printf("已沉淀进 %v：%s\n", out["mainline"], sedimentFilesLine(out["changes"]))
		return nil
	}
	return cli.PrintJSON(os.Stdout, out)
}

// chatStartedAt bounds the commits a shared directory reports to this chat's
// lifetime. "" when the session cannot be read.
func chatStartedAt(ctx context.Context, client *cli.APIClient, sessionID string) string {
	var s struct {
		CreatedAt string `json:"created_at"`
	}
	if err := client.GetJSON(ctx, "/api/chat/sessions/"+url.PathEscape(sessionID), &s); err != nil {
		return ""
	}
	return s.CreatedAt
}

type mergedPR struct {
	Title       string
	Base        string
	MergeCommit string
	Files       []string
}

// viewMergedPR reads a PR through gh and refuses one that is not merged.
var viewMergedPR = func(ctx context.Context, dir, prURL string) (mergedPR, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", prURL, "--json", "state,title,baseRefName,mergeCommit,files")
	cmd.Dir = dir
	raw, err := cmd.Output()
	if err != nil {
		return mergedPR{}, fmt.Errorf("gh pr view %s: %w", prURL, err)
	}
	var row struct {
		State       string `json:"state"`
		Title       string `json:"title"`
		BaseRefName string `json:"baseRefName"`
		MergeCommit *struct {
			OID string `json:"oid"`
		} `json:"mergeCommit"`
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &row); err != nil {
		return mergedPR{}, fmt.Errorf("read gh pr view: %w", err)
	}
	if !strings.EqualFold(row.State, "MERGED") || row.MergeCommit == nil {
		return mergedPR{}, fmt.Errorf("PR %s 还没合入（%s）：合进主线后再上报", prURL, strings.ToLower(row.State))
	}
	pr := mergedPR{Title: row.Title, Base: row.BaseRefName, MergeCommit: row.MergeCommit.OID, Files: []string{}}
	for _, f := range row.Files {
		pr.Files = append(pr.Files, f.Path)
	}
	return pr, nil
}

func sedimentLocations(changes []closeprotocol.KnowledgeChange) string {
	names := make([]string, 0, len(changes))
	for _, c := range changes {
		names = append(names, strings.TrimSpace(c.Location))
	}
	return strings.Join(names, ", ")
}

// sedimentFilesLine renders a response's changes as "AGENTS.md, CONTEXT.md".
func sedimentFilesLine(raw any) string {
	changes, _ := raw.([]any)
	var files []string
	for _, c := range changes {
		m, _ := c.(map[string]any)
		list, _ := m["files"].([]any)
		for _, f := range list {
			files = append(files, fmt.Sprint(f))
		}
		if len(list) == 0 {
			files = append(files, fmt.Sprint(m["location"]))
		}
	}
	return strings.Join(files, ", ")
}
