import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { AgentDetailInspector } from "./agent-detail-inspector";

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

describe("AgentDetailInspector work enabled", () => {
  afterEach(() => {
    cleanup();
  });

  it("treats a missing work_enabled field as on", () => {
    renderWithI18n(
      <AgentDetailInspector
        agent={agentFixture()}
        runtime={null}
        runtimes={[]}
        members={[]}
        currentUserId="user-1"
        canEdit
        onUpdate={vi.fn(async () => {})}
      />,
    );

    expect(screen.getByRole("switch", { name: "Accept work" })).toBeChecked();
  });

  it("submits false when the switch is turned off", async () => {
    const onUpdate = vi.fn(async () => {});
    const user = userEvent.setup();
    renderWithI18n(
      <AgentDetailInspector
        agent={agentFixture({ work_enabled: true })}
        runtime={null}
        runtimes={[]}
        members={[]}
        currentUserId="user-1"
        canEdit
        onUpdate={onUpdate}
      />,
    );

    await user.click(screen.getByRole("switch", { name: "Accept work" }));
    expect(onUpdate).toHaveBeenCalledWith("agent-1", {
      work_enabled: false,
    });
  });

  it("disables the switch when the viewer cannot edit", () => {
    renderWithI18n(
      <AgentDetailInspector
        agent={agentFixture({ work_enabled: false })}
        runtime={null}
        runtimes={[]}
        members={[]}
        currentUserId="user-1"
        canEdit={false}
        onUpdate={vi.fn(async () => {})}
      />,
    );

    expect(screen.getByRole("switch", { name: "Accept work" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
  });
});
