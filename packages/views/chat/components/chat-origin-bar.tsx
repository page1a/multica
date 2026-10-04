"use client";

import { useQuery } from "@tanstack/react-query";
import { CornerLeftUp } from "lucide-react";
import { useChatStore } from "@multica/core/chat";
import { chatSessionOptions } from "@multica/core/chat/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import type { ChatSession } from "@multica/core/types";
import { useT } from "../../i18n";

/**
 * "Opened from <chat>" above a chat an agent opened from another chat
 * (DENE-1271). The parent's title comes from the session detail and is only
 * present when this viewer may open the parent; otherwise the bar names no
 * chat and offers no link.
 */
export function ChatOriginBar({ session }: { session: ChatSession }) {
  const { t } = useT("chat");
  const wsId = useWorkspaceId();
  const setActiveSession = useChatStore((s) => s.setActiveSession);
  const originId = session.origin_session_id ?? null;
  const { data: detail } = useQuery({
    ...chatSessionOptions(wsId, session.id),
    enabled: !!originId && session.origin_title === undefined,
  });
  if (!originId) return null;
  // Word order differs by language, so either side may be empty.
  const prefix = t(($) => $.origin.from_prefix);
  const suffix = t(($) => $.origin.from_suffix);
  const title = (session.origin_title ?? detail?.origin_title ?? "").trim();

  return (
    <div className="flex min-w-0 shrink-0 items-center gap-1.5 border-b px-3 py-1.5 text-caption text-muted-foreground">
      <CornerLeftUp className="size-3.5 shrink-0" />
      {title ? (
        <>
          {prefix && <span className="shrink-0">{prefix}</span>}
          <button
            type="button"
            className="min-w-0 truncate font-medium text-foreground hover:underline"
            onClick={() => setActiveSession(originId)}
          >
            {title}
          </button>
          {suffix && <span className="shrink-0">{suffix}</span>}
        </>
      ) : (
        <span className="truncate">{t(($) => $.origin.from_hidden)}</span>
      )}
    </div>
  );
}
