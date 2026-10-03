"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { LoaderCircle, Plus, Search, Trash2 } from "lucide-react";
import { Input } from "@multica/ui/components/ui/input";
import { Button } from "@multica/ui/components/ui/button";
import { Badge } from "@multica/ui/components/ui/badge";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { toast } from "sonner";
import {
  useInfiniteQuery,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useCurrentMember } from "@multica/core/permissions";
import { workspaceKeys } from "@multica/core/workspace/queries";
import {
  githubInstallationRepositoriesOptions,
  githubInstallationsOptions,
} from "@multica/core/github";
import { ApiError, api } from "@multica/core/api";
import { repoConnectionsOptions } from "@multica/core/repo-reach";
import {
  parseRepoLocator,
  repoLinkKeys,
  repoLinkTitle,
  resolveRepoLink,
} from "@multica/core/repo-links";
import type {
  GitHubRepository,
  Workspace,
  WorkspaceRepo,
} from "@multica/core/types";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import {
  SettingsCard,
  SettingsSaveState,
  SettingsSection,
} from "./settings-layout";
import { useAutoSave } from "./use-auto-save";
import { GitHubMark } from "./github-mark";
import { ShareScopeDialog, ShareScopeTrigger } from "../../common/share-scope-dialog";
import { settingsHref } from "./settings-navigation";
import {
  RepositoryConnectionControls,
  repositorySourceLine,
} from "./repository-connection";
import { outcomeText, useRepoLinkLabels } from "./repo-link-present";
import { useRepoCatalog } from "./use-repo-catalog";
import { RepoReachControls } from "./repo-reach-view";

const EMPTY_REPOSITORIES: WorkspaceRepo[] = [];

function repositoriesEqual(left: WorkspaceRepo[], right: WorkspaceRepo[]) {
  if (left.length !== right.length) return false;
  return left.every(
    (repo, index) =>
      repo.url === right[index]?.url &&
      (repo.description ?? "") === (right[index]?.description ?? ""),
  );
}

/** Same identity the connection matcher uses: host lowercased, path casing kept. */
export function repositoryIdentity(rawURL: string): string | null {
  const locator = parseRepoLocator(rawURL);
  if (!locator || locator.owner === "*") return null;
  return locator.name;
}

/**
 * The repositories agents may clone and push to, shown on the Code page. kun
 * keeps inline rows here (not upstream's add/edit dialog) because each row
 * carries its connection status, reach and share scope.
 */
