import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enSettings from "../../locales/en/settings.json";

const mockUpdateWorkspace = vi.hoisted(() => vi.fn());
const mockInvalidateQueries = vi.hoisted(() => vi.fn());
const mockToastSuccess = vi.hoisted(() => vi.fn());
const workspaceRef = vi.hoisted(() => ({
  current: {
    id: "workspace-1",
    name: "Test Workspace",
    slug: "test-workspace",
    description: "",
    context: "",
    issue_prefix: "TES",
    repos: [] as { url: string }[],
    settings: {} as Record<string, unknown>,
  },
}));
const membersRef = vi.hoisted(() => ({
  current: [
    { user_id: "user-1", role: "owner" as "owner" | "admin" | "member", name: "Ada" },
  ] as { user_id: string; role: "owner" | "admin" | "member"; name?: string }[],
}));
const agentsRef = vi.hoisted(() => ({
  current: [] as Array<Record<string, unknown>>,
}));
const namingRef = vi.hoisted(() => {
  const initial = {
    source: "runtime",
    options: [
      { id: "server_llm", label: "Server model", available: false, reason: "No model key configured" },
      { id: "runtime", label: "Chat agent runtime", available: true, recommended: true },
      { id: "rules", label: "Rules only", available: true },
    ],
    stats: { titled: 0, runtime: 0, rules: 0, failed: 0 },
  } as Record<string, unknown>;
  return { initial, current: initial };
});

vi.mock("@tanstack/react-query", () => ({
  useQuery: (options: { queryKey?: readonly unknown[] }) => {
    if (options?.queryKey?.[0] === "agents") {
      return { data: agentsRef.current, isFetched: true };
    }
    if (options?.queryKey?.[0] === "project-memory-locations") {
      return { data: { locations: [], builtin_sediment_instruction: "Fill the five memory locations." }, isFetched: true };
    }
    if (options?.queryKey?.[2] === "naming") {
      return { data: namingRef.current, isFetched: true };
    }
    return { data: membersRef.current, isFetched: true };
  },
  useQueryClient: () => ({
    setQueryData: vi.fn(),
    getQueryData: vi.fn(() => []),
    invalidateQueries: mockInvalidateQueries,
  }),
}));

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => workspaceRef.current,
  useHasOnboarded: () => true,
  resolvePostAuthDestination: () => "/",
}));

vi.mock("@multica/core/platform", () => ({
  setCurrentWorkspace: vi.fn(),
}));

vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members"], queryFn: vi.fn() }),
  agentListOptions: () => ({ queryKey: ["agents"], queryFn: vi.fn() }),
  workspaceNamingOptions: () => ({ queryKey: ["workspaces", "workspace-1", "naming"], queryFn: vi.fn() }),
  workspaceListOptions: () => ({ queryKey: ["workspaces"], queryFn: vi.fn() }),
  workspaceKeys: {
    list: () => ["workspaces"],
    naming: () => ["workspaces", "workspace-1", "naming"],
  },
}));

vi.mock("@multica/core/projects/queries", () => ({
  projectMemoryLocationsOptions: () => ({ queryKey: ["project-memory-locations"], queryFn: vi.fn() }),
}));

vi.mock("@multica/core/issues/queries", () => ({
  issueKeys: { all: (workspaceId: string) => ["issues", workspaceId] },
}));

