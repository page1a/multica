package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/progress"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// A knowledge sediment is one delivery that wrote project memory (DENE-1661):
// an executor's close whose audit named locations, or a chat that finished
// with `multica chat sediment`. The work's own agent writes the map files and
// ships them with the work; this row is the trail a person reads on the
// project page and an agent reads from `multica project memory status`.

const recentSedimentLimit = 8

// KnowledgeSedimentResponse is the shape shared by the project memory status
// and the chat sediment endpoints.
type KnowledgeSedimentResponse struct {
	ID              string                          `json:"id"`
	SourceKind      string                          `json:"source_kind"` // issue or chat
	IssueID         *string                         `json:"issue_id"`
	IssueIdentifier *string                         `json:"issue_identifier"`
	ChatSessionID   *string                         `json:"chat_session_id"`
	SourceTitle     string                          `json:"source_title"`
	Changes         []closeprotocol.KnowledgeChange `json:"changes"`
	Verified        bool                            `json:"verified"`
	Mainline        string                          `json:"mainline"`
	Commits         []string                        `json:"commits"`
	PRURL           string                          `json:"pr_url"`
	AuthorType      string                          `json:"author_type"`
	AuthorID        string                          `json:"author_id"`
	CreatedAt       string                          `json:"created_at"`
	// Layer is worker (an executor's close) or boss (a round opened by a
	// parent ticket or a project, or a chat settling its work; DENE-1680).
	// Sources are what a boss-layer round summed up.
	Layer   string                   `json:"layer"`
	Sources []SedimentSourceResponse `json:"sources"`
}

// The two sediment layers (DENE-1680).
const (
	sedimentLayerWorker = "worker"
	sedimentLayerBoss   = "boss"
)

// sedimentSourcesKey is the round ticket's metadata key listing what opened
// it as a boss-layer round: the parent tickets and projects it sums up.
const sedimentSourcesKey = "sediment_sources"

// sedimentSource is one thing a boss-layer round sums up.
type sedimentSource struct {
	Kind string `json:"kind"` // issue or project
	ID   string `json:"id"`
}

// SedimentSourceResponse is a source with what a reader needs to name it.
type SedimentSourceResponse struct {
	Kind       string  `json:"kind"`
	ID         string  `json:"id"`
	Identifier *string `json:"identifier"`
	Title      string  `json:"title"`
}

// roundSources reads the boss-layer sources an issue carries; empty for any
// ticket that is not a boss-layer round.
func roundSources(issue db.Issue) []sedimentSource {
	var metadata map[string]json.RawMessage
	if json.Unmarshal(issue.Metadata, &metadata) != nil {
		return nil
	}
	var sources []sedimentSource
	if json.Unmarshal(metadata[sedimentSourcesKey], &sources) != nil {
		return nil
	}
	return sources
}

// isBossRound reports whether closing this issue is a boss-layer sediment.
func isBossRound(issue db.Issue) bool { return len(roundSources(issue)) > 0 }

// addRoundSource records source on the round, once.
func (h *Handler) addRoundSource(ctx context.Context, round db.Issue, source sedimentSource) error {
	if fresh, err := h.Queries.GetIssue(ctx, round.ID); err == nil {
		round = fresh
	}
	sources := roundSources(round)
	for _, existing := range sources {
		if existing == source {
			return nil
		}
	}
	value, err := json.Marshal(append(sources, source))
	if err != nil {
		return err
	}
	_, err = h.Queries.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
		ID: round.ID, WorkspaceID: round.WorkspaceID, Key: sedimentSourcesKey, Value: value,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

// resolveSedimentSources names each source for a reader. A source that can
// no longer be read, or that viewer cannot see, keeps its kind and id only;
// a nil viewer names every source.
func (h *Handler) resolveSedimentSources(ctx context.Context, workspaceID pgtype.UUID, raw []byte, viewer *visibilityViewer) []SedimentSourceResponse {
	var sources []sedimentSource
	_ = json.Unmarshal(raw, &sources)
	out := make([]SedimentSourceResponse, 0, len(sources))
	prefix := ""
	for _, source := range sources {
		item := SedimentSourceResponse{Kind: source.Kind, ID: source.ID}
		switch source.Kind {
		case "issue":
			if issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: parseUUID(source.ID), WorkspaceID: workspaceID}); err == nil && (viewer == nil || viewer.canSeeIssue(issue)) {
				if prefix == "" {
					prefix = h.getIssuePrefix(ctx, workspaceID)
				}
				identifier := issueIdentifier(prefix, issue.Number)
				item.Identifier = &identifier
				item.Title = issue.Title
			}
		case "project":
			if project, err := h.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: parseUUID(source.ID), WorkspaceID: workspaceID}); err == nil && (viewer == nil || viewer.canSeeProject(project)) {
				item.Title = project.Title
			}
		}
		out = append(out, item)
	}
	return out
}

