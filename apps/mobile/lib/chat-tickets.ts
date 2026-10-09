/**
 * Where a chat's tickets render (DENE-1665).
 *
 * Mirrors `groupChatTickets` in packages/views/chat/components/chat-ticket-card.tsx:
 * each ticket hangs under the reply of the turn that opened it — the first
 * assistant message created at or after the ticket. A ticket opened by a turn
 * still running has no such reply yet and goes to `tail`, the list's last row.
 */
import type { ChatTicket } from "@multica/core/types";

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
    const at = Date.parse(ticket.created_at);
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

const PHASE_RANK = { waiting_you: 0, in_progress: 1, done: 2 } as const;

export interface ChatProgressRow extends ChatTicket {
  fresh: boolean;
}

/**
 * The chat's progress bar (DENE-1667). Mirrors `ChatReportBar` in
 * packages/views/chat/components/chat-report-bar.tsx: a ticket whose latest
 * status move came after `seenAt` is fresh; fresh rows lead, newest first,
 * the rest by who they wait on.
 */
export function chatProgress(tickets: ChatTicket[], seenAt: number) {
  const all = tickets.map((ticket) => ({
    ...ticket,
    fresh: !!ticket.from_status && Date.parse(ticket.changed_at) > seenAt,
  }));
  const fresh = all
    .filter((r) => r.fresh)
    .sort((a, b) => Date.parse(b.changed_at) - Date.parse(a.changed_at));
  const rest = all
    .filter((r) => !r.fresh)
    .sort(
      (a, b) =>
        PHASE_RANK[a.phase] - PHASE_RANK[b.phase] ||
        Date.parse(b.changed_at) - Date.parse(a.changed_at),
    );
  const counts = { waiting_you: 0, in_progress: 0, done: 0 };
  for (const row of all) counts[row.phase] += 1;
  return { rows: [...fresh, ...rest] as ChatProgressRow[], counts, fresh: fresh.length };
}
