import type {
  IssueDraftChild,
  IssueDraftCreatedIssue,
  IssueDraftFinalizeResult,
  IssueDraftPayload,
} from "../types";

/**
 * The group an alignment settles on: the parent issue plus its sub-issues, and
 * what confirming it will actually start.
 *
 * Two rules live here, both of them things that must agree between what the
 * panel SHOWS and what the server is asked to CREATE — a preview that promises
 * one thing and a confirm that does another is worse than no preview:
 *
 *   1. A sub-issue's status is derived from its stage, never authored. Stage 1
 *      runs the moment the group exists; stage 2 and later sit in Backlog with
 *      their assignee already bound, waiting for someone to promote the stage.
 *      The same rule is mirrored at the server's final write boundary
 *      (issueDraftChildStatusForCreate), because the confirm sends a revision
 *      and the server is the last place a raw payload can be caught.
 *   2. A ROOT with sub-issues is the group's coordinator: an active status (see
 *      ISSUE_DRAFT_COORDINATOR_STATUS) with its assignee kept, but no run of its
 *      own at confirm time. It exists to be woken by the stage barrier, and a
 *      backlog root would never be woken at all.
 *   3. The stage set is normalized before it is stored: stages are 1..N with no
 *      gaps, and a group where SOME sub-issues are staged gets stage 1 for the
 *      rest. A sub-issue with no stage in a staged group falls out of the stage
 *      barrier silently, which is worse than being late.
 *
 * Everything is pure and framework-free so the panel, the fold and the tests
 * can all ask the same question.
 */

/** The most sub-issues one payload may carry. The server refuses more (§2.4). */
export const ISSUE_DRAFT_MAX_CHILDREN = 20;

/** What the alignment prompt asks for by default: a group a person can read. */
export const ISSUE_DRAFT_RECOMMENDED_CHILDREN = 8;

/**
 * The status a root with sub-issues is created with.
 *
 * A group's root coordinates: it is the node the stage barrier wakes when a
 * stage closes, and `notifyParentOfChildDone` skips three kinds of parent
 * outright — `done`, `cancelled`, and `backlog` (MUL-3497, the
 * "don't accidentally activate a parked parent" rule). A backlog coordinator
 * would therefore never be woken, and every stage after the first would sit in
 * Backlog forever with nobody told to promote it.
 *
 * So the coordinator stays ACTIVE. Backlog is deliberately not reused, and the
 * root is kept out of the queue by the create path instead: the confirm
 * suppresses its assignee run (`SuppressAssigneeRun`), which is a rule about
 * this confirm rather than a state the issue is left in. Later writes to the
 * root — a person reassigning it, a status change — behave like any other
 * issue's.
 *
 * The server mirrors this value at its final write boundary
 * (issueDraftCoordinatorStatus), because the panel's save is not the only way a
 * payload reaches the confirm.
 */
export const ISSUE_DRAFT_COORDINATOR_STATUS = "in_progress";

/**
 * The status a sub-issue is created with, from its stage.
 *
 * "No stage" is not "no work": a group with no stages at all is one implicit
 * stage, and everything in it starts together — that is what a small request
 * that happened to be split looks like. Only an explicit stage ≥ 2 is a parking
 * spot.
 */
export function issueDraftChildStatus(
  stage: number | null | undefined,
): "todo" | "backlog" {
  return (stage ?? 1) <= 1 ? "todo" : "backlog";
}

/** The highest stage in a group, 0 when nothing is staged. */
export function maxIssueDraftChildStage(
  children: readonly IssueDraftChild[],
): number {
  return children.reduce(
    (max, child) => (child.stage != null && child.stage > max ? child.stage : max),
    0,
  );
}

/** Whether a stage is one the server will accept (1..20, or absent). */
function isUsableIssueDraftStage(stage: number | null | undefined): boolean {
  if (stage == null) return true;
  return (
    Number.isInteger(stage) && stage >= 1 && stage <= ISSUE_DRAFT_MAX_CHILDREN
  );
}

