import type { Issue } from "../types";
import { closeProtocolWaitingOn, readCloseProtocol, type CloseProtocolView } from "./close-protocol";

export type BlockerState = "ROOT" | "PROPAGATED" | "CLEAR";

export type BlockerIssueRef = Pick<Issue, "id" | "identifier">;

/** Kinds this module derives instead of reading them off `close.block_kind`. */
export type DerivedBlockerKind = "review_overdue" | "wake_missed" | "cycle";

/**
 * Why an issue is its own blocker. `kind` is either a derived kind or the
 * free-form `close.block_kind` string (`decision`, `permission`, `external`,
 * `dependency`, `capacity`, or `blocked` for a recorded block without one), so
 * consumers must keep a default branch. Everything the attribution table
 * (`docs/kun/blocker-attribution-design.md` §3.4) needs to render a next action
 * is resolved here, once — views never re-derive the review threshold.
 */
export interface BlockerAttribution {
  kind: string;
  /** `close.block_action` verbatim — the one-line action the closer recorded. */
  action: string | null;
  /** §3.3 "needs you": a human owner, or a decision/permission kind. */
  needsUserAction: boolean;
  nextOwnerType: string | null;
  nextOwnerId: string | null;
  /** `close.waiting_on`, for `dependency` and `wake_missed`. */
  waitingOn: string | null;
  /** `close.at` — the §3.3 "blocked longest first" tiebreak. */
  at: string | null;
}

export interface BlockerTreeNode {
  issue: BlockerIssueRef;
  state: BlockerState;
  rootCauses: BlockerIssueRef[];
  frontierStage: number | null;
  sideBlockers: BlockerIssueRef[];
  userActionCount: number;
  /** True when this node is a derived review/wake/cycle blocker. */
  derived: boolean;
  cycle: boolean;
  /** Null when the node has no blocker of its own (PROPAGATED / CLEAR). */
  attribution: BlockerAttribution | null;
  /** The node's own stage — the §3.3 final tiebreak. */
  stage: number | null;
}

export interface BlockerTreeOptions {
  childrenByParent?: ReadonlyMap<string, readonly Issue[]> | Record<string, readonly Issue[]>;
  issueByIdentifier?: ReadonlyMap<string, Issue> | Record<string, Issue | undefined>;
  /** Active task ids, issue ids, or actor keys (`type:id`). */
  activeRuns?: ReadonlySet<string> | readonly string[];
  now?: Date | string;
  maxDepth?: number;
}

export interface BlockerTreeResult extends BlockerTreeNode {
  nodes: ReadonlyMap<string, BlockerTreeNode>;
}

const TERMINAL = new Set(["done", "cancelled"]);
const MEMBER_REVIEW_MS = 24 * 60 * 60 * 1000;
const AGENT_REVIEW_MS = 30 * 60 * 1000;

function lookupChildren(options: BlockerTreeOptions, id: string): readonly Issue[] {
  const source = options.childrenByParent;
  if (!source) return [];
  if (typeof (source as ReadonlyMap<string, readonly Issue[]>).get === "function") {
    return (source as ReadonlyMap<string, readonly Issue[]>).get(id) ?? [];
  }
  return (source as Record<string, readonly Issue[]>)[id] ?? [];
}

function lookupIssue(options: BlockerTreeOptions, identifier: string): Issue | undefined {
  const source = options.issueByIdentifier;
  if (!source) return undefined;
  if (typeof (source as ReadonlyMap<string, Issue>).get === "function") {
    return (source as ReadonlyMap<string, Issue>).get(identifier);
  }
  return (source as Record<string, Issue | undefined>)[identifier];
}

function ref(issue: Issue): BlockerIssueRef {
  return { id: issue.id, identifier: issue.identifier };
}

function hasActiveRun(options: BlockerTreeOptions, issue: Issue): boolean {
  const runs = options.activeRuns;
  if (!runs) return false;
  const set = runs instanceof Set ? runs : new Set(runs);
  return (
    set.has(issue.id) ||
    (issue.assignee_type !== null && issue.assignee_id !== null &&
      (set.has(`${issue.assignee_type}:${issue.assignee_id}`) || set.has(issue.assignee_id)))
  );
}

function reviewOverdue(issue: Issue, options: BlockerTreeOptions, now: number): boolean {
  if (issue.status !== "in_review" || hasActiveRun(options, issue)) return false;
  const close = readCloseProtocol(issue.metadata, issue.status);
  if (!close.at) return false;
  const at = Date.parse(close.at);
  if (!Number.isFinite(at)) return false;
  const threshold = close.nextOwnerType === "member" ? MEMBER_REVIEW_MS : AGENT_REVIEW_MS;
  return now - at >= threshold;
}

