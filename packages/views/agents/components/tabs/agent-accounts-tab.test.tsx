// @vitest-environment jsdom
//
// Component suite for the agent "accounts" tab (DENE-308).
//
// The rules this surface renders — parsing, grouping, status mapping, the four
// page states and the switch plan — have one canonical test layer, in node:
// `agent-accounts-model.test.ts`. This file deliberately does NOT re-run that
// matrix through a mount. It checks the wiring only: which state renders which
// control, that the drawer entry disappears in the three untrusted states, and
// how many writes "save and switch" performs.

import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Agent, RuntimeDevice } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
import jaAgents from "../../../locales/ja/agents.json";
import koAgents from "../../../locales/ko/agents.json";
import zhAgents from "../../../locales/zh-Hans/agents.json";
import {
  NavigationProvider,
  type NavigationAdapter,
} from "../../../navigation";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/paths", () => ({
  paths: { workspace: () => ({ runtimes: () => "/acme/runtimes" }) },
  useWorkspaceSlug: () => "acme",
}));

const getAgentEnv = vi.hoisted(() => vi.fn());
const updateAgentEnv = vi.hoisted(() => vi.fn());
vi.mock("@multica/core/api", () => ({
  api: { getAgentEnv, updateAgentEnv },
}));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

vi.mock("@multica/ui/lib/clipboard", () => ({
  copyText: vi.fn().mockResolvedValue(true),
}));

import { toast } from "sonner";
import { AgentAccountsTab } from "./agent-accounts-tab";

const RUNTIME_HOME = "/Users/you";

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

/** One `agent_accounts` entry exactly as DENE-306 reports it. */
function account(
  cli: string,
  id: string,
  home: string,
  overrides: Record<string, unknown> = {},
) {
  return {
    cli,
    account: id,
    home,
    base_url: "",
    key_ref: "",
    lever: "",
    signed_in: true,
    quota_reset_at: 0,
    ...overrides,
  };
}

const AGY_DEFAULT = account("agy", "default", `${RUNTIME_HOME}/.gemini`, {
  lever: "custom_args:--gemini_dir",
});
const AGY_ACCOUNT2 = account("agy", "account2", `${RUNTIME_HOME}/.gemini-account2`, {
  lever: "custom_args:--gemini_dir",
});
const DSH_DEFAULT = account("dsh", "default", `${RUNTIME_HOME}/.dsh`, {
  lever: "env:DSH_HOME",
});
const DSH_ACCOUNT2 = account("dsh", "account2", `${RUNTIME_HOME}/.dsh-account2`, {
  lever: "env:DSH_HOME",
});
const CODEX_DEFAULT = account("codex", "default", `${RUNTIME_HOME}/.codex`, {
  lever: "",
});
const CODEX_ACCOUNT2 = account("codex", "account2", `${RUNTIME_HOME}/.codex-account2`, {
  lever: "",
});

function runtimeWith(
  provider: string,
  entries: unknown,
  error?: string,
): RuntimeDevice {
  return {
    id: "runtime-1",
    workspace_id: "ws-1",
    daemon_id: "daemon-1",
    name: "Runtime",
    runtime_mode: "local",
    provider,
    launch_header: "",
    status: "online",
    device_info: "",
    metadata: {
      home_dir: RUNTIME_HOME,
      agent_accounts: entries,
      ...(error === undefined ? {} : { agent_accounts_error: error }),
    },
    owner_id: null,
    visibility: "private",
    last_seen_at: null,
    created_at: "2026-05-28T00:00:00Z",
    updated_at: "2026-05-28T00:00:00Z",
  };
}

function makeNavigation(): NavigationAdapter {
  return {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/agents/agent-1",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (path) => path,
  };
}

/**
 * Stands in for the detail page: it applies the update the tab hands to
 * `onSave` to its own agent state, which is what `handleUpdate`'s optimistic
 * cache patch does in production.
 */
function Harness({
  agent,
  device,
  onSave,
}: {
  agent: Agent;
  device?: RuntimeDevice;
  onSave: (updates: Partial<Agent>) => Promise<void>;
}) {
  const [current, setCurrent] = useState(agent);
  return (
    <AgentAccountsTab
      agent={current}
      runtimeDevice={device}
      onSave={async (updates) => {
        await onSave(updates);
        setCurrent((prev) => ({ ...prev, ...updates }));
      }}
    />
  );
}

