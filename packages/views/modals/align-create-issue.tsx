"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { ArrowLeftRight, Loader2, Sparkles } from "lucide-react";
import { ApiError, clientErrorMessage } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  type IssueDraftCapabilityKey,
  encodeIssueDraftCapabilities,
  readIssueDraftCapabilityPreference,
  writeIssueDraftCapabilityPreference,
  IssueDraftSessionUnrecognizedError,
  issueDraftListOptions,
  unfinishedIssueDrafts,
  useStartIssueDraft,
} from "@multica/core/issue-drafts";
import { useIssueDraftStore } from "@multica/core/issues/stores";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  isRuntimeUsableForUser,
  runtimeDisplayName,
  runtimeListOptions,
} from "@multica/core/runtimes";
import { contentReferencesAttachment, type IssueDraftSummary } from "@multica/core/types";
import { memberListOptions } from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import { DialogTitle } from "@multica/ui/components/ui/dialog";
import { FileUploadButton } from "@multica/ui/components/common/file-upload-button";
import { cn } from "@multica/ui/lib/utils";
import {
  ContentEditor,
  FileDropOverlay,
  useFileDropZone,
  useUploadGate,
  type ContentEditorRef,
} from "../editor";
import { useT } from "../i18n";
import { UnfinishedIssueDraftsBanner } from "../issues/draft/unfinished-issue-drafts";
import { AlignmentConfigPicker } from "../issues/draft/alignment-config-picker";
import { ClearablePillButton } from "../common/pill-button";
import { ProjectPicker } from "../projects/components/project-picker";
import { AppLink, useNavigation } from "../navigation";
import { useIssueCreateUploads } from "./use-issue-create-uploads";

/**
 * The alignment face of the create-issue dialog (DENE-370) — the third mode of
 * the same shell, not a second dialog.
 *
 * Its job is unchanged from the standalone entry dialog it replaces: open the
 * conversation and hand off. It awaits the server's session id, sends the
 * user's own first turn, and navigates to the alignment page. The conversation
 * itself is deliberately NOT here: it is a durable object that is left and
 * resumed, and a modal cannot be refreshed into, linked to, or restored as a
 * desktop tab.
 *
 * What IS shared with "New issue" is everything about the input: the same
 * `ContentEditor`, the same upload pool (`draft.shared.attachments`), the same
 * optional project (`draft.shared.projectId`), and the same draft store, so a
 * file, a body or a project chosen on either face survives a switch to the
 * other, and — since DENE-443 — which machine (and therefore which CLI) runs
 * the alignment: the toolbar carries the same `RuntimePicker` the page's
 * preview panel does, seeded with the machine this face would have picked
 * silently. The single case that stops the conversation from starting at all
 * — nothing usable to run on, or the chosen machine offline — is stated
 * outright instead of being left for the user to infer from a disabled button.
 */
