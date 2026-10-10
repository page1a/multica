"use client";

import type { ReactNode } from "react";
import { Menu, Plus } from "lucide-react";
import { useCurrentWorkspace, useWorkspacePaths } from "@multica/core/paths";
import { useInboxUnreadCount } from "@multica/core/inbox/queries";
import { openCreateIssueWithPreference } from "@multica/core/issues/stores/create-mode-store";
import { useSidebar } from "@multica/ui/components/ui/sidebar";
import { cn } from "@multica/ui/lib/utils";
import { AppLink, useNavigation } from "../navigation";
import { useT } from "../i18n";
import { WriteAction } from "./guest-readonly";
import { isNavActive, useChatNavUnreadCount } from "./nav-unread";
import { routeIconForPath } from "./route-icon-components";

const ITEM_CLASS =
  "relative flex min-h-11 min-w-11 flex-1 flex-col items-center justify-center gap-0.5 text-micro text-muted-foreground touch-manipulation";

/**
 * The phone's way around: the three places a phone is used for (Chat, Issues,
 * Inbox), "New issue" in the middle, and the rest of the nav behind "More".
 * Sits in the shell's flow under the page, so it never covers content; the
 * shell drops it while a text field has focus so the keyboard gets the room.
 */
export function MobileTabBar() {
  const { t } = useT("layout");
  const { pathname } = useNavigation();
  const workspace = useCurrentWorkspace();
  const p = useWorkspacePaths();
  const { setOpenMobile } = useSidebar();
  const inboxUnread = useInboxUnreadCount(workspace?.id);
  const chatUnread = useChatNavUnreadCount(workspace?.id, pathname, p.chat());

  return (
    <nav
      aria-label={t(($) => $.tab_bar.label)}
      data-testid="mobile-tab-bar"
      className="flex shrink-0 items-stretch border-t border-border bg-background px-1 pb-[env(safe-area-inset-bottom)]"
    >
      <TabLink href={p.chat()} pathname={pathname} label={t(($) => $.nav.chat)} badge={chatUnread} />
      <TabLink href={p.issues()} pathname={pathname} label={t(($) => $.nav.issues)} />
      <WriteAction className="flex flex-1">
        <button
          type="button"
          className={ITEM_CLASS}
          onClick={() => openCreateIssueWithPreference()}
        >
          <span className="flex size-7 items-center justify-center rounded-full bg-primary text-primary-foreground">
            <Plus className="size-4" aria-hidden="true" />
          </span>
          {t(($) => $.tab_bar.new_issue)}
        </button>
      </WriteAction>
      <TabLink href={p.inbox()} pathname={pathname} label={t(($) => $.nav.inbox)} badge={inboxUnread} />
      <button type="button" className={ITEM_CLASS} onClick={() => setOpenMobile(true)}>
        <span className="flex h-7 items-center">
          <Menu className="size-5" aria-hidden="true" />
        </span>
        {t(($) => $.tab_bar.more)}
      </button>
    </nav>
  );
}

function TabLink({
  href,
  pathname,
  label,
  badge = 0,
}: {
  href: string;
  pathname: string;
  label: string;
  badge?: number;
}) {
  const active = isNavActive(pathname, href);
  const Icon = routeIconForPath(href);
  return (
    <AppLink
      href={href}
      aria-current={active ? "page" : undefined}
      className={cn(ITEM_CLASS, active && "text-foreground")}
    >
      <span className="relative flex h-7 items-center">
        <Icon className="size-5" aria-hidden="true" />
        {badge > 0 ? <Badge>{badge > 99 ? "99+" : badge}</Badge> : null}
      </span>
      {label}
    </AppLink>
  );
}

function Badge({ children }: { children: ReactNode }) {
  return (
    <span className="absolute -right-2.5 top-0 min-w-4 rounded-full bg-primary px-1 text-center text-[10px] font-medium leading-4 text-primary-foreground tabular-nums">
      {children}
    </span>
  );
}
