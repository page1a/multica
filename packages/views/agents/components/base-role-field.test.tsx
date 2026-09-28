// @vitest-environment jsdom
//
// Canonical matrix for the "may this agent's base role change at all?" rule
// lives in ../specialization.test.ts (`canChangeBaseRole`). This suite keeps
// the wiring: which write each choice performs, and what happens on refusal.

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import type { Agent } from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enAgents from "../../locales/en/agents.json";
import { BaseRoleField } from "./base-role-field";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

/** The picker opens asynchronously; mirrors create-agent-dialog.test.tsx. */
function option(name: string): Promise<HTMLElement> {
  return screen.findByRole("option", { name });
}

function trigger(): HTMLElement {
  return screen.getByRole("combobox", { name: "Base role" });
}

function makeAgent(overrides: Partial<Agent> = {}): Agent {
  return {
    id: "agent-1",
    workspace_id: "ws-1",
    runtime_id: "runtime-1",
    name: "Builder",
    description: "",
    instructions: "Do the work.",
    conversation_starters: [],
    avatar_url: null,
    runtime_mode: "local",
    runtime_config: {},
    custom_args: [],
    visibility: "workspace",
    permission_mode: "public_to",
    invocation_targets: [{ target_type: "workspace", target_id: null }],
    status: "idle",
    max_concurrent_tasks: 1,
    model: "",
    owner_id: "user-1",
    skills: [],
    created_at: "2026-09-01T00:00:00Z",
    updated_at: "2026-09-01T00:00:00Z",
    archived_at: null,
    archived_by: null,
    ...overrides,
  } as Agent;
}

const baseRole = makeAgent({ id: "base-1", name: "Base Builder" });

function field(
  agent: Agent,
  {
    agents = [baseRole, agent],
    visibleChildren = 0,
    canEdit = true,
    onAttach = vi.fn().mockResolvedValue(undefined),
    onDetach = vi.fn().mockResolvedValue(undefined),
  }: {
    agents?: readonly Agent[];
    visibleChildren?: number;
    canEdit?: boolean;
    onAttach?: (id: string) => Promise<void>;
    onDetach?: () => Promise<void>;
  } = {},
) {
  return (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <BaseRoleField
        agent={agent}
        agents={agents}
        visibleChildren={visibleChildren}
        canEdit={canEdit}
        onAttach={onAttach}
        onDetach={onDetach}
      />
    </I18nProvider>
  );
}

describe("BaseRoleField", () => {
  it("attaches an existing standalone agent to a base role", async () => {
    const user = userEvent.setup();
    const onAttach = vi.fn().mockResolvedValue(undefined);
    render(field(makeAgent(), { onAttach }));

    // No pending change yet, so no save button to press by accident.
    expect(screen.queryByTestId("agent-base-role-save")).toBeNull();

    await user.click(trigger());
    await user.click(await option("Base Builder"));
    await user.click(screen.getByTestId("agent-base-role-save"));

    expect(onAttach).toHaveBeenCalledWith("base-1");
  });

  it("routes 'independent base role' through solidify, not a bare detach", async () => {
    const user = userEvent.setup();
    const onAttach = vi.fn().mockResolvedValue(undefined);
    const onDetach = vi.fn().mockResolvedValue(undefined);
    const child = makeAgent({ parent_agent_id: "base-1" });
    render(field(child, { onAttach, onDetach }));

    await user.click(trigger());
    await user.click(await option("Independent base role"));
    // The detach copy has to be visible BEFORE the write: it is the only
    // notice that the inherited prompt gets baked in rather than dropped.
    expect(screen.getByTestId("agent-base-role-detach-hint")).toBeTruthy();

    await user.click(screen.getByTestId("agent-base-role-save"));

    expect(onDetach).toHaveBeenCalledTimes(1);
    expect(onAttach).not.toHaveBeenCalled();
  });

  it("never offers the agent itself as its own base role", async () => {
    const user = userEvent.setup();
    const self = makeAgent({ id: "base-2", name: "Also A Base Role" });
    render(field(self, { agents: [baseRole, self] }));

    await user.click(trigger());

    expect(await option("Base Builder")).toBeTruthy();
    expect(screen.queryByRole("option", { name: "Also A Base Role" })).toBeNull();
  });

  it("explains the depth cap instead of offering a picker that would 400", () => {
    render(
      field(makeAgent({ child_count: 2 }), { visibleChildren: 2 }),
    );

    expect(screen.queryByRole("combobox", { name: "Base role" })).toBeNull();
    expect(
      screen.getByText(/can't attach to a base role/i),
    ).toBeTruthy();
  });

  it("falls back to the stored base role when the write is refused", async () => {
    const user = userEvent.setup();
    const onAttach = vi.fn().mockRejectedValue(new Error("nope"));
    render(field(makeAgent(), { onAttach }));

    await user.click(trigger());
    await user.click(await option("Base Builder"));
    await user.click(screen.getByTestId("agent-base-role-save"));

    expect(onAttach).toHaveBeenCalledWith("base-1");
    // Back to "no base role": showing the refused parent would claim a
    // relationship the server does not hold.
    expect(screen.queryByTestId("agent-base-role-save")).toBeNull();
    expect(trigger().textContent).toContain(
      "Independent base role",
    );
  });

  it("re-syncs when the stored base role changes under it", () => {
    const child = makeAgent({ parent_agent_id: "base-1" });
    const { rerender } = render(field(child));
    expect(trigger().textContent).toContain(
      "Base Builder",
    );

    rerender(field(makeAgent({ parent_agent_id: undefined })));

    expect(trigger().textContent).toContain(
      "Independent base role",
    );
    expect(screen.queryByTestId("agent-base-role-save")).toBeNull();
  });

  it("renders read-only for a viewer who cannot edit the agent", async () => {
    render(field(makeAgent(), { canEdit: false }));

    expect(trigger()).toBeDisabled();
  });
});
