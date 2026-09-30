/**
 * Settings → 代码仓库 / 连接 catalog (DENE-961).
 *
 * One link covers an account (`github.com/acme`) or a whole host
 * (`gitlab.corp.local` → owner `*`). Repositories match a link; they do not
 * each store a token. Personal links apply only to repositories that member
 * added — the server enforces that. Local `gh` / `glab` is a fallback, not a
 * stored link: a link whose server cannot reach the host reports `health:
 * "cli"`.
 *
 * DENE-966 owns the handlers. The settings UI speaks only these routes:
 *
 * GET    /api/workspaces/:id/repo-links
 * POST   /api/workspaces/:id/repo-links          upsert for this caller
 * POST   /api/workspaces/:id/repo-links/:linkId/test
 * DELETE /api/workspaces/:id/repo-links/:linkId
 * PUT    /api/workspaces/:id/repo-bindings       pin or clear
 * POST   /api/workspaces/:id/repo-bindings/test
 *
 * Failure copy shares the delivery-lookup shape: one `hint` sentence and an
 * optional `next_command`. The UI shows that sentence only where the person
 * has to act (a test result, or a project with no matching link).
 */

export type RepoLinkKind =
  | "github_app"
  | "github_token"
  | "gitlab_token"
  | "forgejo_token"
  | "gitea_token";

/** Workspace-wide, or limited to repositories this member added. */
export type RepoLinkVisibility = "workspace" | "personal";

/**
 * Whether the server can use this link for a delivery lookup.
 * `ok` the provider answered. `cli` the server cannot reach it, so lookup
 * falls back to the machine's `gh` / `glab`. `pending_install` a GitHub App
 * exists but the install step is unfinished.
 */
export type RepoLinkHealth = "ok" | "cli" | "pending_install";

/** What a repository row shows. `disconnected` means no link covers it. */
export type RepoConnectionState =
  | "connected"
  | "cli"
  | "pending_install"
  | "disconnected";

export interface RepoLink {
  id: string;
  kind: RepoLinkKind;
  /** Lowercased hostname, e.g. `github.com`. */
  host: string;
  /** Account or org. `*` covers every account on the host. */
  owner: string;
  visibility: RepoLinkVisibility;
  /** Set when `visibility` is `personal`. */
  owner_user_id?: string | null;
  health: RepoLinkHealth;
  /** One sentence. Present when the person has to do something. */
  hint?: string | null;
  /** Next command, when one would unblock the lookup. */
  next_command?: string | null;
  last_query_ok?: boolean | null;
  last_query_at?: string | null;
  /** Webhook events are arriving. Omitted when this kind has no webhook. */
  webhook_ok?: boolean | null;
  account_login?: string | null;
  /** GitHub App that still needs the browser install step. */
  install_url?: string | null;
  can_manage: boolean;
}

export interface RepoSourceProject {
  id: string;
  title: string;
}

export interface RepoBinding {
  /** Workspace repository URL, as stored. */
  repo_url: string;
  source_projects?: RepoSourceProject[];
  /** Empty or omitted = automatic. */
  pinned_link_id?: string | null;
  resolved_link_id?: string | null;
  state: RepoConnectionState;
  /** One sentence. Only when `state` is not `connected`. */
  hint?: string | null;
  next_command?: string | null;
  /** Caller may pin or clear the link on this repository. */
  can_configure: boolean;
}

export interface ListRepoLinksResponse {
  links: RepoLink[];
  bindings: RepoBinding[];
  can_add_workspace: boolean;
  can_add_personal: boolean;
}

export interface CreateRepoLinkRequest {
  kind: RepoLinkKind;
  /**
   * `host/owner`, `host/owner/*`, `host/*`, or a bare host (whole instance).
   * An absolute instance URL is accepted for self-hosted Git.
   */
  scope: string;
  access_token?: string;
  visibility: RepoLinkVisibility;
}

export interface CreateRepoLinkResponse {
  link: RepoLink;
  /** One-time webhook secret. Not readable again. */
  webhook_secret?: string;
  webhook_url?: string;
  install_url?: string;
}

export interface TestRepoLinkResponse {
  link: RepoLink;
  ok: boolean;
  hint?: string | null;
  next_command?: string | null;
}

export interface PinRepoBindingRequest {
  repo_url: string;
  /** `null` clears the pin and returns to automatic. */
  pinned_link_id: string | null;
}

export interface TestRepoBindingRequest {
  repo_url: string;
}

export interface TestRepoBindingResponse {
  binding: RepoBinding;
  ok: boolean;
  hint?: string | null;
  next_command?: string | null;
}
