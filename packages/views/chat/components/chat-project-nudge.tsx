"use client";

import { chatSessionProjectIds } from "@multica/core/chat/project-context";
import type { ChatSession } from "@multica/core/types";
import { ProjectPicker } from "../../projects/components/project-picker";
import { useT } from "../../i18n";

/**
 * Reminder on a chat that has no project. Dismissing it is "this chat does
 * not need one" — the parent persists that on the server, so another browser
 * does not ask again. Binding a project removes the reminder because the
 * chat is no longer unbound.
 */
export function ChatProjectNudge({
  session,
  onBind,
  onDismiss,
  dismissing,
}: {
  session: ChatSession;
  onBind: (projectIds: string[]) => void;
  onDismiss: () => void;
  dismissing?: boolean;
}) {
  const { t } = useT("chat");
  if (session.status === "archived") return null;
  if (session.project_nudge_dismissed) return null;
  if (chatSessionProjectIds(session).length > 0) return null;

  return (
    <div className="flex shrink-0 items-center gap-2 border-b border-warning/30 bg-warning/10 px-4 py-2">
      <ProjectPicker
        projectId={null}
        onUpdate={(updates) => {
          if (updates.project_id) onBind([updates.project_id]);
        }}
        triggerRender={
          <button
            type="button"
            className="text-caption font-medium text-foreground underline-offset-2 outline-none hover:underline focus-visible:underline"
          >
            {t(($) => $.project_nudge.bind)}
          </button>
        }
      />
      <button
        type="button"
        disabled={dismissing}
        onClick={onDismiss}
        className="ml-auto shrink-0 rounded-md border border-border bg-background px-2 py-1 text-caption text-muted-foreground outline-none hover:text-foreground focus-visible:ring-1 focus-visible:ring-ring disabled:opacity-50"
      >
        {t(($) => $.project_nudge.casual)}
      </button>
    </div>
  );
}
