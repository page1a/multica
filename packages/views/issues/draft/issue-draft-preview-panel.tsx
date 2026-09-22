"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { ExternalLink, FileText, Loader2, Trash2 } from "lucide-react";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  applyIssueDraftAssigneeSuggestions,
  type DraftAssigneeSuggestion,
} from "@multica/core/issue-drafts";
import {
  maxIssueDraftChildStage,
  normalizeIssueDraftPayloadGroup,
  planIssueDraftGroup,
  sameIssueDraftChildren,
} from "@multica/core/issue-drafts";
import type {
  Attachment,
  Issue,
  IssueAssigneeType,
  IssueDraftChild,
  IssueDraftCreatedIssue,
  IssueDraftPayload,
  IssuePriority,
  IssueStatus,
  MemberWithUser,
  RuntimeDevice,
} from "@multica/core/types";
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
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { cn } from "@multica/ui/lib/utils";
import { AppLink } from "../../navigation";
import { RuntimePicker } from "../../agents/components/runtime-picker";
import { ProjectPicker } from "../../projects/components/project-picker";
import { AssigneePicker } from "../components/pickers/assignee-picker";
import { PriorityPicker } from "../components/pickers/priority-picker";
import { StagePicker } from "../components/pickers/stage-picker";
import { StatusPicker } from "../components/pickers/status-picker";
import { useT } from "../../i18n";

/**
 * The right-hand column: exactly what pressing "confirm and create" will write.
 *
 * The fields the server reads at finalize are the fields shown here, and they
 * are editable — a conversation is a good way to arrive at a draft and a bad way
 * to fix a typo in it. The project belongs to that set for the same reason the
 * assignee does: the carrier is never told about either, so if the panel did not
 * offer it, nothing could. Everything is local until "save", so the preview
 * never races the carrier's own revisions into the server one keystroke at a
 * time.
 *
 * The four fields describe the group's PARENT. Since DENE-411 the same
 * conversation can settle on a parent plus sub-issues, so the panel also edits
 * that list — title, stage, assignee, or deleting a row — and states, before
 * anyone presses confirm, what that confirm will actually start. That last part
 * is not decoration: a confirm can enqueue several agents at once, and the
 * stage rule (stage 1 runs, later stages wait in Backlog) is only knowable
 * here.
 */
