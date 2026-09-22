/**
 * Workspace guardrails for delegated agent work, stored in `workspace.settings`.
 * Zero means unlimited for both fields; the server reads the same keys
 * (`GetWorkspaceAgentChainBudget`, `FailTasksOverWorkspaceTimeLimit`).
 */
export interface AutomationLimits {
  agent_chain_budget: number;
  agent_task_timeout_minutes: number;
}

/** What the server applies when a workspace never saved the setting. */
export const DEFAULT_AUTOMATION_LIMITS: AutomationLimits = {
  agent_chain_budget: 30,
  agent_task_timeout_minutes: 0,
};

/** Value restored when the user turns "unlimited" off. */
export const AUTOMATION_LIMIT_PRESETS: AutomationLimits = {
  agent_chain_budget: 30,
  agent_task_timeout_minutes: 120,
};

// The server ignores minute values longer than six digits.
const MAX_LIMIT = 999_999;

function readLimit(value: unknown, fallback: number): number {
  return typeof value === "number" &&
    Number.isInteger(value) &&
    value >= 0 &&
    value <= MAX_LIMIT
    ? value
    : fallback;
}

export function parseAutomationLimits(
  settings: Record<string, unknown> | null | undefined,
): AutomationLimits {
  return {
    agent_chain_budget: readLimit(
      settings?.agent_chain_budget,
      DEFAULT_AUTOMATION_LIMITS.agent_chain_budget,
    ),
    agent_task_timeout_minutes: readLimit(
      settings?.agent_task_timeout_minutes,
      DEFAULT_AUTOMATION_LIMITS.agent_task_timeout_minutes,
    ),
  };
}

/**
 * Turns the text of a limit input into a stored value. Empty or junk input
 * keeps the previous value rather than silently becoming 0, because 0 means
 * unlimited and must only ever be chosen through its own switch.
 */
export function parseLimitInput(text: string, previous: number): number {
  const parsed = Number.parseInt(text, 10);
  if (!Number.isFinite(parsed) || parsed < 1) return previous;
  return Math.min(parsed, MAX_LIMIT);
}

export function automationLimitsEqual(
  left: AutomationLimits,
  right: AutomationLimits,
): boolean {
  return (
    left.agent_chain_budget === right.agent_chain_budget &&
    left.agent_task_timeout_minutes === right.agent_task_timeout_minutes
  );
}
