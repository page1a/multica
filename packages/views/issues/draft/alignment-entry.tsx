"use client";

import { useState } from "react";
import { MessageSquare, RefreshCw, Sparkles } from "lucide-react";
import { useWorkspaceId } from "@multica/core/hooks";
import { planIssueDraftGroupProgress, useReopenIssueDraft } from "@multica/core/issue-drafts";
import { issueBehavesAs } from "@multica/core/issues";
import { openAlignIssue } from "@multica/core/issues/stores";
import { useWorkspacePaths } from "@multica/core/paths";
import type { Issue } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { AppLink, useNavigation } from "../../navigation";
import { useT } from "../../i18n";

/**
 * The alignment entry on an issue detail page: where this group came from, how
 * far it has got, and the way back into the SAME conversation (DENE-415) — or,
 * for an issue that has no alignment behind it, the way to start one ON this
 * issue (DENE-452).
 *
 * It sits beside the "sub-issue of" line because it answers the same kind of
 * question — where does this come from — and because an alignment conversation
 * is reachable from nowhere else: its carrier is a hidden system agent, so it
 * appears in no chat list. DENE-371 put the read-only "view the alignment" link
 * here; this adds the two things a person with a changed requirement needs: the
 * group's progress, and one action that returns them to the conversation that
 * produced it rather than to the issue form.
 *
 * The two halves are mutually exclusive ON PURPOSE. An issue that is already an
 * alignment's product must not offer "start another alignment" beside "continue
 * this one": both would read as the way to say the next thing, and the second
 * one would found a second conversation about the same issue instead of
 * continuing the one that is already there. So a group that came out of an
 * alignment shows the way back and nothing else. An issue with no alignment
 * behind it — a hand-written ticket, or a comment thread that got away from
 * everyone — shows the action that starts one, filed beneath this issue.
 *
 * The reopen is a REOPEN, not a new alignment: the round continues on the same
 * chat session and the same draft row, so the next confirm appends to this
 * group instead of founding another one. The server treats that as idempotent —
 * a draft that is still open is answered as it stands — so double-clicking
 * costs a request, not a second round.
 */
export function IssueAlignmentEntry({
  draftId,
  groupIssues,
  parentIssueId,
  projectId = null,
  selfStarted,
  startedByAnother,
}: {
  /** The alignment conversation this group came out of, when there is one. */
  draftId: string | null;
  /**
   * The group as it stands: the root plus its children, whichever member of it
   * the page is looking at. The progress line is about the whole group, not
   * about the issue that happens to be open.
   */
  groupIssues: Issue[];
  /** The issue a new alignment is filed under — the one on screen. */
  parentIssueId: string;
  /** The parent issue's project, shown as the group's starting project. */
  projectId?: string | null;
  /**
   * This page holds a conversation of the user's own that is still open, or
   * that can be reopened. Drives the primary action, and is what keeps the
   * start action off the page entirely.
   */
  selfStarted: boolean;
  /**
   * The conversation behind this group belongs to somebody else.
   *
   * It is private — the draft list is creator-scoped — so there is nothing to
   * offer and, worse, a second alignment started here would be a second
   * conversation about work that already has one. The line says so instead.
   */
  startedByAnother: boolean;
}) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const reopen = useReopenIssueDraft(wsId);
  const [failed, setFailed] = useState(false);

  const progress = planIssueDraftGroupProgress(groupIssues, (issue) =>
    issueBehavesAs(issue, "done"),
  );

  const continueAligning = () => {
    if (!draftId) return;
    setFailed(false);
    void reopen
      .mutateAsync(draftId)
      .then(() => navigation.push(paths.newIssueDraft(draftId)))
      .catch(() => {
        // Only reachable when the conversation is gone — a node that outlived
        // its alignment, or a link from another workspace. Saying so beats a
        // navigation to a page that can only redirect back.
        setFailed(true);
      });
  };

  const startAligning = () => {
    // The parent rides the modal payload, which the create-issue shell hands to
    // the alignment face and that face writes into the draft: the conversation
    // is filed beneath this issue, so its confirm creates sub-issues here
    // rather than a second top-level issue. The identifier is not carried — the
    // alignment face renders no parent chip, and this page is already showing
    // the issue it is named after.
    openAlignIssue({
      parent_issue_id: parentIssueId,
      project_id: projectId,
    });
  };

  return (
    <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1.5">
      {draftId ? (
        <AppLink
          href={paths.newIssueDraft(draftId)}
          className="inline-flex max-w-full items-center gap-1.5 text-caption text-muted-foreground hover:text-foreground transition-colors"
        >
          <MessageSquare className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          <span className="truncate">{t(($) => $.detail.alignment_origin)}</span>
        </AppLink>
      ) : null}

      {/* What the group is doing, in the same shape the group's own surfaces
          use: how many issues, how many finished, and which stage is being
          worked on. An alignment that produced one issue has no group to
          report on, and "1 issue · 0 done" beside the link is noise, so the
          line is about groups of more than one. */}
      {progress.total > 1 ? (
        <span className="text-caption text-muted-foreground">
          {t(($) => $.detail.alignment_group_progress, {
            count: progress.total,
            done: progress.done,
          })}
          {progress.stages > 0
            ? ` · ${t(($) => $.detail.alignment_group_stage, {
                stage: progress.activeStage ?? progress.stages,
                total: progress.stages,
              })}`
            : ""}
        </span>
      ) : null}

      {selfStarted && draftId ? (
        <Button
          variant="outline"
          size="sm"
          className="h-6 gap-1 px-2 text-caption"
          disabled={reopen.isPending}
          onClick={continueAligning}
        >
          <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
          {reopen.isPending
            ? t(($) => $.detail.alignment_continue_pending)
            : t(($) => $.detail.alignment_continue)}
        </Button>
      ) : null}

      {startedByAnother ? (
        <span className="text-caption text-muted-foreground">
          {t(($) => $.detail.alignment_started_by_another)}
        </span>
      ) : !selfStarted ? (
        <Button
          variant="outline"
          size="sm"
          className="h-6 gap-1 px-2 text-caption"
          onClick={startAligning}
        >
          <Sparkles className="h-3.5 w-3.5" aria-hidden="true" />
          {t(($) => $.detail.alignment_start)}
        </Button>
      ) : null}

      {failed ? (
        <span role="alert" className="text-caption text-destructive">
          {t(($) => $.detail.alignment_continue_failed)}
        </span>
      ) : null}
    </div>
  );
}
