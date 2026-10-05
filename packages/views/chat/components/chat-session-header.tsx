"use client";

import { useEffect, useRef, useState, type ReactNode } from "react";
import { toast } from "sonner";
import {
  Archive,
  ArchiveRestore,
  ArrowRightLeft,
  Copy,
  Globe,
  Link2,
  LockKeyhole,
  MoreHorizontal,
  Pencil,
  Trash2,
  UserRound,
  Users,
} from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { copyText } from "@multica/ui/lib/clipboard";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  useUpdateChatSession,
  useDeleteChatSession,
  useSetChatSessionArchived,
  useHandoffChatSession,
} from "@multica/core/chat/mutations";
import { useChatStore } from "@multica/core/chat";
import type { Agent, ChatMessage, ChatSession } from "@multica/core/types";
import { isImeComposing } from "@multica/core/utils";
import { ActorAvatar } from "../../common/actor-avatar";
import { AppLink, useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { useAuthStore } from "@multica/core/auth";
import { conversationToMarkdown } from "../lib/copy-text";
import { ProgressLine } from "../../common/progress-line";
import { PrivateLinkPrompt, SharingHoverCard } from "../../common/sharing-guide";
import { ChatAccessDialog } from "./chat-access-dialog";

/**
 * Per-session header for the conversation pane: agent avatar + chat title +
 * agent subtitle, with a ⋯ menu (rename / view agent profile / delete). The
 * title itself is not clickable — renaming lives only in the ⋯ menu.
 * The avatar's hover card is the lightweight "view profile" affordance; the
 * menu item navigates to the full agent page.
 */
export function ChatSessionHeader({
  leading,
  trailing,
  session,
  agent,
  onArchive,
  loadAllMessages,
  handoffAgents = [],
}: {
  // Host-supplied control before the avatar — the compact Chat page's way back,
  // so a phone gets one header bar instead of a back bar stacked on this one.
  leading?: ReactNode;
  // Host-supplied control at the far right — the chat page's new-chat button.
  trailing?: ReactNode;
  session: ChatSession;
  agent: Agent | null;
  // Archiving the open conversation must move the pane off it (advance to the
  // next chat on desktop, back to the list on mobile), so the parent owns it —
  // see ChatPage.handleArchive. Falls back to a plain status flip if unwired.
  onArchive?: (session: ChatSession) => void;
  // Full transcript for "copy conversation". The open pane may only have the
  // recent page loaded; the parent fetches the rest when older messages exist.
  loadAllMessages?: () => Promise<ChatMessage[]>;
  // Agents this chat can be handed to (DENE-1350); the current one is skipped.
  handoffAgents?: Agent[];
}) {
  const { t } = useT("chat");
  const currentUserId = useAuthStore((s) => s.user?.id ?? null);
  const canManage = session.access === "owner" || session.creator_id === currentUserId;
  const { t: tc } = useT("common");
  const wsId = useWorkspaceId();
  // Same query the access dialog reads, so the button, its hover card and the
  // dialog agree; can_edit is the server's answer, not a client copy of it.
  const { data: accessSettings } = useQuery({
    queryKey: ["chat", wsId, "access", session.id],
    queryFn: () => api.getChatAccess(session.id),
  });
  const accessMode =
    accessSettings?.mode ??
    (session.visibility === "private"
      ? "private"
      : (session.extra_count ?? 0) > 0
        ? "extra"
        : "project");
  const canEditAccess = accessSettings?.can_edit;
  // Until the server answers, the session's own access field is the best guess.
  const canOpenAccess = canEditAccess ?? canManage;
  const [accessOpen, setAccessOpen] = useState(false);
  const [privateLinkOpen, setPrivateLinkOpen] = useState(false);
  const shareLabel = t(($) => $.sharing.trigger[accessMode]);
  const shareDescription = t(($) => $.sharing[`header_${accessMode}` as const]);
  const ShareIcon = accessMode === "private" ? LockKeyhole : accessMode === "workspace" ? Globe : Users;
  const { getShareableUrl } = useNavigation();
  const wsPaths = useWorkspacePaths();
  const updateSession = useUpdateChatSession();
  const deleteSession = useDeleteChatSession();
  const setArchived = useSetChatSessionArchived();
  const setActiveSession = useChatStore((s) => s.setActiveSession);

  const isArchived = session.status === "archived";

  const [editing, setEditing] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [draft, setDraft] = useState(session.title ?? "");
  const inputRef = useRef<HTMLInputElement>(null);
  const isComposingRef = useRef(false);
  // A browser can blur the input before it emits compositionend. Remember
  // that intent so the final composed value is committed, not the draft.
  const commitAfterCompositionRef = useRef(false);
  const blurCommitTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const title = session.title?.trim() || t(($) => $.window.untitled);

  useEffect(() => {
    if (editing) {
      inputRef.current?.focus();
      inputRef.current?.select();
    }
  }, [editing]);

  useEffect(
    () => () => {
      if (blurCommitTimeoutRef.current !== null) {
        clearTimeout(blurCommitTimeoutRef.current);
      }
    },
    [],
  );

  const clearPendingBlurCommit = () => {
    if (blurCommitTimeoutRef.current === null) return;
    clearTimeout(blurCommitTimeoutRef.current);
    blurCommitTimeoutRef.current = null;
  };

  const startRename = () => {
    clearPendingBlurCommit();
    isComposingRef.current = false;
    commitAfterCompositionRef.current = false;
    setDraft(session.title ?? "");
    setEditing(true);
  };

  const commitRename = (raw = draft) => {
    clearPendingBlurCommit();
    isComposingRef.current = false;
    commitAfterCompositionRef.current = false;
    setEditing(false);
    const trimmed = raw.trim();
    if (!trimmed || trimmed === session.title) return;
    updateSession.mutate({ sessionId: session.id, title: trimmed });
  };

  const doDelete = () => {
    setConfirmDelete(false);
    setActiveSession(null);
    deleteSession.mutate(session.id);
  };

  const doArchive = () =>
    onArchive
      ? onArchive(session)
      : setArchived.mutate({ sessionId: session.id, archived: true });
  const doUnarchive = () => setArchived.mutate({ sessionId: session.id, archived: false });

  // Only the owner can hand a chat over; the server opens the new chat as them.
  const handoffTargets = canManage ? handoffAgents.filter((a) => a.id !== session.agent_id) : [];
  const handoff = useHandoffChatSession();
  const doHandoff = (target: Agent) => {
    if (handoff.isPending) return;
    handoff.mutate(
      { sessionId: session.id, to: target.id },
      {
        onSuccess: (res) => {
          toast.success(t(($) => $.header.handoff_done, { name: target.name }));
          setActiveSession(res.session.id);
        },
        onError: (err) => {
          toast.error(err instanceof Error && err.message ? err.message : t(($) => $.header.handoff_failed));
        },
      },
    );
  };
  const handoffItems = handoffTargets.map((target) => (
    <DropdownMenuItem key={target.id} disabled={handoff.isPending} onClick={() => doHandoff(target)} className="max-sm:min-h-11">
      <ActorAvatar actorType="agent" actorId={target.id} size="xs" />
      <span className="min-w-0 truncate">{target.name}</span>
    </DropdownMenuItem>
  ));
  // A menu label must sit in a group, or the menu throws when it opens.
  const handoffGroup = (
    <DropdownMenuGroup>
      <DropdownMenuLabel className="text-caption font-normal text-muted-foreground">
        {t(($) => $.header.handoff_hint)}
      </DropdownMenuLabel>
      {handoffItems}
    </DropdownMenuGroup>
  );

  const copySessionLink = async (force = false) => {
    // A private chat opens as "can't open this" for whoever receives the link,
    // so say so first (DENE-1214).
    if (!force && accessMode === "private") {
      setPrivateLinkOpen(true);
      return;
    }
    const url = getShareableUrl(wsPaths.chatSession(session.id));
    if (await copyText(url)) {
      toast.success(t(($) => $.header.link_copied));
    } else {
      toast.error(t(($) => $.message_list.copy_failed_toast));
    }
  };

  const copyConversation = async () => {
    try {
      const messages = (await loadAllMessages?.()) ?? [];
      const markdown = conversationToMarkdown(title, messages, {
        user: t(($) => $.header.copy_role_user),
        assistant: t(($) => $.header.copy_role_assistant),
      });
      if (await copyText(markdown)) {
        toast.success(t(($) => $.header.conversation_copied));
      } else {
        toast.error(t(($) => $.message_list.copy_failed_toast));
      }
    } catch {
      toast.error(t(($) => $.message_list.copy_failed_toast));
    }
  };

  return (
    <div className="flex h-12 shrink-0 items-center gap-3 border-b px-4">
      {leading}
      {agent ? (
        <ActorAvatar actorType="agent" actorId={agent.id} size="lg" enableHoverCard showStatusDot />
      ) : (
        <span className="size-[30px] shrink-0" />
      )}

      <div className="min-w-0 flex-1">
        {editing ? (
          <input
            ref={inputRef}
            value={draft}
            maxLength={200}
            aria-label={t(($) => $.header.rename)}
            onChange={(e) => setDraft(e.target.value)}
            onCompositionStart={() => {
              isComposingRef.current = true;
            }}
            onCompositionEnd={(e) => {
              isComposingRef.current = false;
              if (!commitAfterCompositionRef.current) return;
              commitRename(e.currentTarget.value);
            }}
            onBlur={(e) => {
              if (isComposingRef.current) {
                commitAfterCompositionRef.current = true;
                const input = e.currentTarget;
                // Let a compositionend queued by the same focus change win.
                // If it never arrives, commit on the next task instead of
                // leaving an unfocused editor open indefinitely.
                clearPendingBlurCommit();
                blurCommitTimeoutRef.current = setTimeout(() => {
                  if (!commitAfterCompositionRef.current) return;
                  // Without compositionend, the current DOM value is not
                  // known to be final. Close without persisting a partial
                  // composition instead of recreating the original bug.
                  if (isComposingRef.current) {
                    clearPendingBlurCommit();
                    isComposingRef.current = false;
                    commitAfterCompositionRef.current = false;
                    setEditing(false);
                    return;
                  }
                  commitRename(input.value);
                }, 0);
                return;
              }
              commitRename(e.currentTarget.value);
            }}
            onKeyDown={(e) => {
              if (isImeComposing(e)) return;
              if (e.key === "Enter") {
                e.preventDefault();
                commitRename();
              } else if (e.key === "Escape") {
                e.preventDefault();
                clearPendingBlurCommit();
                isComposingRef.current = false;
                commitAfterCompositionRef.current = false;
                setEditing(false);
              }
            }}
            className="w-full rounded-sm bg-background px-1 py-0.5 text-body font-semibold outline-none ring-1 ring-border focus-visible:ring-brand"
          />
        ) : (
          <div className="min-w-0"><div className="flex items-center gap-1 truncate text-body font-semibold text-foreground">{title}{session.title_locked && <span aria-label={t(($) => $.title_locked)} title={t(($) => $.title_locked)} className="text-micro text-muted-foreground">🔒</span>}</div><ProgressLine progress={session.progress} /></div>
        )}
        {(agent || session.agent_name) && (
          <div className="truncate text-caption text-muted-foreground">
            {agent?.name || session.agent_name}
            {agent?.description ? ` · ${agent.description}` : ""}
          </div>
        )}
      </div>

      <SharingHoverCard
        trigger={
          <Button
            variant="ghost"
            size="sm"
            data-testid="chat-share-trigger"
            aria-disabled={canEditAccess === false || undefined}
            className={cn(
              "h-7 gap-1.5 px-2 text-caption text-muted-foreground",
              canEditAccess === false && "cursor-not-allowed aria-disabled:opacity-60",
            )}
            onClick={canOpenAccess ? () => setAccessOpen(true) : undefined}
            aria-label={`${t(($) => $.sharing.title)}: ${shareLabel}`}
          >
            <ShareIcon className="size-3.5" aria-hidden="true" />
            <span className="hidden sm:inline">{shareLabel}</span>
          </Button>
        }
        title={shareLabel}
        description={shareDescription}
        canChange={canEditAccess}
        changeLine={
          canEditAccess === undefined
            ? undefined
            : canEditAccess
              ? tc(($) => $.share_guide.can_change)
              : tc(($) => $.share_guide.cannot_change_not_creator.chat)
        }
      />

      {handoffTargets.length > 0 && (
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button
                variant="ghost"
                size="sm"
                data-testid="chat-handoff-trigger"
                className="hidden h-7 gap-1.5 px-2 text-caption text-muted-foreground sm:inline-flex"
              />
            }
          >
            <ArrowRightLeft className="size-3.5" aria-hidden="true" />
            {t(($) => $.header.handoff)}
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="max-h-80 w-64 overflow-y-auto">
            {handoffGroup}
          </DropdownMenuContent>
        </DropdownMenu>
      )}

      <Button
        variant="ghost"
        size="icon-sm"
        className="text-muted-foreground"
        onClick={() => void copySessionLink()}
        data-testid="chat-copy-link"
        aria-label={t(($) => $.header.copy_link)}
        title={t(($) => $.header.copy_link)}
      >
        <Link2 className="h-4 w-4" />
      </Button>

      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <Button
              variant="ghost"
              size="icon-sm"
              className="text-muted-foreground"
              aria-label={t(($) => $.list.row_actions_aria)}
            />
          }
        >
          <MoreHorizontal className="h-4 w-4" />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-auto">
          {canManage && (
            <DropdownMenuItem onClick={startRename}>
              <Pencil className="h-4 w-4" />
              {t(($) => $.header.rename)}
            </DropdownMenuItem>
          )}
          {handoffTargets.length > 0 && (
            // Phones have no room for the header button; it lives here instead.
            <DropdownMenuSub>
              <DropdownMenuSubTrigger className="sm:hidden max-sm:min-h-11">
                <ArrowRightLeft className="h-4 w-4" />
                {t(($) => $.header.handoff)}
              </DropdownMenuSubTrigger>
              <DropdownMenuSubContent className="max-h-80 w-56 overflow-y-auto">
                {handoffGroup}
              </DropdownMenuSubContent>
            </DropdownMenuSub>
          )}
          <DropdownMenuItem onClick={() => void copyConversation()}>
            <Copy className="h-4 w-4" />
            {t(($) => $.header.copy_conversation)}
          </DropdownMenuItem>
          {agent && (
            <DropdownMenuItem
              render={<AppLink href={wsPaths.agentDetail(agent.id)} />}
            >
              <UserRound className="h-4 w-4" />
              {t(($) => $.header.view_profile)}
            </DropdownMenuItem>
          )}
          {canManage && <DropdownMenuSeparator />}
          {canManage &&
            (isArchived ? (
              <>
                <DropdownMenuItem onClick={doUnarchive}>
                  <ArchiveRestore className="h-4 w-4" />
                  {t(($) => $.header.unarchive)}
                </DropdownMenuItem>
                <DropdownMenuItem variant="destructive" onClick={() => setConfirmDelete(true)}>
                  <Trash2 className="h-4 w-4" />
                  {t(($) => $.header.delete)}
                </DropdownMenuItem>
              </>
            ) : (
              <DropdownMenuItem onClick={doArchive}>
                <Archive className="h-4 w-4" />
                {t(($) => $.header.archive)}
              </DropdownMenuItem>
            ))}
        </DropdownMenuContent>
      </DropdownMenu>
      {trailing}

      <ChatAccessDialog session={session} open={accessOpen} onOpenChange={setAccessOpen} />
      <PrivateLinkPrompt
        open={privateLinkOpen}
        onOpenChange={setPrivateLinkOpen}
        kind="chat"
        canChange={canEditAccess === true}
        onCopyAnyway={() => {
          setPrivateLinkOpen(false);
          void copySessionLink(true);
        }}
        onChangeScope={() => {
          setPrivateLinkOpen(false);
          setAccessOpen(true);
        }}
      />

      <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.session_history.delete_dialog.title)}</AlertDialogTitle>
            <AlertDialogDescription>{title}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t(($) => $.session_history.delete_dialog.cancel)}</AlertDialogCancel>
            <AlertDialogAction
              onClick={doDelete}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {t(($) => $.session_history.delete_dialog.confirm)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
