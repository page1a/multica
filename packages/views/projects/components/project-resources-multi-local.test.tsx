// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { screen, fireEvent, waitFor } from "@testing-library/react";
import { renderWithI18n } from "../../test/i18n";

// The picker after DENE-617: a project may hold several directories on one
// machine, a new one always starts on in-place, and the identity the machine
// measured travels with the save.
//
// The mode rules themselves (which option is blocked, and why) are the
// canonical matrix in local-directory-mode.test.ts; this suite covers the
// wiring the component owns.

const createMock = vi.fn().mockResolvedValue({});
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
];

vi.mock("@tanstack/react-query", () => ({
  useQuery: (options: { queryKey?: unknown[] }) =>
    options?.queryKey?.[0] === "project-resources" ? { data: existing } : { data: [] },
  queryOptions: (options: unknown) => options,
}));

vi.mock("@multica/core/projects", () => ({
  projectResourcesOptions: () => ({ queryKey: ["project-resources"], queryFn: vi.fn() }),
  useCreateProjectResource: () => ({ mutateAsync: createMock, isPending: false }),
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

const pickedPath = { current: "/Users/me/code/docs" };
const validation = {
  current: {
    ok: true,
    is_git_repo: true,
    real_path: "/Users/me/code/docs",
    repo_key: "github.com/o/docs",
    default_worktree_root: "/Users/me/code/docs.multica-worktrees",
  } as Record<string, unknown>,
};

vi.mock("../../platform/local-directory", () => ({
  isDesktopShell: () => true,
  pickDirectory: () =>
    Promise.resolve({
      ok: true,
      path: pickedPath.current,
      basename: pickedPath.current.split("/").pop(),
    }),
  validateLocalDirectory: () => Promise.resolve(validation.current),
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
// Hoisted: vi.mock factories run before module-level consts are initialised.
const { toastErrorMock } = vi.hoisted(() => ({ toastErrorMock: vi.fn() }));
vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: toastErrorMock },
}));

import { ProjectResourcesSection } from "./project-resources-section";

async function openPickerAndWaitForModes() {
  const button = screen.getByRole("button", { name: /add local directory/i });
  expect(button).not.toBeDisabled();
  fireEvent.click(button);
  await waitFor(() => expect(screen.getAllByRole("radio")).toHaveLength(3));
}

describe("ProjectResourcesSection — several local directories on one machine", () => {
  beforeEach(() => {
    createMock.mockClear();
    toastErrorMock.mockClear();
    pickedPath.current = "/Users/me/code/docs";
    validation.current = {
      ok: true,
      is_git_repo: true,
      real_path: "/Users/me/code/docs",
      repo_key: "github.com/o/docs",
      default_worktree_root: "/Users/me/code/docs.multica-worktrees",
    };
  });

  // Invariant 3. This is the behaviour change a user feels first: adding a git
  // repository used to preselect parallel mode, which silently committed them
  // to a full working copy per task. It now starts where "I added my project
  // folder" plainly means — in the folder.
  it("starts a newly added git repository on in-place, not parallel", async () => {
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    await openPickerAndWaitForModes();

    const radios = screen.getAllByRole("radio");
    expect(radios[0]).toBeChecked();
    expect(radios[1]).not.toBeChecked();

    fireEvent.click(screen.getByRole("button", { name: /add folder/i }));
    await waitFor(() => expect(createMock).toHaveBeenCalled());
    const payload = createMock.mock.calls[0]?.[0] as {
      resource_ref: Record<string, unknown>;
    };
    expect(payload.resource_ref.execution_mode).toBe("in_place");
  });

  // Parallel mode's cost is named while it is still a choice: a copy per task,
  // on the user's own disk, in a directory the dialog points at.
  it("names where parallel-mode copies would land before the user picks it", async () => {
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    await openPickerAndWaitForModes();
    expect(
      screen.getByText(/\/Users\/me\/code\/docs\.multica-worktrees/),
    ).toBeInTheDocument();
  });

  // Invariant 10/11's client half: the identity only this machine can measure
  // travels with the save, or the server has nothing to enforce the rules on.
  it("sends the measured real path, repository key and git status", async () => {
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    await openPickerAndWaitForModes();
    fireEvent.click(screen.getByRole("button", { name: /add folder/i }));

    await waitFor(() => expect(createMock).toHaveBeenCalled());
    const ref = (createMock.mock.calls[0]?.[0] as { resource_ref: Record<string, unknown> })
      .resource_ref;
    expect(ref.local_path).toBe("/Users/me/code/docs");
    expect(ref.real_path).toBe("/Users/me/code/docs");
    expect(ref.repo_key).toBe("github.com/o/docs");
    expect(ref.is_git_repo).toBe(true);
  });

  // An unidentifiable directory sends no repo_key at all. Empty and absent are
  // different to the server: absent means "nothing to compare", and an empty
  // string that compared equal to another empty string would make every plain
  // folder collide with every other.
  it("omits repo_key for a plain folder", async () => {
    pickedPath.current = "/Users/me/notes";
    validation.current = {
      ok: true,
      is_git_repo: false,
      real_path: "/Users/me/notes",
    };
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    await openPickerAndWaitForModes();
    expect(screen.getByRole("button", { name: /create local git/i })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /add folder/i }));

    await waitFor(() => expect(createMock).toHaveBeenCalled());
    const ref = (createMock.mock.calls[0]?.[0] as { resource_ref: Record<string, unknown> })
      .resource_ref;
    expect("repo_key" in ref).toBe(false);
    expect(ref.is_git_repo).toBe(false);
  });

  // The same directory reached by a different spelling is still the same
  // directory: identity is the resolved real path, so a symlink to one already
  // added is refused while the folder is still on screen rather than as a 409.
  it("refuses a directory already added, matching on the resolved real path", async () => {
    pickedPath.current = "/Users/me/shortcut-to-app";
    validation.current = {
      ok: true,
      is_git_repo: true,
      real_path: "/Users/me/code/app",
    };
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    fireEvent.click(screen.getByRole("button", { name: /add local directory/i }));

    await waitFor(() => expect(toastErrorMock).toHaveBeenCalled());
    expect(screen.queryAllByRole("radio")).toHaveLength(0);
    expect(createMock).not.toHaveBeenCalled();
  });
});

