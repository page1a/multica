import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { IssueView } from "@multica/core/api/schemas";
import { renderWithI18n } from "../../test/i18n";
import { ManageViewsDialog } from "./manage-views-dialog";
import type { ViewBarItem } from "./view-bar-popover";

function makeView(overrides: Partial<IssueView> = {}): IssueView {
  return {
    id: "view-1",
    workspace_id: "ws-1",
    owner_id: "user-1",
    name: "Sprint board",
    scope_type: "project",
    scope_id: "proj-1",
    scope_variant: null,
    visibility: "private",
    definition_version: 1,
    query: {},
    display: {},
    revision: 1,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

function renderManager(views: IssueView[], onChangeVisibility = vi.fn()) {
  const items: ViewBarItem[] = [
    { barItemId: "builtin:all", label: "All issues", kind: "builtin" },
    ...views.map((view) => ({
      barItemId: `view:${view.id}`,
      label: view.name,
      kind: "view" as const,
      view,
      canManage: true,
    })),
  ];
  renderWithI18n(
    <ManageViewsDialog
      open
      onOpenChange={() => {}}
      items={items}
      hiddenSet={new Set()}
      anchorId="builtin:all"
      onReorder={() => {}}
      onToggleHidden={() => {}}
      onEditView={() => {}}
      onDeleteView={async () => {}}
      onChangeVisibility={onChangeVisibility}
    />,
  );
  return onChangeVisibility;
}

describe("ManageViewsDialog visibility", () => {
  it("offers three sharing choices on a project-scoped view", async () => {
    const user = userEvent.setup();
    renderManager([makeView({ visibility: "project" })]);

    expect(screen.getByText("Project shared")).toBeInTheDocument();
    await user.click(screen.getByRole("combobox", { name: "Sharing for Sprint board" }));

    expect(await screen.findByRole("option", { name: "Only visible to me" })).toBeInTheDocument();
    expect(
      await screen.findByRole("option", { name: "Visible to workspace members" }),
    ).toBeInTheDocument();
    expect(await screen.findByRole("option", { name: "Project members" })).toBeInTheDocument();
  });

  it("hides the project choice on a workspace-scoped view", async () => {
    const user = userEvent.setup();
    renderManager([
      makeView({
        id: "view-ws",
        name: "Workspace board",
        scope_type: "workspace",
        scope_id: null,
        visibility: "workspace",
      }),
    ]);

    await user.click(
      screen.getByRole("combobox", { name: "Sharing for Workspace board" }),
    );

    expect(await screen.findByRole("option", { name: "Only visible to me" })).toBeInTheDocument();
    expect(
      await screen.findByRole("option", { name: "Visible to workspace members" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "Project members" })).not.toBeInTheDocument();
    expect(screen.queryByText("Project shared")).not.toBeInTheDocument();
  });

  it("patches visibility through updateIssueView when switching to project", async () => {
    const user = userEvent.setup();
    const view = makeView({ visibility: "private" });
    const onChangeVisibility = renderManager([view]);

    await user.click(screen.getByRole("combobox", { name: "Sharing for Sprint board" }));
    await user.click(await screen.findByRole("option", { name: "Project members" }));

    expect(onChangeVisibility).toHaveBeenCalledWith(view, "project");
  });
});
