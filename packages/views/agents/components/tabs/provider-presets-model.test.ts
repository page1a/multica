// @vitest-environment node

// Canonical suite for the providers section's rules. The component test
// (`agent-provider-presets-section.test.tsx`) keeps the happy path and the
// wiring and does not re-run this matrix through a DOM mount.

import { describe, expect, it } from "vitest";
import type { RuntimeDevice, RuntimeProviderPreset } from "@multica/core/types";
import {
  IDLE_PROVIDER_PRESET_SAVE,
  canFetchProviderPresetModels,
  canManageProviderPresets,
  deletingActivePreset,
  emptyProviderPresetForm,
  filterProviderPresetModels,
  isKnownProviderPresetFailure,
  providerConsoleUrl,
  providerPresetContextWindow,
  providerPresetFailureFrom,
  providerPresetFormFrom,
  providerPresetKeyState,
  providerPresetModels,
  providerPresetNeedsKeyRegeneration,
  providerPresetPeerState,
  providerPresetSummaryLine,
  providerPresetSyncInput,
  providerPresetSyncNeedsKey,
  providerPresetSyncTargets,
  providerPresetUpsertInput,
  providerPresetsViewState,
  reduceProviderPresetSave,
  supportsProviderPresets,
  validateProviderPresetForm,
} from "./provider-presets-model";
import type { ProviderPresetSyncTarget } from "./provider-presets-model";

function preset(overrides: Partial<RuntimeProviderPreset> = {}): RuntimeProviderPreset {
  return {
    id: "command-code",
    api: "openai-completions",
    base_url: "https://api.example.test/v1",
    api_key_env: "COMMAND_CODE_API_KEY",
    key_mask: "sk-…0000",
    has_key: true,
    models: [{ id: "m1", name: "Model One" }],
    ...overrides,
  };
}

describe("supportsProviderPresets", () => {
  it("accepts dsh regardless of casing or padding", () => {
    expect(supportsProviderPresets("dsh")).toBe(true);
    expect(supportsProviderPresets(" DSH ")).toBe(true);
  });

  it("rejects every provider without a daemon driver", () => {
    for (const provider of ["antigravity", "claude", "codex", "", undefined]) {
      expect(supportsProviderPresets(provider)).toBe(false);
    }
  });
});

describe("providerPresetsViewState", () => {
  it("reports an unreadable list as an error even when rows are present", () => {
    // Stale rows drive an activate and a delete. Rendering them as usable
    // would let the user act on a preset the machine may no longer have.
    const state = providerPresetsViewState({
      presets: [preset()],
      loading: false,
      error: "daemon did not respond within 30 seconds",
    });
    expect(state.kind).toBe("error");
    expect(canManageProviderPresets(state)).toBe(false);
  });

  it("is loading before the first answer", () => {
    expect(
      providerPresetsViewState({ presets: undefined, loading: true, error: "" }).kind,
    ).toBe("loading");
    expect(
      canManageProviderPresets(
        providerPresetsViewState({ presets: undefined, loading: true, error: "" }),
      ),
    ).toBe(false);
  });

  it("offers the add affordance on an empty but trustworthy list", () => {
    const state = providerPresetsViewState({ presets: [], loading: false, error: "" });
    expect(state.kind).toBe("empty");
    expect(canManageProviderPresets(state)).toBe(true);
  });

  it("is ready with rows", () => {
    const state = providerPresetsViewState({
      presets: [preset()],
      loading: false,
      error: "",
    });
    expect(state).toEqual({ kind: "ready", presets: [preset()] });
    expect(canManageProviderPresets(state)).toBe(true);
  });
});

describe("providerPresetFormFrom", () => {
  it("never carries a credential into the form", () => {
    const form = providerPresetFormFrom(preset());
    expect(form.apiKey).toBe("");
    // The mask is kept as a display fact, strictly apart from the input value:
    // pre-filling it would make the next submit write "sk-…0000" as the key.
    expect(form.keyMask).toBe("sk-…0000");
    expect(form.hasKey).toBe(true);
  });

  it("falls back to the default protocol when the daemon reports an unknown one", () => {
    expect(providerPresetFormFrom(preset({ api: "grpc-whatever" })).api).toBe(
      "openai-completions",
    );
  });

  it("gives a preset with no models one blank row to fill", () => {
    expect(providerPresetFormFrom(preset({ models: [] })).models).toEqual([
      { id: "", name: "" },
    ]);
  });
});

