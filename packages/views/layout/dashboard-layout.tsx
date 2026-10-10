"use client";

import type { ReactNode } from "react";
import { SidebarProvider, SidebarInset } from "@multica/ui/components/ui/sidebar";
import { cn } from "@multica/ui/lib/utils";
import { ModalRegistry } from "../modals/registry";
import { SourceBackfillModal } from "../onboarding";
import { AppSidebar } from "./app-sidebar";
import { DashboardGuard } from "./dashboard-guard";
import { NavigationProgress } from "./navigation-progress";
import { ProviderStatusBar } from "./provider-status-bar";
import { WorkspacePresencePrefetch } from "./workspace-presence-prefetch";
import { GlobalShortcuts } from "./global-shortcuts";
import { GuestBanner, GuestReadOnlyProvider } from "./guest-readonly";
import { MobileTabBar } from "./mobile-tab-bar";
import { useIsPhone, useTextEntryFocused } from "./use-is-phone";

interface DashboardLayoutProps {
  children: ReactNode;
  /** Rendered inside SidebarInset (e.g. ChatWindow, ChatFab — absolute-positioned overlays) */
  extra?: ReactNode;
  /** Rendered inside sidebar header as a search trigger */
  searchSlot?: ReactNode;
  /** Loading indicator */
  loadingIndicator?: ReactNode;
}

export function DashboardLayout({
  children,
  extra,
  searchSlot,
  loadingIndicator,
}: DashboardLayoutProps) {
  const isPhone = useIsPhone();
  // The keyboard takes the bottom of the screen; the bar steps aside rather
  // than sit between it and the field being typed into.
  const typing = useTextEntryFocused(isPhone);
  const showTabBar = isPhone && !typing;
  return (
    <DashboardGuard
      loadingFallback={
        <div className="flex h-svh items-center justify-center">
          {loadingIndicator}
        </div>
      }
    >
      <SidebarProvider className="h-svh bg-app-shell">
        <GuestReadOnlyProvider>
        <GlobalShortcuts />
        <WorkspacePresencePrefetch />
        <AppSidebar searchSlot={searchSlot} />
        {/* The page runs edge to edge (viewport-fit=cover), so the shell keeps
            its content — lists, composers, the quota bar — above the Home bar.
            On a phone the tab bar is the bottom edge and carries that inset. */}
        <SidebarInset
          className={cn(
            "relative overflow-hidden",
            !showTabBar && "pb-[env(safe-area-inset-bottom)]",
          )}
        >
          <div className="flex min-h-0 flex-1 flex-col">
            <NavigationProgress />
            <GuestBanner />
            {children}
          </div>
          <ProviderStatusBar />
          {showTabBar && <MobileTabBar />}
          <ModalRegistry />
          <SourceBackfillModal />
          {extra}
        </SidebarInset>
        </GuestReadOnlyProvider>
      </SidebarProvider>
    </DashboardGuard>
  );
}
