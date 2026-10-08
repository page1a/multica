import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { AgentDetailInspector } from "./agent-detail-inspector";

// Tier and usage are the two fields on this page routing reads, so what
// matters here is the wiring: they are mounted, they show the saved values,
// and a click sends the KEY. The vocabulary itself (order, unknown rungs) is covered in
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

describe("AgentDetailInspector routing section", () => {
  afterEach(() => {
    cleanup();
  });

  it("shows the saved rung and usage", () => {
    renderInspector(
      agentFixture({ routing_tier: "strong", routing_usage: "ample" }),
    );
    expect(
      screen.getByRole("radio", { name: "Strong" }).getAttribute("aria-checked"),
    ).toBe("true");
    expect(
      screen.getByRole("radio", { name: "Ample" }).getAttribute("aria-checked"),
    ).toBe("true");
  });

  it("shows an untagged seat as off the ladder with normal usage", () => {
    renderInspector(agentFixture());
    expect(
      screen.getByRole("radio", { name: "Off" }).getAttribute("aria-checked"),
    ).toBe("true");
    expect(
      screen.getByRole("radio", { name: "Normal" }).getAttribute("aria-checked"),
    ).toBe("true");
  });

  it("sends the tier key on click", async () => {
    const user = userEvent.setup();
    const onUpdate = renderInspector(agentFixture({ routing_tier: "weak" }));

    await user.click(screen.getByRole("radio", { name: "Strongest" }));

    expect(onUpdate).toHaveBeenCalledWith("agent-1", {
      routing_tier: "strongest",
    });
  });

  it("takes a seat off the ladder", async () => {
    const user = userEvent.setup();
    const onUpdate = renderInspector(agentFixture({ routing_tier: "medium" }));

    await user.click(screen.getByRole("radio", { name: "Off" }));

    expect(onUpdate).toHaveBeenCalledWith("agent-1", { routing_tier: "" });
  });

  it("sends the usage key on click", async () => {
    const user = userEvent.setup();
    const onUpdate = renderInspector(agentFixture({ routing_usage: "normal" }));

    await user.click(screen.getByRole("radio", { name: "Tight" }));

    expect(onUpdate).toHaveBeenCalledWith("agent-1", { routing_usage: "tight" });
  });

  it("does not write when the current value is clicked", async () => {
    const user = userEvent.setup();
    const onUpdate = renderInspector(agentFixture({ routing_usage: "ample" }));

    await user.click(screen.getByRole("radio", { name: "Ample" }));

    expect(onUpdate).not.toHaveBeenCalled();
  });

  it("sets a seat to mention only without touching its tier", async () => {
    const user = userEvent.setup();
    const onUpdate = renderInspector(agentFixture({ routing_tier: "weak" }));

    await user.click(screen.getByRole("radio", { name: "Mention only" }));

    expect(onUpdate).toHaveBeenCalledWith("agent-1", { dispatch_mode: "mention_only" });
  });

  it("reads a missing dispatch mode as auto", () => {
    renderInspector(agentFixture({}));

    expect(screen.getByRole("radio", { name: "Auto" })).toHaveAttribute("aria-checked", "true");
  });

  it("keeps routing read-only while a specialisation follows its base role", async () => {
    const user = userEvent.setup();
    const onUpdate = renderInspector(
      agentFixture({ parent_agent_id: "base-1", runtime_inherited: true, routing_tier: "weak" }),
    );

    const mentionOnly = screen.getByRole("radio", { name: "Mention only" });
    expect(mentionOnly).toBeDisabled();
    expect(screen.getByRole("radio", { name: "Medium" })).toBeDisabled();
    expect(screen.getByRole("radio", { name: "Tight" })).toBeDisabled();
    expect(screen.getByText(/Follows the base role/)).toBeInTheDocument();

    await user.click(mentionOnly);
    expect(onUpdate).not.toHaveBeenCalledWith("agent-1", expect.objectContaining({ dispatch_mode: expect.anything() }));
  });

  it("lets a specialisation that stopped following set its dispatch mode", async () => {
    const user = userEvent.setup();
    const onUpdate = renderInspector(
      agentFixture({ parent_agent_id: "base-1", runtime_inherited: false }),
    );

    await user.click(screen.getByRole("radio", { name: "Mention only" }));

    expect(onUpdate).toHaveBeenCalledWith("agent-1", { dispatch_mode: "mention_only" });
  });
});
