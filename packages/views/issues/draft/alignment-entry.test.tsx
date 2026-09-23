import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import type { Issue } from "@multica/core/types";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import enCommon from "../../locales/en/common.json";
import enIssues from "../../locales/en/issues.json";
import { IssueAlignmentEntry } from "./alignment-entry";

/**
 * The alignment entry on an issue detail page.
 *
 * Two entry points share one slot, and the rule that matters is that they never
 * both appear: an issue that is already an alignment's product shows the way
 * back into that conversation, and an issue with no alignment behind it shows
 * the action that starts one. Offering "start another" beside "continue this"
 * would found a second conversation about work that already has one — which is
 * the whole reason this component decides by `selfStarted` rather than by
 * whether a draft id happens to be readable.
 */

const mocks = vi.hoisted(() => ({
  push: vi.fn(),
  reopen: vi.fn(),
  openAlignIssue: vi.fn(),
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    issues: () => "/acme/issues",
    issueDetail: (id: string) => `/acme/issues/${id}`,
    newIssueDraft: (id: string) => `/acme/issues/new/${id}`,
  }),
}));

vi.mock("@multica/core/issue-drafts", async () => {
  const actual = await vi.importActual<typeof import("@multica/core/issue-drafts")>(
    "@multica/core/issue-drafts",
  );
  return {
    ...actual,
    useReopenIssueDraft: () => ({ mutateAsync: mocks.reopen, isPending: false }),
  };
});

vi.mock("@multica/core/issues/stores", async () => {
  const actual = await vi.importActual<typeof import("@multica/core/issues/stores")>(
    "@multica/core/issues/stores",
  );
  return { ...actual, openAlignIssue: mocks.openAlignIssue };
});

const TEST_RESOURCES = { en: { common: enCommon, issues: enIssues } };

const navigation: NavigationAdapter = {
  push: mocks.push,
  replace: vi.fn(),
  back: vi.fn(),
  pathname: "/acme/issues/i-1",
  searchParams: new URLSearchParams(),
  hash: "",
  getShareableUrl: (path) => path,
};

function issue(overrides: Partial<Issue> = {}): Issue {
  return {
    id: "i-1",
    identifier: "MUL-1",
    title: "Dark mode",
    status: "todo",
    stage: null,
    ...overrides,
  } as unknown as Issue;
}

function renderEntry(props: Partial<Parameters<typeof IssueAlignmentEntry>[0]> = {}) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <NavigationProvider value={navigation}>
          <IssueAlignmentEntry
            draftId={null}
            groupIssues={[issue()]}
            parentIssueId="i-1"
            selfStarted={false}
            startedByAnother={false}
            {...props}
          />
        </NavigationProvider>
      </I18nProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.reopen.mockResolvedValue({});
});

describe("IssueAlignmentEntry", () => {
  it("offers to start an alignment filed under the issue on screen", async () => {
    renderEntry({ parentIssueId: "issue-7" });

    await userEvent.click(
      screen.getByRole("button", { name: "Align on this issue" }),
    );

    // The parent rides the modal payload, which is what makes the conversation
    // file its group under this issue instead of founding a second top-level
    // one (DENE-452).
    expect(mocks.openAlignIssue).toHaveBeenCalledWith({
      parent_issue_id: "issue-7",
      project_id: null,
    });
  });

  it("starts the alignment on the issue's project", async () => {
    renderEntry({ parentIssueId: "issue-7", projectId: "proj-1" });

    await userEvent.click(
      screen.getByRole("button", { name: "Align on this issue" }),
    );

    expect(mocks.openAlignIssue).toHaveBeenCalledWith({
      parent_issue_id: "issue-7",
      project_id: "proj-1",
    });
  });

  it("does not offer a second alignment on a group that already came out of one", () => {
    // `selfStarted` is true even though the reopen is the primary action: the
    // group's own conversation is the way to say the next thing.
    renderEntry({ draftId: "sess-1", selfStarted: true });

    expect(screen.getByRole("button", { name: "Continue aligning" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Align on this issue" })).toBeNull();
  });

  it("does not offer one on somebody else's alignment either, and says so", () => {
    renderEntry({ startedByAnother: true });

    expect(
      screen.getByText("An alignment is already running for this work."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Align on this issue" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Continue aligning" })).toBeNull();
  });

  it("returns to the same conversation on a reopen", async () => {
    renderEntry({ draftId: "sess-1", selfStarted: true });

    await userEvent.click(
      screen.getByRole("button", { name: "Continue aligning" }),
    );

    await waitFor(() => expect(mocks.reopen).toHaveBeenCalledWith("sess-1"));
    await waitFor(() =>
      expect(mocks.push).toHaveBeenCalledWith("/acme/issues/new/sess-1"),
    );
  });

  it("reports a conversation that is gone instead of navigating into it", async () => {
    mocks.reopen.mockRejectedValue(new Error("gone"));
    renderEntry({ draftId: "sess-1", selfStarted: true });

    await userEvent.click(
      screen.getByRole("button", { name: "Continue aligning" }),
    );

    expect(
      await screen.findByText(
        "Could not reopen this alignment. It may no longer be available.",
      ),
    ).toBeInTheDocument();
    expect(mocks.push).not.toHaveBeenCalled();
  });
});
