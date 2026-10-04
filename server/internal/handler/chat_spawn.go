package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/chattitle"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Chat spawn (DENE-1271): an agent running in a chat opens a new chat with a
// brief, in one call — `multica chat open`. The session, the brief (as the
// first message, so the target agent replies), the origin link, the card in
// the parent chat and the ledger row all commit together.

const (
	SpawnRefusedAgentOnly = "chat_spawn_agent_only"
	SpawnRefusedScope     = "chat_spawn_scope_exceeded"
	chatSpawnBriefMaxLen  = 20000
)

type SpawnChatSessionRequest struct {
	AgentID    string  `json:"agent_id"`
	Brief      string  `json:"brief"`
	Title      string  `json:"title"`
	ProjectID  *string `json:"project_id"`
	Visibility string  `json:"visibility"`
	ClientKey  string  `json:"client_key"`
}

type SpawnChatSessionResponse struct {
	Session         ChatSessionResponse `json:"session"`
	Created         bool                `json:"created"`
	ParentSessionID string              `json:"parent_session_id"`
	MessageID       string              `json:"message_id,omitempty"`
	TaskID          string              `json:"task_id,omitempty"`
}

// visibilityRank orders chat scopes from narrowest to widest.
func visibilityRank(v string) int {
	switch v {
	case "private":
		return 0
	case "project":
		return 1
	case "workspace":
		return 2
	}
	return -1
}

