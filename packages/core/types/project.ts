import type { ChatQuickAction } from "./chat";
import type { KnowledgeAuditChange } from "./close";

export type ProjectStatus = "planned" | "in_progress" | "paused" | "completed" | "cancelled";

export type ProjectPriority = "urgent" | "high" | "medium" | "low" | "none";

export interface Project {
  id: string;
  workspace_id: string;
  title: string;
  description: string | null;
  icon: string | null;
  status: ProjectStatus;
  priority: ProjectPriority;
  lead_type: "member" | "agent" | null;
  lead_id: string | null;
  // Calendar days ("YYYY-MM-DD"), no time-of-day or timezone — same contract as
  // issue.start_date / issue.due_date.
  start_date: string | null;
  due_date: string | null;
  created_at: string;
  updated_at: string;
  created_by?: string | null;
  issue_count: number;
  done_count: number;
  resource_count: number;
  /** Resource sharing scope. Older servers omit this field. */
  visibility?: "private" | "project" | "workspace";
  /** Domain ids this project carries, in workspace order; empty = generic
   *  (DENE-1451). Older servers omit this field. */
  domain_ids?: string[];
}

/** Checklist slot, without a daemon observation. Close dialogs read this. */
export interface ProjectMemoryChecklistItem {
  key: string;
  path: string;
  kind: "file" | "directory";
}

export interface ProjectMemoryLocation {
  key: string;
  path: string;
  kind: "file" | "directory";
  exists: boolean;
  is_directory: boolean;
  modified_at: string | null;
  observed_at: string | null;
  error: string | null;
  /** Set when missing locally but present on this remote ref: the local directory is behind. */
  mainline_ref: string | null;
}

export interface ProjectMemoryIssue {
  id: string;
  identifier: string;
  status: string;
  title: string;
}

// SedimentSource is what a boss-layer sediment rolled up: the parent ticket
// whose children finished, or the project that completed (DENE-1680).
export interface SedimentSource {
  kind: "issue" | "project";
  id: string;
  identifier: string | null;
  title: string;
}

// KnowledgeSediment is one delivery that wrote project memory: an issue close
// or a chat that merged into the main line (DENE-1661).
export interface KnowledgeSediment {
  id: string;
  source_kind: "issue" | "chat";
  issue_id: string | null;
  issue_identifier: string | null;
  chat_session_id: string | null;
  source_title: string;
  changes: KnowledgeAuditChange[];
  verified: boolean;
  mainline: string;
  commits: string[];
  pr_url: string;
  author_type: string;
  author_id: string;
  created_at: string;
  /** "boss" rolls up finished work across tickets; absent on older servers. */
  layer?: "worker" | "boss";
  sources?: SedimentSource[];
}

export interface ProjectMemoryStatus {
  project_id: string;
  /** Whose observation `locations` is: always the project's local directory. */
  source: "local_directory";
  workspace_id: string;
  locations: ProjectMemoryLocation[];
  missing: string[];
  observed_at: string | null;
  latest_sediment_at: string | null;
  sediment_issue: ProjectMemoryIssue | null;
  sediment_agent_configured: boolean;
  sediment_error: string | null;
  /** Newest deliveries that wrote this memory; absent on older servers. */
  recent_sediments?: KnowledgeSediment[];
}

/** A sediment in the monitor window, with what it superseded or deleted. */
export interface MonitorWrite extends KnowledgeSediment {
  /** False for a chat the person cannot open; its title is withheld. */
  source_accessible: boolean;
  superseded: string[];
  /** Deleted lines across memory files; null when the delivery predates the count. */
  deleted_lines: number | null;
}

/** A ticket that finished without writing project memory. */
export interface MonitorUnsettled {
  issue_id: string;
  identifier: string;
  title: string;
  /** none: its close declared nothing qualified; unaudited: no knowledge audit. */
  reason: "none" | "unaudited";
  closed_at: string;
}

export interface MonitorRound {
  issue_id: string;
  identifier: string;
  title: string;
  status: string;
  layer: "worker" | "boss";
  gap: string[];
  wrote: boolean;
  /** Ended without a sediment. */
  idle: boolean;
  created_at: string;
  updated_at: string;
}

