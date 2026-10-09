"use client";

import { paths } from "@multica/core/paths";
import type { InboxItem, InboxItemType } from "@multica/core/types";
import { buttonVariants } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";

// Workspace link requests and their receipts (DENE-1641). They carry no
// issue: the request is answered in Settings → Linked workspaces of the
// workspace it was offered to, the receipt points back to the offering side.

export function isWorkspaceLinkNotice(type: InboxItemType): boolean {
  return (
    type === "workspace_link_request" ||
    type === "workspace_link_accepted" ||
    type === "workspace_link_declined"
  );
}

/** The settings page and section that answer (or show) this notice. */
export function workspaceLinkNoticeHref(item: InboxItem): string | null {
  const details = item.details ?? {};
  const request = item.type === "workspace_link_request";
  const slug = request ? details.target_slug : details.source_slug;
  if (!slug) return null;
  const section = request ? "incoming" : "outgoing";
  return `${paths.workspace(slug).settings()}?tab=workspace-links&section=${section}`;
}

/** Localized one-line title; falls back to the server's English title. */
export function useWorkspaceLinkNoticeTitle(): (item: InboxItem) => string | null {
  const { t } = useT("inbox");
  return (item) => {
    if (!isWorkspaceLinkNotice(item.type)) return null;
    const details = item.details ?? {};
    const source = details.source_name;
    const target = details.target_name;
    if (item.type === "workspace_link_request" && source) {
      return t(($) => $.labels.link_requested_by, { source });
    }
    if (item.type === "workspace_link_accepted" && target) {
      return t(($) => $.labels.link_accepted_by, { target });
    }
    if (item.type === "workspace_link_declined" && target) {
      return t(($) => $.labels.link_declined_by, { target });
    }
    return item.title;
  };
}

/** Detail pane body: what is shared and the way to the answer. */
export function WorkspaceLinkNotice({ item }: { item: InboxItem }) {
  const { t } = useT("inbox");
  const href = workspaceLinkNoticeHref(item);
  const projects = item.details?.project_titles;
  const request = item.type === "workspace_link_request";
  return (
    <>
      {request && projects ? (
        <p className="mt-4 text-body text-foreground">
          {t(($) => $.detail.link_projects, { projects })}
        </p>
      ) : null}
      {href && !item.archived ? (
        <AppLink
          href={href}
          className={cn(buttonVariants({ size: "sm", variant: request ? "default" : "outline" }), "mt-4")}
        >
          {request ? t(($) => $.detail.link_answer) : t(($) => $.detail.link_open)}
        </AppLink>
      ) : null}
    </>
  );
}