func (h *Handler) SpawnChatSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	if actorType != "agent" {
		writeAgentSpawnRefusal(w, &agentSpawnRefusal{
			Code:    SpawnRefusedAgentOnly,
			Message: "only an agent run can open a chat this way; people start chats from the sidebar",
		})
		return
	}
	task, ok := h.agentSpawnTask(r, actorType, actorID)
	if !ok {
		writeError(w, http.StatusBadRequest, "no running task on this request (X-Task-ID)")
		return
	}
	if isTerminalTaskStatus(task.Status) {
		writeError(w, http.StatusForbidden, "this run has finished and can no longer open chats")
		return
	}
	if !task.ChatSessionID.Valid {
		writeAgentSpawnRefusal(w, &agentSpawnRefusal{
			Code:    SpawnRefusedTaskMode,
			Message: "only a chat run can open a chat: in an issue, discuss with `multica issue comment add` or split parallel work with `multica issue create --parent`",
		})
		return
	}

	var req SpawnChatSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Brief = strings.TrimSpace(req.Brief)
	req.Title = strings.TrimSpace(req.Title)
	req.ClientKey = strings.TrimSpace(req.ClientKey)
	if req.AgentID == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	if req.Brief == "" {
		writeError(w, http.StatusBadRequest, "brief is required: it becomes the new chat's first message")
		return
	}
	if utf8.RuneCountInString(req.Brief) > chatSpawnBriefMaxLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("brief is longer than %d characters; put the detail in an attachment or an issue", chatSpawnBriefMaxLen))
		return
	}
	if utf8.RuneCountInString(req.Title) > chatSessionTitleMaxLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("title is longer than %d characters", chatSessionTitleMaxLen))
		return
	}
	if len(req.ClientKey) > chatSpawnClientKeyMaxBytes {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("client_key is longer than %d bytes", chatSpawnClientKeyMaxBytes))
		return
	}
	agentID, ok := parseUUIDOrBadRequest(w, req.AgentID, "agent_id")
	if !ok {
		return
	}

	// A retry of the same run with the same key answers with what the first
	// call created, before any check can refuse it.
	if req.ClientKey != "" {
		if existing, err := h.Queries.GetChatSessionBySpawnKey(r.Context(), db.GetChatSessionBySpawnKeyParams{
			OriginTaskID:    task.ID,
			OriginClientKey: pgtype.Text{String: req.ClientKey, Valid: true},
		}); err == nil {
			h.writeSpawnedSession(w, r, existing, false, "", "")
			return
		} else if !errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusInternalServerError, "failed to look up client key")
			return
		}
	}

	parent, err := h.Queries.GetChatSessionInWorkspace(r.Context(), db.GetChatSessionInWorkspaceParams{
		ID:          task.ChatSessionID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "the chat this run belongs to was not found")
		return
	}
	if parent.OriginType.Valid {
		h.refuseChatSpawn(w, r, parent, &agentSpawnRefusal{
			Code:    SpawnRefusedDepth,
			Message: "this chat was itself opened by an agent, and a spawned chat cannot open another; continue here or create an issue",
		})
		return
	}

	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: workspaceUUID})
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if agent.ArchivedAt.Valid {
		writeError(w, http.StatusBadRequest, "agent is archived")
		return
	}
	if verdict, err := service.AgentReadiness(r.Context(), h.runtimeLookup(obsmetrics.RuntimeLookupSourceChat), agent); err == nil && verdict.Blocked() {
		h.writeDispatchBlocked(w, http.StatusConflict, verdict.Reason)
		return
	}
	// The new chat belongs to the person at the top of the chain, and the
	// target agent must be one that person may invoke.
	originator := h.invokeOriginatorFromRequest(r, actorType, actorID)
	if originator == "" {
		writeError(w, http.StatusForbidden, "this run has no originating person to own a new chat")
		return
	}
	if !h.canInvokeAgent(r.Context(), agent, actorType, actorID, originator, workspaceID) {
		h.writeDispatchBlocked(w, http.StatusForbidden, ReasonInvocationNotAllowed)
		return
	}

	// Scope: the child sees no further than the parent. Projects are a subset
	// of the parent's; visibility is at most the parent's. Shares on the
	// parent are not copied — that only narrows.
	parentProjects, err := h.Queries.ListChatSessionProjectIDs(r.Context(), parent.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read the parent chat's projects")
		return
	}
	projectIDs := parentProjects
	if req.ProjectID != nil && *req.ProjectID != "" {
		pid, ok := parseUUIDOrBadRequest(w, *req.ProjectID, "project_id")
		if !ok {
			return
		}
		inParent := false
		for _, id := range parentProjects {
			if id == pid {
				inParent = true
				break
			}
		}
		if !inParent {
			h.refuseChatSpawn(w, r, parent, &agentSpawnRefusal{
				Code:    SpawnRefusedScope,
				Message: "the new chat can only use a project the current chat already has",
				Scope:   "project",
			})
			return
		}
		projectIDs = []pgtype.UUID{pid}
	}
	visibility := parent.Visibility
	if req.Visibility != "" {
		if visibilityRank(req.Visibility) < 0 {
			writeError(w, http.StatusBadRequest, "visibility must be private, project, or workspace")
			return
		}
		if visibilityRank(req.Visibility) > visibilityRank(parent.Visibility) {
			h.refuseChatSpawn(w, r, parent, &agentSpawnRefusal{
				Code:    SpawnRefusedScope,
				Message: fmt.Sprintf("the new chat cannot be wider than the current one (%s)", parent.Visibility),
				Scope:   "visibility",
			})
			return
		}
		visibility = req.Visibility
	}
	if visibility == "project" && len(projectIDs) == 0 {
		visibility = "private"
	}

	title := req.Title
	cardTitle := title
	if cardTitle == "" {
		cardTitle = chattitle.Derive(req.Brief)
	}
	childID := dbid.NewV7()
	draft := db.ChatSession{ID: childID, WorkspaceID: workspaceUUID, AgentID: agent.ID, CreatorID: parseUUID(originator)}
	var child db.ChatSession
	var card db.ChatMessage

	sent, err := h.TaskService.SpawnDirectChat(r.Context(), draft, agent, parseUUID(originator), req.Brief, func(qtx *db.Queries) error {
		if _, err := qtx.LockWorkspaceForChatSessionCreate(r.Context(), workspaceUUID); err != nil {
			return err
		}
		// Lock the parent so two spawns from the same chat count each other.
		if _, err := qtx.LockChatSessionForRuntimeBind(r.Context(), parent.ID); err != nil {
			return err
		}
		policy, err := h.agentSpawnPolicy(r.Context(), qtx, workspaceUUID)
		if err != nil {
			return err
		}
		if refusal := h.checkAgentSpawn(r.Context(), qtx, task, policy, agentSpawnKindChat, 1); refusal != nil {
			return refusal
		}
		if per := policy.ChatChat.PerChat; per > 0 {
			used, err := qtx.CountChatSessionsSpawnedFrom(r.Context(), parent.ID)
			if err != nil {
				return err
			}
			if int(used) >= per {
				return &agentSpawnRefusal{
					Code:    SpawnRefusedBudget,
					Message: fmt.Sprintf("this chat already opened %d of %d allowed chats; continue in an existing one", used, per),
					Limit:   per,
					Scope:   "chat",
				}
			}
		}
		if err := h.lockChatSessionProjects(r.Context(), qtx, workspaceUUID, projectIDs); err != nil {
			return err
		}
		session, err := qtx.CreateChatSession(r.Context(), db.CreateChatSessionParams{
			ID:          childID,
			WorkspaceID: workspaceUUID,
			AgentID:     agent.ID,
			CreatorID:   parseUUID(originator),
			Title:       title,
			ProjectID:   primaryChatSessionProjectID(projectIDs),
		})
		if err != nil {
			return err
		}
		if title == "" {
			if session, err = qtx.MarkChatSessionExplicitlyCreated(r.Context(), session.ID); err != nil {
				return err
			}
		}
		if err := h.insertChatSessionProjects(r.Context(), qtx, session, projectIDs); err != nil {
			return err
		}
		if visibility != session.Visibility {
			if session, err = qtx.SetChatSessionVisibility(r.Context(), db.SetChatSessionVisibilityParams{ID: session.ID, Visibility: visibility}); err != nil {
				return err
			}
		}
		session, err = qtx.SetChatSessionSpawnOrigin(r.Context(), db.SetChatSessionSpawnOriginParams{
			ID:              session.ID,
			OriginSessionID: parent.ID,
			OriginTaskID:    task.ID,
			OriginClientKey: pgtype.Text{String: req.ClientKey, Valid: req.ClientKey != ""},
		})
		if err != nil {
			return err
		}
		child = session
		if err := recordAgentSpawn(r.Context(), qtx, task, agentSpawnKindChat, workspaceUUID, session.ID); err != nil {
			return err
		}
		msg, err := qtx.CreateChatMessage(r.Context(), db.CreateChatMessageParams{
			ID:            dbid.NewV7(),
			ChatSessionID: parent.ID,
			Role:          "assistant",
			Content:       "已派生会话 → " + cardTitle,
			MessageKind:   pgtype.Text{String: protocol.ChatMessageKindChatSpawn, Valid: true},
		})
		if err != nil {
			return err
		}
		if card, err = qtx.SetChatMessageLinkedSession(r.Context(), db.SetChatMessageLinkedSessionParams{ID: msg.ID, LinkedSessionID: session.ID}); err != nil {
			return err
		}
		return qtx.TouchChatSession(r.Context(), parent.ID)
	})
	if err != nil {
		var refusal *agentSpawnRefusal
		switch {
		case errors.As(err, &refusal):
			h.refuseChatSpawn(w, r, parent, refusal)
		case isUniqueViolation(err) && req.ClientKey != "":
			// A concurrent retry with the same key committed first.
			existing, lookupErr := h.Queries.GetChatSessionBySpawnKey(r.Context(), db.GetChatSessionBySpawnKeyParams{
				OriginTaskID:    task.ID,
				OriginClientKey: pgtype.Text{String: req.ClientKey, Valid: true},
			})
			if lookupErr != nil {
				writeError(w, http.StatusConflict, "a chat with this client key is being opened; retry")
				return
			}
			h.writeSpawnedSession(w, r, existing, false, "", "")
		case errors.Is(err, pgx.ErrNoRows):
			writeError(w, http.StatusNotFound, "workspace or project not found")
		case errors.Is(err, service.ErrChatTaskAgentArchived):
			writeError(w, http.StatusConflict, "chat agent is archived")
		case errors.Is(err, service.ErrChatTaskAgentDisabled):
			writeError(w, http.StatusConflict, "chat agent is not accepting work")
		case errors.Is(err, service.ErrChatTaskAgentNoRuntime):
			writeError(w, http.StatusConflict, "chat agent has no runtime")
		default:
			slog.Warn("chat spawn failed", "parent_session_id", uuidToString(parent.ID), "error", err)
			writeError(w, http.StatusInternalServerError, "failed to open chat: "+err.Error())
		}
		return
	}

	if sent.InitialTitle != "" {
		child.Title = sent.InitialTitle
	}
	childIDString := uuidToString(child.ID)
	parentIDString := uuidToString(parent.ID)
	h.publishChat(protocol.EventChatMessage, workspaceID, "agent", actorID, childIDString, protocol.ChatMessagePayload{
		ChatSessionID: childIDString,
		MessageID:     uuidToString(sent.Message.ID),
		Role:          "user",
		Content:       req.Brief,
		TaskID:        uuidToString(sent.Task.ID),
		CreatedAt:     timestampToString(sent.Message.CreatedAt),
	})
	h.publishChat(protocol.EventChatMessage, workspaceID, "agent", actorID, parentIDString, protocol.ChatMessagePayload{
		ChatSessionID: parentIDString,
		MessageID:     uuidToString(card.ID),
		Role:          "assistant",
		Content:       card.Content,
		CreatedAt:     timestampToString(card.CreatedAt),
	})
	h.publishChatInvalidated(workspaceID, originator, childIDString)
	h.writeSpawnedSession(w, r, child, true, uuidToString(sent.Message.ID), uuidToString(sent.Task.ID))
}

