// @vitest-environment node

// Canonical suite for the providers section's rules. The component test
// (`agent-provider-presets-section.test.tsx`) keeps the happy path and the
// wiring and does not re-run this matrix through a DOM mount.

import { describe, expect, it } from "vitest";
import type { RuntimeProviderPreset } from "@multica/core/types";
import {
  IDLE_PROVIDER_PRESET_SAVE,
  canFetchProviderPresetModels,
  canManageProviderPresets,
  deletingActivePreset,
  emptyProviderPresetForm,
  filterProviderPresetModels,
  isKnownProviderPresetFailure,
  parseProviderSeatModelString,
  providerConsoleUrl,
  providerPresetContextWindow,
  providerPresetFailureFrom,
  providerPresetFormFrom,
  providerPresetKeyState,
  providerPresetModelLabel,
  providerPresetModels,
  providerPresetNeedsKeyRegeneration,
  providerPresetSummaryLine,
  providerPresetUpsertInput,
  providerPresetsViewState,
  providerSeatModelDisplay,
  providerSeatModelString,
  reduceProviderPresetSave,
  supportsProviderPresets,
  validateProviderPresetForm,
} from "./provider-presets-model";

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

  it("labels a row by name and falls back to the id", () => {
    expect(providerPresetModelLabel(catalog[0]!)).toBe("DeepSeek V4.1 Flash");
    expect(providerPresetModelLabel(catalog[1]!)).toBe("claude-sonnet-5");
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

describe("seat model strings", () => {
  const rawId = "deepseek/deepseek-v4.1-flash";
  const presetId = "command-code2";
  const encoded = "command-code2/deepseek%2Fdeepseek-v4.1-flash";

  it("generates, parses and regenerates the same string for a slash-bearing id", () => {
    const generated = providerSeatModelString(presetId, rawId);
    expect(generated).toBe(encoded);

    const parsed = parseProviderSeatModelString(generated);
    expect(parsed).toEqual({ providerId: presetId, modelId: rawId });

    // 回填: writing the parsed pair back out reproduces the same seat string, so
    // what the user selected and what the seat runs are the same thing.
    expect(providerSeatModelString(parsed!.providerId, parsed!.modelId)).toBe(
      generated,
    );
  });

  it("leaves an id without a slash readable, and encodes only what it must", () => {
    expect(providerSeatModelString("command-code2", "claude-sonnet-5")).toBe(
      "command-code2/claude-sonnet-5",
    );
    expect(providerSeatModelString("command-code2", "a b/c")).toBe(
      "command-code2/a%20b%2Fc",
    );
    expect(providerSeatModelString("", rawId)).toBe("");
    expect(providerSeatModelString(presetId, "  ")).toBe("");
  });

  it("refuses a value that cannot be a seat pair", () => {
    for (const bad of ["", "no-slash", "/model", "provider/", "   "]) {
      expect(parseProviderSeatModelString(bad)).toBeNull();
    }
  });

  it("shows provider and model name, never the escape", () => {
    const display = providerSeatModelDisplay(encoded, [
      preset({ id: presetId, models: [{ id: rawId, name: "DeepSeek V4.1 Flash" }] }),
    ]);
    expect(display).toBe("command-code2 · DeepSeek V4.1 Flash");
    // The regression this rule exists for: rendering `%2F` invites a hand-edit
    // that drops the prefix.
    expect(display).not.toContain("%2F");
  });

  it("falls back to the parsed pieces when the preset is not in hand", () => {
    expect(providerSeatModelDisplay(encoded, [])).toBe(
      `command-code2 · ${rawId}`,
    );
    expect(providerSeatModelDisplay("plain-model", [])).toBe("plain-model");
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