export function AlignCreatePanel({
  onClose,
  onSwitchMode,
  parentIssueId,
}: {
  onClose: () => void;
  /** Called with the carry payload for the panel this face switches back to. */
  onSwitchMode?: (carry?: Record<string, unknown> | null) => void;
  /**
   * The issue this alignment is being started FROM, when there is one
   * (DENE-452: started from an issue's detail page, or from one of its comment
   * threads).
   *
   * Deliberately a prop rather than a store field: the parent context is
   * per-invocation, exactly as it is on the manual face, and persisting it
   * would turn the next alignment opened from anywhere into a sub-issue of
   * whatever was last looked at. It is written into the draft at creation, so
   * the confirm files the whole group beneath that issue.
   */
  parentIssueId?: string;
}) {
  const { t } = useT("issues");
  const { t: tModals } = useT("modals");
  const { t: tProjects } = useT("projects");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const currentUserId = useAuthStore((state) => state.user?.id ?? null);

  const draft = useIssueDraftStore((s) => s.draft);
  const setAlign = useIssueDraftStore((s) => s.setAlign);
  const setShared = useIssueDraftStore((s) => s.setShared);
  const setActiveMode = useIssueDraftStore((s) => s.setActiveMode);

  // The project is the SHARED slot's, exactly as it is on the manual face: it
  // is a property of the issue, not of the form that described it, so a project
  // picked here (or there) is what the whole group is filed under. Optional by
  // design — no project is a legitimate choice and the picker says so.
  const projectId = draft.shared.projectId;

  // The alignment request lives in the draft's own `align` slot, exactly like
  // the agent prompt: the manual face assist-inits it when it switches here,
  // and a switch away and back restores it verbatim. Nothing about the body
  // rides the carry channel — that is left to the parent-issue context, which
  // is not persisted at all.
  const initialRequest = draft.align.request;

  const editorRef = useRef<ContentEditorRef>(null);
  const [hasContent, setHasContent] = useState(initialRequest.trim().length > 0);

  /**
   * An alignment opened from a comment is seeded with that comment, and the
   * seed lands AFTER this panel is already on screen: the source-context
   * preview is a request of its own, while `ContentEditor` is uncontrolled and
   * reads `defaultValue` once, at mount. So a request that arrives late has to
   * be pushed into the live document — the same write-back `insertMarkdownAtEnd`
   * exists for, where an upload settles against an editor that never owned its
   * promise (DENE-452).
   *
   * Only into an EMPTY editor, and only once. Anything the user has typed while
   * the preview was in flight is theirs, and a seed that re-applied would be a
   * form fighting whoever is filling it in. `insertMarkdownAtEnd` reports
   * whether it landed — the imperative handle exists from the first commit but
   * the Tiptap instance is created in a passive effect, so an insert attempted
   * in that window is a no-op — and the flag is only set once it did, so the
   * retry below runs instead of dropping the quote.
   */
  const seededRef = useRef(initialRequest.trim().length > 0);
  const [seedRetry, setSeedRetry] = useState(0);
  const seedRequest = draft.align.request;
  useEffect(() => {
    if (seededRef.current) return;
    if (seedRequest.trim().length === 0) return;
    if ((editorRef.current?.getMarkdown() ?? "").trim().length > 0) return;
    if (editorRef.current?.insertMarkdownAtEnd(seedRequest) === true) {
      seededRef.current = true;
      setHasContent(true);
      return;
    }
    // The editor is not live yet. Come back on the next frame rather than
    // spinning: this window is one passive effect wide.
    if (seedRetry >= 10) return;
    const frame = requestAnimationFrame(() => setSeedRetry((n) => n + 1));
    return () => cancelAnimationFrame(frame);
  }, [seedRequest, seedRetry]);
  const [runtimeId, setRuntimeId] = useState("");
  // Model and reasoning level live here rather than in the draft store: they are
  // frozen onto the carrier when the session is created and can never be changed
  // afterwards, so persisting them would offer the next alignment a choice its
  // conversation could not honour. The capability set IS persisted (it is a
  // property of the alignment being described), so it rides the draft store like
  // the request does.
  const [model, setModel] = useState("");
  const [thinkingLevel, setThinkingLevel] = useState("");
  // What the boxes start as: this user's last actually-used combination, on
  // any workspace (DENE-692). Keyed on the user so a late sign-in or an account
  // switch never inherits somebody else's selection. Until that id is known the
  // panel shows a placeholder and will not start — reading the unscoped key
  // would be someone else's record. A first use reads back as the system
  // default; an unreadable record does too, and says so once.
  const remembered = useMemo(
    () => (currentUserId ? readIssueDraftCapabilityPreference(currentUserId) : null),
    [currentUserId],
  );
  const capabilitiesLoading = remembered === null;
  // An edit made in this open (including a switch to the other face and back)
  // wins over the remembered record. `undefined` means this open has not
  // touched the boxes, so the record — or the system default — is what shows.
  const capabilities: readonly IssueDraftCapabilityKey[] =
    draft.align.capabilities ?? remembered?.capabilities ?? [];

  const draftsQuery = useQuery(issueDraftListOptions(wsId));
  const runtimesQuery = useQuery(runtimeListOptions(wsId));
  // The picker names each machine's owner, so it needs the member list the
  // preview panel already reads for the same rows.
  const membersQuery = useQuery(memberListOptions(wsId));
  const start = useStartIssueDraft(wsId);

  const uploadGate = useUploadGate(editorRef);
  const {
    attachments: draftAttachments,
    handleUpload,
    gate,
  } = useIssueCreateUploads("align", uploadGate, editorRef);

  const { isDragOver, dropZoneProps } = useFileDropZone({
    onDrop: (files) => files.forEach((f) => editorRef.current?.uploadFile(f)),
  });

  // Set the persisted draft's active mode so a later reopen (and any reader of
  // the unified draft) knows which form the user is editing in.
  useEffect(() => {
    setActiveMode("align");
  }, [setActiveMode]);

  // Defer focus so it lands after the dialog's focus trap has settled.
  useEffect(() => {
    const id = requestAnimationFrame(() => editorRef.current?.focus());
    return () => cancelAnimationFrame(id);
  }, []);

  const runtimes = runtimesQuery.data ?? [];
  const usableRuntimes = useMemo(
    () =>
      (runtimesQuery.data ?? []).filter((runtime) =>
        isRuntimeUsableForUser(runtime, currentUserId),
      ),
    [runtimesQuery.data, currentUserId],
  );

  // The SEED, not the answer: the toolbar's picker is what the user changes,
  // and this only decides what it starts on. Online first, then the user's own
  // machines, then anything else in the workspace they may use.
  //
  // Online has to outrank "mine", which the picker's own seed does not: the
  // list arrives ordered by `created_at` and `isRuntimeUsableForUser` says
  // nothing about status, so its "first machine I own" is really "oldest
  // machine I own". Opening on an offline machine while an online one sits in
  // the same list would greet the user with "cannot start" on a dialog they
  // have not touched yet — recoverable now that the picker is here, but still
  // the wrong first impression.
  const seededRuntimeId = useMemo(() => {
    const online = usableRuntimes.filter((runtime) => runtime.status === "online");
    // Falls back to the offline set only so the message below can name a
    // machine; submitting still requires an online one.
    const pool = online.length > 0 ? online : usableRuntimes;
    return (
      (pool.find((runtime) => runtime.owner_id === currentUserId) ?? pool[0])
        ?.id ?? ""
    );
  }, [usableRuntimes, currentUserId]);

  // Fills an empty selection only — a choice the user has made is never
  // overwritten, including by runtimes arriving later over WS.
  //
  // The mounted picker seeds an empty selection too, to the first usable row in
  // its own filter. Both fire in the same commit and this one is what sticks:
  // child effects run before the parent's, so the picker's write lands first
  // and is immediately replaced by the online-preferring pick here. Whichever
  // ran last, the next render sees a non-empty selection and both guards hold.
  useEffect(() => {
    if (runtimeId !== "" || !seededRuntimeId) return;
    setRuntimeId(seededRuntimeId);
  }, [runtimeId, seededRuntimeId]);

  const selectedRuntime =
    runtimes.find((runtime) => runtime.id === runtimeId) ?? null;
  const runtimeOnline = selectedRuntime?.status === "online";
  const runtimesLoading = runtimesQuery.isLoading;
  const hasUsableRuntime = usableRuntimes.length > 0;

  // The banner offers work to RESUME, so the terminal records the same list
  // now carries are filtered out here: "you have 2 unfinished alignments" must
  // count the ones with a next turn in them (DENE-371).
  const drafts: IssueDraftSummary[] = unfinishedIssueDrafts(draftsQuery.data ?? []);
  // `gate` is the coordinator-wide gate: it counts the shared pool's
  // placeholders too, so a file still uploading on the manual face keeps this
  // face's button disabled as well — the first turn binds the same pool.
  const canSubmit =
    hasContent && runtimeOnline && !start.isPending && !gate.uploading && !capabilitiesLoading;

  const resume = (draftId: string) => {
    onClose();
    navigation.push(paths.newIssueDraft(draftId));
  };

  const submit = async () => {
    if (!canSubmit || !selectedRuntime || gate.isBlocked() || capabilitiesLoading) return;
    const request = editorRef.current?.getMarkdown()?.trim() ?? "";
    if (!request) return;
    // Only the ids whose markdown link the request still references: a file
    // uploaded on the manual face and then removed from the align body must
    // not ride along into the conversation (same rule as the other panels).
    const activeAttachmentIds = draftAttachments
      .filter((attachment) => contentReferencesAttachment(request, attachment))
      .map((attachment) => attachment.id);
    const result = await start
      .mutateAsync({
        runtimeId: selectedRuntime.id,
        model: model || undefined,
        thinkingLevel: thinkingLevel || undefined,
        request,
        attachmentIds: activeAttachmentIds.length > 0 ? activeAttachmentIds : undefined,
        // Only the boxes that are actually ticked, and always as a list: an
        // emptied picker sends `[]` ("none of them") rather than omitting the
        // field, which the server reads as "use the built-in default".
        capabilities: encodeIssueDraftCapabilities(capabilities),
        // Stored on the draft at creation, so the whole group the conversation
        // settles on is filed under it — a project chosen here is not a display
        // preference the page reads back later.
        projectId,
        // Same reasoning for the parent (DENE-452). Absent for an alignment
        // that founds its own top-level issue, so the stored payload reads
        // exactly as it did before mid-flight alignment existed.
        ...(parentIssueId ? { parentIssueId } : {}),
      })
      // No session means no conversation to navigate to, and the reason is
      // already on screen: `entryFailureMessage` renders it from `start.error`,
      // which React Query keeps. Caught here so `void submit()` turns a stated
      // failure into a return instead of an unhandled rejection.
      .catch(() => null);
    if (!result) return;
    // Remembered only once the alignment really started: the preference is
    // "the combination last USED", not the last one somebody clicked through.
    writeIssueDraftCapabilityPreference(capabilities, currentUserId);
    onClose();
    // Navigating even when the first turn failed: the draft exists and holds
    // the request, so staying would only invite the user to create a second
    // one. The page's composer is where a lost turn is resent.
    if (!result.seeded) {
      // Hand the loss to the page this navigation lands on — the panel is gone
      // from here on, and a conversation that opens empty without saying why is
      // what made the failure look like the user's own mistake.
      setAlign({ seedFailedDraftId: result.draftId });
    }
    navigation.push(paths.newIssueDraft(result.draftId));
  };

  return (
    <>
      <DialogTitle className="sr-only">{t(($) => $.alignment.entry_title)}</DialogTitle>

      {/* `min-h-[140px] flex-1 overflow-y-auto` is the agent panel's proven
          shape (MUL-6236): the region absorbs the delta against the card's
          max height, and the floor keeps a content-driven card from collapsing
          the scroll area to nothing. */}
      <div className="min-h-[140px] flex-1 overflow-y-auto px-6 pt-5 pb-2">
        <UnfinishedIssueDraftsBanner wsId={wsId} drafts={drafts} onResume={resume} />

        <span className="flex size-11 items-center justify-center rounded-lg bg-primary/10 text-primary">
          <Sparkles className="size-5" aria-hidden="true" />
        </span>
        <h2 className="mt-4 text-title-sm font-semibold">
          {t(($) => $.alignment.entry_title)}
        </h2>
        <p className="mt-2 text-body leading-6 text-muted-foreground">
          {t(($) => $.alignment.entry_description)}
        </p>

        <div
          {...dropZoneProps}
          className="relative mt-4 min-h-32 overflow-y-auto rounded-lg border border-border bg-background px-3 py-2 transition-colors focus-within:border-input"
        >
          <ContentEditor
            ref={editorRef}
            defaultValue={initialRequest}
            placeholder={t(($) => $.alignment.entry_placeholder)}
            onUpdate={(md) => {
              setAlign({ request: md });
              setHasContent(md.trim().length > 0);
            }}
            onSubmit={() => void submit()}
            onUploadFile={handleUpload}
            onUploadingChange={uploadGate.onUploadingChange}
            debounceMs={300}
            attachments={draftAttachments}
          />
          {isDragOver && <FileDropOverlay />}
        </div>

        {!runtimesLoading && !hasUsableRuntime ? (
          <p className="mt-4 text-body text-muted-foreground">
            {t(($) => $.alignment.entry_no_runtime)}
          </p>
        ) : null}

        {/* A seeded but offline runtime blocks submitting, which a disabled
            button alone never explains. Only the machines list can fix it, so
            this stays a statement, not a second action. */}
        {selectedRuntime && !runtimeOnline ? (
          <p role="status" className="mt-4 text-body text-destructive">
            {t(($) => $.alignment.entry_runtime_offline, {
              name: runtimeDisplayName(selectedRuntime),
            })}
          </p>
        ) : null}

        {capabilitiesLoading ? (
          <p role="status" className="mt-4 text-body text-muted-foreground">
            {t(($) => $.alignment.capability_preference_loading)}
          </p>
        ) : null}

        {/* A failed read is one notice, not a blocked start: the boxes are the
            system default and the submit button stays available. */}
        {!capabilitiesLoading && remembered?.failed ? (
          <p role="status" className="mt-4 text-body text-muted-foreground">
            {t(($) => $.alignment.capability_preference_failed)}
          </p>
        ) : null}

        {start.isError ? (
          <p role="alert" className="mt-4 text-body text-destructive">
            {entryFailureMessage(start.error, t)}
          </p>
        ) : null}
      </div>

      <div className="grid grid-cols-[auto_1fr] items-center gap-x-2 gap-y-2.5 border-t px-4 py-3 shrink-0 sm:flex sm:flex-wrap">
        {/* Attach + project + runtime: the first two are properties of the
            issue being filed, the third is where the conversation runs, and
            none of them is the one thing this face asks for, so all three stay
            on the toolbar row (DENE-367). `flex-wrap` + `min-w-0`: below the
            640px breakpoint three pills do not fit one line, and wrapping is
            what keeps the submit button on screen — both pills cap themselves
            at 14rem and truncate a long machine or project name. */}
        <div className="flex min-h-7 min-w-0 flex-wrap items-center gap-2 sm:mr-auto">
          <FileUploadButton
            size="sm"
            multiple
            onSelect={(file) => editorRef.current?.uploadFile(file)}
          />
          <ProjectPicker
            projectId={projectId ?? null}
            onUpdate={(updates) => setShared({ projectId: updates.project_id ?? undefined })}
            triggerRender={
              <ClearablePillButton
                onClear={projectId ? () => setShared({ projectId: undefined }) : undefined}
                clearLabel={tProjects(($) => $.picker.clear_aria)}
              />
            }
            align="start"
          />
          {/* One pill for the four choices the conversation cannot be started
              without deciding (DENE-514): which machine, which model, how hard
              it thinks, and which built-in alignment methods run. The machine
              used to be a pill of its own and the other three could not be
              chosen at all — the model and the effort are read off the carrier
              agent row the daemon claims, so they are create-time settings, and
              the methods are assembled into its prompt at creation. Not
              clearable either way: the alignment has to run SOMEWHERE, so there
              is no empty state to offer, unlike the project, which is optional
              by design. */}
          <AlignmentConfigPicker
            runtimes={runtimes}
            runtimesLoading={runtimesLoading}
            members={membersQuery.data ?? []}
            currentUserId={currentUserId}
            runtimeId={runtimeId}
            onRuntimeChange={setRuntimeId}
            model={model}
            onModelChange={setModel}
            thinkingLevel={thinkingLevel}
            onThinkingLevelChange={setThinkingLevel}
            capabilities={capabilities}
            capabilitiesLoading={capabilitiesLoading}
            onCapabilitiesChange={(next) => setAlign({ capabilities: [...next] })}
            disabled={start.isPending || capabilitiesLoading}
          />
        </div>
        {/* The way back to filing this as an issue. The body stays in the
            align slot, and the manual face keeps its own — a switch is a
            no-op on the other side's data. */}
        <button
          type="button"
          onClick={() => onSwitchMode?.(null)}
          disabled={gate.uploading}
          aria-disabled={gate.uploading || undefined}
          aria-busy={gate.uploading || undefined}
          title={tModals(($) => $.create_issue.switch_from_align_tooltip)}
          className="flex shrink-0 items-center gap-1.5 justify-self-end text-caption px-2 py-1 rounded-sm text-muted-foreground hover:text-foreground hover:bg-accent/60 transition-colors cursor-pointer disabled:cursor-not-allowed disabled:opacity-50"
        >
          <ArrowLeftRight className="size-3.5" />
          {tModals(($) => $.create_issue.switch_from_align)}
        </button>
        <Button variant="ghost" size="sm" onClick={onClose} disabled={start.isPending}>
          {t(($) => $.alignment.entry_cancel)}
        </Button>
        {!runtimesLoading && !hasUsableRuntime ? (
          <Button
            size="sm"
            render={<AppLink href={paths.runtimes()} />}
            nativeButton={false}
          >
            {t(($) => $.alignment.entry_connect_runtime)}
          </Button>
        ) : (
          <Button size="sm" onClick={() => void submit()} disabled={!canSubmit}>
            {start.isPending ? (
              <Loader2 className="size-4 animate-spin" aria-hidden="true" />
            ) : null}
            {start.isPending
              ? t(($) => $.alignment.entry_submitting)
              : t(($) => $.alignment.entry_submit)}
          </Button>
        )}
      </div>
    </>
  );
}