// recordIssueSediment writes the close's sediment row inside the close
// transaction. An audit that declared nothing writes no row.
func recordIssueSediment(ctx context.Context, q *db.Queries, issue db.Issue, audit closeprotocol.KnowledgeAudit, memoryFiles *[]closeprotocol.MemoryFile, prURL string, line *deliveryLineMerge, actorType, actorID string) error {
	if audit.None || len(audit.Changes) == 0 {
		return nil
	}
	changes, err := json.Marshal(audit.Changes)
	if err != nil {
		return err
	}
	var commits []string
	mainline := ""
	if line != nil {
		mainline = line.Branch
		for _, c := range line.Commits {
			commits = append(commits, c.SHA)
		}
	}
	if commits == nil {
		commits = []string{}
	}
	rawCommits, err := json.Marshal(commits)
	if err != nil {
		return err
	}
	layer, sources := sedimentLayerWorker, []byte("[]")
	if roundSrc := roundSources(issue); len(roundSrc) > 0 {
		layer = sedimentLayerBoss
		if sources, err = json.Marshal(roundSrc); err != nil {
			return err
		}
	}
	_, err = q.CreateKnowledgeSediment(ctx, db.CreateKnowledgeSedimentParams{
		Layer:       layer,
		Sources:     sources,
		WorkspaceID: issue.WorkspaceID,
		ProjectID:   issue.ProjectID,
		IssueID:     issue.ID,
		Changes:     changes,
		Verified:    !audit.Unverified,
		Mainline:    mainline,
		Commits:     rawCommits,
		PrUrl:       prURL,
		AuthorType:  actorType,
		AuthorID:    parseUUID(actorID),
		MemoryFiles: storedMemoryFiles(memoryFiles),
	})
	return err
}

// storedMemoryFiles is what a sediment row keeps of git's memory-file facts
// (DENE-1681): sizes, deleted lines and supersede marks per file, without the
// per-heading breakdown the hygiene check needed. Nil (an older CLI, a person
// closing from the web) stores [] — unknown, not "deleted nothing".
func storedMemoryFiles(files *[]closeprotocol.MemoryFile) []byte {
	out := []closeprotocol.MemoryFile{}
	if files != nil {
		for _, f := range *files {
			out = append(out, closeprotocol.MemoryFile{Path: f.Path, Bytes: f.Bytes, Deleted: f.Deleted, SupersedeMarks: f.SupersedeMarks})
		}
	}
	raw, _ := json.Marshal(out)
	return raw
}

func (h *Handler) recentKnowledgeSediments(ctx context.Context, project db.Project) []KnowledgeSedimentResponse {
	out := []KnowledgeSedimentResponse{}
	rows, err := h.Queries.ListProjectKnowledgeSediments(ctx, db.ListProjectKnowledgeSedimentsParams{
		ProjectID: project.ID, WorkspaceID: project.WorkspaceID, RowLimit: recentSedimentLimit,
	})
	if err != nil {
		slog.Warn("project memory: list sediments failed", "error", err, "project_id", uuidToString(project.ID))
		return out
	}
	prefix := h.getIssuePrefix(ctx, project.WorkspaceID)
	for _, row := range rows {
		item := sedimentToResponse(db.KnowledgeSediment{
			ID: row.ID, WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID, IssueID: row.IssueID,
			ChatSessionID: row.ChatSessionID, Changes: row.Changes, Verified: row.Verified, Mainline: row.Mainline,
			Commits: row.Commits, PrUrl: row.PrUrl, AuthorType: row.AuthorType, AuthorID: row.AuthorID, CreatedAt: row.CreatedAt,
			Layer: row.Layer,
		})
		item.Sources = h.resolveSedimentSources(ctx, row.WorkspaceID, row.Sources, nil)
		if row.IssueNumber.Valid {
			identifier := issueIdentifier(prefix, row.IssueNumber.Int32)
			item.IssueIdentifier = &identifier
			item.SourceTitle = row.IssueTitle
		} else {
			item.SourceTitle = row.ChatTitle
		}
		out = append(out, item)
	}
	return out
}

