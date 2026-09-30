import type { ProjectResource } from "./project";

/**
 * What the server says about one repository (DENE-985).
 *
 * The server decides whether a repository is reachable and what the next
 * step is. Pages render these fields as they arrive; none of them derives a
 * connection state on its own.
 *
 * GET    /api/projects/:id/repos               repositories on one project
 * POST   /api/projects/:id/repos               attach (idempotent)
 * DELETE /api/projects/:id/repos/:resourceId   detach from the project only
 * GET    /api/workspaces/:id/repos/connections every visible repository
 */

/** `app` GitHub App, `token` stored token, `cli` only a local CLI trace, `none` unreachable. */
export type RepoReachMode = "app" | "token" | "cli" | "none";

export type RepoReachNextActionKind =
  | "install_app"
  | "create_app"
  | "add_token"
  | "replace_token"
  | "ask_owner";

export interface RepoReachContact {
  id: string;
  name: string;
  role: string;
}

export interface RepoReachNextAction {
  kind: RepoReachNextActionKind;
  /** With `ask_owner`: the step the owner has to take. */
  for?: RepoReachNextActionKind;
  /** Browser URL, only for steps that need one. */
  url?: string;
  /** Copyable command that does the same step. */
  command?: string;
  /** The repository already works; the step only makes it sturdier. */
  optional?: boolean;
  contacts?: RepoReachContact[];
}

export interface RepoReachProject {
  id: string;
  title: string;
}

export interface RepoReach {
  repo_url: string;
  key: string;
  provider: string;
  mode: RepoReachMode;
  state: "connected" | "cli" | "pending_install" | "disconnected";
  account_login: string;
  link_id: string | null;
  last_lookup: { ok: boolean | null; at: string; error: string };
  webhook: string;
  projects: RepoReachProject[];
  can_configure: boolean;
  /** One sentence for a person. */
  hint: string;
  /** `null` when the repository is usable and nothing is waiting. */
  next_action: RepoReachNextAction | null;
}

export interface ProjectRepoItem {
  resource: ProjectResource;
  repo: RepoReach;
}

export interface ListProjectReposResponse {
  repos: ProjectRepoItem[];
  total: number;
}

export interface AttachProjectRepoRequest {
  repo_url: string;
}

export interface AttachProjectRepoResponse {
  resource: ProjectResource;
  repo: RepoReach;
  /** Newly attached to the project. */
  created: boolean;
  /** Newly registered on the workspace. */
  registered: boolean;
}

/** One workspace repository, as `GET /repos/connections` returns it. */
export interface RepoConnectionCard {
  url: string;
  provider: string;
  mode: RepoReachMode;
  projects: RepoReachProject[];
  can_configure: boolean;
  reach: RepoReach;
}

export interface ListRepoConnectionsResponse {
  repos: RepoConnectionCard[];
}
