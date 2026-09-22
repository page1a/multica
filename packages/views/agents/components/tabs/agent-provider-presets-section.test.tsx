// @vitest-environment jsdom
//
// Component suite for the agent accounts tab's "providers" section (DENE-348).
//
// The rules this surface renders — view states, form validation, the upsert
// body and the key-state mapping — have one canonical test layer, in node:
// `provider-presets-model.test.ts`. This file does NOT re-run that matrix
// through a mount. It checks the wiring only: which state renders which
// control, that the section stays absent on a runtime with no driver, and the
// named regressions the credential invariant depends on.

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import type {
  RuntimeDevice,
  RuntimeProviderPreset,
  RuntimeProviderPresetsResult,
} from "@multica/core/types";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

const resolveRuntimeProviderPresets = vi.hoisted(() => vi.fn());
const runProviderPresetAction = vi.hoisted(() => vi.fn());
const fetchProviderPresetModels = vi.hoisted(() => vi.fn());
const syncProviderPresetToRuntimes = vi.hoisted(() => vi.fn());
const listRuntimes = vi.hoisted(() => vi.fn());

// The section's data layer is `packages/core/runtimes/provider-presets.ts`,
// whose own contract with the API is covered by its own suite. Here it is
// mocked down to the two functions the hooks call, so the mount exercises the
// component and not the poll loop.
vi.mock("@multica/core/runtimes", async () => {
  const { queryOptions, useMutation, useQueryClient } = await import(
    "@tanstack/react-query"
  );
  const keys = {
    all: () => ["runtimes", "provider-presets"] as const,
    forRuntime: (runtimeId: string) =>
      ["runtimes", "provider-presets", runtimeId] as const,
  };
  return {
    PROVIDER_PRESET_APIS: [
      "openai-completions",
      "openai-responses",
      "anthropic-messages",
    ],
    PROVIDER_PRESET_DEFAULT_API: "openai-completions",
    runtimeProviderPresetsKeys: keys,
    runtimeProviderPresetsOptions: (runtimeId: string) =>
      queryOptions({
        queryKey: keys.forRuntime(runtimeId),
        queryFn: () => resolveRuntimeProviderPresets(runtimeId),
        retry: false,
      }),
    useProviderPresetMutation: (runtimeId: string) => {
      const queryClient = useQueryClient();
      return useMutation({
        mutationFn: (input: unknown) => runProviderPresetAction(runtimeId, input),
        onSuccess: (result: RuntimeProviderPresetsResult) =>
          queryClient.setQueryData(keys.forRuntime(runtimeId), result),
      });
    },
    // Called directly by the model selector, not through a hook. Its own retry
    // rule and payload live in provider-presets.test.ts; here it is a stub whose
    // resolved value is the catalog under test.
    fetchProviderPresetModels: (runtimeId: string, query: unknown) =>
      fetchProviderPresetModels(runtimeId, query),
    // The fan-out's own contract — per-machine outcomes, the key never
    // invented — is covered in provider-presets.test.ts. Here it is the stub
    // whose receipts the dialog has to render.
    useProviderPresetSyncMutation: () => {
      const queryClient = useQueryClient();
      return useMutation({
        mutationFn: (input: unknown) => syncProviderPresetToRuntimes(input),
        onSuccess: (sync: { configs: Record<string, RuntimeProviderPresetsResult> }) => {
          for (const [runtimeId, config] of Object.entries(sync.configs ?? {})) {
            queryClient.setQueryData(keys.forRuntime(runtimeId), config);
          }
        },
      });
    },
    // Pure helper the model layer calls through; the real one is trivial and
    // has no API surface, so the mock keeps its behaviour rather than a stub.
    runtimeDisplayName: (rt: { name: string; custom_name?: string | null }) =>
      rt.custom_name?.trim() ? rt.custom_name : rt.name,
    providerPresetSyncSummary: (
      outcomes: readonly { status: string }[],
    ) => {
      const synced = outcomes.filter((outcome) => outcome.status === "synced").length;
      return { synced, failed: outcomes.length - synced };
    },
  };
});

// The sync target list is the workspace's own runtime list. Both halves are
// mocked so a mount can put a second machine on screen without a route.
vi.mock("@multica/core/runtimes/queries", async () => {
  const { queryOptions } = await import("@tanstack/react-query");
  return {
    runtimeListOptions: (wsId: string) =>
      queryOptions({ queryKey: ["runtimes", wsId, "list"], queryFn: () => listRuntimes() }),
  };
});


vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

import { AgentProviderPresetsSection } from "./agent-provider-presets-section";

function runtime(overrides: Partial<RuntimeDevice> = {}): RuntimeDevice {
  return {
    id: "rt-1",
    workspace_id: "ws-1",
    daemon_id: null,
    name: "Mac",
    runtime_mode: "local",
    provider: "dsh",
    launch_header: "",
    status: "online",
    device_info: "",
    metadata: {},
    owner_id: "user-1",
    visibility: "private",
    last_seen_at: null,
    ...overrides,
  } as RuntimeDevice;
}

function preset(overrides: Partial<RuntimeProviderPreset> = {}): RuntimeProviderPreset {
  return {
    id: "command-code",
    api: "openai-completions",
    base_url: "https://api.example.test/v1",
    api_key_env: "COMMAND_CODE_API_KEY",
    key_mask: "sk-…0000",
    has_key: true,
    active: true,
    models: [{ id: "m1", name: "Model One" }],
    ...overrides,
  };
}

function result(
  presets: RuntimeProviderPreset[],
  overrides: Partial<RuntimeProviderPresetsResult> = {},
): RuntimeProviderPresetsResult {
  return {
    presets,
    active: presets.find((p) => p.active) ? { provider: presets[0]!.id, model: "m1" } : null,
    clearedActive: false,
    ...overrides,
  };
}

function renderSection(device?: RuntimeDevice) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <AgentProviderPresetsSection runtimeDevice={device} />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  resolveRuntimeProviderPresets.mockResolvedValue(result([preset()]));
  // One machine by default: a workspace with nothing to sync to.
  listRuntimes.mockResolvedValue([runtime()]);
});

describe("driver gate", () => {
  it("renders nothing for a runtime with no preset driver", async () => {
    const { container } = renderSection(runtime({ provider: "antigravity" }));
    expect(container).toBeEmptyDOMElement();
    // Not merely hidden — the section never asks the machine for a list it
    // has no driver to answer.
    expect(resolveRuntimeProviderPresets).not.toHaveBeenCalled();
  });

  it("renders nothing without a runtime at all", () => {
    const { container } = renderSection(undefined);
    expect(container).toBeEmptyDOMElement();
  });
});

describe("view states", () => {
  it("lists presets and marks the one in effect", async () => {
    renderSection(runtime());
    expect(await screen.findByText("command-code")).toBeInTheDocument();
    expect(screen.getByText("In effect")).toBeInTheDocument();
    // The active row offers no "use this" — it already is.
    expect(screen.queryByRole("button", { name: "Use this" })).not.toBeInTheDocument();
  });

  it("shows the masked key, never a key value", async () => {
    renderSection(runtime());
    // The mask shares its line with the model count, so match the fragment.
    expect(await screen.findByText(/Key sk-…0000/)).toBeInTheDocument();
    expect(screen.getByText(/1 model\b/)).toBeInTheDocument();
  });

  it("offers no editing affordance when the list could not be read", async () => {
    resolveRuntimeProviderPresets.mockRejectedValue(
      new Error("daemon did not respond within 30 seconds"),
    );
    renderSection(runtime());

    expect(
      await screen.findByText("Can't read provider configuration"),
    ).toBeInTheDocument();
    // An untrustworthy list must not drive an activate or a delete.
    expect(
      screen.queryByRole("button", { name: /Add provider/ }),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit" })).not.toBeInTheDocument();
  });

  // The restore control is the recovery path for a CLI that was reinstalled or
  // reset under a configuration Multica already wrote (DENE-683). Its rules —
  // what the daemon puts back and what it refuses to overwrite — are covered in
  // server/internal/daemon/dsh_provider_ledger_test.go; this checks the wiring.
  it("sends a replay and never a credential when restore is pressed", async () => {
    runProviderPresetAction.mockResolvedValue(result([preset()]));
    renderSection(runtime());

    fireEvent.click(await screen.findByRole("button", { name: /Restore from Multica/ }));

    await waitFor(() => expect(runProviderPresetAction).toHaveBeenCalledTimes(1));
    expect(runProviderPresetAction).toHaveBeenCalledWith("rt-1", { action: "replay" });
  });

  it("offers restore on an empty list, which is what a reset CLI looks like", async () => {
    resolveRuntimeProviderPresets.mockResolvedValue(result([]));
    renderSection(runtime());

    expect(await screen.findByText("No providers configured")).toBeInTheDocument();
    expect(
      screen.getAllByRole("button", { name: /Restore from Multica/ }).length,
    ).toBeGreaterThan(0);
  });

  it("offers no restore when the list could not be read", async () => {
    resolveRuntimeProviderPresets.mockRejectedValue(new Error("daemon did not respond"));
    renderSection(runtime());

    expect(
      await screen.findByText("Can't read provider configuration"),
    ).toBeInTheDocument();
    // A section that could not read the machine must not offer to write it.
    expect(
      screen.queryByRole("button", { name: /Restore from Multica/ }),
    ).not.toBeInTheDocument();
  });

  it("offers add on an empty but trustworthy list", async () => {
    resolveRuntimeProviderPresets.mockResolvedValue(result([]));
    renderSection(runtime());

    expect(await screen.findByText("No providers configured")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Add provider/ })).toBeInTheDocument();
  });
});