func (h *Handler) writeSpawnedSession(w http.ResponseWriter, r *http.Request, session db.ChatSession, created bool, messageID, taskID string) {
	responses := []ChatSessionResponse{chatSessionToResponse(session)}
	if err := h.hydrateChatSessionProjectIDs(r.Context(), responses); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load chat session projects")
		return
	}
	if err := h.decorateChatSession(r.Context(), uuidToString(session.CreatorID), session, &responses[0]); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load chat access")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, SpawnChatSessionResponse{
		Session:         responses[0],
		Created:         created,
		ParentSessionID: uuidToString(session.OriginSessionID),
		MessageID:       messageID,
		TaskID:          taskID,
	})
}

// refuseChatSpawn answers the agent with the structured refusal and leaves a
// readable line in the chat it was running in, so the person sees why no new
// chat appeared.
func (h *Handler) refuseChatSpawn(w http.ResponseWriter, r *http.Request, parent db.ChatSession, refusal *agentSpawnRefusal) {
	msg, err := h.Queries.CreateChatMessage(r.Context(), db.CreateChatMessageParams{
		ID:            dbid.NewV7(),
		ChatSessionID: parent.ID,
		Role:          "assistant",
		Content:       chatSpawnRefusalText(refusal),
		MessageKind:   pgtype.Text{String: protocol.ChatMessageKindChatSpawnRefused, Valid: true},
	})
	if err != nil {
		slog.Warn("chat spawn refusal card failed", "parent_session_id", uuidToString(parent.ID), "error", err)
	} else {
		parentID := uuidToString(parent.ID)
		h.publishChat(protocol.EventChatMessage, uuidToString(parent.WorkspaceID), "agent", "", parentID, protocol.ChatMessagePayload{
			ChatSessionID: parentID,
			MessageID:     uuidToString(msg.ID),
			Role:          "assistant",
			Content:       msg.Content,
			CreatedAt:     timestampToString(msg.CreatedAt),
		})
	}
	writeAgentSpawnRefusal(w, refusal)
}