func sedimentToResponse(row db.KnowledgeSediment) KnowledgeSedimentResponse {
	item := KnowledgeSedimentResponse{
		ID:         uuidToString(row.ID),
		SourceKind: "chat",
		Changes:    []closeprotocol.KnowledgeChange{},
		Verified:   row.Verified,
		Mainline:   row.Mainline,
		Commits:    []string{},
		PRURL:      row.PrUrl,
		AuthorType: row.AuthorType,
		AuthorID:   uuidToString(row.AuthorID),
		CreatedAt:  timestampToString(row.CreatedAt),
		Layer:      row.Layer,
		Sources:    []SedimentSourceResponse{},
	}
	if item.Layer == "" {
		item.Layer = sedimentLayerWorker
	}
	if row.IssueID.Valid {
		item.SourceKind = "issue"
		item.IssueID = uuidToPtr(row.IssueID)
	}
	if row.ChatSessionID.Valid {
		item.ChatSessionID = uuidToPtr(row.ChatSessionID)
	}
	_ = json.Unmarshal(row.Changes, &item.Changes)
	_ = json.Unmarshal(row.Commits, &item.Commits)
	return item
}

// ChatSedimentRequest is what `multica chat sediment` sends after the
// chat's memory edits reached the main line: the locations it wrote, and
// what git says about it — the changed paths, the commits, the main line
// they are on, and how they got there (DENE-1668).
type ChatSedimentRequest struct {
	Changes        []closeprotocol.KnowledgeChange `json:"changes"`
	DeliveredFiles *[]string                       `json:"delivered_files"`
	Commits        []string                        `json:"commits"`
	Mainline       string                          `json:"mainline"`
	Landing        *closeprotocol.SedimentLanding  `json:"landing"`
	// MemoryFiles are git's facts about the delivered memory files
	// (DENE-1680); a chat sediment is boss-layer, so they are required.
	MemoryFiles *[]closeprotocol.MemoryFile `json:"memory_files"`
}

