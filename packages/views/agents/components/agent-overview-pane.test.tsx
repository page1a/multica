// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Agent, AgentRuntime } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enAgents from "../../locales/en/agents.json";
import {
  NavigationProvider,
  type NavigationAdapter,
} from "../../navigation";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

const RAIL_SENTINEL = "rail-sentinel";
const GUTTER_SENTINEL = "gutter-sentinel";
vi.mock("../../layout/page-header", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../layout/page-header")>()),
  PAGE_RAIL: "rail-sentinel",
  PAGE_GUTTER: "gutter-sentinel",
}));

// AgentOverviewPane pulls in ActorIssuesPanel which in turn touches the api
// layer. The test only cares about which top-of-pane tab buttons render,
// not what each tab does, so we stub the heavy children.
vi.mock("./tabs/activity-tab", () => ({
  ActivityTab: () => <div>activity-tab</div>,
  AgentPerformanceSummary: () => <div>performance-summary</div>,
}));
vi.mock("./agent-overview-summary", () => ({
  AgentOverviewSummary: () => <div>agent-overview-summary</div>,
}));
vi.mock("./agent-access-settings", () => ({
  AgentAccessSettings: () => <div>agent-access-settings</div>,
}));
vi.mock("./tabs/instructions-tab", () => ({
  InstructionsTab: () => <div>instructions-tab</div>,
}));
vi.mock("./tabs/skills-tab", () => ({
  SkillsTab: () => <div>skills-tab</div>,
}));
vi.mock("./tabs/env-tab", () => ({
  EnvTab: () => <div>env-tab</div>,
}));
vi.mock("./tabs/agent-accounts-tab", () => ({
  AgentAccountsTab: () => <div>agent-accounts-tab</div>,
}));
vi.mock("./tabs/custom-args-tab", () => ({
  CustomArgsTab: () => <div>custom-args-tab</div>,
}));
vi.mock("./tabs/mcp-config-tab", () => ({
  McpConfigTab: () => <div>mcp-config-tab</div>,
}));
vi.mock("./tabs/integrations-tab", () => ({
  IntegrationsTab: () => <div>integrations-tab</div>,
}));
vi.mock("../../common/actor-issues-panel", () => ({
  ActorIssuesPanel: () => <div>actor-issues-panel</div>,
}));

// The pane now reads workspace context to decide whether the Integrations
// tab is worth showing (it queries Lark installations to learn whether the
// deployment has the feature configured). Provide a stable workspace id and
// a listing query backed by a ref so each test can flip `configured`.
const larkListingRef = vi.hoisted(() => ({
  current: { installations: [] as unknown[], configured: false },
}));
const slackListingRef = vi.hoisted(() => ({
  current: { installations: [] as unknown[], configured: false },
}));
const telegramListingRef = vi.hoisted(() => ({
  current: { installations: [] as unknown[], configured: false },
}));
vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));
vi.mock("@multica/core/lark", () => ({
  larkInstallationsOptions: () => ({
    queryKey: ["lark", "installations"],
    queryFn: () => Promise.resolve(larkListingRef.current),
  }),
}));
vi.mock("@multica/core/slack", () => ({
  slackInstallationsOptions: () => ({
    queryKey: ["slack", "installations"],
    queryFn: () => Promise.resolve(slackListingRef.current),
  }),
}));
vi.mock("@multica/core/telegram", () => ({
  telegramInstallationsOptions: () => ({
    queryKey: ["telegram", "installations"],
    queryFn: () => Promise.resolve(telegramListingRef.current),
  }),
}));

import { AgentOverviewPane } from "./agent-overview-pane";