/**
 * Stages for the group, as the server will store them: contiguous, in the order
 * the sub-issues appear, and never moved down to 1.
 *
 * An unstaged group stays unstaged — forcing stage numbers onto it would invent
 * an order nobody agreed to, and every value the barrier reads would then come
 * from the client's rendering order. Once ANY sub-issue carries a stage,
 * however, the group is a staged one and every member has to participate:
 * `stageBarrierClosed` skips unstaged siblings, so such a sub-issue would
 * neither hold a stage back nor be woken by one (design §6.4).
 *
 * Gaps are closed (1, 3 → 1, 2) because `stageProgressSummary` prints the real
 * numbers into the promotion comment and "Stage 1 / Stage 3" reads as a bug.
 * The LOWEST stage is deliberately not renumbered: a group whose only stage is
 * 2 is a group that waits, and squashing it to 1 would silently turn a parked
 * sub-issue into one that runs the moment it is created.
 */
export function normalizeIssueDraftChildStages(
  children: readonly IssueDraftChild[],
): IssueDraftChild[] {
  const stages = children.map((child) =>
    isUsableIssueDraftStage(child.stage) ? (child.stage ?? null) : null,
  );
  if (stages.every((stage) => stage === null)) {
    return children.map((child) =>
      child.stage == null ? child : { ...child, stage: null },
    );
  }
  const distinct = [...new Set(stages.map((stage) => stage ?? 1))].sort(
    (a, b) => a - b,
  );
  // Renumbered from the lowest stage up, so the carrier's order is the plan and
  // the numbers stay inside the range the server accepts: the values are already
  // 1..20, so a contiguous run starting at the lowest one cannot leave it.
  const start = distinct[0] ?? 1;
  return children.map((child, index) => {
    const stage = stages[index] ?? 1;
    return { ...child, stage: start + distinct.indexOf(stage) };
  });
}

/**
 * Gives every sub-issue a usable key.
 *
 * The carrier is told to keep its keys stable and the parse path mints one when
 * it forgets; this is the same repair applied to a draft that already exists,
 * so a row that arrived without a key cannot reach the confirm — the server
 * refuses a keyless sub-issue with a 400, and a duplicate key derives the same
 * identity twice and collides inside the create transaction.
 */
export function mintIssueDraftChildKeys(
  children: readonly IssueDraftChild[],
): IssueDraftChild[] {
  const used = new Set<string>();
  let next = 1;
  return children.map((child) => {
    const key = child.key.trim();
    if (key.length > 0 && key.length <= 64 && !used.has(key)) {
      used.add(key);
      return child.key === key ? child : { ...child, key };
    }
    while (used.has(`c${next}`)) next += 1;
    const minted = `c${next}`;
    used.add(minted);
    return { ...child, key: minted };
  });
}

/**
 * The sub-issue set as the server should receive it: keys present and unique,
 * stages contiguous, statuses derived from those stages, priority defaulted.
 *
 * Applied at every write boundary (the carrier's block, the panel's save), so
 * the payload stored on the server is already the one the confirm will read —
 * the confirm sends a revision, not a payload, and therefore cannot normalize
 * anything itself.
 */
export function normalizeIssueDraftChildren(
  children: readonly IssueDraftChild[],
): IssueDraftChild[] {
  return mintIssueDraftChildKeys(normalizeIssueDraftChildStages(children)).map(
    (child) => ({
      ...child,
      status: issueDraftChildStatus(child.stage),
      priority: child.priority.trim() || "none",
    }),
  );
}

/** The payload with its group normalized, or the same object when there is
 *  nothing to normalize. */
