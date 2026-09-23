"use client";

import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { cn } from "@multica/ui/lib/utils";
import { Dialog, DialogContent } from "@multica/ui/components/ui/dialog";
import {
  useCreateModeStore,
  type CreateMode,
} from "@multica/core/issues/stores/create-mode-store";
import { issueDraftCommentSeed } from "@multica/core/issue-drafts";
import type { SourceContextPreview } from "@multica/core/types";
import { AgentCreatePanel } from "./quick-create-issue";
import { ManualCreatePanel, manualDialogContentClass } from "./create-issue";
import { AlignCreatePanel, alignDialogContentClass } from "./align-create-issue";
import { sourceContextPreviewOptions } from "@multica/core/issues/queries";
import { useIssueDraftStore } from "@multica/core/issues/stores/draft-store";
import { useWorkspaceId } from "@multica/core/hooks";

/**
 * Shell that owns the single `<Dialog>` AND `<DialogContent>` for the
 * create-issue flow. Mode switching unmounts/mounts only the inner panel
 * body — the Portal, Backdrop, and Popup all stay in the DOM, so Base UI
 * never replays the open animation. That's what makes the switch feel
 * instant; an earlier version put `<DialogContent>` inside each panel and
 * the close→open animation cycle still fired on every toggle.
 *
 * `initialMode` comes from the modal registry (`quick-create-issue` →
 * agent, `create-issue` → manual, or `create-issue` + `data.initial_mode:
 * "align"` → the alignment face). Subsequent switches are local state only
 * and never round-trip through the modal store.
 *
 * Carry payload: when a panel switches mode it can hand a payload up via
 * `onSwitchMode`; the shell stores it as the next panel's `data` so seeding
 * works exactly like a fresh open.
 *
 * Manual-mode `isExpanded` is lifted up because it drives `DialogContent`'s
 * className — the className lives here in the shell since the Popup is here,
 * but the toggle for that state lives in the manual panel body.
 */
export function CreateIssueDialog({
  onClose,
  initialMode,
  data,
}: {
  onClose: () => void;
  initialMode: CreateMode;
  data?: Record<string, unknown> | null;
}) {
  const anchorCommentId = typeof data?.anchor_comment_id === "string"
    ? data.anchor_comment_id
    : null;
  if (anchorCommentId) {
    return (
      <SourceContextCreateIssueDialog
        onClose={onClose}
        initialMode={initialMode}
        data={data}
        anchorCommentId={anchorCommentId}
      />
    );
  }
  return <CreateIssueDialogBody onClose={onClose} initialMode={initialMode} data={data} />;
}

function SourceContextCreateIssueDialog({
  onClose,
  initialMode,
  data,
  anchorCommentId,
}: {
  onClose: () => void;
  initialMode: CreateMode;
  data?: Record<string, unknown> | null;
  anchorCommentId: string;
}) {
  const wsId = useWorkspaceId();
  const [draftReady, setDraftReady] = useState(false);
  const [sourceContextExpanded, setSourceContextExpanded] = useState(false);
  const sourceQuery = useQuery(sourceContextPreviewOptions(wsId, anchorCommentId));

  useLayoutEffect(() => {
    useIssueDraftStore.getState().beginIsolatedDraft();
    setDraftReady(true);
    return () => useIssueDraftStore.getState().endIsolatedDraft();
  }, []);

  if (!draftReady) return null;

  return (
    <CreateIssueDialogBody
      onClose={onClose}
      initialMode={initialMode}
      data={data}
      sourceContextExpanded={sourceContextExpanded}
      sourceContextData={{
        anchor_comment_id: anchorCommentId,
        source_context_preview: sourceQuery.isError || sourceQuery.isFetching
          ? undefined
          : sourceQuery.data,
        source_context_loading: sourceQuery.isLoading || sourceQuery.isFetching,
        source_context_failed: sourceQuery.isError,
        source_context_error: sourceQuery.error,
        source_context_refetch: sourceQuery.refetch,
        source_context_expanded: sourceContextExpanded,
        source_context_on_expanded_change: setSourceContextExpanded,
      }}
    />
  );
}

