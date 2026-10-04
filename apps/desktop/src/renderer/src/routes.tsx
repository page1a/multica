import { lazy, Suspense, useEffect } from "react";
import { createMemoryRouter, Outlet, useMatches } from "react-router-dom";
import type { RouteObject } from "react-router-dom";
const IssueDetailPage = lazy(() => import("./pages/issue-detail-page").then((m) => ({ default: m.IssueDetailPage })));
const ProjectDetailPage = lazy(() => import("./pages/project-detail-page").then((m) => ({ default: m.ProjectDetailPage })));
const AutopilotDetailPage = lazy(() => import("./pages/autopilot-detail-page").then((m) => ({ default: m.AutopilotDetailPage })));
const SkillDetailPage = lazy(() => import("./pages/skill-detail-page").then((m) => ({ default: m.SkillDetailPage })));
const AgentDetailPage = lazy(() => import("./pages/agent-detail-page").then((m) => ({ default: m.AgentDetailPage })));
const AiBuilderSessionPage = lazy(() => import("./pages/ai-builder-session-page").then((m) => ({ default: m.AiBuilderSessionPage })));
const IssueDraftPage = lazy(() => import("./pages/issue-draft-page").then((m) => ({ default: m.IssueDraftPage })));
const MemberDetailPage = lazy(() => import("./pages/member-detail-page").then((m) => ({ default: m.MemberDetailPage })));
const RuntimeDetailPage = lazy(() => import("./pages/runtime-detail-page").then((m) => ({ default: m.RuntimeDetailPage })));
const RuntimeSettingsPage = lazy(() => import("./pages/runtime-detail-page").then((m) => ({ default: m.RuntimeSettingsPage })));
const AttachmentPreviewRoute = lazy(() => import("./pages/attachment-preview-page").then((m) => ({ default: m.AttachmentPreviewRoute })));
const IssuesPage = lazy(() => import("@multica/views/issues/components").then((m) => ({ default: m.IssuesPage })));
const ProjectsPage = lazy(() => import("@multica/views/projects/components").then((m) => ({ default: m.ProjectsPage })));
const DashboardPage = lazy(() => import("@multica/views/dashboard").then((m) => ({ default: m.DashboardPage })));
const LinkedWorkspacesPage = lazy(() => import("@multica/views/workspace-links").then((m) => ({ default: m.LinkedWorkspacesPage })));
const AutopilotsPage = lazy(() => import("@multica/views/autopilots/components").then((m) => ({ default: m.AutopilotsPage })));
const MyIssuesPage = lazy(() => import("@multica/views/my-issues").then((m) => ({ default: m.MyIssuesPage })));
const SkillsPage = lazy(() => import("@multica/views/skills").then((m) => ({ default: m.SkillsPage })));
const DesktopRuntimesPage = lazy(() => import("./components/desktop-runtimes-page").then((m) => ({ default: m.DesktopRuntimesPage })));
const DesktopAgentsPage = lazy(() => import("./components/desktop-agents-page").then((m) => ({ default: m.DesktopAgentsPage })));
const AiCreateAgentPage = lazy(() => import("@multica/views/agents").then((m) => ({ default: m.AiCreateAgentPage })));
const ChooseCreateMethodPage = lazy(() => import("@multica/views/agents").then((m) => ({ default: m.ChooseCreateMethodPage })));
const ManualCreateAgentPage = lazy(() => import("@multica/views/agents").then((m) => ({ default: m.ManualCreateAgentPage })));
const SquadsPage = lazy(() => import("@multica/views/squads/components").then((m) => ({ default: m.SquadsPage })));
const SquadDetailPageView = lazy(() => import("@multica/views/squads/components").then((m) => ({ default: m.SquadDetailPage })));
const InboxPage = lazy(() => import("@multica/views/inbox").then((m) => ({ default: m.InboxPage })));
const ChatPage = lazy(() => import("@multica/views/chat").then((m) => ({ default: m.ChatPage })));
const SettingsPage = lazy(() => import("@multica/views/settings").then((m) => ({ default: m.SettingsPage })));
import { useT } from "@multica/views/i18n";
import { Download, Globe, Server } from "lucide-react";
import { DaemonSettingsTab } from "./components/daemon-settings-tab";
import { ServerSettingsTab } from "./components/server-settings-tab";
import { UpdatesSettingsTab } from "./components/updates-settings-tab";
import { WorkspaceRouteLayout } from "./components/workspace-route-layout";
import { DesktopRouteErrorPage } from "./components/route-error-page";

/**
 * Wraps `SettingsPage` so the desktop-only extra tabs can pull their labels
 * from i18n. The route element has to be a component (not a literal JSX
 * value) for `useT` to run.
 */
function DesktopSettingsRoute() {
  const { t } = useT("settings");
  return (
    <SettingsPage
      extraDeviceTabs={[
        {
          value: "server",
          label: t(($) => $.desktop.tabs.server),
          icon: Globe,
          content: <ServerSettingsTab />,
        },
        {
          value: "daemon",
          label: t(($) => $.desktop.daemon.title),
          icon: Server,
          content: <DaemonSettingsTab />,
        },
        {
          value: "updates",
          label: t(($) => $.desktop.tabs.updates),
          icon: Download,
          content: <UpdatesSettingsTab />,
        },
      ]}
    />
  );
}

/**
 * Sets document.title from the deepest matched route's handle.title.
 * The tab system observes document.title via MutationObserver.
 * Pages with dynamic titles (e.g. issue detail) override by setting
 * document.title directly via useDocumentTitle().
 */
function TitleSync() {
  const matches = useMatches();
  const title = [...matches]
    .reverse()
    .find((m) => (m.handle as { title?: string })?.title)
    ?.handle as { title?: string } | undefined;

  useEffect(() => {
    if (title?.title) document.title = title.title;
  }, [title?.title]);

  return null;
}

