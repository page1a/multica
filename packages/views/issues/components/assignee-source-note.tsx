"use client";

import type { Issue } from "@multica/core/types";
import { useActorName } from "@multica/core/workspace/hooks";
import { useT } from "../../i18n";

/**
 * Whose decision the executor is (DENE-1033), one quiet line under the
 * assignee: picked by a person, on a person's quoted words, by an automation,
 * an agent's own idea, or routing. Renders nothing for a ticket with no
 * executor or one that predates the record.
 */
export function AssigneeSourceNote({ issue }: { issue: Issue }) {
  const { t } = useT("issues");
  const { getActorName } = useActorName();
  const source = issue.assignee_source;
  if (!source || !issue.assignee_id) return null;

  const userId = issue.assignee_source_user_id;
  const name = userId ? getActorName("member", userId) : null;

  let label: string;
  switch (source) {
    case "human":
      label = name
        ? t(($) => $.assignee_source.human, { name })
        : t(($) => $.assignee_source.human_unknown);
      break;
    case "quote":
      label = name
        ? t(($) => $.assignee_source.quote, { name })
        : t(($) => $.assignee_source.quote_unknown);
      break;
    case "automation":
      label = t(($) => $.assignee_source.automation);
      break;
    case "agent":
      label = t(($) => $.assignee_source.agent);
      break;
    case "router":
      label = t(($) => $.assignee_source.router);
      break;
    default:
      return null;
  }

  const quote = source === "quote" ? issue.assignee_quote : null;
  return (
    <div
      data-testid="assignee-source-note"
      data-source={source}
      className="flex flex-col gap-0.5 px-2 pb-1 text-caption text-muted-foreground"
    >
      <span>{label}</span>
      {quote ? (
        <span data-testid="assignee-source-quote" className="italic">
          {t(($) => $.assignee_source.quote_text, { quote })}
        </span>
      ) : null}
    </div>
  );
}
