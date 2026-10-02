export { useModalStore } from "./store";
import { useModalStore } from "./store";

export function openGoalCompletion(data: {
  issueId: string;
  title?: string;
  initialChecks?: string[];
}) {
  useModalStore.getState().open("goal-completion", data);
}
export type { IssueLimitRecoveryReason } from "./store";