export interface MonitorChatTicket {
  issue_id: string;
  identifier: string;
  title: string;
  status: string;
  flow: "open" | "reported" | "no_conclusion";
  updated_at: string;
}

export interface MonitorChat {
  chat_session_id: string;
  title?: string;
  accessible: boolean;
  dispatched: number;
  reported: number;
  no_conclusion: number;
  open: number;
  tickets: MonitorChatTicket[];
}

/** Project memory activity over one window (DENE-1681). */
export interface ProjectMemoryMonitor {
  project_id: string;
  days: number;
  since: string;
  writes: MonitorWrite[];
  unsettled: MonitorUnsettled[];
  rounds: { opened: number; open: number; idle: number; items: MonitorRound[] };
  chats: MonitorChat[];
}

export interface ProjectVisibilityPreview {
  project_id: string;
  visibility: "private" | "project" | "workspace";
  affected_count: number;
  previously_private_count: number;
}

export interface CreateProjectRequest {
  title: string;
  description?: string;
  icon?: string;
  status?: ProjectStatus;
  priority?: ProjectPriority;
  lead_type?: "member" | "agent";
  lead_id?: string;
  start_date?: string;
  due_date?: string;
  // Resources to attach in the same transaction as the project. Server returns
  // 4xx (and rolls back) if any one is invalid or duplicate.
  resources?: CreateProjectResourceRequest[];
  /** Domain ids (or names); empty = generic. */
  domain_ids?: string[];
}

export interface UpdateProjectRequest {
  title?: string;
  description?: string | null;
  icon?: string | null;
  status?: ProjectStatus;
  priority?: ProjectPriority;
  lead_type?: "member" | "agent" | null;
  lead_id?: string | null;
  // Omit the key to leave the date untouched; send null (or "") to clear it.
  start_date?: string | null;
  due_date?: string | null;
  /** Replaces the project's domains; [] makes it generic. */
  domain_ids?: string[];
}

export interface ListProjectsResponse {
  projects: Project[];
  total: number;
}

export interface ProjectMember {
  id: string;
  workspace_id: string;
  project_id: string;
  member_id: string;
  added_by: string | null;
  created_at: string;
  name: string;
  email: string;
  avatar_url: string | null;
}

/** A person granted direct access to one issue or repository ("specific people" scope). */
export interface ResourceShare {
  member_id: string;
  name: string;
  email: string;
  avatar_url: string | null;
  added_by: string | null;
  created_at: string;
}

// ProjectResource is a typed pointer from a project to an external resource.
// The resource_ref shape depends on resource_type. New types add a case in
// validateAndNormalizeResourceRef on the server and a renderer in the UI.
//
// Known types (UI must default-case unknown server-side additions):
//   - github_repo: cloud-side git checkout, ref = { url, ref?, default_branch_hint? }
//   - local_directory: agent execution on a specific daemon,
//     ref = { local_path, daemon_id, label?, execution_mode? }
export type ProjectResourceType = "github_repo" | "local_directory";

export interface GithubRepoResourceRef {
  url: string;
  ref?: string;
  default_branch_hint?: string;
  /**
   * Normalized repository identity (`host/owner/name`), computed from `url`
   * with the same function local_directory uses. Compared for duplicate
   * detection against a local checkout's `repo_key`.
   */
  repo_key?: string;
}

