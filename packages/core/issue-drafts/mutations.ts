import {
  useMutation,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import { api } from "../api";
import { issueKeys } from "../issues/queries";
import { chatKeys } from "../chat/queries";
import { upsertChatMessageToCaches } from "../chat/message-cache";
import type {
  IssueDraft,
  IssueDraftPayload,
  IssueDraftSession,
  IssueDraftSummary,
} from "../types";
import { encodeIssueDraftInput } from "./protocol";
import {
  appendIssueDraftSummary,
  issueDraftKeys,
  patchIssueDraftSummary,
} from "./queries";

/**
 * Every write an alignment conversation can make, as React Query mutations.
 *
 * Mutations, not hand-rolled `useState` flags: the confirm button has to be
 * disabled for exactly as long as the create is in flight — a double-clicked
 * confirm is a second request the protocol tolerates, but a second *button*
 * press is what the user sees as "nothing happened". `isPending` is the only
 * flag that cannot drift from the request it describes.
 */

/**
 * `POST /api/issue-drafts` came back in a shape this client does not recognize.
 *
 * `parseWithFallback` degrades an unparseable body to `EMPTY_ISSUE_DRAFT_SESSION`
 * (`packages/core/api/schemas.ts`) so the app keeps rendering, and the one field
 * that proves the call succeeded — `session_id` — arrives empty. Reporting that
 * as "the session was not created" is a lie with a cost: the draft DOES exist on
 * the server, so the retry it invites leaves an orphan behind. It is also the one
 * failure CLAUDE.md's API-compatibility rule says must be named as itself —
 * response drift, not a network error — so it gets its own type the UI can
 * localize instead of falling in with the transport failures.
 */
export class IssueDraftSessionUnrecognizedError extends Error {
  constructor() {
    super("issue draft session response was not recognized");
    this.name = "IssueDraftSessionUnrecognizedError";
  }
}

export interface StartIssueDraftResult {
  session: IssueDraftSession;
  draftId: string;
  /**
   * False when the conversation was opened but its first turn could not be
   * delivered. The draft still exists, and the idea is still stored in it, so
   * the caller navigates either way rather than stranding a draft the user
   * cannot see and would create again.
   */
  seeded: boolean;
  /**
   * Why that first turn was not delivered, when `seeded` is false — the original
   * rejection, not a message. Preserved rather than discarded so the caller can
   * say what happened (a runtime the carrier cannot run on, an attachment bind
   * that failed) instead of leaving the user on a silent empty conversation.
   *
   * The raw error, so the reader decides what is safe to render: a 4xx reason is
   * written for the user, a 5xx one is internal detail (MUL-6472).
   */
  seedError?: unknown;
}

/**
 * Opens an alignment conversation and asks it the user's question.
 *
 * Two calls, one action. The session is created first and its id is what the
 * page is addressed by; the first turn is sent immediately after, because a
 * conversation the user has to restate their request into is not an
 * improvement over a form. The idea is also stored in the draft itself before
 * either call returns, so a failed send costs the turn, never the request.
 */
export function useStartIssueDraft(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      runtimeId: string;
      model?: string;
      /**
       * The reasoning effort the carrier runs at, empty/absent meaning "let the
       * local CLI decide". Sent with `model` because both are read off the
       * carrier agent row the daemon claims: writing either after the session
       * exists would leave the first turn running the old value (DENE-514).
       */
      thinkingLevel?: string;
      /** What the user already typed at the entry point. */
      request: string;
      /**
       * Attachments the request references. The draft does not exist yet when
       * they were uploaded, so they were bound to no owner; sending their ids
       * with the first turn is what attaches them to it. Same transport as any
       * other chat turn (DENE-369).
       */
      attachmentIds?: string[];
      /**
       * The project the whole group is filed under, when the user picked one at
       * the entry point. Stored on the draft at creation rather than sent to the
       * carrier: the carrier has no project list and is never asked to guess
       * one, so this is the client-owned field the preview panel can still
       * change afterwards (`mergeIssueDraftPayload` preserves what the reply
       * never mentions).
       */
      projectId?: string;
      /**
       * The issue an alignment started mid-flight is filed UNDER (DENE-452).
       *
       * Not a filing preference: it is the whole point of starting the
       * conversation from an existing issue — the issue the person was looking
       * at stays the parent, and everything the conversation settles on is
       * created beneath it instead of beside it. Written into the draft at
       * creation for the same reason the project is: the confirm builds the
       * group out of the stored payload, so a field that only lived in the
       * panel would never reach the server.
       */
      parentIssueId?: string;
      /**
       * Which built-in alignment methods this conversation runs with.
       *
       * Sent at creation and never after: the capabilities are assembled into
       * the carrier's instructions, which are installed once — changing them on
       * a live conversation would need the same two-write switch the policy has,
       * and nothing asks for that yet. Omitted means the server's built-in
       * default set; an empty array means none, which is the state an emptied
       * picker is in (see `encodeIssueDraftCapabilities`).
       */
      capabilities?: string[];
    }): Promise<StartIssueDraftResult> => {
      const request = input.request.trim();
      const session = await api.createIssueDraftSession({
        runtime_id: input.runtimeId,
        model: input.model?.trim() || undefined,
        thinking_level: input.thinkingLevel?.trim() || undefined,
        draft: seedDraft(request, input.projectId, input.parentIssueId),
        capabilities: input.capabilities,
      });
      const draftId = session.session_id;
      // An empty id is reachable only through the schema fallback above: every
      // shape the server has ever sent carries a session id here. So this is
      // response drift, and it must not be reported as "nothing was created" —
      // the draft was created and its request stored before this call returned.
      if (!draftId) throw new IssueDraftSessionUnrecognizedError();
      try {
        const sent = await api.sendChatMessage(
          draftId,
          encodeIssueDraftInput(request, session.draft.draft),
          input.attachmentIds,
        );
        // The same door every chat surface uses (MUL-5711): the send seeds the
        // caches, so the conversation shows the user's own turn immediately
        // instead of waiting for a refetch that the realtime echo may win.
        upsertChatMessageToCaches(
          qc,
          draftId,
          {
            id: sent.message_id,
            chat_session_id: draftId,
            role: "user",
            content: encodeIssueDraftInput(request, session.draft.draft),
            task_id: sent.task_id,
            created_at: new Date().toISOString(),
          },
          { seedIfMissing: true },
        );
        qc.setQueryData(chatKeys.pendingTask(draftId), {
          task_id: sent.task_id,
          status: "queued",
          created_at: new Date().toISOString(),
        });
        return { session, draftId, seeded: true };
      } catch (err) {
        // Kept, not swallowed: the caller navigates to a page that has to be
        // able to say this turn was lost, and the reason is what makes that
        // sentence actionable instead of mysterious.
        return { session, draftId, seeded: false, seedError: err };
      }
    },
    onSuccess: (result) => {
      // Seed before invalidating: the page this create navigates to reads the
      // list on its first render, and a row that is missing from it reads as a
      // finished alignment.
      const at = new Date().toISOString();
      qc.setQueryData<IssueDraftSummary[]>(
        issueDraftKeys.list(wsId),
        (rows) =>
          appendIssueDraftSummary(
            rows,
            result.session,
            result.seeded ? { content: result.session.draft.draft.description, at } : null,
          ),
      );
      void qc.invalidateQueries({ queryKey: issueDraftKeys.list(wsId) });
      void qc.invalidateQueries({
        queryKey: chatKeys.messages(result.draftId),
      });
      void qc.invalidateQueries({
        queryKey: chatKeys.pendingTask(result.draftId),
      });
    },
  });
}