// The landing folder is not just visible before the user commits to parallel
// mode — it is theirs to change, and a location the daemon would refuse is
// caught while the dialog is still open rather than by a failed task later.
describe("ProjectResourcesSection — where parallel-mode copies land", () => {
  beforeEach(() => {
    createMock.mockClear();
    toastErrorMock.mockClear();
    pickedPath.current = "/Users/me/code/docs";
    validation.current = {
      ok: true,
      is_git_repo: true,
      real_path: "/Users/me/code/docs",
      git_root: "/Users/me/code/docs",
      default_worktree_root: "/Users/me/code/docs.multica-worktrees",
    };
  });

  async function chooseParallel() {
    fireEvent.click(screen.getByRole("button", { name: /add local directory/i }));
    await waitFor(() => expect(screen.getAllByRole("radio")).toHaveLength(3));
    fireEvent.click(screen.getAllByRole("radio")[1]!);
    return screen.findByLabelText(/working copies go in/i);
  }

  it("stores a location the user typed, and the default as absent", async () => {
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    const field = await chooseParallel();
    expect(field).toHaveValue("/Users/me/code/docs.multica-worktrees");

    // Confirming without touching it stores nothing: "beside the repository"
    // follows a repository the user later moves, a stored literal would not.
    fireEvent.click(screen.getByRole("button", { name: /add folder/i }));
    await waitFor(() => expect(createMock).toHaveBeenCalled());
    const ref = (createMock.mock.calls[0]?.[0] as { resource_ref: Record<string, unknown> })
      .resource_ref;
    expect("worktree_root" in ref).toBe(false);
    expect(ref.execution_mode).toBe("worktree");
  });

  it("stores a location the user chose", async () => {
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    const field = await chooseParallel();
    fireEvent.change(field, { target: { value: "/Volumes/Fast/copies" } });
    fireEvent.click(screen.getByRole("button", { name: /add folder/i }));

    await waitFor(() => expect(createMock).toHaveBeenCalled());
    const ref = (createMock.mock.calls[0]?.[0] as { resource_ref: Record<string, unknown> })
      .resource_ref;
    expect(ref.worktree_root).toBe("/Volumes/Fast/copies");
  });

  // A folder inside the repository would appear in the user's own git status
  // and need a .gitignore entry — the daemon refuses it, so the dialog does.
  it("refuses a landing folder inside the repository, before saving", async () => {
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    const field = await chooseParallel();
    fireEvent.change(field, { target: { value: "/Users/me/code/docs/.worktrees" } });

    expect(await screen.findByText(/inside the repository/i)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /add folder/i }));
    await waitFor(() => expect(createMock).not.toHaveBeenCalled());
  });

  it("refuses a relative landing folder", async () => {
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    const field = await chooseParallel();
    fireEvent.change(field, { target: { value: "copies" } });

    expect(await screen.findByText(/absolute path/i)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /add folder/i }));
    await waitFor(() => expect(createMock).not.toHaveBeenCalled());
  });

  // The field belongs to parallel mode alone. Showing it beside two modes that
  // ignore it invites an edit that does nothing.
  it("shows the landing folder only while parallel is the choice", async () => {
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    fireEvent.click(screen.getByRole("button", { name: /add local directory/i }));
    await waitFor(() => expect(screen.getAllByRole("radio")).toHaveLength(3));

    expect(screen.queryByLabelText(/working copies go in/i)).not.toBeInTheDocument();
    fireEvent.click(screen.getAllByRole("radio")[1]!);
    expect(await screen.findByLabelText(/working copies go in/i)).toBeInTheDocument();
    fireEvent.click(screen.getAllByRole("radio")[0]!);
    await waitFor(() =>
      expect(screen.queryByLabelText(/working copies go in/i)).not.toBeInTheDocument(),
    );
  });
});

