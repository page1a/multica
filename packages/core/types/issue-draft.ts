/**
 * Requirement alignment: the conversation that happens BEFORE an issue exists.
 *
 * Creating an issue enqueues agent work, so a misunderstood request starts
 * executing before anyone has agreed what it means. A draft is that agreement
 * in progress — a hidden conversation plus a server-owned structured object —
 * and nothing is created until `finalize`.
 */

/** Where an alignment conversation has got to. */
export type IssueDraftStatus = "draft" | "ready" | "completed" | "abandoned";

/**
 * One sub-issue of an alignment. `key` is this sub-issue's name WITHIN this
 * draft: the preview panel mints one when a row first enters the draft, and
 * every later save carries it back unchanged. It is not a UUID — the server
 * hashes (conversation, key) into the sub-issue's real identity, so a client
 * cannot name an identity that belongs to someone else's alignment, and the
 * same key resolves to the same issue on a repeated confirm.
 */
export interface IssueDraftChild {
  key: string;
  title: string;
  description: string;
  status: string;
  priority: string;
  assignee_type?: string | null;
  assignee_id?: string | null;
  /** The stage this sub-issue belongs to, 1-based. Absent/null means "no
   *  stage", i.e. the implicit single stage. Stage 1 starts on confirm; later
   *  stages are parked in Backlog until someone promotes them. */
  stage?: number | null;
  /**
   * What kind of work this is ("backend implementation"), as the carrier
   * describes it.
   *
   * Client-owned: the server never reads it. The carrier has no workspace
   * roster, so it names the work instead of a person, and the preview panel
   * shows the hint beside the assignee picker until the user has picked the
   * real assignee.
   */
  assignee_hint?: string | null;
}

/**
 * The structured issue a draft has arrived at. Only these fields are read by
 * the server at finalize; anything else the client stores alongside them is
 * preserved untouched.
 *
 * The eight flat fields describe the group's ROOT — the parent issue. A request
 * that turned out to be several pieces of work puts them in `children`, and the
 * confirm creates the whole group in one transaction. An absent or empty
 * `children` is not a legacy shape to be tolerated: it is a group with a single
 * node, which is exactly what a lone issue always was.
 */
export interface IssueDraftPayload {
  title: string;
  description: string;
  status: string;
  priority: string;
  assignee_type?: string | null;
  assignee_id?: string | null;
  project_id?: string | null;
  parent_issue_id?: string | null;
  children?: IssueDraftChild[];
  /**
   * Where the alignment carrier thinks this work belongs.
   *
   * A name, never an id. The carrier has no authority to file work into a
   * project: the preview resolves the name against the real list, and the
   * person confirms. `create` is only honoured for a group (a parent plus
   * sub-issues); a single issue that proposes one is ignored.
   */
  project_proposal?: IssueDraftProjectProposal | null;
  /**
   * The person's own decision about that proposal. Absent means "follow the
   * proposal". Set the moment they pick, rename, or clear, so a later reply
   * that restates the proposal does not undo them.
   */
  project_choice?: IssueDraftProjectChoice | null;
}

/** The carrier's project judgement. `name` is a title, never a project id. */
export interface IssueDraftProjectProposal {
  action: "existing" | "create";
  name: string;
  icon?: string | null;
  description?: string | null;
}

/**
 * What the person decided on the confirm panel.
 *
 * `existing` records the project they picked (the id lives here AND on
 * `project_id`, so a reader that only knows the id still files the group).
 * `create` is the project the confirm will make together with the issues.
 * `none` is an explicit "do not attach a project".
 */
export interface IssueDraftProjectChoice {
  kind: "none" | "existing" | "create";
  project_id?: string | null;
  name?: string | null;
  icon?: string | null;
  description?: string | null;
}

/**
 * How the carrier is asking: which alignment policy this conversation runs
 * under, and which version of that policy's prompt it was given.
 *
 * `version` is the audit half — it names the prompt that produced the draft, and
 * it is read back from the draft row, so a finished alignment still reports the
 * version it ran after the registry moved on. `key` is empty when the backend
 * predates policies: nothing here can be switched then, and the page hides the
 * control rather than offering a switch that cannot land.
 */
export interface IssueDraftPolicy {
  key: string;
  version: string;
  /** Whether this policy asks the user questions — the guided `question` and
   *  `frontend` policies do, `conversation` does not. The server decides, so
   *  the page never hardcodes which key is which. */
  guided: boolean;
}

