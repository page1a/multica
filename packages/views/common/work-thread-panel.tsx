"use client";

import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Button } from "@multica/ui/components/ui/button";
import { Loader2, Pause, Play, ListPlus, ArrowUp } from "lucide-react";
import { api } from "@multica/core/api";
import type { WorkThreadSnapshot } from "@multica/core/types/work_thread";
import { useState } from "react";
import { useT } from "../i18n";

interface Props {
  kind: "issue" | "chat";
  id: string;
}

export function WorkThreadPanel({ kind, id }: Props) {
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
  if (!snapshot) return null;

  const action = async (name: "continue" | "interrupt" | "queue" | "prioritize", taskId?: string) => {
    setBusy(true);
    try {
      if (kind === "issue") await api.issueWorkThreadAction(id, name, name === "queue" ? "Queued from Work Thread controls" : undefined, taskId);
      else if (name !== "prioritize") await api.chatWorkThreadAction(id, name, name === "queue" ? "Queued from Work Thread controls" : undefined);
      await queryClient.invalidateQueries({ queryKey });
    } finally {
      setBusy(false);
    }
  };
  const stateLabel = snapshot.state.replaceAll("_", " ");
  const queued = snapshot.queued_inputs.length;
  return (
    <div className="flex items-center gap-2 rounded-md border bg-muted/30 px-2 py-1" data-testid={`work-thread-${kind}`}>
      <span className="text-caption text-muted-foreground" title={snapshot.thread_id}>
        {t(($) => $.work_thread.thread, { state: stateLabel })}
        {queued ? ` · ${t(($) => $.work_thread.queued_count, { count: queued })}` : ""}
      </span>
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
      <Button size="sm" variant="ghost" disabled={busy} onClick={() => action("queue")} aria-label={t(($) => $.work_thread.queue_aria)}>
        <ListPlus className="mr-1 h-3.5 w-3.5" />
        {t(($) => $.work_thread.queue)}
      </Button>
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
