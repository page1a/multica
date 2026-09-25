"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@multica/core/api";
import { chatKeys, chatMessagesOptions, pendingChatTaskOptions } from "@multica/core/chat/queries";
import { upsertChatMessageToCaches } from "@multica/core/chat/message-cache";
import { useWorkspaceId } from "@multica/core/hooks";
import { projectListOptions } from "@multica/core/projects/queries";
import {
  issueDraftBuiltNodeKeys,
  issueDraftCanConfirm,
  issueDraftCreatedGroup,
  issueDraftIsContinuation,
  issueDraftIsCreatable,
  issueDraftIsRecord,
  issueDraftKeys,
  issueDraftListOptions,
  issueDraftPendingQuestion,
  issueDraftRound,
  issueDraftStage,
  encodeIssueDraftInput,
  findIssueDraft,
  mergeIssueDraftPayload,
  parseIssueDraftBlock,
  planIssueDraftFold,
  useAbandonIssueDraft,
  useFinalizeIssueDraft,
  useReopenIssueDraft,
  useSaveIssueDraft,
  useSwitchIssueDraftPolicy,
  useSwitchIssueDraftRuntime,
  type IssueDraftPolicyKey,
  type IssueDraftQuestion,
  type IssueDraftStage,
} from "@multica/core/issue-drafts";
import { childIssuesOptions } from "@multica/core/issues/queries";
import { useIssueDraftStore } from "@multica/core/issues/stores";
import type { IssueDraftPolicy } from "@multica/core/types";
import { runtimeListOptions } from "@multica/core/runtimes";
import type {
  Attachment,
  ChatMessage,
  Issue,
  IssueDraftAssignmentWarning,
  IssueDraftCreatedIssue,
  IssueDraftPayload,
  IssueDraftSummary,
  RuntimeDevice,
} from "@multica/core/types";
import { useT } from "../../i18n";

const EMPTY_MESSAGES: ChatMessage[] = [];
const EMPTY_DRAFTS: readonly IssueDraftSummary[] = [];
const EMPTY_ISSUES: readonly Issue[] = [];

/**
 * What a draft reports before the server has said anything about policies.
 *
 * `key: ""` is "this backend has no policies", not "the guided default": the
 * page hides the switch on it rather than offering one that cannot land. An
 * older backend is a real deployment shape — an installed desktop client can
 * talk to one — so the state has to be representable, not assumed away.
 */
const UNKNOWN_POLICY: IssueDraftPolicy = { key: "", version: "", guided: false };

