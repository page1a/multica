// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, screen } from "@testing-library/react";
import type { Project } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { ChatAddMenu } from "./chat-add-menu";

// Set arithmetic (which entry a toggle adds or drops, and in what order) is
// covered once in packages/core/chat/project-context.test.ts. This suite owns
// the wiring: what the menu renders, and the complete set it hands back.

function project(id: string, title: string): Project {
  return {
    id,
    workspace_id: "ws-1",
    title,
    description: null,
    icon: "\u{1F4D8}",
    status: "planned",
    priority: "none",
    lead_type: null,
    lead_id: null,
    start_date: null,
    due_date: null,
    created_at: new Date(0).toISOString(),
    updated_at: new Date(0).toISOString(),
    issue_count: 0,
    done_count: 0,
    resource_count: 0,
  } as Project;
}

const ALPHA = project("project-alpha", "Project Alpha");
const BETA = project("project-beta", "Project Beta");

async function openProjectSubmenu(props: Partial<React.ComponentProps<typeof ChatAddMenu>>) {
  const onProjectsChange = vi.fn();
  renderWithI18n(
    <ChatAddMenu
      projects={[ALPHA, BETA]}
      projectIds={[]}
      onProjectsChange={onProjectsChange}
      {...props}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Add" }));
  fireEvent.click(await screen.findByRole("menuitem", { name: /^Project context/ }));
  return onProjectsChange;
}

afterEach(cleanup);

describe("ChatAddMenu project context", () => {
  it("renders the attached projects as checkboxes reflecting the set", async () => {
    await openProjectSubmenu({ projectIds: [ALPHA.id, BETA.id] });

    expect(
      await screen.findByRole("menuitemcheckbox", { name: /Project Alpha/ }),
    ).toBeChecked();
    expect(
      screen.getByRole("menuitemcheckbox", { name: /Project Beta/ }),
    ).toBeChecked();
  });

  // DENE-603 §3: the first screen is the project at hand, not the workspace.
  it("opens focused on the current project and hides the rest", async () => {
    await openProjectSubmenu({ currentProjectId: ALPHA.id, projectIds: [] });

    expect(await screen.findByRole("menuitemcheckbox", { name: /Project Alpha/ }))
      .toBeInTheDocument();
    expect(screen.queryByRole("menuitemcheckbox", { name: /Project Beta/ })).toBeNull();
  });

  it("reveals the whole workspace from the switch entry", async () => {
    const onProjectsChange = await openProjectSubmenu({
      currentProjectId: ALPHA.id,
      projectIds: [],
    });

    fireEvent.click(
      await screen.findByRole("menuitem", { name: "Switch to another project…" }),
    );

    fireEvent.click(
      await screen.findByRole("menuitemcheckbox", { name: /Project Beta/ }),
    );
    expect(onProjectsChange).toHaveBeenCalledWith([BETA.id]);
  });

  // An empty first screen would teach nothing and cost an extra click.
  it("falls back to the full list when nothing is current or attached", async () => {
    await openProjectSubmenu({ projectIds: [] });

    expect(await screen.findByRole("menuitemcheckbox", { name: /Project Alpha/ }))
      .toBeInTheDocument();
    expect(
      screen.getByRole("menuitemcheckbox", { name: /Project Beta/ }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: "Switch to another project…" }))
      .toBeNull();
  });

  // The whole point of DENE-522: a second project is ADDED, not swapped in.
  it("hands back the whole set when a second project is checked", async () => {
    const onProjectsChange = await openProjectSubmenu({
      projectIds: [ALPHA.id],
      currentProjectId: BETA.id,
    });

    fireEvent.click(
      await screen.findByRole("menuitemcheckbox", { name: /Project Beta/ }),
    );

    expect(onProjectsChange).toHaveBeenCalledWith([ALPHA.id, BETA.id]);
  });

  it("drops just the unchecked project", async () => {
    const onProjectsChange = await openProjectSubmenu({
      projectIds: [ALPHA.id, BETA.id],
    });

    fireEvent.click(
      await screen.findByRole("menuitemcheckbox", { name: /Project Alpha/ }),
    );

    expect(onProjectsChange).toHaveBeenCalledWith([BETA.id]);
  });

  it("clears the whole set from the remove-all entry", async () => {
    const onProjectsChange = await openProjectSubmenu({
      projectIds: [ALPHA.id, BETA.id],
    });

    fireEvent.click(
      await screen.findByRole("menuitem", { name: "Remove all project context" }),
    );

    expect(onProjectsChange).toHaveBeenCalledWith([]);
  });

  it("offers no remove-all entry when nothing is attached", async () => {
    await openProjectSubmenu({ projectIds: [] });

    expect(await screen.findByRole("menuitemcheckbox", { name: /Project Alpha/ }))
      .toBeInTheDocument();
    expect(
      screen.queryByRole("menuitem", { name: "Remove all project context" }),
    ).toBeNull();
  });

  it("explains an empty project list instead of rendering an empty menu", async () => {
    await openProjectSubmenu({ projects: [] });

    expect(await screen.findByText("No projects yet")).toBeInTheDocument();
  });

  // Soft gate (MUL-5150): the daemon warning must inform, never lock the
  // selection — a user can still attach context for the run after an upgrade.
  it("warns about an outdated daemon without disabling selection", async () => {
    const onProjectsChange = await openProjectSubmenu({
      projectIds: [ALPHA.id],
      currentProjectId: BETA.id,
      projectContextUnsupported: true,
    });

    expect(
      await screen.findByText(
        "Project description won't apply — this agent's daemon needs an upgrade",
      ),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("menuitemcheckbox", { name: /Project Beta/ }));
    expect(onProjectsChange).toHaveBeenCalled();
  });
});

