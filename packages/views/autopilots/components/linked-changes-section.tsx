"use client";

import { useQuery } from "@tanstack/react-query";
import { autopilotLinkedChangesOptions } from "@multica/core/autopilots/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import type { AutopilotLinkedChange } from "@multica/core/types";
import { formatInTimeZone } from "../../common/format-in-time-zone";
import { useT } from "../../i18n";

type RouteKey = "create" | "update" | "delete" | "run" | "trigger_add" | "trigger_update" | "trigger_delete";
const ROUTES: readonly RouteKey[] = ["create", "update", "delete", "run", "trigger_add", "trigger_update", "trigger_delete"];

function routeKey(route: string): RouteKey | "other" {
  const key = route.replace(/^autopilot\./, "");
  return (ROUTES as readonly string[]).includes(key) ? (key as RouteKey) : "other";
}

/**
 * Writes an agent of a linked workspace made to this autopilot on a managed
 * link (DENE-1663): who started the run, through which workspace, which
 * agent, and what it did. Takes no space when there are none.
 */
export function LinkedChangesSection({ autopilotId }: { autopilotId: string }) {
  const { t } = useT("autopilots");
  const wsId = useWorkspaceId();
  const { data: changes = [] } = useQuery(autopilotLinkedChangesOptions(wsId, autopilotId));
  if (changes.length === 0) return null;
  return (
    <section className="space-y-3" data-testid="autopilot-linked-changes">
      <h2 className="text-body font-medium text-muted-foreground uppercase tracking-wider">
        {t(($) => $.detail.section_linked_changes)}
      </h2>
      <ul className="divide-y rounded-md border">
        {changes.map((change) => (
          <LinkedChangeRow key={change.id} change={change} />
        ))}
      </ul>
    </section>
  );
}

function LinkedChangeRow({ change }: { change: AutopilotLinkedChange }) {
  const { t, i18n } = useT("autopilots");
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-0.5 px-4 py-2.5 text-body">
      {/* Narrow screens wrap the names onto their own line instead of
          cutting them; the action and time follow underneath. */}
      <span className="w-full min-w-0 break-words sm:w-auto sm:flex-1 sm:truncate">
        {t(($) => $.detail.linked_change_by, {
          actor: change.actor_name,
          via: change.via_workspace_name,
          agent: change.agent_name,
        })}
      </span>
      <span className="shrink-0 text-caption text-muted-foreground">
        {t(($) => $.detail.linked_change_route[routeKey(change.route)])}
      </span>
      <span className="shrink-0 text-right text-caption text-muted-foreground tabular-nums">
        {formatInTimeZone(change.created_at, undefined, i18n.language)}
      </span>
    </li>
  );
}