function renderTab({
  agent = baseAgent,
  device,
  onSave = vi.fn().mockResolvedValue(undefined),
}: {
  agent?: Agent;
  device?: RuntimeDevice;
  onSave?: (updates: Partial<Agent>) => Promise<void>;
} = {}) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const navigation = makeNavigation();
  const result = render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <NavigationProvider value={navigation}>
        <QueryClientProvider client={queryClient}>
          <Harness agent={agent} device={device} onSave={onSave} />
        </QueryClientProvider>
      </NavigationProvider>
    </I18nProvider>,
  );
  return { ...result, onSave, navigation };
}

// A report carrying an env-lever account renders the loading state until the
// env query settles, so the entry point has to be awaited rather than queried
// eagerly.
async function openDrawer() {
  fireEvent.click(
    await screen.findByRole("button", { name: /manage accounts/i }),
  );
}

function selectAccount(name: RegExp) {
  fireEvent.click(screen.getByRole("radio", { name }));
}

function saveAndSwitch() {
  fireEvent.click(screen.getByRole("button", { name: /save and switch/i }));
}

beforeEach(() => {
  vi.clearAllMocks();
  getAgentEnv.mockResolvedValue({ agent_id: "agent-1", custom_env: {} });
  // The env endpoint echoes the map it accepted; the tab caches that answer, so
  // echoing keeps the fake faithful to the server.
  updateAgentEnv.mockImplementation(
    async (_id: string, body: { custom_env: Record<string, string> }) => ({
      agent_id: "agent-1",
      custom_env: body.custom_env,
    }),
  );
});

describe("AgentAccountsTab rest state", () => {
  it("shows the account in effect and lists the others as read-only chips", async () => {
    renderTab({
      agent: {
        ...baseAgent,
        custom_args: ["--gemini_dir", AGY_ACCOUNT2.home],
      },
      device: runtimeWith("antigravity", [AGY_DEFAULT, AGY_ACCOUNT2, DSH_DEFAULT]),
    });

    expect(await screen.findByText("Antigravity · account2")).toBeInTheDocument();
    expect(screen.getByText("In use")).toBeInTheDocument();
    expect(
      screen.getByText(`--gemini_dir=${AGY_ACCOUNT2.home}`),
    ).toBeInTheDocument();

    // Non-current accounts are chips, never controls: clicking one must not
    // switch the account.
    const chip = screen.getByText("DSH · default");
    expect(chip.closest("button")).toBeNull();
    expect(screen.getByText("Antigravity · default")).toBeInTheDocument();
  });

  it("reads the env endpoint only when an account is bound through an env key", async () => {
    // That endpoint is audited server-side, so a machine whose accounts all use
    // the custom_args lever must not pay for it.
    renderTab({
      device: runtimeWith("antigravity", [AGY_DEFAULT, AGY_ACCOUNT2]),
    });

    expect(
      await screen.findByText("Antigravity · default"),
    ).toBeInTheDocument();
    expect(getAgentEnv).not.toHaveBeenCalled();
  });

  it("opens the drawer from the one primary entry point", async () => {
    renderTab({
      device: runtimeWith("antigravity", [AGY_DEFAULT, AGY_ACCOUNT2]),
    });

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await openDrawer();

    const drawer = screen.getByRole("dialog", { name: /manage accounts/i });
    expect(drawer).toBeInTheDocument();
    // This agent has no persisted slot list yet, so it seeds slots 1–3 while
    // the daemon reported only two of those directories: slot 3 is part of the
    // rotation list and shows up as a slot whose directory does not exist yet.
    expect(screen.getByText("1 CLIs · 3 accounts")).toBeInTheDocument();
    // Group header carries the binding lever.
    expect(screen.getByText("--gemini_dir")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /save and switch/i }),
    ).toBeInTheDocument();
  });

  it("describes the binding it read from the env endpoint", async () => {
    getAgentEnv.mockResolvedValue({
      agent_id: "agent-1",
      custom_env: { DSH_HOME: DSH_ACCOUNT2.home, KEEP: "untouched" },
    });
    renderTab({
      device: runtimeWith("dsh", [DSH_DEFAULT, DSH_ACCOUNT2]),
    });

    expect(await screen.findByText("DSH · account2")).toBeInTheDocument();
    expect(
      screen.getByText(`DSH_HOME=${DSH_ACCOUNT2.home}`),
    ).toBeInTheDocument();
  });
});

