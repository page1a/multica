import type { GitHubInstallation } from "../types/github";
import type { RepoLink, RepoLinkKind } from "../types/repo-link";
import type { VCSConnection, VCSProvider } from "../types/vcs";

/** Older servers expose GitHub App installs and VCS tokens on separate routes. */
export function hostFromInstanceUrl(instanceUrl: string): string {
  try {
    return new URL(instanceUrl).hostname.toLowerCase();
  } catch {
    return instanceUrl
      .trim()
      .replace(/^https?:\/\//i, "")
      .split("/")[0]
      ?.toLowerCase() ?? "";
  }
}

export function legacyGithubLink(installation: GitHubInstallation): RepoLink {
  return {
    id: `github:${installation.id}`,
    kind: "github_app",
    host: "github.com",
    owner: installation.account_login,
    visibility: "workspace",
    health: "ok",
    account_login: installation.account_login,
    webhook_ok: true,
    can_manage: installation.installation_id != null,
  };
}

function vcsKind(provider: VCSProvider): RepoLinkKind {
  switch (provider) {
    case "gitlab":
      return "gitlab_token";
    case "gitea":
      return "gitea_token";
    case "forgejo":
      return "forgejo_token";
    default:
      return "forgejo_token";
  }
}

export function legacyVcsLink(
  connection: VCSConnection,
  canManage: boolean,
): RepoLink | null {
  const host = hostFromInstanceUrl(connection.instance_url);
  if (!host) return null;
  return {
    id: `vcs:${connection.id}`,
    kind: vcsKind(connection.provider),
    host,
    owner: "*",
    visibility: "workspace",
    health: "ok",
    account_login: connection.account_login,
    can_manage: canManage,
  };
}
