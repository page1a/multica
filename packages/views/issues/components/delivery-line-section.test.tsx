import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import type { IssueDeliveryLines } from "@multica/core/api/schemas";
import enCommon from "../../locales/en/common.json";
import enIssues from "../../locales/en/issues.json";

const TEST_RESOURCES = { en: { common: enCommon, issues: enIssues } };

const apiMock = vi.hoisted(() => ({ getIssueDeliveryLines: vi.fn() }));
vi.mock("@multica/core/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/api")>()),
  api: apiMock,
}));
vi.mock("../../navigation", () => ({
  AppLink: ({ href, children }: { href: string; children: React.ReactNode }) => <a href={href}>{children}</a>,
}));
vi.mock("@multica/core/paths", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/paths")>()),
  useWorkspacePaths: () => ({ issueDetail: (id: string) => `/acme/issues/${id}` }),
}));

import { DeliveryLineSection } from "./delivery-line-section";

function renderSection() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <I18nProvider resources={TEST_RESOURCES} locale="en">
        <DeliveryLineSection wsId="ws-1" issueId="issue-1" />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

const commit = (n: number) => ({ sha: `${n}`.padStart(40, "a"), subject: `feat: step ${n}` });

describe("DeliveryLineSection (DENE-1537)", () => {
  beforeEach(() => apiMock.getIssueDeliveryLines.mockReset());

  it("names the parent's branch a sub-issue delivers to", async () => {
    apiMock.getIssueDeliveryLines.mockResolvedValue({
      line: {
        owner_issue_id: "parent-1",
        owner_identifier: "DENE-9",
        branch: "agent/delivery/dene-9",
        status: "merged",
        commits: [commit(1), commit(2)],
        conflict_files: [],
      },
      contributions: [],
    } satisfies IssueDeliveryLines);
    renderSection();
    const link = await screen.findByRole("link", { name: "Delivers to DENE-9's branch" });
    expect(link).toHaveAttribute("href", "/acme/issues/parent-1");
    expect(screen.getByText("agent/delivery/dene-9")).toBeInTheDocument();
    expect(screen.getByText("2 commits merged")).toBeInTheDocument();
    expect(screen.getByText("feat: step 2")).toBeInTheDocument();
  });

  it("lists each sub-issue's commits on the parent, conflicts included", async () => {
    apiMock.getIssueDeliveryLines.mockResolvedValue({
      line: null,
      contributions: [
        { issue_id: "c1", identifier: "DENE-10", title: "Stage one", issue_status: "done", status: "merged", commits: [commit(3)], conflict_files: [] },
        { issue_id: "c2", identifier: "DENE-11", title: "Stage two", issue_status: "blocked", status: "conflict", commits: [], conflict_files: ["web/app.ts"] },
      ],
    } satisfies IssueDeliveryLines);
    renderSection();
    expect(await screen.findByText("Sub-issue commits")).toBeInTheDocument();
    expect(screen.getByText("1 commit merged")).toBeInTheDocument();
    expect(screen.getByText("Conflict, executor resolves")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "DENE-11" }));
    expect(screen.getByText("web/app.ts")).toBeInTheDocument();
  });

  it("stays hidden on an issue without a parent line or sub-issue commits", async () => {
    apiMock.getIssueDeliveryLines.mockResolvedValue({ line: null, contributions: [] } satisfies IssueDeliveryLines);
    const { container } = renderSection();
    await vi.waitFor(() => expect(apiMock.getIssueDeliveryLines).toHaveBeenCalled());
    expect(container).toBeEmptyDOMElement();
  });
});
