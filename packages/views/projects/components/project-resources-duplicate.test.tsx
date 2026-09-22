// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { screen, fireEvent, waitFor } from "@testing-library/react";
import { renderWithI18n } from "../../test/i18n";

// Wiring + the named regression only. The matching matrix (URL forms, name
// collisions, what counts as a duplicate) is canonical in
// packages/core/projects/source-rule.test.ts.

const deleteMock = vi.fn().mockResolvedValue({});

// The exact shape found in this workspace: one repository reachable three ways
// — two github_repo rows and the directory that actually holds it.
const RESOURCES = [
  {
    id: "remote-1",
    project_id: "p1",
    workspace_id: "workspace-1",
    resource_type: "github_repo",
    resource_ref: { url: "https://github.com/jeff-kunkun/multica" },
    label: null,
    position: 0,
    created_at: "2026-08-18T00:00:00Z",
    created_by: "u1",
  },
  {
    id: "remote-2",
    project_id: "p1",
    workspace_id: "workspace-1",
    resource_type: "github_repo",
    resource_ref: { url: "git@github.com:jeff-kunkun/multica.git" },
    label: null,
    position: 1,
    created_at: "2026-08-18T00:00:00Z",
    created_by: "u1",
  },
  // A second repository, also configured twice. It shares nothing with the
  // group above and must survive that group's merge untouched.
  {
    id: "remote-tarot-1",
    project_id: "p1",
    workspace_id: "workspace-1",
    resource_type: "github_repo",
    resource_ref: { url: "https://github.com/kun/online-tarot" },
    label: null,
    position: 2,
    created_at: "2026-08-18T00:00:00Z",
    created_by: "u1",
  },
  {
    id: "remote-tarot-2",
    project_id: "p1",
    workspace_id: "workspace-1",
    resource_type: "github_repo",
    resource_ref: { url: "git@github.com:kun/online-tarot.git" },
    label: null,
    position: 3,
    created_at: "2026-08-18T00:00:00Z",
    created_by: "u1",
  },
  {
    id: "local-1",
    project_id: "p1",
    workspace_id: "workspace-1",
    resource_type: "local_directory",
    resource_ref: {
      local_path: "/Users/kunkun/.agents/multica",
      daemon_id: "daemon-1",
      execution_mode: "worktree",
    },
    label: "multica",
    position: 4,
    created_at: "2026-08-18T00:00:00Z",
    created_by: "u1",
  },
];

vi.mock("@tanstack/react-query", () => ({
  useQuery: (options: { queryKey?: unknown[] }) => {
    const key = options?.queryKey?.[0];
    if (key === "project-resources") return { data: RESOURCES };
    return { data: [] };
  },
  queryOptions: (options: unknown) => options,
}));

vi.mock("@multica/core/projects", () => ({
  projectResourcesOptions: () => ({ queryKey: ["project-resources"], queryFn: vi.fn() }),
  useCreateProjectResource: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useUpdateProjectResource: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useDeleteProjectResource: () => ({ mutateAsync: deleteMock, isPending: false }),
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

describe("ProjectResourcesSection — duplicate code sources", () => {
  beforeEach(() => deleteMock.mockClear());

  // The whole defect: saving both said nothing, so nobody knew the machine was
  // keeping two copies of one repository.
  it("says the repository is configured twice instead of passing silently", () => {
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    expect(screen.getByText(/configured twice/i)).toBeInTheDocument();
  });

  it("merging removes every redundant remote row and keeps the local directory", async () => {
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);

    fireEvent.click(screen.getByRole("button", { name: /merge into local directory/i }));

    await waitFor(() => expect(deleteMock).toHaveBeenCalledTimes(2));
    const deleted = deleteMock.mock.calls.map((c) => c[0]);
    expect(deleted).toEqual(expect.arrayContaining(["remote-1", "remote-2"]));
    // Deleting the directory the user explicitly pointed at is not a merge.
    expect(deleted).not.toContain("local-1");
  });

  // Named regression: the button says "multica", so merging multica must not
  // delete online-tarot's rows. It did, because every redundant remote in the
  // project was folded into every group.
  it("merging one repository leaves another repository's rows alone", async () => {
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);

    fireEvent.click(screen.getByRole("button", { name: /merge into local directory/i }));

    await waitFor(() => expect(deleteMock).toHaveBeenCalled());
    const deleted = deleteMock.mock.calls.map((c) => c[0]);
    expect(deleted).not.toContain("remote-tarot-1");
    expect(deleted).not.toContain("remote-tarot-2");
  });
});