/**
 * Which built-in alignment methods this conversation runs with, and which
 * version of their text the carrier was given.
 *
 * The other half of the audit record `policy` carries: the policy version pins
 * the shared contract and the policy's own behaviour, and this pins the methods
 * they were assembled with. `keys` is the recorded list rather than this
 * client's idea of it — a backend may name a capability a later build retired —
 * so a caller intersects it with the keys it knows before rendering a control.
 *
 * Both fields fall back to the empty state, which is what an installed desktop
 * client sees when it talks to a backend that predates capabilities: no method
 * list, and a control that renders nothing rather than guessing.
 */
export interface IssueDraftCapabilities {
  keys: string[];
  version: string;
}

/** One alignment draft as the server owns it. */
export interface IssueDraft {
  /** The alignment conversation. It is also the draft's identity — one
   *  conversation has exactly one draft. */
  chat_session_id: string;
  workspace_id: string;
  status: IssueDraftStatus;
  /** Optimistic-concurrency token. Every save bumps it, and both save and
   *  finalize refuse a token that is not the one the user was looking at. */
  revision: number;
  draft: IssueDraftPayload;
  /** The issue this draft became. Present only once status is `completed`,
   *  and stable across repeated confirms. It is the group's ROOT issue; the
   *  rest of the group is read back through it. */
  issue_id?: string | null;
  /**
   * How many times this alignment has been reopened for another round: 0 until
   * the second round, then one more per reopen. A confirmed alignment is not
   * necessarily finished — a follow-up round appends to the SAME group — so
   * this is what a page reads to know it is looking at a continuation rather
   * than at a first pass.
   *
   * The round a person sees is this plus one. Absent on a backend that predates
   * rounds; the client then reads 0, which is the "first round" answer.
   */
  finalize_round?: number;
  /**
   * The content revision the previous round confirmed, recorded when the draft
   * was reopened. Absent until then.
   */
  finalized_revision?: number | null;
  /** The policy and prompt version in force for this conversation. */
  policy: IssueDraftPolicy;
  /** The built-in methods this conversation runs with, and their text version. */
  capabilities: IssueDraftCapabilities;
  created_at: string;
  updated_at: string;
}

/** A newly opened alignment conversation and its empty draft. */
export interface IssueDraftSession {
  session_id: string;
  /** The hidden carrier that executes this conversation.
   *
   * It is also where the create-time execution choices live — the model and the
   * reasoning effort are columns on this agent row, read by the daemon when it
   * claims a turn, and they are deliberately not echoed here: the row is the one
   * answer, and a second copy in the response is a copy that can disagree. */
  agent_id: string;
  /** Where this conversation actually runs. Seed the runtime picker from it so
   *  it can never disagree with what answers the next message. */
  runtime_id: string;
  draft: IssueDraft;
}

/** One unfinished alignment conversation, as listed for resuming. */
export interface IssueDraftSummary extends IssueDraft {
  title: string;
  runtime_id: string;
  last_message_content: string;
  last_message_role: string;
  last_message_at: string;
}

/** One issue a confirm created, enough to render it as a row. */
export interface IssueDraftCreatedIssue {
  id: string;
  /** MUL-123 — what a person reads, not the UUID. */
  identifier: string;
  title: string;
  status: string;
  stage?: number | null;
  assignee_type?: string | null;
  assignee_id?: string | null;
  parent_issue_id?: string | null;
}

/**
 * One node of a confirmed group whose assignee could not be applied at confirm.
 * The issue itself was still created — it is unassigned. `key` is empty for the
 * parent, which is what maps the warning back onto the row the panel shows.
 */
export interface IssueDraftAssignmentWarning {
  key: string;
  title: string;
  reason: string;
}

/**
 * Result of confirming a draft.
 *
 * `issue_id` is the same value for every repeat of the same confirm — the
 * protocol creates at most one group per alignment — and it stays the answer to
 * "where does the user go now": it is the group's root (parent) issue.
 * `issues` is the whole group, root first, and is absent from a backend that
 * predates groups; callers that need the group degrade to `[issue_id]`, which
 * is exactly what a group with no sub-issues is.
 *
 * `assignment_warnings` names the nodes that were created unassigned because
 * the seat shown on the panel could not be applied. Absent from a backend that
 * predates the field, and empty when every assignment landed; the two mean the
 * same thing to a reader (DENE-694).
 */
export interface IssueDraftFinalizeResult {
  draft: IssueDraft;
  issue_id: string;
  issues?: IssueDraftCreatedIssue[];
  assignment_warnings?: IssueDraftAssignmentWarning[];
}

/** Result of rebinding a live alignment conversation to another runtime. */
export interface IssueDraftRuntimeSwitch {
  runtime_id: string;
}
