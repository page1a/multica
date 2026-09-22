"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, ChevronDown, ChevronRight } from "lucide-react";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { childIssuesOptions } from "@multica/core/issues/queries";
import { issueStatusCategory } from "@multica/core/issues";
import type { Issue } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { ActorAvatar } from "../../common/actor-avatar";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { StatusIcon } from "./status-icon";
import { groupSubIssuesByStage } from "../utils/sub-issue-stages";
import {
  parentPipelineState,
  type ParentChildRollup,
} from "../utils/parent-rollup";

/**
 * The card's own disclosure control. Lives inside the card body (which is
 * wrapped in the issue link), so it swallows the click the way the inline
 * pickers beside it do — expanding a parent must not navigate away from the
 * board, which is the entire point of expanding it in place. (DENE-444)
 */
export function BoardCardSubIssueToggle({
  expanded,
  rollup,
  onToggle,
}: {
  expanded: boolean;
  rollup: ParentChildRollup;
  onToggle: () => void;
}) {
  const { t } = useT("issues");
  const pipeline = parentPipelineState(rollup);
  const Chevron = expanded ? ChevronDown : ChevronRight;
  // One label for both states: the count is what the reader wants, and
  // `aria-expanded` — not a second wording — is how the state is announced.
  const label = t(($) => $.sub_issue_accordion.count, { count: rollup.total });

  return (
    <div
      className="mt-2 flex items-center gap-1.5"
      onClick={(e) => {
        e.stopPropagation();
        e.preventDefault();
      }}
      onMouseDown={(e) => e.stopPropagation()}
      onPointerDown={(e) => e.stopPropagation()}
    >
      <button
        type="button"
        aria-expanded={expanded}
        onClick={onToggle}
        // Active/expanded stays legible under hover: the weight change is on a
        // dimension hover never touches.
        className={cn(
          "inline-flex items-center gap-1 rounded-sm px-1 py-0.5 text-micro text-muted-foreground hover:bg-muted/60 hover:text-foreground",
          expanded && "font-medium text-foreground",
        )}
      >
        <Chevron className="size-3 shrink-0" aria-hidden />
        <span>{label}</span>
      </button>
      <PipelineChip pipeline={pipeline} rollup={rollup} />
    </div>
  );
}

/**
 * The bubbled-up state of a parent's sub-issue pipeline. `blocked` is the one
 * that has to survive a glance across a full column, so it takes the
 * destructive token; `stalled` is quieter but still worth a word, because a
 * queue nobody picked up looks identical to healthy work in a progress ring.
 */
function PipelineChip({
  pipeline,
  rollup,
}: {
  pipeline: ReturnType<typeof parentPipelineState>;
  rollup: ParentChildRollup;
}) {
  const { t } = useT("issues");
  if (pipeline === "settled") return null;
  if (pipeline === "blocked") {
    return (
      <span
        data-testid="board-card-pipeline-blocked"
        className="inline-flex min-w-0 items-center gap-1 rounded-full bg-destructive/10 px-1.5 py-0.5 text-micro font-medium text-destructive"
      >
        <AlertTriangle className="size-3 shrink-0" aria-hidden />
        <span className="truncate">
          {t(($) => $.sub_issue_accordion.blocked, { count: rollup.blocked })}
        </span>
      </span>
    );
  }
  if (pipeline === "active") {
    return (
      <span
        data-testid="board-card-pipeline-active"
        className="inline-flex min-w-0 items-center gap-1 rounded-full bg-muted/60 px-1.5 py-0.5 text-micro text-muted-foreground"
      >
        <span className="truncate">
          {t(($) => $.sub_issue_accordion.active, { count: rollup.active })}
        </span>
      </span>
    );
  }
  return (
    <span
      data-testid="board-card-pipeline-stalled"
      className="inline-flex min-w-0 items-center gap-1 rounded-full bg-muted/60 px-1.5 py-0.5 text-micro text-muted-foreground"
    >
      <span className="truncate">
        {t(($) => $.sub_issue_accordion.stalled, {
          count: rollup.total - rollup.done,
        })}
      </span>
    </span>
  );
}

/**
 * The expanded sub-issue list, rendered in place inside the parent card.
 *
 * Mounted only while expanded, so the children request is paid per parent the
 * user actually opened rather than once per card on the board.
 */
export function BoardCardSubIssues({ parentId }: { parentId: string }) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const { data, isPending, isError } = useQuery(childIssuesOptions(wsId, parentId));
  const groups = useMemo(() => groupSubIssuesByStage(data ?? []), [data]);

  return (
    <div
      data-testid="board-card-sub-issues"
      className="mt-1.5 border-t border-surface-border pt-1.5"
      onClick={(e) => {
        e.stopPropagation();
        e.preventDefault();
      }}
      onMouseDown={(e) => e.stopPropagation()}
      onPointerDown={(e) => e.stopPropagation()}
    >
      {isPending && (
        <div className="space-y-1 py-0.5">
          <Skeleton className="h-4 w-full" />
          <Skeleton className="h-4 w-3/4" />
        </div>
      )}
      {!isPending && isError && (
        <p className="py-0.5 text-micro text-muted-foreground">
          {t(($) => $.sub_issue_accordion.load_failed)}
        </p>
      )}
      {!isPending && !isError && groups.length === 0 && (
        <p className="py-0.5 text-micro text-muted-foreground">
          {t(($) => $.sub_issue_accordion.empty)}
        </p>
      )}
      {groups.map((group) => (
        <div key={group.stage ?? "unstaged"}>
          {/* A stage header only earns its line when the set is actually
              staged — an unstaged group is the whole list. */}
          {group.stage !== null && (
            <p className="px-1 pt-1 text-micro text-faint-foreground">
              {t(($) => $.sub_issue_accordion.stage, { stage: group.stage })}
            </p>
          )}
          <ul className="space-y-px">
            {group.items.map((child) => (
              <SubIssueRow key={child.id} issue={child} />
            ))}
          </ul>
        </div>
      ))}
    </div>
  );
}

function SubIssueRow({ issue }: { issue: Issue }) {
  const paths = useWorkspacePaths();
  const router = useNavigation();
  const category = issueStatusCategory(issue);
  // `blocked` stayed a STATUS key when MUL-7365 split the four lifecycle
  // categories out of the seven built-ins, so it is a key comparison now.
  const blocked = issue.status === "blocked";
  const terminal = category === "done" || category === "closed";

  return (
    <li>
      <button
        type="button"
        onClick={() => router.push(paths.issueDetail(issue.id))}
        className="flex w-full min-w-0 items-center gap-1.5 rounded-sm px-1 py-0.5 text-left hover:bg-muted/60"
      >
        <StatusIcon
          status={issue.status}
          category={category ?? undefined}
          className="size-3 shrink-0"
        />
        <span
          className={cn(
            "min-w-0 flex-1 truncate text-micro",
            blocked && "text-destructive",
            terminal && "text-faint-foreground line-through",
            !blocked && !terminal && "text-muted-foreground",
          )}
        >
          {issue.title}
        </span>
        {issue.assignee_type && issue.assignee_id && (
          <ActorAvatar
            actorType={issue.assignee_type}
            actorId={issue.assignee_id}
            size="sm"
            profileLink={false}
            className="shrink-0"
          />
        )}
      </button>
    </li>
  );
}
