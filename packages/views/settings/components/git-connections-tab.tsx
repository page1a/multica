"use client";

import { useEffect, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ApiError, api } from "@multica/core/api";
import { githubAppStatusOptions, githubKeys } from "@multica/core/github";
import { useWorkspaceId } from "@multica/core/hooks";
import { repoLinkKeys, parseRepoLocator, repoLinkScope, repoLinkTitle } from "@multica/core/repo-links";
import type { RepoLink, RepoLinkKind, RepoLinkVisibility } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
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
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { GitHubAppCreateFields, GitHubAppPanel, launchGitHubAppSetup } from "./github-app-panel";
import { SettingsTab } from "./settings-layout";
import { useRepoCatalog } from "./use-repo-catalog";
import { repoConnectionsOptions } from "@multica/core/repo-reach";
import type { RepoConnectionCard } from "@multica/core/types";
import {
  RepoStatusText,
  outcomeText,
  toneForHealth,
  useRepoLinkLabels,
} from "./repo-link-present";

const KINDS: RepoLinkKind[] = [
  "github_token",
  "github_app",
  "gitlab_token",
  "forgejo_token",
  "gitea_token",
];

export function GitConnectionsTab() {
  const { t } = useT("settings");
  const labels = useRepoLinkLabels();
  const wsId = useWorkspaceId();
  const navigation = useNavigation();
  const queryClient = useQueryClient();
  const catalog = useRepoCatalog(wsId);
  const [open, setOpen] = useState(false);
  const [kind, setKind] = useState<RepoLinkKind>("github_token");
  const [scope, setScope] = useState("");
  const [token, setToken] = useState("");
  const [visibility, setVisibility] = useState<RepoLinkVisibility>("personal");
  const [saving, setSaving] = useState(false);
  const [testingId, setTestingId] = useState<string | null>(null);
  const [removeTarget, setRemoveTarget] = useState<RepoLink | null>(null);
  const [removing, setRemoving] = useState(false);
  const [webhook, setWebhook] = useState<{ url: string; secret: string } | null>(null);
  const [org, setOrg] = useState("");
  const appQuery = useQuery(githubAppStatusOptions(wsId));
  const { data: connectionCards = [] } = useQuery(repoConnectionsOptions(wsId));
  const appStatus = appQuery.data;

  const canAdd = catalog.canAddPersonal || catalog.canAddWorkspace;

  const openDialog = (nextScope = "") => {
    const locator = parseRepoLocator(nextScope);
    setScope(nextScope);
    setKind(locator && locator.host !== "github.com" ? "gitlab_token" : "github_token");
    setToken("");
    setWebhook(null);
    setOrg("");
    setVisibility(
      catalog.canAddPersonal
        ? "personal"
        : "workspace",
    );
    setOpen(true);
  };

  useEffect(() => {
    if (catalog.isPending) return;
    const preset = navigation.searchParams.get("connect_scope");
    if (!preset) return;
    openDialog(preset);
    const next = new URLSearchParams(navigation.searchParams);
    next.delete("connect_scope");
    const search = next.toString();
    navigation.replace(`${navigation.pathname}${search ? `?${search}` : ""}`);
    // Consumed once: replace drops the preset so this does not reopen.
    // eslint-disable-next-line react-hooks/exhaustive-deps -- preset plus catalog readiness
  }, [navigation.searchParams, catalog.isPending]);

  async function refresh() {
    await queryClient.invalidateQueries({ queryKey: repoLinkKeys.all(wsId) });
    await queryClient.invalidateQueries({ queryKey: githubKeys.all(wsId) });
    await queryClient.invalidateQueries({ queryKey: ["vcs", wsId] });
  }

  useEffect(() => {
    const appError = navigation.searchParams.get("github_app_error");
    const githubError = navigation.searchParams.get("github_error");
    const connected = navigation.searchParams.get("github_connected") === "1";
    if (!appError && !githubError && !connected) return;
    if (appError === "expired") toast.error(t(($) => $.repo_links.github_app_error_expired));
    else if (appError === "duplicate") toast.error(t(($) => $.repo_links.github_app_error_duplicate));
    else if (appError === "not_owner") toast.error(t(($) => $.repo_links.github_app_not_owner_error));
    else if (appError) toast.error(t(($) => $.repo_links.github_app_error_failed));
    else if (githubError === "installation_taken") toast.error(t(($) => $.repo_links.github_app_error_taken));
    else if (githubError) toast.error(t(($) => $.repositories.github_connect_failed));
    else toast.success(t(($) => $.repo_links.github_app_connected));
    const next = new URLSearchParams(navigation.searchParams);
    next.delete("github_app_error");
    next.delete("github_error");
    next.delete("github_connected");
    const search = next.toString();
    navigation.replace(`${navigation.pathname}${search ? `?${search}` : ""}`);
  }, [navigation, t]);

  async function startGitHubApp() {
    if (saving) return;
    setSaving(true);
    try {
      const setup = await api.beginGitHubApp(wsId, org.trim());
      if (!setup.action_url) {
        toast.error(t(($) => $.repo_links.github_app_error_failed));
        return;
      }
      if (launchGitHubAppSetup(setup) === "browser") {
        toast.info(t(($) => $.repo_links.github_app_opened_in_browser), { duration: 10000 });
      }
    } catch (error) {
      toast.error(
        error instanceof Error ? error.message : t(($) => $.repo_links.github_app_error_failed),
      );
    } finally {
      setSaving(false);
    }
  }

  async function handleSave() {
    if (saving) return;
    if (kind === "github_app" && appStatus?.source === "none") {
      if (!appStatus.can_create) {
        toast.error(
          appStatus.block_reason === "not_owner"
            ? t(($) => $.repo_links.github_app_need_owner)
            : t(($) => $.repo_links.github_app_unavailable),
        );
        return;
      }
      await startGitHubApp();
      return;
    }
    const locator = parseRepoLocator(scope);
    if (!locator) {
      toast.error(t(($) => $.repo_links.scope_invalid));
      return;
    }
    if (kind !== "github_app" && !token.trim()) {
      toast.error(t(($) => $.repo_links.token_required));
      return;
    }
    const chosen: RepoLinkVisibility =
      visibility === "workspace" && catalog.canAddWorkspace
        ? "workspace"
        : "personal";
    setSaving(true);
    try {
      const response = await api.createRepoLink(wsId, {
        kind,
        scope: scope.trim(),
        visibility: chosen,
        ...(kind === "github_app" ? {} : { access_token: token.trim() }),
      });
      if (!response.link.id) {
        throw new Error(t(($) => $.repo_links.toast_failed));
      }
      await refresh();
      const installUrl = response.install_url || response.link.install_url;
      if (installUrl) {
        window.open(installUrl, "_blank", "noopener");
      }
      if (response.webhook_secret) {
        setWebhook({
          url: response.webhook_url || response.link.install_url || "",
          secret: response.webhook_secret,
        });
        setToken("");
      } else {
        setOpen(false);
      }
      toast.success(t(($) => $.repo_links.toast_saved));
    } catch (error) {
      if (error instanceof ApiError && (error.status === 404 || error.status === 405)) {
        try {
          const fellBack = await saveOnLegacy(locator.host);
          if (fellBack) return;
          if (kind === "github_token") {
            toast.error(t(($) => $.repo_links.token_unavailable));
            return;
          }
        } catch (legacyError) {
          toast.error(
            legacyError instanceof Error
              ? legacyError.message
              : t(($) => $.repo_links.toast_failed),
          );
          return;
        }
      }
      toast.error(
        error instanceof Error ? error.message : t(($) => $.repo_links.toast_failed),
      );
    } finally {
      setSaving(false);
    }
  }

  async function saveOnLegacy(host: string): Promise<boolean> {
    if (kind === "github_app") {
      const response = await api.getGitHubConnectURL(wsId);
      if (!response.configured || !response.url) {
        toast.error(t(($) => $.repositories.github_not_configured));
        return true;
      }
      window.open(response.url, "_blank", "noopener");
      setOpen(false);
      toast.success(t(($) => $.repo_links.toast_saved));
      return true;
    }
    if (kind === "gitlab_token" || kind === "forgejo_token" || kind === "gitea_token") {
      const instance = /^https?:\/\//i.test(scope.trim())
        ? scope.trim().replace(/\/$/, "")
        : `https://${host}`;
      const provider =
        kind === "gitlab_token" ? "gitlab" : kind === "gitea_token" ? "gitea" : "forgejo";
      const response = await api.connectVCS(wsId, {
        provider,
        instance_url: instance,
        access_token: token.trim(),
      });
      await refresh();
      if (response.webhook_secret) {
        setWebhook({
          url: response.webhook_url || response.webhook_path,
          secret: response.webhook_secret,
        });
        setToken("");
      } else {
        setOpen(false);
      }
      toast.success(t(($) => $.repo_links.toast_saved));
      return true;
    }
    return false;
  }

  async function handleTest(link: RepoLink) {
    if (testingId) return;
    setTestingId(link.id);
    try {
      if (link.health === "pending_install") {
        const url = link.install_url;
        if (url) {
          window.open(url, "_blank", "noopener");
          return;
        }
        if (link.kind === "github_app") {
          const response = await api.getGitHubConnectURL(wsId);
          if (response.url) {
            window.open(response.url, "_blank", "noopener");
            return;
          }
        }
      }
      const result = await api.testRepoLink(wsId, link.id);
      await refresh();
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
      setTestingId(null);
    }
  }

  async function handleRemove() {
    if (!removeTarget || removing) return;
    setRemoving(true);
    try {
      if (catalog.source === "legacy" && removeTarget.id.startsWith("github:")) {
        await api.deleteGitHubInstallation(wsId, removeTarget.id.slice("github:".length));
      } else if (catalog.source === "legacy" && removeTarget.id.startsWith("vcs:")) {
        await api.deleteVCSConnection(wsId, removeTarget.id.slice("vcs:".length));
      } else {
        await api.deleteRepoLink(wsId, removeTarget.id);
      }
      await refresh();
      toast.success(t(($) => $.repo_links.toast_removed));
      setRemoveTarget(null);
    } catch (error) {
      toast.error(
        error instanceof Error ? error.message : t(($) => $.repo_links.toast_failed),
      );
    } finally {
      setRemoving(false);
    }
  }

  const workspaceLinks = catalog.links.filter((link) => link.visibility !== "personal");
  const personalLinks = catalog.links.filter((link) => link.visibility === "personal");

  return (
    <SettingsTab title={t(($) => $.page.tabs.git_connections)}>
      <GitHubAppPanel
        status={appStatus}
        org={org}
        onOrg={setOrg}
        creating={saving}
        onCreate={() => void startGitHubApp()}
      />

      <div className="flex items-center justify-end">
        {canAdd ? (
          <Button size="sm" onClick={() => openDialog()}>
            {t(($) => $.repo_links.add_connection)}
          </Button>
        ) : null}
      </div>

      {catalog.source === "unavailable" ? (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.repo_links.load_failed)}
        </p>
      ) : null}

      {!catalog.isPending && catalog.source !== "unavailable" ? (
        <div className="space-y-8">
          <LinkGroup
            title={t(($) => $.repo_links.workspace_group)}
            links={workspaceLinks}
            empty={null}
            cards={connectionCards}
            testingId={testingId}
            onTest={handleTest}
            onRemove={setRemoveTarget}
          />
          <LinkGroup
            title={t(($) => $.repo_links.personal_group)}
            links={personalLinks}
            empty={t(($) => $.repo_links.personal_empty)}
            cards={connectionCards}
            testingId={testingId}
            onTest={handleTest}
            onRemove={setRemoveTarget}
          />
        </div>
      ) : null}

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t(($) => $.repo_links.dialog_title)}</DialogTitle>
          </DialogHeader>
          {webhook ? (
            <div className="space-y-3">
              <p className="text-caption text-muted-foreground">
                {t(($) => $.repo_links.webhook_once)}
              </p>
              {webhook.url ? (
                <CopyRow
                  label={t(($) => $.repo_links.webhook_url)}
                  value={webhook.url}
                />
              ) : null}
              <CopyRow
                label={t(($) => $.repo_links.webhook_secret)}
                value={webhook.secret}
              />
            </div>
          ) : kind === "github_app" && appStatus?.source === "none" ? (
            <GitHubAppCreateFields
              inputId="github-app-org-dialog"
              org={org}
              onOrg={setOrg}
              creating={saving}
              onCreate={() => void startGitHubApp()}
              showButton={false}
              blocked={
                appStatus.can_create
                  ? null
                  : appStatus.block_reason === "not_owner"
                    ? t(($) => $.repo_links.github_app_need_owner)
                    : appStatus.block_reason === "public_url_missing"
                      ? t(($) => $.repo_links.github_app_public_url)
                      : t(($) => $.repo_links.github_app_unavailable)
              }
            />
          ) : (
            <div className="space-y-4">
              <Field label={t(($) => $.repo_links.field_kind)}>
                <select
                  aria-label={t(($) => $.repo_links.field_kind)}
                  className="h-9 w-full rounded-lg border border-input bg-background px-3 text-body"
                  value={kind}
                  onChange={(event) => setKind(event.target.value as RepoLinkKind)}
                >
                  {KINDS.map((item) => (
                    <option key={item} value={item}>
                      {labels.kind(item)}
                    </option>
                  ))}
                </select>
              </Field>
              <Field label={t(($) => $.repo_links.field_scope)}>
                <Input
                  value={scope}
                  onChange={(event) => setScope(event.target.value)}
                  spellCheck={false}
                  autoComplete="off"
                  className="font-mono text-caption"
                  placeholder={t(($) => $.repo_links.scope_placeholder)}
                  aria-label={t(($) => $.repo_links.field_scope)}
                />
              </Field>
              {kind === "github_app" ? null : (
                <Field label={t(($) => $.repo_links.field_token)}>
                  <Input
                    type="password"
                    value={token}
                    onChange={(event) => setToken(event.target.value)}
                    autoComplete="off"
                    aria-label={t(($) => $.repo_links.field_token)}
                  />
                </Field>
              )}
              <Field label={t(($) => $.repo_links.field_visibility)}>
                <select
                  aria-label={t(($) => $.repo_links.field_visibility)}
                  className="h-9 w-full rounded-lg border border-input bg-background px-3 text-body"
                  value={visibility}
                  onChange={(event) =>
                    setVisibility(event.target.value as RepoLinkVisibility)
                  }
                >
                  {catalog.canAddPersonal ? (
                    <option value="personal">
                      {t(($) => $.repo_links.visibility_personal)}
                    </option>
                  ) : null}
                  {catalog.canAddWorkspace ? (
                    <option value="workspace">
                      {t(($) => $.repo_links.visibility_workspace)}
                    </option>
                  ) : null}
                </select>
              </Field>
            </div>
          )}
          <DialogFooter>
            <Button variant="ghost" onClick={() => setOpen(false)}>
              {t(($) => $.repo_links.cancel)}
            </Button>
            {webhook ? null : (
              <Button
                onClick={() => void handleSave()}
                disabled={
                  saving ||
                  (kind === "github_app" && appStatus?.source === "none"
                    ? !appStatus.can_create
                    : !canAdd)
                }
              >
                {saving
                  ? kind === "github_app" && appStatus?.source === "none"
                    ? t(($) => $.repo_links.github_app_creating)
                    : t(($) => $.repo_links.saving)
                  : kind === "github_app" && appStatus?.source === "none"
                    ? t(($) => $.repo_links.github_app_create)
                    : t(($) => $.repo_links.save_and_test)}
              </Button>
            )}
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <AlertDialog
        open={removeTarget !== null}
        onOpenChange={(next) => {
          if (!next) setRemoveTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.repo_links.remove_title)}</AlertDialogTitle>
            <AlertDialogDescription>
              {removeTarget
                ? t(($) => $.repo_links.remove_body, {
                    name: repoLinkTitle(labels.kind(removeTarget.kind), removeTarget),
                  })
                : null}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t(($) => $.repo_links.cancel)}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => void handleRemove()}
            >
              {t(($) => $.repo_links.remove_confirm)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsTab>
  );
}

