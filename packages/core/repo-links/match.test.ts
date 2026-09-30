// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { RepoLink } from "../types/repo-link";
import {
  candidateLinks,
  findRepoBinding,
  parseRepoLocator,
  resolveRepoLink,
} from "./match";

function link(partial: Partial<RepoLink> & Pick<RepoLink, "id" | "kind" | "owner">): RepoLink {
  return {
    host: "github.com",
    visibility: "workspace",
    health: "ok",
    can_manage: true,
    ...partial,
  };
}

describe("parseRepoLocator", () => {
  it("keeps path casing and strips the git suffix", () => {
    expect(parseRepoLocator("https://GitHub.com/Acme/Repo.git")).toEqual({
      host: "github.com",
      owner: "Acme",
      name: "github.com/Acme/Repo",
    });
    expect(parseRepoLocator("git@github.com:acme/repo.git")?.name).toBe(
      "github.com/acme/repo",
    );
  });

  it("reads a scope as the account, and a bare host as the whole instance", () => {
    expect(parseRepoLocator("github.com/kkunkunya")).toMatchObject({
      host: "github.com",
      owner: "kkunkunya",
    });
    expect(parseRepoLocator("gitlab.corp.local/*")).toMatchObject({
      host: "gitlab.corp.local",
      owner: "*",
    });
    expect(parseRepoLocator("gitlab.corp.local")).toMatchObject({
      owner: "*",
    });
  });
});

describe("resolveRepoLink", () => {
  const app = link({
    id: "app",
    kind: "github_app",
    owner: "jeff-kunkun",
    health: "pending_install",
  });
  const token = link({
    id: "token",
    kind: "github_token",
    owner: "jeff-kunkun",
    health: "ok",
  });
  const personal = link({
    id: "me",
    kind: "github_token",
    owner: "kkunkunya",
    visibility: "personal",
  });
  const gitlab = link({
    id: "gl",
    kind: "gitlab_token",
    host: "gitlab.corp.local",
    owner: "*",
    health: "cli",
  });

  it("prefers a working token over an app that is not installed yet", () => {
    const resolved = resolveRepoLink(
      [app, token],
      "https://github.com/jeff-kunkun/multica.git",
    );
    expect(resolved.link?.id).toBe("token");
    expect(resolved.state).toBe("connected");
  });

  it("uses the app when it is the only healthy match", () => {
    const resolved = resolveRepoLink(
      [{ ...app, health: "ok" }, token],
      "git@github.com:jeff-kunkun/multica.git",
    );
    expect(resolved.link?.id).toBe("app");
  });

  it("surfaces a pending app when nothing else covers the repository", () => {
    const resolved = resolveRepoLink(
      [app],
      "https://github.com/jeff-kunkun/ai100",
    );
    expect(resolved.state).toBe("pending_install");
  });

  it("does not borrow a link from another account", () => {
    const resolved = resolveRepoLink(
      [personal],
      "https://github.com/someone-else/tools.git",
    );
    expect(resolved.state).toBe("disconnected");
    expect(candidateLinks([personal, token], "https://github.com/kkunkunya/online-tarot")).toEqual([
      personal,
    ]);
  });

  it("matches a whole-host link and reports the cli fallback", () => {
    const resolved = resolveRepoLink(
      [gitlab],
      "https://gitlab.corp.local/infra/deploy.git",
    );
    expect(resolved.link?.id).toBe("gl");
    expect(resolved.state).toBe("cli");
  });

  it("honours a pin even when another link would win automatically", () => {
    const resolved = resolveRepoLink(
      [app, token],
      "https://github.com/jeff-kunkun/multica",
      "app",
    );
    expect(resolved.link?.id).toBe("app");
    expect(resolved.state).toBe("pending_install");
  });

  it("finds a binding across https and ssh spellings", () => {
    const binding = findRepoBinding(
      [
        {
          repo_url: "git@github.com:jeff-kunkun/multica.git",
          state: "connected",
          can_configure: true,
        },
      ],
      "https://github.com/jeff-kunkun/multica",
    );
    expect(binding?.state).toBe("connected");
  });
});
