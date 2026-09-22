// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enAgents from "../../locales/en/agents.json";
import { SolidifyUnbindDialog } from "./solidify-unbind-dialog";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

const mocks = vi.hoisted(() => ({
  solidifyAgent: vi.fn(),
  archiveAgent: vi.fn(),
  invalidateQueries: vi.fn(),
  toastError: vi.fn(),
  toastSuccess: vi.fn(),
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/api", () => ({
  api: {
    solidifyAgent: (...args: unknown[]) => mocks.solidifyAgent(...args),
    archiveAgent: (...args: unknown[]) => mocks.archiveAgent(...args),
  },
}));

vi.mock("@multica/core/workspace/queries", () => ({
  workspaceKeys: { agents: (wsId: string) => ["agents", wsId] },
}));

vi.mock("sonner", () => ({
  toast: {
    error: (...args: unknown[]) => mocks.toastError(...args),
    success: (...args: unknown[]) => mocks.toastSuccess(...args),
  },
}));

vi.mock("../../common/actor-avatar", () => ({ ActorAvatar: () => null }));

function makeAgent(overrides: Partial<Agent>): Agent {
  return {
    id: "agent",
    workspace_id: "ws-1",
    runtime_id: "rt-1",
    name: "Agent",
    description: "",
    instructions: "",
    avatar_url: null,
    runtime_mode: "local",
    runtime_config: {},
    custom_args: [],
    visibility: "private",
    permission_mode: "private",
    invocation_targets: [],
    status: "idle",
    max_concurrent_tasks: 1,
    model: "",
    owner_id: null,
    skills: [],
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    archived_at: null,
    archived_by: null,
    ...overrides,
  };
}

const parent = makeAgent({ id: "agent-base", name: "Base Role" });
const firstChild = makeAgent({
  id: "agent-child-1",
  name: "Nightly Variant",
  parent_agent_id: "agent-base",
});
const secondChild = makeAgent({
  id: "agent-child-2",
  name: "Weekly Variant",
  parent_agent_id: "agent-base",
});

function renderDialog(
  children: Agent[] = [firstChild, secondChild],
  serverChildNames: string[] = [],
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  queryClient.invalidateQueries = mocks.invalidateQueries;
  const onClose = vi.fn();
  const onArchived = vi.fn();
  render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <QueryClientProvider client={queryClient}>
        <SolidifyUnbindDialog
          parent={parent}
          childAgents={children}
          serverChildNames={serverChildNames}
          onClose={onClose}
          onArchived={onArchived}
        />
      </QueryClientProvider>
    </I18nProvider>,
  );
  return { onClose, onArchived };
}

describe("SolidifyUnbindDialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.solidifyAgent.mockResolvedValue(null);
    mocks.archiveAgent.mockResolvedValue(null);
    mocks.invalidateQueries.mockResolvedValue(undefined);
  });
  // Base UI renders the dialog into a portal on document.body and leaves
  // wrapper residue behind; wipe it between tests (same as the other dialogs).
  afterEach(() => {
    cleanup();
    document.body.innerHTML = "";
  });

  it("names what blocks the archive and what will happen to it", () => {
    renderDialog();

    const dialog = screen.getByTestId("solidify-unbind-dialog");
    expect(dialog).toHaveTextContent('"Base Role" still has 2 specializations');
    expect(dialog).toHaveTextContent("Nightly Variant");
    expect(dialog).toHaveTextContent("Weekly Variant");
    expect(dialog).toHaveTextContent(
      /writes the base role's current prompt into each specialization/i,
    );
  });

  it("solidifies every child, then archives the base role", async () => {
    const user = userEvent.setup();
    const { onClose, onArchived } = renderDialog();

    await user.click(
      screen.getByRole("button", { name: /Solidify & unbind, then archive/i }),
    );

    await waitFor(() => expect(mocks.archiveAgent).toHaveBeenCalledWith("agent-base"));
    expect(mocks.solidifyAgent.mock.calls.map((call) => call[0])).toEqual([
      "agent-child-1",
      "agent-child-2",
    ]);
    // The call order matters: archiving first would be refused by the server.
    expect(mocks.solidifyAgent.mock.invocationCallOrder[0]).toBeLessThan(
      mocks.archiveAgent.mock.invocationCallOrder[0] ?? 0,
    );
    expect(mocks.toastSuccess).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(onArchived).toHaveBeenCalledTimes(1);
  });

  // DENE-384: the guard counts every active child, including ones this viewer
  // cannot see, so a list built from visible rows alone could show fewer rows
  // than are actually blocking — and the second archive would be refused too.
  it("lists one row per child the refusal reported, seen or not", async () => {
    const user = userEvent.setup();
    renderDialog([firstChild], ["Nightly Variant", "Hidden Variant"]);

    const dialog = screen.getByTestId("solidify-unbind-dialog");
    expect(dialog).toHaveTextContent('"Base Role" still has 2 specializations');
    expect(screen.getAllByTestId("solidify-unbind-child")).toHaveLength(2);
    expect(dialog).toHaveTextContent("Hidden Variant");
    expect(screen.getByTestId("solidify-unbind-hidden-note")).toHaveTextContent(
      /1 more specialization isn't visible to you/i,
    );

    // Only the child this viewer holds an id for can be solidified; the count
    // and the note are what keep the user from thinking the job is done.
    await user.click(
      screen.getByRole("button", { name: /Solidify & unbind, then archive/i }),
    );
    await waitFor(() =>
      expect(mocks.solidifyAgent.mock.calls.map((call) => call[0])).toEqual([
        "agent-child-1",
      ]),
    );
  });

  it("says nothing about hidden children when the refusal named them all", () => {
    renderDialog([firstChild, secondChild], ["Nightly Variant", "Weekly Variant"]);

    expect(screen.getAllByTestId("solidify-unbind-child")).toHaveLength(2);
    expect(
      screen.queryByTestId("solidify-unbind-hidden-note"),
    ).not.toBeInTheDocument();
  });

  it("stops at the first refusal and reports which child failed", async () => {
    const user = userEvent.setup();
    mocks.solidifyAgent
      .mockResolvedValueOnce(null)
      .mockRejectedValueOnce(new Error("you cannot read this agent's base role"));
    const { onClose, onArchived } = renderDialog();

    await user.click(
      screen.getByRole("button", { name: /Solidify & unbind, then archive/i }),
    );

    await waitFor(() => expect(mocks.toastError).toHaveBeenCalledTimes(1));
    // The base role is NOT archived: the guard is still in force for the
    // child that could not be solidified.
    expect(mocks.archiveAgent).not.toHaveBeenCalled();
    expect(mocks.toastError.mock.calls[0]?.[0]).toMatch(/Weekly Variant/);
    expect(onClose).not.toHaveBeenCalled();
    expect(onArchived).not.toHaveBeenCalled();
    // The children that DID solidify are already detached server-side; the
    // list has to be re-read either way.
    expect(mocks.invalidateQueries).toHaveBeenCalled();
  });

  it("reports an archive failure without claiming success", async () => {
    const user = userEvent.setup();
    mocks.archiveAgent.mockRejectedValueOnce(new Error("archive exploded"));
    const { onClose, onArchived } = renderDialog();

    await user.click(
      screen.getByRole("button", { name: /Solidify & unbind, then archive/i }),
    );

    await waitFor(() => expect(mocks.toastError).toHaveBeenCalledTimes(1));
    expect(mocks.toastSuccess).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    expect(onArchived).not.toHaveBeenCalled();
  });
});