// Upgrading an EXISTING directory to parallel is the same decision as choosing
// it while adding one, so the dialog has to tell the user the same thing: where
// the copies would land, and whether a folder typed there is inside the repo.
describe("ProjectResourcesSection — upgrading an existing directory to parallel", () => {
  beforeEach(() => {
    createMock.mockClear();
    updateMock.mockClear();
    toastErrorMock.mockClear();
    validation.current = {
      ok: true,
      is_git_repo: true,
      real_path: "/Users/me/code/app",
      git_root: "/Users/me/code/app",
      default_worktree_root: "/Users/me/code/app.multica-worktrees",
    };
  });

  async function openModeEditor() {
    fireEvent.click(
      await screen.findByRole("button", { name: /change how runs use this folder/i }),
    );
    await waitFor(() => expect(screen.getAllByRole("radio")).toHaveLength(3));
  }

  it("measures the saved directory and offers its landing folder", async () => {
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    await openModeEditor();
    fireEvent.click(screen.getAllByRole("radio")[1]!);

    // The measurement arrives after the dialog opens; the field appears with it.
    const field = await screen.findByLabelText(/working copies go in/i);
    expect(field).toHaveValue("/Users/me/code/app.multica-worktrees");
  });

  it("saves a changed landing folder onto the existing resource", async () => {
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    await openModeEditor();
    fireEvent.click(screen.getAllByRole("radio")[1]!);
    const field = await screen.findByLabelText(/working copies go in/i);
    fireEvent.change(field, { target: { value: "/Volumes/Fast/copies" } });
    fireEvent.click(screen.getByRole("button", { name: /save/i }));

    await waitFor(() => expect(updateMock).toHaveBeenCalled());
    const payload = updateMock.mock.calls[0]?.[0] as {
      data: { resource_ref: Record<string, unknown> };
    };
    expect(payload.data.resource_ref.execution_mode).toBe("worktree");
    expect(payload.data.resource_ref.worktree_root).toBe("/Volumes/Fast/copies");
    // Every other field survives: the server replaces the whole ref rather
    // than deep-merging it.
    expect(payload.data.resource_ref.local_path).toBe("/Users/me/code/app");
    expect(payload.data.resource_ref.real_path).toBe("/Users/me/code/app");
  });
});
