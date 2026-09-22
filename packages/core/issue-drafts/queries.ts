import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type { DraftAssigneeSuggestionRequest } from "./assignee-suggestions";
import type {
  IssueDraft,
  IssueDraftSession,
  IssueDraftSummary,
} from "../types";

/**
 * Alignment drafts are server state: the draft is what the server will read at
 * finalize, so the page renders what the server holds and never a local
 * approximation of it. Every key carries `wsId` — the list is workspace-scoped
 * and two workspaces open in one client must not share a cache entry.
 */
export const issueDraftKeys = {
  all: (wsId: string) => ["workspace", wsId, "issue-drafts"] as const,
  list: (wsId: string) => [...issueDraftKeys.all(wsId), "list"] as const,
  assigneeSuggestions: (wsId: string, sessionId: string, request: DraftAssigneeSuggestionRequest | null) =>
    [...issueDraftKeys.all(wsId), "assignee-suggestions", sessionId, request] as const,
};

/**
 * The seats routing suggests for a draft's unassigned rows.
 *
 * Keyed on the request itself, which is built from the draft the SERVER holds:
 * every answer costs a routing-model call per row, so it must be re-asked when
 * a save or a generate changes the rows and never on a keystroke. A null
 * request (nothing unassigned, or no title yet) disables the query.
 */
export function issueDraftAssigneeSuggestionsOptions(
  wsId: string,
  sessionId: string,
  request: DraftAssigneeSuggestionRequest | null,
) {
  return queryOptions({
    queryKey: issueDraftKeys.assigneeSuggestions(wsId, sessionId, request),
    queryFn: () =>
      request ? api.suggestIssueDraftAssignees(sessionId, request) : Promise.resolve([]),
    enabled: wsId.length > 0 && sessionId.length > 0 && request !== null,
    staleTime: Infinity,
    retry: false,
  });
}

/**
 * Every alignment conversation this user has in the workspace — live ones and
 * the records of finished ones — newest activity first.
 *
 * A draft's carrier is a `kind='system'` agent, so it is invisible to every
 * chat list; this list is the only route back to one, which is why it carries
 * all four statuses in ONE cache entry. Two entries (unfinished and records)
 * would be two answers to "what alignments do I have", and the alignment page
 * — which has to open either kind — would then have to guess which one to
 * believe.
 *
 * Consumers that only want actionable work narrow it with
 * `unfinishedIssueDrafts`; the page itself takes the row as it comes and
 * renders a terminal one read-only.
 */
export function issueDraftListOptions(wsId: string) {
  return queryOptions({
    queryKey: issueDraftKeys.list(wsId),
    queryFn: () => api.listIssueDrafts({ status: "all" }),
    enabled: wsId.length > 0,
    // Overrides the client-wide `staleTime: Infinity`. This list changes
    // through work done on another screen — the entry dialog opening a
    // conversation, a turn moving it to the top, a confirm retiring it — and
    // the surfaces that render it mount on demand. Cached forever, a user who
    // just aligned a draft would come back to a list that still shows it.
    staleTime: 0,
  });
}

/** The two live statuses: an alignment still waiting on the user. */
export function unfinishedIssueDrafts(
  drafts: readonly IssueDraftSummary[],
): IssueDraftSummary[] {
  return drafts.filter((draft) => draft.status === "draft" || draft.status === "ready");
}

/**
 * Whether this alignment is over — it produced an issue, or was given up on.
 *
 * Read off the server's status rather than inferred from a missing issue id:
 * `completed` without a readable issue is a broken record, not a live
 * conversation, and offering the composer for it would promise a next turn the
 * server refuses.
 */
export function issueDraftIsRecord(draft: Pick<IssueDraftSummary, "status">): boolean {
  return draft.status === "completed" || draft.status === "abandoned";
}

/**
 * Whether this alignment is on a round after its first — it already produced
 * the group and is being continued (DENE-415).
 *
 * Read off the pair, never off either half: a live status means the round is
 * open, and an `issue_id` means a group already exists for it. A first-round
 * alignment has neither — it is `draft`/`ready` with no `issue_id` until the
 * confirm lands — and a finished one is a record, which the page reads back
 * rather than continues.
 *
 * This is also what tells a caller "the group's issues are worth reading": they
 * are, only here.
 */
export function issueDraftIsContinuation(
  draft: Pick<IssueDraftSummary, "status" | "issue_id">,
): boolean {
  return !issueDraftIsRecord(draft) && !!draft.issue_id;
}

/**
 * Which round this alignment is on, as a person counts them: the first confirm
 * is round 1, and every reopen starts the next one.
 *
 * `finalize_round` counts reopens, so the label is +1. A backend that predates
 * rounds reports 0, which reads as round 1 — the same thing it would have said
 * before continuation existed.
 */
export function issueDraftRound(
  draft: Pick<IssueDraftSummary, "finalize_round">,
): number {
  return Math.max(0, draft.finalize_round ?? 0) + 1;
}

/** The draft with this id, or undefined while it is loading or already retired. */
export function findIssueDraft(
  drafts: readonly IssueDraftSummary[],
  draftId: string,
): IssueDraftSummary | undefined {
  return drafts.find((row) => row.chat_session_id === draftId);
}

/**
 * Adds a just-created conversation to the list, so the page it navigates to
 * finds it on its first render.
 *
 * Without this the page mounts against the list as it was before the create —
 * an alignment that is not in the unfinished list reads as finished — and shows
 * "this alignment has finished" for one refetch. Seeding is safe because the
 * create already returned: the row is a real server object, not a prediction.
 */
export function appendIssueDraftSummary(
  drafts: readonly IssueDraftSummary[] | undefined,
  session: IssueDraftSession,
  firstTurn: { content: string; at: string } | null,
): IssueDraftSummary[] {
  const rows = drafts ?? [];
  const row: IssueDraftSummary = {
    ...session.draft,
    // The carrier session's own title is the same constant string on every row,
    // so the list names a draft by its structured title instead. Empty here
    // means "the conversation has not drafted one yet", which is the truth.
    title: "",
    runtime_id: session.runtime_id,
    last_message_content: firstTurn?.content ?? "",
    last_message_role: firstTurn ? "user" : "",
    last_message_at: firstTurn?.at ?? "",
  };
  const existing = rows.findIndex(
    (candidate) => candidate.chat_session_id === row.chat_session_id,
  );
  if (existing < 0) return [row, ...rows];
  return rows.map((candidate, index) =>
    index === existing ? { ...candidate, ...row } : candidate,
  );
}

/**
 * Replaces one row's server-owned fields in place, keeping the summary-only
 * fields (`title`, `runtime_id`, last-message preview) that a save response
 * does not carry.
 *
 * Used to apply a mutation's own answer immediately: a save is locally
 * predictable and the user stays on this screen, so waiting for the refetch
 * would flash the previous revision — and, worse, leave the confirm button
 * holding a revision the server has already superseded.
 */
export function patchIssueDraftSummary(
  drafts: readonly IssueDraftSummary[] | undefined,
  updated: IssueDraft,
): IssueDraftSummary[] {
  if (!drafts) return [];
  return drafts.map((row) =>
    row.chat_session_id === updated.chat_session_id
      ? { ...row, ...updated }
      : row,
  );
}
