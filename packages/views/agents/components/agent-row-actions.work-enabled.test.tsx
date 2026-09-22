import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { AgentRowActions } from "./agent-row-actions";

const { updateAgent } = vi.hoisted(() => ({
  updateAgent: vi.fn(async () => ({})),
}));

vi.mock("@tanstack/react-query", () => ({
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    agentDetail: (id: string) => `/agents/${id}`,
  }),
}));

vi.mock("../../navigation", () => ({
  AppLink: ({ children }: { children: React.ReactNode }) => <a>{children}</a>,
  useIntentNavigate: () => vi.fn(),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    updateAgent,
    archiveAgent: vi.fn(),
    restoreAgent: vi.fn(),
    cancelAgentTasks: vi.fn(),
  },
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

function agentFixture(overrides: Partial<Agent> = {}): Agent {
  return {
    id: "agent-1",
    workspace_id: "workspace-1",
    name: "Lambda",
    description: "Test agent",
    runtime_id: "runtime-1",
    max_concurrent_tasks: 1,
    archived_at: null,
    ...overrides,
  } as Agent;
}

describe("AgentRowActions work enabled switch", () => {
  afterEach(() => {
    cleanup();
    updateAgent.mockClear();
  });

  it("shows an on switch for a live seat and turns it off in one click", async () => {
    const user = userEvent.setup();
    renderWithI18n(
      <AgentRowActions
        agent={agentFixture({ work_enabled: true })}
        presence={null}
        canManage
        duplicateHref="/agents/new"
      />,
    );

    const toggle = screen.getByRole("switch", { name: "Accept new work" });
    expect(toggle).toBeChecked();
    await user.click(toggle);
    expect(updateAgent).toHaveBeenCalledWith("agent-1", { work_enabled: false });
  });

  it("hides the switch when the viewer cannot manage the agent", () => {
    renderWithI18n(
      <AgentRowActions
        agent={agentFixture({ work_enabled: true })}
        presence={null}
        canManage={false}
        duplicateHref="/agents/new"
      />,
    );

    expect(screen.queryByRole("switch", { name: "Accept new work" })).toBeNull();
  });

  it("hides the switch on an archived row", () => {
    renderWithI18n(
      <AgentRowActions
        agent={agentFixture({
          work_enabled: true,
          archived_at: "2026-04-01T00:00:00Z",
        })}
        presence={null}
        canManage
        duplicateHref="/agents/new"
      />,
    );

    expect(screen.queryByRole("switch", { name: "Accept new work" })).toBeNull();
  });
});