export interface IssueDraftSession {
  stage: IssueDraftStage;
  /** The server-owned draft, absent until the list lands or once it is retired. */
  draft: IssueDraftPayload | null;
  revision: number | null;
  /** The conversation is gone server-side; the page must leave. */
  missing: boolean;
  /**
   * The conversation is alive but the draft row is gone from this user's list —
   * it was discarded with its conversation, or the list predates it. Not an
   * error: the page says so instead of offering actions the server refuses.
   */
  retired: boolean;
  /**
   * This alignment is over — it produced an issue, or was given up on. The page
   * renders it as a record: the transcript and what was agreed, with no
   * composer and no actions (DENE-371). This is NOT the same as `retired`:
   * a record still has a draft, and everything it needs to be read back.
   */
  isRecord: boolean;
  /**
   * This alignment is on a round after its first: a group already exists and
   * the conversation is adding to it (DENE-415). False for a first pass, which
   * is the shape the page had before continuation existed.
   */
  continuation: boolean;
  /**
   * Which round this alignment is on, counting the first confirm as round 1.
   * Always ≥ 1; a backend that predates rounds reports round 1.
   */
  round: number;
  /** The issue this alignment produced, once it has one. */
  producedIssueId: string | null;
  /**
   * The group as the server reads it back: every child of the group's root,
   * with the `origin_id` each node was created under. Empty until the alignment
   * has produced a group.
   */
  groupChildren: readonly Issue[];
  groupLoading: boolean;
  /**
   * The payload sub-issue keys whose node already owns an issue, so the next
   * confirm adopts them and never rewrites them. Empty on a first round.
   */
  builtKeys: ReadonlySet<string>;
  loading: boolean;
  loadFailed: boolean;
  messages: ChatMessage[];
  messagesLoading: boolean;
  /**
   * The files this alignment produced, in the order the conversation produced
   * them. This is the set the confirm hands to the group's root, so the preview
   * panel can say what it is about to carry instead of leaving the user to
   * discover it on the issue afterwards (DENE-453).
   */
  attachments: Attachment[];
  /** A turn is running on the carrier. */
  pending: boolean;
  /** The in-flight turn, as the transcript renders it. */
  pendingTask: { task_id?: string; status?: string; created_at?: string } | undefined;
  sending: boolean;
  error: string | null;
  runtime: RuntimeDevice | null;
  runtimeOnline: boolean;
  switchingRuntime: boolean;
  /**
   * The alignment policy in force, as the server records it: which behaviour the
   * carrier runs, and which version of its prompt it was given.
   */
  policy: IssueDraftPolicy;
  switchingPolicy: boolean;
  /**
   * The question the carrier is waiting on, or null. Only ever set while the
   * guided policy is running and the carrier's question is the last thing said:
   * an answered question clears itself when the user's own turn lands.
   */
  question: IssueDraftQuestion | null;
  /**
   * Reports whether the preview panel holds unsaved edits. While it does, this
   * hook will not adopt a carrier revision on the user's behalf.
   */
  setLocalDirty: (dirty: boolean) => void;
  canConfirm: boolean;
  saving: boolean;
  saved: boolean;
  confirming: boolean;
  abandoning: boolean;
  createdIssueId: string | null;
  /**
   * The whole group this confirm created, root first. Null until a confirm has
   * answered; `[the root]` for a backend that predates groups.
   */
  createdIssues: IssueDraftCreatedIssue[] | null;
  /**
   * Nodes the confirm created unassigned because the seat shown on the panel
   * could not be applied. Empty until a confirm answers, and empty when every
   * assignment landed — a backend that predates the field says the same thing
   * by omitting it (DENE-694).
   */
  assignmentWarnings: IssueDraftAssignmentWarning[];
  send: (
    content: string,
    attachmentIds?: string[],
    commitInput?: () => void,
  ) => Promise<boolean>;
  save: (draft: IssueDraftPayload, status?: "draft" | "ready") => Promise<boolean>;
  /**
   * Folds the carrier's latest `<issue_draft>` block into `current` and saves
   * the result as `ready`. This is "the conversation has converged" expressed
   * as one action: parsing and persisting together is what makes the preview
   * the thing the server will actually create from, rather than a rendering of
   * a message that could still change under it.
   *
   * Resolves with the draft that was persisted, or null when nothing was: the
   * caller's editor has to adopt it, because what the server now holds is no
   * longer what the editor shows.
   */
  generatePreview: (current: IssueDraftPayload) => Promise<IssueDraftPayload | null>;
  confirm: () => Promise<boolean>;
  /**
   * Starts another round on this alignment — the detail page's "continue
   * aligning". Idempotent server-side, so a repeat is answered with the row as
   * it stands rather than counting the round twice. This is a deliberate write,
   * never a read: the round a page is on comes off its list row.
   */
  reopen: () => Promise<boolean>;
  abandon: () => Promise<boolean>;
  switchRuntime: (runtimeId: string) => Promise<string | null>;
  /** Switches between guided questions and plain dialogue. */
  setPolicy: (policy: IssueDraftPolicyKey) => Promise<boolean>;
  stop: () => Promise<void>;
  retry: () => void;
  clearError: () => void;
}

/**
 * Lifecycle of one alignment conversation: exchange turns on the hidden
 * carrier, save the structured draft, confirm it into an issue, or discard it.
 *
 * The conversation is identified by the id in the URL and nothing here holds
 * session state of its own, which is what makes a refresh, a back/forward and a
 * reopened desktop tab land back in the same alignment. There is no polling:
 * the global realtime sync already invalidates `chatKeys.messages` /
 * `chatKeys.pendingTask` per session id on `chat:message`, `chat:done` and the
 * task lifecycle events, exactly as it does for the main chat window.
 */