export function RepositoriesSection() {
  const { t } = useT("settings");
  const linkLabels = useRepoLinkLabels();
  const workspace = useCurrentWorkspace();
  const wsId = useWorkspaceId();
  const queryClient = useQueryClient();
  const navigation = useNavigation();
  const { role } = useCurrentMember(wsId);
  const [repositories, setRepositories] = useState<WorkspaceRepo[]>(
    workspace?.repos ?? EMPTY_REPOSITORIES,
  );
  const [pendingRemovalIndex, setPendingRemovalIndex] = useState<number | null>(null);
  const [connectingGitHub, setConnectingGitHub] = useState(false);
  const [githubPickerOpen, setGitHubPickerOpen] = useState(false);
  const [selectedInstallationID, setSelectedInstallationID] = useState("");
  const [selectedRepositories, setSelectedRepositories] = useState<
    Map<number, GitHubRepository>
  >(new Map());
  const [repositorySearch, setRepositorySearch] = useState("");
  const [shareScopeIndex, setShareScopeIndex] = useState<number | null>(null);
  const [shareAudienceSizes, setShareAudienceSizes] = useState<Record<string, number>>({});
  const [testingUrl, setTestingUrl] = useState<string | null>(null);
  const catalog = useRepoCatalog(wsId);
  const { data: connectionCards } = useQuery(repoConnectionsOptions(wsId));
  const reachOf = (url: string) => {
    const identity = repositoryIdentity(url)?.toLowerCase();
    return (connectionCards ?? []).find(
      (card) =>
        card.url === url ||
        (identity !== undefined &&
          repositoryIdentity(card.url)?.toLowerCase() === identity),
    )?.reach;
  };
  const showReach = (connectionCards?.length ?? 0) > 0;
  const showLinks = catalog.source === "catalog" || catalog.source === "legacy";

  const canManageWorkspace = role === "owner" || role === "admin";
  const {
    data: githubData,
    isPending: githubInstallationsPending,
    isFetching: githubInstallationsFetching,
  } = useQuery({
    ...githubInstallationsOptions(wsId),
    enabled: !!wsId && canManageWorkspace,
  });
  const githubInstallations = useMemo(
    () => githubData?.installations ?? [],
    [githubData?.installations],
  );
  const githubConnectConfigured = githubData?.configured === true;
  const githubBrowseConfigured =
    githubData?.repository_browse_configured === true;
  // No App on this server yet: the owner creates it on the Connections tab,
  // so the button leads there instead of sitting disabled with no way out.
  const githubAppMissing =
    githubData !== undefined &&
    !githubConnectConfigured &&
    !githubBrowseConfigured &&
    githubInstallations.length === 0;
  const isWorkspaceOwner = role === "owner";
  const githubRepositoriesQuery = useInfiniteQuery({
    ...githubInstallationRepositoriesOptions(wsId, selectedInstallationID),
    enabled:
      githubPickerOpen &&
      canManageWorkspace &&
      githubBrowseConfigured &&
      !!selectedInstallationID,
  });
  const githubRepositories = useMemo(
    () =>
      githubRepositoriesQuery.data?.pages.flatMap(
        (page) => page.repositories,
      ) ?? [],
    [githubRepositoriesQuery.data?.pages],
  );
  const existingRepositoryIdentities = useMemo(
    () =>
      new Set(
        repositories
          .map((repository) => repositoryIdentity(repository.url))
          .filter((identity): identity is string => !!identity),
      ),
    [repositories],
  );
  const filteredGitHubRepositories = useMemo(() => {
    const search = repositorySearch.trim().toLowerCase();
    if (!search) return githubRepositories;
    return githubRepositories.filter((repository) =>
      repository.full_name.toLowerCase().includes(search),
    );
  }, [githubRepositories, repositorySearch]);

  useEffect(() => {
    setRepositories(workspace?.repos ?? EMPTY_REPOSITORIES);
    // A cache update after auto-save replaces the Workspace object. Keying on
    // identity prevents that response from wiping a newer local keystroke.
    // eslint-disable-next-line react-hooks/exhaustive-deps -- intentionally keyed on workspace identity
  }, [workspace?.id]);

  useEffect(() => {
    if (
      selectedInstallationID &&
      githubInstallations.some(
        (installation) => installation.id === selectedInstallationID,
      )
    ) {
      return;
    }
    setSelectedInstallationID(githubInstallations[0]?.id ?? "");
  }, [githubInstallations, selectedInstallationID]);

  useEffect(() => {
    const connected = navigation.searchParams.get("github_connected") === "1";
    const githubError = navigation.searchParams.get("github_error");
    if ((!connected && !githubError) || !canManageWorkspace) return;
    if (
      !githubError &&
      (githubInstallationsPending || githubInstallationsFetching)
    ) {
      return;
    }

    if (githubError === "installation_taken") {
      toast.error(t(($) => $.repositories.github_installation_taken));
    } else if (githubError) {
      toast.error(t(($) => $.repositories.github_connect_failed));
    } else if (githubInstallations.length > 0 && githubBrowseConfigured) {
      setSelectedInstallationID(githubInstallations[0]!.id);
      setGitHubPickerOpen(true);
    } else if (githubInstallations.length > 0) {
      toast.error(t(($) => $.repositories.github_browse_not_configured));
    }

    const next = new URLSearchParams(navigation.searchParams);
    next.delete("github_connected");
    next.delete("github_error");
    const search = next.toString();
    navigation.replace(`${navigation.pathname}${search ? `?${search}` : ""}`);
  }, [
    canManageWorkspace,
    githubBrowseConfigured,
    githubInstallations,
    githubInstallationsFetching,
    githubInstallationsPending,
    navigation,
    t,
  ]);

  const savedRepositories = workspace?.repos ?? EMPTY_REPOSITORIES;
  const draft = useMemo(() => repositories, [repositories]);
  const saveRepositories = useCallback(
    async (next: WorkspaceRepo[]) => {
      if (!workspace) return;
      const updated = await api.updateWorkspace(workspace.id, { repos: next });
      queryClient.setQueryData(
        workspaceKeys.list(),
        (old: Workspace[] | undefined) =>
          old?.map((item) => (item.id === updated.id ? updated : item)),
      );
    },
    [queryClient, workspace],
  );
  const allUrlsValid = repositories.every((repo) => repo.url.trim().length > 0);
  const autoSave = useAutoSave({
    value: draft,
    savedValue: savedRepositories,
    onSave: saveRepositories,
    onSuccess: () =>
      toast.success(t(($) => $.repositories.toast_saved), {
        id: "settings-auto-save",
      }),
    onError: (error) =>
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.repositories.toast_save_failed),
      ),
    enabled: !!workspace && canManageWorkspace && allUrlsValid,
    isEqual: repositoriesEqual,
  });

  const updateRepository = (
    index: number,
    field: keyof WorkspaceRepo,
    value: string,
  ) => {
    setRepositories((current) =>
      current.map((repo, repoIndex) =>
        repoIndex === index ? { ...repo, [field]: value } : repo,
      ),
    );
  };

  const addRepository = () => {
    setRepositories((current) => [...current, { url: "" }]);
  };

  const openGitHubPicker = () => {
    setSelectedInstallationID(
      selectedInstallationID || githubInstallations[0]?.id || "",
    );
    setGitHubPickerOpen(true);
  };

  const handleGitHubAction = async () => {
    if (githubInstallations.length > 0) {
      openGitHubPicker();
      return;
    }
    setConnectingGitHub(true);
    try {
      const response = await api.getGitHubConnectURL(wsId, "repositories");
      if (!response.configured || !response.url) {
        toast.error(t(($) => $.repositories.github_not_configured));
        return;
      }
      window.open(response.url, "_blank", "noopener");
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.repositories.github_connect_failed),
      );
    } finally {
      setConnectingGitHub(false);
    }
  };

  const closeGitHubPicker = () => {
    setGitHubPickerOpen(false);
    setSelectedRepositories(new Map());
    setRepositorySearch("");
  };

  const toggleGitHubRepository = (
    repository: GitHubRepository,
    checked: boolean,
  ) => {
    setSelectedRepositories((current) => {
      const next = new Map(current);
      if (checked) next.set(repository.id, repository);
      else next.delete(repository.id);
      return next;
    });
  };

  const importGitHubRepositories = () => {
    if (!allUrlsValid) {
      toast.error(t(($) => $.repositories.complete_manual_entry_first));
      return;
    }
    const additions: WorkspaceRepo[] = [];
    const known = new Set(existingRepositoryIdentities);
    for (const repository of selectedRepositories.values()) {
      const identity = repositoryIdentity(repository.clone_url);
      if (!identity || known.has(identity) || repository.archived) continue;
      known.add(identity);
      additions.push({
        url: repository.clone_url,
        ...(repository.description?.trim()
          ? { description: repository.description.trim() }
          : {}),
      });
    }
    if (additions.length === 0) {
      closeGitHubPicker();
      return;
    }
    const next = [...repositories, ...additions];
    setRepositories(next);
    autoSave.saveNow(next);
    closeGitHubPicker();
  };

  const removeRepository = (index: number) => {
    const next = repositories.filter((_, repoIndex) => repoIndex !== index);
    setRepositories(next);
    autoSave.saveNow(next);
  };

  const openConnect = (scope: string) => {
    const href = settingsHref(
      navigation.pathname,
      navigation.searchParams,
      "git-connections",
    );
    navigation.push(`${href}&connect_scope=${encodeURIComponent(scope)}`);
  };

  const testRepository = async (repoUrl: string) => {
    if (testingUrl) return;
    setTestingUrl(repoUrl);
    try {
      const result = await api.testRepoBinding(wsId, { repo_url: repoUrl });
      await queryClient.invalidateQueries({ queryKey: repoLinkKeys.all(wsId) });
      const text = outcomeText(
        result.ok,
        result.hint,
        result.next_command,
        t(($) => $.repo_links.toast_ok),
        t(($) => $.repo_links.toast_failed),
      );
      if (result.ok) toast.success(text);
      else toast.error(text);
    } catch (error) {
      if (error instanceof ApiError && (error.status === 404 || error.status === 405)) {
        toast.error(t(($) => $.repo_links.test_unavailable));
        return;
      }
      toast.error(
        error instanceof Error ? error.message : t(($) => $.repo_links.toast_failed),
      );
    } finally {
      setTestingUrl(null);
    }
  };

  const pinRepository = async (repoUrl: string, linkId: string | null) => {
    try {
      await api.pinRepoBinding(wsId, {
        repo_url: repoUrl,
        pinned_link_id: linkId,
      });
      await queryClient.invalidateQueries({ queryKey: repoLinkKeys.all(wsId) });
    } catch (error) {
      toast.error(
        error instanceof Error ? error.message : t(($) => $.repo_links.toast_failed),
      );
    }
  };

  if (!workspace) return null;
  const selectedRepository = shareScopeIndex === null ? null : repositories[shareScopeIndex] ?? null;

  return (
    <>
      <SettingsSection
        title={t(($) => $.repositories.section_title)}
        anchor="repositories"
        action={
          <SettingsSaveState
            status={autoSave.status}
            savingLabel={t(($) => $.auto_save.saving)}
            savedLabel={t(($) => $.auto_save.saved)}
            errorLabel={t(($) => $.auto_save.failed)}
          />
        }
      >
        <SettingsCard>
          {repositories.length === 0 ? (
            <div className="px-4 py-8 text-center text-caption text-muted-foreground">
              {t(($) => $.repositories.empty)}
            </div>
          ) : null}

          {showLinks && repositories.length > 0 ? (
            <div className="hidden gap-2 px-4 pt-3 text-caption text-muted-foreground sm:grid sm:grid-cols-[minmax(0,1.3fr)_minmax(0,0.9fr)_auto_auto]">
              <span>{t(($) => $.repo_links.column_repo)}</span>
              <span>
                {showReach
                  ? t(($) => $.repo_reach.column_projects)
                  : t(($) => $.repo_links.column_connection)}
              </span>
              <span>{t(($) => $.repo_links.column_status)}</span>
              <span />
            </div>
          ) : null}

          {catalog.source === "unavailable" ? (
            <p className="px-4 py-3 text-caption text-muted-foreground">
              {t(($) => $.repo_links.load_failed)}
            </p>
          ) : null}

          {repositories.map((repository, index) => {
            const savedUrl = savedRepositories[index]?.url ?? "";
            const urlDirty = repository.url.trim() !== savedUrl.trim();
            const match =
              showLinks && urlDirty
                ? resolveRepoLink(catalog.links, repository.url, null)
                : null;
            const preview = match
              ? match.link
                ? t(($) => $.repo_links.preview_match, {
                    name: repoLinkTitle(linkLabels.kind(match.link.kind), match.link),
                  })
                : t(($) => $.repo_links.preview_none)
              : "";
            const sourceLine = showLinks
              ? repositorySourceLine(catalog, repository.url)
              : "";
            const fields = (
              <>
                <Input
                  type="text"
                  name={`repository-${index}-url`}
                  autoComplete="off"
                  spellCheck={false}
                  aria-label={t(($) => $.repositories.url_placeholder)}
                  value={repository.url}
                  onChange={(event) =>
                    updateRepository(index, "url", event.target.value)
                  }
                  onBlur={autoSave.flush}
                  disabled={!canManageWorkspace}
                  aria-invalid={!repository.url.trim()}
                  placeholder={t(($) => $.repositories.url_placeholder)}
                  className="font-mono text-caption"
                />
                {sourceLine ? (
                  <p className="truncate text-caption text-muted-foreground">{sourceLine}</p>
                ) : null}
                <Input
                  type="text"
                  name={`repository-${index}-description`}
                  autoComplete="off"
                  aria-label={t(($) => $.repositories.description_placeholder)}
                  value={repository.description ?? ""}
                  onChange={(event) =>
                    updateRepository(index, "description", event.target.value)
                  }
                  onBlur={autoSave.flush}
                  disabled={!canManageWorkspace}
                  placeholder={t(($) => $.repositories.description_placeholder)}
                />
                {preview ? (
                  <p className="truncate text-caption text-muted-foreground">{preview}</p>
                ) : null}
              </>
            );
            const actions = canManageWorkspace ? (
              <div className="flex items-center justify-self-end gap-1">
                <ShareScopeTrigger
                  scope={repository.visibility}
                  audienceSize={shareAudienceSizes[repository.url]}
                  onClick={() => setShareScopeIndex(index)}
                />
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label={t(($) => $.repositories.delete_aria)}
                  className="text-muted-foreground hover:text-destructive"
                  onClick={() => setPendingRemovalIndex(index)}
                >
                  <Trash2 className="size-3.5" />
                </Button>
              </div>
            ) : null;
            const reach = showReach ? reachOf(repository.url) : undefined;
            return (
              <div
                key={index}
                className={
                  showLinks
                    ? "grid gap-2 px-4 py-3.5 sm:grid-cols-[minmax(0,1.3fr)_minmax(0,0.9fr)_auto_auto] sm:items-center"
                    : "grid gap-2 px-4 py-3.5 sm:grid-cols-[minmax(0,1fr)_minmax(0,0.8fr)_auto] sm:items-center"
                }
              >
                {showLinks ? <div className="min-w-0 space-y-1.5">{fields}</div> : fields}
                {reach ? (
                  <RepoReachControls reach={reach} trailing={actions} />
                ) : showLinks ? (
                  <RepositoryConnectionControls
                    repoUrl={repository.url}
                    catalog={catalog}
                    canManageWorkspace={canManageWorkspace}
                    testing={testingUrl === repository.url}
                    trailing={actions}
                    onTest={(url) => void testRepository(url)}
                    onConnect={openConnect}
                    onPin={(url, linkId) => void pinRepository(url, linkId)}
                  />
                ) : (
                  actions
                )}
              </div>
            );
          })}

          {canManageWorkspace ? (
            <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-3.5">
              <div className="flex flex-wrap items-center gap-2">
                <Button variant="outline" size="sm" onClick={addRepository}>
                  <Plus className="size-3.5" />
                  {t(($) => $.repositories.add)}
                </Button>
                {githubAppMissing ? (
                  <Button
                    size="sm"
                    onClick={() =>
                      navigation.push(
                        settingsHref(
                          navigation.pathname,
                          navigation.searchParams,
                          "git-connections",
                        ),
                      )
                    }
                    disabled={!isWorkspaceOwner}
                  >
                    <GitHubMark className="size-3.5" />
                    {t(($) => $.repositories.github_app_create)}
                  </Button>
                ) : (
                  <Button
                    size="sm"
                    onClick={handleGitHubAction}
                    disabled={
                      connectingGitHub ||
                      !githubBrowseConfigured ||
                      (!githubConnectConfigured &&
                        githubInstallations.length === 0)
                    }
                    title={
                      !githubBrowseConfigured
                        ? t(($) => $.repositories.github_browse_not_configured)
                        : undefined
                    }
                  >
                    {connectingGitHub ? (
                      <LoaderCircle className="size-3.5 animate-spin" />
                    ) : (
                      <GitHubMark className="size-3.5" />
                    )}
                    {githubInstallations.length > 0
                      ? t(($) => $.repositories.choose_from_github)
                      : t(($) => $.repositories.connect_github)}
                  </Button>
                )}
              </div>
              {!allUrlsValid ? (
                <span className="text-caption text-muted-foreground">
                  {t(($) => $.repositories.url_empty)}
                </span>
              ) : githubAppMissing ? (
                <span className="text-caption text-muted-foreground">
                  {isWorkspaceOwner
                    ? t(($) => $.repositories.github_app_missing_owner)
                    : t(($) => $.repositories.github_app_missing_member)}
                </span>
              ) : null}
            </div>
          ) : (
            <div className="px-4 py-3 text-caption text-muted-foreground">
              {t(($) => $.repositories.manage_hint)}
            </div>
          )}
        </SettingsCard>
      </SettingsSection>

      {selectedRepository && (
        <ShareScopeDialog
          open
          onOpenChange={(open) => {
            if (!open) setShareScopeIndex(null);
          }}
          target={{
            kind: "repo",
            resourceId: selectedRepository.url,
            currentScope: selectedRepository.visibility,
            audienceSize: shareAudienceSizes[selectedRepository.url],
            projectId: selectedRepository.project_id,
            resourceLabel: selectedRepository.url,
          }}
          onSaved={(result) => {
            const url = selectedRepository.url;
            setRepositories((current) => current.map((repo, index) =>
              index === shareScopeIndex ? { ...repo, visibility: result.visibility } : repo,
            ));
            if (result.audience_size !== undefined) {
              setShareAudienceSizes((current) => ({ ...current, [url]: result.audience_size! }));
            }
          }}
        />
      )}

      <Dialog
        open={githubPickerOpen}
        onOpenChange={(open) => {
          if (!open) closeGitHubPicker();
        }}
      >
        <DialogContent className="flex max-h-[85vh] flex-col gap-0 p-0 sm:max-w-2xl">
          <DialogHeader className="border-b px-6 py-5">
            <DialogTitle>
              {t(($) => $.repositories.github_picker_title)}
            </DialogTitle>
            <DialogDescription>
              {t(($) => $.repositories.github_picker_description)}
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-3 px-6 py-4">
            {githubInstallations.length > 1 ? (
              <Select
                items={githubInstallations.map((installation) => ({
                  value: installation.id,
                  label: installation.account_login,
                }))}
                value={selectedInstallationID}
                onValueChange={(value) =>
                  setSelectedInstallationID(value ?? "")
                }
              >
                <SelectTrigger
                  aria-label={t(($) => $.repositories.github_account)}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {githubInstallations.map((installation) => (
                    <SelectItem
                      key={installation.id}
                      value={installation.id}
                    >
                      {installation.account_login}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            ) : githubInstallations[0] ? (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.repositories.github_account)}:{" "}
                <span className="font-medium text-foreground">
                  {githubInstallations[0].account_login}
                </span>
              </p>
            ) : null}

            <div className="relative">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={repositorySearch}
                onChange={(event) => setRepositorySearch(event.target.value)}
                placeholder={t(
                  ($) => $.repositories.github_search_placeholder,
                )}
                aria-label={t(
                  ($) => $.repositories.github_search_placeholder,
                )}
                className="pl-8"
              />
            </div>
          </div>

          <div className="min-h-0 flex-1 overflow-y-auto border-y">
            {githubRepositoriesQuery.isPending ? (
              <div className="flex items-center justify-center gap-2 px-6 py-12 text-body text-muted-foreground">
                <LoaderCircle className="size-4 animate-spin" />
                {t(($) => $.repositories.github_loading)}
              </div>
            ) : githubRepositoriesQuery.isError ? (
              <div className="px-6 py-12 text-center text-body text-muted-foreground">
                {t(($) => $.repositories.github_load_failed)}
              </div>
            ) : filteredGitHubRepositories.length === 0 ? (
              <div className="px-6 py-12 text-center text-body text-muted-foreground">
                {repositorySearch
                  ? t(($) => $.repositories.github_no_search_results)
                  : t(($) => $.repositories.github_empty)}
              </div>
            ) : (
              <div className="divide-y">
                {filteredGitHubRepositories.map((repository) => {
                  const identity = repositoryIdentity(repository.clone_url);
                  const alreadyAdded =
                    !!identity && existingRepositoryIdentities.has(identity);
                  const disabled = alreadyAdded || repository.archived;
                  return (
                    <label
                      key={repository.id}
                      htmlFor={`github-repository-${repository.id}`}
                      className="flex items-start gap-3 px-6 py-3.5"
                    >
                      <Checkbox
                        id={`github-repository-${repository.id}`}
                        checked={
                          alreadyAdded ||
                          selectedRepositories.has(repository.id)
                        }
                        disabled={disabled}
                        onCheckedChange={(checked) =>
                          toggleGitHubRepository(
                            repository,
                            checked === true,
                          )
                        }
                        className="mt-0.5"
                      />
                      <span className="min-w-0 flex-1 space-y-1">
                        <span className="flex flex-wrap items-center gap-2">
                          <span className="truncate text-body font-medium">
                            {repository.full_name}
                          </span>
                          {repository.private ? (
                            <Badge variant="secondary">
                              {t(($) => $.repositories.github_private)}
                            </Badge>
                          ) : null}
                          {repository.archived ? (
                            <Badge variant="outline">
                              {t(($) => $.repositories.github_archived)}
                            </Badge>
                          ) : null}
                          {alreadyAdded ? (
                            <Badge variant="outline">
                              {t(($) => $.repositories.github_added)}
                            </Badge>
                          ) : null}
                        </span>
                        {repository.description ? (
                          <span className="block truncate text-caption text-muted-foreground">
                            {repository.description}
                          </span>
                        ) : null}
                      </span>
                    </label>
                  );
                })}
              </div>
            )}

            {githubRepositoriesQuery.hasNextPage ? (
              <div className="flex justify-center border-t p-3">
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => githubRepositoriesQuery.fetchNextPage()}
                  disabled={githubRepositoriesQuery.isFetchingNextPage}
                >
                  {githubRepositoriesQuery.isFetchingNextPage
                    ? t(($) => $.repositories.github_loading)
                    : t(($) => $.repositories.github_load_more)}
                </Button>
              </div>
            ) : null}
          </div>

          <DialogFooter className="m-0 border-t bg-muted/30 px-6 py-4">
            <p className="mr-auto text-caption text-muted-foreground">
              {t(($) => $.repositories.github_selected_count, {
                count: selectedRepositories.size,
              })}
            </p>
            <Button variant="ghost" onClick={closeGitHubPicker}>
              {t(($) => $.repositories.github_cancel)}
            </Button>
            <Button
              onClick={importGitHubRepositories}
              disabled={selectedRepositories.size === 0 || !allUrlsValid}
            >
              {t(($) => $.repositories.github_import)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <AlertDialog
        open={pendingRemovalIndex !== null}
        onOpenChange={(open) => {
          if (!open) setPendingRemovalIndex(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.repositories.delete_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.repositories.delete_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>
              {t(($) => $.repositories.delete_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                if (pendingRemovalIndex !== null) {
                  removeRepository(pendingRemovalIndex);
                }
                setPendingRemovalIndex(null);
              }}
            >
              {t(($) => $.repositories.delete_confirm_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
