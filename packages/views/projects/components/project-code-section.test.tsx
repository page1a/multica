import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ProjectRepoItem, RepoReach } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

const mockList = vi.hoisted(() => vi.fn());
const mockAttach = vi.hoisted(() => vi.fn());
const mockRemove = vi.hoisted(() => vi.fn());
const mockPush = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", () => ({
  api: {
    listProjectRepos: mockList,
    attachProjectRepo: mockAttach,
    removeProjectRepo: mockRemove,
    listRepoConnections: vi.fn(),
  },
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({ settings: () => "/acme/settings" }),
}));
vi.mock("../../navigation", () => ({
  useNavigation: () => ({ push: mockPush }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
// The picker has its own queries; here it only needs to hand back a choice.
vi.mock("./add-repo-dialog", () => ({
  AddRepoDialog: ({
    open,
    onSelect,
  }: {
    open: boolean;
    onSelect: (url: string) => Promise<void>;
  }) =>
    open ? (
      <button type="button" onClick={() => void onSelect("https://github.com/acme/new")}>
        pick-new
      </button>
    ) : null,
}));

import { ProjectCodeSection } from "./project-code-section";

function reach(over: Partial<RepoReach>): RepoReach {
  return {
    repo_url: "https://github.com/acme/api",
    key: "github.com/acme/api",
    provider: "github",
    mode: "none",
    state: "disconnected",
    account_login: "",
    link_id: null,
    last_lookup: { ok: null, at: "", error: "" },
    webhook: "",
    projects: [],
    can_configure: true,
    hint: "",
    next_action: null,
    ...over,
  };
}

function item(id: string, repo: RepoReach): ProjectRepoItem {
  return {
    resource: {
      id,
      project_id: "p-1",
      workspace_id: "ws-1",
      resource_type: "github_repo",
      resource_ref: { url: repo.repo_url },
      label: null,
      position: 0,
      created_at: "",
      created_by: null,
    } as ProjectRepoItem["resource"],
    repo,
  };
}

function renderSection() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <ProjectCodeSection projectId="p-1" />
    </QueryClientProvider>,
  );
}

describe("ProjectCodeSection", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("shows exactly what the server said for each repository", async () => {
    mockList.mockResolvedValue({
      total: 4,
      repos: [
        item("r1", reach({ repo_url: "https://github.com/acme/a", mode: "app", state: "connected", account_login: "acme" })),
        item("r2", reach({
          repo_url: "https://github.com/acme/b",
          next_action: { kind: "install_app", url: "https://github.com/apps/x/installations/new" },
        })),
        item("r3", reach({
          repo_url: "https://github.com/acme/c",
          mode: "token",
          state: "connected",
          next_action: { kind: "replace_token" },
        })),
        item("r4", reach({
          repo_url: "https://github.com/acme/d",
          can_configure: false,
          next_action: {
            kind: "ask_owner",
            for: "create_app",
            contacts: [{ id: "u1", name: "Kun", role: "owner" }],
          },
        })),
      ],
    });
    renderSection();

    expect(await screen.findByText("GitHub App · acme")).toBeInTheDocument();
    expect(screen.getByText("GitHub App not installed")).toBeInTheDocument();
    expect(screen.getByText("Token expired")).toBeInTheDocument();
    expect(screen.getByText(/ask Kun/)).toBeInTheDocument();

    // One button per row that has a next step; a connected repo and an
    // ask-the-owner repo have none.
    expect(screen.getByRole("button", { name: "Install GitHub App" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Replace token" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Create GitHub App" })).not.toBeInTheDocument();
  });

  it("opens the install URL the server named", async () => {
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    mockList.mockResolvedValue({
      total: 1,
      repos: [
        item("r1", reach({ next_action: { kind: "install_app", url: "https://github.com/apps/x/installations/new" } })),
      ],
    });
    renderSection();
    await userEvent.click(await screen.findByRole("button", { name: "Install GitHub App" }));
    expect(open).toHaveBeenCalledWith(
      "https://github.com/apps/x/installations/new",
      "_blank",
      "noopener",
    );
    open.mockRestore();
  });

  it("sends a token step to the connections page for that account", async () => {
    mockList.mockResolvedValue({
      total: 1,
      repos: [item("r1", reach({ next_action: { kind: "add_token", command: "multica vcs add" } }))],
    });
    renderSection();
    await userEvent.click(await screen.findByRole("button", { name: "Add token" }));
    expect(mockPush).toHaveBeenCalledWith(
      "/acme/settings?tab=git-connections&connect_scope=github.com%2Facme",
    );
  });

  it("attaches through the server and detaches by resource id", async () => {
    mockList.mockResolvedValue({
      total: 1,
      repos: [item("r1", reach({ mode: "app", state: "connected" }))],
    });
    mockAttach.mockResolvedValue({ created: true, registered: true });
    mockRemove.mockResolvedValue({ id: "r1", removed: true });
    renderSection();

    await userEvent.click(await screen.findByRole("button", { name: "Add repository" }));
    await userEvent.click(screen.getByRole("button", { name: "pick-new" }));
    await waitFor(() =>
      expect(mockAttach).toHaveBeenCalledWith("p-1", { repo_url: "https://github.com/acme/new" }),
    );

    await userEvent.click(screen.getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(mockRemove).toHaveBeenCalledWith("p-1", "r1"));
  });

  it("says so when the list cannot be loaded", async () => {
    mockList.mockRejectedValue(new Error("boom"));
    renderSection();
    expect(await screen.findByText(/couldn.t load/i)).toBeInTheDocument();
  });
});