export function useIssueDraftSession(draftId: string): IssueDraftSession {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();

  const listQuery = useQuery(issueDraftListOptions(wsId));
  const messagesQuery = useQuery(chatMessagesOptions(draftId));
  const pendingQuery = useQuery(pendingChatTaskOptions(draftId));
  const runtimesQuery = useQuery(runtimeListOptions(wsId));

  const row = findIssueDraft(listQuery.data ?? EMPTY_DRAFTS, draftId);
  const messages = messagesQuery.data ?? EMPTY_MESSAGES;
  const pending = !!pendingQuery.data?.task_id;

  // A successful confirm is final even after the refetch drops the completed
  // draft out of the unfinished list, so it outranks everything the list says.
  const [createdIssueId, setCreatedIssueId] = useState<string | null>(null);
  const [createdIssues, setCreatedIssues] = useState<
    IssueDraftCreatedIssue[] | null
  >(null);
  // Kept beside `createdIssues` and for the same reason: the confirm's own
  // answer is the only place a dropped seat is ever visible. The panel is on
  // "created" for exactly this mount, and a refetch cannot answer it — the
  // group is an ordinary unassigned issue by then.
  const [assignmentWarnings, setAssignmentWarnings] = useState<
    IssueDraftAssignmentWarning[]
  >([]);
  const [error, setError] = useState<string | null>(null);
  const [sending, setSending] = useState(false);
  const [saved, setSaved] = useState(false);

  /**
   * A first turn the entry panel could not deliver (DENE-422).
   *
   * That panel closes the moment a conversation exists, so it cannot say this
   * itself; it leaves the draft id in the create draft's align slot and this is
   * the one page the id names. The flag is read into local state and cleared
   * from the slot in the same pass — what the composer shows must not depend on
   * a store write that a remount would re-run — and the composer's error slot is
   * where it belongs, because resending from there is the whole recovery.
   */
  const seedFailedDraftId = useIssueDraftStore(
    (s) => s.draft.align.seedFailedDraftId,
  );
  const setAlign = useIssueDraftStore((s) => s.setAlign);
  const [seedLost, setSeedLost] = useState(false);
  useEffect(() => {
    if (seedFailedDraftId !== draftId) return;
    setSeedLost(true);
    setAlign({ seedFailedDraftId: undefined });
  }, [draftId, seedFailedDraftId, setAlign]);

  const saveMutation = useSaveIssueDraft(wsId);
  const abandonMutation = useAbandonIssueDraft(wsId);
  const finalizeMutation = useFinalizeIssueDraft(wsId);
  const reopenMutation = useReopenIssueDraft(wsId);
  const runtimeMutation = useSwitchIssueDraftRuntime(wsId);
  const policyMutation = useSwitchIssueDraftPolicy(wsId);

  const draft = row?.draft ?? null;
  const revision = row?.revision ?? null;
  // A terminal draft is a record, not a conversation: the page reads it back
  // and offers nothing that would need a next turn. Computed from the row (not
  // from `stage`, which folds completed and abandoned into "aligning" because
  // neither can be acted on) so the page can tell "over" from "still going".
  const isRecord = row ? issueDraftIsRecord(row) : false;
  // The issue this alignment produced. `row.issue_id` is the durable answer —
  // it survives the refetch that drops the draft out of the live list — and
  // the local confirm result covers the window before that refetch lands.
  const producedIssueId = createdIssueId ?? row?.issue_id ?? null;

  const stage = issueDraftStage({
    status: row?.status ?? "draft",
    creating: finalizeMutation.isPending,
    createdIssueId,
  });

  // Read off the transcript fetch, not "absent from the list": a conversation
  // with no draft row is a different situation from one that no longer exists,
  // and only a 404 proves the latter.
  const missing =
    messagesQuery.error instanceof ApiError && messagesQuery.error.status === 404;
  const retired = !missing && !createdIssueId && listQuery.isSuccess && !row;

  /**
   * The group this alignment has produced, read back through its root: the
   * root's children are the nodes the alignment created (plus any a person
   * added under it afterwards, which belong to the answer to "what is this
   * group now").
   *
   * Gated on `issue_id`, so a first-round alignment — and every alignment that
   * had not been confirmed yet — costs no request. (DENE-415)
   */
  const rootIssueId = producedIssueId;
  const groupQuery = useQuery({
    ...childIssuesOptions(wsId, rootIssueId ?? ""),
    enabled: !!rootIssueId,
  });
  const groupChildren = groupQuery.data ?? EMPTY_ISSUES;
  const groupLoading = !!rootIssueId && groupQuery.isLoading;

  const continuation = row ? issueDraftIsContinuation(row) : false;
  // Which payload nodes already own an issue. Derived from the node ids the
  // server stamped the group's children with, so it agrees with the confirm's
  // own skip rule instead of guessing from a title someone can edit.
  //
  // The ROOT's key is "" and it is part of the payload like any other node, so
  // it joins the same set once a group exists: the confirm skips it and never
  // rewrites it exactly as it does the children, and folding it in here keeps
  // one rule ("adopted iff the key is in this set") instead of two.
  const builtKeys = useMemo(() => {
    const keys = new Set(
      draft
        ? issueDraftBuiltNodeKeys(draft.children ?? [], groupChildren, draftId)
        : [],
    );
    if (draft && rootIssueId) keys.add("");
    return keys as ReadonlySet<string>;
  }, [draft, draftId, groupChildren, rootIssueId]);

  /**
   * Which round this page is on. `finalize_round` counts the reopens, so the
   * label is +1 (see `issueDraftRound`), and the list endpoint this page reads
   * its row from carries the column, so a continuation says round 2 without a
   * request of its own. A backend that predates the field reports round 1,
   * which is the shape the page had before continuation existed.
   */
  const round = issueDraftRound(row ?? {});

  /**
   * The files this alignment produced, read off the transcript rather than
   * fetched again: `ListChatMessages` already ships each message's attachments,
   * and both directions count — the file the user dropped in rides a user
   * message, the prototype the carrier uploaded rides its reply. Deduplicated
   * by id because a message list that renders the same attachment twice is one
   * file, not two, and the panel is describing what the confirm will carry.
   */
  const attachments = useMemo(() => {
    const byId = new Map<string, Attachment>();
    for (const message of messages) {
      for (const attachment of message.attachments ?? []) {
        if (!byId.has(attachment.id)) byId.set(attachment.id, attachment);
      }
    }
    return [...byId.values()];
  }, [messages]);

  const runtime = useMemo(
    () => runtimesQuery.data?.find((device) => device.id === row?.runtime_id) ?? null,
    [row?.runtime_id, runtimesQuery.data],
  );
  const runtimeOnline = runtime?.status === "online";

  const policy = row?.policy ?? UNKNOWN_POLICY;

  // Which question is still open is read off the transcript, so nothing has to
  // remember that it was answered: the user's next turn becomes the last
  // message and the chips go with it.
  const question = useMemo(
    () => (policy.guided && !pending ? (issueDraftPendingQuestion(messages)?.question ?? null) : null),
    [messages, pending, policy.guided],
  );

  const canConfirm = issueDraftCanConfirm({
    stage,
    hasTitle: draft ? issueDraftIsCreatable(draft) : false,
    pending,
  });

  /**
   * Whether the preview panel holds edits the user has not saved.
   *
   * A ref, not state: this is read by the auto-persist effect below, and
   * re-rendering the whole page because someone typed a character into the
   * title would be a cost with no reader. The panel owns the answer — it is
   * the only thing that knows what is on screen — and reports it here.
   */
  const localDirtyRef = useRef(false);
  const setLocalDirty = useCallback((dirty: boolean) => {
    localDirtyRef.current = dirty;
  }, []);

  /**
   * The reply whose draft proposal has already been folded into the server
   * draft. One fold per reply: the effect re-runs on every revision bump its
   * own save causes, and without this it would write the same block forever.
   */
  const appliedReplyRef = useRef<string | null>(null);

  /**
   * Persists the carrier's proposal as the conversation goes.
   *
   * "The question was answered, so the draft changed" has to reach the server
   * on its own: the draft is what finalize reads, and a proposal that only
   * exists in the browser until someone presses a button is one refresh away
   * from being lost. Two gates keep that from becoming a way to overwrite the
   * user:
   *
   *   - Unsaved edits in the preview panel stop the fold entirely. The panel is
   *     the user's own hand on the draft, and a reply must never win over it.
   *   - A failed fold is not retried and not surfaced: the usual cause is a
   *     revision that moved underneath (another tab saved), and the answer to
   *     that is to re-read, which the mutation already does.
   *
   * The decision itself lives in `planIssueDraftFold`, where those rules are
   * pinned by tests; this is only the wiring that carries it out.
   */
  useEffect(() => {
    if (!draftId || !row) return;
    const fold = planIssueDraftFold({
      messages,
      draft: row.draft,
      status: row.status,
      revision: row.revision,
      localDirty: localDirtyRef.current,
      appliedReplyId: appliedReplyRef.current,
      busy:
        pending || saveMutation.isPending || finalizeMutation.isPending,
    });
    if (!fold) return;
    // Recorded before the write: the effect re-runs on every revision bump the
    // write itself causes, and an unrecorded reply would be written again.
    appliedReplyRef.current = fold.replyId;
    void saveMutation
      .mutateAsync({
        draftId,
        draft: fold.draft,
        status: fold.status,
        expectedRevision: row.revision,
      })
      .catch(() => {
        void qc.invalidateQueries({ queryKey: issueDraftKeys.list(wsId) });
      });
  }, [
    draftId,
    finalizeMutation.isPending,
    messages,
    pending,
    qc,
    row,
    saveMutation,
    wsId,
  ]);

  /**
   * Sends one turn.
   *
   * Every turn goes out as a `MULTICA_ISSUE_DRAFT_INPUT` envelope carrying the
   * draft the user is asking about, not just their words. The carrier's
   * instructions say to "preserve good existing draft fields supplied in the
   * user's message" — supply nothing and the next reply rebuilds the draft from
   * one message, dropping fields that were already agreed or edited by hand in
   * the preview. The transcript decodes the envelope back for display.
   *
   * `commitInput` is the composer's clear, and it runs the moment the server
   * has accepted the message and the caches render it — not after the
   * reconciling invalidations settle. Awaiting those would hold the user's text
   * in the box for three more round-trips while their message is already on
   * screen (MUL-5181).
   *
   * Attachments ride the same session-scoped transport as any other chat turn:
   * the composer has already uploaded them and hands down the bound ids, so
   * aligning a request that starts as a screenshot or a spec file works
   * without a second upload surface (DENE-369).
   */
  const projectsQuery = useQuery(projectListOptions(wsId));
  const knownProjects = useMemo(
    () => (projectsQuery.data ?? []).map((project) => project.title),
    [projectsQuery.data],
  );

  const send = useCallback(
    async (
      content: string,
      attachmentIds?: string[],
      commitInput?: () => void,
    ): Promise<boolean> => {
      const text = content.trim();
      // A record has no next turn: the server refuses a save or a turn against
      // a terminal draft, and the page does not render a composer for one —
      // this is the same refusal one layer down, so a stale event or a test
      // cannot make a finished alignment speak again.
      if (!text || !draftId || pending || sending || !draft || isRecord) return false;
      setError(null);
      setSending(true);
      const wire = encodeIssueDraftInput(text, draft, knownProjects);
      try {
        const result = await api.sendChatMessage(draftId, wire, attachmentIds);
        const createdAt = new Date().toISOString();
        upsertChatMessageToCaches(
          qc,
          draftId,
          {
            id: result.message_id,
            chat_session_id: draftId,
            role: "user",
            content: wire,
            task_id: result.task_id,
            created_at: createdAt,
          },
          { seedIfMissing: true },
        );
        qc.setQueryData(chatKeys.pendingTask(draftId), {
          task_id: result.task_id,
          status: "queued",
          created_at: createdAt,
        });
        commitInput?.();
        // The turn that was lost at the entry point is no longer lost: the
        // sentence the composer is showing about it has to go with it.
        setSeedLost(false);
        // The server reports which ids it actually bound. Diff against what we
        // asked for so a silent bind failure is visible here rather than only
        // as an assistant that never mentions the file. Servers that predate
        // the field report undefined; skip rather than false-alarm.
        if (attachmentIds && attachmentIds.length > 0 && result.attachment_ids) {
          const bound = new Set(result.attachment_ids);
          if (attachmentIds.some((id) => !bound.has(id))) {
            setError(t(($) => $.alignment.attachment_bind_failed));
          }
        }
        void qc.invalidateQueries({ queryKey: chatKeys.messages(draftId) });
        void qc.invalidateQueries({ queryKey: chatKeys.messagesPage(draftId) });
        void qc.invalidateQueries({ queryKey: chatKeys.pendingTask(draftId) });
        return true;
      } catch (err) {
        setError(err instanceof Error ? err.message : t(($) => $.alignment.send_failed));
        return false;
      } finally {
        setSending(false);
      }
    },
    [draft, draftId, isRecord, knownProjects, pending, qc, sending, t],
  );

  const save = useCallback(
    async (
      next: IssueDraftPayload,
      status?: "draft" | "ready",
    ): Promise<boolean> => {
      if (revision === null) return false;
      setError(null);
      try {
        await saveMutation.mutateAsync({
          draftId,
          draft: next,
          ...(status ? { status } : {}),
          expectedRevision: revision,
        });
        setSaved(true);
        return true;
      } catch {
        // The mutation's own onError already re-read the list; the panel is
        // about to show the revision that actually won.
        setError(t(($) => $.alignment.save_failed));
        return false;
      }
    },
    [draftId, revision, saveMutation, t],
  );

  /**
   * `current` is the panel's editor value, not the stored draft: the user's
   * unsaved edits are part of what they are asking to have previewed, and
   * rebuilding from the stored draft would silently discard them.
   *
   * The merged draft is what comes back, not a bare success flag: it is now
   * what the server holds, and the panel has no other way to learn that its own
   * screen is stale. Without it the editor keeps `dirty` true over the old
   * values, and the next "save draft" writes them straight back over the
   * preview that was just generated (DENE-319).
   */
  const generatePreview = useCallback(
    async (current: IssueDraftPayload): Promise<IssueDraftPayload | null> => {
      const latest = [...messages]
        .reverse()
        .find(
          (message) =>
            message.role === "assistant" &&
            parseIssueDraftBlock(message.content) !== null,
        );
      const merged = mergeIssueDraftPayload(
        current,
        latest ? parseIssueDraftBlock(latest.content) : null,
      );
      if (!issueDraftIsCreatable(merged)) return null;
      const saved = await save(merged, "ready");
      return saved ? merged : null;
    },
    [messages, save],
  );

  const confirm = useCallback(async (extra?: {
    newProject?: {
      title: string;
      icon?: string;
      description?: string;
      directory?: Record<string, unknown>;
    };
  }): Promise<boolean> => {
    // An already-created alignment is not confirmable again: the server answers
    // a repeat with the issue it made, and the page's job for one is to show
    // that issue, not to ask for another.
    if (revision === null || isRecord) return false;
    setError(null);
    try {
      const result = await finalizeMutation.mutateAsync({
        draftId,
        expectedRevision: revision,
        newProject: extra?.newProject,
      });
      setCreatedIssueId(result.issue_id);
      // The group the confirm reported, not just its root: the page shows what
      // was created, and a repeat confirm has to read back the same list rather
      // than degrading to "one issue".
      setCreatedIssues(issueDraftCreatedGroup(result));
      // Only the answer that INSERTED the rows can carry the seats it could not
      // apply; the answer that adopted the group it created says nothing about
      // seats because it created none. Two presses in one task send two confirms
      // of the same round (DENE-317) and the adopting answer lands last, so read
      // an empty list as "this answer has nothing to add" rather than "every
      // seat landed" — otherwise the duplicate erases the notice the creating
      // answer produced. A genuinely new round clears it: `reopen` resets this
      // alongside the group it is about to append to.
      setAssignmentWarnings((previous) =>
        result.assignment_warnings?.length
          ? result.assignment_warnings
          : previous,
      );
      return true;
    } catch (err) {
      setError(err instanceof Error ? err.message : t(($) => $.alignment.confirm_failed));
      // Rethrow so the panel can remove a directory it created for a project
      // the server did not keep. The message above is what the page shows.
      throw err;
    }
  }, [draftId, finalizeMutation, isRecord, revision, t]);

  const abandon = useCallback(async (): Promise<boolean> => {
    setError(null);
    try {
      await abandonMutation.mutateAsync(draftId);
      return true;
    } catch (err) {
      setError(err instanceof Error ? err.message : t(($) => $.alignment.abandon_failed));
      return false;
    }
  }, [abandonMutation, draftId, t]);

  /**
   * Starts another round. Idempotent server-side, and the response is applied to
   * the list, so the round and status this page reads are the ones the server
   * just confirmed rather than a stale snapshot waiting on a refetch.
   */
  const reopen = useCallback(async (): Promise<boolean> => {
    setError(null);
    try {
      await reopenMutation.mutateAsync(draftId);
      // A new round reports its own dropped seats on its own confirm; what the
      // finished round could not apply is not news about this one.
      setAssignmentWarnings([]);
      return true;
    } catch (err) {
      setError(err instanceof Error ? err.message : t(($) => $.alignment.reopen_failed));
      return false;
    }
  }, [draftId, reopenMutation, t]);

  const switchRuntime = useCallback(
    async (runtimeId: string): Promise<string | null> => {
      setError(null);
      try {
        const result = await runtimeMutation.mutateAsync({ draftId, runtimeId });
        // Follow the runtime the server says it bound. Resolving here at all
        // means the rebind committed, so refusing to move the picker would
        // leave it pointing at a runtime that no longer executes anything.
        return result.runtime_id || runtimeId;
      } catch (err) {
        setError(err instanceof Error ? err.message : t(($) => $.alignment.runtime_failed));
        return null;
      }
    },
    [draftId, runtimeMutation, t],
  );

  /**
   * Switches the alignment policy. The control must not move until the server
   * has answered: what changes is what the NEXT reply will do, and a picker that
   * shows "plain dialogue" while the carrier is still interviewing is the same
   * class of lie as a runtime picker that moved before the rebind (MUL-5163).
   */
  const setPolicy = useCallback(
    async (next: IssueDraftPolicyKey): Promise<boolean> => {
      setError(null);
      try {
        await policyMutation.mutateAsync({ draftId, policy: next });
        return true;
      } catch (err) {
        setError(
          err instanceof Error ? err.message : t(($) => $.alignment.policy_failed),
        );
        return false;
      }
    },
    [draftId, policyMutation, t],
  );

  const stop = useCallback(async () => {
    const taskId = pendingQuery.data?.task_id;
    if (!taskId || !draftId) return;
    qc.setQueryData(chatKeys.pendingTask(draftId), {});
    try {
      await api.cancelTaskById(taskId);
    } finally {
      // The cancel may or may not have landed; re-read either way rather than
      // trusting the optimistic clear.
      void qc.invalidateQueries({ queryKey: chatKeys.messages(draftId) });
      void qc.invalidateQueries({ queryKey: chatKeys.pendingTask(draftId) });
    }
  }, [draftId, pendingQuery.data?.task_id, qc]);

  const loading = listQuery.isLoading || messagesQuery.isLoading;

  return {
    stage,
    draft,
    revision,
    missing,
    retired,
    isRecord,
    continuation,
    round,
    producedIssueId,
    groupChildren,
    groupLoading,
    builtKeys,
    loading,
    loadFailed: listQuery.isError,
    messages,
    messagesLoading: messagesQuery.isLoading,
    attachments,
    pending,
    pendingTask: pendingQuery.data,
    sending,
    // One slot, two sources: a turn this page tried to send, and the first turn
    // the entry panel could not. The user's next action is the same for both —
    // say it again from the composer below the sentence.
    error: error ?? (seedLost ? t(($) => $.alignment.seed_lost) : null),
    runtime,
    runtimeOnline,
    switchingRuntime: runtimeMutation.isPending,
    policy,
    switchingPolicy: policyMutation.isPending,
    question,
    setLocalDirty,
    canConfirm,
    saving: saveMutation.isPending,
    saved,
    confirming: finalizeMutation.isPending,
    abandoning: abandonMutation.isPending,
    createdIssueId,
    createdIssues,
    assignmentWarnings,
    send,
    save,
    generatePreview,
    confirm,
    reopen,
    abandon,
    switchRuntime,
    setPolicy,
    stop,
    retry: () => {
      void listQuery.refetch();
      void messagesQuery.refetch();
    },
    clearError: () => {
      setError(null);
      setSeedLost(false);
    },
  };
}