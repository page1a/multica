"use client";

import { useMemo } from "react";
import { agentSceneOf, groupAgentsByFit, type DomainFit } from "@multica/core/agents";
import type { Agent, Project } from "@multica/core/types";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import { PickerEmpty, PickerSection } from "../../issues/components/pickers/property-picker";
import { useAgentFitLabel } from "../../agents/components/agent-fit-sections";
import { useT } from "../../i18n";

export type AgentGroupKind = "mine" | "others" | DomainFit;

export interface AgentGroup {
  kind: AgentGroupKind;
  agents: Agent[];
}

/**
 * Groups the agent list for the "new chat" pickers. One function so the ⊕
 * button and the in-chat dropdown never drift apart.
 *
 * - No project in play: the long-standing "my agents / others" split.
 * - Projects in play: the shared domain-fit groups (DENE-1477, rule in
 *   `@multica/core/agents` domain-fit) — 对口 / 通用 / 其他. They replace
 *   "mine / others": two competing splits in one 256px popover read as noise.
 *
 * Order inside a group follows the input order. Search runs over every agent,
 * hits are then grouped the same way; empty groups are dropped.
 */
export function groupAgentsForPicker({
  agents,
  userId,
  projects,
  query = "",
}: {
  agents: Agent[];
  userId: string | undefined;
  projects: readonly Project[];
  query?: string;
}): AgentGroup[] {
  const q = query.trim().toLowerCase();
  const hits = q
    ? agents.filter((a) => a.name.toLowerCase().includes(q) || matchesPinyin(a.name, q))
    : agents;

  const scene = agentSceneOf(projects);
  if (scene) {
    return groupAgentsByFit(hits, scene).map((g) => ({ kind: g.fit, agents: g.items }));
  }
  return [
    { kind: "mine" as const, agents: hits.filter((a) => a.owner_id === userId) },
    { kind: "others" as const, agents: hits.filter((a) => a.owner_id !== userId) },
  ].filter((g) => g.agents.length > 0);
}

/** Sections for the picker body: grouped, headed by one word, empty-aware. */
export function AgentPickerGroups({
  agents,
  userId,
  projects,
  query,
  renderAgent,
}: {
  agents: Agent[];
  userId: string | undefined;
  projects: readonly Project[];
  query: string;
  renderAgent: (agent: Agent) => React.ReactNode;
}) {
  const { t } = useT("chat");
  const groups = useMemo(
    () => groupAgentsForPicker({ agents, userId, projects, query }),
    [agents, userId, projects, query],
  );
  const fitLabel = useAgentFitLabel(agentSceneOf(projects));
  if (groups.length === 0) return <PickerEmpty />;

  const label = (kind: AgentGroupKind) =>
    kind === "mine"
      ? t(($) => $.window.my_agents)
      : kind === "others"
        ? t(($) => $.window.others)
        : fitLabel(kind);
  return (
    <>
      {groups.map((g) => (
        <PickerSection key={g.kind} label={label(g.kind)}>
          {g.agents.map(renderAgent)}
        </PickerSection>
      ))}
    </>
  );
}