/**
 * Whether the close record still describes the ticket: it matches the
 * current status and no reply has superseded it (DENE-1301).
 */
function closeRecordCurrent(close: CloseProtocolView): boolean {
  return !close.statusDrift && !close.superseded;
}

/**
 * The ticket is blocked right now: by its status, or by a blocked close
 * record that still describes it. A blocked close a person already answered
 * (superseded, or the status moved on) is history, not a blocker.
 */
function isBlockedNow(issue: Issue, close: CloseProtocolView): boolean {
  return issue.status === "blocked" || (close.conclusion === "blocked" && closeRecordCurrent(close));
}

function metaText(issue: Issue, key: string): string {
  const value = issue.metadata?.[key];
  return typeof value === "string" ? value.trim() : "";
}

/**
 * A finished wait target only means a missed wake when nothing happened on
 * the waiter afterwards (DENE-1301): the platform did not record waking it
 * for that target, it has no run in flight, and it has no activity since the
 * target last changed. A waiter that was woken and moved on by comments
 * without closing again is not stuck.
 */
function wakeMissedOn(issue: Issue, target: Issue, waiting: string, options: BlockerTreeOptions): boolean {
  if (!TERMINAL.has(target.status)) return false;
  const woken = metaText(issue, "block.woken_by").split(",").map((token) => token.trim());
  if (woken.includes(waiting) || woken.includes(target.identifier) || woken.includes(target.id)) return false;
  if (hasActiveRun(options, issue)) return false;
  const activity = Date.parse(issue.last_activity_at ?? issue.updated_at);
  const finished = Date.parse(target.updated_at);
  return !(Number.isFinite(activity) && Number.isFinite(finished) && activity > finished);
}

/** §3.3 / §3.4 "needs you": a member owner, or a decision/permission kind. */
function attributionNeedsUserAction(issue: Issue, close: CloseProtocolView, overdue: boolean): boolean {
  if (overdue) return close.nextOwnerType === "member";
  if (!isBlockedNow(issue, close)) return false;
  return (closeRecordCurrent(close) && close.nextOwnerType === "member") ||
    close.blockKind === "decision" ||
    close.blockKind === "permission" ||
    metaText(issue, "block.needs_human") !== "";
}

/**
 * Derive the current blocker frontier from issue metadata and staged children.
 * This is intentionally pure: callers provide their already-fetched snapshots.
 */
