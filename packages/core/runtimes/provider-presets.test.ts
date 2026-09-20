import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import {
  PROVIDER_PRESET_DEFAULT_API,
  PROVIDER_PRESET_PROVIDER,
  ProviderPresetActionError,
  activeProviderPreset,
  fetchProviderPresetModels,
  providerPresetErrorKind,
  resolveRuntimeProviderPresets,
  runProviderPresetAction,
  runtimeProviderPresetsKeys,
  runtimeProviderPresetsOptions,
} from "./provider-presets";
import type {
  RuntimeProviderPreset,
  RuntimeProviderPresetRequest,
} from "../types/agent";

const initiateProviderPresetAction = vi.fn();
const getProviderPresetResult = vi.fn();

vi.mock("../api", () => ({
  api: {
    initiateProviderPresetAction: (
      runtimeId: string,
      provider: string,
      action: string,
      payload?: unknown,
    ) => initiateProviderPresetAction(runtimeId, provider, action, payload),
    getProviderPresetResult: (runtimeId: string, requestId: string) =>
      getProviderPresetResult(runtimeId, requestId),
  },
}));

function preset(overrides: Partial<RuntimeProviderPreset> = {}): RuntimeProviderPreset {
  return {
    id: "command-code",
    api: "openai-completions",
    base_url: "http://127.0.0.1:1/v1",
    api_key_env: "COMMAND_CODE_API_KEY",
    key_mask: "sk-…0000",
    has_key: true,
    models: [{ id: "deepseek/deepseek-v4.1-flash", name: "DeepSeek V4.1 Flash" }],
    ...overrides,
  };
}

function request(
  overrides: Partial<RuntimeProviderPresetRequest>,
): RuntimeProviderPresetRequest {
  return {
    id: "req-1",
    runtime_id: "rt-1",
    provider: PROVIDER_PRESET_PROVIDER,
    action: "list",
    status: "completed",
    created_at: "2026-09-16T00:00:00Z",
    updated_at: "2026-09-16T00:00:00Z",
    ...overrides,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  initiateProviderPresetAction.mockResolvedValue({ id: "req-1", status: "pending" });
});

describe("runProviderPresetAction payloads", () => {
  it("omits api_key entirely when the user left the field blank", async () => {
    getProviderPresetResult.mockResolvedValue(
      request({ action: "upsert", providers: [preset()] }),
    );

    await runProviderPresetAction("rt-1", {
      action: "upsert",
      preset: {
        id: "command-code",
        api: PROVIDER_PRESET_DEFAULT_API,
        base_url: "http://127.0.0.1:1/v1",
        models: [{ id: "m1" }],
        api_key: "",
      },
    });

    const payload = initiateProviderPresetAction.mock.calls[0]?.[3] as Record<
      string,
      unknown
    >;
    // Absent, not empty-string: the daemon reads "no api_key field" as "keep
    // the stored credential", so an edit that did not retype the key must not
    // serialise one at all.
    expect("api_key" in payload).toBe(false);
  });

  it("sends the key only when the user typed one", async () => {
    getProviderPresetResult.mockResolvedValue(
      request({ action: "upsert", providers: [preset()] }),
    );

    await runProviderPresetAction("rt-1", {
      action: "upsert",
      preset: {
        id: "command-code",
        api: PROVIDER_PRESET_DEFAULT_API,
        base_url: "http://127.0.0.1:1/v1",
        models: [{ id: "m1" }],
        api_key: "sk-test-0000",
      },
    });

    const payload = initiateProviderPresetAction.mock.calls[0]?.[3] as Record<
      string,
      unknown
    >;
    expect(payload.api_key).toBe("sk-test-0000");
  });

  it("omits model on activate when none was picked", async () => {
    getProviderPresetResult.mockResolvedValue(
      request({ action: "activate", providers: [preset({ active: true })] }),
    );

    await runProviderPresetAction("rt-1", { action: "activate", id: "command-code" });

    expect(initiateProviderPresetAction).toHaveBeenCalledWith(
      "rt-1",
      PROVIDER_PRESET_PROVIDER,
      "activate",
      { id: "command-code" },
    );
  });

  it("sends a delete payload with only the provider id", async () => {
    getProviderPresetResult.mockResolvedValue(
      request({ action: "delete", providers: [] }),
    );

    await runProviderPresetAction("rt-1", { action: "delete", id: "command-code" });

    expect(initiateProviderPresetAction).toHaveBeenCalledWith(
      "rt-1",
      PROVIDER_PRESET_PROVIDER,
      "delete",
      { id: "command-code" },
    );
  });
});

