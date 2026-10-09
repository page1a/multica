"use client";

import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import { askOptions, asksOptions } from "@multica/core/asks/queries";
import { useAnswerAsk } from "@multica/core/asks/mutations";
import type { Ask } from "@multica/core/types";
import { AskOptionsCard } from "./ask-options-card";

export function AskPrompt({ askId, initialAsk }: { askId?: string; initialAsk?: Ask }) {
  const wsId = useWorkspaceId();
  const detail = useQuery({ ...askOptions(wsId, askId ?? ""), enabled: !!askId });
  const answer = useAnswerAsk();
  const ask = detail.data ?? initialAsk;
  if (!ask || ask.status !== "open") return null;
  return (
    <AskOptionsCard
      title={ask.title}
      questions={ask.questions}
      mode={ask.mode}
      disabled={answer.isPending}
      onSubmit={async (answers) => {
        try {
          await answer.mutateAsync({ id: ask.id, answers });
        } catch (err) {
          // Same compact copy as the card until the locale bundle adds ask keys.
          toast.error(err instanceof Error && err.message ? err.message : "提交失败，请重试");
        }
      }}
    />
  );
}

export function AskPromptList({ issueId, className = "" }: { issueId?: string; className?: string }) {
  const wsId = useWorkspaceId();
  const asks = useQuery(asksOptions(wsId, "open", issueId));
  if (!asks.data?.length) return null;
  return (
    <div className={`grid gap-3 ${className}`}>
      {asks.data.map((ask) => <AskPrompt key={ask.id} initialAsk={ask} />)}
    </div>
  );
}