export function IssueDraftPreviewPanel({
  draft,
  stage,
  canConfirm,
  saving,
  saved,
  confirming,
  abandoning,
  runtime,
  runtimes,
  runtimesLoading,
  members,
  assigneeSuggestions,
  currentUserId,
  switchingRuntime,
  pending,
  attachments,
  readOnly: readOnlyProp,
  producedIssueId,
  createdIssues,
  round,
  continuation,
  builtChildren,
  builtKeys,
  onDirtyChange,
  onSave,
  onGenerate,
  onConfirm,
  onAbandon,
  onSwitchRuntime,
}: {
  draft: IssueDraftPayload | null;
  stage: "aligning" | "ready" | "creating" | "created";
  canConfirm: boolean;
  saving: boolean;
  saved: boolean;
  confirming: boolean;
  abandoning: boolean;
  runtime: RuntimeDevice | null;
  runtimes: RuntimeDevice[];
  runtimesLoading: boolean;
  members: MemberWithUser[];
  assigneeSuggestions?: readonly (DraftAssigneeSuggestion | null)[];
  currentUserId: string | null;
  switchingRuntime: boolean;
  /** A turn is running: nothing may be written while the carrier is replying. */
  pending: boolean;
  /**
   * The files this conversation produced, which the confirm hands to the ROOT
   * issue. Absent (or empty) means the alignment touched no file at all, and
   * the panel then says nothing about files — there is no empty state to
   * explain, exactly as the project and assignee pickers have none (DENE-453).
   */
  attachments?: readonly Attachment[];
  /**
   * Render what was agreed and nothing more — the shape a FINISHED alignment is
   * read back in (DENE-371). Every field is disabled and the whole action strip
   * is replaced by the outcome: an alignment whose draft is terminal can be
   * neither saved, confirmed nor abandoned, and a disabled button that can
   * never enable is worse than no button.
   */
  readOnly?: boolean;
  /** The issue this alignment produced, for the record's way across to it. */
  producedIssueId?: string | null;
  /**
   * The whole group this alignment produced, root first. Absent until a confirm
   * has answered; a backend that predates groups answers with the parent alone,
   * which is what `[issue_id]` means (DENE-411).
   */
  createdIssues?: IssueDraftCreatedIssue[] | null;
  /**
   * Which round this alignment is on: 1 for a first pass, one more per reopen.
   * Only shown past the first, because "round 1" on a brand-new alignment is a
   * label about nothing. (DENE-415)
   */
  round?: number;
  /**
   * This round adds to a group that already exists. What changes is the promise
   * under the confirm button and the split of the sub-issue list — the parent
   * fields and the confirm itself are the same action either way.
   */
  continuation?: boolean;
  /**
   * The group as the server reads it back: the root's children, each carrying
   * the node id it was created under. These are the issues this round ADOPTS
   * and never rewrites, which is why they are rendered from the server's own
   * rows rather than from the payload: the payload can be edited, the group
   * cannot.
   */
  builtChildren?: readonly Issue[];
  /**
   * The payload sub-issue keys that already own an issue. Read off the same
   * node ids `builtChildren` carry, so this is the same judgement stated as
   * "which of the rows below are already real".
   */
  builtKeys?: ReadonlySet<string>;
  /**
   * Reports whether the editor holds unsaved edits. The alignment session uses
   * it to decide whether it may adopt a carrier revision on its own: an edit in
   * progress is the user's, and nothing may be written over it.
   */
  onDirtyChange: (dirty: boolean) => void;
  onSave: (draft: IssueDraftPayload, status?: "draft" | "ready") => Promise<boolean>;
  /** Folds the carrier's latest draft block into what is on screen and marks it
   *  ready — the step that turns "we agreed" into something confirmable.
   *  Resolves with the draft the server now holds, or null when nothing was
   *  written; the editor adopts it. */
  onGenerate: (draft: IssueDraftPayload) => Promise<IssueDraftPayload | null>;
  onConfirm: () => Promise<boolean>;
  onAbandon: () => Promise<boolean>;
  onSwitchRuntime: (runtimeId: string) => Promise<string | null>;
}) {
  const { t } = useT("issues");
  const paths = useWorkspacePaths();
  const record = readOnlyProp === true;
  const [editing, setEditing] = useState<IssueDraftPayload | null>(draft);
  const [confirmingAbandon, setConfirmingAbandon] = useState(false);

  // The server revision the editor was last synchronised with. `dirty` is "the
  // user has typed something since then", NOT "the editor differs from the
  // server": the server moves on its own, because every carrier reply is folded
  // into the draft as it arrives. Comparing against the live server value made
  // that ordinary movement look like an unsaved edit — the panel froze on the
  // seed values it mounted with, reported `dirty` forever, and that in turn
  // blocked the fold from ever updating it again (DENE-319).
  const [baseline, setBaseline] = useState<IssueDraftPayload | null>(draft);
  const dirty =
    editing !== null && baseline !== null && !sameDraft(editing, baseline);

  /** Take the server's revision as both what is shown and what "clean" means. */
  const adopt = useCallback((next: IssueDraftPayload | null) => {
    setEditing(next);
    setBaseline(next);
  }, []);

  // A new server revision replaces the local copy only when the user has
  // nothing unsaved, which is what keeps a carrier reply from wiping an edit in
  // progress.
  useEffect(() => {
    if (!dirty) adopt(draft);
  }, [adopt, draft, dirty]);
  useEffect(() => {
    onDirtyChange(dirty);
  }, [dirty, onDirtyChange]);

  const value = editing ?? draft ?? EMPTY_DRAFT;
  const children = value.children ?? [];
  // The reference material that will be carried, stated once so the section
  // below and its own emptiness agree. Sorted by filename is deliberately NOT
  // done: the caller hands these over in the order the conversation produced
  // them, which is the order someone reading the transcript remembers.
  const carriedAttachments = attachments ?? EMPTY_ATTACHMENTS;
  // Rows a previous round already created. They stay in `value` — the payload
  // is one object and the confirm reads it whole — but they are not offered for
  // editing: whatever is typed over one is dropped server-side, so letting the
  // field look editable would be a promise the confirm cannot keep.
  const adopted = builtKeys ?? EMPTY_BUILT_KEYS;
  const isContinuation = continuation === true;
  const newChildren = children
    .map((child, index) => ({ child, index }))
    .filter(({ child }) => !adopted.has(child.key));
  // What confirming will create and start. Read off the editor value, not the
  // stored draft: the user is editing this group, and the counts have to follow
  // what is on screen or they are answering a different question.
  const groupPlan = planIssueDraftGroup(value, adopted);
  const locked = record || pending || confirming || stage === "created";
  // The parent fields lock one state earlier than the rest: on a continuation
  // round the root issue already exists, and the server adopts it without
  // rewriting a single field. Leaving the title editable would invite an edit
  // the confirm silently drops. The NEW sub-issue rows stay editable — they are
  // what the round is for.
  const parentLocked = locked || isContinuation;
  // Unsaved edits and the confirm cannot both be right: see the footer.
  const confirmNeedsSave = dirty && !locked;
  const canSave = !locked && !saving && value.title.trim().length > 0;
  // A title is not required to generate: the carrier's block is the only place
  // a title comes from before someone types one, so gating this button on the
  // title deadlocks the one step that can supply it. "Nothing to fold in yet"
  // is answered by the generate call itself, which writes nothing.
  const canGenerate = !locked && !saving;
  // Whether the server still has a draft to write. Once it is gone — the row
  // was confirmed or retired — the panel is a read-only leftover, and anything
  // it writes is a request against a draft that no longer exists.
  const hasDraft = draft !== null;
  // Which lifecycle state an explicit save writes back. `ready` is preserved:
  // this button refines the words of a draft the user already converged on, and
  // the server reads an omitted status as `draft` — so saving a tweak to a
  // generated preview would otherwise close the confirm gate again. The fold
  // path has held this line since DENE-279 (`planIssueDraftFold` never
  // downgrades `ready`).
  const saveStatus = stage === "ready" ? "ready" : "draft";

  // Routing's suggested seats land in the unassigned rows, where the ordinary
  // assignee pickers show them and can change or clear them. They are applied
  // only to a clean editor — the suggestions are index-aligned with the draft
  // the server holds, and unsaved row edits would misalign them — and saved
  // straight away: the confirm creates what the SERVER holds, so a suggestion
  // that only lived on screen would be shown and then not created. Each row is
  // offered once (see applyIssueDraftAssigneeSuggestions).
  const offeredRows = useRef(new Set<string>());
  useEffect(() => {
    if (!assigneeSuggestions || assigneeSuggestions.length === 0) return;
    const current = editing ?? draft;
    if (locked || saving || dirty || !current) return;
    const suggested = applyIssueDraftAssigneeSuggestions(current, assigneeSuggestions, offeredRows.current);
    if (suggested === current) return;
    const next = normalizeIssueDraftPayloadGroup(suggested);
    setEditing(next);
    void onSave(next, saveStatus).then((savedNow) => {
      if (savedNow) setBaseline(next);
    });
  }, [assigneeSuggestions, dirty, draft, editing, locked, onSave, saveStatus, saving]);

  const handleGenerate = () => {
    void onGenerate(value).then((persisted) => {
      // Adopt what was just persisted. Leaving the pre-generate value on screen
      // keeps `dirty` true forever, and the next "save draft" then overwrites
      // the generated preview with it — the carrier's work, silently lost.
      if (persisted) adopt(persisted);
    });
  };

  const handleSave = () => {
    // The group is normalized on the way out — keys, contiguous stages, and the
    // status each stage implies. The confirm sends a revision rather than a
    // payload, so whatever is stored here is what gets created; a sub-issue
    // left with a stale status would be dispatched by the wrong rule, and the
    // editor adopts the normalized value so "clean" means the same thing on
    // both sides.
    const next = normalizeIssueDraftPayloadGroup(value);
    if (next !== value) setEditing(next);
    void onSave(next, saveStatus).then((savedNow) => {
      // What the server now holds is the new clean point; the server's own echo
      // arrives as a `draft` prop and is adopted from there.
      if (savedNow) setBaseline(next);
    });
  };

  const updateChild = (index: number, patch: Partial<IssueDraftChild>) => {
    setEditing({
      ...value,
      children: children.map((child, at) =>
        at === index ? { ...child, ...patch } : child,
      ),
    });
  };

  const removeChild = (index: number) => {
    setEditing({
      ...value,
      children: children.filter((_, at) => at !== index),
    });
  };

  const handleSelectRuntime = useCallback(
    (runtimeId: string) => {
      if (!runtimeId || runtimeId === runtime?.id) return;
      // The picker seeds an empty selection by itself, and this callback is
      // what that seed lands on. With no draft on the server there is nothing
      // to rebind, and writing anyway is a request per render against a draft
      // the server has already retired.
      if (locked || !hasDraft) return;
      void onSwitchRuntime(runtimeId);
    },
    [hasDraft, locked, onSwitchRuntime, runtime?.id],
  );

  return (
    <div className="flex h-full min-h-0 flex-col border-l bg-muted/10">
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-2xl px-5 py-6">
          <div className="mb-5">
            <h2 className="text-title-sm font-semibold tracking-tight">
              {t(($) => $.alignment.preview_title)}
            </h2>
            <p className="mt-1 text-caption text-muted-foreground">
              {t(($) => $.alignment.preview_hint)}
            </p>
          </div>

          <div className="space-y-5">
            {children.length > 0 || isContinuation ? (
              <p className="text-caption font-medium text-muted-foreground">
                {t(($) => $.alignment.parent_label)}
              </p>
            ) : null}
            {/* The parent is a node like any other, so on a continuation round
                it is adopted too: the confirm never rewrites it. Saying that
                beside the fields it locks is what turns a disabled input into
                an explanation instead of a bug. */}
            {isContinuation ? (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.alignment.parent_built)}
                {producedIssueId ? (
                  <>
                    {" "}
                    <AppLink
                      href={paths.issueDetail(producedIssueId)}
                      className="text-primary hover:underline"
                    >
                      {t(($) => $.alignment.parent_built_link)}
                    </AppLink>
                  </>
                ) : null}
              </p>
            ) : null}

            <Field label={t(($) => $.alignment.field_title)} htmlFor="issue-draft-title">
              <Input
                id="issue-draft-title"
                value={value.title}
                disabled={parentLocked}
                placeholder={t(($) => $.alignment.field_title_placeholder)}
                onChange={(event) =>
                  setEditing({ ...value, title: event.target.value })
                }
              />
            </Field>

            <Field
              label={t(($) => $.alignment.field_description)}
              htmlFor="issue-draft-description"
            >
              <Textarea
                id="issue-draft-description"
                value={value.description}
                disabled={parentLocked}
                rows={10}
                placeholder={t(($) => $.alignment.field_description_placeholder)}
                onChange={(event) =>
                  setEditing({ ...value, description: event.target.value })
                }
              />
            </Field>

            {/* `inert` and not only `pointer-events-none`: these pickers take
                no `disabled` prop, and dimming them leaves their trigger in the
                tab order, so a keyboard user could still change a field this
                round's confirm silently drops — the exact promise the dimming
                is there to make. Read-only stays readable; it stops being
                reachable. */}
            <div
              inert={parentLocked && !locked}
              className={cn(
                "flex flex-wrap items-center gap-x-6 gap-y-3",
                parentLocked && !locked && "pointer-events-none opacity-60",
              )}
            >
              <div className="flex items-center gap-2">
                <span className="text-caption text-muted-foreground">
                  {t(($) => $.alignment.field_status)}
                </span>
                <StatusPicker
                  status={(value.status || null) as IssueStatus | null}
                  onUpdate={(updates) =>
                    setEditing({ ...value, status: updates.status ?? "" })
                  }
                />
              </div>
              <div className="flex items-center gap-2">
                <span className="text-caption text-muted-foreground">
                  {t(($) => $.alignment.field_priority)}
                </span>
                <PriorityPicker
                  priority={(value.priority || null) as IssuePriority | null}
                  onUpdate={(updates) =>
                    setEditing({ ...value, priority: updates.priority ?? "" })
                  }
                />
              </div>
              {/* The project is one of the fields the carrier is never told
                  about (it has no project list and is not asked to guess one),
                  which is exactly why the panel owns it. Root only: a
                  sub-issue's project is backfilled from the parent inside the
                  create transaction, so a control here would be a promise the
                  confirm overwrites. */}
              <div className="flex min-w-0 items-center gap-2">
                <span className="text-caption text-muted-foreground">
                  {t(($) => $.alignment.field_project)}
                </span>
                <ProjectPicker
                  projectId={value.project_id ?? null}
                  disabled={parentLocked}
                  onUpdate={(updates) =>
                    setEditing({ ...value, project_id: updates.project_id ?? null })
                  }
                />
              </div>
              {/* The root's own assignee. The carrier is never told about it
                  either — it has no workspace roster to resolve a name
                  against, which is why `assignee_hint` exists for children —
                  so the panel owns it exactly as it owns status, priority and
                  project. `open={false}` rather than a missing control on a
                  finished alignment: the row still reads back who it went to.
                  Children keep their own pickers; this one is the parent's. */}
              <div className="flex min-w-0 items-center gap-2">
                <span className="text-caption text-muted-foreground">
                  {t(($) => $.alignment.field_assignee)}
                </span>
                <AssigneePicker
                  assigneeType={
                    (value.assignee_type as IssueAssigneeType | null) ?? null
                  }
                  assigneeId={value.assignee_id ?? null}
                  open={parentLocked ? false : undefined}
                  align="start"
                  onUpdate={(updates) =>
                    setEditing({
                      ...value,
                      assignee_type: updates.assignee_type ?? null,
                      assignee_id: updates.assignee_id ?? null,
                    })
                  }
                />
              </div>
            </div>

            <div className="space-y-2">
              <span className="text-caption text-muted-foreground">
                {t(($) => $.alignment.entry_runtime)}
              </span>
              <RuntimePicker
                runtimes={runtimes}
                runtimesLoading={runtimesLoading}
                members={members}
                currentUserId={currentUserId}
                selectedRuntimeId={runtime?.id ?? ""}
                // The rebind has to commit server-side before the picker moves:
                // showing runtime B while messages still run on A is exactly
                // what MUL-5163 fixed in the agent builder.
                onSelect={handleSelectRuntime}
                disabled={locked || switchingRuntime || pending}
              />
            </div>

            {/* The files the conversation produced, which the confirm hands to
                the ROOT issue — a screenshot someone dropped in, the prototype
                a carrier uploaded. Rendered only when there is at least one:
                an empty "no files" placeholder would be a section about
                nothing, and the restraint the project and assignee pickers
                keep is the one this panel is read in. */}
            {carriedAttachments.length > 0 ? (
              <div className="space-y-2 border-t pt-5">
                <div>
                  <h3 className="text-body font-medium">
                    {t(($) => $.alignment.attachments_title)}
                  </h3>
                  <p className="mt-1 text-caption text-muted-foreground">
                    {t(($) => $.alignment.attachments_hint, {
                      count: carriedAttachments.length,
                    })}
                  </p>
                </div>
                <ul className="space-y-1">
                  {carriedAttachments.map((attachment) => (
                    <li key={attachment.id}>
                      <CarriedFileRow attachment={attachment} />
                    </li>
                  ))}
                </ul>
              </div>
            ) : null}

            {isContinuation ? (
              <div className="rounded-md border bg-background p-3">
                <p className="text-caption font-medium text-muted-foreground">
                  {t(($) => $.alignment.round_label, { n: round ?? 1 })}
                </p>
                <p className="mt-1 text-caption text-foreground">
                  {groupPlan.built > 0
                    ? t(($) => $.alignment.round_hint, { count: groupPlan.built })
                    : t(($) => $.alignment.round_hint_empty)}
                </p>
              </div>
            ) : null}

            {/* What the previous rounds already built. Rendered from the
                server's own rows and not from the payload, because the payload
                is what the user edits while these issues are what exists: a row
                deleted below does not delete its issue, and a row renamed below
                does not rename it either. Saying "these, and they will not
                change" is the whole promise of a continuation round. */}
            {isContinuation && (builtChildren?.length ?? 0) > 0 ? (
              <div className="space-y-2 border-t pt-5">
                <div>
                  <h3 className="text-body font-medium">
                    {t(($) => $.alignment.built_title)}
                  </h3>
                  <p className="mt-1 text-caption text-muted-foreground">
                    {t(($) => $.alignment.built_hint)}
                  </p>
                </div>
                <ul className="space-y-1">
                  {(builtChildren ?? []).map((issue) => (
                    <li key={issue.id}>
                      <AppLink
                        href={paths.issueDetail(issue.id)}
                        className="flex min-w-0 items-center gap-2 rounded-md border bg-background px-3 py-2 hover:bg-muted/40"
                      >
                        <span className="shrink-0 text-caption tabular-nums text-muted-foreground">
                          {issue.identifier}
                        </span>
                        <span className="min-w-0 flex-1 truncate text-caption">
                          {issue.title}
                        </span>
                        {issue.stage != null ? (
                          <span className="shrink-0 text-caption text-muted-foreground">
                            {t(($) => $.alignment.built_stage, { n: issue.stage })}
                          </span>
                        ) : null}
                        <ExternalLink
                          className="size-3.5 shrink-0 text-muted-foreground"
                          aria-hidden="true"
                        />
                      </AppLink>
                    </li>
                  ))}
                </ul>
              </div>
            ) : null}

            {children.length > 0 ? (
              <div className="space-y-3 border-t pt-5">
                <div>
                  <h3 className="text-body font-medium">
                    {isContinuation
                      ? t(($) => $.alignment.new_group_title)
                      : t(($) => $.alignment.group_title)}
                  </h3>
                  <p className="mt-1 text-caption text-muted-foreground">
                    {isContinuation
                      ? t(($) => $.alignment.new_group_hint)
                      : t(($) => $.alignment.group_hint)}
                  </p>
                  {/* A sub-issue has no project control of its own, and silently
                      inheriting one is the kind of thing a preview should say
                      rather than leave to be discovered after the confirm. */}
                  <p className="mt-1 text-caption text-muted-foreground">
                    {t(($) => $.alignment.group_project_inherited)}
                  </p>
                </div>
                {newChildren.length === 0 ? (
                  <p className="text-caption text-muted-foreground">
                    {t(($) => $.alignment.new_group_empty)}
                  </p>
                ) : null}
                {newChildren.map(({ child, index }) => {
                  const row = groupPlan.rows[index + 1];
                  const startsNow = row?.startsOnCreate === true;
                  return (
                    <div
                      key={child.key}
                      className="space-y-2 rounded-md border bg-background p-3"
                    >
                      <div className="flex items-center gap-2">
                        <span className="text-caption text-muted-foreground">
                          {t(($) => $.alignment.child_label, { n: index + 1 })}
                        </span>
                        <span
                          className={cn(
                            "ml-auto text-caption",
                            startsNow
                              ? "font-medium text-foreground"
                              : "text-muted-foreground",
                          )}
                        >
                          {startsNow
                            ? t(($) => $.alignment.child_starts_now)
                            : t(($) => $.alignment.child_parked)}
                        </span>
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          className="text-muted-foreground"
                          aria-label={t(($) => $.alignment.child_remove, {
                            n: index + 1,
                          })}
                          disabled={locked}
                          onClick={() => removeChild(index)}
                        >
                          <Trash2 className="size-4" aria-hidden="true" />
                        </Button>
                      </div>
                      <Input
                        aria-label={t(($) => $.alignment.child_title, {
                          n: index + 1,
                        })}
                        value={child.title}
                        disabled={locked}
                        placeholder={t(
                          ($) => $.alignment.field_title_placeholder,
                        )}
                        onChange={(event) =>
                          updateChild(index, { title: event.target.value })
                        }
                      />
                      {/* A record is read back, not edited: the pickers are
                          closed rather than removed so the row still shows what
                          was agreed. Neither picker takes a `disabled` prop,
                          and a controlled `open={false}` is what keeps a
                          finished alignment from offering a choice that can no
                          longer be saved. */}
                      <div
                        className={cn(
                          "flex flex-wrap items-center gap-x-4 gap-y-2",
                          locked && "pointer-events-none opacity-60",
                        )}
                      >
                        <StagePicker
                          stage={child.stage ?? null}
                          maxStage={maxIssueDraftChildStage(children)}
                          open={locked ? false : undefined}
                          align="start"
                          onUpdate={(updates) =>
                            updateChild(index, { stage: updates.stage ?? null })
                          }
                        />
                        <AssigneePicker
                          assigneeType={
                            (child.assignee_type as IssueAssigneeType | null) ??
                            null
                          }
                          assigneeId={child.assignee_id ?? null}
                          open={locked ? false : undefined}
                          align="start"
                          onUpdate={(updates) =>
                            updateChild(index, {
                              assignee_type: updates.assignee_type ?? null,
                              assignee_id: updates.assignee_id ?? null,
                            })
                          }
                        />
                      </div>
                      {child.assignee_hint ? (
                        <p className="text-caption text-muted-foreground">
                          {t(($) => $.alignment.child_hint, {
                            hint: child.assignee_hint,
                          })}
                        </p>
                      ) : null}
                    </div>
                  );
                })}
              </div>
            ) : null}
          </div>
        </div>
      </div>

      <footer className="shrink-0 border-t bg-background px-5 py-3">
        {record ? (
          <AlignmentRecordFooter producedIssueId={producedIssueId ?? null} />
        ) : stage === "created" ? (
          <CreatedGroupFooter
            issues={createdIssues ?? null}
            fallbackIssueId={producedIssueId ?? null}
          />
        ) : (
          <>
            <div className="mb-3 space-y-1">
              {/* What the button below is about to do, in numbers. A confirm
                  can enqueue more than one agent, and the stage rule decides
                  how many: stage 1 runs the moment the group exists, stage 2+
                  is created in Backlog and waits for a person to promote it.
                  Saying so here is the only place the user can learn it before
                  it happens.

                  On a continuation round the numbers are about THIS round: the
                  nodes that already exist are adopted rather than created, so
                  counting them would promise work the confirm does not do. */}
              {isContinuation ? (
                <p className="text-caption text-muted-foreground">
                  {groupPlan.creating > 0
                    ? t(($) => $.alignment.group_increment, {
                        count: groupPlan.creating,
                      })
                    : t(($) => $.alignment.group_increment_none)}
                </p>
              ) : groupPlan.total > 1 ? (
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.alignment.group_summary, {
                    count: groupPlan.total,
                  })}
                </p>
              ) : null}
              {groupPlan.starting > 0 ? (
                <p className="text-caption font-medium text-foreground">
                  {t(($) => $.alignment.group_summary_running, {
                    count: groupPlan.starting,
                  })}
                </p>
              ) : null}
              {groupPlan.parked > 0 ? (
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.alignment.group_summary_parked, {
                    count: groupPlan.parked,
                  })}
                </p>
              ) : null}
              {!isContinuation &&
              groupPlan.total > 1 &&
              groupPlan.rows[0]?.startsOnCreate !== true ? (
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.alignment.group_summary_parent)}
                </p>
              ) : null}
              {isContinuation && groupPlan.built > 0 ? (
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.alignment.group_summary_built, {
                    count: groupPlan.built,
                  })}
                </p>
              ) : null}
              <p className="text-caption text-muted-foreground">
                {t(($) => $.alignment.confirm_hint)}
              </p>
              {/* The confirm sends a revision, not a payload: it creates the
                  draft the SERVER holds, so unsaved edits are not in it. The
                  counts above are read off the screen, which is the only
                  honest thing they can be while someone is editing — so the
                  button has to wait for the two to agree. Without this, a
                  deleted sub-issue is still created and its agent still
                  started, under a line that just promised it would not be. */}
              {confirmNeedsSave ? (
                <p className="text-caption font-medium text-foreground">
                  {t(($) => $.alignment.confirm_unsaved)}
                </p>
              ) : null}
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Button
                onClick={() => void onConfirm()}
                disabled={!canConfirm || confirmNeedsSave}
                className="min-w-32"
              >
                {confirming ? (
                  <Loader2 className="size-4 animate-spin" aria-hidden="true" />
                ) : null}
                {confirming
                  ? t(($) => $.alignment.confirming)
                  : t(($) => $.alignment.confirm)}
              </Button>
              <Button
                variant="outline"
                onClick={handleGenerate}
                // Available while the draft is still editable, `ready`
                // included: a converged draft that the user keeps refining
                // produces new carrier blocks, and refusing to fold them in
                // would leave retyping as the only way to apply them.
                disabled={!canGenerate}
              >
                {t(($) => $.alignment.generate)}
              </Button>
              <Button
                variant="outline"
                onClick={handleSave}
                disabled={!canSave}
              >
                {saving
                  ? t(($) => $.alignment.saving)
                  : saved && !dirty
                    ? t(($) => $.alignment.saved)
                    : t(($) => $.alignment.save)}
              </Button>
              <Button
                variant="ghost"
                className={cn("ml-auto text-muted-foreground")}
                onClick={() => setConfirmingAbandon(true)}
                disabled={locked || abandoning}
              >
                {abandoning
                  ? t(($) => $.alignment.abandoning)
                  : t(($) => $.alignment.abandon)}
              </Button>
            </div>
          </>
        )}
      </footer>

      <AlertDialog open={confirmingAbandon} onOpenChange={setConfirmingAbandon}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.alignment.abandon_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.alignment.abandon_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={abandoning}>
              {t(($) => $.alignment.abandon_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              disabled={abandoning}
              onClick={(event) => {
                // Keep the dialog up until the server has actually accepted;
                // `onAbandon` navigates away on success.
                event.preventDefault();
                void onAbandon();
              }}
            >
              {t(($) => $.alignment.confirm_abandon)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

/**
 * The outcome of a finished alignment: what it became, and the way across to
 * it.
 *
 * A record with an issue shows the link; one without is an alignment that was
 * given up on, and saying so is the honest answer — the alternative is an
 * empty footer that reads as a loading state. (DENE-371)
 */
function AlignmentRecordFooter({
  producedIssueId,
}: {
  producedIssueId: string | null;
}) {
  const { t } = useT("issues");
  const paths = useWorkspacePaths();
  return (
    <div className="space-y-2">
      <p className="text-body font-medium text-foreground">
        {producedIssueId
          ? t(($) => $.alignment.record_created_title)
          : t(($) => $.alignment.record_abandoned_title)}
      </p>
      {producedIssueId ? (
        <AppLink
          href={paths.issueDetail(producedIssueId)}
          className="inline-flex items-center gap-1 text-body text-primary hover:underline"
        >
          <ExternalLink className="size-3.5" aria-hidden="true" />
          {t(($) => $.alignment.record_issue_link)}
        </AppLink>
      ) : (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.alignment.record_abandoned_hint)}
        </p>
      )}
    </div>
  );
}

/**
 * What a confirm just created, as the server reported it.
 *
 * The rows are the confirm's own answer, so a repeat confirm on a draft whose
 * issue already exists (a second tab, a retry) reads back the SAME group rather
 * than degrading to "one issue" — which is the whole reason the endpoint answers
 * with the set instead of the root alone (DENE-411, and
 * `docs/design/issue-draft-group-finalize.md` §5.2).
 *
 * A backend that predates groups reports the root alone, and the row that
 * produces carries no title or identifier: saying "1 issue" is the honest
 * version of that, because that is all the server told us.
 */
function CreatedGroupFooter({
  issues,
  fallbackIssueId,
}: {
  issues: IssueDraftCreatedIssue[] | null;
  fallbackIssueId: string | null;
}) {
  const { t } = useT("issues");
  const paths = useWorkspacePaths();
  const rows =
    issues && issues.length > 0
      ? issues
      : fallbackIssueId
        ? [
            {
              id: fallbackIssueId,
              identifier: "",
              title: "",
              status: "",
              stage: null,
              assignee_type: null,
              assignee_id: null,
              parent_issue_id: null,
            } satisfies IssueDraftCreatedIssue,
          ]
        : [];
  return (
    <div className="space-y-2">
      <p className="text-body font-medium text-foreground">
        {t(($) => $.alignment.created_title)}
      </p>
      {rows.length > 1 ? (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.alignment.created_group_title, { count: rows.length })}
        </p>
      ) : null}
      <ul className="space-y-1">
        {rows.map((issue) => (
          <li key={issue.id}>
            <AppLink
              href={paths.issueDetail(issue.id)}
              className="inline-flex items-center gap-1 text-body text-primary hover:underline"
            >
              <ExternalLink className="size-3.5" aria-hidden="true" />
              {issue.identifier || issue.title || t(($) => $.alignment.record_issue_link)}
            </AppLink>
          </li>
        ))}
      </ul>
    </div>
  );
}

