// Cross-workspace read-only links (DENE-1225). Shapes mirror
// server/internal/workspacelink; the server decides every permission.

export type WorkspaceLinkSide = "source" | "viewer";
export type WorkspaceLinkStatus = "pending" | "active";

export interface WorkspaceLinkWorkspace {
  name: string;
  slug: string;
  avatar_url: string | null;
}

export interface WorkspaceLinkProject {
  id: string;
  title: string;
  icon: string | null;
}

export interface WorkspaceLink {
  id: string;
  side: WorkspaceLinkSide;
  status: WorkspaceLinkStatus;
  source: WorkspaceLinkWorkspace;
  target: WorkspaceLinkWorkspace;
  /** Empty unless the caller is the source side's owner/admin. */
  projects: WorkspaceLinkProject[];
  /** The viewer's agents may manage the source's issues and autopilots for
   *  their run's originator (DENE-1663). */
  managed?: boolean;
  /** Whether the caller may switch `managed`; the list answers it. */
  can_set_managed?: boolean;
  created_at: string;
  accepted_at: string | null;
}

/** What the caller may do, answered by the server's decision table. */
export interface WorkspaceLinkAbilities {
  create: boolean;
  accept: boolean;
  /** May pull another workspace's projects in; the lookup adds whether the
   * caller also owns that workspace. */
  pull: boolean;
  manage: boolean;
  audit: boolean;
}

/** The workspace a pasted link or slug names (exact match, owner/admin). */
export interface WorkspaceLinkLookup {
  workspace: WorkspaceLinkWorkspace;
  /** Whether the caller may pull that workspace's projects in (owns it), and
   * its shareable projects when so. */
  pull?: { allowed: boolean; projects: WorkspaceLinkProject[] };
}

/** "offer" shares this workspace's projects; "pull" reads the other's. */
export type WorkspaceLinkDirection = "offer" | "pull";

export interface ListWorkspaceLinksResponse {
  links: WorkspaceLink[];
  can: WorkspaceLinkAbilities;
}

export interface WorkspaceLinkAuditEntry {
  link_id: string;
  action: "create" | "update_projects" | "accept" | "revoke" | "set_managed" | "managed_write";
  actor_name: string;
  by_side: WorkspaceLinkSide;
  source_workspace_id: string;
  target_workspace_id: string;
  detail: Record<string, unknown> | null;
  created_at: string;
}

/** A shared project's resource: a repository URL or a directory path. */
export interface LinkedResource {
  type: string;
  label: string | null;
  url?: string;
  path?: string;
}

export interface LinkedViewProject {
  id: string;
  title: string;
  icon: string | null;
  status: string;
  done: number;
  total: number;
  /** Sharing a project shares its context (DENE-1643); optional for older servers. */
  description?: string;
  resources?: LinkedResource[];
  memory_line?: string;
}

/** A shared project a chat may attach as a read-only reference (DENE-1643). */
export interface LinkedProjectOption {
  link_id: string;
  id: string;
  title: string;
  icon: string | null;
  source: { name: string; avatar_url: string | null };
}

/** Names one shared project through the link it arrives on. */
export interface ChatLinkedProjectRef {
  link_id: string;
  project_id: string;
}

/** A read-only linked project attached to a chat. `available` turns false
 *  once the link is revoked or the project unticked; the agent stops
 *  receiving it then, and the chat shows it as stale. */
export interface ChatLinkedProject extends ChatLinkedProjectRef {
  title: string;
  icon: string | null;
  source_name: string;
  available: boolean;
}

export interface LinkedViewIssue {
  identifier: string;
  title: string;
  status: string;
  priority: string;
  project_id: string;
  assignee_name: string | null;
  assignee_avatar_url: string | null;
  due_date: string | null;
  updated_at: string;
}

export interface LinkedViewStatus {
  key: string;
  name: string;
  category: string;
}

/** The only thing a viewer ever receives about the source workspace. */
export interface LinkedView {
  link_id: string;
  source: { name: string; avatar_url: string | null };
  projects: LinkedViewProject[];
  issues: LinkedViewIssue[];
  next_cursor: string | null;
  statuses: LinkedViewStatus[];
}

export interface LinkedViewParams {
  projectId?: string;
  cursor?: string;
  limit?: number;
}
