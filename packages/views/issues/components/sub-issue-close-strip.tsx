"use client";

import { cn } from "@multica/ui/lib/utils";
import type { Issue } from "@multica/core/types";
import { useActorName } from "@multica/core/workspace/hooks";
import {
  closeProtocolIsStuck,
  closeProtocolNextOwnerId,
  closeProtocolWaitingOn,
  readCloseProtocol,
} from "@multica/core/issues";
import { useT, useTimeAgo } from "../../i18n";

/**
 * Per-sub-issue close-protocol strip (DENE-234 / Stage 5).
 *
 * Renders the five Stage 5 fields next to `groupSubIssuesByStage`: current
 * stage, close.conclusion, next owner, waiting_on, last_activity_at. Two
 * exception states are never silent: missing close.* keys, and close.status
 * drifting from issue.status.
 */
export function SubIssueCloseStrip({
  issue,
  parentStatus = "in_progress",
  hasStagedSibling = issue.stage != null,
}: {
  issue: Issue;
  parentStatus?: Issue["status"];
  hasStagedSibling?: boolean;
}) {
  const { t } = useT("issues");
  const timeAgo = useTimeAgo();
  const { getActorName } = useActorName();
  const close = readCloseProtocol(issue.metadata, issue.status);
  const stuck = closeProtocolIsStuck(issue.status, close.conclusion);
  const waitingOn = closeProtocolWaitingOn(close.waitingOn);
  const nextOwnerId = closeProtocolNextOwnerId(
    close.nextOwnerType,
    close.nextOwnerId,
  );

  const needsCloseRecord =
    (issue.status === "done" || issue.status === "cancelled") &&
    issue.parent_issue_id != null &&
    hasStagedSibling &&
    (parentStatus === "in_progress" || parentStatus === "in_review" || parentStatus === "blocked");
  const state = !close.complete && needsCloseRecord
    ? "missing"
    : close.statusDrift
      ? "drift"
      : "ok";

  const nextOwnerLabel = (() => {
    if (close.nextOwnerType === null) return null;
    if (close.nextOwnerType === "none" || nextOwnerId === null) {
      return t(($) => $.close_protocol.next_owner_none);
    }
    const name = getActorName(close.nextOwnerType, nextOwnerId);
    return t(($) => $.close_protocol.next_owner, { name });
  })();

  const lastActivity = issue.last_activity_at
    ? timeAgo(issue.last_activity_at)
    : t(($) => $.close_protocol.last_activity_missing);

  return (
    <div
      data-testid="sub-issue-close-strip"
      data-close-state={state}
      data-stuck={stuck ? "true" : "false"}
      className={cn(
        "flex min-w-0 flex-wrap items-center gap-1 pl-11",
        state === "missing" && "text-muted-foreground",
        state === "drift" && "text-warning",
      )}
    >
      <Chip
        kind="stage"
        tone={state === "ok" ? "muted" : state}
      >
        {issue.stage == null
          ? t(($) => $.stage.none)
          : t(($) => $.stage.value, { n: issue.stage })}
      </Chip>
      {state === "missing" && (
        <Chip kind="close.missing" tone="muted">
          {t(($) => $.close_protocol.missing)}
        </Chip>
      )}
      {close.statusDrift && (
        <Chip kind="close.status" tone="drift">
          {t(($) => $.close_protocol.status_drift, {
            issueStatus: issue.status,
          })}
        </Chip>
      )}
      {close.conclusion !== null && (
        <Chip kind="close.conclusion" tone={stuck && state === "ok" ? "stuck" : "muted"}>
          {close.conclusion}
        </Chip>
      )}
      {nextOwnerLabel !== null && (
        <Chip
          kind="close.next_owner"
          tone={stuck && state === "ok" ? "stuck" : "muted"}
        >
          {nextOwnerLabel}
        </Chip>
      )}
      {waitingOn !== null && (
        <Chip kind="close.waiting_on" tone={stuck && state === "ok" ? "stuck" : "muted"}>
          {t(($) => $.close_protocol.waiting_on, { id: waitingOn })}
        </Chip>
      )}
      <Chip kind="last_activity_at" tone="muted">
        {lastActivity}
      </Chip>
    </div>
  );
}

function Chip({
  kind,
  tone,
  children,
}: {
  // A technical identifier for the chip, not user-visible text: it labels the
  // chip for tests and DOM inspection. It used to be rendered as `title`,
  // which turned untranslated keys like "close.waiting_on" into a tooltip a
  // screen reader would read out.
  kind: string;
  tone: "muted" | "missing" | "drift" | "stuck";
  children: string;
}) {
  return (
    <span
      data-chip={kind}
      className={cn(
        "inline-flex max-w-full items-center truncate rounded-full px-1.5 py-0.5 text-micro",
        tone === "muted" && "bg-muted/60 text-muted-foreground",
        tone === "missing" && "bg-destructive/10 font-medium text-destructive",
        tone === "drift" && "bg-warning/10 font-medium text-warning",
        tone === "stuck" && "bg-muted font-medium text-foreground",
      )}
    >
      {children}
    </span>
  );
}
