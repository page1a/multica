import { cleanup, fireEvent, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Issue } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

const mutate = vi.fn();
vi.mock("@multica/core/issues/mutations", () => ({
  useDisposeIssue: () => ({ mutate, isPending: false }),
}));

import { IssueUndrivenRow, UndrivenRowMark } from "./issue-driver";

afterEach(() => {
  cleanup();
  mutate.mockReset();
});

function issue(overrides: Partial<Issue> = {}): Issue {
  return { id: "issue-1", workspace_id: "ws-1", ...overrides } as Issue;
}

const none = { kind: "none", reason: "有执行人，但没有运行", revives: 2, escalated: true };

describe("IssueUndrivenRow", () => {
  it("stays silent while something drives the issue", () => {
    renderWithI18n(<IssueUndrivenRow issue={issue({ driver: { kind: "run", reason: "有运行在跑" } })} />);
    expect(screen.queryByTestId("issue-undriven")).toBeNull();
    renderWithI18n(<IssueUndrivenRow issue={issue()} />);
    expect(screen.queryByTestId("issue-undriven")).toBeNull();
  });

  it("says why nobody drives it and what the patrol tried", () => {
    renderWithI18n(<IssueUndrivenRow issue={issue({ driver: none })} />);
    const row = screen.getByTestId("issue-undriven");
    expect(row.textContent).toContain("有执行人，但没有运行");
    expect(row.textContent).toContain("2");
  });

  it("reruns in one click and asks for a reason before cancelling", () => {
    renderWithI18n(<IssueUndrivenRow issue={issue({ driver: none })} />);
    fireEvent.click(screen.getByRole("button", { name: "Rerun" }));
    expect(mutate).toHaveBeenCalledWith({ action: "rerun" }, expect.anything());

    fireEvent.click(screen.getByRole("button", { name: "Cancel issue" }));
    const submit = screen.getByRole("button", { name: "Cancel issue" });
    expect(submit).toHaveProperty("disabled", true);
    fireEvent.change(screen.getByPlaceholderText("Why cancel"), { target: { value: "需求撤回" } });
    fireEvent.click(submit);
    expect(mutate).toHaveBeenLastCalledWith({ action: "cancel", reason: "需求撤回" }, expect.anything());
  });

  it("splits into the typed title", () => {
    renderWithI18n(<IssueUndrivenRow issue={issue({ driver: none })} />);
    fireEvent.click(screen.getByRole("button", { name: "Split" }));
    fireEvent.change(screen.getByPlaceholderText("Sub-issue title"), { target: { value: "先修接口" } });
    fireEvent.click(screen.getByRole("button", { name: "Split" }));
    expect(mutate).toHaveBeenCalledWith({ action: "split", into: ["先修接口"] }, expect.anything());
  });

  it("hides the actions from a read-only viewer", () => {
    renderWithI18n(<IssueUndrivenRow issue={issue({ driver: none })} readOnly />);
    expect(screen.getByTestId("issue-undriven")).toBeTruthy();
    expect(screen.queryByRole("button")).toBeNull();
  });
});

describe("UndrivenRowMark", () => {
  it("marks only an undriven sub-issue", () => {
    renderWithI18n(<UndrivenRowMark driver={none} />);
    expect(screen.getByTestId("sub-issue-undriven").getAttribute("title")).toBe(none.reason);
    cleanup();
    renderWithI18n(<UndrivenRowMark driver={{ kind: "wait", reason: "等子票" }} />);
    expect(screen.queryByTestId("sub-issue-undriven")).toBeNull();
  });
});