/**
 * One row of the carried-files list: a thumbnail when the file is an image, a
 * file icon when it is not, and the name either way.
 *
 * The row opens the file itself, at the same URL a description embeds as a
 * markdown link — so what the panel shows and what the issue will point at can
 * never disagree. Everything an older backend may omit is defaulted rather than
 * assumed: `markdown_url` / `download_url` are additive fields, `content_type`
 * is only leniently parsed (`AttachmentSchema` validates the id alone), and a
 * row with no URL at all renders as the same row, unlinked, instead of an
 * anchor pointing back at this page.
 */
function CarriedFileRow({ attachment }: { attachment: Attachment }) {
  const filename = attachment.filename || attachment.id;
  const href =
    attachment.markdown_url || attachment.download_url || attachment.url || "";
  const thumbnail = (attachment.content_type ?? "").startsWith("image/")
    ? href
    : "";
  const body = (
    <>
      {thumbnail ? (
        <img
          src={thumbnail}
          alt=""
          className="size-8 shrink-0 rounded-sm border object-cover"
        />
      ) : (
        <FileText
          className="size-4 shrink-0 text-muted-foreground"
          aria-hidden="true"
        />
      )}
      <span className="min-w-0 flex-1 truncate text-caption">{filename}</span>
      {href ? (
        <ExternalLink
          className="size-3.5 shrink-0 text-muted-foreground"
          aria-hidden="true"
        />
      ) : null}
    </>
  );
  const className = cn(
    "flex min-w-0 items-center gap-2 rounded-md border bg-background px-3 py-2",
    href && "hover:bg-muted/40",
  );
  if (!href) return <div className={className}>{body}</div>;
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer"
      className={className}
      title={filename}
    >
      {body}
    </a>
  );
}

