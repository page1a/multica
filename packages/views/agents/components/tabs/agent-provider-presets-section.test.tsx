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