export function normalizeIssueDraftPayloadGroup(
  payload: IssueDraftPayload,
): IssueDraftPayload {
  const children = payload.children ?? [];
  if (children.length === 0) return payload;
  const normalized = normalizeIssueDraftChildren(children);
  const sameChildren = sameIssueDraftChildren(children, normalized);
  // A grouped root is the coordinator, so its status is derived too — from the
  // existence of sub-issues rather than from a stage. Normalizing the stored
  // payload (not just the preview) is what keeps the draft the confirm reads
  // byte-compatible with the issue the confirm creates.
  const statusChanged =
    (payload.status.trim() || "todo") !== ISSUE_DRAFT_COORDINATOR_STATUS;
  if (sameChildren && !statusChanged) return payload;
  return {
    ...payload,
    ...(statusChanged ? { status: ISSUE_DRAFT_COORDINATOR_STATUS } : {}),
    ...(sameChildren ? {} : { children: normalized }),
  };
}

/** Whether two sub-issue sets are the same, field by field. */
export function sameIssueDraftChildren(
  a: readonly IssueDraftChild[],
  b: readonly IssueDraftChild[],
): boolean {
  if (a.length !== b.length) return false;
  return a.every((child, index) => {
    const other = b[index];
    if (!other) return false;
    return (
      child.key === other.key &&
      child.title === other.title &&
      child.description === other.description &&
      child.status === other.status &&
      child.priority === other.priority &&
      (child.stage ?? null) === (other.stage ?? null) &&
      (child.assignee_type ?? null) === (other.assignee_type ?? null) &&
      (child.assignee_id ?? null) === (other.assignee_id ?? null) &&
      (child.assignee_hint ?? null) === (other.assignee_hint ?? null)
    );
  });
}

/** The fields whose value decides whether an agent starts on its own. */
export interface IssueDraftNodeDispatch {
  status: string;
  assignee_type?: string | null;
  assignee_id?: string | null;
}

/**
 * Whether creating this node starts an agent straight away.
 *
 * The server's rule, mirrored from `shouldEnqueueAgentTaskWithQueries` /
 * `maybeEnqueueOnAssign`: work is enqueued unless the effective status is
 * Backlog, and there is nothing to enqueue without an agent OR SQUAD assignee.
 * `status` is normalized the way the server normalizes it — an empty string is
 * `todo`.
 *
 * A member assignee is deliberately not "running": assigning a person enqueues
 * nothing, and saying otherwise would tell the user a human had been paged.
 * A squad does run — the server wakes its leader — and leaving it out made the
 * preview promise nothing for a row that starts work.
 */
export function issueDraftNodeRunsOnCreate(
  node: IssueDraftNodeDispatch,
): boolean {
  const status = node.status.trim() || "todo";
  if (status === "backlog") return false;
  const type = node.assignee_type;
  if (type !== "agent" && type !== "squad") return false;
  return !!node.assignee_id;
}

/**
 * Whether a stage closing can wake this node's seat.
 *
 * The sibling of `issueDraftNodeRunsOnCreate`, and a different question: a
 * coordinator is never started at confirm time, but it is the node the stage
 * barrier wakes afterwards, and `notifyParentOfChildDone` skips two shapes of
 * parent outright — one assigned to a person (MUL-2538) and one with no
 * assignee at all, which has no seat to wake. Both leave every stage after the
 * first parked in Backlog with nothing said: no comment, no inbox row, no
 * promotion. The panel has to name that gap rather than imply an automatic
 * advance that cannot happen.
 *
 * Only agent and squad qualify, exactly as on the create path: a person
 * assignee is skipped by the barrier, and no status of the coordinator's own
 * can change that.
 */
export function issueDraftNodeWakesOnStageClose(
  node: IssueDraftNodeDispatch,
): boolean {
  const type = node.assignee_type;
  return (type === "agent" || type === "squad") && !!node.assignee_id;
}

/**
 * What one row of the confirm preview will do.
 *
 * One value per row, so the panel renders a badge instead of re-deriving the
 * rule — a second copy of the dispatch policy is what let "unassigned sub-issue"
 * and "waits for its stage" drift apart in the first place.
 */
