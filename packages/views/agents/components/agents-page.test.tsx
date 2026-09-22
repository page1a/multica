import React from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen } from "@testing-library/react";
import type { Agent, AgentRuntime } from "@multica/core/types";
import type { AgentActivity } from "@multica/core/agents";
import { renderWithI18n } from "../../test/i18n";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import { AgentsPage } from "./agents-page";

// These tests pin the `listReady` render gate (MUL-4511): the Agents list must
// not paint real rows until the auxiliary queries the active sort field /
// filter depends on have landed, or it sorts on placeholder values
// (lastActiveDays null→Infinity, runCount 0) and visibly re-orders when each
// query resolves. The gate waits per need — nothing for name/created,
// run-counts for runs, activity + run-counts for the default lastActive,
// presence when an availability filter is active — and never blocks the empty
// state on those queries.

const mocks = vi.hoisted(() => ({
  agents: [] as Agent[],
  agentsLoading: false,
  runtimes: [] as AgentRuntime[],
  runCounts: [] as Array<{ agent_id: string; run_count: number }>,
  runCountsPending: false,
  activity: {
    byAgent: new Map<string, AgentActivity>(),
    loading: false,
  },
  presence: {
    byAgent: new Map<string, unknown>(),
    loading: false,
  },
  viewState: {
    scope: "all",
    grouping: "none" as "none" | "squad" | "specialization",
    sortField: "lastActive" as string,
    sortDirection: "desc" as string,
    hiddenColumns: ["model", "created"] as string[],
    filters: {
      availability: [] as string[],
      runtimes: [] as string[],
      owners: [] as string[],
      models: [] as string[],
      access: [] as string[],
      squads: [] as string[],
    },
    setScope: vi.fn(),
    setGrouping: vi.fn(),
    toggleSort: vi.fn(),
    setSortField: vi.fn(),
    setSortDirection: vi.fn(),
    toggleColumn: vi.fn(),
    toggleFilter: vi.fn(),
    clearFilters: vi.fn(),
  },
  squads: [] as Array<{
    id: string;
    name: string;
    archived_at: string | null;
    members: Array<{ member_type: string; member_id: string; role: string }>;
  }>,
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (options: { queryKey?: readonly unknown[] }) => {
    const key = options.queryKey?.[0];
    if (key === "agents") {
      return {
        data: mocks.agents,
        isLoading: mocks.agentsLoading,
        error: null,
        refetch: vi.fn(),
      };
    }
    if (key === "agent-run-counts") {
      return { data: mocks.runCounts, isPending: mocks.runCountsPending };
    }
    if (key === "squads") {
      return { data: mocks.squads, isLoading: false, isPending: false };
    }
    if (key === "runtimes") {
      return { data: mocks.runtimes, isLoading: false, isPending: false };
    }
    return { data: [], isLoading: false, isPending: false };
  },
  useQueries: ({ queries }: { queries: unknown[] }) =>
    queries.map(() => ({
      data: undefined,
      isPending: false,
      isSuccess: false,
      isError: false,
    })),
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
}));

// The list virtualizes; render every row so DOM order reflects sort order.
vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getVirtualItems: () =>
      Array.from({ length: count }, (_, index) => ({
        index,
        key: index,
        start: index * 64,
        end: (index + 1) * 64,
        size: 64,
      })),
    getTotalSize: () => count * 64,
  }),
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

vi.mock("@multica/core/agents", () => ({
  isAgentRuntimeBound: (agent: { runtime_id: string; runtime_bound?: boolean }) =>
    agent.runtime_bound !== false && agent.runtime_id.length > 0,
  isAgentWorkEnabled: (agent: { work_enabled?: boolean }) =>
    agent.work_enabled !== false,
  agentRunCounts30dOptions: () => ({ queryKey: ["agent-run-counts"] }),
  useWorkspaceActivityMap: () => mocks.activity,
  useWorkspacePresenceMap: () => mocks.presence,
  VISIBILITY_TOOLTIP: { private: "Private", workspace: "Workspace" },
  effectiveAccessScope: (pm: unknown, it: unknown) => {
    if (pm !== "public_to") return "owner-only";
    if ((Array.isArray(it) ? it : []).some((t) => (t as {target_type?: string})?.target_type === "workspace")) return "workspace";
    return "specific-people";
  },
  ALL_ACCESS_SCOPES: ["workspace", "specific-people", "owner-only"],
}));