vi.mock("@multica/core/workspace/mutations", () => ({
  useLeaveWorkspace: () => ({ mutateAsync: vi.fn() }),
  useDeleteWorkspace: () => ({ mutateAsync: vi.fn() }),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    updateWorkspace: mockUpdateWorkspace,
    updateWorkspaceNaming: vi.fn(async (_id: string, source: string) => ({ ...namingRef.current, source })),
    getBaseUrl: () => "http://127.0.0.1:8080",
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

vi.mock("../../navigation", () => ({
  useNavigation: () => ({ push: vi.fn() }),
}));

vi.mock("./delete-workspace-dialog", () => ({
  DeleteWorkspaceDialog: () => null,
}));

vi.mock("sonner", () => ({
  toast: { success: mockToastSuccess, error: vi.fn() },
}));

import { api } from "@multica/core/api";
import { WorkspaceTab } from "./workspace-tab";

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

describe("WorkspaceTab — automatic updates", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers({ shouldAdvanceTime: true });
    workspaceRef.current = {
      id: "workspace-1",
      name: "Test Workspace",
      slug: "test-workspace",
      description: "",
      context: "",
      issue_prefix: "TES",
      repos: [],
      settings: {},
    };
    membersRef.current = [{ user_id: "user-1", role: "owner", name: "Ada" }];
    namingRef.current = namingRef.initial;
    mockUpdateWorkspace.mockImplementation(
      async (_id: string, payload: Record<string, unknown>) => ({
        ...workspaceRef.current,
        ...payload,
        issue_prefix:
          (payload.issue_prefix as string | undefined) ?? workspaceRef.current.issue_prefix,
      }),
    );
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  function setupUser() {
    return userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
  }

  it("shows the prefix and slug as values, not editable fields", () => {
    render(<WorkspaceTab />, { wrapper: I18nWrapper });

    expect(screen.getByText("TES")).toBeInTheDocument();
    expect(screen.getByText("test-workspace")).toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: "Slug" })).toBeNull();
    expect(screen.queryByRole("button", { name: /^Save$/ })).toBeNull();
  });

  it("auto-saves ordinary workspace fields silently, without invalidating issue caches", async () => {
    const user = setupUser();
    render(<WorkspaceTab />, { wrapper: I18nWrapper });
    const nameInput = screen.getByDisplayValue("Test Workspace");

    await user.clear(nameInput);
    await user.type(nameInput, "Renamed Workspace");
    await user.tab();

    await waitFor(() => {
      expect(mockUpdateWorkspace).toHaveBeenCalledWith("workspace-1", {
        name: "Renamed Workspace",
        description: "",
        context: "",
      });
    });
    // The inline save state reports success; a toast would repeat it.
    expect(mockToastSuccess).not.toHaveBeenCalled();
    expect(mockInvalidateQueries).not.toHaveBeenCalled();
  });

  it("changes the prefix only from its own dialog, after previewing the result", async () => {
    const user = setupUser();
    render(<WorkspaceTab />, { wrapper: I18nWrapper });

    await user.click(screen.getByRole("button", { name: "Change prefix..." }));
    const dialog = await screen.findByRole("dialog", { name: "Change issue prefix" });
    const input = within(dialog).getByRole("textbox", { name: "New prefix" });

    await user.clear(input);
    await user.type(input, "ab-12!cd");
    expect(input).toHaveValue("AB12CD");
    expect(within(dialog).getByText("TES-123")).toBeInTheDocument();
    expect(within(dialog).getByText("AB12CD-123")).toBeInTheDocument();
    expect(mockUpdateWorkspace).not.toHaveBeenCalled();

    await user.click(within(dialog).getByRole("button", { name: "Change to AB12CD" }));

    await waitFor(() => {
      expect(mockUpdateWorkspace).toHaveBeenCalledWith("workspace-1", {
        issue_prefix: "AB12CD",
      });
    });
    expect(mockInvalidateQueries).toHaveBeenCalledWith({
      queryKey: ["issues", "workspace-1"],
    });
    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "Change issue prefix" })).toBeNull();
    });
  });

  it("does not persist a prefix when the dialog is cancelled", async () => {
    const user = setupUser();
    render(<WorkspaceTab />, { wrapper: I18nWrapper });

    await user.click(screen.getByRole("button", { name: "Change prefix..." }));
    const dialog = await screen.findByRole("dialog", { name: "Change issue prefix" });
    await user.type(within(dialog).getByRole("textbox", { name: "New prefix" }), "X");
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));

    expect(mockUpdateWorkspace).not.toHaveBeenCalled();
  });

  it("cannot confirm an empty or unchanged prefix", async () => {
    const user = setupUser();
    render(<WorkspaceTab />, { wrapper: I18nWrapper });

    await user.click(screen.getByRole("button", { name: "Change prefix..." }));
    const dialog = await screen.findByRole("dialog", { name: "Change issue prefix" });
    const input = within(dialog).getByRole("textbox", { name: "New prefix" });
    expect(within(dialog).getByRole("button", { name: "Change to TES" })).toBeDisabled();

    await user.clear(input);
    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(within(dialog).getByRole("button", { name: /^Change to/ })).toBeDisabled();
  });

  it("shows regular members the values read-only, with who to ask", () => {
    membersRef.current = [
      { user_id: "user-1", role: "member" },
      { user_id: "user-2", role: "owner", name: "Grace Hopper" },
    ];
    render(<WorkspaceTab />, { wrapper: I18nWrapper });

    expect(screen.getByRole("note")).toHaveTextContent("Ask Grace Hopper");
    expect(screen.queryByDisplayValue("Test Workspace")).toBeNull();
    expect(screen.getByText("Test Workspace")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Change prefix..." })).toBeNull();
  });

  it("renders the sediment agent selector with default unconfigured state", () => {
    agentsRef.current = [
      {
        id: "agent-1",
        name: "Test Agent",
        status: "online",
        runtime_id: "rt-1",
        work_enabled: true,
      },
    ];
    render(<WorkspaceTab />, { wrapper: I18nWrapper });

    expect(screen.getByText("Memory Sediment Agent")).toBeTruthy();
    expect(screen.getByRole("combobox", { name: "Memory Sediment Agent" })).toBeTruthy();
  });

  it("renders configured sediment agent and warning when offline", () => {
    workspaceRef.current = {
      ...workspaceRef.current,
      settings: {
        memory: {
          sediment_agent: "agent-offline",
        },
      },
    };
    agentsRef.current = [
      {
        id: "agent-offline",
        name: "Offline Agent",
        status: "offline",
        runtime_id: "rt-1",
        work_enabled: true,
      },
    ];
    render(<WorkspaceTab />, { wrapper: I18nWrapper });

    expect(screen.getByText(/Agent is currently offline/i)).toBeTruthy();
  });

  it("shows the built-in sediment prompt and saves a custom one beside the seat", async () => {
    vi.useRealTimers();
    workspaceRef.current = {
      ...workspaceRef.current,
      settings: { memory: { sediment_agent: "agent-1" } },
    };
    const user = userEvent.setup();
    render(<WorkspaceTab />, { wrapper: I18nWrapper });

    expect(screen.getByText("Fill the five memory locations.")).toBeTruthy();
    const field = screen.getByLabelText("Sediment ticket prompt");
    await user.type(field, "Read AGENTS.md first.");
    await user.tab();

    await waitFor(() =>
      expect(mockUpdateWorkspace).toHaveBeenCalledWith("workspace-1", {
        settings: { memory: { sediment_agent: "agent-1", sediment_instruction: "Read AGENTS.md first." } },
      }),
    );
  });

  it("keeps the sediment prompt read-only for regular members", () => {
    membersRef.current = [{ user_id: "user-1", role: "member", name: "Ada" }];
    render(<WorkspaceTab />, { wrapper: I18nWrapper });

    expect((screen.getByLabelText("Sediment ticket prompt") as HTMLTextAreaElement).disabled).toBe(true);
  });

  it("leads the naming card with whether naming works", () => {
    namingRef.current = {
      ...namingRef.current,
      health: "ok",
      stats: { titled: 3, runtime: 2, rules: 1, failed: 0 },
      last: { title: "Multica · naming card", source: "runtime", created_at: new Date().toISOString() },
    };
    render(<WorkspaceTab />, { wrapper: I18nWrapper });

    expect(screen.getByText("Agents are naming new chats")).toBeTruthy();
    expect(screen.getByText(/Latest: “Multica · naming card”/)).toBeTruthy();
    expect(screen.queryByRole("radiogroup")).toBeNull();
  });

  it("says what to do when agents stop naming", () => {
    namingRef.current = { ...namingRef.current, health: "degraded", stats: { titled: 5, runtime: 0, rules: 5, failed: 0 }, last: null };
    render(<WorkspaceTab />, { wrapper: I18nWrapper });

    expect(screen.getByText("Agents haven't named a chat in 24 hours")).toBeTruthy();
    expect(screen.getByText(/5 new chats were named by rules instead/)).toBeTruthy();
  });

  it("keeps the source picker behind Change and blocks the unavailable server model", async () => {
    namingRef.current = { ...namingRef.current, health: "idle", stats: { titled: 0, runtime: 0, rules: 0, failed: 0 }, last: null };
    render(<WorkspaceTab />, { wrapper: I18nWrapper });

    const user = setupUser();
    await user.click(screen.getByRole("button", { name: "Change" }));
    expect(screen.getByRole("radio", { name: "Server model" })).toBeDisabled();
    expect(screen.getByRole("radio", { name: "Chat agent" })).toHaveAttribute("aria-checked", "true");
    expect(screen.getByText(/needs a model key configured/)).toBeTruthy();

    await user.click(screen.getByRole("radio", { name: "Rules only" }));
    await waitFor(() => expect(api.updateWorkspaceNaming).toHaveBeenCalledWith("workspace-1", "rules"));
  });

  it("keeps naming source read-only for regular members with an explanation", () => {
    membersRef.current = [{ user_id: "user-1", role: "member" }];
    render(<WorkspaceTab />, { wrapper: I18nWrapper });

    expect(screen.queryByRole("button", { name: "Change" })).toBeNull();
    expect(screen.getByText("Only owners and admins can change this")).toBeTruthy();
  });
});