export type IssueDraftGroupRowOutcome =
  /** An agent or squad is paged the moment the row exists. */
  | "starts"
  /** Created in Backlog with its assignee bound, waiting for its stage. */
  | "parked"
  /** Created with nobody on it; nothing is started, and it stays visible as unassigned. */
  | "unassigned"
  /** Assigned to a person: created, and no agent is paged. */
  | "member"
  /** The root of a group with sub-issues: it coordinates instead of executing. */
  | "coordinates";

/** One line of the confirm preview: a node and what confirming does with it. */
export interface IssueDraftGroupRow {
  /** The node's key. Empty for the group's root. */
  key: string;
  title: string;
  stage: number | null;
  /** The status the node will be created with. */
  status: string;
  assigneeType: string | null;
  assigneeId: string | null;
  assigneeHint: string | null;
  isRoot: boolean;
  /** Confirming creates this issue AND starts its assignee's work. */
  startsOnCreate: boolean;
  /** Why it runs, or why nothing runs. One badge per row reads from this. */
  outcome: IssueDraftGroupRowOutcome;
  /**
   * This node already owns an issue, so this confirm adopts it and never
   * rewrites it (DENE-414). False for every node of a first round, which is
   * what makes a payload with no group behind it read exactly as it did before
   * continuation rounds existed.
   */
  alreadyBuilt: boolean;
}

export interface IssueDraftGroupPlan {
  /** The root first, then the sub-issues in payload order. */
  rows: IssueDraftGroupRow[];
  /** Every node the payload names, built ones included. */
  total: number;
  /** How many issues this confirm creates — the nodes that own none yet. */
  creating: number;
  /** How many of them start an agent the moment they exist. */
  starting: number;
  /** How many are created in Backlog, waiting for their stage. */
  parked: number;
  /** How many already exist and are adopted untouched. */
  built: number;
  /**
   * This group HAS a coordinator and it can be woken when a stage closes: the
   * parent carries an agent or squad assignee and the stage barrier will page
   * it to promote the next stage.
   *
   * False both for a group whose coordinator is unassigned or held by a person
   * — nobody will be woken, and every stage after the first stays in Backlog
   * until someone promotes it by hand — and for a payload with no sub-issues,
   * where there is no coordinator at all. The two are told apart by `total`;
   * the panel is the only surface that has to, because it is the only one that
   * promises the user what confirming will do.
   */
  coordinatorWakeable: boolean;
}

/**
 * What pressing "confirm and create" will do, as one list.
 *
 * A sub-issue's status is derived from its stage, which is what makes the
 * preview agree with the stored payload even while the user is mid-edit. The
 * root's is taken from the payload — except when the payload HAS sub-issues, in
 * which case the root is the group's coordinator and its status is the
 * coordinator's, whatever the payload happened to carry (see
 * ISSUE_DRAFT_COORDINATOR_STATUS).
 *
 * `builtKeys` are the nodes a previous round already created (see
 * `issueDraftBuiltNodeKeys`); the ROOT's key is `""`, exactly as it is in the
 * payload and in the server's own node model. They stay in `rows` — the panel
 * still has to render what the group is made of — but they are excluded from
 * every "this confirm will…" count, because the server skips them and never
 * rewrites their fields. Without it, a continuation round would promise to
 * create work it adopts.
 */
