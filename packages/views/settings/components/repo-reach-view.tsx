"use client";

import type { ReactNode } from "react";
import { parseRepoLocator } from "@multica/core/repo-links";
import { useWorkspacePaths } from "@multica/core/paths";
import type { RepoReach, RepoReachNextActionKind } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { RepoStatusText, type RepoStatusTone } from "./repo-link-present";

/** The step to take, looking through an `ask_owner` wrapper to the real one. */
function actionKind(reach: RepoReach): RepoReachNextActionKind | undefined {
  const action = reach.next_action;
  if (!action) return undefined;
  return action.kind === "ask_owner" ? action.for : action.kind;
}

/** Colour follows the server's mode; an expired token is the one red among the connected. */
export function reachTone(reach: RepoReach): RepoStatusTone {
  if (actionKind(reach) === "replace_token") return "bad";
  switch (reach.mode) {
    case "app":
    case "token":
      return "ok";
    case "cli":
      return "warn";
    default:
      return "bad";
  }
}

export function useRepoReachLabel() {
  const { t } = useT("settings");
  return (reach: RepoReach): string => {
    const account = reach.account_login;
    const kind = actionKind(reach);
    let status: string;
    switch (reach.mode) {
      case "app":
        status = account
          ? t(($) => $.repo_reach.status_app, { account })
          : t(($) => $.repo_reach.status_app_plain);
        break;
      case "token":
        status =
          kind === "replace_token"
            ? t(($) => $.repo_reach.status_token_broken)
            : account
              ? t(($) => $.repo_reach.status_token_named, { account })
              : t(($) => $.repo_reach.status_token);
        break;
      case "cli":
        status = t(($) => $.repo_reach.status_cli);
        break;
      default:
        status =
          kind === "install_app"
            ? t(($) => $.repo_reach.status_install_app)
            : t(($) => $.repo_reach.status_none);
    }
    if (reach.next_action?.kind !== "ask_owner") return status;
    const name = reach.next_action.contacts?.[0]?.name;
    return name
      ? t(($) => $.repo_reach.ask_owner, { status, name })
      : t(($) => $.repo_reach.ask_owner_generic, { status });
  };
}

/** Dot plus the one-line status the server's `mode` and `next_action` ask for. */
export function RepoReachStatus({ reach }: { reach: RepoReach }) {
  const label = useRepoReachLabel();
  return (
    <span title={reach.hint || undefined}>
      <RepoStatusText tone={reachTone(reach)}>{label(reach)}</RepoStatusText>
    </span>
  );
}

/**
 * The button for `next_action`. It only opens what the server named: the
 * install URL, or the connections page where a token or App is set up.
 * Nothing here decides whether a repository is connected.
 */
export function RepoReachAction({
  reach,
  fallback,
}: {
  reach: RepoReach;
  fallback?: ReactNode;
}) {
  const { t } = useT("settings");
  const navigation = useNavigation();
  const paths = useWorkspacePaths();
  const action = reach.next_action;
  const kind = actionKind(reach);
  if (!action || action.kind === "ask_owner" || !kind) return <>{fallback}</>;

  const label = {
    install_app: t(($) => $.repo_reach.action_install_app),
    create_app: t(($) => $.repo_reach.action_create_app),
    add_token: t(($) => $.repo_reach.action_add_token),
    replace_token: t(($) => $.repo_reach.action_replace_token),
    ask_owner: "",
  }[kind];

  const openConnections = () => {
    const locator = parseRepoLocator(reach.key);
    const scope =
      locator && locator.owner !== "*" ? `${locator.host}/${locator.owner}` : "";
    const query = scope ? `&connect_scope=${encodeURIComponent(scope)}` : "";
    navigation.push(`${paths.settings()}?tab=git-connections${query}`);
  };

  const onClick = () => {
    if (kind === "install_app" && action.url) {
      window.open(action.url, "_blank", "noopener");
      return;
    }
    openConnections();
  };

  return (
    <Button
      type="button"
      size="sm"
      variant={action.optional ? "outline" : "default"}
      className="h-7 shrink-0 px-2.5 text-caption"
      onClick={onClick}
    >
      {label}
    </Button>
  );
}

/** Titles of the projects that list this repository, as the server returned them. */
export function RepoReachProjects({ reach }: { reach: RepoReach }) {
  const { t } = useT("settings");
  const titles = reach.projects.map((project) => project.title).join(" · ");
  return (
    <span
      className="block min-w-0 truncate text-caption text-muted-foreground"
      title={titles || undefined}
    >
      {titles || t(($) => $.repo_reach.projects_none)}
    </span>
  );
}

/** Three grid cells for a settings row: projects, status, and the one button. */
export function RepoReachControls({
  reach,
  trailing,
}: {
  reach: RepoReach;
  trailing?: ReactNode;
}) {
  return (
    <>
      <RepoReachProjects reach={reach} />
      <RepoReachStatus reach={reach} />
      <div className="flex items-center justify-end gap-1">
        <RepoReachAction reach={reach} />
        {trailing}
      </div>
    </>
  );
}