describe("runProviderPresetAction results", () => {
  it("returns the refreshed list from the receipt, without a follow-up list", async () => {
    getProviderPresetResult.mockResolvedValue(
      request({
        action: "activate",
        providers: [preset({ active: true })],
        active: { provider: "command-code", model: "deepseek/deepseek-v4.1-flash" },
      }),
    );

    const result = await runProviderPresetAction("rt-1", {
      action: "activate",
      id: "command-code",
    });

    expect(result.presets).toHaveLength(1);
    expect(result.active?.provider).toBe("command-code");
    // One POST for the action itself and nothing else: the receipt IS the
    // refresh, so a second round trip to the user's machine would be waste.
    expect(initiateProviderPresetAction).toHaveBeenCalledTimes(1);
  });

  it("reads cleared_active strictly, so an older daemon's omission is not a clear", async () => {
    getProviderPresetResult.mockResolvedValue(
      request({ action: "delete", providers: [] }),
    );

    const result = await runProviderPresetAction("rt-1", {
      action: "delete",
      id: "command-code",
    });

    expect(result.clearedActive).toBe(false);
  });

  it("surfaces cleared_active when the daemon reports it", async () => {
    getProviderPresetResult.mockResolvedValue(
      request({ action: "delete", providers: [], cleared_active: true }),
    );

    const result = await runProviderPresetAction("rt-1", {
      action: "delete",
      id: "command-code",
    });

    expect(result.clearedActive).toBe(true);
  });

  it("throws the daemon's own error text on a failed action", async () => {
    getProviderPresetResult.mockResolvedValue(
      request({ status: "failed", error: "settings.yaml is not valid YAML" }),
    );

    await expect(resolveRuntimeProviderPresets("rt-1")).rejects.toThrow(
      "settings.yaml is not valid YAML",
    );
  });

  it("throws rather than reporting an empty configuration on a timeout", async () => {
    getProviderPresetResult.mockResolvedValue(request({ status: "timeout" }));

    await expect(resolveRuntimeProviderPresets("rt-1")).rejects.toThrow(/timeout/);
  });

  // The client's own malformed-response guard: `parseWithFallback` hands this
  // module a `failed` record rather than throwing, and an unknown status is
  // normalised to `failed` too. Either way the caller must see an error, never
  // an empty preset list — which would read as "this machine has no providers"
  // and offer to overwrite a configuration we could not read.
  it("treats a malformed receipt as a failure, not an empty configuration", async () => {
    getProviderPresetResult.mockResolvedValue(
      request({ status: "failed", error: "invalid provider preset response" }),
    );

    await expect(resolveRuntimeProviderPresets("rt-1")).rejects.toThrow(
      "invalid provider preset response",
    );
  });

  it("treats a status this client does not know as a failure", async () => {
    getProviderPresetResult.mockResolvedValue(
      request({ status: "superseded" as never }),
    );

    await expect(resolveRuntimeProviderPresets("rt-1")).rejects.toThrow();
  });
});

describe("query wiring", () => {
  it("keys the cache by runtime, not by workspace", () => {
    expect(runtimeProviderPresetsKeys.forRuntime("rt-1")).toEqual([
      "runtimes",
      "provider-presets",
      "rt-1",
    ]);
  });

  it("stays disabled without a runtime id", () => {
    const client = new QueryClient();
    const options = runtimeProviderPresetsOptions(null);
    expect(options.enabled).toBe(false);
    client.clear();
  });
});

describe("activeProviderPreset", () => {
  it("picks the preset flagged active", () => {
    const active = preset({ id: "b", active: true });
    expect(activeProviderPreset([preset({ id: "a" }), active])?.id).toBe("b");
  });

  it("returns null when no preset claims to be active", () => {
    expect(activeProviderPreset([preset({ id: "a" })])).toBeNull();
  });
});

