// @vitest-environment jsdom

import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { IssueGoal } from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enIssues from "../../locales/en/issues.json";
import { GoalSection } from "./goal-section";

const goalRef = vi.hoisted(() => ({ current: null as unknown }));

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({ data: goalRef.current, isLoading: false }),
}));
vi.mock("@multica/core/issues/queries", () => ({ issueGoalOptions: () => ({}) }));
vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({
    getMemberName: (id: string) => (id === "user-1" ? "Kun" : "Unknown"),
    getAgentName: (id: string) => (id === "agent-1" ? "孙悟空" : "Unknown Agent"),
  }),
}));

const RESOURCES = { en: { common: enCommon, issues: enIssues } };

function renderGoal(goal: Partial<IssueGoal>) {
  goalRef.current = { id: "g", issue_id: "i", status: "stopped", checks: [], ...goal };
  return render(
    <I18nProvider locale="en" resources={RESOURCES}>
      <GoalSection wsId="ws" issueId="i" />
    </I18nProvider>,
  );
}

describe("GoalSection stop record", () => {
  it("names the agent, the person it acted for, and their words", () => {
    renderGoal({ stopped_by: { type: "agent", id: "agent-1" }, stopped_on_behalf_of: "user-1", stop_reason: "drop goal mode" });
    expect(screen.getByText("Stopped by 孙悟空 at Kun's request. Continues as a regular issue.")).toBeTruthy();
    expect(screen.getByText("“drop goal mode”")).toBeTruthy();
  });

  it("names a member who stopped it", () => {
    renderGoal({ stopped_by: { type: "member", id: "user-1" }, stop_reason: "" });
    expect(screen.getByText("Stopped by Kun. Continues as a regular issue.")).toBeTruthy();
  });

  it("keeps the next-step hint for a brake pause", () => {
    renderGoal({ stopped_by: { type: "system" }, stop_reason: "budget_exhausted" });
    expect(screen.getByText(enIssues.detail.goal.stopped_hint)).toBeTruthy();
  });
});
