"use client";

import { useMemo, useState } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { LoaderCircle } from "lucide-react";
import { useAuthStore } from "@multica/core/auth";
import {
  githubInstallationRepositoriesOptions,
  githubInstallationsOptions,
} from "@multica/core/github";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentWorkspace } from "@multica/core/paths";
import { parseRepoLocator } from "@multica/core/repo-links";
import { memberListOptions } from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { cn } from "@multica/ui/lib/utils";
import { githubShortLabel } from "../../common/github-url";
import { useT } from "../../i18n";

type Tab = "link" | "pick";

interface PickItem {
  url: string;
  label: string;
}

const identityOf = (url: string) => parseRepoLocator(url)?.name.toLowerCase() ?? url.trim().toLowerCase();

/**
 * Add a repository by link, or pick one the workspace already lists (and,
 * for owners and admins, one the GitHub App can see). It only reports the
 * chosen URL; the caller attaches it.
 */
export function AddRepoDialog({
  open,
  onOpenChange,
  attachedUrls,
  onSelect,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  attachedUrls: readonly string[];
  onSelect: (url: string) => Promise<void> | void;
}) {
  const { t } = useT("projects");
  const wsId = useWorkspaceId();
  const workspace = useCurrentWorkspace();
  const userId = useAuthStore((state) => state.user?.id);
  const { data: members = [] } = useQuery({ ...memberListOptions(wsId), enabled: open });
  const role = members.find((member) => member.user_id === userId)?.role;
  const canBrowseGitHub = role === "owner" || role === "admin";

  const [tab, setTab] = useState<Tab>("link");
  const [url, setUrl] = useState("");
  const [search, setSearch] = useState("");
  const [installationId, setInstallationId] = useState("");
  const [pending, setPending] = useState<string | null>(null);

  const installations = useQuery({
    ...githubInstallationsOptions(wsId),
    enabled: open && tab === "pick" && canBrowseGitHub,
  });
  const installationList = installations.data?.installations ?? [];
  const activeInstallation =
    installationList.find((installation) => installation.id === installationId) ??
    installationList[0];
  const githubRepos = useInfiniteQuery({
    ...githubInstallationRepositoriesOptions(wsId, activeInstallation?.id ?? ""),
    enabled:
      open &&
      tab === "pick" &&
      canBrowseGitHub &&
      installations.data?.repository_browse_configured === true &&
      !!activeInstallation,
  });

  const attached = useMemo(
    () => new Set(attachedUrls.map(identityOf)),
    [attachedUrls],
  );

  const items = useMemo<PickItem[]>(() => {
    const seen = new Set<string>();
    const out: PickItem[] = [];
    const push = (itemUrl: string, label: string) => {
      const id = identityOf(itemUrl);
      if (seen.has(id)) return;
      seen.add(id);
      out.push({ url: itemUrl, label });
    };
    for (const repo of workspace?.repos ?? []) push(repo.url, githubShortLabel(repo.url));
    for (const page of githubRepos.data?.pages ?? []) {
      for (const repo of page.repositories) {
        if (!repo.archived) push(repo.clone_url, repo.full_name);
      }
    }
    const needle = search.trim().toLowerCase();
    return needle
      ? out.filter((item) => `${item.label} ${item.url}`.toLowerCase().includes(needle))
      : out;
  }, [workspace?.repos, githubRepos.data?.pages, search]);

  const reset = () => {
    setTab("link");
    setUrl("");
    setSearch("");
    setPending(null);
  };

  const choose = async (chosen: string) => {
    if (pending) return;
    setPending(chosen);
    try {
      await onSelect(chosen);
      onOpenChange(false);
      reset();
    } catch {
      // The caller already told the person; keep the dialog open to retry.
    } finally {
      setPending(null);
    }
  };

  const tabButton = (value: Tab, label: string) => (
    <button
      type="button"
      role="tab"
      aria-selected={tab === value}
      onClick={() => setTab(value)}
      className={cn(
        "-mb-px border-b-[1.5px] pb-2 text-caption transition-colors",
        tab === value
          ? "border-foreground font-medium text-foreground"
          : "border-transparent text-muted-foreground hover:text-foreground",
      )}
    >
      {label}
    </button>
  );

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        onOpenChange(next);
        if (!next) reset();
      }}
    >
      <DialogContent className="gap-0 p-0 sm:max-w-md">
        <DialogHeader className="px-5 pt-5">
          <DialogTitle>{t(($) => $.code.dialog_title)}</DialogTitle>
        </DialogHeader>
        <div role="tablist" className="mx-5 mt-3 flex gap-5 border-b">
          {tabButton("link", t(($) => $.code.tab_link))}
          {tabButton("pick", t(($) => $.code.tab_pick))}
        </div>

        {tab === "link" ? (
          <form
            className="px-5 py-4"
            onSubmit={(event) => {
              event.preventDefault();
              const trimmed = url.trim();
              if (trimmed) void choose(trimmed);
            }}
          >
            <Input
              autoFocus
              value={url}
              onChange={(event) => setUrl(event.target.value)}
              placeholder={t(($) => $.code.url_placeholder)}
              aria-label={t(($) => $.code.url_placeholder)}
              className="font-mono text-caption"
            />
            <DialogFooter className="mt-4 border-0 bg-transparent p-0">
              <Button type="button" variant="ghost" size="sm" onClick={() => onOpenChange(false)}>
                {t(($) => $.code.cancel)}
              </Button>
              <Button type="submit" size="sm" disabled={!url.trim() || pending !== null}>
                {t(($) => $.code.submit)}
              </Button>
            </DialogFooter>
          </form>
        ) : (
          <div className="px-5 py-4">
            <Input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder={t(($) => $.code.search_placeholder)}
              aria-label={t(($) => $.code.search_placeholder)}
            />
            {installationList.length > 1 ? (
              <select
                // eslint-disable-next-line no-restricted-syntax -- brand name, not translated copy
                aria-label="GitHub"
                className="mt-2 w-full rounded-md border bg-background px-2 py-1 text-caption"
                value={activeInstallation?.id ?? ""}
                onChange={(event) => setInstallationId(event.target.value)}
              >
                {installationList.map((installation) => (
                  <option key={installation.id} value={installation.id}>
                    {installation.account_login}
                  </option>
                ))}
              </select>
            ) : null}
            <div className="mt-2 max-h-72 overflow-y-auto">
              {items.length === 0 ? (
                <p className="py-6 text-center text-caption text-muted-foreground">
                  {githubRepos.isPending && activeInstallation
                    ? t(($) => $.code.loading)
                    : t(($) => $.code.search_empty)}
                </p>
              ) : (
                items.map((item) => {
                  const isAttached = attached.has(identityOf(item.url));
                  return (
                    <div
                      key={item.url}
                      className="flex items-center justify-between gap-3 border-t py-2.5 first:border-t-0"
                    >
                      <span className="min-w-0 truncate text-caption" title={item.url}>
                        {item.label}
                      </span>
                      {isAttached ? (
                        <span className="text-caption text-muted-foreground">
                          {t(($) => $.code.pick_added)}
                        </span>
                      ) : (
                        <Button
                          type="button"
                          variant="outline"
                          size="sm"
                          className="h-7 px-2.5 text-caption"
                          disabled={pending !== null}
                          onClick={() => void choose(item.url)}
                        >
                          {pending === item.url ? (
                            <LoaderCircle className="size-3 animate-spin" />
                          ) : (
                            t(($) => $.code.pick_select)
                          )}
                        </Button>
                      )}
                    </div>
                  );
                })
              )}
              {githubRepos.hasNextPage ? (
                <div className="flex justify-center border-t pt-2">
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    disabled={githubRepos.isFetchingNextPage}
                    onClick={() => void githubRepos.fetchNextPage()}
                  >
                    {t(($) => $.code.load_more)}
                  </Button>
                </div>
              ) : null}
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
