"use client";

import type { CodeDecision } from "@multica/core/types";
import { useT } from "../../i18n";
import { viewOfCodeDecision } from "./code-decision-view";

/**
 * The server's answer to "where do tasks on this machine run?", rendered
 * without re-deciding it. An unresolvable answer shows the code the server
 * sent, not a guess about which folder would have been chosen instead.
 */
export function CodeDecisionBanner({ decision }: { decision: CodeDecision }) {
  const { t } = useT("projects");
  const view = viewOfCodeDecision(decision);

  let text: string;
  switch (view.kind) {
    case "place":
      text = t(($) => $.resources.code_decision_in_place, { name: view.name });
      break;
    case "worktree":
      text = t(($) => $.resources.code_decision_worktree, { root: view.root });
      break;
    case "remote":
      text = t(($) => $.resources.code_decision_remote, { url: view.url });
      break;
    case "scratch":
      text = t(($) => $.resources.code_decision_scratch);
      break;
    case "unresolvable":
      return (
        <div role="status" className="px-2 py-1 text-micro text-destructive">
          <div className="font-mono">{view.code}</div>
          {view.reason && <div className="mt-0.5 text-muted-foreground">{view.reason}</div>}
        </div>
      );
    default:
      text = view.kindName;
  }

  return (
    <p role="status" className="px-2 py-1 text-micro text-muted-foreground break-all">
      {text}
    </p>
  );
}
