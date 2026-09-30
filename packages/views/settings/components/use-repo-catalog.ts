"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { ApiError } from "@multica/core/api";
import { useConfigStore } from "@multica/core/config";
import { githubInstallationsOptions } from "@multica/core/github";
import {
  legacyGithubLink,
  legacyVcsLink,
  repoLinksOptions,
} from "@multica/core/repo-links";
import type { RepoBinding, RepoLink } from "@multica/core/types";
import { vcsConnectionsOptions } from "@multica/core/vcs";

export type RepoCatalogSource = "loading" | "catalog" | "legacy" | "unavailable";

export interface RepoCatalog {
  source: RepoCatalogSource;
  links: RepoLink[];
  bindings: RepoBinding[];
  canAddWorkspace: boolean;
  canAddPersonal: boolean;
  isPending: boolean;
}

function catalogMissing(error: unknown): boolean {
  return error instanceof ApiError && (error.status === 404 || error.status === 405);
}

function idle(source: RepoCatalogSource, isPending: boolean): RepoCatalog {
  return {
    source,
    links: [],
    bindings: [],
    canAddWorkspace: false,
    canAddPersonal: false,
    isPending,
  };
}

/**
 * The settings pages read one catalog. Until that route exists, GitHub App
 * installs and self-hosted tokens already on the server are shown in the same
 * list so the old pages can leave the directory without going blank.
 */
export function useRepoCatalog(wsId: string): RepoCatalog {
  const catalog = useQuery({
    ...repoLinksOptions(wsId),
    retry: (count, error) => (catalogMissing(error) ? false : count < 1),
  });
  const missing = catalogMissing(catalog.error);
  const vcsAvailable = useConfigStore((state) => state.vcsIntegrationAvailable);
  const github = useQuery({
    ...githubInstallationsOptions(wsId),
    enabled: missing && !!wsId,
  });
  const vcs = useQuery({
    ...vcsConnectionsOptions(wsId),
    enabled: missing && !!wsId && vcsAvailable,
  });

  return useMemo(() => {
    if (catalog.data) {
      return {
        source: "catalog",
        links: catalog.data.links ?? [],
        bindings: catalog.data.bindings ?? [],
        canAddWorkspace: catalog.data.can_add_workspace === true,
        canAddPersonal: catalog.data.can_add_personal === true,
        isPending: false,
      };
    }
    if (catalog.isPending) return idle("loading", true);
    if (!missing) return idle("unavailable", false);

    const legacyPending = github.isPending || (vcsAvailable && vcs.isPending);
    if (legacyPending) return idle("loading", true);
    const links: RepoLink[] = [
      ...(github.data?.installations ?? []).map(legacyGithubLink),
      ...(vcs.data?.connections ?? []).flatMap((connection) => {
        const link = legacyVcsLink(connection, vcs.data?.can_manage === true);
        return link ? [link] : [];
      }),
    ];
    return {
      source: "legacy",
      links,
      bindings: [],
      canAddWorkspace:
        github.data?.can_manage === true || vcs.data?.can_manage === true,
      canAddPersonal: false,
      isPending: false,
    };
  }, [
    catalog.data,
    catalog.isPending,
    github.data,
    github.isPending,
    missing,
    vcs.data,
    vcs.isPending,
    vcsAvailable,
  ]);
}