function CreateIssueDialogBody({
  onClose: closeDialog,
  initialMode,
  data,
  sourceContextData,
  sourceContextExpanded = false,
}: {
  onClose: () => void;
  initialMode: CreateMode;
  data?: Record<string, unknown> | null;
  sourceContextData?: Record<string, unknown>;
  sourceContextExpanded?: boolean;
}) {
  const setLastMode = useCreateModeStore((s) => s.setLastMode);

  // Closing the dialog throws the unsent draft away (DENE-421). A draft that
  // has not been submitted — and, on the alignment face, has not opened a
  // conversation — is scratch input, so reopening "New issue", "Hand to an
  // agent" or "Align first" starts blank rather than resurrecting whatever was
  // typed and abandoned days ago. Everything worth keeping already has a
  // durable home by then: a submitted issue, or a server-side alignment draft
  // that the unfinished-alignments banner lists and can resume.
  //
  // This deliberately reverses MUL-5181's cross-open persistence; the store
  // still spans a mode switch WITHIN one open, which is what kept a manual body
  // and an agent prompt from destroying each other. `clearDraft` (not a full
  // reset) so the last-assignee preference survives, same as after a submit.
  const onClose = () => {
    useIssueDraftStore.getState().clearDraft();
    closeDialog();
  };

  const [mode, setMode] = useState<CreateMode>(initialMode);
  const [panelData, setPanelData] = useState(data ?? null);
  const [isExpanded, setIsExpanded] = useState(false);
  const effectiveData = sourceContextData
    ? { ...(panelData ?? {}), ...sourceContextData }
    : panelData;

  // The issue an alignment opened from an existing issue is filed UNDER
  // (DENE-452). Per-invocation, like the manual face's parent context: it
  // arrives in the modal payload (the detail page's "start aligning", the
  // comment menu's, or the manual face's carry) and is handed to the alignment
  // face so the draft records it. It is deliberately NOT persisted in the draft
  // store — see `AlignCreatePanel`.
  const alignParentIssueId =
    typeof effectiveData?.parent_issue_id === "string" &&
    effectiveData.parent_issue_id.length > 0
      ? effectiveData.parent_issue_id
      : undefined;

  // The alignment face reads the project from the shared draft, not from this
  // payload. Seed it once from the parent so the picker shows the inherited
  // project. A project already in the draft — picked on the manual face, or
  // cleared there — is left alone.
  const projectSeedApplied = useRef(false);
  useLayoutEffect(() => {
    if (projectSeedApplied.current) return;
    if (!effectiveData || !("project_id" in effectiveData)) return;
    projectSeedApplied.current = true;
    const seeded = effectiveData.project_id;
    if (typeof seeded !== "string" || seeded.length === 0) return;
    const store = useIssueDraftStore.getState();
    if (store.draft.shared.projectId) return;
    store.setShared({ projectId: seeded });
  }, [effectiveData]);

  // An alignment opened FROM a comment thread starts on that comment rather
  // than on an empty box (DENE-452). The seed is written ONCE, the first time it
  // is both available and safe to write:
  //
  //   - the preview is a request of its own, so it lands a beat after the dialog
  //     opens; until it does there is nothing to quote and the panel shows its
  //     normal empty state;
  //   - `applied` makes it once per open, so a user who deletes the quote and
  //     writes their own sentence keeps it — a seed that came back on every
  //     render would be a form that fights its user;
  //   - it only ever fills an EMPTY align slot. A request already there belongs
  //     to whoever typed it — the manual face's mode switch seeds this same slot
  //     on its way out.
  //
  // It goes through the store rather than a prop because that is where the
  // align panel reads its body from (`draft.align.request`), exactly as it does
  // for a request carried over from the manual face.
  const alignSeed = issueDraftCommentSeed(
    effectiveData?.source_context_preview as SourceContextPreview | undefined,
  );
  const alignSeedApplied = useRef(false);
  useEffect(() => {
    if (alignSeedApplied.current || alignSeed === null) return;
    alignSeedApplied.current = true;
    const store = useIssueDraftStore.getState();
    if (store.draft.align.request.trim().length > 0) return;
    store.setAlign({ request: alignSeed });
  }, [alignSeed]);

  const switchTo = (next: CreateMode) => (carry?: Record<string, unknown> | null) => {
    // The alignment face is NOT a filing preference: remembering it would make
    // the `c` shortcut and the sidebar's "New issue" reopen alignment for
    // someone who only ever meant to file an issue.
    if (next !== "align") setLastMode(next);
    setPanelData(carry ?? null);
    setMode(next);
  };

  const className =
    mode === "agent"
      ? cn(
          "p-0 gap-0 flex flex-col overflow-hidden",
          "!top-1/2 !left-1/2 !-translate-x-1/2 !-translate-y-1/2",
          // Smooth size transition when switching modes — the manual mode
          // uses the same easing.
          "!transition-all !duration-300 !ease-out",
          // Phone gutter. The widths below are `!important` so they beat
          // DialogContent's own sizing — which also made them beat its
          // `max-w-[calc(100%-2rem)]` safety margin, leaving the card flush
          // against both screen edges on a 430px viewport (MUL-6236). Restore
          // the margin here and let the `sm:` widths take over above 640px.
          "!w-full !max-w-[calc(100vw-1.5rem)]",
          // Source-context create needs numeric collapsed/expanded endpoints
          // so its preview transition can interpolate the height. Ordinary
          // quick create has no expanding preview and keeps its original
          // content-driven height, capped for mobile browser chrome.
          isExpanded
            ? "!h-5/6 sm:!max-w-4xl"
            : sourceContextData
              ? sourceContextExpanded
                ? "!h-5/6 sm:!max-w-2xl"
                : "!h-96 sm:!max-w-xl"
              : "!max-h-[80dvh] sm:!max-w-xl",
        )
      : mode === "align"
        ? alignDialogContentClass()
        : cn(
            manualDialogContentClass(isExpanded),
            sourceContextExpanded && "!h-5/6",
          );

  return (
    <Dialog open onOpenChange={(v) => { if (!v) onClose(); }}>
      <DialogContent
        finalFocus={false}
        showCloseButton={false}
        className={className}
      >
        {mode === "agent" ? (
          <AgentCreatePanel
            onClose={onClose}
            onSwitchMode={switchTo("manual")}
            data={effectiveData}
            isExpanded={isExpanded}
            setIsExpanded={setIsExpanded}
          />
        ) : mode === "align" ? (
          // The parent context is read off the payload, not the store. The
          // project is the one seed that crosses over on its own — the manual
          // face commits it to `draft.shared` on the way out, so opening this
          // dialog from a project page and switching to alignment files the
          // whole group under that project — while priority, due date and stage
          // still do not apply: the conversation decides its own group.
          <AlignCreatePanel
            onClose={onClose}
            parentIssueId={alignParentIssueId}
            // Hands the untouched payload back on the way out. The alignment
            // face reads none of it, but the manual face's parent context is
            // per-invocation and NOT persisted in the draft store, so dropping
            // it here would turn "Add sub issue" → align → back into a
            // top-level issue without saying so.
            onSwitchMode={(carry) => switchTo("manual")(carry ?? panelData)}
          />
        ) : (
          <ManualCreatePanel
            onClose={onClose}
            onSwitchMode={switchTo("agent")}
            onSwitchToAlign={switchTo("align")}
            data={effectiveData}
            isExpanded={isExpanded}
            setIsExpanded={setIsExpanded}
          />
        )}
      </DialogContent>
    </Dialog>
  );
}