vi.mock("@multica/core/agents/stores", () => ({
  useAgentsViewStore: (selector: (state: unknown) => unknown) =>
    selector(mocks.viewState),
  AGENT_DEFAULT_HIDDEN_COLUMNS: ["model", "created"],
  AGENT_SCOPES: ["mine", "all", "archived"],
  AGENT_GROUPINGS: ["none", "squad", "specialization"],
}));

vi.mock("@multica/core/api", () => ({
  api: { archiveAgent: vi.fn(), restoreAgent: vi.fn() },
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: unknown) => unknown) =>
    selector({ user: { id: "user-1" } }),
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    newAgent: () => "/test-workspace/agents/new",
    newAgentManual: () => "/test-workspace/agents/new/manual",
    agentDetail: (id: string) => `/test-workspace/agents/${id}`,
    squadDetail: (id: string) => `/test-workspace/squads/${id}`,
  }),
}));

vi.mock("@multica/core/workspace/queries", () => ({
  agentListOptions: () => ({ queryKey: ["agents"] }),
  memberListOptions: () => ({ queryKey: ["members"] }),
  squadListOptions: () => ({ queryKey: ["squads"] }),
  squadMembersOptions: (_wsId: string, squadId: string) => ({
    queryKey: ["squad-members", squadId],
  }),
  workspaceKeys: { agents: (wsId: string) => ["agents", wsId] },
}));

vi.mock("@multica/core/runtimes", () => ({
  runtimeListOptions: () => ({ queryKey: ["runtimes"] }),
  runtimeDisplayLabel: (runtime: { name: string }) => runtime.name,
}));

// View-layer children with heavy / portal deps — stub to keep the test focused
// on the gate, not on avatars, row menus, the toolbar, or tooltip portals.
vi.mock("../../common/actor-avatar", () => ({ ActorAvatar: () => null }));
vi.mock("./agent-row-actions", () => ({ AgentRowActions: () => null }));
vi.mock("./agent-list-toolbar", () => ({
  AgentListToolbar: (props: {
    grouping: string;
    onGroupingChange: (
      grouping: "none" | "squad" | "specialization",
    ) => void;
    onToggleFilter: (key: string, value: string) => void;
  }) => (
    <div data-testid="agent-list-toolbar">
      <button
        type="button"
        onClick={() => props.onGroupingChange("squad")}
      >
        Group by squad
      </button>
      <button
        type="button"
        onClick={() => props.onGroupingChange("specialization")}
      >
        Group by base role
      </button>
      <button
        type="button"
        onClick={() => props.onToggleFilter("squads", "sq-alpha")}
      >
        Filter Alpha Squad
      </button>
      <button
        type="button"
        onClick={() => props.onToggleFilter("squads", "__none__")}
      >
        Filter no squad
      </button>
    </div>
  ),
  countActiveFilterDimensions: (filters: {
    availability: string[];
    runtimes: string[];
    owners: string[];
    models: string[];
    access: string[];
    squads: string[];
  }) =>
    [
      filters.availability,
      filters.runtimes,
      filters.owners,
      filters.models,
      filters.access,
      filters.squads,
    ].filter((dim) => dim.length > 0).length,
}));
vi.mock("../presence", () => ({ availabilityConfig: {} }));
vi.mock("@multica/ui/components/ui/skeleton", () => ({
  Skeleton: (props: Record<string, unknown>) => (
    <div data-testid="skeleton" {...props} />
  ),
}));
vi.mock("@multica/ui/components/ui/tooltip", () => ({
  Tooltip: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  TooltipTrigger: ({ render }: { render: React.ReactNode }) => <>{render}</>,
  TooltipContent: ({ children }: { children: React.ReactNode }) => (
    <div role="tooltip">{children}</div>
  ),
}));