/** Wrapper that renders route children + TitleSync */
function PageShell() {
  return (
    <>
      <TitleSync />
      <Suspense
        fallback={
          <div className="flex min-h-[40vh] items-center justify-center" aria-live="polite">
            Loading…
          </div>
        }
      >
        <Outlet />
      </Suspense>
    </>
  );
}

/**
 * Route definitions shared by all tabs.
 *
 * Every tab path is workspace-scoped: `/{slug}/{route}/...`. Pre-workspace
 * flows (create workspace, accept invite) are NOT routes — they render as a
 * window-level overlay via `WindowOverlay`, dispatched by the navigation
 * adapter's transition-path interception. The `activeWorkspaceSlug` in the
 * tab store decides which workspace's tabs are visible in the TabBar;
 * workspace-less state (zero-workspace user) shows the overlay instead.
 *
 * The root index route stays as a harmless safety net. With per-workspace
 * tabs, nothing should construct a tab at `/` — but if one ever slips
 * through (malformed persisted state that dodges the migration, direct
 * router.navigate from unforeseen code), the index falls back to null
 * rather than 404; App.tsx's bootstrap repoints activeWorkspaceSlug on the
 * next render pass.
 */
export const appRoutes: RouteObject[] = [
  {
    element: <PageShell />,
    errorElement: <DesktopRouteErrorPage />,
    children: [
      { index: true, element: null },
      {
        path: ":workspaceSlug",
        element: <WorkspaceRouteLayout />,
        children: [
          // A bare `/{slug}` URL is normalized to `/{slug}/issues` by
          // sanitizeTabPath before it ever becomes a session, so the index
          // route is unreachable in practice; null keeps it a harmless
          // safety net instead of an in-router <Navigate> (MUL-4741
          // invariant 1: the router never self-navigates).
          { index: true, element: null },
          {
            path: "issues",
            element: <IssuesPage />,
            handle: { title: "Issues" },
          },
          {
            // Requirement alignment: a conversation before an issue exists.
            // Declared above `issues/:id` and longer than it, so the literal
            // `new` segment can never be read as an issue identifier.
            path: "issues/new/:draftId",
            element: <IssueDraftPage />,
            handle: { title: "Align Issue" },
          },
          {
            path: "issues/:id",
            element: <IssueDetailPage />,
            handle: { title: "Issue" },
          },
          {
            path: "projects",
            element: <ProjectsPage />,
            handle: { title: "Projects" },
          },
          {
            path: "projects/:id",
            element: <ProjectDetailPage />,
            handle: { title: "Project" },
          },
          {
            path: "autopilots",
            element: <AutopilotsPage />,
            handle: { title: "Autopilot" },
          },
          {
            path: "autopilots/:id",
            element: <AutopilotDetailPage />,
            handle: { title: "Autopilot" },
          },
          {
            path: "my-issues",
            element: <MyIssuesPage />,
            handle: { title: "My Issues" },
          },
          {
            path: "runtimes",
            element: <DesktopRuntimesPage />,
            handle: { title: "Runtimes" },
          },
          {
            path: "runtimes/:id",
            element: <RuntimeDetailPage />,
            handle: { title: "Machine" },
          },
          {
            path: "runtimes/:id/runtime/:runtimeId",
            element: <RuntimeSettingsPage />,
            handle: { title: "Runtime" },
          },
          { path: "skills", element: <SkillsPage />, handle: { title: "Skills" } },
          {
            path: "skills/:id",
            element: <SkillDetailPage />,
            handle: { title: "Skill" },
          },
          { path: "agents", element: <DesktopAgentsPage />, handle: { title: "Agents" } },
          {
            path: "agents/new",
            element: <ChooseCreateMethodPage />,
            handle: { title: "Create Agent" },
          },
          {
            path: "agents/new/manual",
            element: <ManualCreateAgentPage />,
            handle: { title: "Create Agent" },
          },
          {
            path: "agents/new/ai",
            element: <AiCreateAgentPage />,
            handle: { title: "Create Agent" },
          },
          {
            path: "agents/new/ai/:sessionId",
            element: <AiBuilderSessionPage />,
            handle: { title: "Create Agent" },
          },
          {
            path: "agents/:id",
            element: <AgentDetailPage />,
            handle: { title: "Agent" },
          },
          {
            path: "members/:id",
            element: <MemberDetailPage />,
            handle: { title: "Member" },
          },
          { path: "squads", element: <SquadsPage />, handle: { title: "Squads" } },
          {
            path: "squads/:id",
            element: <SquadDetailPageView />,
            handle: { title: "Squad" },
          },
          { path: "inbox", element: <InboxPage />, handle: { title: "Inbox" } },
          { path: "chat", element: <ChatPage />, handle: { title: "Chat" } },
          { path: "chat/:sessionId", element: <ChatPage />, handle: { title: "Chat" } },
          {
            path: "attachments/:id/preview",
            element: <AttachmentPreviewRoute />,
            handle: { title: "Attachment" },
          },
          {
            path: "usage",
            element: <DashboardPage />,
            handle: { title: "Usage" },
          },
          {
            path: "linked",
            element: <LinkedWorkspacesPage />,
            handle: { title: "Linked workspaces" },
          },
          {
            path: "settings",
            element: <DesktopSettingsRoute />,
            handle: { title: "Settings" },
          },
        ],
      },
    ],
  },
];

/**
 * Create THE app router (MUL-4741 single-router session architecture).
 * There is exactly one instance, owned by the tab Coordinator; it projects
 * the active tab session's URL and is never navigated by anything else.
 */
export function createAppRouter() {
  return createMemoryRouter(appRoutes, {
    initialEntries: ["/"],
  });
}
