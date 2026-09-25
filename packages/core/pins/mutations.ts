import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useAuthStore } from "../auth";
import { pinKeys } from "./queries";
import { chatKeys } from "../chat/queries";
import { useWorkspaceId } from "../hooks";
import type { PinnedItem, PinnedItemType } from "../types";

/**
 * Drag payload type a Chat list row carries so the sidebar's pinned group can
 * accept it as a native HTML drop. Native DnD (not dnd-kit) because the list
 * and the sidebar live in separate trees; a MIME of our own keeps a stray
 * text drop from being mistaken for a chat.
 */
export const CHAT_PIN_DRAG_TYPE = "application/x-multica-chat-session";

export function useCreatePin() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  const userId = useAuthStore((s) => s.user?.id ?? "");
  return useMutation({
    mutationFn: (data: { item_type: PinnedItemType; item_id: string }) =>
      api.createPin(data),
    onSuccess: (newPin) => {
      qc.setQueryData<PinnedItem[]>(pinKeys.list(wsId, userId), (old) =>
        old ? [...old, newPin] : [newPin],
      );
    },
    onSettled: (_data, _err, vars) => {
      qc.invalidateQueries({ queryKey: pinKeys.list(wsId, userId) });
      // The Chat list sorts on the same pin (DENE-866).
      if (vars.item_type === "chat") qc.invalidateQueries({ queryKey: chatKeys.sessions(wsId) });
    },
  });
}

export function useDeletePin() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  const userId = useAuthStore((s) => s.user?.id ?? "");
  return useMutation({
    mutationFn: ({ itemType, itemId }: { itemType: PinnedItemType; itemId: string }) =>
      api.deletePin(itemType, itemId),
    onMutate: async ({ itemType, itemId }) => {
      await qc.cancelQueries({ queryKey: pinKeys.list(wsId, userId) });
      const prev = qc.getQueryData<PinnedItem[]>(pinKeys.list(wsId, userId));
      qc.setQueryData<PinnedItem[]>(pinKeys.list(wsId, userId), (old) =>
        old ? old.filter((p) => !(p.item_type === itemType && p.item_id === itemId)) : old,
      );
      return { prev };
    },
    onError: (_err, _vars, ctx) => {
      if (ctx?.prev) qc.setQueryData(pinKeys.list(wsId, userId), ctx.prev);
    },
    onSettled: (_data, _err, vars) => {
      qc.invalidateQueries({ queryKey: pinKeys.list(wsId, userId) });
      if (vars.itemType === "chat") qc.invalidateQueries({ queryKey: chatKeys.sessions(wsId) });
    },
  });
}

export function useReorderPins() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  const userId = useAuthStore((s) => s.user?.id ?? "");
  return useMutation({
    mutationFn: (reorderedPins: PinnedItem[]) => {
      const items = reorderedPins.map((p, i) => ({ id: p.id, position: i + 1 }));
      return api.reorderPins({ items });
    },
    onMutate: async (reorderedPins) => {
      await qc.cancelQueries({ queryKey: pinKeys.list(wsId, userId) });
      const prev = qc.getQueryData<PinnedItem[]>(pinKeys.list(wsId, userId));
      qc.setQueryData<PinnedItem[]>(pinKeys.list(wsId, userId), reorderedPins);
      return { prev };
    },
    onError: (_err, _vars, ctx) => {
      if (ctx?.prev) qc.setQueryData(pinKeys.list(wsId, userId), ctx.prev);
    },
  });
}
