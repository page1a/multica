// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { screen, fireEvent, waitFor } from "@testing-library/react";
import { renderWithI18n } from "../../test/i18n";

// Reordering the local directories on this machine (DENE-617). The hint tells
// the user the FIRST directory is the one tasks write in, so the list needs an
// entry point that changes which one that is — the hint described a gesture
// that did not exist.
//
// The ordering rule itself is the canonical matrix in
// local-directory-order.test.ts; this covers the wiring: which controls the
// rows offer, and what they send.

const updateMock = vi.fn().mockResolvedValue({});

const existing = [
  {
    id: "r1",
    resource_type: "local_directory",
    resource_ref: {
      local_path: "/Users/me/code/app",
      real_path: "/Users/me/code/app",
      daemon_id: "daemon-1",
      label: "app",
    },
    position: 0,
  },
  {
    id: "r2",
    resource_type: "local_directory",
    resource_ref: {
      local_path: "/Users/me/code/docs",
      real_path: "/Users/me/code/docs",
      daemon_id: "daemon-1",
      label: "docs",
    },
    position: 1,
  },
  // Another machine's directory: never a neighbour, and never gets arrows.
  {
    id: "r3",
    resource_type: "local_directory",
    resource_ref: {
      local_path: "/Users/me/code/other",
      real_path: "/Users/me/code/other",
      daemon_id: "daemon-2",
      label: "other",
    },
    position: 2,
  },
];

vi.mock("@tanstack/react-query", () => ({
  useQuery: (options: { queryKey?: unknown[] }) => {
    const key = options?.queryKey ?? [];
    if (key[0] === "project-resources") return { data: existing };
    if (key.includes("code-decision")) {
      return {
        data: {
          kind: "local_in_place",
          display_name: "app",
          path: "/Users/me/code/app",
        },
      };
    }
    return { data: [] };
  },
  queryOptions: (options: unknown) => options,
}));

vi.mock("@multica/core/projects", () => ({
  projectResourcesOptions: () => ({ queryKey: ["project-resources"], queryFn: vi.fn() }),
  useCreateProjectResource: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useUpdateProjectResource: () => ({ mutateAsync: updateMock }),
  useDeleteProjectResource: () => ({ mutateAsync: vi.fn() }),
}));

vi.mock("@multica/core/config", () => ({
  useConfigStore: (
    selector: (state: {
      localWorktreeSupported: boolean;
      localSharedSupported: boolean;
    }) => unknown,
  ) => selector({ localWorktreeSupported: true, localSharedSupported: true }),
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
  initLocalGit: async () => ({ ok: false, reason: "unsupported" as const }),
}));
vi.mock("../../platform/use-local-daemon-status", () => ({
  useLocalDaemonStatus: () => ({
    daemonId: "daemon-1",
    deviceName: "MacBook",
    running: true,
  }),
}));
vi.mock("../../platform/use-local-directory-shared-overrides", () => ({
  useLocalDirectorySharedOverrides: () => ({
    canPersist: true,
    hasOverride: () => false,
    setOverride: vi.fn().mockResolvedValue({ ok: true }),
    refresh: vi.fn(),
  }),
}));
vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

import { ProjectResourcesSection } from "./project-resources-section";

describe("ProjectResourcesSection — choosing which directory tasks write in", () => {
  beforeEach(() => {
    updateMock.mockClear();
  });

  it("offers move controls only where a move is possible", () => {
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);

    // Two directories on this machine: both rows carry the pair so the row
    // does not reflow, but only the move that exists is enabled — up on the
    // second, down on the first. The other machine's row has neither control,
    // because its order is not this machine's to change.
    const enabled = (name: RegExp) =>
      screen.queryAllByRole("button", { name }).filter((b) => !b.hasAttribute("disabled"));
    expect(screen.getAllByRole("button", { name: /move up/i })).toHaveLength(2);
    expect(enabled(/move up/i)).toHaveLength(1);
    expect(enabled(/move down/i)).toHaveLength(1);
  });

  it("promotes the second directory to the working directory", async () => {
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);

    const moveUp = screen
      .getAllByRole("button", { name: /move up/i })
      .find((b) => !b.hasAttribute("disabled"));
    expect(moveUp).toBeDefined();
    fireEvent.click(moveUp!);

    await waitFor(() => expect(updateMock).toHaveBeenCalled());
    const byId = new Map(
      updateMock.mock.calls.map(([call]) => [call.resourceId, call.data]),
    );
    // Only position travels: resending resource_ref on an unrelated edit is
    // how a rename once dropped a directory's isolation (#7113).
    for (const data of byId.values()) {
      expect(Object.keys(data as object)).toEqual(["position"]);
    }
    const promoted = byId.get("r2") as { position: number } | undefined;
    const demoted = byId.get("r1") as { position: number } | undefined;
    expect(promoted).toBeDefined();
    expect(promoted!.position).toBeLessThan(
      demoted ? demoted.position : existing[0]!.position,
    );
  });

  // A reorder is several sequential position writes. A second click landing
  // mid-flight computes its patch from the pre-refresh list, which writes a
  // half-set of new numbers (DENE-648).
  it("locks every arrow while the reorder's writes are in flight", async () => {
    let release: (() => void) | undefined;
    updateMock.mockImplementationOnce(
      () =>
        new Promise<void>((resolve) => {
          release = () => resolve();
        }),
    );
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);

    const arrows = () => [
      ...screen.getAllByRole("button", { name: /move up/i }),
      ...screen.getAllByRole("button", { name: /move down/i }),
    ];
    const moveUp = arrows().find(
      (b) => /move up/i.test(b.getAttribute("aria-label") ?? "") && !b.hasAttribute("disabled"),
    );
    fireEvent.click(moveUp!);

    await waitFor(() =>
      expect(arrows().every((b) => b.hasAttribute("disabled"))).toBe(true),
    );

    release!();
    await waitFor(() =>
      expect(arrows().some((b) => !b.hasAttribute("disabled"))).toBe(true),
    );
  });

  // `disabled:opacity-30` outranks `group-hover:opacity-100`, so the arrow at
  // the end of the list sat visible without hovering while the usable one was
  // the hidden one — exactly backwards (DENE-648).
  it("keeps a disabled arrow hidden until the row is hovered", () => {
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    const disabled = screen
      .getAllByRole("button", { name: /move up/i })
      .find((b) => b.hasAttribute("disabled"));
    expect(disabled).toBeDefined();
    const className = disabled!.className;
    expect(className).toContain("disabled:opacity-0");
    expect(className).toContain("group-hover:disabled:opacity-30");
  });

  // Where tasks run is the server's sentence, shown as it arrived. The list
  // still reorders with arrows; the sentence must not invent a gesture.
  it("shows the server's decision and does not invent a drag gesture", () => {
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    const banner = screen.getByRole("status");
    expect(banner.textContent ?? "").toMatch(/app/);
    expect(banner.textContent ?? "").not.toMatch(/drag/i);
    expect(screen.queryByText(/first directory/i)).not.toBeInTheDocument();
  });
});
