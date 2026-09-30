"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useDefaultLayout } from "react-resizable-panels";
import { ArrowLeft, MessageSquare } from "lucide-react";
import { toast } from "sonner";
import { cn } from "@multica/ui/lib/utils";
import { Button } from "@multica/ui/components/ui/button";
import {
  ResizablePanelGroup,
  ResizablePanel,
  ResizableHandle,
} from "@multica/ui/components/ui/resizable";
import { useIsCompact } from "@multica/ui/hooks/use-mobile";
import { useWorkspacePaths } from "@multica/core/paths";
import { useChatStore } from "@multica/core/chat";
import { chatSessionProjectIds } from "@multica/core/chat/project-context";
import {
  rankChatProjects,
  sessionMatchesChatProjectFilter,
  type ChatProjectFilter,
} from "@multica/core/chat/project-bar";
import {
  draftProjectIdsForNewChat,
  orderProjectsForQuickSwitch,
  sessionToLandOn,
} from "@multica/core/chat/project-switch";
import { useChatProjectOpenStore } from "@multica/core/chat/project-open-store";
import { useChatListViewStore } from "@multica/core/chat/list-view-store";
import { chatPageShortcutAction } from "@multica/core/chat/chat-page-shortcuts";
import {
  isEditableShortcutTarget,
  isPortalLayerShortcutTarget,
  useShortcut,
} from "@multica/core/shortcuts";
import { isAgentRuntimeBound } from "@multica/core/agents";
import { isImeComposing } from "@multica/core/utils";
import {
  useDismissChatProjectNudge,
  useRegenerateChatQuickActions,
} from "@multica/core/chat/mutations";
import {
  chatMessageSearchOptions,
  chatMessagesOptions,
  chatQuickActionsPendingOptions,
} from "@multica/core/chat/queries";
import { useQuickActionsPendingTimeout } from "@multica/core/chat/use-quick-actions-pending-timeout";
import { useQuickActionsFailureToast } from "./components/use-quick-actions-failure-toast";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { chatSessionIdFromLocation } from "@multica/core/paths";
import type { Agent, ChatSession } from "@multica/core/types";
import { PageHeader } from "../layout/page-header";
import { useBackOrReplace, useNavigation } from "../navigation";
import { useT } from "../i18n";
import { ChatMessageList, ChatMessageSkeleton } from "./components/chat-message-list";
import { ChatInput } from "./components/chat-input";
import { ChatQueue } from "./components/chat-queue";
import { ChatThreadList } from "./components/chat-thread-list";
import { ChatProjectBar } from "./components/chat-project-bar";
import { ChatProjectSwitcher } from "./components/chat-project-switcher";
import { ChatProjectNudge } from "./components/chat-project-nudge";
import { ChatSessionHeader } from "./components/chat-session-header";
import { EmptyState } from "./components/chat-empty-state";
import { DirectNewChatButton, NewChatButton } from "./components/new-chat-button";
import { CHAT_COLUMN, CHAT_GUTTER } from "./components/chat-column";
import { AlignmentRecords } from "../issues/draft";
import { useChatController } from "./components/use-chat-controller";
import { OfflineBanner } from "./components/offline-banner";
import { NoAgentBanner } from "./components/no-agent-banner";
import { ArchivedAgentBanner } from "./components/archived-agent-banner";
import { AgentAccessRevokedBanner } from "./components/agent-access-revoked-banner";
import { RuntimeRequiredBanner } from "./components/runtime-required-banner";
import { WorkThreadPanel } from "../common/work-thread-panel";
import { PageSearchInput } from "../common/page-search-input";
import { matchesPinyin } from "../editor/extensions/pinyin-match";
import { useDebouncedValue } from "../common/use-debounced-value";
import { useRestoredScrollRef } from "../platform";

/**
 * Title half of the chat page search: every word in the title, or the whole
 * query as pinyin (from the start of the title, as in Cmd+K).
 */
function isChatComposerTarget(target: EventTarget | null): boolean {
  return (
    target instanceof Element &&
    target.closest("[data-slot='chat-input-surface']") !== null
  );
}