// The one-click switch (DENE-468). Which situation qualifies is decided by
// `quotaSwitchCandidate` and its matrix is covered in
// `agent-accounts-model.test.ts`; what is checked here is the wiring — that the
// button appears, performs the write through the same path as "save and
// switch", and moves the summary bar.
describe("AgentAccountsTab one-click switch", () => {
  // 2100-01-01, so a spent quota stays spent for the life of this suite.
  const FUTURE_RESET_AT = 4_102_444_800;
  const SPENT = { quota_reset_at: FUTURE_RESET_AT };
  const SIGNED_OUT = { signed_in: false };
  const antigravityDefault = {
    ...baseAgent,
    custom_args: ["--gemini_dir", `${RUNTIME_HOME}/.gemini`],
  };

  function quickSwitchButton() {
    return screen.queryByRole("button", { name: /^switch to /i });
  }

  it("switches to the eligible sibling in one click and rewrites the binding", async () => {
    const { onSave } = renderTab({
      agent: antigravityDefault,
      device: runtimeWith("antigravity", [
        account("agy", "default", `${RUNTIME_HOME}/.gemini`, {
          lever: "custom_args:--gemini_dir",
          ...SPENT,
        }),
        AGY_ACCOUNT2,
      ]),
    });

    expect(await screen.findByText("Quota used up", { exact: false })).toBeInTheDocument();
    const button = screen.getByRole("button", { name: "Switch to account2" });
    fireEvent.click(button);

    // The write is the drawer's own path: one `custom_args` update, no env
    // call, and the summary bar moves to the account that took over.
    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    expect(onSave).toHaveBeenCalledWith({
      custom_args: ["--gemini_dir", AGY_ACCOUNT2.home],
    });
    expect(updateAgentEnv).not.toHaveBeenCalled();
    expect(
      await screen.findByText(`--gemini_dir=${AGY_ACCOUNT2.home}`),
    ).toBeInTheDocument();
    expect(quickSwitchButton()).not.toBeInTheDocument();
  });

  it("writes the env key when the sibling's lever is an environment variable", async () => {
    const { onSave } = renderTab({
      device: runtimeWith("dsh", [
        account("dsh", "default", `${RUNTIME_HOME}/.dsh`, {
          lever: "env:DSH_HOME",
          ...SPENT,
        }),
        DSH_ACCOUNT2,
      ]),
    });

    fireEvent.click(
      await screen.findByRole("button", { name: "Switch to account2" }),
    );

    await waitFor(() => expect(updateAgentEnv).toHaveBeenCalledTimes(1));
    expect(updateAgentEnv).toHaveBeenCalledWith("agent-1", {
      custom_env: { DSH_HOME: DSH_ACCOUNT2.home },
    });
    expect(onSave).not.toHaveBeenCalled();
  });

  it("offers nothing when the only sibling is spent or signed out too", async () => {
    renderTab({
      agent: antigravityDefault,
      device: runtimeWith("antigravity", [
        account("agy", "default", `${RUNTIME_HOME}/.gemini`, {
          lever: "custom_args:--gemini_dir",
          ...SPENT,
        }),
        account("agy", "account2", `${RUNTIME_HOME}/.gemini-account2`, {
          lever: "custom_args:--gemini_dir",
          ...SIGNED_OUT,
        }),
      ]),
    });

    // The state still announces itself; it just has no one-click way out, and
    // "manage" stays the escape hatch.
    expect(await screen.findByText("Quota used up", { exact: false })).toBeInTheDocument();
    expect(quickSwitchButton()).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /manage accounts/i }),
    ).toBeInTheDocument();
  });

  it("offers nothing for a CLI the daemon reports no lever for", async () => {
    // codex and cursor cannot be pointed at another directory at all, so a
    // button here would be dead on click (DENE-305's read-only group rule).
    renderTab({
      agent: { ...baseAgent, model: "", runtime_id: "runtime-1" },
      device: runtimeWith("codex", [
        account("codex", "default", `${RUNTIME_HOME}/.codex`, {
          lever: "",
          ...SPENT,
        }),
        CODEX_ACCOUNT2,
      ]),
    });

    expect(await screen.findByText("Codex · default")).toBeInTheDocument();
    expect(quickSwitchButton()).not.toBeInTheDocument();
  });

  it("keeps the binding untouched while the account in effect has quota left", async () => {
    const { onSave } = renderTab({
      agent: antigravityDefault,
      device: runtimeWith("antigravity", [AGY_DEFAULT, AGY_ACCOUNT2]),
    });

    expect(await screen.findByText("Antigravity · default")).toBeInTheDocument();
    expect(quickSwitchButton()).not.toBeInTheDocument();
    expect(onSave).not.toHaveBeenCalled();
  });
});

