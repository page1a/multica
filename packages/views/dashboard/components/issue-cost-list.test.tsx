// @vitest-environment jsdom

import { cleanup, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import { IssueCostList } from "./issue-cost-list";
import type { IssueCostRow } from "../utils";

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    issueDetail: (id: string) => `/acme/issues/${id}`,
  }),
}));

function makeAdapter(): NavigationAdapter {
  return {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/usage",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (path: string) => path,
  };
}

function makeRow(overrides: Partial<IssueCostRow> = {}): IssueCostRow {
  return {
    issueId: "issue-1",
    identifier: "DENE-1",
    title: "Pricey issue",
    tokens: 6_000_000,
    cost: 12.5,
    ...overrides,
  };
}

function renderList(rows: IssueCostRow[]) {
  return renderWithI18n(
    <NavigationProvider value={makeAdapter()}>
      <IssueCostList rows={rows} />
    </NavigationProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("IssueCostList", () => {
  it("links each row into that issue's Token cost view", () => {
    renderList([
      makeRow(),
      makeRow({
        issueId: "issue-2",
        identifier: "DENE-2",
        title: "Cheap issue",
        tokens: 1_000,
        cost: 0.01,
      }),
    ]);

    const list = within(screen.getByRole("list", { name: "Costliest issues" }));
    expect(list.getByRole("link", { name: /DENE-1/ })).toHaveAttribute(
      "href",
      "/acme/issues/DENE-1?usage=1",
    );
    expect(list.getByRole("link", { name: /DENE-2/ })).toHaveAttribute(
      "href",
      "/acme/issues/DENE-2?usage=1",
    );
  });

  it("falls back to the issue id when the server omits the identifier", () => {
    // An older backend may not send `identifier`; the issue route resolves the
    // UUID, so the row must still be a working deep link rather than a dead "/".
    renderList([makeRow({ identifier: "", title: "" })]);

    expect(screen.getByRole("link")).toHaveAttribute(
      "href",
      "/acme/issues/issue-1?usage=1",
    );
  });

  it("explains an empty window instead of rendering an empty table", () => {
    renderList([]);

    expect(screen.getByText("No issue spend in this window.")).toBeInTheDocument();
    expect(screen.queryByRole("list")).toBeNull();
  });

  it("caps the ranking at ten issues and reveals the tail behind the toggle", async () => {
    const user = userEvent.setup();
    renderList(
      Array.from({ length: 12 }, (_, i) =>
        makeRow({
          // Descending cost so the visible set is the top ten by rank.
          issueId: `issue-${i}`,
          identifier: `DENE-${i}`,
          title: `Issue ${i}`,
          cost: 12 - i,
        }),
      ),
    );

    const list = screen.getByRole("list", { name: "Costliest issues" });
    expect(within(list).getAllByRole("listitem")).toHaveLength(10);
    // The ranking is by cost, so the two cheapest are the ones held back.
    expect(within(list).queryByText("Issue 11")).toBeNull();

    await user.click(screen.getByRole("button", { name: "Show all" }));

    const expanded = screen.getByRole("list", { name: "Costliest issues" });
    expect(within(expanded).getAllByRole("listitem")).toHaveLength(12);
    expect(within(expanded).getByText("Issue 11")).toBeInTheDocument();
  });
});
