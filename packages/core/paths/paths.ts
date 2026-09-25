/**
 * Centralized URL path builder. All navigation in shared packages (packages/views)
 * MUST go through this module — no hardcoded string paths.
 *
 * Two kinds of paths:
 *  - workspace-scoped: paths.workspace(slug).xxx() — carry workspace in URL
 *  - global: paths.login(), paths.newWorkspace(), paths.invite(id) — pre-workspace routes
 *
 * Why pure functions + builder pattern:
 *  - Changing a route shape (e.g. adding workspace slug prefix) becomes a single-file edit
 *  - IDs are always URL-encoded here so callers can't forget
 *  - Zero runtime deps means this module is safe in Node (tests) and browsers
 */

const encode = (id: string) => encodeURIComponent(id);

/**
 * `?focus=` token that scrolls the agent's Instructions tab to its
 * conversation-starters editor and flashes it. Lives here because it is URL
 * vocabulary: `agentConversationStarters()` writes it and the tab reads it,
 * and a shared constant is what stops the two from drifting apart.
 */
export const AGENT_FOCUS_CONVERSATION_STARTERS = "conversation_starters";

function workspaceScoped(slug: string) {
  const ws = `/${encode(slug)}`;
  return {
    root: () => `${ws}/issues`,
    usage: () => `${ws}/usage`,
    issues: () => `${ws}/issues`,
    issueDetail: (id: string) => `${ws}/issues/${encode(id)}`,
    // One alignment conversation. Like an agent-creation conversation, it is a
    // durable server-side object rather than a step of the create flow: it is
    // left, resumed and refreshed into, so it owns an address. Addressed by the
    // carrier chat session id, which is also the draft's identity.
    newIssueDraft: (draftId: string) =>
      `${ws}/issues/new/${encode(draftId)}`,
    projects: () => `${ws}/projects`,
    projectDetail: (id: string) => `${ws}/projects/${encode(id)}`,
    autopilots: () => `${ws}/autopilots`,
    autopilotDetail: (id: string) => `${ws}/autopilots/${encode(id)}`,
    agents: () => `${ws}/agents`,
    newAgent: () => `${ws}/agents/new`,
    // The two creation methods behind the chooser. Each is a real route so a
    // half-filled form survives a refresh and can be linked to directly.
    newAgentManual: () => `${ws}/agents/new/manual`,
    newAgentAi: () => `${ws}/agents/new/ai`,
    // One creation conversation. It is a durable object, not a step of the
    // route above: it survives leaving the studio and is resumed later, so it
    // owns an address instead of being a query param on the "start one" screen.
    newAgentAiSession: (sessionId: string) =>
      `${ws}/agents/new/ai/${encode(sessionId)}`,
    agentDetail: (id: string) => `${ws}/agents/${encode(id)}`,
    // Deep link behind "customize" in a chat's empty state: the agent's
    // Instructions tab, scrolled to the conversation starters that produced
    // the buttons the viewer just looked at.
    agentConversationStarters: (id: string) =>
      `${ws}/agents/${encode(id)}?view=instructions&focus=${AGENT_FOCUS_CONVERSATION_STARTERS}`,
    memberDetail: (id: string) => `${ws}/members/${encode(id)}`,
    squads: () => `${ws}/squads`,
    squadDetail: (id: string) => `${ws}/squads/${encode(id)}`,
    inbox: () => `${ws}/inbox`,
    chat: () => `${ws}/chat`,
    chatWithAgent: (agentId: string) =>
      `${ws}/chat?agent=${encode(agentId)}`,
    // Stable internal address of one conversation. Older links used
    // `?session=`; those still open (see chatSessionIdFromLocation).
    chatSession: (sessionId: string) => `${ws}/chat/${encode(sessionId)}`,
    myIssues: () => `${ws}/my-issues`,
    runtimes: () => `${ws}/runtimes`,
    runtimeDetail: (id: string) => `${ws}/runtimes/${encode(id)}`,
    runtimeSettings: (machineId: string, runtimeId: string) =>
      `${ws}/runtimes/${encode(machineId)}/runtime/${encode(runtimeId)}`,
    skills: () => `${ws}/skills`,
    skillDetail: (id: string) => `${ws}/skills/${encode(id)}`,
    settings: () => `${ws}/settings`,
    attachmentPreview: (id: string) => `${ws}/attachments/${encode(id)}/preview`,
  };
}

export const paths = {
  workspace: workspaceScoped,

  // Global (pre-workspace) routes
  login: () => "/login",
  signup: () => "/signup",
  newWorkspace: () => "/workspaces/new",
  invite: (id: string) => `/invite/${encode(id)}`,
  invitations: () => "/invitations",
  onboarding: () => "/onboarding",
  authCallback: () => "/auth/callback",
  root: () => "/",
};

export type WorkspacePaths = ReturnType<typeof workspaceScoped>;

// Prefixes — not slug names — because we match against full URL paths.
// A path is global if it equals or begins with any of these.
// Note: `/workspaces/` (trailing slash) is the prefix — `workspaces` is reserved,
// so any path starting with `/workspaces/...` is system-owned, not user-owned.
const GLOBAL_PREFIXES = ["/login", "/workspaces/", "/invite/", "/invitations", "/onboarding", "/auth/", "/logout", "/signup"];

export function isGlobalPath(path: string): boolean {
  return GLOBAL_PREFIXES.some((p) => path === p || path.startsWith(p));
}

function decodePathSegment(value: string): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

/**
 * Session id carried by a chat URL.
 *
 * `/{slug}/chat/{sessionId}` is the address a copied link uses. The older
 * `?session=` form still resolves so bookmarks and in-app history keep opening.
 * A path id wins when both are present.
 */
export function chatSessionIdFromLocation(
  pathname: string,
  search: string | URLSearchParams,
): string | null {
  const hashIdx = pathname.indexOf("#");
  const withoutHash = hashIdx === -1 ? pathname : pathname.slice(0, hashIdx);
  const queryIdx = withoutHash.indexOf("?");
  const cleanPath = queryIdx === -1 ? withoutHash : withoutHash.slice(0, queryIdx);
  const segments = cleanPath.split("/").filter(Boolean);
  if (segments.length >= 3 && segments[1] === "chat" && segments[2]) {
    const id = decodePathSegment(segments[2]);
    if (id) return id;
  }
  const params = typeof search === "string" ? new URLSearchParams(search) : search;
  return params.get("session") || null;
}
