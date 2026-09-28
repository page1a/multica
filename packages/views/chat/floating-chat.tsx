"use client";

import { useEffect, useRef } from "react";
import { useChatStore } from "@multica/core/chat";
import { useWorkspacePaths } from "@multica/core/paths";
import { useIsMobile } from "@multica/ui/hooks/use-mobile";
import { useBackToDismiss, useNavigation } from "../navigation";
import { ChatFab } from "./components/chat-fab";
import { ChatWindow } from "./components/chat-window";
import { isFloatingChatRouteSuppressed } from "./floating-chat-visibility";

/**
 * Mount point for the floating chat overlay (FAB + window). Rendered once in
 * each app shell's dashboard layout; owns the two gates that decide whether the
 * overlay exists at all:
 *
 *  1. The Settings → Chat preference (`floatingChatEnabled`). When a user turns
 *     the floating window off, Chat lives only in its dedicated tab.
 *  2. The Chat tab route itself. On `/:slug/chat` the full-page surface already
 *     owns the conversation, so a floating copy of the same `activeSessionId`
 *     would be pure duplication — hide it there.
 *
 * On a phone the window is a full-screen sheet over the page, so it also has to
 * behave like one: the back gesture closes it (instead of navigating the page
 * hidden under it), moving to another page closes it, and it never reopens by
 * itself from the persisted open flag.
 */
export function FloatingChat() {
  const enabled = useChatStore((s) => s.floatingChatEnabled);
  const isOpen = useChatStore((s) => s.isOpen);
  const setOpen = useChatStore((s) => s.setOpen);
  const { pathname } = useNavigation();
  const wsPaths = useWorkspacePaths();
  const isMobile = useIsMobile();
  const suppressed = isFloatingChatRouteSuppressed(pathname, wsPaths.chat());

  // The open flag is a desktop habit ("keep my chat panel open"). On a phone a
  // restored flag means a full-screen sheet covering the page just opened, so
  // start closed there — without persisting, the preference itself stays.
  const settledMobile = useRef(false);
  useEffect(() => {
    if (!isMobile || settledMobile.current) return;
    settledMobile.current = true;
    if (useChatStore.getState().isOpen) useChatStore.setState({ isOpen: false });
  }, [isMobile]);

  // A link followed from inside the sheet lands on a page the sheet would
  // cover; leaving the Chat tab must not reveal it either.
  const lastPathname = useRef(pathname);
  useEffect(() => {
    if (lastPathname.current === pathname) return;
    lastPathname.current = pathname;
    if (isMobile && useChatStore.getState().isOpen) setOpen(false);
  }, [pathname, isMobile, setOpen]);

  useBackToDismiss(enabled && !suppressed && isMobile && isOpen, () =>
    setOpen(false),
  );

  if (!enabled) return null;
  // Suppress on the Chat tab — it renders the same conversation full-page.
  if (suppressed) return null;

  return (
    <>
      <ChatWindow />
      <ChatFab />
    </>
  );
}