function LinkGroup({
  title,
  links,
  empty,
  cards,
  testingId,
  onTest,
  onRemove,
}: {
  title: string;
  links: RepoLink[];
  empty: string | null;
  cards: RepoConnectionCard[];
  testingId: string | null;
  onTest: (link: RepoLink) => void;
  onRemove: (link: RepoLink) => void;
}) {
  const { t } = useT("settings");
  const labels = useRepoLinkLabels();
  return (
    <section>
      <h3 className="mb-2 text-caption font-medium text-muted-foreground">{title}</h3>
      <div className="border-t border-surface-border">
        {links.length === 0 && empty ? (
          <p className="py-3 text-caption text-muted-foreground">{empty}</p>
        ) : null}
        {links.map((link) => {
          const pending = link.health === "pending_install";
          return (
            <div
              key={link.id}
              className="grid grid-cols-[minmax(0,1fr)_auto_auto] items-center gap-3 border-b border-surface-border py-3"
            >
              <div className="min-w-0">
                <p className="truncate text-body">
                  {repoLinkTitle(labels.kind(link.kind), link)}
                </p>
                <p className="truncate font-mono text-caption text-muted-foreground">
                  {repoLinkScope(link)}
                </p>
                <LinkProjects link={link} cards={cards} />
              </div>
              <RepoStatusText tone={toneForHealth(link.health)}>
                {labels.health(link.health)}
              </RepoStatusText>
              <div className="flex items-center justify-end gap-1">
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-7 px-2 text-caption text-muted-foreground"
                  disabled={testingId === link.id}
                  onClick={() => onTest(link)}
                >
                  {pending
                    ? t(($) => $.repo_links.continue)
                    : t(($) => $.repo_links.test)}
                </Button>
                {link.can_manage ? (
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-7 px-2 text-caption text-muted-foreground"
                    onClick={() => onRemove(link)}
                  >
                    {t(($) => $.repo_links.remove)}
                  </Button>
                ) : null}
              </div>
            </div>
          );
        })}
      </div>
    </section>
  );
}