/**
 * Persists what the conversation has agreed so far.
 *
 * `expected_revision` is the revision the caller was looking at. A save the
 * server refuses because that view is superseded — by another tab, or by the
 * page's own refetch — is not retried here: the answer is to reload and show
 * the user what actually changed, which is why the failure invalidates the list
 * rather than replaying the write on top of a draft nobody has seen.
 */
export function useSaveIssueDraft(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: {
      draftId: string;
      draft: IssueDraftPayload;
      status?: "draft" | "ready";
      expectedRevision: number;
    }) =>
      api.updateIssueDraft(input.draftId, {
        draft: input.draft,
        ...(input.status ? { status: input.status } : {}),
        expected_revision: input.expectedRevision,
      }),
    onSuccess: (updated) => {
      applyDraftRow(qc, wsId, updated);
      void qc.invalidateQueries({ queryKey: issueDraftKeys.list(wsId) });
    },
    onError: () => {
      void qc.invalidateQueries({ queryKey: issueDraftKeys.list(wsId) });
    },
  });
}

/** Discards a draft. The conversation itself is left alone. */
export function useAbandonIssueDraft(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (draftId: string) => api.abandonIssueDraft(draftId),
    onSuccess: (updated) => {
      applyDraftRow(qc, wsId, updated);
      // A retired draft is no longer in the unfinished list, so the row this
      // patch just updated is about to disappear; the refetch is what removes
      // it rather than a local removal, which would hide a server refusal.
      void qc.invalidateQueries({ queryKey: issueDraftKeys.list(wsId) });
    },
  });
}

