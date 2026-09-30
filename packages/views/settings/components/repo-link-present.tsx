"use client";

import type { ReactNode } from "react";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

export type RepoStatusTone = "ok" | "warn" | "idle" | "bad";

export function toneForState(state: string): RepoStatusTone {
  switch (state) {
    case "connected":
    case "ok":
      return "ok";
    case "cli":
      return "warn";
    case "pending_install":
      return "idle";
    default:
      return "bad";
  }
}

export function toneForHealth(health: string): RepoStatusTone {
  switch (health) {
    case "ok":
      return "ok";
    case "cli":
      return "warn";
    case "pending_install":
      return "idle";
    default:
      return "warn";
  }
}

export function RepoStatusText({
  tone,
  children,
}: {
  tone: RepoStatusTone;
  children: ReactNode;
}) {
  const dot = {
    ok: "bg-success",
    warn: "bg-warning",
    idle: "bg-muted-foreground/40",
    bad: "bg-destructive",
  }[tone];
  return (
    <span className="inline-flex items-center whitespace-nowrap text-caption text-foreground">
      <span aria-hidden className={cn("mr-1.5 inline-block size-1.5 shrink-0 rounded-full", dot)} />
      {children}
    </span>
  );
}

export function outcomeText(
  ok: boolean,
  hint: string | null | undefined,
  nextCommand: string | null | undefined,
  okLabel: string,
  failLabel: string,
): string {
  if (ok) return okLabel;
  const sentence = [hint, nextCommand]
    .map((part) => part?.trim())
    .filter((part): part is string => !!part)
    .join(" ");
  return sentence || failLabel;
}

export function useRepoLinkLabels() {
  const { t } = useT("settings");
  return {
    kind(kind: string) {
      switch (kind) {
        case "github_app":
          return t(($) => $.repo_links.kind_github_app);
        case "github_token":
          return t(($) => $.repo_links.kind_github_token);
        case "gitlab_token":
          return t(($) => $.repo_links.kind_gitlab_token);
        case "forgejo_token":
          return t(($) => $.repo_links.kind_forgejo_token);
        case "gitea_token":
          return t(($) => $.repo_links.kind_gitea_token);
        default:
          return t(($) => $.repo_links.kind_unknown);
      }
    },
    state(state: string) {
      switch (state) {
        case "connected":
          return t(($) => $.repo_links.status_connected);
        case "cli":
          return t(($) => $.repo_links.status_cli);
        case "pending_install":
          return t(($) => $.repo_links.status_pending);
        default:
          return t(($) => $.repo_links.status_disconnected);
      }
    },
    health(health: string) {
      switch (health) {
        case "ok":
          return t(($) => $.repo_links.health_ok);
        case "cli":
          return t(($) => $.repo_links.health_cli);
        case "pending_install":
          return t(($) => $.repo_links.health_pending);
        default:
          return t(($) => $.repo_links.health_cli);
      }
    },
  };
}
