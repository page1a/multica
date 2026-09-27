// Server projections the one-row-per-issue inbox reads (DENE-882). Both are
// produced by stage-1 work: open calls on a person (DENE-880) and each issue's
// latest parking record (DENE-881).

/** Who holds the next move on a parked issue. `issue` carries an identifier. */
export interface ParkingOwner {
  type: "agent" | "member" | "squad" | "issue" | "none" | "";
  id: string;
}

export interface ParkingEvent {
  at: string;
  kind:
    | "run_started"
    | "run_completed"
    | "run_failed"
    | "run_cancelled"
    | "pr_linked"
    | "pr_merged"
    | "rejected"
    | "closed"
    | (string & {});
  detail?: string;
}

export type ParkingCategory =
  | "running"
  | "done"
  | "awaiting_review"
  | "blocked"
  | "waiting_person"
  | "delegated"
  | "idle"
  | "stalled_delivery"
  | "stalled_unclosed"
  | "stalled_reply_unclosed";

/** GET /api/issues/parking — one issue's latest parking record. */
export interface ParkingRecord {
  issue_id: string;
  identifier: string;
  number: number;
  title: string;
  parent_issue_id: string | null;
  /** The issue's live status; differs from recorded_status after a later manual move. */
  current_status: string;
  recorded_status: string;
  state: "running" | "parked";
  category: ParkingCategory | (string & {});
  stuck_kind: string;
  unexplained: boolean;
  /** The agent's own words, a model sentence, or fixed wording — see summary_source. */
  summary: string;
  summary_source: "close" | "agent" | "model" | "template" | (string & {});
  next_owner: ParkingOwner;
  task_id: string | null;
  timeline: ParkingEvent[];
  evaluated_at: string;
}

export interface ParkingRecordsResponse {
  records: ParkingRecord[];
}

/** GET /api/summons/waiting — one unanswered call on the current user. */
export interface WaitingSummon {
  id: string;
  issue_id: string;
  identifier: string;
  issue_title: string;
  issue_status: string;
  issue_priority: string;
  caller_type: "member" | "agent" | "system" | (string & {});
  caller_id: string | null;
  caller_name: string;
  source:
    | "manual"
    | "needs_human"
    | "routing"
    | "patrol"
    | "time_limit"
    | "quota_relay"
    | "mention"
    | (string & {});
  reason: string;
  comment_id: string | null;
  inbox_item_id: string | null;
  created_at: string;
}

/**
 * GET /api/inbox/unread-issues — one row per issue the current user has
 * unread inbox rows on (DENE-901). `held_count` of them hang on an open call
 * and stay unread until the call is answered or closed.
 */
export interface UnreadInboxIssue {
  issue_id: string;
  identifier: string;
  title: string;
  status: string;
  parent_issue_id: string | null;
  unread_count: number;
  held_count: number;
  latest_at: string;
}
