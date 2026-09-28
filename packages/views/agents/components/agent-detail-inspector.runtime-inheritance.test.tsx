import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { AgentDetailInspector } from "./agent-detail-inspector";

// DENE-506: the follow/independent switch. The state matrix itself —
// base role, pre-feature backend, permission gate — is the canonical
// `specialization.test.ts`; these pin the wiring: when the switch appears,
// what it submits, and that the runtime-profile controls it governs become
// read-only while following.

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: () => ({ data: undefined, isSuccess: false }),
}));

vi.mock("../../common/avatar-upload-control", () => ({
  AvatarUploadControl: () => <div data-testid="avatar-upload" />,
}));

vi.mock("./inspector/model-picker", () => ({
  ModelPicker: ({ canEdit }: { canEdit?: boolean }) => (
    <div data-testid="model-picker" data-can-edit={String(canEdit !== false)} />
  ),
}));

vi.mock("./inspector/runtime-picker", () => ({
  RuntimePicker: ({ canEdit }: { canEdit?: boolean }) => (
    <div
      data-testid="runtime-picker"
      data-can-edit={String(canEdit !== false)}
    />
  ),
}));

vi.mock("./inspector/thinking-prop-row", () => ({
  ThinkingSettingField: ({ canEdit }: { canEdit?: boolean }) => (
    <div
      data-testid="thinking-field"
      data-can-edit={String(canEdit !== false)}
    />
  ),
}));

vi.mock("./inspector/service-tier-setting-field", () => ({
  ServiceTierSettingField: ({ canEdit }: { canEdit?: boolean }) => (
    <div
      data-testid="service-tier-field"
      data-can-edit={String(canEdit !== false)}
    />
  ),
}));

const SWITCH_NAME = "Follow the base role's runtime";

function agentFixture(overrides: Partial<Agent> = {}): Agent {
  return {
    id: "agent-1",
    workspace_id: "workspace-1",
    name: "Lambda",
    description: "Test agent",
    runtime_id: "runtime-1",
    max_concurrent_tasks: 1,
    ...overrides,
  } as Agent;
}

function renderInspector(
  agent: Agent,
  options: { canEdit?: boolean; onUpdate?: (id: string, data: Record<string, unknown>) => Promise<void> } = {},
) {
  const onUpdate = options.onUpdate ?? vi.fn(async () => {});
  renderWithI18n(
    <AgentDetailInspector
      agent={agent}
      runtime={null}
      runtimes={[]}
      members={[]}
      currentUserId="user-1"
      canEdit={options.canEdit ?? true}
      onUpdate={onUpdate}
    />,
  );
  return onUpdate;
}

describe("AgentDetailInspector runtime inheritance", () => {
  afterEach(() => {
    cleanup();
  });

  it("offers the switch, checked, while the specialisation follows", () => {
    renderInspector(
      agentFixture({ parent_agent_id: "base-1", runtime_inherited: true }),
    );

    expect(screen.getByRole("switch", { name: SWITCH_NAME })).toBeChecked();
    expect(
      screen.getByText(/speed follow the base role/),
    ).toBeInTheDocument();
  });

  it("offers the switch, unchecked, when the specialisation is independent", () => {
    renderInspector(
      agentFixture({ parent_agent_id: "base-1", runtime_inherited: false }),
    );

    expect(screen.getByRole("switch", { name: SWITCH_NAME })).not.toBeChecked();
    expect(
      screen.getByText(/own runtime settings/),
    ).toBeInTheDocument();
  });

  it("offers no switch to a base role", () => {
    renderInspector(agentFixture({ runtime_inherited: false }));

    expect(
      screen.queryByRole("switch", { name: SWITCH_NAME }),
    ).not.toBeInTheDocument();
  });

  it("offers no switch for a specialisation on a backend without the flag", () => {
    renderInspector(agentFixture({ parent_agent_id: "base-1" }));

    expect(
      screen.queryByRole("switch", { name: SWITCH_NAME }),
    ).not.toBeInTheDocument();
  });

  it("submits runtime_inherited:false when the switch is turned off", async () => {
    const onUpdate = renderInspector(
      agentFixture({ parent_agent_id: "base-1", runtime_inherited: true }),
    );
    const user = userEvent.setup();

    await user.click(screen.getByRole("switch", { name: SWITCH_NAME }));

    expect(onUpdate).toHaveBeenCalledWith("agent-1", {
      runtime_inherited: false,
    });
  });

  it("submits runtime_inherited:true when the switch is turned on", async () => {
    const onUpdate = renderInspector(
      agentFixture({ parent_agent_id: "base-1", runtime_inherited: false }),
    );
    const user = userEvent.setup();

    await user.click(screen.getByRole("switch", { name: SWITCH_NAME }));

    expect(onUpdate).toHaveBeenCalledWith("agent-1", {
      runtime_inherited: true,
    });
  });

  it("locks the runtime profile controls while following, and only then", () => {
    renderInspector(
      agentFixture({ parent_agent_id: "base-1", runtime_inherited: true }),
    );
    for (const testId of [
      "runtime-picker",
      "model-picker",
      "thinking-field",
      "service-tier-field",
    ]) {
      expect(screen.getByTestId(testId)).toHaveAttribute(
        "data-can-edit",
        "false",
      );
    }
  });

  it("leaves the runtime profile controls editable when independent", () => {
    renderInspector(
      agentFixture({ parent_agent_id: "base-1", runtime_inherited: false }),
    );
    for (const testId of [
      "runtime-picker",
      "model-picker",
      "thinking-field",
      "service-tier-field",
    ]) {
      expect(screen.getByTestId(testId)).toHaveAttribute(
        "data-can-edit",
        "true",
      );
    }
  });

  it("keeps the profile controls locked for a viewer who cannot edit", () => {
    renderInspector(
      agentFixture({ parent_agent_id: "base-1", runtime_inherited: false }),
      { canEdit: false },
    );

    expect(screen.getByTestId("runtime-picker")).toHaveAttribute(
      "data-can-edit",
      "false",
    );
    expect(screen.getByRole("switch", { name: SWITCH_NAME })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
  });
});
