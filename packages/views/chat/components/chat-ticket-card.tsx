"use client";

import { MoreHorizontal } from "lucide-react";
import { toast } from "sonner";
import type { ChatTicket } from "@multica/core/types";
import { useAuthStore } from "@multica/core/auth";
import { statusCategoryOfKey } from "@multica/core/issues";
import { useUpdateIssue } from "@multica/core/issues/mutations";
import { useSetChatTicket } from "@multica/core/chat/mutations";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { AppLink } from "../../navigation";
import { StatusIcon } from "../../issues/components/status-icon";
import { useStatusLabel } from "../../issues/utils/status-label";
import { useT } from "../../i18n";

/** The one-line caption saying how a ticket came to be in this chat. */
export function useChatTicketSourceLabel() {
  const { t } = useT("chat");
  return (source: ChatTicket["source"]) =>
    source === "auto"
      ? t(($) => $.tickets.source_auto)
      : source === "manual"
        ? t(($) => $.tickets.source_manual)
        : t(($) => $.tickets.source_created);
}

/**
 * The issues a chat turn opened or followed (DENE-1665, DENE-1719): where each
 * came from, who has it, why, and its live status, with the corrections a
 * reader makes without leaving the chat. Issues are dispatched as they are
 * opened; this card is where a wrong dispatch gets undone, not a confirmation
 * step.
 */
export function ChatTicketCard({
  wsId,
  sessionId,
  tickets,
}: {
  wsId: string;
  sessionId: string;
  tickets: ChatTicket[];
}) {
  const { t } = useT("chat");
  if (tickets.length === 0) return null;
  // "Opened N" is only true when this chat opened every one of them.
  const allCreated = tickets.every((ticket) => ticket.source === "created");
  return (
    <div className="mt-2 rounded-md border text-body" data-testid="chat-ticket-card">
      <div className="px-3 pt-2 pb-1 text-caption text-muted-foreground">
        {allCreated
          ? t(($) => $.tickets.heading, { count: tickets.length })
          : t(($) => $.tickets.heading_mixed, { count: tickets.length })}
      </div>
      <ul className="pb-1">
        {tickets.map((ticket) => (
          <ChatTicketRow key={ticket.id} wsId={wsId} sessionId={sessionId} ticket={ticket} />
        ))}
      </ul>
    </div>
  );
}

function ChatTicketRow({ wsId, sessionId, ticket }: { wsId: string; sessionId: string; ticket: ChatTicket }) {
  const { t } = useT("chat");
  const paths = useWorkspacePaths();
  const statusLabel = useStatusLabel(wsId);
  const userId = useAuthStore((s) => s.user?.id ?? null);
  const update = useUpdateIssue();
  const unpin = useSetChatTicket();
  const sourceLabel = useChatTicketSourceLabel();

  const category = statusCategoryOfKey(ticket.status);
  const settled = category === "done" || category === "closed";
  const unassigned = !ticket.assignee_type || !ticket.assignee_id;
  const mine = ticket.assignee_type === "member" && ticket.assignee_id === userId;
  // A settled ticket nobody holds is waiting on no one.
  const owner = unassigned
    ? settled ? "" : t(($) => $.tickets.routing)
    : t(($) => $.tickets.to, { name: ticket.assignee_name || ticket.identifier });
  const detail = [sourceLabel(ticket.source), statusLabel(ticket.status), owner, ticket.goal].filter(Boolean).join(" · ");

  const run = (data: Parameters<typeof update.mutate>[0], done: string) =>
    update.mutate(data, {
      onSuccess: () => toast.success(done),
      onError: () => toast.error(t(($) => $.tickets.failed, { id: ticket.identifier })),
    });

  // Taking a ticket off the chat leaves the issue as it is.
  const remove = () =>
    unpin.mutate(
      { sessionId, issue: ticket.id, remove: true },
      {
        onSuccess: () => toast.success(t(($) => $.tickets.removed, { id: ticket.identifier })),
        onError: () => toast.error(t(($) => $.tickets.remove_failed, { id: ticket.identifier })),
      },
    );

  return (
    <li className="flex items-center gap-2 px-3 py-1.5 max-sm:min-h-11">
      <StatusIcon status={ticket.status} className="size-3.5 shrink-0" />
      <AppLink href={paths.issueDetail(ticket.id)} className="group/ticket min-w-0 flex-1">
        <div className="flex min-w-0 items-baseline gap-1.5">
          <span className="shrink-0 tabular-nums text-muted-foreground">{ticket.identifier}</span>
          <span className="truncate font-medium group-hover/ticket:underline">{ticket.title}</span>
        </div>
        <div className="truncate text-caption text-muted-foreground">{detail}</div>
      </AppLink>
      {sessionId && (
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button
                type="button"
                variant="ghost"
                size="icon-xs"
                className="shrink-0 text-muted-foreground max-sm:size-11"
                disabled={update.isPending || unpin.isPending}
                aria-label={t(($) => $.tickets.actions, { id: ticket.identifier })}
              >
                <MoreHorizontal className="size-4" />
              </Button>
            }
          />
          <DropdownMenuContent align="end" className="w-auto">
            {!settled && !mine && userId && (
              <DropdownMenuItem
                onClick={() =>
                  run(
                    { id: ticket.id, assignee_type: "member", assignee_id: userId },
                    t(($) => $.tickets.assigned_me, { id: ticket.identifier }),
                  )
                }
              >
                {t(($) => $.tickets.assign_me)}
              </DropdownMenuItem>
            )}
            {!settled && !(unassigned && ticket.status === "todo") && (
              <DropdownMenuItem
                onClick={() =>
                  run(
                    { id: ticket.id, assignee_type: null, assignee_id: null, status: "todo" },
                    t(($) => $.tickets.assigned_agent, { id: ticket.identifier }),
                  )
                }
              >
                {t(($) => $.tickets.assign_agent)}
              </DropdownMenuItem>
            )}
            <DropdownMenuItem onClick={remove}>{t(($) => $.tickets.remove)}</DropdownMenuItem>
            {!settled && (
              <DropdownMenuItem
                variant="destructive"
                onClick={() =>
                  run(
                    { id: ticket.id, status: "cancelled" },
                    t(($) => $.tickets.withdrawn, { id: ticket.identifier }),
                  )
                }
              >
                {t(($) => $.tickets.withdraw)}
              </DropdownMenuItem>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      )}
    </li>
  );
}

/**
 * Hangs each ticket under the reply of the turn that brought it in: the first
 * assistant message created at or after the ticket joined the chat (opened,
 * followed or pinned). A ticket brought in by a turn still running has no such
 * reply yet and goes to `tail`, the list's last row.
 */
export function groupChatTickets(
  messages: { id: string; role: string; created_at: string }[],
  tickets: ChatTicket[],
): { byMessage: Map<string, ChatTicket[]>; tail: ChatTicket[] } {
  const replies = messages
    .filter((m) => m.role === "assistant")
    .map((m) => ({ id: m.id, at: Date.parse(m.created_at) }))
    .sort((a, b) => a.at - b.at);
  const byMessage = new Map<string, ChatTicket[]>();
  const tail: ChatTicket[] = [];
  for (const ticket of tickets) {
    const at = Date.parse(ticket.linked_at || ticket.created_at);
    const reply = replies.find((r) => r.at >= at);
    if (!reply) {
      tail.push(ticket);
      continue;
    }
    const group = byMessage.get(reply.id);
    if (group) group.push(ticket);
    else byMessage.set(reply.id, [ticket]);
  }
  return { byMessage, tail };
}