describe("AgentAccountsTab untrusted states keep the drawer shut", () => {
  it("renders the empty state with its three entry points and no drawer entry", () => {
    renderTab({ device: runtimeWith("dsh", []) });

    expect(
      screen.getByText("This agent has no accounts yet"),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /create the first dsh account/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /use an agy account/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /i already have a directory/i }),
    ).toBeInTheDocument();
    expect(screen.getByText(/available lever DSH_HOME/)).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /manage accounts/i }),
    ).not.toBeInTheDocument();
    // An empty list is not a list with a candidate in it: the one-click switch
    // needs a sibling it can actually write, so it stays away too.
    expect(
      screen.queryByRole("button", { name: /^switch to /i }),
    ).not.toBeInTheDocument();
  });

  it("renders the loading skeleton until the runtime row arrives", () => {
    renderTab({ device: undefined });

    expect(
      screen.getByText("Reading account directories and sign-in state…"),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /manage accounts/i }),
    ).not.toBeInTheDocument();
  });

  it("falls through to the empty state when the agent has no runtime bound", () => {
    // `agent-detail-page` passes `runtime ?? undefined`, and `runtime` is null
    // for an unbound agent as well as for a row still in flight. Without the
    // agent's own binding signal this screen waited forever for a report no
    // daemon was ever going to send.
    renderTab({ agent: { ...baseAgent, runtime_id: "" }, device: undefined });

    expect(
      screen.queryByText("Reading account directories and sign-in state…"),
    ).not.toBeInTheDocument();
    expect(
      screen.getByText("This agent has no accounts yet"),
    ).toBeInTheDocument();
  });

  it("renders the error state when the env binding cannot be read", async () => {
    // A denied or failed env read leaves the lever unknown. Treating it as "no
    // override" would resolve the CLI's own default directory and name the
    // wrong account as the one in effect, which is the single question this
    // screen exists to answer.
    getAgentEnv.mockRejectedValue(new Error("403 forbidden"));

    renderTab({ device: runtimeWith("dsh", [DSH_DEFAULT, DSH_ACCOUNT2]) });

    await waitFor(() => {
      expect(
        screen.getByText("Can't read local account state"),
      ).toBeInTheDocument();
    });
    expect(screen.getByText("403 forbidden")).toBeInTheDocument();
    expect(screen.queryByText("In use")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /manage accounts/i }),
    ).not.toBeInTheDocument();
  });

  it("renders the error state with the raw reason and no drawer entry", () => {
    renderTab({
      device: runtimeWith(
        "dsh",
        [],
        "dial tcp 127.0.0.1:7433: connect: connection refused",
      ),
    });

    expect(screen.getByText("Can't read local account state")).toBeInTheDocument();
    expect(
      screen.getByText("dial tcp 127.0.0.1:7433: connect: connection refused"),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /^retry$/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /view daemon status/i }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /manage accounts/i }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /^switch to /i }),
    ).not.toBeInTheDocument();
  });
});

