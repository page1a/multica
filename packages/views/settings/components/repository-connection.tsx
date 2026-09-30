"use client";

import type { ReactNode } from "react";
import {
  candidateLinks,
  findRepoBinding,
  parseRepoLocator,
  repoLinkTitle,
  resolveRepoLink,
} from "@multica/core/repo-links";
import type { RepoLink } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";
import type { RepoCatalog } from "./use-repo-catalog";
import { RepoStatusText, toneForState, useRepoLinkLabels } from "./repo-link-present";

export function RepositoryConnectionControls({
  repoUrl,
  catalog,
  canManageWorkspace,
  testing,
  trailing,
  onTest,
  onConnect,
  onPin,
}: {
  repoUrl: string;
  catalog: RepoCatalog;
  canManageWorkspace: boolean;
  testing: boolean;
  trailing?: ReactNode;
  onTest: (repoUrl: string) => void;
  onConnect: (scope: string) => void;
  onPin: (repoUrl: string, linkId: string | null) => void;
}) {
  const { t } = useT("settings");
  const labels = useRepoLinkLabels();
  const locator = parseRepoLocator(repoUrl);
  if (!locator || locator.owner === "*") {
    return (
      <>
        <span className="text-caption text-muted-foreground">—</span>
        <span />
        <span className="flex justify-end">{trailing}</span>
      </>
    );
  }

  const binding = findRepoBinding(catalog.bindings, repoUrl);
  const pinnedId = binding?.pinned_link_id || "";
  const client = resolveRepoLink(catalog.links, repoUrl, pinnedId || null);
  const resolved =
    catalog.links.find(
      (link) => link.id === (binding?.resolved_link_id || client.link?.id),
    ) ?? client.link;
  const state = binding?.state ?? client.state;
  const canConfigure = binding
    ? binding.can_configure
    : catalog.source === "catalog" && canManageWorkspace;
  const candidates = candidateLinks(catalog.links, repoUrl);
  const options = optionsFor(candidates, resolved);
  const titleFor = (link: RepoLink) => repoLinkTitle(labels.kind(link.kind), link);
  const autoLabel =
    !pinnedId && resolved
      ? t(($) => $.repo_links.auto_named, { name: titleFor(resolved) })
      : t(($) => $.repo_links.auto);
  const scope = `${locator.host}/${locator.owner}`;
  const canAdd = catalog.canAddPersonal || catalog.canAddWorkspace;
  const disconnected = !resolved || state === "disconnected";

  return (
    <>
      <div className="min-w-0">
        {canConfigure && options.length > 0 ? (
          <select
            aria-label={t(($) => $.repo_links.column_connection)}
            className="w-full max-w-full truncate bg-transparent text-caption text-foreground"
            value={options.some((link) => link.id === pinnedId) ? pinnedId : ""}
            onChange={(event) => onPin(repoUrl, event.target.value || null)}
          >
            <option value="">{autoLabel}</option>
            {options.map((link) => (
              <option key={link.id} value={link.id}>
                {titleFor(link)}
              </option>
            ))}
          </select>
        ) : (
          <span className="block truncate text-caption text-muted-foreground">
            {resolved ? (pinnedId ? titleFor(resolved) : autoLabel) : "—"}
          </span>
        )}
      </div>
      <RepoStatusText tone={toneForState(state)}>{labels.state(state)}</RepoStatusText>
      <div className="flex items-center justify-end gap-1">
        {disconnected ? (
          canAdd ? (
            <Button
              variant="ghost"
              size="sm"
              className="h-7 px-2 text-caption text-muted-foreground"
              aria-label={t(($) => $.repo_links.connect_named, { repo: locator.name })}
              onClick={() => onConnect(scope)}
            >
              {t(($) => $.repo_links.connect)}
            </Button>
          ) : null
        ) : (
          <Button
            variant="ghost"
            size="sm"
            className="h-7 px-2 text-caption text-muted-foreground"
            aria-label={t(($) => $.repo_links.test_named, { repo: locator.name })}
            disabled={testing}
            onClick={() => onTest(repoUrl)}
          >
            {t(($) => $.repo_links.test)}
          </Button>
        )}
        {trailing}
      </div>
    </>
  );
}

function optionsFor(candidates: RepoLink[], resolved: RepoLink | null): RepoLink[] {
  const options = [...candidates];
  if (resolved && !options.some((link) => link.id === resolved.id)) {
    options.unshift(resolved);
  }
  return options;
}

export function repositorySourceLine(
  catalog: RepoCatalog,
  repoUrl: string,
): string {
  const titles =
    findRepoBinding(catalog.bindings, repoUrl)
      ?.source_projects?.map((project) => project.title)
      .filter((title) => title.trim().length > 0) ?? [];
  return titles.join(" · ");
}
