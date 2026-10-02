import { z } from "zod";

/** Lifecycle state for the completion line attached to an issue. */
export type IssueGoalStatus = "draft" | "active" | "locked" | "stopped" | "achieved" | (string & {});

export interface IssueGoalEvidence {
  id?: string;
  label?: string;
  kind?: string;
  url?: string;
  detail?: string;
  [key: string]: unknown;
}

export interface IssueGoalCheck {
  id: string;
  title?: string;
  description?: string | null;
  method?: string | null;
  status?: string | null;
  passed?: boolean;
  evidence?: IssueGoalEvidence[];
}

export interface IssueGoalBudget {
  tokens?: number | null;
  runs?: number | null;
  duration_seconds?: number | null;
  duration_ms?: number | null;
  [key: string]: unknown;
}

export interface IssueGoal {
  id: string;
  issue_id: string;
  status: IssueGoalStatus;
  checks: IssueGoalCheck[];
  budget?: IssueGoalBudget | null;
  usage?: IssueGoalBudget | null;
  round?: number | null;
  evidence?: IssueGoalEvidence[];
  no_progress_rounds?: number | null;
  max_no_progress_rounds?: number | null;
  budget_warning_at?: string | null;
  last_continuation_task_id?: string | null;
  next_action?: string | null;
  [key: string]: unknown;
}

export interface IssueGoalCheckInput {
  description: string;
  method?: "command" | "test" | "screenshot" | "acceptance";
}

export interface CreateIssueGoalInput {
  checks: IssueGoalCheckInput[];
  budget?: {
    token_limit?: number;
    run_limit?: number;
    duration_seconds?: number;
  };
}

/** A tolerant response schema keeps old servers (which may return null) readable. */
export const IssueGoalSchema = z.object({
  id: z.string(),
  issue_id: z.string(),
  status: z.string(),
  checks: z.array(z.object({
    id: z.string(),
    title: z.string().optional(),
    description: z.string().nullable().optional(),
    method: z.string().nullable().optional(),
    status: z.string().nullable().optional(),
    passed: z.boolean().optional(),
    evidence: z.array(z.record(z.string(), z.unknown())).optional(),
  }).passthrough()),
  budget: z.record(z.string(), z.unknown()).nullable().optional(),
  usage: z.record(z.string(), z.unknown()).nullable().optional(),
  round: z.number().nullable().optional(),
  evidence: z.array(z.record(z.string(), z.unknown())).optional(),
  no_progress_rounds: z.number().nullable().optional(),
  max_no_progress_rounds: z.number().nullable().optional(),
  budget_warning_at: z.string().nullable().optional(),
  last_continuation_task_id: z.string().nullable().optional(),
  next_action: z.string().nullable().optional(),
}).passthrough() satisfies z.ZodType<IssueGoal>;