describe("AgentAccountsTab drawer", () => {
  it("keeps a CLI with no lever read-only instead of offering a dead switch", async () => {
    renderTab({
      device: runtimeWith("dsh", [DSH_DEFAULT, DSH_ACCOUNT2, CODEX_DEFAULT]),
    });
    await openDrawer();

    // Read-only is said with its reason — and it is a different sentence from
    // the one a switchable-but-manual group gets, so "cannot add" and "cannot
    // switch" are no longer rendered as the same thing (DENE-678).
    expect(
      screen.getByText(/gets its own Codex directory/i),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /add codex account/i }),
    ).not.toBeInTheDocument();
    // Only the two dsh rows are selectable; the codex row is static.
    expect(screen.getAllByRole("radio")).toHaveLength(2);
    expect(
      screen.getByText(`${RUNTIME_HOME}/.codex`).closest("button"),
    ).toBeNull();
  });

  it("writes only the target env key on save and switch", async () => {
    getAgentEnv.mockResolvedValue({
      agent_id: "agent-1",
      custom_env: { KEEP: "untouched" },
    });
    const { onSave } = renderTab({
      device: runtimeWith("dsh", [DSH_DEFAULT, DSH_ACCOUNT2]),
    });

    await screen.findByText("DSH · default");
    await openDrawer();
    selectAccount(/account2/);
    saveAndSwitch();

    // The summary bar reads the new binding; "DSH · account2" alone would also
    // match the read-only chip, so assert on the lever line the bar owns.
    expect(
      await screen.findByText(`DSH_HOME=${DSH_ACCOUNT2.home}`),
    ).toBeInTheDocument();
    expect(updateAgentEnv).toHaveBeenCalledTimes(1);
    // Every other variable is written back untouched; only DSH_HOME moves.
    expect(updateAgentEnv).toHaveBeenCalledWith("agent-1", {
      custom_env: { KEEP: "untouched", DSH_HOME: DSH_ACCOUNT2.home },
    });
    // An env-lever switch never travels through PUT /api/agents/{id}.
    expect(onSave).not.toHaveBeenCalled();
    expect(toast.success).toHaveBeenCalled();
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("writes the agy binding through the agent and updates the summary", async () => {
    const { onSave } = renderTab({
      device: runtimeWith("antigravity", [AGY_DEFAULT, AGY_ACCOUNT2]),
    });

    await openDrawer();
    selectAccount(/account2/);
    saveAndSwitch();

    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave).toHaveBeenCalledWith({
      custom_args: ["--gemini_dir", AGY_ACCOUNT2.home],
    });
    expect(updateAgentEnv).not.toHaveBeenCalled();
    // The bar owns this line, so it proves the summary — not a chip — moved.
    expect(
      await screen.findByText(`--gemini_dir=${AGY_ACCOUNT2.home}`),
    ).toBeInTheDocument();
  });

  it("sends nothing when the target is already the account in effect", async () => {
    renderTab({
      device: runtimeWith("antigravity", [AGY_DEFAULT, AGY_ACCOUNT2]),
      agent: {
        ...baseAgent,
        custom_args: ["--gemini_dir", AGY_DEFAULT.home],
      },
    });

    await openDrawer();
    // Opening pre-selects the account in effect, so there is nothing to save.
    expect(
      screen.getByRole("button", { name: /save and switch/i }),
    ).toBeDisabled();
    saveAndSwitch();

    expect(getAgentEnv).not.toHaveBeenCalled();
    expect(updateAgentEnv).not.toHaveBeenCalled();
  });

  it("forgets a selection that was never saved", async () => {
    renderTab({
      device: runtimeWith("antigravity", [AGY_DEFAULT, AGY_ACCOUNT2]),
    });

    await openDrawer();
    selectAccount(/account2/);
    expect(
      screen.getByRole("button", { name: /save and switch/i }),
    ).toBeEnabled();

    // Closing the drawer drops the pending selection; nothing was written.
    fireEvent.click(screen.getByRole("button", { name: /close/i }));
    await openDrawer();
    expect(
      screen.getByRole("button", { name: /save and switch/i }),
    ).toBeDisabled();
    expect(
      screen.getByRole("radio", { name: /default/ }),
    ).toHaveAttribute("aria-checked", "true");
  });
});

