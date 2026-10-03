import type { ReactNode } from "react";
import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import enIssues from "../locales/en/issues.json";
import { GoalCompletionModal } from "./goal-completion";

const mockGetIssueGoal = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/issues/queries", () => ({
  issueGoalOptions: (wsId: string, issueId: string) => ({
    queryKey: ["goal", wsId, issueId],
    queryFn: () => mockGetIssueGoal(issueId),
  }),
}));
vi.mock("@multica/core/issues/mutations", () => ({
  useOpenIssueGoal: () => ({ isPending: false, mutateAsync: vi.fn() }),
}));

function Providers({ children }: { children: ReactNode }) {
  return (
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <I18nProvider locale="en" resources={{ en: { issues: enIssues } }}>{children}</I18nProvider>
    </QueryClientProvider>
  );
}

describe("GoalCompletionModal", () => {
  // Every entry point opens this modal through openGoalCompletion(), which
  // passes camelCase keys; reading any other shape leaves the dialog blank.
  it("renders the shared panel from openGoalCompletion's payload", () => {
    render(
      <GoalCompletionModal
        onClose={() => {}}
        data={{ issueId: "issue-1", title: "Ship it", initialChecks: ["Entry is visible"] }}
      />,
      { wrapper: Providers },
    );
    expect(screen.getByText(/Ship it ·/)).toBeTruthy();
    expect(screen.getByDisplayValue("Entry is visible")).toBeTruthy();
  });

  it("starts from the server-drafted checks when the caller passes none", async () => {
    mockGetIssueGoal.mockResolvedValue({
      status: "draft",
      checks: [{ id: "c1", description: "Drafted from chat" }],
    });
    render(<GoalCompletionModal onClose={() => {}} data={{ issueId: "issue-2" }} />, { wrapper: Providers });
    expect(await screen.findByDisplayValue("Drafted from chat")).toBeTruthy();
  });
});
