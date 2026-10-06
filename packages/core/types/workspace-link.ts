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
  created_at: string;
  accepted_at: string | null;
}

/** What the caller may do, answered by the server's decision table. */
export interface WorkspaceLinkAbilities {
  create: boolean;
  accept: boolean;
  manage: boolean;
  audit: boolean;
}

/** The workspace a pasted link or slug names (exact match, owner only). */
export interface WorkspaceLinkLookup {
  workspace: WorkspaceLinkWorkspace;
}

export interface ListWorkspaceLinksResponse {
  links: WorkspaceLink[];
  can: WorkspaceLinkAbilities;
}

export interface WorkspaceLinkAuditEntry {
  link_id: string;
  action: "create" | "update_projects" | "accept" | "revoke";
  actor_name: string;
  by_side: WorkspaceLinkSide;
  source_workspace_id: string;
  target_workspace_id: string;
  detail: Record<string, unknown> | null;
  created_at: string;
}

export interface LinkedViewProject {
  id: string;
  title: string;
  icon: string | null;
  status: string;
  done: number;
  total: number;
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
