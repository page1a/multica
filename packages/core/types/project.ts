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
  issue_count: number;
  done_count: number;
  resource_count: number;
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
