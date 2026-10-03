import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enSettings from "../../locales/en/settings.json";

const mockUpdateWorkspace = vi.hoisted(() => vi.fn());
const mockGetGitHubConnectURL = vi.hoisted(() => vi.fn());
const mockFetchNextPage = vi.hoisted(() => vi.fn());
const mockNavReplace = vi.hoisted(() => vi.fn());
const mockNavPush = vi.hoisted(() => vi.fn());
const mockToastSuccess = vi.hoisted(() => vi.fn());
const mockToastError = vi.hoisted(() => vi.fn());
const mockInvalidate = vi.hoisted(() => vi.fn());
const mockTestRepoBinding = vi.hoisted(() => vi.fn());
const mockPinRepoBinding = vi.hoisted(() => vi.fn());
const catalogRef = vi.hoisted(() => ({
  current: {
    links: [] as {
      id: string;
      kind: string;
      host: string;
      owner: string;
      visibility: string;
      health: string;
      can_manage: boolean;
    }[],
    bindings: [] as {
      repo_url: string;
      state: string;
      can_configure: boolean;
      pinned_link_id?: string | null;
      source_projects?: { id: string; title: string }[];
    }[],
    can_add_workspace: false,
    can_add_personal: false,
  },
}));
const workspaceRef = vi.hoisted(() => ({
  current: {
    id: "workspace-1",
    name: "Test Workspace",
    slug: "test-workspace",
    repos: [{ url: "https://github.com/multica-ai/multica" }] as {
      url: string;
      description?: string;
    }[],
  },
}));
const membersRef = vi.hoisted(() => ({
  current: [{ user_id: "user-1", role: "owner" as "owner" | "admin" | "member" }],
}));
const githubRef = vi.hoisted(() => ({
  current: {
    installations: [] as { id: string; account_login: string }[],
    configured: true,
    repository_browse_configured: true,
    can_manage: true,
  },
}));
const githubQueryStateRef = vi.hoisted(() => ({
  current: {
    isPending: false,
    isFetching: false,
  },
}));
const githubRepositoriesRef = vi.hoisted(() => ({
  current: [] as {
    id: number;
    full_name: string;
    html_url: string;
    clone_url: string;
    description: string | null;
    private: boolean;
    archived: boolean;
    default_branch: string;
  }[],
}));
const searchParamsRef = vi.hoisted(() => ({
  current: new URLSearchParams("tab=repositories"),
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (options: { queryKey: readonly unknown[] }) => {
    if (options.queryKey.includes("installations")) {
      return { data: githubRef.current, ...githubQueryStateRef.current, isError: false };
    }
    if (options.queryKey[0] === "repo-links") {
      return { data: catalogRef.current, isPending: false, isError: false, error: null };
    }
    if (options.queryKey[0] === "vcs") {
      return {
        data: { connections: [], can_manage: false },
        isPending: false,
        isError: false,
        error: null,
      };
    }
    return { data: membersRef.current, isPending: false, isError: false, error: null };
  },
  useInfiniteQuery: () => ({
    data: {
      pages: [
        {
          repositories: githubRepositoriesRef.current,
          total_count: githubRepositoriesRef.current.length,
          next_page: null,
        },
      ],
    },
    isPending: false,
    isError: false,
    hasNextPage: false,
    isFetchingNextPage: false,
    fetchNextPage: mockFetchNextPage,
  }),
  useQueryClient: () => ({ setQueryData: vi.fn(), invalidateQueries: mockInvalidate }),
  queryOptions: <T,>(options: T) => options,
  infiniteQueryOptions: <T,>(options: T) => options,
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => workspaceRef.current,
  useWorkspacePaths: () => ({ projectDetail: (id: string) => `/test-workspace/projects/${id}` }),
}));

vi.mock("@multica/core/permissions", () => ({
  useCurrentMember: () => ({ role: membersRef.current[0]?.role ?? null }),
}));

vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members"], queryFn: vi.fn() }),
  workspaceKeys: { list: () => ["workspaces"] },
}));

