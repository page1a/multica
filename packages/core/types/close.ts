// KnowledgeAuditChange is one project-memory checklist slot this close wrote.
export interface KnowledgeAuditChange {
  location: string;
  /** How the change treats existing entries (DENE-1680); required on the boss layer. */
  action?: "new" | "update" | "merge" | "supersede";
  /** The existing entry an update / merge / supersede names. */
  entry?: string;
  summary: string;
  /** The delivered files that wrote the slot (DENE-1661). */
  files?: string[];
}

// KnowledgeAudit is either an explicit "nothing qualified" declaration or one
// or more checklist changes. The server rejects a close that sends both, or
// neither.
export interface KnowledgeAudit {
  none?: boolean;
  changes?: KnowledgeAuditChange[];
  /** True when git could not say what the close delivered. */
  unverified?: boolean;
}

// Outcome is the status the close writes. done / in_review / blocked /
// cancelled are the original four; backlog, todo and in_progress (DENE-1002)
// deliberately leave the ticket open while still recording why.
export type CloseOutcome =
  | "done"
  | "in_review"
  | "blocked"
  | "cancelled"
  | "backlog"
  | "todo"
  | "in_progress";

export interface CloseIssueRequest {
  outcome: CloseOutcome;
  evidence: string;
  summary?: string;
  parent_id?: string;
  blocked_by?: string;
  wake_at?: string;
  wait_condition?: string;
  wait_probe?: string;
  wait_timeout?: string;
  needs_human?: string;
  no_code_reason?: string;
  verdict?: string;
  pr_url?: string;
  knowledge_audit?: KnowledgeAudit;
}

export interface CloseIssueResponse {
  status: string;
  prev_status: string;
  status_changed: boolean;
  close?: Record<string, string>;
  knowledge_audit?: KnowledgeAudit;
  merged: boolean;
  pr_url?: string;
  woken?: string[];
  warnings?: string[];
}