/** Popup and non-composer fields keep their own keys. The composer does not. */
function chatShortcutGates(target: EventTarget | null): {
  inForeignEditable: boolean;
  inPortal: boolean;
} {
  const inPortal =
    isPortalLayerShortcutTarget(target) ||
    (typeof document !== "undefined" &&
      document.querySelector("[data-slot='popover-content']") !== null);
  return {
    inPortal,
    inForeignEditable: isEditableShortcutTarget(target) && !isChatComposerTarget(target),
  };
}

function chatTitleMatches(session: ChatSession, words: string[], query: string) {
  const title = session.title?.trim().toLowerCase() ?? "";
  if (!title) return false;
  return words.every((word) => title.includes(word)) || matchesPinyin(title, query);
}

/**
 * Chat tab — the first-class two-pane surface (thread list on the left,
 * conversation on the right), mirroring the Inbox page layout. Shares all
 * conversation logic with the floating FAB via `useChatController`; the
 * left rail reuses `ChatThreadList`.
 *
 * Selection is URL-addressable via `/chat/<session-id>` so a thread can be
 * deep-linked, opened from a notification, and survive refresh. Older
 * `?session=` links still open. The chat store's `activeSessionId` stays the
 * source of truth (both surfaces read it); the URL is kept in sync in both
 * directions. `?agent=<id>` is the
 * complementary one-shot deep link for a NEW chat: it starts a fresh compose
 * bound to that agent and is then stripped from the URL.
 *
 * Starting a chat is where the agent is chosen: the header ⊕ opens an agent
 * picker (see NewChatButton), so the compose box no longer needs its own
 * agent selector. Unlike the FAB, this page passes no `contextItems` to
 * `ChatInput`, so its `@` mentions fall back to manual search (issue-comment
 * style).
 */