const baseAgent: Agent = {
  id: "agent-1",
  workspace_id: "ws-1",
  runtime_id: "runtime-1",
  name: "Agent",
  description: "",
  instructions: "",
  avatar_url: null,
  runtime_mode: "local",
  runtime_config: {},
  custom_args: [],
  visibility: "workspace",
  permission_mode: "public_to",
  invocation_targets: [{ target_type: "workspace", target_id: null }],
  status: "idle",
  max_concurrent_tasks: 1,
  model: "",
  owner_id: "user-1",
  skills: [],
  created_at: "2026-05-28T00:00:00Z",
  updated_at: "2026-05-28T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

function makeRuntime(provider: string, accounts?: unknown[]): AgentRuntime {
  return {
    id: "runtime-1",
    workspace_id: "ws-1",
    daemon_id: null,
    name: "Runtime",
    runtime_mode: "local",
    provider,
    launch_header: "",
    status: "online",
    device_info: "",
    metadata: accounts === undefined ? {} : { agent_accounts: accounts },
    owner_id: null,
    visibility: "private",
    last_seen_at: null,
    created_at: "2026-05-28T00:00:00Z",
    updated_at: "2026-05-28T00:00:00Z",
  };
}

// 2100-01-01, so a spent quota stays spent for the life of this suite.
const FUTURE_RESET_AT = 4_102_444_800;

function account(id: string, overrides: Record<string, unknown> = {}) {
  return {
    cli: "agy",
    account: id,
    home: `/Users/you/.gemini-${id}`,
    base_url: "",
    key_ref: "",
    lever: "custom_args:--gemini_dir",
    signed_in: true,
    quota_reset_at: 0,
    ...overrides,
  };
}

function renderPane(
  runtimes: AgentRuntime[],
  {
    canEdit = true,
    view,
    agent = baseAgent,
    agents,
  }: {
    canEdit?: boolean;
    view?: string;
    agent?: Agent;
    agents?: Agent[];
  } = {},
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const navigation: NavigationAdapter = {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/agents/agent-1",
    searchParams: new URLSearchParams(view ? { view } : {}),
    hash: "",
    getShareableUrl: (path) => path,
  };
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <NavigationProvider value={navigation}>
        <QueryClientProvider client={queryClient}>
          <AgentOverviewPane
            agent={agent}
            agents={agents}
            runtime={runtimes[0] ?? null}
            owner={null}
            runtimes={runtimes}
            members={[]}
            onUpdate={vi.fn().mockResolvedValue(undefined)}
            canEdit={canEdit}
          />
        </QueryClientProvider>
      </NavigationProvider>
    </I18nProvider>,
  );
}

function openCapabilities() {
  fireEvent.click(screen.getByRole("tab", { name: /^Capabilities$/i }));
}

function openSettings() {
  fireEvent.click(screen.getByRole("tab", { name: /^Settings$/i }));
}

beforeEach(() => {
  larkListingRef.current = { installations: [], configured: false };
  slackListingRef.current = { installations: [], configured: false };
  telegramListingRef.current = { installations: [], configured: false };
});

describe("AgentOverviewPane MCP tab visibility", () => {
  it.each([
    ["Claude", "claude"],
    ["Codex", "codex"],
    ["Cursor", "cursor"],
    ["Hermes", "hermes"],
    ["Kimi", "kimi"],
    ["Kiro", "kiro"],
    ["OpenCode", "opencode"],
    ["OpenClaw", "openclaw"],
    ["Oh My Pi", "omp"],
  ])("renders the MCP tab when the agent runs on the %s runtime", (_label, provider) => {
    renderPane([makeRuntime(provider)]);
    openCapabilities();
    expect(screen.getByRole("tab", { name: /^MCP$/i })).toBeInTheDocument();
  });

  it("hides the MCP tab for providers whose backend does not read mcp_config", () => {
    // Saving an MCP config on e.g. Gemini would be a silent no-op at run
    // time — that's the bug this hiding logic is meant to prevent.
    renderPane([makeRuntime("gemini")]);
    openCapabilities();
    expect(
      screen.queryByRole("tab", { name: /^MCP$/i }),
    ).not.toBeInTheDocument();
  });

  it("keeps the MCP tab visible when the runtime row hasn't loaded yet", () => {
    // Empty runtimes[] mimics the brief window between the page mounting and
    // the runtimes query resolving. Hiding the tab would flicker it off and
    // then back on, which reads as a bug.
    renderPane([]);
    openCapabilities();
    expect(screen.getByRole("tab", { name: /^MCP$/i })).toBeInTheDocument();
  });
});

describe("AgentOverviewPane Integrations tab visibility", () => {
  it("shows the Integrations tab once the deployment has Lark configured", async () => {
    larkListingRef.current = { installations: [], configured: true };
    renderPane([makeRuntime("claude")]);
    openCapabilities();
    expect(
      await screen.findByRole("tab", { name: /^Integrations$/i }),
    ).toBeInTheDocument();
  });

  it("shows the Integrations tab when only Slack is configured (Lark off)", async () => {
    // Regression: the tab gate must consider Slack too, not just Lark —
    // a Slack-only deployment was hiding the tab (and its bind entry).
    slackListingRef.current = { installations: [], configured: true };
    renderPane([makeRuntime("claude")]);
    openCapabilities();
    expect(
      await screen.findByRole("tab", { name: /^Integrations$/i }),
    ).toBeInTheDocument();
  });

  it("shows the Integrations tab when only Telegram is configured", async () => {
    telegramListingRef.current = { installations: [], configured: true };
    renderPane([makeRuntime("claude")]);
    openCapabilities();
    expect(
      await screen.findByRole("tab", { name: /^Integrations$/i }),
    ).toBeInTheDocument();
  });

  it("hides the Integrations tab when no channel integration is configured", () => {
    // Default refs are configured:false; the tab must not appear on a
    // deployment without any channel integration, the common case.
    renderPane([makeRuntime("claude")]);
    openCapabilities();
    expect(
      screen.queryByRole("tab", { name: /^Integrations$/i }),
    ).not.toBeInTheDocument();
  });
});

