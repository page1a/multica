import type {
  InfiniteData,
  QueryClient,
  QueryKey,
} from "@tanstack/react-query";
import {
  agentActivityKeys,
  agentRunCountsKeys,
  agentTaskSnapshotKeys,
  agentTasksKeys,
} from "../agents/queries";
import { labelKeys } from "../labels/queries";
import type {
  Issue,
  ListIssuesCache,
  ListIssuesResponse,
} from "../types";
import { findIssueLocation, removeIssueFromBuckets } from "./cache-helpers";
import { issueKeys } from "./queries";
import { useRecentIssuesStore } from "./stores/recent-issues-store";

export type DeletedIssueCacheMetadata = {
  parentIssueIds: string[];
};

function collectParentId(
  parentIssueIds: Set<string>,
  parentId: string | null | undefined,
) {
  if (parentId) parentIssueIds.add(parentId);
}

function collectParentFromListCache(
  parentIssueIds: Set<string>,
  data: ListIssuesCache | undefined,
  issueId: string,
) {
  const parentId = data
    ? findIssueLocation(data, issueId)?.issue.parent_issue_id
    : undefined;
  collectParentId(parentIssueIds, parentId);
}

function parentIdFromChildrenKey(key: QueryKey) {
  const parentId = key[key.length - 1];
  return typeof parentId === "string" ? parentId : null;
}

export function collectDeletedIssueCacheMetadata(
  qc: QueryClient,
  wsId: string,
  issueId: string,
): DeletedIssueCacheMetadata {
  const parentIssueIds = new Set<string>();

  const detail = qc.getQueryData<Issue>(issueKeys.detail(wsId, issueId));
  collectParentId(parentIssueIds, detail?.parent_issue_id);

  for (const [, data] of qc.getQueriesData<ListIssuesCache>({
    queryKey: issueKeys.list(wsId),
  })) {
    collectParentFromListCache(parentIssueIds, data, issueId);
  }

  for (const [, data] of qc.getQueriesData<
    InfiniteData<ListIssuesResponse, number>
  >({ queryKey: issueKeys.flatAll(wsId) })) {
    for (const page of data?.pages ?? []) {
      collectParentId(
        parentIssueIds,
        page.issues.find((issue) => issue.id === issueId)?.parent_issue_id,
      );
    }
  }

  for (const [, data] of qc.getQueriesData<ListIssuesCache>({
    queryKey: issueKeys.myAll(wsId),
  })) {
    collectParentFromListCache(parentIssueIds, data, issueId);
  }

  for (const [key, data] of qc.getQueriesData<Issue[]>({
    queryKey: [...issueKeys.all(wsId), "children"],
  })) {
    const child = data?.find((issue) => issue.id === issueId);
    if (!child) continue;
    collectParentId(parentIssueIds, child.parent_issue_id);
    collectParentId(parentIssueIds, parentIdFromChildrenKey(key));
  }

  return { parentIssueIds: Array.from(parentIssueIds) };
}

export function pruneDeletedIssueFromListCaches(
  qc: QueryClient,
  wsId: string,
  issueId: string,
) {
  for (const [key] of qc.getQueriesData<ListIssuesCache>({
    queryKey: issueKeys.list(wsId),
  })) {
    qc.setQueryData<ListIssuesCache>(key, (old) =>
      old ? removeIssueFromBuckets(old, issueId) : old,
    );
  }

  for (const [key] of qc.getQueriesData<ListIssuesCache>({
    queryKey: issueKeys.myAll(wsId),
  })) {
    qc.setQueryData<ListIssuesCache>(key, (old) =>
      old ? removeIssueFromBuckets(old, issueId) : old,
    );
  }

  for (const [key, data] of qc.getQueriesData<
    InfiniteData<ListIssuesResponse, number>
  >({ queryKey: issueKeys.flatAll(wsId) })) {
    if (!data?.pages) continue;
    const found = data.pages.some((page) =>
      page.issues.some((issue) => issue.id === issueId),
    );
    if (!found) continue;
    qc.setQueryData<InfiniteData<ListIssuesResponse, number>>(key, {
      ...data,
      pages: data.pages.map((page) => ({
        ...page,
        total: Math.max(0, page.total - 1),
        issues: page.issues.filter((issue) => issue.id !== issueId),
      })),
    });
  }
}

export function pruneDeletedIssueFromParentChildrenCaches(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  metadata: DeletedIssueCacheMetadata,
) {
  for (const parentId of metadata.parentIssueIds) {
    qc.setQueryData<Issue[]>(issueKeys.children(wsId, parentId), (old) =>
      old?.filter((issue) => issue.id !== issueId),
    );
  }
}

