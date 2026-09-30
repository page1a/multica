import type { ChatProjectFilter } from "./project-bar";

/**
 * Projects a brand-new chat should be created with.
 *
 * The chat list's filter is the choice: a project view binds the new chat to
 * that project (the server then shares it with the project, DENE-840). All
 * and the no-project view ("随手聊") start unbound. The open conversation's
 * own projects are not a default — leaving them on would drag a historical
 * chat into the next one.
 */
export function draftProjectIdsForNewChat(filter: ChatProjectFilter): string[] {
  return filter.type === "project" ? [filter.id] : [];
}

/** Scope key for "the conversation last opened in this view". All has none. */
export function openMemoryScope(filter: ChatProjectFilter): string | null {
  if (filter.type === "project") return filter.id;
  if (filter.type === "none") return "\0none";
  return null;
}

export interface SwitchLandingSession {
  id: string;
  projectIds: readonly string[];
  updatedAt: string;
  status: "active" | "archived";
}

/**
 * Which conversation to open after the project filter changes.
 *
 * `null` means leave the open conversation alone: All never yanks it, the
 * open one already belongs to the view, or the view has nothing to land on
 * (clearing it would be the blank page this is avoiding).
 */
export function sessionToLandOn(input: {
  filter: ChatProjectFilter;
  sessions: readonly SwitchLandingSession[];
  activeSessionId: string | null;
  rememberedSessionId: string | null;
}): string | null {
  if (input.filter.type === "all") return null;

  const belongs = (session: SwitchLandingSession) => {
    if (session.status === "archived") return false;
    if (input.filter.type === "none") return session.projectIds.length === 0;
    if (input.filter.type !== "project") return false;
    return session.projectIds.includes(input.filter.id);
  };

  const active = input.sessions.find((session) => session.id === input.activeSessionId);
  if (active && belongs(active)) return null;

  const remembered = input.sessions.find((session) => session.id === input.rememberedSessionId);
  if (remembered && belongs(remembered)) return remembered.id;

  let best: SwitchLandingSession | null = null;
  let bestAt = Number.NEGATIVE_INFINITY;
  for (const session of input.sessions) {
    if (!belongs(session)) continue;
    const at = Date.parse(session.updatedAt);
    const recentAt = Number.isFinite(at) ? at : 0;
    if (!best || recentAt > bestAt || (recentAt === bestAt && session.id < best.id)) {
      best = session;
      bestAt = recentAt;
    }
  }
  return best?.id ?? null;
}

/**
 * Quick-switch order: projects chatted in most recently, then the rest by
 * title. Pin order stays on the chip row; this list is "where I was just
 * working", not "what I pinned".
 */
export function orderProjectsForQuickSwitch(
  projects: readonly { id: string; title: string }[],
  recentAtById: ReadonlyMap<string, number>,
): { id: string; title: string }[] {
  return [...projects].sort((a, b) => {
    const recent = (recentAtById.get(b.id) ?? 0) - (recentAtById.get(a.id) ?? 0);
    if (recent !== 0) return recent;
    const title = a.title.localeCompare(b.title, undefined, { sensitivity: "base" });
    if (title !== 0) return title;
    return a.id.localeCompare(b.id);
  });
}