export function planIssueDraftGroup(
  payload: IssueDraftPayload | null,
  builtKeys?: ReadonlySet<string>,
): IssueDraftGroupPlan {
  if (!payload) {
    return {
      rows: [],
      total: 0,
      creating: 0,
      starting: 0,
      parked: 0,
      built: 0,
      coordinatorWakeable: false,
    };
  }

  const isBuilt = (key: string) => builtKeys?.has(key) === true;
  const hasChildren = (payload.children?.length ?? 0) > 0;
  const rootStatus = hasChildren
    ? ISSUE_DRAFT_COORDINATOR_STATUS
    : payload.status.trim() || "todo";
  const root: IssueDraftGroupRow = {
    key: "",
    title: payload.title,
    stage: null,
    status: rootStatus,
    assigneeType: payload.assignee_type ?? null,
    assigneeId: payload.assignee_id ?? null,
    assigneeHint: null,
    isRoot: true,
    // A coordinator is not an executor: it never starts its own run at confirm
    // time, however it is assigned. That is a statement about THIS confirm, not
    // about "nobody is assigned", which is why the row carries `coordinates`
    // rather than the unassigned badge.
    startsOnCreate: !hasChildren && issueDraftNodeRunsOnCreate(payload),
    outcome: hasChildren
      ? "coordinates"
      : issueDraftRowOutcome(rootStatus, payload.assignee_type, payload.assignee_id),
    // The root's key is "", the same key the payload and the server's node
    // model give it, so an adopted root is expressed in the same set as an
    // adopted sub-issue rather than as a second flag.
    alreadyBuilt: isBuilt(""),
  };
  const children = (payload.children ?? []).map<IssueDraftGroupRow>((child) => {
    const stage = child.stage ?? null;
    const status = issueDraftChildStatus(stage);
    return {
      key: child.key,
      title: child.title,
      stage,
      status,
      assigneeType: child.assignee_type ?? null,
      assigneeId: child.assignee_id ?? null,
      assigneeHint: child.assignee_hint ?? null,
      isRoot: false,
      startsOnCreate: issueDraftNodeRunsOnCreate({
        status,
        assignee_type: child.assignee_type,
        assignee_id: child.assignee_id,
      }),
      outcome: issueDraftRowOutcome(
        status,
        child.assignee_type,
        child.assignee_id,
      ),
      alreadyBuilt: isBuilt(child.key),
    };
  });
  const rows = [root, ...children];
  // A node that already exists is not created again, so it neither runs nor
  // waits: the counts below answer "what does THIS confirm do", and the
  // adoption is stated separately as `built`.
  const incoming = rows.filter((row) => !row.alreadyBuilt);
  return {
    rows,
    total: rows.length,
    creating: incoming.length,
    starting: incoming.filter((row) => row.outcome === "starts").length,
    parked: incoming.filter((row) => row.outcome === "parked").length,
    built: rows.length - incoming.length,
    // Asked of the payload's root, and only when there IS a coordinator: a
    // single-issue draft has no stage barrier to be woken by, and answering
    // "yes" for its assigned root would describe a promise this plan does not
    // make.
    coordinatorWakeable:
      hasChildren && issueDraftNodeWakesOnStageClose(payload),
  };
}

/**
 * The one reason a row does or does not run.
 *
 * Status first: a row in Backlog is parked whoever holds it — that is the
 * parking rule the server itself applies, and for a sub-issue the status is
 * already the stage's. Saying "unassigned" about a parked row would hide the
 * promotion it is actually waiting for. Only inside the running stage does the
 * assignee decide: nobody at all, a person, or an agent/squad that is paged on
 * create.
 *
 * An unassigned stage-1 sub-issue is created `todo` with no run: an issue the
 * board shows as unassigned is how a person finds it and fills the seat in, and
 * parking it in Backlog instead would hide exactly the gap the confirm warned
 * about. Nothing is enqueued either way — `maybeEnqueueOnAssign` has no
 * assignee to enqueue for.
 */
function issueDraftRowOutcome(
  status: string,
  assigneeType: string | null | undefined,
  assigneeId: string | null | undefined,
): IssueDraftGroupRowOutcome {
  if (status === "backlog") return "parked";
  if (!assigneeType || !assigneeId) return "unassigned";
  if (assigneeType === "member") return "member";
  return "starts";
}

/**
 * How far a group has got, for a surface that is not the alignment page: the
 * issue detail of one of its members.
 *
 * Reads the SAME two signals the board reads — the stage an issue carries, and
 * whether its status is done — and answers the only two questions worth a line
 * of text there: how much of the group is finished, and which stage is being
 * worked on. `activeStage` is the lowest stage that has work which is neither
 * finished nor parked in Backlog; when every remaining stage is parked, the
 * lowest parked one is the one waiting, so the answer names real work rather
 * than "stage 1" on a group whose stage 1 is long done.
 */
