/**
 * Workspace tier. `guest` is read-only: the server rejects every guest write
 * in one interception layer (DENE-697), so the tier is safe to hand out.
 */
export type MemberRole = "owner" | "admin" | "member" | "guest";

/** Product areas that can be shared independently of any one resource (DENE-699). */
export const MODULE_KEYS = ["issues", "projects", "repos", "runtimes"] as const;
export type ModuleKey = (typeof MODULE_KEYS)[number];

export interface ModuleVisibility {
  key: ModuleKey;
  visibility: "private" | "project" | "workspace";
  project_id: string | null;
  allowed: boolean;
}

export interface ModuleVisibilityList {
  modules: ModuleVisibility[];
}

export interface WorkspaceRepo {
  url: string;
  description?: string;
  /** Resource sharing scope. Older servers omit this field. */
  visibility?: "private" | "project" | "workspace";
  created_by?: string;
  /** First project that contains this repository, when applicable. */
  project_id?: string;
}

export interface Workspace {
  id: string;
  name: string;
  slug: string;
  description: string | null;
  context: string | null;
  settings: Record<string, unknown>;
  repos: WorkspaceRepo[];
  issue_prefix: string;
  avatar_url: string | null;
  created_at: string;
  updated_at: string;
}

export interface WorkspaceNamingOption {
  id: "server_llm" | "runtime" | "rules";
  label: string;
  available: boolean;
  recommended?: boolean;
  reason?: string;
}

export type WorkspaceNamingSource = "server_llm" | "runtime" | "rules";

export interface WorkspaceNaming {
  source: WorkspaceNamingSource;
  options: WorkspaceNamingOption[];
  /** Last 24 hours of naming events. */
  stats: { titled: number; server_llm?: number; runtime: number; rules: number; failed: number };
  /**
   * Whether the selected source is doing its job: ok, degraded (chats arrived
   * but it named none, or it cannot run), or idle (no new chats in 24 hours).
   * Absent on servers that predate the field.
   */
  health?: "ok" | "degraded" | "idle";
  /** Most recent successfully named chat, if any. */
  last?: { title: string; source: WorkspaceNamingSource; created_at: string } | null;
}

/**
 * One MCP server in the workspace's library.
 *
 * This is the WHOLE read shape: the stored configuration is write-only, so
 * urls, commands, headers, and env never leave the server for any role. UI
 * that needs to change an entry sends a replacement rather than editing what
 * it read back.
 *
 * `enabled` is only present on an AGENT's assignment list, where it is the
 * per-agent toggle; the workspace library listing has no binding to report.
 */
export interface WorkspaceMcpServer {
  id: string;
  workspace_id: string;
  name: string;
  transport: string;
  enabled?: boolean;
  /**
   * Live agents the entry is assigned to. Only the workspace library listing
   * carries it, and servers older than this field omit it.
   */
  agent_count?: number;
  created_at: string;
  updated_at: string;
}

export interface Member {
  id: string;
  workspace_id: string;
  user_id: string;
  role: MemberRole;
  created_at: string;
}

export interface User {
  id: string;
  name: string;
  email: string;
  avatar_url: string | null;
  onboarded_at: string | null;
  /**
   * JSONB payload from the server. Typed as `unknown` here so this module
   * stays independent of the questionnaire shape — the onboarding views
   * cast into `Partial<QuestionnaireAnswers>` when reading. Server always
   * returns an object (defaults to `{}`), never null.
   */
  onboarding_questionnaire: Record<string, unknown>;
  /**
   * Legacy column from the removed starter-content dialog. The column is
   * still written to (always 'imported' for new accounts after the
   * mark-onboarded paths run) so older desktop builds — which still render
   * the dialog on NULL — don't show it to anyone created on a newer server.
   * Kept as `string | null` for forward compatibility.
   */
  starter_content_state: string | null;
  /** Preferred UI language. null means "follow client/system". */
  language: string | null;
  /**
   * Free-form self-description (role, stack, preferences). Injected into
   * the agent brief so coding agents have cheap, durable context about
   * who is requesting the work. Server always returns a string —
   * NOT NULL DEFAULT '' at the column level, empty when unset.
   */
  profile_description: string;
  /** Pinned IANA tz; null means "use browser-detected tz at render time". */
  timezone: string | null;
  created_at: string;
  updated_at: string;
}

export interface MemberWithUser {
  id: string;
  workspace_id: string;
  user_id: string;
  /**
   * Member management data — role, email and created_at — is owner-only
   * (DENE-1022). For a non-owner viewer, everyone but themselves comes back
   * with these empty ("" here), keeping only id / name / avatar.
   */
  role: MemberRole | "";
  created_at: string;
  name: string;
  email: string;
  avatar_url: string | null;
  /** Projects this person has joined (DENE-1706). Owner-only like role: a
   *  non-owner gets their own and [] for everyone else. Optional so rows
   *  built locally (invite responses, fixtures) need not carry it. */
  projects?: MemberProjectRef[];
}

export interface MemberProjectRef {
  id: string;
  title: string;
  icon: string | null;
}

export interface Invitation {
  id: string;
  workspace_id: string;
  inviter_id: string;
  invitee_email: string;
  invitee_user_id: string | null;
  role: MemberRole;
  status: "pending" | "accepted" | "declined" | "expired";
  created_at: string;
  updated_at: string;
  expires_at: string;
  inviter_name?: string;
  inviter_email?: string;
  workspace_name?: string;
}

export interface ShareLink {
  id: string;
  workspace_id: string;
  code: string;
  created_by: string;
  role: MemberRole;
  expires_at: string | null;
  max_uses: number | null;
  use_count: number;
  is_active: boolean;
  created_at: string;
  creator_name?: string;
  creator_email?: string;
}

export interface ShareLinkInfo {
  workspace_name: string;
  workspace_slug: string;
  creator_name?: string;
  role: MemberRole;
}
