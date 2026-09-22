// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { screen, fireEvent, waitFor } from "@testing-library/react";
import { renderWithI18n } from "../../test/i18n";

const updateMock = vi.fn().mockResolvedValue({});

function localDirectoryResource(executionMode: "in_place" | "worktree" | "shared") {
  return {
    id: "res-shared",
    project_id: "p1",
    workspace_id: "workspace-1",
    resource_type: "local_directory",
    resource_ref: {
      local_path: "/Users/dev/work/umbrella",
      daemon_id: "daemon-1",
      label: "Umbrella",
      execution_mode: executionMode,
    },
    label: "Umbrella",
    position: 0,
    created_at: "2026-08-18T00:00:00Z",
    created_by: "u1",
  };
}

const resources: ReturnType<typeof localDirectoryResource>[] = [];

vi.mock("@tanstack/react-query", () => ({
  useQuery: (options: { queryKey?: unknown[] }) => {
    const key = options?.queryKey?.[0];
    if (key === "project-resources") return { data: resources };
    return { data: [] };
  },
  queryOptions: (options: unknown) => options,
}));

vi.mock("@multica/core/projects", () => ({
  projectResourcesOptions: () => ({ queryKey: ["project-resources"], queryFn: vi.fn() }),
  useCreateProjectResource: () => ({ mutateAsync: vi.fn() }),
  useUpdateProjectResource: () => ({ mutateAsync: updateMock }),
  useDeleteProjectResource: () => ({ mutateAsync: vi.fn() }),
}));

vi.mock("@multica/core/config", () => ({
  useConfigStore: (selector: (state: {
    localWorktreeSupported: boolean;
    localSharedSupported: boolean;
  }) => unknown) =>
    selector({ localWorktreeSupported: true, localSharedSupported: true }),
}));

vi.mock("@multica/core/runtimes", () => ({
  runtimeListOptions: () => ({ queryKey: ["runtimes"], queryFn: vi.fn() }),
  runtimeAdvertisesLocalWorktree: () => true,
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));
vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "workspace-1", slug: "ws", repos: [] }),
}));
vi.mock("../../platform/local-directory", () => ({
  isDesktopShell: () => true,
  pickDirectory: vi.fn(),
  validateLocalDirectory: vi.fn(),
  validateWritablePath: async () => true,
}));
vi.mock("../../platform/use-local-daemon-status", () => ({
  useLocalDaemonStatus: () => ({ daemonId: "daemon-1", deviceName: "MacBook", running: true }),
}));
vi.mock("../../platform/use-local-directory-shared-overrides", () => ({
  useLocalDirectorySharedOverrides: () => ({
    canPersist: true,
    hasOverride: () => false,
    setOverride: vi.fn().mockResolvedValue({ ok: true }),
    refresh: vi.fn(),
  }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

import { ProjectResourcesSection } from "./project-resources-section";

function setResource(mode: "in_place" | "worktree" | "shared") {
  resources.splice(0, resources.length, localDirectoryResource(mode));
}

function openModeEditor() {
  fireEvent.click(screen.getByTitle(/change how runs use this folder/i));
}

function modeRadios(): HTMLElement[] {
  return screen.getAllByRole("radio") as HTMLElement[];
}

describe("ProjectResourcesSection — saved shared mode echo", () => {
  beforeEach(() => {
    updateMock.mockClear();
    setResource("shared");
  });

  // A helper that only recognises worktree collapses shared to in_place, so
  // the row looks like a locked working copy and the next save would restore
  // the path lock. The badge is the user-visible proof the stored mode survived.
  it("shows the Shared badge, not Parallel, for a saved shared resource", () => {
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    expect(screen.getByText("Shared")).toBeInTheDocument();
    expect(screen.queryByText("Parallel")).not.toBeInTheDocument();
  });

  it("preselects shared in the mode editor, not in_place", async () => {
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    openModeEditor();

    await waitFor(() => expect(modeRadios()).toHaveLength(3));
    const radios = modeRadios();
    expect(radios[0]?.getAttribute("aria-checked")).toBe("false");
    expect(radios[1]?.getAttribute("aria-checked")).toBe("false");
    expect(radios[2]?.getAttribute("aria-checked")).toBe("true");
  });

  // Saving with the stored mode still selected must be a no-op. If the editor
  // had preselected in_place, confirming would rewrite execution_mode and
  // restore the path lock the resource opted out of.
  it("does not rewrite the ref when the editor is saved without changing mode", async () => {
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    openModeEditor();
    await waitFor(() => expect(modeRadios()).toHaveLength(3));

    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
    expect(updateMock).not.toHaveBeenCalled();
  });

  it("still shows Parallel for a saved worktree resource", () => {
    setResource("worktree");
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    expect(screen.getByText("Parallel")).toBeInTheDocument();
    expect(screen.queryByText("Shared")).not.toBeInTheDocument();
  });
});