export function invalidateDeletedIssueParentCaches(
  qc: QueryClient,
  wsId: string,
  metadata: DeletedIssueCacheMetadata,
) {
  if (metadata.parentIssueIds.length === 0) return;
  for (const parentId of metadata.parentIssueIds) {
    qc.invalidateQueries({ queryKey: issueKeys.children(wsId, parentId) });
  }
  qc.invalidateQueries({ queryKey: issueKeys.childProgress(wsId) });
  qc.invalidateQueries({ queryKey: issueKeys.childrenByParentsAll(wsId) });
}

export function invalidateDeletedIssueDependentCaches(
  qc: QueryClient,
  wsId: string,
) {
  qc.invalidateQueries({ queryKey: agentTaskSnapshotKeys.list(wsId) });
  qc.invalidateQueries({ queryKey: agentActivityKeys.last30d(wsId) });
  qc.invalidateQueries({ queryKey: agentRunCountsKeys.last30d(wsId) });
  qc.invalidateQueries({ queryKey: agentTasksKeys.all(wsId) });
}

export function invalidateIssueScopedCaches(
  qc: QueryClient,
  wsId: string,
  issueId: string,
) {
  qc.invalidateQueries({ queryKey: issueKeys.timeline(issueId) });
  qc.invalidateQueries({ queryKey: issueKeys.reactions(issueId) });
  qc.invalidateQueries({ queryKey: issueKeys.subscribers(issueId) });
  qc.invalidateQueries({ queryKey: issueKeys.usage(issueId) });
  qc.invalidateQueries({ queryKey: issueKeys.attachments(issueId) });
  qc.invalidateQueries({ queryKey: issueKeys.tasks(issueId) });
  qc.invalidateQueries({ queryKey: issueKeys.children(wsId, issueId) });
  qc.invalidateQueries({ queryKey: labelKeys.byIssue(wsId, issueId) });
}

export function cleanupDeletedIssueCaches(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  metadata = collectDeletedIssueCacheMetadata(qc, wsId, issueId),
) {
  evictIssueQueries(qc, wsId, issueId, metadata);
  invalidateDeletedIssueDependentCaches(qc, wsId);

  // Recent Issues store persists to localStorage and survives reloads, so a
  // deleted id left behind keeps the Cmd+K command bar firing 404s on every
  // open. Both the delete mutation and the WS delete event flow through here,
  // so a single call covers self-delete and cross-client delete.
  useRecentIssuesStore.getState().forgetIssue(wsId, issueId);
}

/**
 * Drop one issue from every cache a viewer reads, then refetch the lists.
 *
 * This is what an `issue:invalidated` frame asks for: the issue's sharing
 * scope moved and this client may or may not still be allowed to see it. The
 * row itself still exists, so — unlike a deletion — nothing here is permanent:
 * the refetch returns the issue to viewers who kept access and omits it for
 * viewers who lost it. The recent-issues store is deliberately left alone
 * (the id is still valid) and inbox rows are left to the server-filtered
 * refetch, which is the only thing that knows whether a row should survive.
 */
export function evictIssueAfterVisibilityChange(
  qc: QueryClient,
  wsId: string,
  issueId: string,
) {
  evictIssueQueries(qc, wsId, issueId, collectDeletedIssueCacheMetadata(qc, wsId, issueId));
}

function evictIssueQueries(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  metadata: DeletedIssueCacheMetadata,
) {
  pruneDeletedIssueFromListCaches(qc, wsId, issueId);
  pruneDeletedIssueFromParentChildrenCaches(qc, wsId, issueId, metadata);
  invalidateDeletedIssueParentCaches(qc, wsId, metadata);

  qc.removeQueries({ queryKey: issueKeys.detail(wsId, issueId) });
  qc.removeQueries({ queryKey: issueKeys.timeline(issueId) });
  qc.removeQueries({ queryKey: issueKeys.reactions(issueId) });
  qc.removeQueries({ queryKey: issueKeys.subscribers(issueId) });
  qc.removeQueries({ queryKey: issueKeys.usage(issueId) });
  qc.removeQueries({ queryKey: issueKeys.attachments(issueId) });
  qc.removeQueries({ queryKey: issueKeys.tasks(issueId) });
  qc.removeQueries({ queryKey: issueKeys.children(wsId, issueId) });
  qc.removeQueries({ queryKey: labelKeys.byIssue(wsId, issueId) });

  qc.invalidateQueries({ queryKey: issueKeys.childProgress(wsId) });
  qc.invalidateQueries({ queryKey: issueKeys.list(wsId) });
  qc.invalidateQueries({ queryKey: issueKeys.myAll(wsId) });
  qc.invalidateQueries({ queryKey: issueKeys.flatAll(wsId) });
  qc.invalidateQueries({ queryKey: issueKeys.tableAll(wsId) });
  // Project Gantt cache lives outside `myAll`, so it needs an explicit
  // refresh when an issue leaves a viewer's window — the row may have been a
  // scheduled bar visible right now.
  qc.invalidateQueries({ queryKey: issueKeys.projectGanttAll(wsId) });
}
