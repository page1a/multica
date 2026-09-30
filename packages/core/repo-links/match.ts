import type {
  RepoConnectionState,
  RepoLink,
  RepoLinkHealth,
  RepoLinkKind,
  RepoBinding,
} from "../types/repo-link";

export interface RepoLocator {
  /** Lowercased hostname. */
  host: string;
  /** First path segment, or `*` when the input names only a host. */
  owner: string;
  /**
   * `host/path` with the host lowercased and the path's casing preserved.
   * `.git` is stripped. This is the same identity `repositoryIdentity` uses.
   */
  name: string;
}

const RANK: Record<RepoLinkKind, number> = {
  github_app: 0,
  github_token: 1,
  gitlab_token: 1,
  forgejo_token: 1,
  gitea_token: 1,
};

function rankOf(kind: string): number {
  switch (kind) {
    case "github_app":
    case "github_token":
    case "gitlab_token":
    case "forgejo_token":
    case "gitea_token":
      return RANK[kind];
    default:
      return 2;
  }
}

/**
 * Parse a repository URL, an scp-style remote, or a connection scope
 * (`github.com/acme`, `gitlab.corp.local/*`).
 */
export function parseRepoLocator(rawURL: string): RepoLocator | null {
  const value = rawURL.trim();
  if (!value) return null;

  let host = "";
  let path = "";
  if (!value.includes("://")) {
    const scpLike = value.match(/^(?:[^@\s/]+@)?([^:\s/]+):(.+)$/);
    if (scpLike && !scpLike[2]?.includes("//")) {
      host = scpLike[1] ?? "";
      path = scpLike[2] ?? "";
    }
  }
  if (!host) {
    const withScheme = /^https?:\/\//i.test(value) ? value : `https://${value}`;
    try {
      const parsed = new URL(withScheme);
      host = parsed.hostname;
      path = parsed.pathname;
    } catch {
      return null;
    }
  }

  const hostName = host.toLowerCase();
  if (!hostName) return null;

  const normalizedPath = path
    .replace(/^\/+|\/+$/g, "")
    .replace(/\.git$/i, "")
    .replace(/\/\*$/, "");
  if (!normalizedPath || normalizedPath === "*") {
    return { host: hostName, owner: "*", name: `${hostName}/*` };
  }

  const segments = normalizedPath.split("/").filter(Boolean);
  const owner = segments[0] === "*" ? "*" : (segments[0] ?? "*");
  return {
    host: hostName,
    owner,
    name: `${hostName}/${segments.join("/")}`,
  };
}

export function repoLinkScope(link: { host: string; owner: string }): string {
  return link.owner === "*" ? `${link.host}/*` : `${link.host}/${link.owner}/*`;
}

export function repoLinkTitle(
  kindLabel: string,
  link: { host: string; owner: string },
): string {
  const who = link.owner === "*" ? link.host : link.owner;
  return `${kindLabel} · ${who}`;
}

export function candidateLinks(links: RepoLink[], repoUrl: string): RepoLink[] {
  const locator = parseRepoLocator(repoUrl);
  if (!locator || locator.owner === "*") return [];
  return links
    .map((link, index) => ({ link, index }))
    .filter(
      ({ link }) =>
        link.host === locator.host &&
        (link.owner === "*" ||
          link.owner.toLowerCase() === locator.owner.toLowerCase()),
    )
    .sort(
      (left, right) =>
        rankOf(left.link.kind) - rankOf(right.link.kind) ||
        left.index - right.index,
    )
    .map(({ link }) => link);
}

export function stateFromHealth(health: string): RepoConnectionState {
  switch (health) {
    case "ok":
      return "connected";
    case "cli":
      return "cli";
    case "pending_install":
      return "pending_install";
    default:
      // A link is present. An unrecognised health must not render as
      // "no connection", which would send the person to add a second one.
      return "cli";
  }
}

function pickAutomatic(candidates: RepoLink[]): RepoLink | null {
  return (
    candidates.find((link) => link.health === "ok") ??
    candidates.find((link) => link.health === "cli") ??
    candidates.find((link) => link.health === "pending_install") ??
    candidates[0] ??
    null
  );
}

/**
 * Pinned link wins when it still exists. Otherwise the first link that can
 * actually answer (ok, then cli fallback, then an app waiting to be installed).
 * App ranks ahead of token, so two healthy links prefer the app.
 */
export function resolveRepoLink(
  links: RepoLink[],
  repoUrl: string,
  pinnedId?: string | null,
): { link: RepoLink | null; state: RepoConnectionState } {
  const pinned = pinnedId
    ? links.find((link) => link.id === pinnedId)
    : undefined;
  const link = pinned ?? pickAutomatic(candidateLinks(links, repoUrl));
  if (!link) return { link: null, state: "disconnected" };
  return { link, state: stateFromHealth(link.health) };
}

export function findRepoBinding(
  bindings: RepoBinding[],
  repoUrl: string,
): RepoBinding | undefined {
  const identity = parseRepoLocator(repoUrl)?.name;
  return bindings.find((binding) => {
    if (binding.repo_url === repoUrl) return true;
    if (!identity) return false;
    return parseRepoLocator(binding.repo_url)?.name === identity;
  });
}

export function isKnownLinkHealth(health: string): health is RepoLinkHealth {
  switch (health) {
    case "ok":
    case "cli":
    case "pending_install":
      return true;
    default:
      return false;
  }
}