vi.mock("@multica/core/api", () => ({
  ApiError: class ApiError extends Error {
    status: number;
    constructor(message: string, status: number) {
      super(message);
      this.status = status;
    }
  },
  api: {
    updateWorkspace: mockUpdateWorkspace,
    getGitHubConnectURL: mockGetGitHubConnectURL,
    testRepoBinding: mockTestRepoBinding,
    pinRepoBinding: mockPinRepoBinding,
  },
}));

vi.mock("@multica/core/auth", () => {
  const useAuthStore = Object.assign(
    (selector?: (state: { user: { id: string } }) => unknown) =>
      selector ? selector({ user: { id: "user-1" } }) : { user: { id: "user-1" } },
    { getState: () => ({ user: { id: "user-1" } }) },
  );
  return { useAuthStore };
});

vi.mock("sonner", () => ({
  toast: { success: mockToastSuccess, error: mockToastError },
}));

vi.mock("../../navigation", () => ({
  useNavigation: () => ({
    push: mockNavPush,
    replace: mockNavReplace,
    back: vi.fn(),
    pathname: "/acme/settings",
    searchParams: searchParamsRef.current,
    hash: "",
    getShareableUrl: (path: string) => `https://app.example${path}`,
  }),
}));

import { RepositoriesSection, repositoryIdentity } from "./repositories-section";

const TEST_RESOURCES = {
  en: { common: enCommon, settings: enSettings },
};

function I18nWrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>
  );
}