const BASE_AGENT: Agent = {
  id: "agent-base",
  workspace_id: "workspace-1",
  runtime_id: "runtime-1",
  name: "Base Agent",
  description: "",
  instructions: "",
  avatar_url: null,
  runtime_mode: "cloud",
  runtime_config: {},
  custom_args: [],
  visibility: "workspace",
  permission_mode: "private",
  invocation_targets: [],
  status: "idle",
  max_concurrent_tasks: 1,
  model: "claude",
  owner_id: "user-1",
  skills: [],
  created_at: "2026-06-01T00:00:00Z",
  updated_at: "2026-06-01T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

function makeAgent(over: Partial<Agent>): Agent {
  return { ...BASE_AGENT, ...over };
}

// Build a 30-bucket activity series whose most-recent bucket with runs is
// `daysAgo` days back — `lastActiveDaysAgo` reads exactly this.
function activityLastActive(daysAgo: number): AgentActivity {
  const buckets = Array.from({ length: 30 }, () => ({ total: 0, failed: 0, completed: 0, cancelled: 0 }));
  buckets[29 - daysAgo] = { total: 1, failed: 0, completed: 1, cancelled: 0 };
  return { buckets, daysSinceCreated: 30 };
}

const ALPHA = makeAgent({ id: "a-alpha", name: "Alpha Agent" });
const BETA = makeAgent({ id: "a-beta", name: "Beta Agent" });

function makeAdapter(
  overrides: Partial<NavigationAdapter> = {},
): NavigationAdapter {
  return {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/test-workspace/agents",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (p) => p,
    ...overrides,
  };
}

function renderPage() {
  renderWithI18n(
    <NavigationProvider value={makeAdapter()}>
      <AgentsPage />
    </NavigationProvider>,
  );
}

/** Beta before Alpha in document order? */
function betaPrecedesAlpha(): boolean {
  const alpha = screen.getByText("Alpha Agent");
  const beta = screen.getByText("Beta Agent");
  return Boolean(
    beta.compareDocumentPosition(alpha) & Node.DOCUMENT_POSITION_FOLLOWING,
  );
}

beforeEach(() => {
  mocks.agents = [ALPHA, BETA];
  mocks.agentsLoading = false;
  mocks.runtimes = [];
  mocks.runCounts = [];
  mocks.runCountsPending = false;
  mocks.activity = { byAgent: new Map(), loading: false };
  mocks.presence = { byAgent: new Map(), loading: false };
  mocks.viewState.scope = "all";
  mocks.viewState.grouping = "none";
  mocks.viewState.sortField = "lastActive";
  mocks.viewState.sortDirection = "desc";
  mocks.viewState.hiddenColumns = ["model", "created"];
  mocks.viewState.filters = {
    availability: [],
    runtimes: [],
    owners: [],
    models: [],
    access: [],
    squads: [],
  };
  mocks.viewState.setGrouping = vi.fn();
  mocks.viewState.toggleFilter = vi.fn();
  mocks.squads = [];
});

describe("AgentsPage listReady gate", () => {
  it("shows only a skeleton (no real rows) while lastActive deps are pending", () => {
    // Default lastActive sort depends on activity + run-counts.
    mocks.activity = { byAgent: new Map(), loading: true };
    mocks.runCountsPending = true;

    renderPage();

    expect(screen.queryByText("Alpha Agent")).not.toBeInTheDocument();
    expect(screen.queryByText("Beta Agent")).not.toBeInTheDocument();
    expect(screen.getAllByTestId("skeleton").length).toBeGreaterThan(0);
  });

  it("renders rows in the resolved lastActive order once deps land", () => {
    // Alpha active 5d ago, Beta active today → lastActive desc puts Beta first,
    // the opposite of the name-order fallback the ungated list would show.
    mocks.activity = {
      byAgent: new Map<string, AgentActivity>([
        [ALPHA.id, activityLastActive(5)],
        [BETA.id, activityLastActive(0)],
      ]),
      loading: false,
    };
    mocks.runCounts = [
      { agent_id: ALPHA.id, run_count: 0 },
      { agent_id: BETA.id, run_count: 0 },
    ];
    mocks.runCountsPending = false;

    renderPage();

    expect(screen.getByText("Alpha Agent")).toBeInTheDocument();
    expect(screen.getByText("Beta Agent")).toBeInTheDocument();
    expect(betaPrecedesAlpha()).toBe(true);
  });

  it("renders rows immediately for name sort without waiting on activity/run-counts", () => {
    mocks.viewState.sortField = "name";
    mocks.viewState.sortDirection = "asc";
    // Auxiliary queries are still in flight — name sort must not wait on them.
    mocks.activity = { byAgent: new Map(), loading: true };
    mocks.runCountsPending = true;
    mocks.presence = { byAgent: new Map(), loading: true };

    renderPage();

    expect(screen.getByText("Alpha Agent")).toBeInTheDocument();
    expect(screen.getByText("Beta Agent")).toBeInTheDocument();
    // name asc → Alpha before Beta.
    expect(betaPrecedesAlpha()).toBe(false);
  });

  it("shows a skeleton (not a false empty/false result) while an availability filter waits on presence", () => {
    // Availability filter needs presence; sort by name so ONLY presence gates.
    // Ungated, presence-null rows would all be filtered out → a false "no
    // matches" state. Gated, we hold on a skeleton instead.
    mocks.viewState.sortField = "name";
    mocks.viewState.filters.availability = ["online"];
    mocks.presence = { byAgent: new Map(), loading: true };

    renderPage();

    expect(screen.queryByText("Alpha Agent")).not.toBeInTheDocument();
    expect(screen.queryByText("Beta Agent")).not.toBeInTheDocument();
    expect(screen.getAllByTestId("skeleton").length).toBeGreaterThan(0);
  });

  it("shows the empty state without blocking on auxiliary queries when there are no agents", () => {
    mocks.agents = [];
    // All auxiliary queries pending — the empty state must not wait on them.
    mocks.activity = { byAgent: new Map(), loading: true };
    mocks.runCountsPending = true;
    mocks.presence = { byAgent: new Map(), loading: true };

    renderPage();

    expect(screen.getByText("No agents yet")).toBeInTheDocument();
    expect(screen.queryByTestId("skeleton")).not.toBeInTheDocument();
  });
});

const GAMMA = makeAgent({ id: "a-gamma", name: "Gamma Agent" });

function squadFixture(
  id: string,
  name: string,
  agentIds: string[],
) {
  return {
    id,
    name,
    archived_at: null,
    members: agentIds.map((member_id) => ({
      member_type: "agent",
      member_id,
      role: "member",
    })),
  };
}

describe("AgentsPage squad filter and grouping", () => {
  beforeEach(() => {
    mocks.viewState.sortField = "name";
    mocks.viewState.sortDirection = "asc";
    mocks.agents = [ALPHA, BETA, GAMMA];
    mocks.squads = [
      squadFixture("sq-alpha", "Alpha Squad", [ALPHA.id, GAMMA.id]),
      squadFixture("sq-beta", "Beta Squad", [GAMMA.id]),
    ];
  });

  it("filters to a selected squad", () => {
    mocks.viewState.filters.squads = ["sq-alpha"];
    renderPage();
    expect(screen.getByText("Alpha Agent")).toBeInTheDocument();
    expect(screen.getByText("Gamma Agent")).toBeInTheDocument();
    expect(screen.queryByText("Beta Agent")).not.toBeInTheDocument();
  });

  it("filters to agents with no squad", () => {
    mocks.viewState.filters.squads = ["__none__"];
    renderPage();
    expect(screen.getByText("Beta Agent")).toBeInTheDocument();
    expect(screen.queryByText("Alpha Agent")).not.toBeInTheDocument();
    expect(screen.queryByText("Gamma Agent")).not.toBeInTheDocument();
  });

  it("wires grouping and squad filter controls to the view store", () => {
    renderPage();
    fireEvent.click(screen.getByRole("button", { name: "Group by squad" }));
    expect(mocks.viewState.setGrouping).toHaveBeenCalledWith("squad");
    fireEvent.click(screen.getByRole("button", { name: "Filter Alpha Squad" }));
    expect(mocks.viewState.toggleFilter).toHaveBeenCalledWith(
      "squads",
      "sq-alpha",
    );
    fireEvent.click(screen.getByRole("button", { name: "Filter no squad" }));
    expect(mocks.viewState.toggleFilter).toHaveBeenCalledWith(
      "squads",
      "__none__",
    );
  });

  it("groups by squad with sticky headers, duplicating multi-squad agents", () => {
    mocks.viewState.grouping = "squad";
    renderPage();

    const headers = screen.getAllByTestId("agent-squad-group");
    expect(headers.map((el) => el.getAttribute("data-squad-id"))).toEqual([
      "sq-alpha",
      "sq-beta",
      "__none__",
    ]);
    expect(screen.getByRole("link", { name: "Alpha Squad" })).toHaveAttribute(
      "href",
      "/test-workspace/squads/sq-alpha",
    );
    expect(screen.getByRole("link", { name: "Beta Squad" })).toHaveAttribute(
      "href",
      "/test-workspace/squads/sq-beta",
    );
    expect(screen.getByText("No squad")).toBeInTheDocument();

    expect(screen.getAllByText("Gamma Agent")).toHaveLength(2);
    expect(screen.getByText("Alpha Agent")).toBeInTheDocument();
    expect(screen.getByText("Beta Agent")).toBeInTheDocument();
  });
});

// DENE-304: the nested view is what makes a specialisation visible as a
// specialisation. These pin the wiring (switch → grouping, fold control,
// derive entry destination); the grouping/pairing matrix itself is the
// canonical `.test.ts` next to `agents-page-specializations.ts`.
describe("AgentsPage base-role nesting", () => {
  const BASE_ROLE = makeAgent({
    id: "a-base",
    name: "Base Role",
    child_count: 1,
  });
  const VARIANT = makeAgent({
    id: "a-variant",
    name: "Variant Agent",
    parent_agent_id: "a-base",
    parent_agent_name: "Base Role",
  });

  beforeEach(() => {
    mocks.viewState.sortField = "name";
    mocks.viewState.sortDirection = "asc";
  });

  it("renders a specialisation nested under its base role", () => {
    mocks.agents = [BASE_ROLE, VARIANT];
    mocks.viewState.grouping = "specialization";

    renderPage();

    expect(screen.getByText("Base Role")).toBeInTheDocument();
    expect(screen.getByText("Variant Agent")).toBeInTheDocument();
    // The relationship is labelled on both ends: a count chip on the base
    // role, and a tag + origin line on the child.
    expect(screen.getByTestId("agents-specialization-count")).toHaveTextContent(
      "1 specialization",
    );
    expect(screen.getByTestId("agents-specialization-chip")).toHaveTextContent(
      "Specialization",
    );
    expect(screen.getByText("from Base Role")).toBeInTheDocument();
    // Fold control is available because the base role has a specialisation.
    expect(
      screen.getByTestId("agents-specialization-toggle"),
    ).toHaveAttribute("aria-expanded", "true");
  });

  it("folds a group away and back without leaving the nested view", () => {
    mocks.agents = [BASE_ROLE, VARIANT];
    mocks.viewState.grouping = "specialization";

    renderPage();

    fireEvent.click(screen.getByTestId("agents-specialization-toggle"));
    expect(screen.queryByText("Variant Agent")).not.toBeInTheDocument();
    // The base row stays, and its count still says what is hidden.
    expect(screen.getByText("Base Role")).toBeInTheDocument();
    expect(screen.getByTestId("agents-specialization-count")).toHaveTextContent(
      "1 specialization",
    );

    fireEvent.click(screen.getByTestId("agents-specialization-toggle"));
    expect(screen.getByText("Variant Agent")).toBeInTheDocument();
  });

  it("turns the nested view on from the toolbar switch and persists it", () => {
    mocks.agents = [BASE_ROLE, VARIANT];
    mocks.viewState.grouping = "none";
    const setGrouping = vi.fn();
    mocks.viewState.setGrouping = setGrouping;

    renderPage();

    fireEvent.click(screen.getByText("Group by base role"));
    expect(setGrouping).toHaveBeenCalledWith("specialization");
  });

  it("stays flat when the persisted grouping is one this build does not know", () => {
    mocks.agents = [BASE_ROLE, VARIANT];
    // A value written by a newer build (or a corrupt persisted payload) must
    // not produce an empty list.
    mocks.viewState.grouping = "bogus" as "none";

    renderPage();

    expect(screen.getByText("Base Role")).toBeInTheDocument();
    expect(screen.getByText("Variant Agent")).toBeInTheDocument();
    // Flat means flat: no fold control and no derive entry, even though the
    // relationship is still labelled on the row itself.
    expect(
      screen.queryByTestId("agents-specialization-toggle"),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByTestId("agents-derive-specialization"),
    ).not.toBeInTheDocument();
    // The indent belongs to the nested layout, not to the agent.
    expect(
      screen.queryByTestId("agents-specialization-indent"),
    ).not.toBeInTheDocument();
    expect(screen.getByTestId("agents-specialization-chip")).toBeInTheDocument();
  });

  it("gives a flat-fallback specialisation no fold control and no indent", () => {
    // Only the child is visible (its base role is out of scope), so it renders
    // as its own row. It can never hold children, and there is no parent row
    // above it — a chevron or an indent elbow would both point at nothing.
    mocks.agents = [VARIANT];
    mocks.viewState.grouping = "specialization";

    renderPage();

    expect(screen.getByText("Variant Agent")).toBeInTheDocument();
    expect(
      screen.queryByTestId("agents-specialization-toggle"),
    ).not.toBeInTheDocument();
    expect(screen.getByTestId("agents-specialization-chip")).toBeInTheDocument();
    expect(
      screen.queryByTestId("agents-specialization-indent"),
    ).not.toBeInTheDocument();
  });

  it("derives a specialisation from the base role via the create flow", () => {
    mocks.agents = [BASE_ROLE, VARIANT];
    mocks.viewState.grouping = "specialization";
    const push = vi.fn();
    renderWithI18n(
      <NavigationProvider value={makeAdapter({ push })}>
        <AgentsPage />
      </NavigationProvider>,
    );

    fireEvent.click(screen.getByTestId("agents-derive-specialization"));
    expect(push).toHaveBeenCalledWith(
      "/test-workspace/agents/new/manual?parent=a-base",
    );
  });

  // DENE-384: the empty derive entry under every base role and the fold
  // control that folds nothing turned one agent into two rows and read as a
  // broken list.
  it("adds no fold control or derive entry to a base role with no specialisations", () => {
    mocks.agents = [makeAgent({ id: "a-lonely", name: "Lonely Role" })];
    mocks.viewState.grouping = "specialization";

    renderPage();

    expect(screen.getByText("Lonely Role")).toBeInTheDocument();
    expect(
      screen.queryByTestId("agents-specialization-toggle"),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByTestId("agents-derive-specialization"),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByTestId("agents-specialization-count"),
    ).not.toBeInTheDocument();
  });
});

// DENE-506: for a specialisation the runtime cell has to answer a second
// question the runtime's name cannot — is that value the base role's, or this
// agent's own choice? Without it a row that will silently follow the base role
// reads exactly like one that will not. The state matrix lives in
// `specialization.test.ts`; this pins that the list renders it.
describe("AgentsPage runtime inheritance tag", () => {
  const RUNTIME = {
    id: "runtime-1",
    workspace_id: "workspace-1",
    daemon_id: "daemon-1",
    name: "Local Codex",
    runtime_mode: "local",
    provider: "codex",
    launch_header: "",
    status: "online",
    device_info: "Mac",
    metadata: {},
    owner_id: "user-1",
    visibility: "private",
    last_seen_at: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  } satisfies AgentRuntime;

  const BASE_ROLE = makeAgent({
    id: "a-base",
    name: "Base Role",
    child_count: 3,
    runtime_id: RUNTIME.id,
  });
  const follows = makeAgent({
    id: "a-follows",
    name: "Following Variant",
    parent_agent_id: "a-base",
    parent_agent_name: "Base Role",
    runtime_id: RUNTIME.id,
    runtime_inherited: true,
  });
  const owns = makeAgent({
    id: "a-owns",
    name: "Independent Variant",
    parent_agent_id: "a-base",
    parent_agent_name: "Base Role",
    runtime_id: RUNTIME.id,
    runtime_inherited: false,
  });
  const legacy = makeAgent({
    id: "a-legacy",
    name: "Legacy Variant",
    parent_agent_id: "a-base",
    parent_agent_name: "Base Role",
    runtime_id: RUNTIME.id,
  });

  beforeEach(() => {
    mocks.viewState.sortField = "name";
    mocks.viewState.sortDirection = "asc";
    mocks.viewState.grouping = "specialization";
    mocks.runtimes = [RUNTIME];
  });

  it("labels each specialisation row with inherited or independent", () => {
    mocks.agents = [BASE_ROLE, follows, owns];

    renderPage();

    const tags = screen.getAllByTestId("agents-runtime-inheritance");
    expect(tags.map((tag) => tag.getAttribute("data-inheritance"))).toEqual([
      "inherited",
      "independent",
    ]);
    expect(tags[0]).toHaveTextContent("Inherited");
    expect(tags[1]).toHaveTextContent("Own runtime");
    // The runtime itself is still shown next to the tag.
    expect(screen.getAllByText("Local Codex")).toHaveLength(3);
  });

  it("leaves a base role untagged — it cannot inherit anything", () => {
    mocks.agents = [BASE_ROLE, follows];

    renderPage();

    expect(screen.getAllByTestId("agents-runtime-inheritance")).toHaveLength(1);
  });

  it("tags nothing on a backend that does not serve the flag", () => {
    // An older backend omits `runtime_inherited` entirely; guessing a state
    // would be worse than saying nothing.
    mocks.agents = [BASE_ROLE, legacy];

    renderPage();

    expect(screen.getByText("Legacy Variant")).toBeInTheDocument();
    expect(
      screen.queryByTestId("agents-runtime-inheritance"),
    ).not.toBeInTheDocument();
  });

  it("keeps the tag in the flat list too", () => {
    mocks.viewState.grouping = "none";
    mocks.agents = [BASE_ROLE, follows];

    renderPage();

    expect(screen.getByTestId("agents-runtime-inheritance")).toHaveTextContent(
      "Inherited",
    );
  });
});
