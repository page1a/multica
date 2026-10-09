import { describe, it, expect, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import type { ProjectMemoryLocation, ProjectMemoryMonitor, ProjectMemoryStatus } from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enProjects from "../../locales/en/projects.json";

const TEST_RESOURCES = { en: { common: enCommon, projects: enProjects } };

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/paths", () => ({ useWorkspacePaths: () => ({}) }));
vi.mock("../../navigation", () => ({ AppLink: ({ children }: { children: React.ReactNode }) => <a>{children}</a> }));

const mockGetProjectMemory = vi.hoisted(() => vi.fn());
// Older servers have no monitor route: the card falls back to recent_sediments.
const mockGetProjectMemoryMonitor = vi.hoisted(() => vi.fn(() => Promise.reject(new Error("404"))));
vi.mock("@multica/core/api", () => ({
  api: {
    getProjectMemory: (...args: unknown[]) => mockGetProjectMemory(...args),
    getProjectMemoryMonitor: (...args: unknown[]) => mockGetProjectMemoryMonitor(...(args as [])),
  },
}));

import { ProjectMemoryCard } from "./project-memory-card";

function location(key: string, path: string, overrides: Partial<ProjectMemoryLocation> = {}): ProjectMemoryLocation {
  return {
    key, path, kind: "file", exists: true, is_directory: false,
    modified_at: null, observed_at: null, error: null, mainline_ref: null, ...overrides,
  };
}

describe("ProjectMemoryCard", () => {
  it("tells a slot the mainline already has apart from one never written", async () => {
    const status: ProjectMemoryStatus = {
      project_id: "p-1", workspace_id: "ws-1", source: "local_directory",
      locations: [
        location("agents", "AGENTS.md"),
        location("docs_index", "docs/README.md", { exists: false, mainline_ref: "origin/dev" }),
        location("evidence_index", "docs/evidence/INDEX.md", { exists: false }),
      ],
      missing: ["docs_index", "evidence_index"], observed_at: null, latest_sediment_at: null,
      sediment_issue: null, sediment_agent_configured: true, sediment_error: null,
    };
    mockGetProjectMemory.mockResolvedValue(status);
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <I18nProvider locale="en" resources={TEST_RESOURCES}>
          <ProjectMemoryCard projectId="p-1" />
        </I18nProvider>
      </QueryClientProvider>,
    );
    const behind = await screen.findByText("Behind origin/dev");
    expect(behind.getAttribute("title")).toBe("Present on origin/dev; sync the local directory");
    expect(screen.getByText("Missing")).toBeTruthy();
    expect(screen.getByText("Five memory locations in the project's local directory")).toBeTruthy();
  });

  it("lists the deliveries that wrote the memory, with their files", async () => {
    const status: ProjectMemoryStatus = {
      project_id: "p-1", workspace_id: "ws-1", source: "local_directory",
      locations: [location("agents", "AGENTS.md")],
      missing: [], observed_at: null, latest_sediment_at: "2026-10-01T00:00:00Z",
      sediment_issue: null, sediment_agent_configured: true, sediment_error: null,
      recent_sediments: [
        {
          id: "s-1", source_kind: "issue", issue_id: "i-1", issue_identifier: "DENE-7", chat_session_id: null,
          source_title: "加词条", verified: true, mainline: "kun", commits: [], pr_url: "",
          author_type: "agent", author_id: "a-1", created_at: new Date().toISOString(),
          changes: [{ location: "context", summary: "term", files: ["CONTEXT.md"] }],
        },
        {
          id: "s-2", source_kind: "chat", issue_id: null, issue_identifier: null, chat_session_id: "abcd1234-0000",
          source_title: "Routing talk", verified: false, mainline: "main", commits: [], pr_url: "",
          author_type: "agent", author_id: "a-1", created_at: new Date().toISOString(),
          changes: [{ location: "agents", summary: "rule" }],
        },
      ],
    };
    mockGetProjectMemory.mockResolvedValue(status);
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <I18nProvider locale="en" resources={TEST_RESOURCES}>
          <ProjectMemoryCard projectId="p-1" />
        </I18nProvider>
      </QueryClientProvider>,
    );
    expect(await screen.findByText("Recent sediment")).toBeTruthy();
    expect(screen.getByText("DENE-7")).toBeTruthy();
    expect(screen.getByText("CONTEXT.md")).toBeTruthy();
    expect(screen.getByText("Chat Routing talk")).toBeTruthy();
    expect(screen.getByText("Not checked")).toBeTruthy();
    expect(screen.queryByText(/Last sediment/)).toBeNull();
  });

  it("shows what a boss-layer sediment rolled up and what each change did", async () => {
    const status: ProjectMemoryStatus = {
      project_id: "p-1", workspace_id: "ws-1", source: "local_directory",
      locations: [location("agents", "AGENTS.md")],
      missing: [], observed_at: null, latest_sediment_at: "2026-10-01T00:00:00Z",
      sediment_issue: null, sediment_agent_configured: true, sediment_error: null,
      recent_sediments: [
        {
          id: "s-1", source_kind: "issue", issue_id: "i-9", issue_identifier: "DENE-9", chat_session_id: null,
          source_title: "沉淀第 1 轮", verified: true, mainline: "kun", commits: [], pr_url: "",
          author_type: "agent", author_id: "a-1", created_at: new Date().toISOString(),
          layer: "boss",
          sources: [{ kind: "issue", id: "i-1", identifier: "DENE-1672", title: "回执贯通" }],
          changes: [
            { location: "agents", action: "update", entry: "工作单", summary: "s", files: ["AGENTS.md"] },
            { location: "agents", action: "supersede", entry: "旧派单", summary: "s", files: ["AGENTS.md"] },
          ],
        },
      ],
    };
    mockGetProjectMemory.mockResolvedValue(status);
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <I18nProvider locale="en" resources={TEST_RESOURCES}>
          <ProjectMemoryCard projectId="p-1" />
        </I18nProvider>
      </QueryClientProvider>,
    );
    expect(await screen.findByText("Rolled up from DENE-1672")).toBeTruthy();
    expect(screen.getByText("Updated「工作单」 · AGENTS.md")).toBeTruthy();
    expect(screen.getByText("Marked「旧派单」superseded · AGENTS.md")).toBeTruthy();
  });

  it("answers the monitor: deletions, closes that wrote nothing, idle rounds, chat receipts", async () => {
    const status: ProjectMemoryStatus = {
      project_id: "p-1", workspace_id: "ws-1", source: "local_directory",
      locations: [location("agents", "AGENTS.md")],
      missing: [], observed_at: null, latest_sediment_at: null,
      sediment_issue: null, sediment_agent_configured: true, sediment_error: null,
    };
    const now = new Date().toISOString();
    const monitor: ProjectMemoryMonitor = {
      project_id: "p-1", days: 14, since: now,
      writes: [
        {
          id: "s-1", source_kind: "issue", issue_id: "i-1", issue_identifier: "DENE-7", chat_session_id: null,
          source_title: "", verified: true, mainline: "kun", commits: [], pr_url: "", author_type: "agent",
          author_id: "a-1", created_at: now, changes: [{ location: "agents", summary: "s", files: ["AGENTS.md"] }],
          source_accessible: true, superseded: [], deleted_lines: 3,
        },
        {
          id: "s-2", source_kind: "chat", issue_id: null, issue_identifier: null, chat_session_id: "c-private",
          source_title: "", verified: true, mainline: "kun", commits: [], pr_url: "", author_type: "member",
          author_id: "u-2", created_at: now, changes: [{ location: "context", summary: "s" }],
          source_accessible: false, superseded: [], deleted_lines: null,
        },
      ],
      unsettled: [
        { issue_id: "i-2", identifier: "DENE-8", title: "只改了代码", reason: "unaudited", closed_at: now },
      ],
      rounds: {
        opened: 2, open: 1, idle: 1,
        items: [
          { issue_id: "i-3", identifier: "DENE-9", title: "沉淀第 2 轮", status: "done", layer: "worker",
            gap: ["context"], wrote: false, idle: true, created_at: now, updated_at: now },
        ],
      },
      chats: [{
        chat_session_id: "c-1", title: "派单聊天", accessible: true,
        dispatched: 6, reported: 1, no_conclusion: 5, open: 0,
        tickets: Array.from({ length: 6 }, (_, i) => ({
          issue_id: `t-${i}`, identifier: `DENE-${20 + i}`, title: `票 ${i}`, status: "done",
          flow: i === 0 ? "reported" as const : "no_conclusion" as const, updated_at: now,
        })),
      }],
    };
    mockGetProjectMemory.mockResolvedValue(status);
    mockGetProjectMemoryMonitor.mockResolvedValue(monitor as never);
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <I18nProvider locale="en" resources={TEST_RESOURCES}>
          <ProjectMemoryCard projectId="p-1" />
        </I18nProvider>
      </QueryClientProvider>,
    );
    expect(await screen.findByText("Sediments, last 14 days")).toBeTruthy();
    expect(screen.getByText("3 lines deleted")).toBeTruthy();
    expect(screen.getByText("Chat you can't open")).toBeTruthy();
    expect(screen.getByText("DENE-8 只改了代码")).toBeTruthy();
    expect(screen.getByText("No audit")).toBeTruthy();
    expect(screen.getByText("2 opened · 1 open · 1 idle")).toBeTruthy();
    expect(screen.getByText("Idle")).toBeTruthy();
    expect(screen.getByText("Chat 派单聊天")).toBeTruthy();
    expect(screen.getByText("6 sent · 1 reported · 5 no conclusion · 0 open")).toBeTruthy();
    // The sixth ticket waits behind "Show all" and is still reachable.
    expect(screen.queryByText("DENE-25 票 5")).toBeNull();
    fireEvent.click(screen.getByText("Show all 6"));
    expect(screen.getByText("DENE-25 票 5")).toBeTruthy();
  });
});