export function ChatPage() {
  const { t } = useT("chat");
  const { pathname, searchParams, replace, push, back } = useNavigation();
  const backOrReplace = useBackOrReplace();
  const queryClient = useQueryClient();
  const wsPaths = useWorkspacePaths();
  const isCompact = useIsCompact();

  const c = useChatController({ isActive: true });
  const restoreListScroll = useRestoredScrollRef("chat-list");
  const { data: quickActionsPending = null } = useQuery(
    chatQuickActionsPendingOptions(c.activeSessionId ?? ""),
  );
  // Drop a stuck pending marker (dead daemon / failed supplement) so the pill
  // spinner stops and a later refresh starts clean (MUL-5149).
  useQuickActionsPendingTimeout(c.activeSessionId ?? null, quickActionsPending);
  // Toast when an accepted refresh later fails in the daemon (async half).
  useQuickActionsFailureToast(c.activeSessionId ?? null);
  const regenerateQuickActions = useRegenerateChatQuickActions();
  const urlSession = chatSessionIdFromLocation(pathname, searchParams);
  const urlAgent = searchParams.get("agent") || null;

  // "Composing a brand-new chat" — the user hit ⊕ but hasn't sent yet, so no
  // session exists. At compact widths this decides list-vs-conversation; on desktop the
  // conversation pane is always mounted so it only needs to reset itself once a
  // real session takes over.
  const [composingNew, setComposingNew] = useState(false);
  // Project, search and archive view survive the list unmounting (a phone
  // opens a chat by replacing the list) and a discarded tab's reload.
  const projectFilter = useChatListViewStore((s) => s.projectFilter);
  const setProjectFilter = useChatListViewStore((s) => s.setProjectFilter);
  const [switcherOpen, setSwitcherOpen] = useState(false);
  const startDirectRef = useRef<() => void>(() => {});
  const dismissProjectNudge = useDismissChatProjectNudge();
  // In-page search: titles match locally on the keystroke; what was said in a
  // chat comes from the server once typing settles. Archived chats match too.
  const search = useChatListViewStore((s) => s.search);
  const setSearch = useChatListViewStore((s) => s.setSearch);
  const query = search.trim().toLowerCase();
  const debouncedQuery = useDebouncedValue(query, 250);
  const { data: contentHits } = useQuery(chatMessageSearchOptions(c.wsId, debouncedQuery));
  const searchSnippets = useMemo(() => {
    if (!query) return null;
    const snippets = new Map<string, string>();
    // Hits for a stale query would attach the wrong snippet; wait for the
    // settled query instead.
    if (debouncedQuery === query) {
      for (const hit of contentHits ?? []) snippets.set(hit.session_id, hit.snippet);
    }
    return snippets;
  }, [contentHits, debouncedQuery, query]);
  // A restored project filter can name a project deleted since it was saved;
  // once the project list is known, fall back to the full list instead of
  // showing an empty, unexplained view.
  useEffect(() => {
    if (!c.projectsLoaded || projectFilter.type !== "project") return;
    if (c.projects.some((project) => project.id === projectFilter.id)) return;
    setProjectFilter({ type: "all" });
  }, [c.projectsLoaded, c.projects, projectFilter, setProjectFilter]);
  const visibleSessions = useMemo(() => {
    const inProject = c.sessions.filter((session) =>
      sessionMatchesChatProjectFilter(chatSessionProjectIds(session), projectFilter),
    );
    if (!searchSnippets) return inProject;
    const words = query.split(/\s+/).filter(Boolean);
    return inProject.filter(
      (session) =>
        searchSnippets.has(session.id) || chatTitleMatches(session, words, query),
    );
  }, [c.sessions, projectFilter, query, searchSnippets]);
  useEffect(() => {
    // Read the LIVE store value for the same reason as the session sync
    // effects below: under StrictMode's double-invoke this effect replays
    // with the render-captured snapshot, and a stale non-null session (a
    // persisted chat the URL→store effect already cleared) would revert the
    // composingNew=true that the later `?agent=` intent effect just set.
    if (useChatStore.getState().activeSessionId) setComposingNew(false);
  }, [c.activeSessionId]);

  // Two-way sync between the URL (`/chat/<id>`, or the older `?session=`) and
  // the chat store's activeSessionId. Both effects read the LIVE store value via
  // `useChatStore.getState()` rather than the render-captured `c.activeSessionId`.
  // That is what keeps them from fighting on mount: a naive mirror effect fires
  // with the stale (null) snapshot and "corrects" the URL by stripping the
  // session before the URL→store effect has applied — breaking deep links and
  // making selection / new-chat feel unresponsive. Reading getState() sees the
  // value the sibling effect just wrote, so the reconciliation converges in one
  // pass and is idempotent under StrictMode's double-invoke.

  // How the compact conversation was reached, which decides where its back
  // button goes (see `leaveConversation`):
  //  - "pushed": picked from the list on this page — the list is one history
  //    step behind, so the phone's back gesture and the button agree.
  //  - "inplace": a new chat composed here — the URL only turns into the
  //    session once it is sent, and never gained a list entry to step back to.
  //  - null: arrived from elsewhere (Cmd+K, a notification, another page's
  //    link), so back returns to the page that sent the person here.
  const conversationEntry = useRef<"pushed" | "inplace" | null>(null);
  // Set by a compact list pick so the store → URL sync below pushes instead of
  // replacing, giving the conversation its own step in the back stack.
  const pushNextSessionSync = useRef(false);

  // URL → store: deep link, refresh, notification click, back/forward.
  useEffect(() => {
    if (!urlSession && !composingNew) conversationEntry.current = null;
    if (urlSession !== useChatStore.getState().activeSessionId) {
      c.setActiveSession(urlSession);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- react to URL only
  }, [urlSession]);

  // store → URL: thread selection, "new chat", and sessions created by sending.
  useEffect(() => {
    const live = useChatStore.getState().activeSessionId;
    const current = chatSessionIdFromLocation(pathname, searchParams);
    const pushSync = pushNextSessionSync.current;
    pushNextSessionSync.current = false;
    if (live !== current) {
      const target = live ? wsPaths.chatSession(live) : wsPaths.chat();
      if (pushSync && live) push(target);
      else replace(target);
      return;
    }
    // An older `?session=` link opened the right chat. Move the address bar
    // onto the stable path the copy button shares.
    if (live) {
      const canonical = wsPaths.chatSession(live);
      if (pathname !== canonical) replace(canonical);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- react to store only
  }, [c.activeSessionId]);

  const { defaultLayout, onLayoutChanged } = useDefaultLayout({
    id: "multica_chat_layout",
  });

  // `?agent=` intent bookkeeping. The ref holds the param value already
  // consumed (or superseded) so the effect below fires at most once per deep
  // link — it also bridges the async window between replace() and the
  // searchParams actually dropping the param. Any explicit user action must
  // supersede a still-pending intent: agent/member queries can resolve late,
  // and a deferred intent firing after the user picked a thread (or started
  // another chat) would clobber that choice.
  const consumedAgentIntent = useRef<string | null>(null);
  const supersedeAgentIntent = () => {
    if (urlAgent) consumedAgentIntent.current = urlAgent;
  };

  const handleSelect = (session: ChatSession) => {
    supersedeAgentIntent();
    // A compact pick opens the conversation over the list; push so the back
    // gesture returns to the list instead of leaving Chat altogether.
    if (isCompact && !c.activeSessionId) {
      pushNextSessionSync.current = true;
      conversationEntry.current = "pushed";
    }
    c.handleSelectSession(session);
    setComposingNew(false);
  };

  // Compact back button. A conversation opened from the list returns to it; one
  // opened from another page returns to that page (a cold link falls back to
  // the list rather than stepping off Multica).
  const leaveConversation = () => {
    const entry = conversationEntry.current;
    conversationEntry.current = null;
    if (entry === "pushed" && c.activeSessionId) {
      back();
      return;
    }
    if (entry === "inplace" || composingNew || !c.activeSessionId) {
      c.setActiveSession(null);
      setComposingNew(false);
      return;
    }
    backOrReplace(wsPaths.chat());
  };

  // Single archive path for both entry points (thread-list row + conversation
  // header). When the archived chat is the one in view, move the pane off it:
  // on desktop advance to the next chat (Inbox-style); when compact drop back to
  // the list, which reads more naturally than being thrown into an unrelated
  // conversation full-screen. Archiving any other chat leaves the view put.
  const handleArchive = (session: ChatSession) => {
    supersedeAgentIntent();
    if (session.id === c.activeSessionId) {
      if (isCompact) {
        if (conversationEntry.current === "pushed") {
          conversationEntry.current = null;
          back();
        } else {
          c.setActiveSession(null);
          setComposingNew(false);
        }
      } else {
        c.advanceSelectionAfterArchive(session);
      }
    }
    c.archiveSession(session.id);
  };

  const startNewChat = (agent: Agent | null) => {
    // A manual ⊕ pick outranks a pending deep link; when called FROM the
    // intent effect the ref is already set to this param, so this is a no-op.
    supersedeAgentIntent();
    if (isCompact) conversationEntry.current = "inplace";
    const projectIds = draftProjectIdsForNewChat(projectFilter);
    if (agent) c.handleStartNewChat(agent, projectIds);
    else c.handleNewChat(projectIds);
    setComposingNew(true);
  };

  // "+" and the chord skip the agent picker: the chat continues with whoever
  // is already in play. The list "+" still opens the picker when there are
  // several agents, then lands in the same project.
  const startDirect = () => {
    const agent = c.activeAgent;
    if (agent && !isAgentRuntimeBound(agent)) {
      toast.error(t(($) => $.input.runtime_required_toast));
      return;
    }
    startNewChat(agent);
  };
  startDirectRef.current = startDirect;

  const changeProjectFilter = (next: ChatProjectFilter) => {
    setProjectFilter(next);
    const userId = c.user?.id ?? null;
    const land = sessionToLandOn({
      filter: next,
      sessions: c.sessions.map((session) => ({
        id: session.id,
        projectIds: chatSessionProjectIds(session),
        updatedAt: session.updated_at,
        status: session.status,
      })),
      activeSessionId: c.activeSessionId,
      rememberedSessionId: userId
        ? useChatProjectOpenStore.getState().recall(userId, next)
        : null,
    });
    if (land) {
      const session = c.sessions.find((item) => item.id === land);
      if (session) {
        handleSelect(session);
        return;
      }
    }
    // A compose that is still open has to follow the view, or the next send
    // would bind the project the person just left.
    if (!c.currentSession && composingNew) {
      c.handleProjectsChange(draftProjectIdsForNewChat(next));
    }
  };

  useEffect(() => {
    const userId = c.user?.id;
    const session = c.currentSession;
    if (!userId || !session) return;
    useChatProjectOpenStore.getState().remember(
      userId,
      chatSessionProjectIds(session),
      session.id,
    );
  }, [c.user?.id, c.currentSession]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.repeat || isImeComposing(event)) return;
      const action = chatPageShortcutAction(
        event,
        chatShortcutGates(event.target),
      );
      if (!action) return;
      event.preventDefault();
      if (action === "new-chat") startDirectRef.current();
      else setSwitcherOpen(true);
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, []);

  const changeProjectContext = (projectIds: string[]) => {
    c.handleProjectsChange(projectIds);
    // Attaching or detaching a project now always stays in the current
    // conversation (DENE-522) — no affordance here starts a clean session any
    // more. Only a compose-pane selection still needs the compact layout
    // pinned to the composer.
    if (!c.currentSession) setComposingNew(true);
  };

  // URL → new chat: `?agent=<id>` is the deep link used by "DM" entry points
  // (e.g. the agent detail page) to land on a fresh compose bound to that
  // agent. The permission-filtered agent list loads async, so the intent is
  // consumed on the render where the agent resolves, then the param is
  // stripped so refresh / the session sync above don't replay it. The ref
  // resets once the param is gone so a later identical deep link fires again.
  // A settled miss (access revoked, agent archived, bad id) is a denial: it
  // explains itself with a toast and consumes the intent so a later refetch
  // that surfaces the agent cannot start a chat without a fresh click. While
  // the queries are still loading the intent simply stays pending.
  useEffect(() => {
    if (!urlAgent) {
      consumedAgentIntent.current = null;
      return;
    }
    if (consumedAgentIntent.current === urlAgent) return;
    const agent = c.availableAgents.find((a) => a.id === urlAgent);
    if (agent) {
      consumedAgentIntent.current = urlAgent;
      startNewChat(agent);
      replace(wsPaths.chat());
      return;
    }
    if (c.agentsSettled) {
      consumedAgentIntent.current = urlAgent;
      toast.error(t(($) => $.page.agent_link_no_access));
      replace(wsPaths.chat());
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- consume when the URL param or the resolving agent list changes
  }, [urlAgent, c.availableAgents, c.agentsSettled]);

  // URL → new chat that sends: `?prompt=<text>` (DENE-975, the inbox page's
  // "walk me through it") opens a fresh chat with the agent already in play
  // and sends the text once that agent resolves. When it cannot be sent (no
  // agent, no runtime, a refused send) the text is left in the composer
  // instead. The ref keeps StrictMode's double effect from sending twice.
  const urlPrompt = searchParams.get("prompt") || null;
  const consumedPrompt = useRef<string | null>(null);
  const [queuedPrompt, setQueuedPrompt] = useState<{ id: number; text: string } | null>(null);
  const sentPrompt = useRef<number | null>(null);
  useEffect(() => {
    if (!urlPrompt) {
      consumedPrompt.current = null;
      return;
    }
    if (consumedPrompt.current === urlPrompt) return;
    consumedPrompt.current = urlPrompt;
    startNewChat(null);
    setQueuedPrompt({ id: Date.now(), text: urlPrompt });
    replace(wsPaths.chat());
    // eslint-disable-next-line react-hooks/exhaustive-deps -- react to the URL only
  }, [urlPrompt]);
  useEffect(() => {
    if (!queuedPrompt || sentPrompt.current === queuedPrompt.id || c.activeSessionId) return;
    if (!c.agentsSettled && !c.activeAgent) return;
    sentPrompt.current = queuedPrompt.id;
    const { text } = queuedPrompt;
    setQueuedPrompt(null);
    if (!c.activeAgent || !c.isAgentRuntimeBound) {
      c.prefillConversationStarter(text);
      return;
    }
    void c.handleSend(text).then((sent) => {
      if (!sent) c.prefillConversationStarter(text);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps -- send once the new chat and its agent are in place
  }, [queuedPrompt, c.activeSessionId, c.activeAgent, c.agentsSettled, c.isAgentRuntimeBound]);

  const newChatChord = useShortcut("newChat");
  const newChatButton = (
    <NewChatButton
      agents={c.availableAgents}
      userId={c.user?.id}
      onStart={startNewChat}
      side="bottom"
      shortcut={newChatChord}
    />
  );
  const directNewChatButton = (
    <DirectNewChatButton onClick={startDirect} shortcut={newChatChord} />
  );

  const switchProjects = useMemo(() => {
    const projects = c.projects ?? [];
    const ranked = rankChatProjects(
      projects.map((project) => project.id),
      [],
      c.sessions.map((session) => ({
        projectIds: chatSessionProjectIds(session),
        updatedAt: session.updated_at,
        status: session.status,
        hasUnread: !!session.has_unread,
      })),
    );
    const recentAt = new Map<string, number>();
    for (const row of [...ranked.pinned, ...ranked.rest]) recentAt.set(row.id, row.recentAt);
    return orderProjectsForQuickSwitch(
      projects.map((project) => ({ id: project.id, title: project.title })),
      recentAt,
    );
  }, [c.projects, c.sessions]);

  const browsedProjectTitle =
    projectFilter.type === "project"
      ? (c.projects ?? []).find((project) => project.id === projectFilter.id)?.title
      : null;
  const projectCaption =
    projectFilter.type === "project"
      ? browsedProjectTitle
        ? t(($) => $.page.new_chat_in_project, { title: browsedProjectTitle })
        : null
      : t(($) => $.page.new_chat_unbound);

  const projectSwitcher = (
    <ChatProjectSwitcher
      open={switcherOpen}
      onOpenChange={setSwitcherOpen}
      projects={switchProjects}
      filter={projectFilter}
      onSelect={changeProjectFilter}
    />
  );

  const listHeader = (
    <PageHeader>
      <h1 className="flex-1 text-body font-semibold">{t(($) => $.page.title)}</h1>
      {newChatButton}
    </PageHeader>
  );

  const searchBox = (
    <div className="shrink-0 px-3 pt-2 pb-1">
      <PageSearchInput
        value={search}
        onChange={setSearch}
        placeholder={t(($) => $.page.search_placeholder)}
        clearLabel={t(($) => $.page.search_clear)}
        className="w-full"
      />
    </div>
  );

  const projectBar = (
    <ChatProjectBar
      projects={c.projects ?? []}
      sessions={c.sessions}
      userId={c.user?.id ?? null}
      filter={projectFilter}
      onFilterChange={changeProjectFilter}
      onOpenSwitcher={() => setSwitcherOpen(true)}
    />
  );

  const listBody = (
    <div className="px-2 py-1">
      <ChatThreadList
        sessions={visibleSessions}
        sessionsLoaded={c.sessionsLoaded}
        agents={c.agents}
        activeSessionId={c.activeSessionId}
        onSelectSession={handleSelect}
        onArchive={handleArchive}
        search={searchSnippets ? { query, snippets: searchSnippets } : undefined}
        collapseHistory={projectFilter.type === "all"}
        emptyLabel={
          searchSnippets
            ? t(($) => $.page.search_empty)
            : projectFilter.type === "all"
              ? undefined
              : t(($) => $.project_bar.empty)
        }
      />
      {/* Below the conversations and outside them: an alignment is a different
          kind of thing, kept out of the chat list by the access boundary its
          hidden carrier sits behind, so it gets its own group rather than a
          tag on rows it can never appear among (DENE-371). */}
      <AlignmentRecords wsId={c.wsId} />
    </div>
  );

  // Compact only: the conversation replaces the list, so it needs a way back.
  // With a session open it sits inside the session header — one 48px bar, not
  // a back bar stacked on the header; a new chat has no header yet, so it keeps
  // a bar of its own below.
  const compactBackButton = (
    <Button
      variant="ghost"
      size="icon-sm"
      onClick={leaveConversation}
      aria-label={t(($) => $.page.back)}
      className="-ml-2 shrink-0 text-muted-foreground"
    >
      <ArrowLeft className="h-4 w-4" />
    </Button>
  );

  // The conversation pane: message list / skeleton / empty above a persistent
  // banner + input. Identical composition to the floating window's body, so a
  // brand-new chat (no active session) shows the agent-aware empty state + input.
  // No compose-box agent selector — the agent is fixed when the chat starts.
  // `@container`: the conversation column's gutter (CHAT_GUTTER) widens with
  // THIS pane, which the user resizes independently of the browser window.
  const queuedTasks = c.pendingTask?.queued_tasks ?? [];
  const conversation = (
    <div className="flex flex-1 flex-col min-h-0 @container">
      {!c.currentSession && !isCompact && (
        <div className="flex h-12 shrink-0 items-center justify-end border-b px-2">
          {directNewChatButton}
        </div>
      )}
      {c.currentSession && (
        <ChatSessionHeader
          leading={isCompact ? compactBackButton : undefined}
          trailing={directNewChatButton}
          session={c.currentSession}
          agent={c.activeAgent}
          onArchive={handleArchive}
          loadAllMessages={() =>
            c.hasOlderMessages
              ? queryClient.fetchQuery(chatMessagesOptions(c.currentSession!.id))
              : Promise.resolve(c.messages)
          }
        />
      )}
      {c.currentSession && (
        <ChatProjectNudge
          session={c.currentSession}
          onBind={changeProjectContext}
          onDismiss={() => dismissProjectNudge.mutate(c.currentSession!.id)}
          dismissing={dismissProjectNudge.isPending}
        />
      )}
      {c.currentSession && <div className="flex shrink-0 px-3 py-1"><WorkThreadPanel kind="chat" id={c.currentSession.id} /></div>}
      {c.showSkeleton ? (
        <ChatMessageSkeleton />
      ) : c.hasMessages ? (
        <ChatMessageList
          key={c.activeSessionId}
          messages={c.messages}
          pendingTask={c.pendingTask}
          availability={c.availability}
          firstItemIndex={c.firstItemIndex}
          hasOlderMessages={c.hasOlderMessages}
          isFetchingOlderMessages={c.isFetchingOlderMessages}
          onLoadOlderMessages={() => void c.fetchOlderMessages()}
          onQuickAction={(action) => c.handleSend(action.prompt)}
          creatorId={c.currentSession?.creator_id}
          quickActionsDisabled={
            !!c.pendingTaskId ||
            c.isSessionArchived ||
            c.isAgentArchived ||
            c.isAgentAccessRevoked ||
            c.isChatViewOnly ||
            !c.isAgentRuntimeBound ||
            c.noAgent
          }
          onRegenerateQuickActions={(message) =>
            c.activeSessionId
              ? regenerateQuickActions.mutateAsync({
                  sessionId: c.activeSessionId,
                  messageId: message.id,
                })
              : undefined
          }
          quickActionsPendingMessageId={quickActionsPending?.message_id ?? null}
        />
      ) : (
        <EmptyState
          agent={c.activeAgent}
          hasSessions={c.sessions.length > 0}
          onPickPrompt={c.prefillConversationStarter}
          customizeHref={c.customizeConversationStartersHref}
        />
      )}

      {c.isChatViewOnly ? (
        <p className="px-4 py-2 text-caption text-muted-foreground">
          {t(($) => $.sharing.view_only)}
        </p>
      ) : c.isAgentAccessRevoked ? (
        <AgentAccessRevokedBanner agentName={c.activeAgent?.name} />
      ) : c.noAgent ? (
        <NoAgentBanner />
      ) : c.isAgentArchived ? (
        <ArchivedAgentBanner agentName={c.activeAgent?.name} />
      ) : !c.isAgentRuntimeBound && c.activeAgent ? (
        <RuntimeRequiredBanner
          agentId={c.activeAgent.id}
          agentName={c.activeAgent.name}
        />
      ) : (
        <OfflineBanner agentName={c.activeAgent?.name} availability={c.availability} />
      )}

      <ChatQueue
        tasks={queuedTasks}
        headStatus={c.pendingTask?.status}
        onSendNow={c.handleSendQueuedTaskNow}
        sendNowDisabled={c.isAgentAccessRevoked}
        onEdit={c.handleEditQueuedTask}
        onRemove={c.handleRemoveQueuedTask}
        onClear={c.handleClearQueuedTasks}
      />

      {projectCaption && (
        <div className={cn(CHAT_GUTTER, "pb-1")} data-slot="chat-new-chat-project">
          <p className={cn(CHAT_COLUMN, "text-caption text-muted-foreground")}>{projectCaption}</p>
        </div>
      )}

      <ChatInput
        onSend={c.handleSend}
        restoreDraftRequest={c.restoreDraftRequest}
        conversationStarterRequest={c.conversationStarterRequest}
        onConversationStarterApplied={c.handleConversationStarterApplied}
        onRestoreDraftApplied={c.handleRestoreDraftApplied}
        uploadEnabled={c.uploadEnabled && !c.isAgentAccessRevoked}
        onStop={c.handleStop}
        isRunning={!!c.pendingTaskId}
        allowSubmitWhileRunning={c.pendingTask?.supports_queue === true}
        disabled={
          c.isSessionArchived ||
          c.isAgentArchived ||
          c.isAgentAccessRevoked ||
          c.isChatViewOnly ||
          !c.isAgentRuntimeBound
        }
        noAgent={c.noAgent}
        agentArchived={c.isAgentArchived}
        agentAccessRevoked={c.isAgentAccessRevoked}
        agentRuntimeRequired={!c.isAgentRuntimeBound}
        agentName={c.activeAgent?.name}
        projects={c.projects}
        projectIds={c.activeProjectIds}
        projectContextUnsupported={c.projectContextUnsupported}
        onProjectsChange={changeProjectContext}
        isProjectUpdating={c.isProjectUpdating}
        focusRequest={c.focusInputRequest}
      />
    </div>
  );

  // -- Compact: list / conversation toggle -----------------------------------
  if (isCompact) {
    if (c.activeSessionId || composingNew) {
      return (
        <div className="flex flex-1 flex-col min-h-0">
          {!c.currentSession && (
            <div className="flex h-12 shrink-0 items-center gap-1 border-b px-2">
              <Button
                variant="ghost"
                size="sm"
                onClick={leaveConversation}
                className="gap-1.5 text-muted-foreground"
              >
                <ArrowLeft className="h-4 w-4" />
                {t(($) => $.page.title)}
              </Button>
              <div className="flex-1" />
              {directNewChatButton}
            </div>
          )}
          {conversation}
          {projectSwitcher}
        </div>
      );
    }
    return (
      <div className="flex flex-1 flex-col min-h-0">
        {listHeader}
        {searchBox}
        {projectBar}
        <div
          ref={restoreListScroll}
          data-tab-scroll-root="chat-list"
          className="flex-1 min-h-0 overflow-y-auto"
        >
          {listBody}
        </div>
        {projectSwitcher}
      </div>
    );
  }

  // -- Desktop: resizable two-panel. The conversation pane appears only once
  // there is a chat target — an open thread or a new chat whose agent was just
  // picked via ⊕. With nothing selected there is no agent, so we show a neutral
  // prompt instead of an orphaned compose box. -------------------------------
  const hasTarget = !!c.activeSessionId || composingNew;
  return (
    <>
    <ResizablePanelGroup
      orientation="horizontal"
      className="flex-1 min-h-0"
      defaultLayout={defaultLayout}
      onLayoutChanged={onLayoutChanged}
    >
      <ResizablePanel
        id="list"
        defaultSize={260}
        minSize={240}
        maxSize={480}
        groupResizeBehavior="preserve-pixel-size"
      >
        <div className="flex flex-col border-r h-full">
          {listHeader}
          {searchBox}
          {projectBar}
          <div
          ref={restoreListScroll}
          data-tab-scroll-root="chat-list"
          className="flex-1 min-h-0 overflow-y-auto"
        >
          {listBody}
        </div>
        </div>
      </ResizablePanel>
      <ResizableHandle />
      <ResizablePanel id="detail" minSize="40%">
        <div className="flex flex-col min-h-0 h-full">
          {hasTarget ? (
            conversation
          ) : (
            <div className="flex h-full min-h-0 flex-col">
              <div className="flex h-12 shrink-0 items-center justify-end border-b px-2">
                {directNewChatButton}
              </div>
              <div className="flex flex-1 flex-col items-center justify-center gap-3 text-muted-foreground">
                <MessageSquare className="h-10 w-10 text-faint-foreground" />
                <p className="text-body">{t(($) => $.page.select_prompt)}</p>
                {projectCaption && (
                  <p className="max-w-sm px-6 text-center text-caption">{projectCaption}</p>
                )}
              </div>
            </div>
          )}
        </div>
      </ResizablePanel>
    </ResizablePanelGroup>
    {projectSwitcher}
    </>
  );
}