describe("ChatAddMenu linked projects (DENE-1643)", () => {
  const OPTION = {
    link_id: "link-1",
    id: "shared-1",
    title: "Shared Docs",
    icon: null,
    source: { name: "Acme", avatar_url: null },
  };

  it("lists shared projects under their own group and adds one to the set", async () => {
    const onLinkedProjectsChange = vi.fn();
    await openProjectSubmenu({
      projects: [],
      linkedProjectOptions: [OPTION],
      linkedProjects: [],
      onLinkedProjectsChange,
    });

    expect(await screen.findByText("Linked projects (read-only)")).toBeInTheDocument();
    expect(screen.queryByText("No projects yet")).toBeNull();
    fireEvent.click(screen.getByRole("menuitemcheckbox", { name: /Shared Docs/ }));
    expect(onLinkedProjectsChange).toHaveBeenCalledWith([
      { link_id: "link-1", project_id: "shared-1" },
    ]);
  });

  it("keeps a stale attachment ticked and marked so it can be removed", async () => {
    const onLinkedProjectsChange = vi.fn();
    await openProjectSubmenu({
      linkedProjectOptions: [],
      linkedProjects: [{
        link_id: "link-gone",
        project_id: "shared-gone",
        title: "Old Docs",
        icon: null,
        source_name: "Acme",
        available: false,
      }],
      onLinkedProjectsChange,
    });

    const item = await screen.findByRole("menuitemcheckbox", { name: /Old Docs/ });
    expect(item).toBeChecked();
    expect(item).toHaveTextContent("Stale");
    fireEvent.click(item);
    expect(onLinkedProjectsChange).toHaveBeenCalledWith([]);
  });

  it("drops one stale entry at a time and keeps the rest of the set", async () => {
    const onLinkedProjectsChange = vi.fn();
    const stale = (id: string, title: string) => ({
      link_id: "link-gone",
      project_id: id,
      title,
      icon: null,
      source_name: "Acme",
      available: false,
    });
    await openProjectSubmenu({
      linkedProjectOptions: [OPTION],
      linkedProjects: [
        stale("gone-a", "Old A"),
        stale("gone-b", "Old B"),
        { ...stale("shared-1", "Shared Docs"), link_id: "link-1", available: true },
      ],
      onLinkedProjectsChange,
    });

    fireEvent.click(await screen.findByRole("menuitemcheckbox", { name: /Old A/ }));
    expect(onLinkedProjectsChange).toHaveBeenCalledWith([
      { link_id: "link-gone", project_id: "gone-b" },
      { link_id: "link-1", project_id: "shared-1" },
    ]);
  });
});
