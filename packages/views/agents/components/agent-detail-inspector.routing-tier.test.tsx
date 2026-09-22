import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { AgentDetailInspector } from "./agent-detail-inspector";

// The tier tag is the only field on this page routing reads, so what matters
// here is the wiring: it is mounted, it shows the saved rung, and picking one
// sends the KEY. The vocabulary itself (order, unknown rungs) is covered in
// packages/core/agents/routing-tier.test.ts.

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: () => ({ data: undefined, isSuccess: false }),
}));

vi.mock("../../common/avatar-upload-control", () => ({
  AvatarUploadControl: () => <div data-testid="avatar-upload" />,
}));

vi.mock("./inspector/model-picker", () => ({
  ModelPicker: () => <div data-testid="model-picker" />,
}));

vi.mock("./inspector/runtime-picker", () => ({
  RuntimePicker: () => <div data-testid="runtime-picker" />,
}));

vi.mock("./inspector/thinking-prop-row", () => ({
  ThinkingSettingField: () => <div data-testid="thinking-field" />,
}));

vi.mock("./inspector/service-tier-setting-field", () => ({
  ServiceTierSettingField: () => <div data-testid="service-tier-field" />,
}));

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

function renderInspector(agent: Agent, onUpdate = vi.fn(async () => {})) {
  renderWithI18n(
    <AgentDetailInspector
      agent={agent}
      runtime={null}
      runtimes={[]}
      members={[]}
      currentUserId="user-1"
      canEdit
      onUpdate={onUpdate}
    />,
  );
  return onUpdate;
}

describe("AgentDetailInspector dispatch tier", () => {
  afterEach(() => {
    cleanup();
  });

  it("shows the saved rung", () => {
    renderInspector(agentFixture({ routing_tier: "strong" }));
    expect(screen.getByLabelText(/Dispatch tier · Strong/)).toBeTruthy();
  });

  it("shows a seat with no tier as off the ladder", () => {
    renderInspector(agentFixture());
    expect(screen.getByLabelText(/Dispatch tier · Off the ladder/)).toBeTruthy();
  });

  it("sends the tier key when a rung is picked", async () => {
    const user = userEvent.setup();
    const onUpdate = renderInspector(agentFixture({ routing_tier: "weak" }));

    await user.click(screen.getByLabelText(/Dispatch tier · Light/));
    await user.click(screen.getByText("Strongest"));

    expect(onUpdate).toHaveBeenCalledWith("agent-1", {
      routing_tier: "strongest",
    });
  });

  it("takes a seat off the ladder", async () => {
    const user = userEvent.setup();
    const onUpdate = renderInspector(agentFixture({ routing_tier: "medium" }));

    await user.click(screen.getByLabelText(/Dispatch tier · Medium/));
    await user.click(
      screen.getByTitle(/Routing never picks this seat automatically/),
    );

    expect(onUpdate).toHaveBeenCalledWith("agent-1", { routing_tier: "" });
  });
});