// The AGY slot list is an agent field (`runtime_config.agy_slots`) the backend
// rotates over, so the drawer's add/drop controls have to reach it — and reach
// it in the same request as the binding, never before "save and switch".
describe("AgentAccountsTab agy slots", () => {
  const twoSlots: Agent = {
    ...baseAgent,
    custom_args: ["--gemini_dir", AGY_DEFAULT.home],
    runtime_config: { agy_slots: { accounts: [1, 2] } },
  };

  it("adds a numbered slot and submits it with the switch in one write", async () => {
    const { onSave } = renderTab({
      device: runtimeWith("antigravity", [AGY_DEFAULT, AGY_ACCOUNT2]),
      agent: twoSlots,
    });

    await openDrawer();
    // Slot 3 has no directory on disk, so the daemon never reported it: the
    // row exists because the agent's own slot list says it may rotate there.
    fireEvent.click(
      screen.getByRole("button", { name: "Add Antigravity account 3" }),
    );
    expect(screen.getByRole("radio", { name: /account3/ })).toBeInTheDocument();

    selectAccount(/account3/);
    saveAndSwitch();

    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave).toHaveBeenCalledWith({
      custom_args: ["--gemini_dir", `${RUNTIME_HOME}/.gemini-account3`],
      runtime_config: { agy_slots: { accounts: [1, 2, 3] } },
    });
    expect(updateAgentEnv).not.toHaveBeenCalled();
  });

  it("saves a slot drop on its own, without touching the binding", async () => {
    const { onSave } = renderTab({
      device: runtimeWith("antigravity", [AGY_DEFAULT, AGY_ACCOUNT2]),
      agent: {
        ...twoSlots,
        runtime_config: { agy_slots: { accounts: [1, 2, 3] } },
      },
    });

    await openDrawer();
    // Dropping a slot that is neither selected nor in effect only changes the
    // rotation list, so the write carries no `custom_args`.
    fireEvent.click(screen.getByRole("button", { name: "Remove Antigravity account 3 from this agent" }));
    saveAndSwitch();

    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave).toHaveBeenCalledWith({
      runtime_config: { agy_slots: { accounts: [1, 2] } },
    });
    expect(updateAgentEnv).not.toHaveBeenCalled();
  });

  it("falls back to slot 1 when the dropped slot was the one selected", async () => {
    const { onSave } = renderTab({
      device: runtimeWith("antigravity", [AGY_DEFAULT, AGY_ACCOUNT2]),
      agent: {
        ...twoSlots,
        custom_args: ["--gemini_dir", AGY_ACCOUNT2.home],
      },
    });

    await openDrawer();
    // Opening selects the account in effect, so dropping it must move the
    // selection back to slot 1 rather than leave a row that no longer exists.
    fireEvent.click(screen.getByRole("button", { name: "Remove Antigravity account 2 from this agent" }));

    expect(
      screen.queryByRole("radio", { name: /account2/ }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /default/ })).toHaveAttribute(
      "aria-checked",
      "true",
    );

    saveAndSwitch();
    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave).toHaveBeenCalledWith({
      custom_args: ["--gemini_dir", AGY_DEFAULT.home],
      runtime_config: { agy_slots: { accounts: [1] } },
    });
  });

  it("sends nothing when the drawer closes with slot edits unsaved", async () => {
    const { onSave } = renderTab({
      device: runtimeWith("antigravity", [AGY_DEFAULT, AGY_ACCOUNT2]),
      agent: twoSlots,
    });

    await openDrawer();
    fireEvent.click(
      screen.getByRole("button", { name: "Add Antigravity account 3" }),
    );
    expect(
      screen.getByRole("button", { name: /save and switch/i }),
    ).toBeEnabled();

    fireEvent.click(screen.getByRole("button", { name: /close/i }));

    // Reopening starts from the agent's real slot list again.
    await openDrawer();
    expect(
      screen.queryByRole("radio", { name: /account3/ }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /save and switch/i }),
    ).toBeDisabled();
    expect(onSave).not.toHaveBeenCalled();
    expect(updateAgentEnv).not.toHaveBeenCalled();
  });

  // Regression: an agent bound to a numbered directory its stored slot list
  // omits — a state the retired custom-path field could save, since it wrote
  // `--gemini_dir` without touching the list. Dropping that row left the
  // account in effect unselectable and made every slot edit fail with
  // "this account cannot be switched from here", losing the edit.
  it("keeps the numbered account in effect even when the stored list omits it", async () => {
    const AGY_ACCOUNT5 = account(
      "agy",
      "account5",
      `${RUNTIME_HOME}/.gemini-account5`,
      { lever: "custom_args:--gemini_dir" },
    );
    const { onSave } = renderTab({
      device: runtimeWith("antigravity", [
        AGY_DEFAULT,
        AGY_ACCOUNT2,
        AGY_ACCOUNT5,
      ]),
      agent: {
        ...baseAgent,
        custom_args: ["--gemini_dir", AGY_ACCOUNT5.home],
        runtime_config: { agy_slots: { accounts: [1, 2, 3] } },
      },
    });

    await openDrawer();
    expect(screen.getByRole("radio", { name: /account5/ })).toHaveAttribute(
      "aria-checked",
      "true",
    );

    // A slot edit still commits, and the write repairs the pair: the account
    // the agent launches with is now also one it may rotate to.
    fireEvent.click(screen.getByRole("button", { name: "Remove Antigravity account 3 from this agent" }));
    saveAndSwitch();

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    expect(onSave).toHaveBeenCalledWith({
      runtime_config: { agy_slots: { accounts: [1, 2, 5] } },
    });
    expect(toast.error).not.toHaveBeenCalled();
  });

  // The case above has the daemon reporting the bound directory. A slot bound
  // straight after `add numbered account` has no directory on disk yet, so the
  // report cannot carry it and the row exists only as the synthesised
  // "signed out" one. Resolving the account in effect against the raw report
  // then claimed no local account matched while listing that exact account as
  // an "other account" one line below, and left every radio unchecked.
  it("keeps a numbered account in effect when its directory is not on disk yet", async () => {
    renderTab({
      device: runtimeWith("antigravity", [AGY_DEFAULT]),
      agent: {
        ...baseAgent,
        custom_args: ["--gemini_dir", `${RUNTIME_HOME}/.gemini-account4`],
        runtime_config: { agy_slots: { accounts: [1, 4] } },
      },
    });

    expect(await screen.findByText("Antigravity · account4")).toBeInTheDocument();
    expect(screen.getByText("In use")).toBeInTheDocument();
    expect(screen.queryByText(/no account on this machine/i)).toBeNull();

    await openDrawer();
    expect(screen.getByRole("radio", { name: /account4/ })).toHaveAttribute(
      "aria-checked",
      "true",
    );
  });

  // The summary bar answers "what is in effect", which only a save can change.
  it("does not let an unsaved slot edit move the account in effect", async () => {
    renderTab({
      device: runtimeWith("antigravity", [AGY_DEFAULT]),
      agent: {
        ...baseAgent,
        custom_args: ["--gemini_dir", `${RUNTIME_HOME}/.gemini-account4`],
        runtime_config: { agy_slots: { accounts: [1, 4] } },
      },
    });

    await openDrawer();
    fireEvent.click(screen.getByRole("button", { name: "Remove Antigravity account 4 from this agent" }));

    // Slot 4 is gone from the pending list, but nothing is saved: the account
    // in effect is still the one the agent launches with.
    expect(screen.getByText("Antigravity · account4")).toBeInTheDocument();
  });
});