// DENE-492 (plan B): accounts stopped being a settings tab of its own and
// became a section of General, below Execution. The tab must be gone — not
// kept as a second entry point — and the section must carry the permission
// rule the tab used to carry.
describe("AgentOverviewPane Accounts section", () => {
  it("no longer offers Accounts as a settings tab", () => {
    renderPane([makeRuntime("dsh")]);
    openSettings();

    expect(
      screen.queryByRole("tab", { name: /^Accounts$/i }),
    ).not.toBeInTheDocument();
  });

  it("renders the accounts section inside General, after Execution", () => {
    const { container } = renderPane([makeRuntime("dsh")]);
    openSettings();

    // openSettings() lands on the first settings tab, which is General.
    expect(screen.getByRole("tab", { name: /^General$/i })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByText("Accounts")).toBeInTheDocument();
    expect(screen.getByText("agent-accounts-tab")).toBeInTheDocument();

    // Order matters: "which account does it run as" reads below the runtime
    // and model that decide how it runs.
    const headings = [...container.querySelectorAll("h3")].map(
      (node) => node.textContent,
    );
    expect(headings.indexOf("Accounts")).toBeGreaterThan(
      headings.indexOf("Execution"),
    );
  });

  it("still opens General for a stale ?view=accounts link", () => {
    // The id is no longer a view, so the pane falls back rather than rendering
    // an empty settings pane.
    renderPane([makeRuntime("dsh")], { view: "accounts" });
    expect(screen.getByText("activity-tab")).toBeInTheDocument();
    expect(screen.queryByText("agent-accounts-tab")).not.toBeInTheDocument();
  });

  it("hides the accounts section from users who cannot manage the agent", () => {
    // Accounts reads GET /api/agents/{id}/env to tell a bound lever from an
    // unbound one, so it inherits the env endpoint's permission rule: showing
    // it to anyone else guarantees a 403 and an unanswerable summary.
    renderPane([makeRuntime("dsh")], { canEdit: false });
    openSettings();
    expect(screen.queryByText("agent-accounts-tab")).not.toBeInTheDocument();
  });
});

// The settings tab that owns accounts carries a status dot when a quota is
// spent (DENE-468): the rail is the only place a user passes through before
// the accounts surface, so a spent quota that cannot be acted on has to be
// visible from there. Since DENE-492 that tab is General.
describe("AgentOverviewPane accounts quota warning", () => {
  const spent = account("default", { quota_reset_at: FUTURE_RESET_AT });
  const ready = account("account2");

  function flaggedTab() {
    return screen.getByRole("tab", { name: /Quota used up/i });
  }

  it("flags the General tab and explains the dot where it is read", () => {
    renderPane([makeRuntime("antigravity", [spent, ready])]);
    openSettings();

    // Exactly one tab carries it, and its accessible name says why.
    expect(screen.getAllByRole("tab", { name: /Quota used up/i })).toHaveLength(1);
    expect(flaggedTab()).toHaveTextContent(/^General/);

    fireEvent.click(flaggedTab());

    // The explanation is the copy the summary pill already uses, so the two
    // surfaces cannot disagree about when the quota comes back.
    expect(
      screen.getByText(/Quota used up · back/, { selector: "p" }),
    ).toBeInTheDocument();
  });

  it("stays quiet for a viewer who cannot open the accounts section", () => {
    // `canEdit === false` removes the section, so a dot would point at nothing.
    renderPane([makeRuntime("antigravity", [spent, ready])], {
      canEdit: false,
    });
    openSettings();

    expect(
      screen.queryByRole("tab", { name: /Quota used up/i }),
    ).not.toBeInTheDocument();
  });

  it("leaves the tab unmarked while every quota is available", () => {
    renderPane([makeRuntime("antigravity", [ready])]);
    openSettings();

    expect(
      screen.queryByRole("tab", { name: /Quota used up/i }),
    ).not.toBeInTheDocument();
  });

  it("stops flagging a deadline that has already passed", () => {
    // A reset time in the past means the account is usable again; the dot must
    // not outlive the quota it describes.
    renderPane([
      makeRuntime("antigravity", [account("default", { quota_reset_at: 1 }), ready]),
    ]);
    openSettings();

    expect(
      screen.queryByRole("tab", { name: /Quota used up/i }),
    ).not.toBeInTheDocument();
  });

  it("stays quiet when the daemon reported no accounts at all", () => {
    renderPane([makeRuntime("antigravity")]);
    openSettings();

    expect(
      screen.queryByRole("tab", { name: /Quota used up/i }),
    ).not.toBeInTheDocument();
  });
});

