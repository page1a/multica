"use client";

import React, { useState } from "react";
import { Plus } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Tooltip, TooltipTrigger, TooltipContent } from "@multica/ui/components/ui/tooltip";
import { ActorAvatar } from "../../common/actor-avatar";
import { PickerItem, PropertyPicker } from "../../issues/components/pickers/property-picker";
import { AgentPickerGroups } from "./agent-picker-groups";
import type { ShortcutChord } from "@multica/core/shortcuts";
import type { Agent, Project } from "@multica/core/types";
import { isAgentRuntimeBound } from "@multica/core/agents";
import { toast } from "sonner";
import { ShortcutKeycaps } from "../../common/shortcut-keycaps";
import { useT } from "../../i18n";

const NO_PROJECTS: readonly Project[] = [];

function NewChatTooltipLabel({ shortcut }: { shortcut?: ShortcutChord | null }) {
  const { t } = useT("chat");
  const label = t(($) => $.window.new_chat_tooltip);
  if (!shortcut) return label;
  return (
    <span className="inline-flex items-center gap-1.5">
      {label}
      <ShortcutKeycaps shortcut={shortcut} />
    </span>
  );
}

/**
 * Agent picker: a searchable, grouped (fits the project / My agents / Others) list of agents in a
 * PropertyPicker. The caller supplies the trigger. `currentAgentId` marks one
 * agent with a check — omit it (as "new chat" does) when there is no current
 * selection to highlight.
 */
export function AgentPicker({
  agents,
  userId,
  projects = NO_PROJECTS,
  currentAgentId,
  onSelect,
  trigger,
  triggerRender,
  side = "bottom",
  align = "start",
}: {
  agents: Agent[];
  userId: string | undefined;
  /** Projects the chat is in; their domains decide which agents list first. */
  projects?: readonly Project[];
  currentAgentId?: string;
  onSelect: (agent: Agent) => void;
  trigger: React.ReactNode;
  triggerRender: React.ReactElement;
  side?: "top" | "bottom";
  align?: "start" | "center" | "end";
}) {
  const { t } = useT("chat");
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState("");
  const handlePick = (agent: Agent) => {
    onSelect(agent);
    setOpen(false);
  };

  return (
    <PropertyPicker
      open={open}
      onOpenChange={setOpen}
      width="w-64"
      align={align}
      side={side}
      searchable
      searchPlaceholder={t(($) => $.window.agent_filter_placeholder)}
      onSearchChange={setFilter}
      triggerRender={triggerRender}
      trigger={trigger}
    >
      <AgentPickerGroups
        agents={agents}
        userId={userId}
        projects={projects}
        query={filter}
        renderAgent={(agent) => (
          <AgentPickerItem
            key={agent.id}
            agent={agent}
            isCurrent={agent.id === currentAgentId}
            onSelect={handlePick}
          />
        )}
      />
    </PropertyPicker>
  );
}

export function AgentPickerItem({
  agent,
  isCurrent,
  onSelect,
}: {
  agent: Agent;
  isCurrent: boolean;
  onSelect: (agent: Agent) => void;
}) {
  const { t } = useT("chat");
  const runtimeBound = isAgentRuntimeBound(agent);
  return (
    <PickerItem
      selected={isCurrent}
      disabled={!runtimeBound}
      tooltip={
        runtimeBound ? undefined : t(($) => $.window.agent_needs_runtime_hint)
      }
      onClick={() => onSelect(agent)}
    >
      <ActorAvatar
        actorType="agent"
        actorId={agent.id}
        size="md"
        enableHoverCard
        showStatusDot
      />
      <span className="truncate flex-1">{agent.name}</span>
      {!runtimeBound && (
        <span className="shrink-0 text-micro text-amber-600 dark:text-amber-400">
          {t(($) => $.window.agent_needs_runtime)}
        </span>
      )}
    </PickerItem>
  );
}

/**
 * "New chat" ⊕ button. Per the Chat V2 design, starting a new chat is where the
 * agent is chosen — so this opens an AgentPicker and reports the pick via
 * `onStart`. No agent is pre-checked: a new chat has no "current" agent yet.
 * Shortcuts: with a single available agent it starts immediately (no point
 * showing a one-item menu); with none it still fires `onStart(null)` so the
 * surface shows its no-agent empty state.
 */
export function NewChatButton({
  agents,
  userId,
  projects,
  onStart,
  side = "bottom",
  shortcut = null,
}: {
  agents: Agent[];
  userId: string | undefined;
  /** Projects the new chat will start in; see AgentPicker. */
  projects?: readonly Project[];
  onStart: (agent: Agent | null) => void;
  side?: "top" | "bottom";
  /**
   * Shown on the tooltip only when the click itself starts a chat. With
   * several agents the click opens the picker, and the chord (which skips
   * the picker) would be a lie on this button.
   */
  shortcut?: ShortcutChord | null;
}) {
  const { t } = useT("chat");
  const label = t(($) => $.window.new_chat_tooltip);
  const immediateShortcut = agents.length <= 1 ? shortcut : null;

  if (agents.length <= 1) {
    const only = agents[0] ?? null;
    return (
      <Tooltip>
        <TooltipTrigger
          render={
            <Button
              variant="ghost"
              size="icon-sm"
              className="rounded-full text-muted-foreground"
              aria-label={label}
              onClick={() => {
                if (only && !isAgentRuntimeBound(only)) {
                  toast.error(t(($) => $.input.runtime_required_toast));
                  return;
                }
                onStart(only);
              }}
            />
          }
        >
          <Plus />
        </TooltipTrigger>
        <TooltipContent side={side === "top" ? "top" : "bottom"}>
          <NewChatTooltipLabel shortcut={immediateShortcut} />
        </TooltipContent>
      </Tooltip>
    );
  }

  return (
    <AgentPicker
      agents={agents}
      userId={userId}
      projects={projects}
      onSelect={(agent) => onStart(agent)}
      side={side}
      align="start"
      triggerRender={
        <Button
          variant="ghost"
          size="icon-sm"
          className="rounded-full text-muted-foreground"
          aria-label={label}
        />
      }
      trigger={<Plus />}
    />
  );
}

/** "+" that starts a chat with the agent already in play. The tooltip names the chord. */
export function DirectNewChatButton({
  onClick,
  shortcut,
  side = "bottom",
}: {
  onClick: () => void;
  shortcut: ShortcutChord | null;
  side?: "top" | "bottom";
}) {
  const { t } = useT("chat");
  const label = t(($) => $.window.new_chat_tooltip);
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <Button
            variant="ghost"
            size="icon-sm"
            className="rounded-full text-muted-foreground"
            aria-label={label}
            onClick={onClick}
          />
        }
      >
        <Plus />
      </TooltipTrigger>
      <TooltipContent side={side}>
        <NewChatTooltipLabel shortcut={shortcut} />
      </TooltipContent>
    </Tooltip>
  );
}
