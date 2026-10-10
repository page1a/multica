"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { chatSessionsOptions } from "@multica/core/chat/queries";
import { countUnreadChatMessages } from "@multica/core/chat/unread";
import { useChatStore } from "@multica/core/chat";
import { useAppForeground } from "../common/use-app-foreground";

// Top-level nav items stay active when the user is on a child route
// (e.g. "Projects" stays lit on /:slug/projects/:id). Pinned items keep
// strict equality elsewhere — a pinned project shouldn't highlight on
// sub-pages of itself.
export function isNavActive(pathname: string, href: string): boolean {
  return pathname === href || pathname.startsWith(href + "/");
}

/**
 * The Chat nav badge: IM-style total of unread *messages* across chat threads
 * (countUnreadChatMessages is the shared definition — mobile's tab badge
 * derives from the same function, keeping the platforms in agreement).
 *
 * The session the user is reading right now must not count: the thread list
 * renders its row badge as 0 (auto mark-read is about to clear it), and a
 * reply landing in the open conversation would otherwise flash a nav count
 * with no matching row. "Reading right now" = a session is active, a chat
 * surface is actually showing it (chat page route or the floating window),
 * AND the app is in the foreground. When the app is backgrounded, auto
 * mark-read is suppressed (MUL-4485) so the reply stays unread — the badge
 * must count it, or the notification is silently eaten while the user is
 * away. A remembered selection while both surfaces are closed also still
 * counts, for the same reason.
 */
export function useChatNavUnreadCount(
  wsId: string | undefined,
  pathname: string,
  chatHref: string,
): number {
  const { data: chatSessions } = useQuery({
    ...chatSessionsOptions(wsId ?? ""),
    enabled: !!wsId,
  });
  const activeChatSessionId = useChatStore((s) => s.activeSessionId);
  const floatingChatOpen = useChatStore((s) => s.isOpen);
  const appForeground = useAppForeground();
  const viewedChatSessionId =
    appForeground && (floatingChatOpen || isNavActive(pathname, chatHref))
      ? activeChatSessionId
      : null;
  return useMemo(
    () => countUnreadChatMessages(chatSessions ?? [], viewedChatSessionId),
    [chatSessions, viewedChatSessionId],
  );
}