export function deriveBlockerTree(root: Issue, options?: BlockerTreeOptions): BlockerTreeResult;
export function deriveBlockerTree(input: BlockerTreeOptions & { root: Issue }): BlockerTreeResult;
export function deriveBlockerTree(
  rootOrInput: Issue | (BlockerTreeOptions & { root: Issue }),
  maybeOptions?: BlockerTreeOptions,
): BlockerTreeResult {
  const root = "root" in rootOrInput ? rootOrInput.root : rootOrInput;
  const options = "root" in rootOrInput ? rootOrInput : (maybeOptions ?? {});
  const now = Date.parse(options.now instanceof Date ? options.now.toISOString() : options.now ?? new Date().toISOString());
  const nodes = new Map<string, BlockerTreeNode>();
  const maxDepth = options.maxDepth ?? 4;

  function walk(issue: Issue, ancestors: Set<string>, depth: number): BlockerTreeNode {
    const loop = ancestors.has(issue.id);
    if (loop || depth > maxDepth) {
      // A cycle placeholder is authoritative for this path.  Do not allow the
      // outer frame to memoize a propagated result over the cycle attribution.
      const result: BlockerTreeNode = { issue: ref(issue), state: loop ? "ROOT" : "CLEAR", rootCauses: loop ? [ref(issue)] : [], frontierStage: null, sideBlockers: [], userActionCount: 0, derived: loop, cycle: loop, attribution: loop ? { kind: "cycle", action: null, needsUserAction: false, nextOwnerType: null, nextOwnerId: null, waitingOn: null, at: null } : null, stage: issue.stage };
      nodes.set(issue.id, result);
      return result;
    }
    const existing = nodes.get(issue.id);
    if (existing) return existing;
    const close = readCloseProtocol(issue.metadata, issue.status);
    const waiting = closeProtocolWaitingOn(close.waitingOn);
    const waitingIssue = waiting ? lookupIssue(options, waiting) : undefined;
    // §3.4 precedence, first hit wins: a wait whose target already finished is
    // a wake_missed blocker, then a review nobody picked up, then a recorded
    // blocked conclusion. `dependency` is never its own root — the target is.
    const wakeMissed = waitingIssue !== undefined && waiting !== null && wakeMissedOn(issue, waitingIssue, waiting, options);
    const overdue = reviewOverdue(issue, options, now);
    const blockedNow = isBlockedNow(issue, close);
    // A dependency block is rooted at its target, reached through waiting_on;
    // without that edge the ticket itself is the only place to show it.
    const own = wakeMissed || overdue || (blockedNow && !(close.blockKind === "dependency" && waiting));
    const needsUserAction = own && attributionNeedsUserAction(issue, close, overdue);
    const children = lookupChildren(options, issue.id).filter((child) => !TERMINAL.has(child.status));
    const staged = children.filter((child) => child.stage !== null);
    const frontierStage = staged.length ? Math.min(...staged.map((child) => child.stage!)) : null;
    const frontier = frontierStage === null ? children : children.filter((child) => child.stage === frontierStage);
    const nextAncestors = new Set(ancestors).add(issue.id);
    const childResults = frontier.map((child) => walk(child, nextAncestors, depth + 1));
    const waitingResult = waitingIssue && !TERMINAL.has(waitingIssue.status) ? walk(waitingIssue, nextAncestors, depth + 1) : undefined;
    // waiting_on is itself an active dependency edge while the target is
    // non-terminal. Keep a reference even when its snapshot is missing or it
    // currently has no own blocker, so cross-ticket waits are never silent.
    // Terminal targets are already represented by this waiting node's
    // wake-missed ROOT attribution. They are no longer actionable blockers,
    // so do not expose the completed/cancelled target as a root cause. Keep
    // the dependency reference only while the target is active or its
    // snapshot is unavailable.
    const waitingRef = waiting && (!waitingIssue || !TERMINAL.has(waitingIssue.status))
      ? (waitingIssue ? ref(waitingIssue) : { id: waiting, identifier: waiting })
      : undefined;
    const causes = [...(own ? [ref(issue)] : []), ...childResults.flatMap((child) => child.rootCauses), ...(waitingRef ? [waitingRef] : []), ...(waitingResult?.rootCauses ?? [])];
    const unique = [...new Map(causes.map((cause) => [cause.id, cause])).values()];
    const state: BlockerState = own ? "ROOT" : unique.length ? "PROPAGATED" : "CLEAR";
    const userActionCount = (needsUserAction ? 1 : 0) +
      childResults.reduce((count, child) => count + child.userActionCount, 0) +
      (waitingResult?.userActionCount ?? 0);
    const prior = nodes.get(issue.id);
    if (prior?.cycle) return prior;
    const result: BlockerTreeNode = { issue: ref(issue), state, rootCauses: unique, frontierStage, sideBlockers: children.filter((child) => !frontier.includes(child)).map(ref), userActionCount, derived: own && !blockedNow, cycle: false, attribution: own ? { kind: wakeMissed ? "wake_missed" : overdue ? "review_overdue" : close.blockKind ?? "blocked", action: close.blockAction, needsUserAction, nextOwnerType: close.nextOwnerType, nextOwnerId: close.nextOwnerId, waitingOn: waiting, at: close.at } : null, stage: issue.stage };
    nodes.set(issue.id, result);
    return result;
  }

  return { ...walk(root, new Set(), 0), nodes };
}

/** Total order over numbers that treats equal infinities as equal. */
function compareNumbers(a: number, b: number): number {
  if (a === b) return 0;
  return a < b ? -1 : 1;
}

/**
 * §3.3 order for the parent summary card: whoever needs a human first, then
 * the ticket that has been blocked longest, then the earliest stage. The
 * design's second rule — blocking set before side blockers — is structural
 * here: side blockers are never walked, so they cannot be root causes. Rows
 * with no resolved node (a cross-family target no snapshot reached) sort after
 * their group, and `sort` is stable, so they keep frontier order.
 */
export function orderBlockerRootCauses(tree: BlockerTreeResult): BlockerIssueRef[] {
  const rank = (cause: BlockerIssueRef) => tree.nodes.get(cause.id)?.attribution?.needsUserAction === true ? 0 : 1;
  const blockedSince = (cause: BlockerIssueRef) => {
    const at = tree.nodes.get(cause.id)?.attribution?.at;
    const parsed = at ? Date.parse(at) : Number.NaN;
    return Number.isFinite(parsed) ? parsed : Number.POSITIVE_INFINITY;
  };
  const stage = (cause: BlockerIssueRef) => tree.nodes.get(cause.id)?.stage ?? Number.POSITIVE_INFINITY;
  return [...tree.rootCauses].sort((a, b) =>
    compareNumbers(rank(a), rank(b)) ||
    compareNumbers(blockedSince(a), blockedSince(b)) ||
    compareNumbers(stage(a), stage(b)));
}