// chatSpawnRefusalText is the line a person reads in the parent chat.
func chatSpawnRefusalText(refusal *agentSpawnRefusal) string {
	switch refusal.Code {
	case SpawnRefusedDepth:
		return "没有新开聊天：这个聊天本身是派生出来的，不能再往下开。"
	case SpawnRefusedDisabled:
		return "没有新开聊天：工作区设置不允许智能体在聊天中新建聊天。"
	case SpawnRefusedBudget:
		if refusal.Scope == "chat" {
			return fmt.Sprintf("没有新开聊天：这个聊天已开过 %d 个，达到上限。", refusal.Limit)
		}
		return fmt.Sprintf("没有新开聊天：本次运行已开过 %d 个，达到上限。", refusal.Limit)
	case SpawnRefusedScope:
		return "没有新开聊天：新聊天的可见范围不能超过这个聊天。"
	}
	return "没有新开聊天：" + refusal.Message
}

// chatOriginTitle fills the child bar's link text when the viewer can open
// the parent; otherwise the bar shows a generic label and no title leaks.
func (h *Handler) chatOriginTitle(ctx context.Context, session db.ChatSession, userID string) *string {
	if !session.OriginSessionID.Valid {
		return nil
	}
	parent, err := h.Queries.GetChatSession(ctx, session.OriginSessionID)
	if err != nil {
		return nil
	}
	access, err := h.chatAccessFor(ctx, parent, userID)
	if err != nil || !access.see {
		return nil
	}
	title := parent.Title
	return &title
}