describe("AgentOverviewPane Environment tab visibility", () => {
  it("shows the Environment tab to someone who can manage the agent", () => {
    renderPane([makeRuntime("claude")]);
    openSettings();
    expect(
      screen.getByRole("tab", { name: /^Environment$/i }),
    ).toBeInTheDocument();
  });

  it("hides the Environment tab from users who cannot manage the agent", () => {
    // The env endpoints admit the agent owner or a workspace owner/admin
    // (MUL-5438) — the rule `canEdit` already encodes. Anyone else who opens
    // the tab hits a guaranteed 403 on "Reveal & edit".
    renderPane([makeRuntime("claude")], { canEdit: false });
    openSettings();
    expect(
      screen.queryByRole("tab", { name: /^Environment$/i }),
    ).not.toBeInTheDocument();
  });
});

// MUL-7107: the header, the tab bar and every panel share one leading edge.
// The regression these guard against is a centred width cap: `mx-auto` plus a
// `max-w-*` moves an element's edge as the viewport grows, so chrome on a
// centred rail and a panel on the page gutter agreed at 1440px and drifted
// hundreds of pixels apart above it. A cap must be anchored, never centred.
describe("AgentOverviewPane horizontal alignment", () => {
  // Every band on the page has to read the SAME rail: chrome on the rail with
  // panels off it is the original bug, and panels on it with chrome off it is
  // the mirror image. Both were shipped once (MUL-7107).
  //
  // The constants are overridden with sentinels rather than compared against
  // their real values, which are ordinary Tailwind classes a hand-written
  // element could match by accident. Only an element that reads the constant
  // picks a sentinel up.
  const panelFor = (container: HTMLElement) =>
    container.querySelector('[role="tablist"]')?.nextElementSibling
      ?.firstElementChild;

  it("puts the tab bar row on the rail", () => {
    const { container } = renderPane([makeRuntime("claude")]);
    const row = container.querySelector('[role="tablist"] > div');

    expect(row).toHaveClass(RAIL_SENTINEL);
    expect(row).toHaveClass(GUTTER_SENTINEL);
  });

  it("puts the Overview panel on the same rail", () => {
    const { container } = renderPane([makeRuntime("claude")]);

    expect(panelFor(container as HTMLElement)).toHaveClass(RAIL_SENTINEL);
    expect(panelFor(container as HTMLElement)).toHaveClass(GUTTER_SENTINEL);
  });

  it("puts the Work panel on the same rail", () => {
    const { container } = renderPane([makeRuntime("claude")]);
    fireEvent.click(screen.getByRole("tab", { name: /^Work$/i }));

    // Work takes a bare rail: the issues toolbar inside it carries the gutter
    // already, so adding one here would inset it past the tabs.
    expect(panelFor(container as HTMLElement)).toHaveClass(RAIL_SENTINEL);
  });

  it.each([
    ["Capabilities", openCapabilities],
    ["Settings", openSettings],
  ])("puts the %s nav-and-content row on the same rail", (_name, open) => {
    const { container } = renderPane([makeRuntime("claude")]);
    open();

    // Bare rail for the same reason as Work — the nav aside carries the gutter,
    // and that aside, not the form behind it, is what meets the tabs above.
    expect(panelFor(container as HTMLElement)).toHaveClass(RAIL_SENTINEL);
    expect(container.querySelector("aside")).toHaveClass(GUTTER_SENTINEL);
  });
});

describe("AgentOverviewPane execution config inherited from the base role", () => {
  const baseRole: Agent = { ...baseAgent, id: "base-1", name: "Goku" };
  const follower: Agent = {
    ...baseAgent,
    parent_agent_id: "base-1",
    runtime_inherited: true,
  };

  it.each([
    ["env", "env-tab"],
    ["custom_args", "custom-args-tab"],
  ])("renders the %s editor read-only while following", (view, editor) => {
    renderPane([makeRuntime("codex")], {
      view,
      agent: follower,
      agents: [baseRole, follower],
    });
    expect(screen.getByText(/follows the base role “Goku”/)).toBeTruthy();
    expect(screen.getByText(editor).closest("fieldset")?.disabled).toBe(true);
  });

  it("keeps the editor live for a base role owned by someone else", () => {
    renderPane([makeRuntime("codex")], {
      view: "env",
      agent: follower,
      agents: [{ ...baseRole, owner_id: "user-2" }, follower],
    });
    expect(screen.queryByText(/follows the base role/)).toBeNull();
    expect(screen.getByText("env-tab").closest("fieldset")).toBeNull();
  });
});
