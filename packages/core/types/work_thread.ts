export interface WorkThreadTurn {
  id: string;
  status: string;
  session_id?: string;
  started_at?: string;
}

export interface WorkThreadInput {
  id: string;
  status: string;
  summary?: string;
  created_at: string;
}

export interface WorkThreadSnapshot {
  thread_id: string;
  agent_id: string;
  issue_id?: string;
  chat_session_id?: string;
  continuous: boolean;
  current_turn?: WorkThreadTurn;
  last_turn?: WorkThreadTurn;
  state: "idle" | "queued" | "active" | "completed" | "resumable" | "rebuild_required" | string;
  can_resume: boolean;
  session_id?: string;
  queued_inputs: WorkThreadInput[];
  queue_truncated: boolean;
  context: {
    generation: number;
    message_limit: number;
    token_budget: number;
    summary_available: boolean;
    break_reason?: string;
  };
  updated_at: string;
}
