import { createStore } from "zustand/vanilla";
import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  type IssueViewState,
  viewStoreSlice,
} from "@multica/core/issues/stores/view-store";
import { ViewStoreProvider } from "@multica/core/issues/stores/view-store-context";
import { renderWithI18n } from "../../test/i18n";
import { DraftDefinitionFields, SaveViewDialog, type SaveViewScope } from "./save-view-dialog";

const mocks = vi.hoisted(() => ({
  createMutate: vi.fn(),
  updateMutate: vi.fn(),
}));

vi.mock("@multica/core/issue-views/mutations", () => ({
  useCreateIssueView: () => ({ mutate: mocks.createMutate, isPending: false }),
  useUpdateIssueView: () => ({ mutate: mocks.updateMutate, isPending: false }),
}));

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: () => ({ data: [] }),
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("./issues-header", () => ({
  IssueFilterMenu: ({ trigger }: { trigger: React.ReactNode }) => trigger,
}));

vi.mock("./filter-chips-bar", () => ({
  FilterChipList: ({ trailing }: { trailing?: React.ReactNode }) => trailing,
}));

function renderFields(sortBy: IssueViewState["sortBy"]) {
  const store = createStore<IssueViewState>()(viewStoreSlice);
  store.setState({ sortBy, sortDirection: "asc" });
  renderWithI18n(
    <ViewStoreProvider store={store}>
      <DraftDefinitionFields />
    </ViewStoreProvider>,
  );
  return store;
}

describe("DraftDefinitionFields ordering", () => {
  it("lets a saved view default switch between ascending and descending", async () => {
    const user = userEvent.setup();
    const store = renderFields("created_at");

    await user.click(screen.getByRole("button", { name: /Default display/ }));
    await user.click(screen.getByRole("button", { name: "Oldest first" }));

    expect(store.getState().sortDirection).toBe("desc");
    expect(screen.getByRole("button", { name: "Newest first" })).toBeInTheDocument();
  });

  it("hides direction for manual ordering, where direction has no effect", async () => {
    const user = userEvent.setup();
    renderFields("position");

    await user.click(screen.getByRole("button", { name: /Default display/ }));

    expect(screen.queryByRole("button", { name: "Workflow order" })).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Reverse workflow order" }),
    ).not.toBeInTheDocument();
  });
});

function renderSaveDialog(scope: SaveViewScope) {
  const store = createStore<IssueViewState>()(viewStoreSlice);
  renderWithI18n(
    <ViewStoreProvider store={store}>
      <SaveViewDialog open onOpenChange={() => {}} scope={scope} />
    </ViewStoreProvider>,
  );
}

async function openVisibility(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole("combobox", { name: "Visibility" }));
}

describe("SaveViewDialog visibility", () => {
  it("offers three sharing choices on a project surface", async () => {
    const user = userEvent.setup();
    renderSaveDialog({ kind: "project", projectId: "proj-1" });

    await openVisibility(user);

    expect(screen.getByRole("option", { name: "Only visible to me" })).toBeInTheDocument();
    expect(
      screen.getByRole("option", { name: "Visible to workspace members" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Project members" })).toBeInTheDocument();
    expect(
      screen.getByText(
        "Visible only to members of this project, and only on this project page",
      ),
    ).toBeInTheDocument();
  });

  it("hides the project choice on a workspace surface", async () => {
    const user = userEvent.setup();
    renderSaveDialog({ kind: "workspace" });

    await openVisibility(user);

    expect(screen.getByRole("option", { name: "Only visible to me" })).toBeInTheDocument();
    expect(
      screen.getByRole("option", { name: "Visible to workspace members" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "Project members" })).not.toBeInTheDocument();
  });

  it("does not show a sharing control on My issues", () => {
    renderSaveDialog({ kind: "my", variant: "assigned" });

    expect(screen.queryByRole("combobox", { name: "Visibility" })).not.toBeInTheDocument();
    expect(screen.getByText("My issues views are visible only to you")).toBeInTheDocument();
  });

  it("submits visibility=project with the project scope on the request body", async () => {
    const user = userEvent.setup();
    renderSaveDialog({ kind: "project", projectId: "proj-1" });

    await user.type(screen.getByLabelText("Name"), "Sprint board");
    await openVisibility(user);
    await user.click(screen.getByRole("option", { name: "Project members" }));
    await user.click(screen.getByRole("button", { name: "Create view" }));

    expect(mocks.createMutate).toHaveBeenCalledTimes(1);
    expect(mocks.createMutate.mock.calls[0]![0]).toEqual(
      expect.objectContaining({
        name: "Sprint board",
        visibility: "project",
        scope_type: "project",
        scope_id: "proj-1",
      }),
    );
  });
});

describe("SaveViewDialog draft lifecycle", () => {
  const liveStore = createStore<IssueViewState>()(viewStoreSlice);
  // Hosts build `scope` inline, so every host render passes a new object.
  const dialog = (open: boolean) => (
    <ViewStoreProvider store={liveStore}>
      <SaveViewDialog
        open={open}
        onOpenChange={() => {}}
        scope={{ kind: "my", variant: "assigned" }}
      />
    </ViewStoreProvider>
  );

  it("keeps a half-typed name when the host re-renders", async () => {
    const user = userEvent.setup();
    const { rerender } = renderWithI18n(dialog(true));

    await user.type(screen.getByLabelText("Name"), "Ongoing");
    rerender(dialog(true));

    expect(screen.getByLabelText("Name")).toHaveValue("Ongoing");
  });

  it("starts from a blank name on the next open", async () => {
    const user = userEvent.setup();
    const { rerender } = renderWithI18n(dialog(true));

    await user.type(screen.getByLabelText("Name"), "Ongoing");
    rerender(dialog(false));
    rerender(dialog(true));

    expect(screen.getByLabelText("Name")).toHaveValue("");
  });
});