describe("validateProviderPresetForm", () => {
  function valid() {
    return {
      ...emptyProviderPresetForm(),
      id: "my-provider",
      baseUrl: "https://api.example.test/v1",
      models: [{ id: "m1", name: "" }],
    };
  }

  it("accepts a minimal complete form", () => {
    expect(validateProviderPresetForm(valid())).toEqual([]);
  });

  it("requires an id and refuses one that would need YAML quoting", () => {
    expect(validateProviderPresetForm({ ...valid(), id: "  " })).toContain("id_required");
    for (const bad of ["my provider", "a/b", "-lead", "a:b"]) {
      expect(validateProviderPresetForm({ ...valid(), id: bad })).toContain("id_invalid");
    }
  });

  it("requires an http(s) endpoint", () => {
    expect(validateProviderPresetForm({ ...valid(), baseUrl: "" })).toContain(
      "base_url_required",
    );
    // Where the stored key gets sent — a scheme the CLI cannot call is a
    // broken route, and the form is the right place to say so.
    for (const bad of ["ftp://x/y", "file:///etc/passwd", "not a url"]) {
      expect(validateProviderPresetForm({ ...valid(), baseUrl: bad })).toContain(
        "base_url_invalid",
      );
    }
  });

  it("refuses a protocol the daemon driver does not list", () => {
    expect(validateProviderPresetForm({ ...valid(), api: "grpc" })).toContain(
      "api_invalid",
    );
  });

  it("treats a blank env name as valid (the daemon derives one)", () => {
    expect(validateProviderPresetForm({ ...valid(), apiKeyEnv: "" })).toEqual([]);
  });

  it("refuses an env name the shell could not export", () => {
    expect(validateProviderPresetForm({ ...valid(), apiKeyEnv: "9LIVES" })).toContain(
      "api_key_env_invalid",
    );
    expect(validateProviderPresetForm({ ...valid(), apiKeyEnv: "MY KEY" })).toContain(
      "api_key_env_invalid",
    );
  });

  it("requires at least one model, because saving verifies one against the endpoint", () => {
    expect(
      validateProviderPresetForm({ ...valid(), models: [{ id: "  ", name: "x" }] }),
    ).toContain("models_required");
    expect(validateProviderPresetForm({ ...valid(), models: [] })).toContain(
      "models_required",
    );
    expect(
      validateProviderPresetForm({ ...valid(), models: [{ id: "m1", name: "" }] }),
    ).not.toContain("models_required");
  });
});

describe("providerPresetModels", () => {
  it("drops blank scaffolding rows and trims the rest", () => {
    const form = {
      ...emptyProviderPresetForm(),
      models: [
        { id: " m1 ", name: " Model One " },
        { id: "", name: "" },
        { id: "m2", name: "" },
      ],
    };
    expect(providerPresetModels(form)).toEqual([
      { id: "m1", name: "Model One" },
      { id: "m2" },
    ]);
  });
});

describe("providerPresetUpsertInput", () => {
  function form() {
    return {
      ...emptyProviderPresetForm(),
      editingId: "command-code",
      id: "command-code",
      baseUrl: "https://api.example.test/v1",
      hasKey: true,
      keyMask: "sk-…0000",
      models: [{ id: "m1", name: "" }],
    };
  }

  it("omits api_key when the box was left blank, so the stored key survives", () => {
    const input = providerPresetUpsertInput(form());
    expect("api_key" in input).toBe(false);
  });

  it("omits api_key when the box holds only whitespace", () => {
    const input = providerPresetUpsertInput({ ...form(), apiKey: "   " });
    expect("api_key" in input).toBe(false);
  });

  it("never lets the mask reach the wire as a credential", () => {
    // Belt and braces for the invariant this whole section is built around:
    // whatever the mask is, it lives in `keyMask` and nothing copies it into
    // `apiKey`, so a submit made without typing carries no key at all.
    const input = providerPresetUpsertInput(form());
    expect(JSON.stringify(input)).not.toContain("sk-…0000");
  });

  it("sends a typed key, trimmed", () => {
    expect(providerPresetUpsertInput({ ...form(), apiKey: " sk-test-0000 " }).api_key).toBe(
      "sk-test-0000",
    );
  });
});