/**
 * Confirms a draft into an issue.
 *
 * Deliberately not retried by the network layer: the protocol is idempotent, so
 * a repeat is safe, but a silent automatic repeat would hide the very
 * conflict the user needs to see. `.mutateAsync` is what lets the caller keep
 * the panel in `creating` until the server answers.
 */
export function useFinalizeIssueDraft(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { draftId: string; expectedRevision: number }) =>
      api.finalizeIssueDraft(input.draftId, {
        expected_revision: input.expectedRevision,
      }),
    onSuccess: (result) => {
      applyDraftRow(qc, wsId, result.draft);
      void qc.invalidateQueries({ queryKey: issueDraftKeys.list(wsId) });
      // The issue now exists, so every surface that lists issues is stale.
      void qc.invalidateQueries({ queryKey: issueKeys.all(wsId) });
    },
    onError: () => {
      // A refused confirm is usually a superseded revision; re-read so the
      // panel stops offering a create the server has already answered.
      void qc.invalidateQueries({ queryKey: issueDraftKeys.list(wsId) });
    },
  });
}

/**
 * Starts another round on an alignment that already produced its group, so the
 * same conversation can be continued and the next confirm appends to the same
 * group instead of adopting it whole.
 *
 * One endpoint, one deliberate caller: the issue detail page's "continue
 * aligning" — a person asking to reopen a finished alignment. It is never a
 * read: an open alignment page takes its round off the list row, which carries
 * `finalize_round` (DENE-416).
 *
 * The answer is applied to the list rather than merely invalidated: the row's
 * `finalize_round` and status are exactly what the caller is about to render.
 */
export function useReopenIssueDraft(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (draftId: string) => api.reopenIssueDraft(draftId),
    onSuccess: (updated) => {
      applyDraftRow(qc, wsId, updated);
      void qc.invalidateQueries({ queryKey: issueDraftKeys.list(wsId) });
    },
    onError: () => {
      void qc.invalidateQueries({ queryKey: issueDraftKeys.list(wsId) });
    },
  });
}

/**
 * Rebinds a live conversation to another runtime. Callers must not show the new
 * runtime as selected until this resolves — the picker showing runtime B while
 * messages still run on A is exactly the bug the builder's equivalent fixed
 * (MUL-5163).
 */
export function useSwitchIssueDraftRuntime(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { draftId: string; runtimeId: string }) =>
      api.switchIssueDraftRuntime(input.draftId, {
        runtime_id: input.runtimeId,
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: issueDraftKeys.list(wsId) });
    },
  });
}

/**
 * Switches how the carrier asks: guided questions, plain dialogue, or the
 * front-end look round that settles a screen by building something openable.
 *
 * The response is applied to the list cache rather than merely invalidated for
 * the same reason a save is: the switch is a determinate field change the user
 * is standing in front of, and the recorded prompt version it returns is the
 * audit value the page displays. A failure re-reads instead — a switch refused
 * because a reply is in flight must leave the control showing what is actually
 * running.
 */
export function useSwitchIssueDraftPolicy(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { draftId: string; policy: string }) =>
      api.switchIssueDraftPolicy(input.draftId, { policy: input.policy }),
    onSuccess: (updated) => {
      applyDraftRow(qc, wsId, updated);
      void qc.invalidateQueries({ queryKey: issueDraftKeys.list(wsId) });
    },
    onError: () => {
      void qc.invalidateQueries({ queryKey: issueDraftKeys.list(wsId) });
    },
  });
}

/** The idea, kept server-side from the first moment so a lost turn loses nothing. */
function seedDraft(
  request: string,
  projectId?: string,
  parentIssueId?: string,
): Partial<IssueDraftPayload> {
  return {
    title: "",
    description: request,
    status: "",
    priority: "",
    // A sub-issue records an explicit project, including null when the user
    // left it empty, so confirm does not inherit the parent back. A top-level
    // alignment still omits the field when there is no project.
    ...(parentIssueId
      ? { project_id: projectId ?? null }
      : projectId
        ? { project_id: projectId }
        : {}),
    // Same rule for the parent: an alignment filed under an existing issue
    // records it here, and one that founds its own top-level issue leaves the
    // field absent rather than writing a null the server would have to read.
    ...(parentIssueId ? { parent_issue_id: parentIssueId } : {}),
  };
}

function applyDraftRow(
  qc: QueryClient,
  wsId: string,
  updated: IssueDraft,
): void {
  qc.setQueryData<IssueDraftSummary[]>(
    issueDraftKeys.list(wsId),
    (rows) => patchIssueDraftSummary(rows, updated),
  );
}