/** Whether the server says this connection is what reaches the repository. */
function cardUsesLink(card: RepoConnectionCard, link: RepoLink): boolean {
  const { reach } = card;
  if (reach.link_id && (link.id === reach.link_id || link.id === `vcs:${reach.link_id}`)) {
    return true;
  }
  return (
    link.kind === "github_app" &&
    reach.mode === "app" &&
    reach.account_login.toLowerCase() === (link.account_login ?? link.owner).toLowerCase()
  );
}

function LinkProjects({ link, cards }: { link: RepoLink; cards: RepoConnectionCard[] }) {
  const { t } = useT("settings");
  const titles = new Set<string>();
  for (const card of cards) {
    if (!cardUsesLink(card, link)) continue;
    for (const project of card.projects) titles.add(project.title);
  }
  if (titles.size === 0) return null;
  const line = [...titles].join(" · ");
  return (
    <p className="truncate text-caption text-muted-foreground" title={line}>
      {t(($) => $.repo_reach.accounts_projects)}: {line}
    </p>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="space-y-1.5">
      <Label className="text-caption text-muted-foreground">{label}</Label>
      {children}
    </div>
  );
}

function CopyRow({ label, value }: { label: string; value: string }) {
  const { t } = useT("settings");
  return (
    <div className="space-y-1.5">
      <p className="text-caption text-muted-foreground">{label}</p>
      <div className="flex items-center gap-2">
        <code className="min-w-0 flex-1 truncate font-mono text-caption">{value}</code>
        <Button
          variant="ghost"
          size="sm"
          className="h-7 px-2 text-caption"
          onClick={() => {
            void navigator.clipboard.writeText(value).then(
              () => toast.success(t(($) => $.repo_links.copied)),
              () => toast.error(t(($) => $.repo_links.copy_failed)),
            );
          }}
        >
          {t(($) => $.repo_links.copy)}
        </Button>
      </div>
    </div>
  );
}
