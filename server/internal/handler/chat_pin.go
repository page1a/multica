package handler

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// pinnedItemTypeChat is the pinned_item row kind for a chat session. Chat
// pins live in the per-user sidebar pin table rather than on the session row,
// so pinning a shared chat never pins it for the other people who can see it
// (DENE-866). The Chat list's own pin toggle and the sidebar drop zone both
// write this row; `pinned` on a session response is derived from it for the
// viewer.
const pinnedItemTypeChat = "chat"

// chatPinnedFor reports whether userID has the session pinned in workspaceID.
func (h *Handler) chatPinnedFor(ctx context.Context, workspaceID, sessionID pgtype.UUID, userID string) (bool, error) {
	_, err := h.Queries.GetPinnedItemByItem(ctx, db.GetPinnedItemByItemParams{
		WorkspaceID: workspaceID,
		UserID:      parseUUID(userID),
		ItemType:    pinnedItemTypeChat,
		ItemID:      sessionID,
	})
	if err == nil {
		return true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return false, err
}

// setChatPin writes the caller's own pin for the session: pinned=true appends
// a pin row after their existing pins (idempotent when already pinned),
// pinned=false removes it. It fans out the same events the generic pin
// endpoints emit — pin:created / pin:deleted for the sidebar and a personal
// chat:session_updated so the caller's other tabs re-sort their chat list.
func (h *Handler) setChatPin(ctx context.Context, workspaceID, userID string, session db.ChatSession, pinned bool) error {
	wsUUID := parseUUID(workspaceID)
	if pinned {
		maxPos, err := h.Queries.GetMaxPinnedItemPosition(ctx, db.GetMaxPinnedItemPositionParams{
			WorkspaceID: wsUUID,
			UserID:      parseUUID(userID),
		})
		if err != nil {
			return err
		}
		pin, err := h.Queries.CreatePinnedItem(ctx, db.CreatePinnedItemParams{
			WorkspaceID: wsUUID,
			UserID:      parseUUID(userID),
			ItemType:    pinnedItemTypeChat,
			ItemID:      session.ID,
			Position:    maxPos + 1,
		})
		if err != nil {
			if isUniqueViolation(err) {
				return nil
			}
			return err
		}
		h.publish(protocol.EventPinCreated, workspaceID, "member", userID, map[string]any{"pin": pinnedItemToResponse(pin)})
	} else {
		if err := h.Queries.DeletePinnedItem(ctx, db.DeletePinnedItemParams{
			WorkspaceID: wsUUID,
			UserID:      parseUUID(userID),
			ItemType:    pinnedItemTypeChat,
			ItemID:      session.ID,
		}); err != nil {
			return err
		}
		h.publish(protocol.EventPinDeleted, workspaceID, "member", userID, map[string]any{
			"item_type": pinnedItemTypeChat,
			"item_id":   uuidToString(session.ID),
		})
	}
	h.publishChatPinned(workspaceID, userID, session, pinned)
	return nil
}

// publishChatPinned tells the acting user's other tabs about their new pin
// state. chat:session_updated is delivered to the actor only (see
// registerListeners), which is exactly the audience a per-user pin has.
func (h *Handler) publishChatPinned(workspaceID, userID string, session db.ChatSession, pinned bool) {
	sessionID := uuidToString(session.ID)
	h.publishChat(protocol.EventChatSessionUpdated, workspaceID, "member", userID, sessionID, protocol.ChatSessionUpdatedPayload{
		ChatSessionID: sessionID,
		Title:         session.Title,
		Pinned:        &pinned,
		UpdatedAt:     timestampToString(session.UpdatedAt),
	})
}