/**
 * Why the entry failed, in the terms the user can act on.
 *
 * `clientErrorMessage` is the repo's existing contract for exactly this: a 4xx
 * message is written by the handler FOR the reader ("runtime must be online to
 * start an issue draft session") and is worth showing verbatim, while a 5xx
 * message is internal detail that must never be rendered (MUL-6472). Collapsing
 * every exit into one sentence is what made the DENE-366 screenshot
 * unreportable, so this is the only place the exits are told apart — no local
 * status sniffing that could drift from that helper.
 *
 * A 5xx still has to say which class of failure it was, and the status code is
 * the safe part of that; a transport failure carries no status at all and keeps
 * the plain sentence.
 */
function entryFailureMessage(error: unknown, t: TFunction<"issues">): string {
  const server = clientErrorMessage(error);
  if (server) return server;
  // Drift is named as drift. The draft was created and the request stored on the
  // server before this client lost the response, so "could not start" would
  // point the user at a second, orphaned draft.
  if (error instanceof IssueDraftSessionUnrecognizedError) {
    return t(($) => $.alignment.entry_response_unrecognized);
  }
  if (error instanceof ApiError) {
    return t(($) => $.alignment.entry_failed_status, { status: error.status });
  }
  return t(($) => $.alignment.entry_failed);
}

/** className for DialogContent in align mode. The shell (which owns the
 *  DialogContent) applies it, so a switch into alignment swaps only the inner
 *  panel — the Portal, Backdrop and Popup stay in the DOM. Exported from here
 *  so the sizing stays next to the face that needs it. */
export function alignDialogContentClass() {
  return cn(
    "p-0 gap-0 flex flex-col overflow-hidden",
    "!top-1/2 !left-1/2 !-translate-x-1/2 !-translate-y-1/2",
    "!transition-all !duration-300 !ease-out",
    // Phone gutter — see the matching note in create-issue-dialog.tsx. The
    // height stays content-driven (capped at 80% of the viewport, like the
    // ordinary agent form) so this face opens at the size of its own content
    // instead of a mostly-empty tall card.
    "!w-full !max-w-[calc(100vw-1.5rem)]",
    "!max-h-[80dvh] sm:!max-w-xl",
  );
}
