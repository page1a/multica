/**
 * The issue state card (DENE-1328): a few hundred characters the server
 * derives from what the issue already records. Only `decisions` is stored on
 * its own; everything else is read from the close record, handoff, goal and
 * comments.
 */
export interface StateCardCheck {
  description: string;
  status: string;
}

export interface StateCardDecision {
  id: string;
  text: string;
  /** close, handoff or manual. */
  source: string;
  author_type: string;
  author_id?: string;
  created_at: string;
  updated_at: string;
}

export interface StateCardNow {
  status: string;
  closed: boolean;
  conclusion?: string;
  close_status?: string;
  next_owner_type?: string;
  next_owner_id?: string;
  waiting_on?: string;
  wait_condition?: string;
  wake_at?: string;
  needs_human?: string;
  closed_at?: string;
  superseded?: boolean;
  /** The status moved after the close: the record is history, its wait dropped. */
  stale?: boolean;
}

export interface StateCardBaton {
  /** close or handoff, whichever is newer. */
  kind: string;
  summary?: string;
  by_type?: string;
  by_id?: string;
  to?: string;
  at?: string;
  comment_id?: string;
}

export interface StateCardThread {
  thread_id: string;
  title: string;
  author_type: string;
  author_id?: string;
  new_count: number;
  last_at: string;
}

export interface StateCardChanges {
  /** last_run, last_comment, explicit or none. */
  anchor: string;
  since?: string;
  threads: StateCardThread[];
  more?: number;
}

/** The chat the issue was opened from and what was asked there (DENE-1672). */
export interface StateCardSource {
  chat_session_id: string;
  chat_title: string;
  message_id?: string;
  excerpt?: string;
}

/** A sub-task's receipt (DENE-1679): its conclusion, pull requests and knowledge. */
export interface StateCardChildReceipt {
  issue_id: string;
  identifier: string;
  title: string;
  /** Canonical status. */
  status: string;
  conclusion?: string;
  summary?: string;
  knowledge?: string;
  pull_requests: { number: number; url: string; state: string }[];
}

export interface IssueStateCard {
  issue_id: string;
  identifier: string;
  goal: { title: string; goal_status?: string; finish_line?: StateCardCheck[] };
  decisions: StateCardDecision[];
  now: StateCardNow;
  baton?: StateCardBaton | null;
  changes: StateCardChanges;
  source?: StateCardSource | null;
  /** Sub-task receipts the viewer can see. */
  children?: StateCardChildReceipt[];
  /** The card as the CLI prints it. */
  text: string;
}
