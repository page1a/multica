/**
 * The routing judge's default tier preference.
 *
 * This string is the source the settings page shows when nobody has saved a
 * custom prompt. The server keeps the same bytes in
 * `server/internal/routing/policy.go` (`DefaultPolicyPrompt`); a Go test
 * fails if the two drift. An empty saved value means this text, never "no
 * preference" — a blank prompt would hand the choice back to the model's
 * unspoken habit.
 */
export const DEFAULT_ROUTING_POLICY_PROMPT = `Tier preference:
- Default: medium or strong. Ordinary work lands on one of these.
- Simple, explicit, low-risk work: weak.
- Complex, vague, cross-module, or high-cost work: strong or strongest.

Do not send a clearly simple ticket to the strongest tier. Do not send ordinary work to the weakest tier. Step up to strong or strongest only when the work is complex or high-risk.

You only choose a tier. You do not change status, assignee, reviewer, or any other ticket field, and you do not take an action.

Seat availability and provider quota in this state are observed facts. unknown means the fact is missing: do not treat it as available, unavailable, zero, or exhausted. A seat whose availability is not available or unknown is not a candidate. Provider quota never decides whether a seat is alive.`.trim();

/** Providers whose quota the routing context subscribes to until a workspace replaces the list. */
export const DEFAULT_WATCHED_PROVIDERS = ["claude", "codex", "grok"] as const;