describe("providerPresetKeyState", () => {
  it("is masked when the daemon reported both the bit and the mask", () => {
    expect(providerPresetKeyState(preset())).toBe("masked");
  });

  it("is stored — not absent — when a key exists without a mask", () => {
    expect(providerPresetKeyState(preset({ key_mask: "" }))).toBe("stored");
  });

  it("is absent only when has_key is strictly false", () => {
    expect(providerPresetKeyState(preset({ has_key: false }))).toBe("absent");
    expect(
      providerPresetKeyState(preset({ has_key: undefined as never, key_mask: "sk-…0" })),
    ).toBe("absent");
  });
});

describe("presentation helpers", () => {
  it("summarises endpoint and protocol, skipping what the daemon omitted", () => {
    expect(providerPresetSummaryLine(preset())).toBe(
      "https://api.example.test/v1 · openai-completions",
    );
    expect(providerPresetSummaryLine(preset({ api: undefined }))).toBe(
      "https://api.example.test/v1",
    );
  });

  it("flags a delete that would leave the machine with no default model", () => {
    expect(deletingActivePreset(preset({ active: true }))).toBe(true);
    expect(deletingActivePreset(preset())).toBe(false);
  });
});

describe("the fetched catalog", () => {
  const catalog = [
    { id: "deepseek/deepseek-v4.1-flash", name: "DeepSeek V4.1 Flash", context_window: 1_000_000 },
    { id: "claude-sonnet-5", context_length: 200_000 },
    { id: "gpt-5.6-sol", name: "GPT-5.6 Sol" },
  ];

  it("filters on both the real id and the display name", () => {
    // 70+ rows is the normal case, so both fields have to be searchable.
    expect(filterProviderPresetModels(catalog, "deepseek").map((m) => m.id)).toEqual([
      "deepseek/deepseek-v4.1-flash",
    ]);
    expect(filterProviderPresetModels(catalog, "SONNET").map((m) => m.id)).toEqual([
      "claude-sonnet-5",
    ]);
    expect(filterProviderPresetModels(catalog, "  ")).toHaveLength(3);
    expect(filterProviderPresetModels(catalog, "nothing")).toEqual([]);
  });

  it("reads either spelling of the context window, and rejects a non-size", () => {
    expect(providerPresetContextWindow(catalog[0]!)).toBe(1_000_000);
    expect(providerPresetContextWindow(catalog[1]!)).toBe(200_000);
    expect(providerPresetContextWindow(catalog[2]!)).toBeNull();
    expect(providerPresetContextWindow({ id: "m", context_window: 0 })).toBeNull();
  });

  it("offers the fetch only with an endpoint and a usable credential", () => {
    const base = {
      ...emptyProviderPresetForm(),
      baseUrl: "https://api.example.test/v1",
    };
    expect(canFetchProviderPresetModels(base)).toBe(false);
    expect(canFetchProviderPresetModels({ ...base, apiKey: "sk-1" })).toBe(true);
    // Editing reuses the stored key: making the user retype it would be a second
    // dead end on top of the one the selector removes.
    expect(
      canFetchProviderPresetModels({
        ...base,
        editingId: "command-code",
        hasKey: true,
      }),
    ).toBe(true);
    expect(canFetchProviderPresetModels({ ...base, apiKey: " " })).toBe(false);
    expect(canFetchProviderPresetModels({ ...base, baseUrl: "" })).toBe(false);
  });
});

describe("failure translation", () => {
  it("carries the daemon's kind and parameters onto the failure", () => {
    const error = Object.assign(new Error("quota"), {
      kind: "rate_limited",
      params: { status: "429", action: "regenerate_key" },
    });
    const failure = providerPresetFailureFrom(error, "fallback");
    expect(failure.kind).toBe("rate_limited");
    expect(failure.params.action).toBe("regenerate_key");
    expect(providerPresetNeedsKeyRegeneration(failure)).toBe(true);
  });

  it("uses the caller's fallback when the error carries no classification", () => {
    expect(providerPresetFailureFrom(new Error(""), "Could not save.")).toEqual({
      kind: "",
      params: {},
      message: "Could not save.",
    });
    expect(providerPresetFailureFrom(undefined, "Could not save.").message).toBe(
      "Could not save.",
    );
    // A kind that is not a string (or params with a non-string value) must not
    // reach the copy switch as if it were one.
    const weird = Object.assign(new Error("boom"), {
      kind: 7,
      params: { reset_at_local: 3 },
    });
    expect(providerPresetFailureFrom(weird, "fallback")).toEqual({
      kind: "",
      params: {},
      message: "boom",
    });
  });

  it("knows the kinds this build has copy for", () => {
    expect(isKnownProviderPresetFailure("rate_limited")).toBe(true);
    expect(isKnownProviderPresetFailure("something_newer")).toBe(false);
    expect(isKnownProviderPresetFailure("")).toBe(false);
  });

  it("derives a clickable dashboard target from the endpoint", () => {
    expect(providerConsoleUrl("https://api.commandcode.ai/provider/v1")).toBe(
      "https://api.commandcode.ai/",
    );
    for (const bad of ["", "not a url", "file:///etc/passwd"]) {
      expect(providerConsoleUrl(bad)).toBeNull();
    }
  });
});

