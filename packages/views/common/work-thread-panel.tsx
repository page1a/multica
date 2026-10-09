"use client";

import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Button } from "@multica/ui/components/ui/button";
import { Loader2, Pause, Play, ArrowUp } from "lucide-react";
import { api } from "@multica/core/api";
import type { WorkThreadSnapshot } from "@multica/core/types/work_thread";
import { useState } from "react";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";

interface Props {
  kind: "issue" | "chat";
  id: string;
  /** Outer spacing; applied only when the panel has something to show. */
  className?: string;
}

export function WorkThreadPanel({ kind, id, className }: Props) {
  const { t } = useT("common");
  const queryClient = useQueryClient();
  const [busy, setBusy] = useState(false);
  const queryKey = ["work-thread", kind, id];
  const { data: snapshot } = useQuery<WorkThreadSnapshot | null>({
    queryKey,
    queryFn: () => kind === "issue" ? api.getIssueWorkThread(id) : api.getChatWorkThread(id),
    enabled: !!id,
    refetchInterval: 5_000,
  });
  const queued = snapshot?.queued_inputs.length ?? 0;
  // Only show up when there is something to do here. Queuing new input
  // belongs to the comment / chat composer, which carries the actual text.
  if (!snapshot || (!snapshot.current_turn && !snapshot.can_resume && !queued)) return null;

  const action = async (name: "continue" | "interrupt" | "prioritize", taskId?: string) => {
    setBusy(true);
    try {
      if (kind === "issue") await api.issueWorkThreadAction(id, name, undefined, taskId);
      else if (name !== "prioritize") await api.chatWorkThreadAction(id, name);
      await queryClient.invalidateQueries({ queryKey });
    } finally {
      setBusy(false);
    }
  };
  const labels = [
    snapshot.can_resume ? t(($) => $.work_thread.resumable) : null,
    queued ? t(($) => $.work_thread.queued_count, { count: queued }) : null,
  ].filter(Boolean);
  return (
    <div className={cn("flex shrink-0 items-center gap-2 whitespace-nowrap rounded-md border bg-muted/30 px-2 py-1", className)} data-testid={`work-thread-${kind}`}>
      {labels.length > 0 && (
        <span className="text-caption text-muted-foreground" title={snapshot.thread_id}>
          {labels.join(" · ")}
        </span>
      )}
      {snapshot.can_resume && (
        <Button size="sm" variant="ghost" disabled={busy} onClick={() => action("continue")} aria-label={t(($) => $.work_thread.continue_aria)}>
          <Play className="mr-1 h-3.5 w-3.5" />
          {t(($) => $.work_thread.continue)}
        </Button>
      )}
      {snapshot.current_turn && (
        <Button size="sm" variant="ghost" disabled={busy} onClick={() => action("interrupt")} aria-label={t(($) => $.work_thread.interrupt_aria)}>
          <Pause className="mr-1 h-3.5 w-3.5" />
          {t(($) => $.work_thread.interrupt)}
        </Button>
      )}
      {kind === "issue" && snapshot.queued_inputs.map((input) => (
        <Button key={input.id} size="sm" variant="ghost" disabled={busy} onClick={() => action("prioritize", input.id)} aria-label={t(($) => $.work_thread.prioritize_aria, { id: input.id })} title={input.summary}>
          <ArrowUp className="mr-1 h-3.5 w-3.5" />
          {t(($) => $.work_thread.insert)}
        </Button>
      ))}
      {busy && <Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground" />}
    </div>
  );
}