/**
 * How tasks sharing one local directory are executed.
 *
 * - `in_place`: the agent works directly in the user's directory and tasks run
 *   one at a time — a second task waits in `waiting_local_directory`. Edits
 *   land in the user's working copy.
 * - `worktree`: each task gets its own git worktree of that repo inside the
 *   runtime's workspace, so tasks run concurrently and deliver their work as a
 *   branch instead of touching the working copy. Every task of one conversation
 *   shares that branch — `agent/<agent>/<issue>` — so a follow-up continues the
 *   previous turn's work; a task with no conversation behind it gets
 *   `agent/<agent>/<task>`. Continuation is decided by an ownership record in
 *   the repo, not by the branch name, so a same-named branch the user made is
 *   never adopted.
 * - `shared`: the agent works directly in the user's directory like `in_place`,
 *   but without the per-directory lock. Tasks run concurrently; Multica keeps
 *   its own per-task files out of the directory. Isolation is the workspace's
 *   job — typically each task already runs in its own per-repo worktree under
 *   an umbrella directory of several repositories. The directory need not be a
 *   git repository. Nothing protects two tasks that edit the same checkout.
 *
 * Absent means `in_place`: resources created before the mode existed keep their
 * original behavior, so this is optional rather than defaulted on the server.
 * UI switches must `default` unknown values to `in_place` rather than claiming
 * isolation or a lock-free share they cannot verify.
 */
export type LocalDirectoryExecutionMode = "in_place" | "worktree" | "shared";

export interface LocalDirectoryResourceRef {
  local_path: string;
  daemon_id: string;
  label?: string;
  execution_mode?: LocalDirectoryExecutionMode;
  /**
   * Symlink-resolved absolute path — the directory's IDENTITY. The server's
   * "one row per directory per machine" rule keys on it, so a symlink and its
   * target cannot be bound twice (DENE-617). Absent on rows written before it
   * existed; the server then falls back to `local_path`.
   */
  real_path?: string;
  /**
   * Normalized identity of the repository this directory holds
   * (`host/owner/name`). Empty or absent means unidentifiable — a plain
   * folder, or a repository with no remote — and those never collide.
   */
  repo_key?: string;
  /**
   * Where parallel mode puts this directory's working copies. Absent means the
   * daemon's default: the repository's sibling `<repo>.multica-worktrees`, on
   * the user's own disk.
   */
  worktree_root?: string;
  /**
   * What the machine holding the directory saw at pick time. Parallel mode
   * requires `true` (a git working tree with at least one commit). `false`
   * and absent both refuse it — a client that cannot look at the disk must
   * not save a mode every task would fail.
   */
  is_git_repo?: boolean;
}

export type ProjectResourceRef =
  | GithubRepoResourceRef
  | LocalDirectoryResourceRef
  | Record<string, unknown>;

export interface ProjectResource {
  id: string;
  project_id: string;
  workspace_id: string;
  resource_type: ProjectResourceType;
  resource_ref: ProjectResourceRef;
  label: string | null;
  position: number;
  created_at: string;
  created_by: string | null;
}

export interface CreateProjectResourceRequest {
  resource_type: ProjectResourceType;
  resource_ref: ProjectResourceRef;
  label?: string;
  position?: number;
}

// resource_type is immutable server-side; partial-update payload mirrors that.
// Sending only the field(s) you want to change is fine — the server merges
// the request body with the existing row, including resource_ref shortcuts.
export interface UpdateProjectResourceRequest {
  resource_ref?: ProjectResourceRef;
  label?: string | null;
  position?: number;
}

export interface ListProjectResourcesResponse {
  resources: ProjectResource[];
  total: number;
}

/** Where a report item stands for the person hearing it (DENE-1667). */
export type ProjectReportPhase = "done" | "in_progress" | "waiting_you";

export interface ProjectReportSourceChat {
  id: string;
  /** Empty when the person can't open that chat. */
  title?: string;
  accessible: boolean;
}

export interface ProjectReportItem {
  issue_id: string;
  identifier: string;
  title: string;
  status: string;
  priority: string;
  gist?: string;
  /** The status before the window; empty for a ticket opened inside it. */
  from_status?: string;
  opened: boolean;
  changed_at: string;
  phase: ProjectReportPhase;
  needs_you: boolean;
  source_chat?: ProjectReportSourceChat;
}

/** A project's news since the person last heard it ("听汇报"). */
export interface ProjectReport {
  project_id: string;
  project_title: string;
  since: string;
  until: string;
  last_heard_at: string | null;
  items: ProjectReportItem[];
  counts: { total: number; done: number; in_progress: number; waiting_you: number };
  actions: ChatQuickAction[];
  marked: boolean;
  inbox_read: number;
}
