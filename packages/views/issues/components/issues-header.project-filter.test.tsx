// @vitest-environment jsdom

/**
 * The toolbar-level project quick filter (DENE-499).
 *
 * Project filtering used to live only behind Filter → Project. It is now also
 * a first-class toolbar button, sitting to the left of Filter — but it is not
 * a second filter: both entry points render the SAME `ProjectSubContent` over
 * the SAME `projectFilters` / `includeNoProject` store fields, and the chip bar
 * reads those same fields. Each test below drives one entry point and asserts
 * the others already agree, which is the property that makes the promotion
 * safe.
 *
 * The button disappears entirely where the surface already pins the project
 * dimension (project detail, or a saved view that fixes projects): a project
 * filter there could only filter the page down to nothing.
 *
 * Real Base UI menu primitives, no dropdown-menu mock — for the reason the
 * status filter suite states: a flattened mock renders a menu the real one
 * would crash on.
 */

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createStore } from "zustand/vanilla";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { baselineFromQuery } from "@multica/core/issue-views/baseline";
import {
  type IssueViewState,
  viewStoreSlice,
} from "@multica/core/issues/stores/view-store";
import { ViewStoreProvider } from "@multica/core/issues/stores/view-store-context";
import type {
  IssueTableFacetSpec,
  IssueTableFacetsResponse,
  Project,
} from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { FilterChipsBar } from "./filter-chips-bar";
import { IssueDisplayControls } from "./issues-header";

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

function project(id: string, title: string): Project {
  return {
    id,
    workspace_id: "ws-1",
    title,
    description: null,
    icon: null,
    status: "in_progress",
    priority: "medium",
    lead_type: null,
    lead_id: null,
    start_date: null,
    due_date: null,
    created_at: "",
    updated_at: "",
    issue_count: 0,
    done_count: 0,
    resource_count: 0,
  };
}

const ALPHA = project("p-alpha", "Alpha");
const BETA = project("p-beta", "Beta");

function renderToolbar(
  options: {
    /** Mirrors IssuesHeader's project-detail scope. */
    projectScopeFixed?: boolean;
    /** Mirrors an open saved view's query. */
    viewQuery?: Record<string, unknown>;
    /** Mirrors a paged surface: counts come from the server facets. */
    facetCountsExact?: boolean;
    tableFacetCounts?: IssueTableFacetsResponse;
    onTableFacetChange?: (facet: IssueTableFacetSpec | null) => void;
  } = {},
) {
  setApiInstance({
    listProjects: async () => ({ projects: [ALPHA, BETA], total: 2 }),
    listIssueStatuses: async () => ({ statuses: [], categories: [], total: 0 }),
    listProperties: async () => ({ properties: [] }),
  } as unknown as ApiClient);

  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  const store = createStore<IssueViewState>()(viewStoreSlice);

  const result = renderWithI18n(
    <QueryClientProvider client={qc}>
      <ViewStoreProvider store={store}>
        <IssueDisplayControls
          scopedIssues={[]}
          projectScopeFixed={options.projectScopeFixed}
          facetCountsExact={options.facetCountsExact}
          tableFacetCounts={options.tableFacetCounts}
          onTableFacetChange={options.onTableFacetChange}
          viewBaseline={
            options.viewQuery ? baselineFromQuery(options.viewQuery) : undefined
          }
        />
        <FilterChipsBar />
      </ViewStoreProvider>
    </QueryClientProvider>,
  );

  return { store, ...result };
}

/** The quick filter's own handle: its label IS the current selection, so a
 *  role query would have to change with every assertion. */
function projectTrigger(): HTMLElement {
  const trigger = document.querySelector<HTMLElement>(
    '[data-slot="project-filter-trigger"]',
  );
  if (!trigger) throw new Error("project quick filter trigger is not rendered");
  return trigger;
}

/** Opens the quick filter. Checkbox items keep the menu open (Base UI's
 *  closeOnClick default), so a test can toggle several in one visit. */
async function openQuickMenu() {
  fireEvent.click(projectTrigger());
  await screen.findByRole("menuitemcheckbox", { name: /Alpha/ });
}

/** The chip bar's project chip, matched through its remove affordance so the
 *  assertion cannot accidentally read the toolbar trigger's own label. */
async function projectChip() {
  const remove = await screen.findByRole("button", {
    name: "Remove Project filter",
  });
  const chip = remove.closest("span");
  if (!chip) throw new Error("project chip has no container");
  return chip;
}

afterEach(() => {
  cleanup();
  // Base UI portals menus onto document.body; leftovers would duplicate labels
  // across tests.
  document.body.innerHTML = "";
  vi.restoreAllMocks();
});