describe("RepositoriesSection — automatic updates", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers({ shouldAdvanceTime: true });
    workspaceRef.current = {
      id: "workspace-1",
      name: "Test Workspace",
      slug: "test-workspace",
      repos: [{ url: "https://github.com/multica-ai/multica" }],
    };
    membersRef.current = [{ user_id: "user-1", role: "owner" }];
    githubRef.current = {
      installations: [],
      configured: true,
      repository_browse_configured: true,
      can_manage: true,
    };
    githubQueryStateRef.current = {
      isPending: false,
      isFetching: false,
    };
    githubRepositoriesRef.current = [];
    catalogRef.current = {
      links: [],
      bindings: [],
      can_add_workspace: false,
      can_add_personal: false,
    };
    searchParamsRef.current = new URLSearchParams("tab=repositories");
    mockTestRepoBinding.mockResolvedValue({ ok: true });
    mockNavReplace.mockImplementation((path: string) => {
      searchParamsRef.current = new URLSearchParams(path.split("?")[1] ?? "");
    });
    mockUpdateWorkspace.mockImplementation(
      async (_id: string, payload: { repos: { url: string; description?: string }[] }) => ({
        ...workspaceRef.current,
        repos: payload.repos,
      }),
    );
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  function setupUser() {
    return userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
  }

  it("renders persisted repositories as the same shared input controls used for editing", () => {
    render(<RepositoriesSection />, { wrapper: I18nWrapper });

    const inputs = screen.getAllByRole("textbox") as HTMLInputElement[];
    expect(inputs).toHaveLength(2);
    expect(inputs[0]!.value).toBe("https://github.com/multica-ai/multica");
    expect(screen.queryByRole("button", { name: /^Save$/ })).toBeNull();
  });

  it("updates a changed URL automatically on blur", async () => {
    const user = setupUser();
    render(<RepositoriesSection />, { wrapper: I18nWrapper });

    const urlInput = screen.getAllByRole("textbox")[0]!;
    await user.clear(urlInput);
    await user.type(urlInput, "https://github.com/multica-ai/edited");
    await user.tab();

    await waitFor(() => {
      expect(mockUpdateWorkspace).toHaveBeenCalledWith("workspace-1", {
        repos: [{ url: "https://github.com/multica-ai/edited" }],
      });
      expect(mockToastSuccess).toHaveBeenCalledWith("Repositories saved", {
        id: "settings-auto-save",
      });
    });
  });

  it("debounces updates while the user is still typing", async () => {
    const user = setupUser();
    render(<RepositoriesSection />, { wrapper: I18nWrapper });

    const urlInput = screen.getAllByRole("textbox")[0]!;
    await user.type(urlInput, "-next");
    expect(mockUpdateWorkspace).not.toHaveBeenCalled();

    vi.advanceTimersByTime(650);
    await waitFor(() => expect(mockUpdateWorkspace).toHaveBeenCalledTimes(1));
  });

  it("does not persist a new row until its URL is non-empty", async () => {
    const user = setupUser();
    render(<RepositoriesSection />, { wrapper: I18nWrapper });

    await user.click(screen.getByRole("button", { name: /Add repository/ }));
    expect(screen.getAllByRole("textbox")).toHaveLength(4);
    vi.advanceTimersByTime(1000);
    expect(mockUpdateWorkspace).not.toHaveBeenCalled();

    const newUrlInput = screen.getAllByRole("textbox")[2]!;
    await user.type(newUrlInput, "git@github.com:multica-ai/second.git");
    await user.tab();

    await waitFor(() => {
      expect(mockUpdateWorkspace).toHaveBeenCalledWith("workspace-1", {
        repos: [
          { url: "https://github.com/multica-ai/multica" },
          { url: "git@github.com:multica-ai/second.git" },
        ],
      });
    });
  });

  it("persists deletion immediately without a separate save action", async () => {
    const user = setupUser();
    render(<RepositoriesSection />, { wrapper: I18nWrapper });

    await user.click(screen.getByRole("button", { name: "Delete repository" }));
    expect(mockUpdateWorkspace).not.toHaveBeenCalled();
    await user.click(
      screen.getByRole("button", { name: "Delete repository" }),
    );

    await waitFor(() => {
      expect(mockUpdateWorkspace).toHaveBeenCalledWith("workspace-1", { repos: [] });
    });
    expect(screen.getByText("No repositories yet.")).toBeTruthy();
  });

  it("accepts scp-like repository shorthand", async () => {
    const user = setupUser();
    render(<RepositoriesSection />, { wrapper: I18nWrapper });

    const urlInput = screen.getAllByRole("textbox")[0] as HTMLInputElement;
    await user.clear(urlInput);
    await user.type(urlInput, "git@github.com:multica-ai/multica.git");
    expect(urlInput.type).toBe("text");
    expect(urlInput.validity.valid).toBe(true);
    await user.tab();

    await waitFor(() => {
      expect(mockUpdateWorkspace).toHaveBeenCalledWith("workspace-1", {
        repos: [{ url: "git@github.com:multica-ai/multica.git" }],
      });
    });
  });

  it("includes the description in the automatic update payload", async () => {
    workspaceRef.current = {
      ...workspaceRef.current,
      repos: [{ url: "https://github.com/multica-ai/multica", description: "Main app" }],
    };
    const user = setupUser();
    render(<RepositoriesSection />, { wrapper: I18nWrapper });

    const descriptionInput = screen.getAllByRole("textbox")[1] as HTMLInputElement;
    expect(descriptionInput.value).toBe("Main app");
    await user.clear(descriptionInput);
    await user.type(descriptionInput, "Updated description");
    await user.tab();

    await waitFor(() => {
      expect(mockUpdateWorkspace).toHaveBeenCalledWith("workspace-1", {
        repos: [
          {
            url: "https://github.com/multica-ai/multica",
            description: "Updated description",
          },
        ],
      });
    });
  });

  it("keeps repository controls read-only for members", () => {
    membersRef.current = [{ user_id: "user-1", role: "member" }];
    render(<RepositoriesSection />, { wrapper: I18nWrapper });

    expect(screen.getAllByRole("textbox").every((input) => input.hasAttribute("disabled"))).toBe(true);
    expect(screen.queryByRole("button", { name: /Add repository/ })).toBeNull();
  });

  it("starts GitHub connection with the signed repository return target", async () => {
    const user = setupUser();
    mockGetGitHubConnectURL.mockResolvedValue({
      configured: true,
      url: "https://github.com/apps/multica/installations/new",
    });
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    render(<RepositoriesSection />, { wrapper: I18nWrapper });

    await user.click(screen.getByRole("button", { name: "Connect GitHub" }));

    await waitFor(() => {
      expect(mockGetGitHubConnectURL).toHaveBeenCalledWith(
        "workspace-1",
        "repositories",
      );
      expect(open).toHaveBeenCalledWith(
        "https://github.com/apps/multica/installations/new",
        "_blank",
        "noopener",
      );
    });
    open.mockRestore();
  });

  it("keeps GitHub import disabled when repository browsing is unavailable", () => {
    githubRef.current = {
      installations: [],
      configured: true,
      repository_browse_configured: false,
      can_manage: true,
    };
    render(<RepositoriesSection />, { wrapper: I18nWrapper });

    const button = screen.getByRole("button", { name: "Connect GitHub" });
    expect(
      button.hasAttribute("disabled") ||
        button.getAttribute("aria-disabled") === "true",
    ).toBe(true);
    expect(button.getAttribute("title")).toContain("GITHUB_APP_ID");
  });

  it("sends the owner to Connections when the server has no GitHub App", async () => {
    githubRef.current = {
      installations: [],
      configured: false,
      repository_browse_configured: false,
      can_manage: true,
    };
    const user = setupUser();
    render(<RepositoriesSection />, { wrapper: I18nWrapper });

    expect(screen.getByText(/Create it on the Connections tab/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Create GitHub App" }));
    expect(mockNavPush).toHaveBeenCalledWith(expect.stringContaining("tab=git-connections"));
  });

  it("tells an admin that the owner must create the missing GitHub App", () => {
    membersRef.current = [{ user_id: "user-1", role: "admin" }];
    githubRef.current = {
      installations: [],
      configured: false,
      repository_browse_configured: false,
      can_manage: true,
    };
    render(<RepositoriesSection />, { wrapper: I18nWrapper });

    expect(screen.getByRole("button", { name: "Create GitHub App" })).toBeDisabled();
    expect(screen.getByText(/Ask a workspace owner/)).toBeInTheDocument();
  });

  it("imports selected GitHub repositories and deduplicates HTTPS against SSH", async () => {
    workspaceRef.current = {
      ...workspaceRef.current,
      repos: [{ url: "git@github.com:multica-ai/multica.git" }],
    };
    githubRef.current = {
      installations: [{ id: "installation-row-1", account_login: "multica-ai" }],
      configured: true,
      repository_browse_configured: true,
      can_manage: true,
    };
    githubRepositoriesRef.current = [
      {
        id: 1,
        full_name: "multica-ai/multica",
        html_url: "https://github.com/multica-ai/multica",
        clone_url: "https://github.com/multica-ai/multica.git",
        description: "Existing repository",
        private: false,
        archived: false,
        default_branch: "main",
      },
      {
        id: 2,
        full_name: "multica-ai/console",
        html_url: "https://github.com/multica-ai/console",
        clone_url: "https://github.com/multica-ai/console.git",
        description: "Console app",
        private: true,
        archived: false,
        default_branch: "main",
      },
    ];
    const user = setupUser();
    render(<RepositoriesSection />, { wrapper: I18nWrapper });

    await user.click(
      screen.getByRole("button", { name: "Choose from GitHub" }),
    );
    const checkboxes = screen.getAllByRole("checkbox");
    expect(checkboxes).toHaveLength(2);
    expect(
      checkboxes[0]!.hasAttribute("disabled") ||
        checkboxes[0]!.getAttribute("aria-disabled") === "true",
    ).toBe(true);

    await user.click(checkboxes[1]!);
    await user.click(screen.getByRole("button", { name: "Add repositories" }));

    await waitFor(() => {
      expect(mockUpdateWorkspace).toHaveBeenCalledWith("workspace-1", {
        repos: [
          { url: "git@github.com:multica-ai/multica.git" },
          {
            url: "https://github.com/multica-ai/console.git",
            description: "Console app",
          },
        ],
      });
    });
  });

  it("preserves repository path casing when comparing clone URLs", () => {
    expect(
      repositoryIdentity("https://GitHub.com/Acme/Repo.git"),
    ).toBe("github.com/Acme/Repo");
    expect(
      repositoryIdentity("git@github.com:acme/repo.git"),
    ).toBe("github.com/acme/repo");
  });

  it("opens the picker after returning from a GitHub connection", async () => {
    githubRef.current = {
      installations: [{ id: "installation-row-1", account_login: "multica-ai" }],
      configured: true,
      repository_browse_configured: true,
      can_manage: true,
    };
    searchParamsRef.current = new URLSearchParams(
      "tab=repositories&github_connected=1",
    );

    render(<RepositoriesSection />, { wrapper: I18nWrapper });

    expect(
      await screen.findByRole("heading", {
        name: "Choose GitHub repositories",
      }),
    ).toBeTruthy();
    expect(mockNavReplace).toHaveBeenCalledWith(
      "/acme/settings?tab=repositories",
    );
  });

  it("clears the GitHub callback query after an empty installation result", async () => {
    searchParamsRef.current = new URLSearchParams(
      "tab=repositories&github_connected=1",
    );

    render(<RepositoriesSection />, { wrapper: I18nWrapper });

    await waitFor(() => {
      expect(mockNavReplace).toHaveBeenCalledWith(
        "/acme/settings?tab=repositories",
      );
    });
    expect(
      screen.queryByRole("heading", {
        name: "Choose GitHub repositories",
      }),
    ).toBeNull();
  });

  it("shows the matched connection and tests that repository", async () => {
    const user = setupUser();
    catalogRef.current = {
      links: [
        {
          id: "link-1",
          kind: "github_token",
          host: "github.com",
          owner: "multica-ai",
          visibility: "workspace",
          health: "ok",
          can_manage: true,
        },
      ],
      bindings: [],
      can_add_workspace: true,
      can_add_personal: false,
    };
    render(<RepositoriesSection />, { wrapper: I18nWrapper });

    expect(screen.getByText("Connected")).toBeTruthy();
    expect(screen.getByRole("combobox", { name: "Connection" })).toHaveValue("");
    await user.click(
      screen.getByRole("button", { name: "Test github.com/multica-ai/multica" }),
    );
    await waitFor(() => {
      expect(mockTestRepoBinding).toHaveBeenCalledWith("workspace-1", {
        repo_url: "https://github.com/multica-ai/multica",
      });
    });
    expect(mockInvalidate).toHaveBeenCalled();
  });

  it("offers connect when the account has no link", async () => {
    const user = setupUser();
    catalogRef.current = {
      links: [],
      bindings: [],
      can_add_workspace: false,
      can_add_personal: true,
    };
    render(<RepositoriesSection />, { wrapper: I18nWrapper });

    expect(screen.getByText("Not connected")).toBeTruthy();
    await user.click(
      screen.getByRole("button", { name: "Connect github.com/multica-ai/multica" }),
    );
    expect(mockNavPush).toHaveBeenCalledWith(
      "/acme/settings?tab=git-connections&connect_scope=github.com%2Fmultica-ai",
    );
  });

  it("pins a repository to one connection", async () => {
    const user = setupUser();
    mockPinRepoBinding.mockResolvedValue({});
    catalogRef.current = {
      links: [
        {
          id: "link-1",
          kind: "github_app",
          host: "github.com",
          owner: "multica-ai",
          visibility: "workspace",
          health: "ok",
          can_manage: true,
        },
      ],
      bindings: [],
      can_add_workspace: true,
      can_add_personal: false,
    };
    render(<RepositoriesSection />, { wrapper: I18nWrapper });

    await user.selectOptions(screen.getByRole("combobox", { name: "Connection" }), "link-1");
    await waitFor(() => {
      expect(mockPinRepoBinding).toHaveBeenCalledWith("workspace-1", {
        repo_url: "https://github.com/multica-ai/multica",
        pinned_link_id: "link-1",
      });
    });
  });
});