function Field({
  label,
  htmlFor,
  children,
}: {
  label: string;
  htmlFor: string;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-2">
      <label htmlFor={htmlFor} className="text-caption text-muted-foreground">
        {label}
      </label>
      {children}
    </div>
  );
}

const EMPTY_DRAFT: IssueDraftPayload = {
  title: "",
  description: "",
  status: "",
  priority: "",
};

/** A caller that does not pass attachments has an alignment that produced no
 *  files, which is the state the section renders as nothing at all. */
const EMPTY_ATTACHMENTS: readonly Attachment[] = [];

/** A first round has nothing adopted, and the empty set is the honest default
 *  for a caller that does not know about continuation rounds at all. */
const EMPTY_BUILT_KEYS: ReadonlySet<string> = new Set<string>();

function sameDraft(a: IssueDraftPayload, b: IssueDraftPayload): boolean {
  return (
    a.title === b.title &&
    a.description === b.description &&
    a.status === b.status &&
    a.priority === b.priority &&
    // The project is edited here like any other field. Without it the panel
    // would report itself clean after a project change, and the next carrier
    // reply would be adopted straight over the edit — silently reverting a
    // choice the user just made.
    (a.project_id ?? null) === (b.project_id ?? null) &&
    // Same reason for the root's assignee: it is panel-owned, so a change here
    // must count as dirty or the carrier's next reply reverts it.
    (a.assignee_type ?? null) === (b.assignee_type ?? null) &&
    (a.assignee_id ?? null) === (b.assignee_id ?? null) &&
    // Editing the group is an edit to the draft like any other: without this
    // the panel would report itself clean, the session would fold the carrier's
    // next reply straight over the row someone just deleted, and "save" would
    // write nothing.
    sameIssueDraftChildren(a.children ?? [], b.children ?? [])
  );
}