describe("toolbar project quick filter", () => {
  it("selects and unselects a project, and the chip bar follows", async () => {
    const { store } = renderToolbar();

    expect(projectTrigger()).toHaveTextContent("Project");
    expect(screen.queryByRole("button", { name: "Remove Project filter" })).toBeNull();

    await openQuickMenu();
    fireEvent.click(screen.getByRole("menuitemcheckbox", { name: /Alpha/ }));

    expect(store.getState().projectFilters).toEqual([ALPHA.id]);
    // One project reads as its own name on the trigger.
    await waitFor(() => expect(projectTrigger()).toHaveTextContent("Alpha"));
    expect(await projectChip()).toHaveTextContent("Alpha");

    // Selecting inside the still-open menu and toggling back off in it.
    fireEvent.click(screen.getByRole("menuitemcheckbox", { name: /Alpha/ }));

    expect(store.getState().projectFilters).toEqual([]);
    await waitFor(() => expect(projectTrigger()).toHaveTextContent("Project"));
    expect(screen.queryByRole("button", { name: "Remove Project filter" })).toBeNull();
  });

  it("counts the extra projects as +N once more than one is selected", async () => {
    const { store } = renderToolbar();

    await openQuickMenu();
    fireEvent.click(screen.getByRole("menuitemcheckbox", { name: /Alpha/ }));
    fireEvent.click(screen.getByRole("menuitemcheckbox", { name: /Beta/ }));

    expect(store.getState().projectFilters).toEqual([ALPHA.id, BETA.id]);
    await waitFor(() => expect(projectTrigger()).toHaveTextContent("Alpha+1"));
  });

  it("shows the no-project flag on its own, since it has no project name", async () => {
    const { store } = renderToolbar();

    await openQuickMenu();
    fireEvent.click(screen.getByRole("menuitemcheckbox", { name: /No project/ }));

    expect(store.getState().includeNoProject).toBe(true);
    await waitFor(() => expect(projectTrigger()).toHaveTextContent("No project"));
  });

  it("clears projects and the no-project flag from the quick menu", async () => {
    const { store } = renderToolbar();

    await openQuickMenu();
    fireEvent.click(screen.getByRole("menuitemcheckbox", { name: /Alpha/ }));
    fireEvent.click(screen.getByRole("menuitemcheckbox", { name: /No project/ }));
    expect(store.getState().includeNoProject).toBe(true);
    await waitFor(() => expect(projectTrigger()).toHaveTextContent("Alpha+1"));

    fireEvent.click(screen.getByRole("menuitem", { name: "Clear project filter" }));

    expect(store.getState().projectFilters).toEqual([]);
    expect(store.getState().includeNoProject).toBe(false);
    await waitFor(() => expect(projectTrigger()).toHaveTextContent("Project"));
    expect(screen.queryByRole("button", { name: "Remove Project filter" })).toBeNull();
  });

  it("agrees with the Filter menu's own project section, in both directions", async () => {
    const { store } = renderToolbar();

    fireEvent.click(screen.getByRole("button", { name: /^Filter/ }));
    fireEvent.click(await screen.findByRole("menuitem", { name: /^Project$/ }));
    const alphaInFilterMenu = await screen.findByRole("menuitemcheckbox", {
      name: /Alpha/,
    });
    expect(alphaInFilterMenu).toHaveAttribute("aria-checked", "false");

    // Selecting through the ORIGINAL entry point reaches the new button and
    // the chip bar with no propagation step of its own.
    fireEvent.click(alphaInFilterMenu);

    expect(store.getState().projectFilters).toEqual([ALPHA.id]);
    expect(alphaInFilterMenu).toHaveAttribute("aria-checked", "true");
    await waitFor(() => expect(projectTrigger()).toHaveTextContent("Alpha"));
    expect(await projectChip()).toHaveTextContent("Alpha");
  });

  it("renders no quick filter while the saved view pins the project dimension", () => {
    renderToolbar({ viewQuery: { projectFilters: [ALPHA.id] } });

    expect(
      document.querySelector('[data-slot="project-filter-trigger"]'),
    ).toBeNull();
    // The Filter menu keeps its project section, so the pinned project still
    // shows as checked-and-disabled rather than disappearing.
    expect(screen.getByRole("button", { name: /^Filter/ })).toBeInTheDocument();
  });

  it("renders no quick filter on a project-detail surface", () => {
    renderToolbar({ projectScopeFixed: true });

    expect(
      document.querySelector('[data-slot="project-filter-trigger"]'),
    ).toBeNull();
    expect(screen.getByRole("button", { name: /^Filter/ })).toBeInTheDocument();
  });

  it("keeps the quick filter when the open view pins something else", () => {
    renderToolbar({ viewQuery: { statusFilters: ["todo"] } });

    expect(projectTrigger()).toBeInTheDocument();
  });

  it("asks the surface for the project facet while it is open", async () => {
    const onTableFacetChange = vi.fn();
    renderToolbar({ facetCountsExact: false, onTableFacetChange });

    await openQuickMenu();
    expect(onTableFacetChange).toHaveBeenCalledWith({ kind: "project" });

    // Closing hands the facet back, exactly like the Filter menu's sub-menu:
    // paged surfaces only count the facet a visible menu asked for.
    fireEvent.keyDown(document.body, { key: "Escape" });
    await waitFor(() => expect(onTableFacetChange).toHaveBeenCalledWith(null));
  });

  it("badges the projects from the server facets it asked for", async () => {
    renderToolbar({
      facetCountsExact: false,
      tableFacetCounts: {
        query_fingerprint: "fp",
        total: 5,
        facets: [
          {
            kind: "project",
            values: [
              { key: ALPHA.id, count: 3 },
              { key: "__none__", count: 2 },
            ],
          },
        ],
      },
    });

    await openQuickMenu();
    expect(screen.getByRole("menuitemcheckbox", { name: /Alpha/ })).toHaveTextContent(
      "3",
    );
    expect(
      screen.getByRole("menuitemcheckbox", { name: /No project/ }),
    ).toHaveTextContent("2");
  });
});