describe("the models action payload", () => {
  it("sends the endpoint, the declared protocol and the typed key", async () => {
    getProviderPresetResult.mockResolvedValue(
      request({
        action: "models",
        models: [{ id: "deepseek/deepseek-v4.1-flash", name: "DeepSeek" }],
      }),
    );

    await fetchProviderPresetModels("rt-1", {
      id: "command-code",
      baseUrl: " https://api.example.test/v1 ",
      api: " openai-completions ",
      apiKey: " sk-test-0000 ",
    });

    expect(initiateProviderPresetAction).toHaveBeenCalledWith(
      "rt-1",
      PROVIDER_PRESET_PROVIDER,
      "models",
      {
        base_url: "https://api.example.test/v1",
        api: "openai-completions",
        id: "command-code",
        api_key: "sk-test-0000",
      },
    );
  });

  it("omits api_key when nothing was typed, so a stored credential is reused", async () => {
    getProviderPresetResult.mockResolvedValue(
      request({ action: "models", models: [{ id: "m1" }] }),
    );

    await fetchProviderPresetModels("rt-1", {
      id: "command-code",
      baseUrl: "https://api.example.test/v1",
      api: PROVIDER_PRESET_DEFAULT_API,
    });

    const payload = initiateProviderPresetAction.mock.calls[0]?.[3] as Record<
      string,
      unknown
    >;
    expect("api_key" in payload).toBe(false);
  });

  it("returns the catalog verbatim and the protocol that answered", async () => {
    getProviderPresetResult.mockResolvedValue(
      request({
        action: "models",
        models: [
          { id: "deepseek/deepseek-v4.1-flash", name: "DeepSeek V4.1 Flash" },
          { id: "claude-sonnet-5", context_length: 200000 },
        ],
      }),
    );

    const result = await fetchProviderPresetModels("rt-1", {
      baseUrl: "https://api.example.test/v1",
      api: PROVIDER_PRESET_DEFAULT_API,
    });

    // The gateway's own id travels untouched — no escaping happens here.
    expect(result.models.map((model) => model.id)).toEqual([
      "deepseek/deepseek-v4.1-flash",
      "claude-sonnet-5",
    ]);
    expect(result.api).toBe(PROVIDER_PRESET_DEFAULT_API);
  });
});

describe("the anthropic auth-convention retry", () => {
  it("retries once on the other convention and reports the one that worked", async () => {
    getProviderPresetResult
      .mockResolvedValueOnce(
        request({
          action: "models",
          status: "failed",
          error: "The provider rejected this API key (HTTP 401)",
          error_kind: "invalid_credential",
          error_params: { status: "401" },
        }),
      )
      .mockResolvedValueOnce(
        request({
          action: "models",
          models: [{ id: "claude-sonnet-5" }],
        }),
      );

    const result = await fetchProviderPresetModels("rt-1", {
      baseUrl: "https://api.example.test/v1",
      api: PROVIDER_PRESET_DEFAULT_API,
    });

    expect(initiateProviderPresetAction).toHaveBeenCalledTimes(2);
    expect(initiateProviderPresetAction.mock.calls[1]?.[2]).toBe("models");
    expect(
      (initiateProviderPresetAction.mock.calls[1]?.[3] as Record<string, unknown>)
        .api,
    ).toBe("anthropic-messages");
    expect(result.api).toBe("anthropic-messages");
    expect(result.models.map((model) => model.id)).toEqual(["claude-sonnet-5"]);
  });

  it("tries the defined default first when the route already declares anthropic", async () => {
    getProviderPresetResult.mockResolvedValue(
      request({ action: "models", models: [{ id: "m1" }] }),
    );

    await fetchProviderPresetModels("rt-1", {
      baseUrl: "https://api.example.test/v1",
      api: "anthropic-messages",
    });

    expect(initiateProviderPresetAction).toHaveBeenCalledTimes(1);
    expect(
      (initiateProviderPresetAction.mock.calls[0]?.[3] as Record<string, unknown>)
        .api,
    ).toBe("anthropic-messages");
  });

  it("does not retry a failure a second convention cannot fix", async () => {
    getProviderPresetResult.mockResolvedValue(
      request({
        action: "models",
        status: "failed",
        error: "https://api.example.test/v1 does not publish a model list (HTTP 404).",
        error_kind: "models_unavailable",
      }),
    );

    await expect(
      fetchProviderPresetModels("rt-1", {
        baseUrl: "https://api.example.test/v1",
        api: PROVIDER_PRESET_DEFAULT_API,
      }),
    ).rejects.toThrow(/does not publish a model list/);
    expect(initiateProviderPresetAction).toHaveBeenCalledTimes(1);
  });
});

describe("providerPresetErrorKind", () => {
  it("reads the kind off a thrown action error", async () => {
    getProviderPresetResult.mockResolvedValue(
      request({
        status: "failed",
        error: "quota",
        error_kind: "rate_limited",
        error_params: { action: "regenerate_key" },
      }),
    );

    await expect(resolveRuntimeProviderPresets("rt-1")).rejects.toMatchObject({
      name: "ProviderPresetActionError",
      kind: "rate_limited",
      params: { action: "regenerate_key" },
    });
  });

  it("is empty for an error the client raised itself", () => {
    expect(providerPresetErrorKind(new Error("boom"))).toBe("");
    expect(providerPresetErrorKind(new ProviderPresetActionError("boom"))).toBe("");
  });
});
