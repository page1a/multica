"use client";

import { Settings2 } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { selectConversationStarters } from "@multica/core/agents";
import type { Agent } from "@multica/core/types";
import { ActorAvatar } from "../../common/actor-avatar";
import { useT } from "../../i18n";
import { GoalFlowSteps } from "../../issues/draft/goal-flow-steps";
import { AppLink } from "../../navigation";
import {
  ConversationStarterList,
  useFallbackConversationStarters,
} from "./conversation-starter-list";

/** Empty compose placeholder shown before the first user message. */
export function EmptyState({
  agent,
  hasSessions = true,
  onPickPrompt,
  customizeHref = null,
  onStartAlignment,
}: {
  agent: Agent | null;
  hasSessions?: boolean;
  onPickPrompt: (prompt: string) => void;
  onStartAlignment?: () => void;
  /**
   * Where "customize" sends this viewer, or `null` to hide the affordance.
   * The container resolves it: only someone who may edit THIS agent on a
   * backend that persists conversation starters gets a link. Keeping it a prop
   * leaves this component presentational — and keeps the link out of the DOM
   * entirely for readers, rather than rendering a disabled tease.
   */
  customizeHref?: string | null;
}) {
  const { t } = useT("chat");
  const description = agent?.description?.trim();
  const fallbackStarters = useFallbackConversationStarters();
  const { starters } = selectConversationStarters(
    agent?.conversation_starters,
    fallbackStarters,
  );
  // Every chat can walk its person through their inbox (DENE-975); it is not
  // one of the agent's own starters, so it sits under them whatever they are.
  const withInbox = [
    ...starters,
    {
      label: t(($) => $.conversation_starters.inbox.label),
      prompt: t(($) => $.conversation_starters.inbox.prompt),
    },
  ];

  return (
    <div className="flex min-h-0 flex-1 flex-col items-center justify-center-safe gap-5 overflow-y-auto px-6 py-8">
      {agent && (
        <ActorAvatar
          actorType="agent"
          actorId={agent.id}
          size="2xl"
          className="ring-1 ring-inset ring-border"
        />
      )}
      <div className="max-w-sm space-y-1 text-center">
        <h3 className="text-title-sm font-semibold">
          {agent
            ? t(($) => $.empty_state.chat_with_named, { name: agent.name })
            : t(($) => $.empty_state.first_time_title)}
        </h3>
        {description && (
          <p className="text-body text-muted-foreground">{description}</p>
        )}
        {!hasSessions && (
          <p className="text-body text-muted-foreground">
            {t(($) => $.empty_state.first_time_actions)}
          </p>
        )}
      </div>
      {agent ? (
        <div
          className="w-full max-w-sm space-y-2"
          aria-label={t(($) => $.conversation_starters.aria_label)}
        >
          <ConversationStarterList starters={withInbox} onPick={onPickPrompt} />
          {customizeHref ? (
            <div className="flex justify-center pt-1">
              <AppLink
                href={customizeHref}
                className="inline-flex items-center gap-1.5 text-caption text-muted-foreground transition-colors hover:text-foreground"
              >
                <Settings2 className="size-3.5" aria-hidden="true" />
                {t(($) => $.conversation_starters.customize)}
              </AppLink>
            </div>
          ) : null}
        </div>
      ) : null}
      {agent && onStartAlignment ? (
        <div className="w-full max-w-sm rounded-lg border border-border/80 bg-muted/20 px-3 py-3 text-left">
          <GoalFlowSteps active="chat" />
          <div className="mt-2 flex items-center justify-between gap-3">
            <p className="text-caption text-muted-foreground">
              {t(($) => $.goal_flow.hint)}
            </p>
            <Button variant="outline" size="sm" onClick={onStartAlignment}>
              {t(($) => $.goal_flow.action)}
            </Button>
          </div>
        </div>
      ) : null}
    </div>
  );
}