var commitSHAPattern = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// checkChatSediment is the chat sediment's whole gate. The audit is the
// close's (same locations, summaries, and file binding); a chat that settled
// nothing simply does not call, so "无够格知识" has no chat form.
func checkChatSediment(req ChatSedimentRequest) (closeprotocol.KnowledgeAudit, []string, string) {
	audit, _, err := closeprotocol.CanonicalKnowledgeAudit(closeprotocol.StripKnowledgeEvidence(closeprotocol.KnowledgeAudit{Changes: req.Changes}))
	if err != nil {
		return closeprotocol.KnowledgeAudit{}, nil, err.Error()
	}
	if req.DeliveredFiles == nil {
		return closeprotocol.KnowledgeAudit{}, nil, "缺 delivered_files：在聊天的工作目录里用 `multica chat sediment` 上报，它会从 git 读出改动文件"
	}
	bound, err := closeprotocol.BindDeliveredFiles(audit, req.DeliveredFiles)
	if err != nil {
		return closeprotocol.KnowledgeAudit{}, nil, err.Error()
	}
	if strings.TrimSpace(req.Mainline) == "" {
		return closeprotocol.KnowledgeAudit{}, nil, "缺 mainline：沉淀要先合进主线分支再上报"
	}
	commits := make([]string, 0, len(req.Commits))
	for _, sha := range req.Commits {
		sha = strings.ToLower(strings.TrimSpace(sha))
		if !commitSHAPattern.MatchString(sha) {
			return closeprotocol.KnowledgeAudit{}, nil, fmt.Sprintf("提交号 %q 不是 git SHA", sha)
		}
		commits = append(commits, sha)
	}
	if len(commits) == 0 {
		return closeprotocol.KnowledgeAudit{}, nil, "缺 commits：沉淀要以提交的形式进主线，上报带上那几个提交"
	}
	if req.Landing == nil {
		return closeprotocol.KnowledgeAudit{}, nil, "缺 landing：用新版 `multica chat sediment` 上报，它会核实主线确实收到了"
	}
	if reason := closeprotocol.CheckSedimentLanding(*req.Landing, req.Mainline); reason != "" {
		return closeprotocol.KnowledgeAudit{}, nil, reason
	}
	// A chat settling its work is the boss layer (DENE-1680): every change
	// says what it did to the existing entries, deletions are declared, and
	// the map files stay under their limit.
	if req.MemoryFiles == nil {
		return closeprotocol.KnowledgeAudit{}, nil, "缺 memory_files：用新版 `multica chat sediment` 上报，它会从 git 读出记忆文件的体积和删改"
	}
	if err := closeprotocol.CheckMemoryHygiene(bound, req.MemoryFiles, true); err != nil {
		return closeprotocol.KnowledgeAudit{}, nil, err.Error()
	}
	return bound, commits, ""
}

