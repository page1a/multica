import { useCallback, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { useChatStore } from "@multica/core/chat";
import { useSetChatSessionLinkedProjects } from "@multica/core/chat/mutations";
import { linkedProjectOptionsOptions } from "@multica/core/workspace-links";
import type {
  ChatLinkedProject,
  ChatLinkedProjectRef,
  ChatSession,
  LinkedProjectOption,
} from "@multica/core/types";

const EMPTY: ChatLinkedProject[] = [];

/** The ref a linked entry is stored and sent as. */
export function linkedProjectRef(item: ChatLinkedProjectRef): ChatLinkedProjectRef {
  return { link_id: item.link_id, project_id: item.project_id };
}

/** A menu option as an attached entry, for the not-yet-created chat. */
export function linkedProjectFromOption(option: LinkedProjectOption): ChatLinkedProject {
  return {
    link_id: option.link_id,
    project_id: option.id,
    title: option.title,
    icon: option.icon,
    source_name: option.source.name,
    available: true,
  };
}

export interface ChatLinkedProjects {
  /** What the chat carries now, stale entries included. */
  items: ChatLinkedProject[];
  /** What the menu can offer. */
  options: LinkedProjectOption[];
  /** The complete next set. */
  onChange: (refs: ChatLinkedProjectRef[]) => void;
  isUpdating: boolean;
  /** Refs for a chat about to be created; call clearDraft once it exists. */
  draftRefs: ChatLinkedProjectRef[];
  clearDraft: () => void;
}

/**
 * Read-only projects a chat attaches from linked workspaces (DENE-1643). An
 * open chat reads and writes its server set; a new chat keeps a draft that
 * the create call sends along.
 */
export function useChatLinkedProjects(session: ChatSession | null | undefined): ChatLinkedProjects {
  const wsId = useWorkspaceId();
  const { data: options = [] } = useQuery(linkedProjectOptionsOptions(wsId));
  const draft = useChatStore((s) => s.selectedLinkedProjects);
  const setDraft = useChatStore((s) => s.setSelectedLinkedProjects);
  const mutation = useSetChatSessionLinkedProjects();
  const sessionId = session?.id;

  const items = session ? (session.linked_projects ?? EMPTY) : draft;

  const onChange = useCallback(
    (refs: ChatLinkedProjectRef[]) => {
      if (sessionId) {
        mutation.mutate({ sessionId, refs });
        return;
      }
      setDraft(
        refs.flatMap((ref) => {
          const option = options.find((o) => o.link_id === ref.link_id && o.id === ref.project_id);
          return option ? [linkedProjectFromOption(option)] : [];
        }),
      );
    },
    [sessionId, mutation, options, setDraft],
  );

  const draftRefs = useMemo(() => draft.map(linkedProjectRef), [draft]);
  const clearDraft = useCallback(() => setDraft([]), [setDraft]);

  return {
    items,
    options,
    onChange,
    isUpdating: mutation.isPending,
    draftRefs,
    clearDraft,
  };
}
