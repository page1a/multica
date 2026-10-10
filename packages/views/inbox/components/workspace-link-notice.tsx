"use client";

import { useQuery } from "@tanstack/react-query";
import { paths } from "@multica/core/paths";
import type { InboxItem, InboxItemType } from "@multica/core/types";
import { useAcceptWorkspaceLink, useRevokeWorkspaceLink, workspaceLinksOptions } from "@multica/core/workspace-links";
import { Button, buttonVariants } from "@multica/ui/components/ui/button";
import { toast } from "sonner";
import { cn } from "@multica/ui/lib/utils";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";

// Workspace link requests and their receipts (DENE-1641). They carry no
// issue: a still-pending request can be accepted or declined right here when
// the reader may answer (the server's `can.accept`); otherwise it points to
// Settings → Linked workspaces. The receipt points back to the offering side.

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
  const { t: tw } = useT("workspace");
  const href = workspaceLinkNoticeHref(item);
  const projects = item.details?.project_titles;
  const request = item.type === "workspace_link_request";
  const wsId = request && !item.archived ? item.workspace_id : "";
  const { data } = useQuery(workspaceLinksOptions(wsId));
  const accept = useAcceptWorkspaceLink(wsId);
  const decline = useRevokeWorkspaceLink(wsId);
  const answering = accept.isPending || decline.isPending;
  const onError = (error: unknown) =>
    toast.error(error instanceof Error && error.message ? error.message : tw(($) => $.links.tab.failed));
  const pending = data?.links.find((link) => link.id === item.details?.link_id && link.status === "pending");
  const canAccept = !!pending && !!data?.can.accept;
  return (
    <>
      {request && projects ? (
        <p className="mt-4 text-body text-foreground">
          {t(($) => $.detail.link_projects, { projects })}
        </p>
      ) : null}
      {href && !item.archived ? (
        <div className="mt-4 flex flex-wrap gap-2">
          {canAccept ? (
            <>
              <Button size="sm" disabled={answering} onClick={() => accept.mutate(pending.id, { onError })}>
                {tw(($) => $.links.tab.accept)}
              </Button>
              <Button size="sm" variant="outline" disabled={answering} onClick={() => decline.mutate(pending.id, { onError })}>
                {tw(($) => $.links.tab.decline)}
              </Button>
            </>
          ) : null}
          <AppLink
            href={href}
            className={cn(buttonVariants({ size: "sm", variant: request && !canAccept ? "default" : "outline" }))}
          >
            {request ? t(($) => $.detail.link_answer) : t(($) => $.detail.link_open)}
          </AppLink>
        </div>
      ) : null}
    </>
  );
}