// CreateChatSediment is POST /api/chat/sessions/{sessionId}/sediment —
// `multica chat sediment`. It records the sediment and sets the chat's
// progress line to what was written, so the chat itself shows it.
func (h *Handler) CreateChatSediment(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	session, ok := h.gateChatSessionForUser(w, r, userID, workspaceID, chi.URLParam(r, "sessionId"))
	if !ok {
		return
	}
	var req ChatSedimentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	audit, commits, rejection := checkChatSediment(req)
	if rejection != "" {
		writeError(w, http.StatusBadRequest, rejection)
		return
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	changes, _ := json.Marshal(audit.Changes)
	rawCommits, _ := json.Marshal(commits)
	ctx := r.Context()
	row, err := h.Queries.CreateKnowledgeSediment(ctx, db.CreateKnowledgeSedimentParams{
		WorkspaceID:   session.WorkspaceID,
		ProjectID:     session.ProjectID,
		ChatSessionID: session.ID,
		Changes:       changes,
		Verified:      true,
		Mainline:      strings.TrimSpace(req.Mainline),
		Commits:       rawCommits,
		PrUrl:         strings.TrimSpace(req.Landing.PRURL),
		AuthorType:    actorType,
		AuthorID:      parseUUID(actorID),
		Layer:         sedimentLayerBoss,
		Sources:       []byte("[]"),
		MemoryFiles:   storedMemoryFiles(req.MemoryFiles),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record chat sediment")
		return
	}
	if updated, wrote, err := h.recordChatProgress(ctx, session, progressEntry{
		Text: progress.Clip(sedimentProgressLine(audit, req.Mainline)), Source: progress.SourceAgent, Tone: progress.ToneDone,
		AuthorType: actorType, AuthorID: actorID,
	}, false, pgtype.Timestamptz{}); err != nil {
		slog.Warn("chat sediment: progress line failed", "error", err, "chat_session_id", uuidToString(session.ID))
	} else if wrote {
		h.publishChatSessionState(workspaceID, actorType, actorID, updated)
	}
	writeJSON(w, http.StatusCreated, sedimentToResponse(row))
}

// ListChatSediments is GET /api/chat/sessions/{sessionId}/sediment.
func (h *Handler) ListChatSediments(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	session, ok := h.gateChatSessionForUser(w, r, userID, workspaceID, chi.URLParam(r, "sessionId"))
	if !ok {
		return
	}
	rows, err := h.Queries.ListChatKnowledgeSediments(r.Context(), db.ListChatKnowledgeSedimentsParams{
		ChatSessionID: session.ID, WorkspaceID: session.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list chat sediments")
		return
	}
	items := make([]KnowledgeSedimentResponse, 0, len(rows))
	for _, row := range rows {
		item := sedimentToResponse(row)
		item.SourceTitle = session.Title
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"sediments": items})
}

// sedimentProgressLine is the chat's progress line after a sediment: which
// files went where, short enough for the one-line slot.
func sedimentProgressLine(audit closeprotocol.KnowledgeAudit, mainline string) string {
	var files []string
	for _, change := range audit.Changes {
		files = append(files, change.Files...)
	}
	return "已沉淀进 " + strings.TrimSpace(mainline) + "：" + strings.Join(files, "、")
}

const (
	digestMaxIssues   = 20
	digestEvidenceMax = 160
)

// sedimentDigest is what a milestone sediment ticket carries about the work
// it sums up (DENE-1661): per source ticket its conclusion (the close
// summary), what its close wrote into project memory, and the head of its
// close evidence. The summarising agent reads this instead of opening each
// ticket. Tickets that never closed through `issue close` show what exists.
func (h *Handler) sedimentDigest(ctx context.Context, issues []db.Issue) string {
	if len(issues) == 0 {
		return ""
	}
	prefix := h.getIssuePrefix(ctx, issues[0].WorkspaceID)
	var b strings.Builder
	b.WriteString("来源票：\n")
	for i, issue := range issues {
		if i == digestMaxIssues {
			fmt.Fprintf(&b, "- 另有 %d 张未列出\n", len(issues)-digestMaxIssues)
			break
		}
		meta := parseIssueMetadata(issue.Metadata)
		fmt.Fprintf(&b, "- %s %s（%s）", issueIdentifier(prefix, issue.Number), issue.Title, issue.Status)
		if text := strings.TrimSpace(issue.ProgressText); text != "" {
			b.WriteString("｜结论：" + oneLine(text, digestEvidenceMax))
		}
		b.WriteString("｜沉淀：" + digestAudit(metaString(meta, closeprotocol.KeyKnowledgeAudit)))
		if id := metaString(meta, closeprotocol.KeyEvidenceCommentID); id != "" && h.Queries != nil {
			if comment, err := h.Queries.GetComment(ctx, parseUUID(id)); err == nil {
				b.WriteString("｜证据：" + oneLine(comment.Content, digestEvidenceMax))
			}
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// projectSedimentDigest sums up a project's recent sediments for the
// project-completed round: which deliveries wrote which files.
func (h *Handler) projectSedimentDigest(ctx context.Context, project db.Project) string {
	items := h.recentKnowledgeSediments(ctx, project)
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("最近的沉淀：\n")
	for _, item := range items {
		source := "聊天「" + item.SourceTitle + "」"
		if item.IssueIdentifier != nil {
			source = *item.IssueIdentifier + " " + item.SourceTitle
		}
		var files []string
		for _, change := range item.Changes {
			files = append(files, change.Files...)
		}
		if len(files) == 0 {
			files = append(files, "未核对")
		}
		fmt.Fprintf(&b, "- %s：%s\n", source, strings.Join(files, "、"))
	}
	return strings.TrimRight(b.String(), "\n")
}

func digestAudit(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "未审计"
	}
	audit, err := closeprotocol.ParseStoredKnowledgeAudit(raw)
	if err != nil {
		return "未审计"
	}
	if audit.None {
		return "无够格知识"
	}
	parts := make([]string, 0, len(audit.Changes))
	for _, change := range audit.Changes {
		part := change.Location
		if change.Action != "" {
			part += ":" + change.Action
			if change.Entry != "" {
				part += "「" + change.Entry + "」"
			}
		}
		if len(change.Files) > 0 {
			part += "(" + strings.Join(change.Files, "、") + ")"
		}
		parts = append(parts, part+" "+oneLine(change.Summary, 60))
	}
	return strings.Join(parts, "；")
}

func metaString(meta map[string]any, key string) string {
	s, _ := meta[key].(string)
	return s
}

func oneLine(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	if r := []rune(text); len(r) > max {
		return string(r[:max]) + "…"
	}
	return text
}