describe("the save state machine", () => {
  const failure = { kind: "rate_limited", params: {}, message: "quota" };

  it("moves idle → verifying → saved on success", () => {
    const verifying = reduceProviderPresetSave(IDLE_PROVIDER_PRESET_SAVE, {
      type: "begin",
    });
    expect(verifying.phase).toBe("verifying");
    expect(reduceProviderPresetSave(verifying, { type: "succeeded" })).toEqual({
      phase: "saved",
      failure: null,
    });
  });

  it("never reports saved when verification failed", () => {
    const verifying = reduceProviderPresetSave(IDLE_PROVIDER_PRESET_SAVE, {
      type: "begin",
    });
    const failed = reduceProviderPresetSave(verifying, {
      type: "failed",
      failure,
    });
    expect(failed.phase).toBe("failed");
    expect(failed.failure).toEqual(failure);
    expect(failed.phase).not.toBe("saved");

    // A late success must not paper over a failure that is still on screen.
    expect(reduceProviderPresetSave(failed, { type: "succeeded" })).toBe(failed);
  });

  it("ignores a result for a run that was never started", () => {
    expect(
      reduceProviderPresetSave(IDLE_PROVIDER_PRESET_SAVE, { type: "succeeded" }),
    ).toBe(IDLE_PROVIDER_PRESET_SAVE);
    expect(
      reduceProviderPresetSave(IDLE_PROVIDER_PRESET_SAVE, {
        type: "failed",
        failure,
      }),
    ).toBe(IDLE_PROVIDER_PRESET_SAVE);
  });

  it("clears the previous failure when a new attempt starts", () => {
    const failed = reduceProviderPresetSave(
      reduceProviderPresetSave(IDLE_PROVIDER_PRESET_SAVE, { type: "begin" }),
      { type: "failed", failure },
    );
    expect(
      reduceProviderPresetSave(failed, { type: "begin" }).failure,
    ).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// Cross-machine sync (DENE-335)
// ---------------------------------------------------------------------------

function runtime(
  overrides: Partial<
    Pick<RuntimeDevice, "id" | "name" | "custom_name" | "provider" | "status">
  > = {},
): Pick<RuntimeDevice, "id" | "name" | "custom_name" | "provider" | "status"> {
  return {
    id: "rt-1",
    name: "MacBook-Pro (dsh)",
    custom_name: null,
    provider: "dsh",
    status: "online",
    ...overrides,
  };
}

function target(
  overrides: Partial<ProviderPresetSyncTarget> = {},
): ProviderPresetSyncTarget {
  return { runtimeId: "rt-2", label: "MacBook-Air-5", online: true, ...overrides };
}

describe("providerPresetSyncTargets", () => {
  it("drops the machine being edited and every CLI without a preset driver", () => {
    const targets = providerPresetSyncTargets(
      [
        runtime({ id: "rt-1" }),
        runtime({ id: "rt-2", name: "Air" }),
        runtime({ id: "rt-3", name: "Codex box", provider: "codex" }),
      ],
      "rt-1",
    );

    expect(targets.map((entry) => entry.runtimeId)).toEqual(["rt-2"]);
  });

  // Offline machines are the drift that matters most — they are still running
  // the old endpoint and nobody is looking at them. Hiding the row would read
  // as "everything is in sync".
  it("keeps offline machines, listed after the online ones", () => {
    const targets = providerPresetSyncTargets(
      [
        runtime({ id: "rt-2", name: "Zulu", status: "offline" }),
        runtime({ id: "rt-3", name: "Yankee" }),
        runtime({ id: "rt-4", name: "Alpha" }),
      ],
      "rt-1",
    );

    expect(targets.map((entry) => [entry.label, entry.online])).toEqual([
      ["Alpha", true],
      ["Yankee", true],
      ["Zulu", false],
    ]);
  });

  it("prefers a user alias over the daemon's name", () => {
    const targets = providerPresetSyncTargets(
      [runtime({ id: "rt-2", custom_name: "Studio" })],
      "rt-1",
    );

    expect(targets[0]?.label).toBe("Studio");
  });
});

describe("providerPresetPeerState", () => {
  const source = preset();

  it("settles an offline machine without waiting on a read", () => {
    const state = providerPresetPeerState({
      target: target({ online: false }),
      presets: undefined,
      loading: true,
      error: "",
      source,
    });

    expect(state.status).toBe("offline");
  });

  // An unreadable machine is not a matching machine. Rendering it as "in sync"
  // would be the exact false assurance this dialog exists to remove.
  it("keeps an unreadable machine apart from a matching one", () => {
    const state = providerPresetPeerState({
      target: target(),
      presets: undefined,
      loading: false,
      error: "daemon did not answer",
      source,
    });

    expect(state.status).toBe("unreadable");
    expect(state.message).toBe("daemon did not answer");
  });

  it("reports a machine that has no copy of the preset", () => {
    const state = providerPresetPeerState({
      target: target(),
      presets: [preset({ id: "other" })],
      loading: false,
      error: "",
      source,
    });

    expect(state.status).toBe("missing");
  });

  it("matches on endpoint and protocol together, and reports the peer's own row", () => {
    const same = providerPresetPeerState({
      target: target(),
      presets: [preset({ key_mask: "sk-…9999" })],
      loading: false,
      error: "",
      source,
    });
    expect(same.status).toBe("match");
    expect(same.preset?.key_mask).toBe("sk-…9999");

    // Same URL, other protocol — a different request, so not a match.
    const protocol = providerPresetPeerState({
      target: target(),
      presets: [preset({ api: "anthropic-messages" })],
      loading: false,
      error: "",
      source,
    });
    expect(protocol.status).toBe("drift");

    const endpoint = providerPresetPeerState({
      target: target(),
      presets: [preset({ base_url: "https://zen.example.test/v1" })],
      loading: false,
      error: "",
      source,
    });
    expect(endpoint.status).toBe("drift");
    expect(endpoint.preset?.base_url).toBe("https://zen.example.test/v1");
  });
});

describe("providerPresetSyncNeedsKey", () => {
  const stateFor = (peer: RuntimeProviderPreset | null, status: "match" | "missing") => ({
    status,
    preset: peer,
    message: "",
  });

  it("requires a typed key when a selected machine has none to keep", () => {
    expect(
      providerPresetSyncNeedsKey([stateFor(null, "missing")], ""),
    ).toBe(true);
    expect(
      providerPresetSyncNeedsKey([stateFor(preset({ has_key: false }), "match")], ""),
    ).toBe(true);
  });

  it("asks for nothing when every selected machine already stores one", () => {
    expect(providerPresetSyncNeedsKey([stateFor(preset(), "match")], "")).toBe(false);
  });

  it("is satisfied by a typed key whatever the machines hold", () => {
    expect(providerPresetSyncNeedsKey([stateFor(null, "missing")], " sk-live ")).toBe(
      false,
    );
  });
});

describe("providerPresetSyncInput", () => {
  it("carries the source route and omits the key unless one was typed", () => {
    expect(providerPresetSyncInput(preset(), "  ")).toEqual({
      id: "command-code",
      api: "openai-completions",
      base_url: "https://api.example.test/v1",
      api_key_env: "COMMAND_CODE_API_KEY",
      models: [{ id: "m1", name: "Model One" }],
    });

    expect(providerPresetSyncInput(preset(), " sk-live ")).toMatchObject({
      api_key: "sk-live",
    });
  });

  // The mask is a display string, never a credential. Nothing on this path may
  // turn `sk-…0000` into the key a machine then authenticates with.
  it("never derives a key from the source's mask", () => {
    expect(providerPresetSyncInput(preset({ key_mask: "sk-…0000" }), "")).not.toHaveProperty(
      "api_key",
    );
  });

  it("falls back to the default protocol when the source reports an unknown one", () => {
    expect(providerPresetSyncInput(preset({ api: "grpc-whatever" }), "").api).toBe(
      "openai-completions",
    );
  });
});