// The global sweep for every namespace and locale lives in
// `packages/views/locales/parity.test.ts`. This is the narrow guard for the
// keys THIS surface added: all four bundles must carry the same account keys,
// or one locale silently falls back to English.
// DENE-678: the slot registry is per CLI family. The parse/write matrix lives in
// `account-slots.test.ts` and the add/drop rules in
// `agent-accounts-model.test.ts`; this block only checks that a non-agy family
// is wired to the same controls and the same single write path.
describe("AgentAccountsTab slots of a family that does not rotate", () => {
  const CLAUDE_DEFAULT = account("claude", "default", `${RUNTIME_HOME}/.claude`, {
    lever: "env:CLAUDE_CONFIG_DIR",
  });

  it("registers a dsh account, binds DSH_HOME to it and stores dsh_slots", async () => {
    const { onSave } = renderTab({
      device: runtimeWith("dsh", [DSH_DEFAULT, AGY_DEFAULT]),
      agent: { ...baseAgent, runtime_config: { agy_slots: { accounts: [1, 2] } } },
    });

    await openDrawer();
    fireEvent.click(screen.getByRole("button", { name: "Add DSH account 2" }));
    // The slot has no directory on disk yet, so the row is synthesized — and
    // the drawer says out loud that nothing here rotates on its own.
    expect(screen.getByText(/no automatic switching/i)).toBeInTheDocument();

    fireEvent.click(
      within(screen.getByRole("radiogroup", { name: "DSH accounts" })).getByRole(
        "radio",
        { name: /account2/ },
      ),
    );
    saveAndSwitch();

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    expect(updateAgentEnv).toHaveBeenCalledWith("agent-1", {
      custom_env: { DSH_HOME: `${RUNTIME_HOME}/.dsh-account2` },
    });
    // Only the edited family is written; agy's stored key rides along untouched.
    expect(onSave).toHaveBeenCalledWith({
      runtime_config: {
        agy_slots: { accounts: [1, 2] },
        dsh_slots: { accounts: [1, 2] },
      },
    });
  });

  it("registers a claude account under claude_slots and binds CLAUDE_CONFIG_DIR", async () => {
    const { onSave } = renderTab({
      device: runtimeWith("claude", [CLAUDE_DEFAULT]),
    });

    await openDrawer();
    fireEvent.click(screen.getByRole("button", { name: "Add Claude account 2" }));
    selectAccount(/account2/);
    saveAndSwitch();

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    expect(updateAgentEnv).toHaveBeenCalledWith("agent-1", {
      custom_env: { CLAUDE_CONFIG_DIR: `${RUNTIME_HOME}/.claude-account2` },
    });
    expect(onSave).toHaveBeenCalledWith({
      runtime_config: { claude_slots: { accounts: [1, 2] } },
    });
  });

  it("offers no drop for a directory the daemon keeps reporting", async () => {
    renderTab({
      device: runtimeWith("dsh", [DSH_DEFAULT, DSH_ACCOUNT2]),
      agent: { ...baseAgent, runtime_config: { dsh_slots: { accounts: [1, 2, 3] } } },
    });

    await openDrawer();
    expect(
      screen.getByRole("button", { name: "Remove DSH account 3 from this agent" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Remove DSH account 2 from this agent" }),
    ).not.toBeInTheDocument();
    // The next free number skips both the registered and the reported ones.
    expect(screen.getByRole("button", { name: "Add DSH account 4" })).toBeInTheDocument();
  });

  it("tells a rotating group apart from a manual one", async () => {
    renderTab({
      device: runtimeWith("antigravity", [AGY_DEFAULT, DSH_DEFAULT, CODEX_DEFAULT]),
    });

    await openDrawer();
    expect(screen.getByText(/switches to the next signed-in account/i)).toBeInTheDocument();
    expect(screen.getByText(/no automatic switching/i)).toBeInTheDocument();
    expect(screen.getByText(/gets its own Codex directory/i)).toBeInTheDocument();
  });
});

describe("agents locale bundles", () => {
  it("ship the same accounts keys in all four locales", () => {
    const bundles = {
      en: enAgents,
      "zh-Hans": zhAgents,
      ja: jaAgents,
      ko: koAgents,
    };
    const keySet = (bundle: typeof enAgents) =>
      Object.keys(
        (bundle as { tab_body: { accounts: Record<string, unknown> } }).tab_body
          .accounts,
      ).sort();

    const expected = keySet(enAgents);
    expect(expected.length).toBeGreaterThan(0);
    for (const [locale, bundle] of Object.entries(bundles)) {
      expect(keySet(bundle as typeof enAgents), locale).toEqual(expected);
      // The heading and description now come from the General section that
      // wraps this surface (DENE-492), so they are part of the same parity
      // guard as the keys inside it.
      const inspector = (bundle as { inspector: Record<string, unknown> })
        .inspector;
      expect(inspector.section_accounts, locale).toBeTruthy();
      expect(inspector.section_accounts_hint, locale).toBeTruthy();
      // The retired tab's label must be gone everywhere, not just in English —
      // a leftover key is what keeps a dead entry point one edit away.
      expect(
        (bundle as { tabs: Record<string, unknown> }).tabs.accounts,
        locale,
      ).toBeUndefined();
    }
  });
});
