// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { screen, fireEvent, waitFor } from "@testing-library/react";
import { renderWithI18n } from "../../test/i18n";

const createMock = vi.fn().mockResolvedValue({});
const setOverrideMock = vi.fn().mockResolvedValue({ ok: true });

vi.mock("@tanstack/react-query", () => ({
  useQuery: (options: { queryKey?: unknown[] }) => {
    const key = options?.queryKey?.[0];
    if (key === "project-resources") return { data: [] };
    return { data: [] };
  },
  queryOptions: (options: unknown) => options,
}));

vi.mock("@multica/core/projects", () => ({
  projectResourcesOptions: () => ({ queryKey: ["project-resources"], queryFn: vi.fn() }),
  useCreateProjectResource: () => ({ mutateAsync: createMock, isPending: false }),
  useUpdateProjectResource: () => ({ mutateAsync: vi.fn() }),
  useDeleteProjectResource: () => ({ mutateAsync: vi.fn() }),
}));

vi.mock("@multica/core/config", () => ({
  useConfigStore: (selector: (state: {
    localWorktreeSupported: boolean;
    localSharedSupported: boolean;
  }) => unknown) =>
    selector({ localWorktreeSupported: true, localSharedSupported: false }),
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
  pickDirectory: () =>
    Promise.resolve({
      ok: true,
      path: "/Volumes/Stoige/pg-game",
      basename: "pg-game",
    }),
  validateLocalDirectory: () => Promise.resolve({ ok: true, is_git_repo: false }),
  validateWritablePath: async () => true,
}));
vi.mock("../../platform/use-local-daemon-status", () => ({
  useLocalDaemonStatus: () => ({ daemonId: "daemon-1", deviceName: "MacBook", running: true }),
}));
vi.mock("../../platform/use-local-directory-shared-overrides", () => ({
  useLocalDirectorySharedOverrides: () => ({
    canPersist: true,
    hasOverride: () => false,
    setOverride: setOverrideMock,
    refresh: vi.fn(),
  }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

import { ProjectResourcesSection } from "./project-resources-section";

describe("ProjectResourcesSection — official cloud shared save", () => {
  beforeEach(() => {
    createMock.mockClear();
    setOverrideMock.mockClear();
  });

  it("stores in_place and records a local override instead of POSTing shared", async () => {
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);

    fireEvent.click(screen.getByRole("button", { name: /add local directory/i }));
    await waitFor(() => expect(screen.getAllByRole("radio")).toHaveLength(3));

    const radios = screen.getAllByRole("radio");
    expect(radios[2]?.hasAttribute("disabled")).toBe(false);
    fireEvent.click(radios[2]!);
    fireEvent.click(screen.getByRole("button", { name: /add folder/i }));

    await waitFor(() => expect(createMock).toHaveBeenCalled());
    const payload = createMock.mock.calls[0]?.[0] as {
      resource_ref: { execution_mode: string; local_path: string };
    };
    expect(payload.resource_ref.execution_mode).toBe("in_place");
    expect(payload.resource_ref.local_path).toBe("/Volumes/Stoige/pg-game");
    expect(setOverrideMock).toHaveBeenCalledWith(
      "daemon-1",
      "/Volumes/Stoige/pg-game",
      true,
    );
  });
});
