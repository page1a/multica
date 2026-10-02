"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { agentTaskSnapshotOptions } from "@multica/core/agents";
import { useWorkspaceId } from "@multica/core/hooks";
import type { AgentTask } from "@multica/core/types";

export interface IssueActivityState {
  isWorking: boolean;
  isQueued: boolean;
  runningTasks: AgentTask[];
  queuedTasks: AgentTask[];
}

export interface IssueSurfaceActivity {
  activityByIssueId: Map<string, IssueActivityState>;
  runningIssueIds: Set<string>;
}

function isQueuedTaskStatus(status: AgentTask["status"]) {
  return (
    status === "queued" ||
    status === "dispatched" ||
    status === "waiting_local_directory" ||
    status === "deferred"
  );
}

export interface IssueTaskGroups {
  running: AgentTask[];
  queued: AgentTask[];
}

/**
 * Per-issue slice of the workspace-wide agent task snapshot. Used as the
 * `select` for a row-level `useQuery(agentTaskSnapshotOptions)`: every row
 * still observes the one shared snapshot query, but React Query's structural
 * sharing keeps this returned object referentially stable when *this* issue's
 * tasks are unchanged, so a snapshot invalidation only re-renders the rows
 * whose own tasks actually moved — not the whole list. Terminal statuses are
 * dropped (they belong on issue history, not the live indicator).
 */
export function selectIssueTasks(
  snapshot: readonly AgentTask[],
  issueId: string,
): IssueTaskGroups {
  const running: AgentTask[] = [];
  const queued: AgentTask[] = [];
  for (const task of snapshot) {
    if (task.issue_id !== issueId) continue;
    if (task.status === "running") running.push(task);
    else if (isQueuedTaskStatus(task.status)) queued.push(task);
  }
  return { running, queued };
}

/**
 * Live state shown in front of an issue's progress line (DENE-1037): an agent
 * running beats one queued; with neither, a failed latest run counts when it
 * ended after `since` (the progress line's time) — a newer report means the
 * failure was already dealt with. Returns primitives so the row-level
 * `select` stays referentially stable.
 */
export type IssueLiveState = "running" | "queued" | "failed" | null;

export function selectIssueLiveState(
  snapshot: readonly AgentTask[],
  issueId: string,
  since?: string | null,
): IssueLiveState {
  let queued = false;
  let latestTerminal: AgentTask | null = null;
  let latestAt = 0;
  for (const task of snapshot) {
    if (task.issue_id !== issueId) continue;
    if (task.status === "running") return "running";
    if (isQueuedTaskStatus(task.status)) queued = true;
    else if (task.completed_at) {
      const at = Date.parse(task.completed_at);
      if (at > latestAt) {
        latestAt = at;
        latestTerminal = task;
      }
    }
  }
  if (queued) return "queued";
  if (latestTerminal?.status !== "failed") return null;
  if (since && latestAt <= Date.parse(since)) return null;
  return "failed";
}

export function deriveIssueSurfaceActivity(
  tasks: readonly AgentTask[],
): IssueSurfaceActivity {
  const activityByIssueId = new Map<string, IssueActivityState>();

  for (const task of tasks) {
    if (!task.issue_id) continue;
    if (task.status !== "running" && !isQueuedTaskStatus(task.status)) {
      continue;
    }

    const current =
      activityByIssueId.get(task.issue_id) ?? {
        isWorking: false,
        isQueued: false,
        runningTasks: [],
        queuedTasks: [],
      };

    if (task.status === "running") {
      current.runningTasks.push(task);
      current.isWorking = true;
    } else {
      current.queuedTasks.push(task);
      current.isQueued = true;
    }

    activityByIssueId.set(task.issue_id, current);
  }

  const runningIssueIds = new Set<string>();
  for (const [issueId, activity] of activityByIssueId) {
    if (activity.isWorking) runningIssueIds.add(issueId);
  }

  return { activityByIssueId, runningIssueIds };
}

export function useIssueSurfaceActivity(): IssueSurfaceActivity {
  const wsId = useWorkspaceId();
  const { data: snapshot = [] } = useQuery(agentTaskSnapshotOptions(wsId));
  return useMemo(() => deriveIssueSurfaceActivity(snapshot), [snapshot]);
}
