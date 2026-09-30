"use client";

import type { GitHubAppSetup, GitHubAppStatus } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { useT } from "../../i18n";
import { isDesktopShell, openExternal } from "../../platform";

export function postGitHubAppManifest(
  actionURL: string,
  manifest: Record<string, unknown>,
) {
  const form = document.createElement("form");
  form.method = "post";
  form.action = actionURL;
  const input = document.createElement("input");
  input.type = "hidden";
  input.name = "manifest";
  input.value = JSON.stringify(manifest);
  form.appendChild(input);
  document.body.appendChild(form);
  form.submit();
}

/**
 * Send the owner to GitHub's "create App" page. Web posts the manifest form
 * in this tab. The desktop window's navigation guard blocks form posts to
 * other sites, so desktop hands the server's single-use launch link to the
 * system browser instead; "browser" tells the caller to say so.
 */
export function launchGitHubAppSetup(setup: GitHubAppSetup): "browser" | "form" {
  if (isDesktopShell() && setup.launch_url) {
    openExternal(setup.launch_url);
    return "browser";
  }
  postGitHubAppManifest(setup.action_url, setup.manifest);
  return "form";
}

export function GitHubAppPanel({
  status,
  org,
  onOrg,
  creating,
  onCreate,
}: {
  status: GitHubAppStatus | undefined;
  org: string;
  onOrg: (value: string) => void;
  creating: boolean;
  onCreate: () => void;
}) {
  const { t } = useT("settings");
  if (!status) return null;
  const manage = status.manage_url || status.html_url;
  let body = t(($) => $.repo_links.github_app_unconfigured);
  if (status.source === "env") body = t(($) => $.repo_links.github_app_env);
  else if (status.source === "database") body = t(($) => $.repo_links.github_app_ready);
  else if (status.block_reason === "not_owner") body = t(($) => $.repo_links.github_app_need_owner);
  else if (status.block_reason === "public_url_missing") body = t(($) => $.repo_links.github_app_public_url);
  else if (status.block_reason === "secret_unavailable") body = t(($) => $.repo_links.github_app_unavailable);
  return (
    <section
      className="rounded-xl border border-surface-border px-4 py-3"
      aria-label={t(($) => $.repo_links.github_app_status)}
    >
      <h3 className="text-body font-medium">{t(($) => $.repo_links.github_app_status)}</h3>
      <p className="mt-1 text-caption text-muted-foreground">{body}</p>
      {status.app_name ? <p className="mt-2 text-body">{status.app_name}</p> : null}
      {manage ? (
        <a
          href={manage}
          target="_blank"
          rel="noreferrer"
          className="mt-1 inline-block text-caption text-muted-foreground underline-offset-2 hover:underline"
        >
          {t(($) => $.repo_links.github_app_manage)}
        </a>
      ) : null}
      {status.source === "none" && status.can_create ? (
        <GitHubAppCreateFields
          inputId="github-app-org"
          org={org}
          onOrg={onOrg}
          creating={creating}
          onCreate={onCreate}
        />
      ) : null}
    </section>
  );
}

export function GitHubAppCreateFields({
  inputId,
  org,
  onOrg,
  creating,
  onCreate,
  blocked,
  showButton = true,
}: {
  inputId: string;
  org: string;
  onOrg: (value: string) => void;
  creating: boolean;
  onCreate: () => void;
  blocked?: string | null;
  showButton?: boolean;
}) {
  const { t } = useT("settings");
  if (blocked) {
    return <p className="text-caption text-muted-foreground">{blocked}</p>;
  }
  return (
    <form
      className="mt-3 space-y-3"
      onSubmit={(event) => {
        event.preventDefault();
        onCreate();
      }}
    >
      <div className="space-y-1.5">
        <Label htmlFor={inputId}>{t(($) => $.repo_links.github_app_org)}</Label>
        <Input
          id={inputId}
          value={org}
          onChange={(event) => onOrg(event.target.value)}
          placeholder={t(($) => $.repo_links.github_app_org_placeholder)}
          autoComplete="off"
          spellCheck={false}
        />
      </div>
      {showButton ? (
        <Button type="submit" disabled={creating}>
          {creating
            ? t(($) => $.repo_links.github_app_creating)
            : t(($) => $.repo_links.github_app_create)}
        </Button>
      ) : null}
    </form>
  );
}