describe("the write-only key", () => {
  it("opens the edit form with an empty key box, not the mask", async () => {
    renderSection(runtime());
    fireEvent.click(await screen.findByRole("button", { name: "Edit" }));

    const keyInput = (await screen.findByLabelText("API key")) as HTMLInputElement;
    // The regression this section is built to prevent: a mask pre-filled here
    // would be submitted as the new credential on the very next save.
    expect(keyInput.value).toBe("");
    expect(keyInput.type).toBe("password");
    // The mask still has to be readable somewhere, just not as an input value.
    expect(screen.getByText(/A key is stored \(sk-…0000\)/)).toBeInTheDocument();
  });

  it("submits no api_key at all when the box was left untouched", async () => {
    runProviderPresetAction.mockResolvedValue(result([preset()]));
    renderSection(runtime());
    fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
    fireEvent.click(await screen.findByRole("button", { name: "Save" }));

    await waitFor(() => expect(runProviderPresetAction).toHaveBeenCalled());
    const input = runProviderPresetAction.mock.calls[0]?.[1] as {
      action: string;
      preset: Record<string, unknown>;
    };
    expect(input.action).toBe("upsert");
    expect("api_key" in input.preset).toBe(false);
  });

  it("sends the key when the user typed one", async () => {
    runProviderPresetAction.mockResolvedValue(result([preset()]));
    renderSection(runtime());
    fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
    fireEvent.change(await screen.findByLabelText("API key"), {
      target: { value: "sk-test-0000" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(runProviderPresetAction).toHaveBeenCalled());
    const input = runProviderPresetAction.mock.calls[0]?.[1] as {
      preset: Record<string, unknown>;
    };
    expect(input.preset.api_key).toBe("sk-test-0000");
  });
});

describe("actions", () => {
  it("activates the row the user picked and redraws from the receipt", async () => {
    resolveRuntimeProviderPresets.mockResolvedValue(
      result([preset(), preset({ id: "other", active: false })]),
    );
    runProviderPresetAction.mockResolvedValue(
      result([preset({ active: false }), preset({ id: "other", active: true })]),
    );
    renderSection(runtime());

    fireEvent.click(await screen.findByRole("button", { name: "Use this" }));

    await waitFor(() =>
      expect(runProviderPresetAction).toHaveBeenCalledWith("rt-1", {
        action: "activate",
        id: "other",
      }),
    );
    // One round trip: the receipt carries the refreshed list, so no follow-up
    // read is issued.
    await waitFor(() => expect(resolveRuntimeProviderPresets).toHaveBeenCalledTimes(1));
  });

  it("requires a confirmation before deleting, and says what deleting the active one costs", async () => {
    renderSection(runtime());
    fireEvent.click(await screen.findByRole("button", { name: /Delete provider/ }));

    expect(await screen.findByText("Delete command-code?")).toBeInTheDocument();
    expect(
      screen.getByText(/leaves the CLI with no default model/),
    ).toBeInTheDocument();
    // Nothing is written until the confirmation is taken.
    expect(runProviderPresetAction).not.toHaveBeenCalled();

    runProviderPresetAction.mockResolvedValue(result([], { clearedActive: true }));
    fireEvent.click(screen.getByRole("button", { name: "Delete" }));

    await waitFor(() =>
      expect(runProviderPresetAction).toHaveBeenCalledWith("rt-1", {
        action: "delete",
        id: "command-code",
      }),
    );
  });

  it("keeps the form open and writes nothing when validation fails", async () => {
    resolveRuntimeProviderPresets.mockResolvedValue(result([]));
    renderSection(runtime());

    fireEvent.click(await screen.findByRole("button", { name: /Add provider/ }));
    fireEvent.click(await screen.findByRole("button", { name: "Save" }));

    expect(await screen.findByText("A name is required.")).toBeInTheDocument();
    expect(runProviderPresetAction).not.toHaveBeenCalled();
  });

  it("surfaces a failed write and leaves the list as the machine last reported it", async () => {
    runProviderPresetAction.mockRejectedValue(new Error("settings.yaml is not valid YAML"));
    const { toast } = await import("sonner");
    renderSection(runtime());

    fireEvent.click(await screen.findByRole("button", { name: /Delete provider/ }));
    fireEvent.click(await screen.findByRole("button", { name: "Delete" }));

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith("settings.yaml is not valid YAML"),
    );
    // Not optimistic: the row the delete failed on is still on screen.
    expect(screen.getByText("command-code")).toBeInTheDocument();
  });
});

// The catalog rules themselves — filtering, label fallback, context-window
// reading — are unit-tested in provider-presets-model.test.ts. These mounts
// cover the wiring the acceptance names: the fetched dropdown, the in-progress
// state of a multi-second verification, and the failed one.
const CATALOG = [
  {
    id: "deepseek/deepseek-v4.1-flash",
    name: "DeepSeek V4.1 Flash",
    context_window: 1_000_000,
  },
  { id: "claude-sonnet-5", name: "Claude Sonnet 5", context_window: 200_000 },
];

async function openAddForm() {
  resolveRuntimeProviderPresets.mockResolvedValue(result([]));
  renderSection(runtime());
  fireEvent.click(await screen.findByRole("button", { name: /Add provider/ }));
  fireEvent.change(screen.getByLabelText("Name"), {
    target: { value: "command-code2" },
  });
  fireEvent.change(screen.getByLabelText("API endpoint"), {
    target: { value: "https://api.example.test/v1" },
  });
}

function addManualModel(id: string) {
  fireEvent.change(screen.getByLabelText("Model id"), { target: { value: id } });
  fireEvent.click(screen.getByRole("button", { name: "Add model" }));
}

describe("the model selector", () => {
  it("fetches on request, filters as the user types, and keeps the real id", async () => {
    fetchProviderPresetModels.mockResolvedValue({
      models: CATALOG,
      api: "openai-completions",
    });
    await openAddForm();

    // Nothing is asked of the machine until a credential exists to ask with.
    expect(screen.getByRole("button", { name: "Fetch models" })).toBeDisabled();
    expect(
      screen.getByText(/Enter an API endpoint and key first/),
    ).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("API key"), {
      target: { value: "sk-1" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Fetch models" }));

    await waitFor(() => expect(fetchProviderPresetModels).toHaveBeenCalled());
    expect(fetchProviderPresetModels.mock.calls[0]?.[1]).toMatchObject({
      baseUrl: "https://api.example.test/v1",
      api: "openai-completions",
      apiKey: "sk-1",
    });

    fireEvent.click(await screen.findByTestId("preset-model-catalog-trigger"));
    fireEvent.change(await screen.findByPlaceholderText("Search models"), {
      target: { value: "sonnet" },
    });

    // The search hits both the display name and the wire id, and a row that
    // does not match is gone rather than merely hidden.
    expect(
      screen.getByTestId("preset-model-catalog-row-claude-sonnet-5"),
    ).toBeInTheDocument();
    expect(
      screen.queryByTestId("preset-model-catalog-row-deepseek/deepseek-v4.1-flash"),
    ).not.toBeInTheDocument();

    fireEvent.click(screen.getByTestId("preset-model-catalog-row-claude-sonnet-5"));

    // Selected: the label the user reads plus the id the request needs.
    const selectedModels = screen.getByTestId("preset-selected-models");
    expect(selectedModels).toHaveTextContent("Claude Sonnet 5");
    expect(selectedModels).toHaveTextContent("claude-sonnet-5");
    expect(selectedModels).toHaveTextContent("200,000 tokens");
  });

  it("sends the picked id verbatim, and still accepts a hand-typed one", async () => {
    fetchProviderPresetModels.mockResolvedValue({
      models: CATALOG,
      api: "openai-completions",
    });
    runProviderPresetAction.mockResolvedValue(result([]));
    await openAddForm();
    fireEvent.change(screen.getByLabelText("API key"), {
      target: { value: "sk-1" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Fetch models" }));
    fireEvent.click(await screen.findByTestId("preset-model-catalog-trigger"));
    fireEvent.click(
      await screen.findByTestId(
        "preset-model-catalog-row-deepseek/deepseek-v4.1-flash",
      ),
    );

    // A gateway with no model list is not worse off than before this selector.
    addManualModel("local-llama");
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(runProviderPresetAction).toHaveBeenCalledWith(
        "rt-1",
        expect.objectContaining({ action: "upsert" }),
      ),
    );
    const input = runProviderPresetAction.mock.calls[0]?.[1] as {
      preset: { models: { id: string }[] };
    };
    // Verbatim: escaping belongs to the seat string's codec, not the preset.
    expect(input.preset.models.map((model) => model.id)).toEqual([
      "deepseek/deepseek-v4.1-flash",
      "local-llama",
    ]);
  });
});

describe("verifying a save", () => {
  it("reports the multi-second step and blocks a second submit", async () => {
    let settle: (value: unknown) => void = () => {};
    runProviderPresetAction.mockReturnValue(
      new Promise((resolve) => {
        settle = resolve;
      }),
    );
    await openAddForm();
    addManualModel("deepseek/deepseek-v4.1-flash");
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByTestId("preset-verify-progress")).toBeInTheDocument();
    expect(screen.getByText("Verifying before saving")).toBeInTheDocument();
    // A second press while the probes run would race two writes.
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();

    settle(result([]));
    await waitFor(() =>
      expect(
        screen.queryByTestId("preset-verify-progress"),
      ).not.toBeInTheDocument(),
    );
  });

  it("keeps the form open on a failed probe and names the actionable next step", async () => {
    runProviderPresetAction.mockRejectedValue(
      Object.assign(new Error("provider quota is used up"), {
        kind: "rate_limited",
        params: {
          status: "429",
          action: "regenerate_key",
          reset_at_local: "2026-09-21 08:00",
        },
      }),
    );
    const { toast } = await import("sonner");
    await openAddForm();
    addManualModel("deepseek/deepseek-v4.1-flash");
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByTestId("preset-verify-failure")).toBeInTheDocument();
    expect(screen.getByTestId("preset-verify-failure-message")).toHaveTextContent(
      /quota is used up/,
    );
    expect(screen.getByText(/2026-09-21 08:00/)).toBeInTheDocument();
    const link = screen.getByRole("link", {
      name: /Open the provider dashboard/,
    });
    expect(link).toHaveAttribute("href", "https://api.example.test/");

    // Everything the user typed is still there, and the failure is in the form
    // rather than a toast that would outlive the screen that explains it.
    expect(screen.getByLabelText("Name")).toHaveValue("command-code2");
    expect(screen.getByTestId("preset-selected-models")).toHaveTextContent(
      "deepseek/deepseek-v4.1-flash",
    );
    expect(toast.success).not.toHaveBeenCalled();
    expect(toast.error).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// Syncing one preset across machines (DENE-335)
// ---------------------------------------------------------------------------
//
// The drift classification itself is canonical in the model suite. These
// mounts check the three things only a render can be wrong about: that the
// affordance appears exactly when there is another machine, that an offline
// machine cannot be selected, and that a partial fan-out keeps the dialog open
// naming the machine that refused.

const peer = () =>
  runtime({ id: "rt-2", name: "MacBook-Air-5", custom_name: "Air" });

async function openSyncDialog() {
  renderSection(runtime());
  fireEvent.click(
    await screen.findByRole("button", { name: "Sync provider command-code to other machines" }),
  );
  return screen.findByRole("dialog");
}

describe("syncing a preset to other machines", () => {
  it("offers no sync affordance when this is the only preset-capable machine", async () => {
    listRuntimes.mockResolvedValue([runtime(), runtime({ id: "rt-3", provider: "codex" })]);
    renderSection(runtime());

    expect(await screen.findByText("command-code")).toBeInTheDocument();
    await waitFor(() =>
      expect(
        screen.queryByRole("button", { name: /Sync provider/ }),
      ).not.toBeInTheDocument(),
    );
  });

  it("names the other machine's endpoint when it differs", async () => {
    listRuntimes.mockResolvedValue([runtime(), peer()]);
    resolveRuntimeProviderPresets.mockImplementation((runtimeId: string) =>
      Promise.resolve(
        result([
          runtimeId === "rt-2"
            ? preset({ base_url: "https://opencode.example.test/zen/v1" })
            : preset(),
        ]),
      ),
    );

    await openSyncDialog();

    expect(await screen.findByText("Air")).toBeInTheDocument();
    expect(
      await screen.findByText("Different endpoint: https://opencode.example.test/zen/v1"),
    ).toBeInTheDocument();
  });

  // An offline daemon never claims the request, so a checked row would promise
  // a write that cannot happen.
  it("lists an offline machine but does not let it be selected", async () => {
    listRuntimes.mockResolvedValue([runtime(), peer()]);
    // The peer is offline: `status` is what the list reports, and the section
    // must not even try to read it.
    listRuntimes.mockResolvedValue([
      runtime(),
      runtime({ id: "rt-2", custom_name: "Air", status: "offline" }),
    ]);

    await openSyncDialog();

    expect(await screen.findByText("Air")).toBeInTheDocument();
    expect(
      screen.getByText("Offline — can't be written to until it is back"),
    ).toBeInTheDocument();
    expect(screen.getByRole("checkbox")).toHaveAttribute("aria-disabled", "true");
    expect(resolveRuntimeProviderPresets).not.toHaveBeenCalledWith("rt-2");
  });

  // Until the peer's read lands nobody knows whether it has the preset or a
  // key, so a write now would skip both the drift report and the key check.
  it("does not let a sync start while a selected machine is still being read", async () => {
    listRuntimes.mockResolvedValue([runtime(), peer()]);
    resolveRuntimeProviderPresets.mockImplementation((runtimeId: string) =>
      runtimeId === "rt-2" ? new Promise(() => {}) : Promise.resolve(result([preset()])),
    );

    await openSyncDialog();

    expect(await screen.findByText("Air")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sync 1 machine" })).toBeDisabled();
  });

  it("refuses to sync to a machine with no key of its own until one is typed", async () => {
    listRuntimes.mockResolvedValue([runtime(), peer()]);
    resolveRuntimeProviderPresets.mockImplementation((runtimeId: string) =>
      Promise.resolve(runtimeId === "rt-2" ? result([]) : result([preset()])),
    );

    await openSyncDialog();
    expect(await screen.findByText("Doesn't have this provider")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Sync 1 machine" }));

    expect(
      await screen.findByText(
        "One of the selected machines has no key for this provider. Type the key to sync it.",
      ),
    ).toBeInTheDocument();
    expect(syncProviderPresetToRuntimes).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText("API key (optional)"), {
      target: { value: "sk-live" },
    });
    syncProviderPresetToRuntimes.mockResolvedValue({
      outcomes: [{ runtimeId: "rt-2", status: "synced", error: "", errorKind: "" }],
      configs: {},
    });
    fireEvent.click(screen.getByRole("button", { name: "Sync 1 machine" }));

    await waitFor(() =>
      expect(syncProviderPresetToRuntimes).toHaveBeenCalledWith({
        runtimeIds: ["rt-2"],
        preset: expect.objectContaining({ id: "command-code", api_key: "sk-live" }),
      }),
    );
  });

  it("keeps the dialog open on a partial fan-out and says which machine refused", async () => {
    listRuntimes.mockResolvedValue([
      runtime(),
      peer(),
      runtime({ id: "rt-3", custom_name: "Studio" }),
    ]);
    syncProviderPresetToRuntimes.mockResolvedValue({
      outcomes: [
        { runtimeId: "rt-2", status: "synced", error: "", errorKind: "" },
        {
          runtimeId: "rt-3",
          status: "failed",
          error: "settings.yaml is locked",
          errorKind: "",
        },
      ],
      configs: {},
    });

    await openSyncDialog();
    fireEvent.click(await screen.findByRole("button", { name: "Sync 2 machines" }));

    expect(await screen.findByText("Failed: settings.yaml is locked")).toBeInTheDocument();
    expect(screen.getByText("Synced")).toBeInTheDocument();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });
});