export interface IssueDraftGroupStageProgress {
  total: number;
  done: number;
  /** The highest stage in the group; 0 when the group is unstaged. */
  stages: number;
  /** The lowest stage with work still to do, or null when the group is done. */
  activeStage: number | null;
}

export function planIssueDraftGroupProgress<T extends { stage?: number | null; status: string }>(
  issues: readonly T[],
  isDone: (issue: T) => boolean,
): IssueDraftGroupStageProgress {
  const stages = issues.reduce(
    (max, issue) => (issue.stage != null && issue.stage > max ? issue.stage : max),
    0,
  );
  const remaining = issues.filter((issue) => !isDone(issue));
  const live = remaining.filter((issue) => (issue.status.trim() || "todo") !== "backlog");
  const pool = live.length > 0 ? live : remaining;
  const activeStage = pool.reduce<number | null>((lowest, issue) => {
    const stage = issue.stage ?? 1;
    return lowest === null || stage < lowest ? stage : lowest;
  }, null);
  return {
    total: issues.length,
    done: issues.length - remaining.length,
    stages,
    activeStage,
  };
}

/**
 * The issue this alignment is filed UNDER, when it was started from one
 * (DENE-452).
 *
 * An alignment started mid-flight — from an existing issue's detail page, or
 * from one of its comment threads — does not found a new top-level issue: the
 * issue it was started from is the group's parent, and everything the
 * conversation settles on is created beneath it. That intent lives in the
 * payload's `parent_issue_id`, which is written once when the draft is created
 * and travels with every later save untouched.
 *
 * Empty strings are read as absent, the same way the server reads them: a
 * client that writes `""` means "no parent", and addressing an issue with an
 * empty id is not a state worth representing.
 */
export function issueDraftParentIssueId(
  payload: Pick<IssueDraftPayload, "parent_issue_id"> | null | undefined,
): string | null {
  const id = payload?.parent_issue_id;
  return typeof id === "string" && id.length > 0 ? id : null;
}

/**
 * Where a confirmed alignment should land the user: back on the issue this
 * alignment was started from, when there was one, and on the issue it just
 * created otherwise.
 *
 * The person who starts an alignment from an issue is working IN that issue's
 * context; the group it settles on is that issue's children, so the parent is
 * what they come back to (DENE-452). A standalone alignment has no such
 * context, and the root it produced is the only sensible destination — which is
 * exactly what this returned before the parent case existed.
 */
export function issueDraftLandingIssueId(
  parentIssueId: string | null,
  createdIssueId: string | null,
): string | null {
  return parentIssueId ?? createdIssueId;
}

/**
 * The group a confirm produced, from the confirm's own response.
 *
 * A backend that predates groups sends no `issues`, and a group with no
 * sub-issues is reported as exactly that: one row for the root. Degrading here
 * rather than at the call site is what lets the panel render the same list
 * whichever server it is talking to, without a second request.
 */
export function issueDraftCreatedGroup(
  result: Pick<IssueDraftFinalizeResult, "issue_id" | "issues">,
): IssueDraftCreatedIssue[] {
  if (result.issues && result.issues.length > 0) return result.issues;
  return [
    {
      id: result.issue_id,
      identifier: "",
      title: "",
      status: "",
      stage: null,
      assignee_type: null,
      assignee_id: null,
      // A backend that predates groups describes no parent, and the fallback
      // row exists precisely to say "the server told us nothing beyond this
      // id". The group's real parent — the issue an alignment started
      // mid-flight was filed under — comes off the DRAFT, not off this
      // fabricated row, so `issueDraftParentIssueId` is what a caller reads
      // for it. Writing anything else here would invent a relationship the
      // server never reported.
      parent_issue_id: null,
    },
  ];
}
