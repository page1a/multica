"use client";

import type { ReactNode } from "react";
import {
  groupAgentsByFit,
  type AgentScene,
  type DomainFit,
} from "@multica/core/agents";
import type { Agent } from "@multica/core/types";
import { PickerSection } from "../../issues/components/pickers/property-picker";
import { useT } from "../../i18n";

/** Group headings, the DENE-1462 wording: 适合 X / 通用 / 其他. */
export function useAgentFitLabel(scene: AgentScene | null): (fit: DomainFit) => string {
  const { t } = useT("common");
  return (fit) => {
    if (fit === "generic") return t(($) => $.agent_fit.generic);
    if (fit === "other") return t(($) => $.agent_fit.other);
    const projects = scene?.projects ?? [];
    return projects.length === 1
      ? t(($) => $.agent_fit.project, { name: projects[0]!.title })
      : t(($) => $.agent_fit.projects);
  };
}

/**
 * The agent block of a picker. With a scene the agents split into 对口 /
 * 通用 / 其他 sections; without one they stay one section under `label`, in
 * the caller's order. Callers filter (search, archived) before passing in.
 */
export function AgentFitSections({
  agents,
  scene,
  label,
  renderAgent,
  renderSection = (key, heading, children) => (
    <PickerSection key={key} label={heading}>
      {children}
    </PickerSection>
  ),
}: {
  agents: Agent[];
  scene: AgentScene | null;
  label: string;
  renderAgent: (agent: Agent) => ReactNode;
  renderSection?: (key: string, label: string, children: ReactNode) => ReactNode;
}) {
  const fitLabel = useAgentFitLabel(scene);
  if (agents.length === 0) return null;
  if (!scene) return <>{renderSection("agents", label, agents.map(renderAgent))}</>;
  return (
    <>
      {groupAgentsByFit(agents, scene).map((g) =>
        renderSection(g.fit, fitLabel(g.fit), g.items.map(renderAgent)),
      )}
    </>
  );
}
