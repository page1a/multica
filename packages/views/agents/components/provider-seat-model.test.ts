// @vitest-environment node

// Canonical suite for the seat model string's codec and its display form. Every
// surface that shows a seat's model — the provider-presets panel and both
// seat-side model pickers — renders through this module, so the "no `%2F` on
// screen" rule is proven here once rather than re-run through each DOM mount.

import { describe, expect, it } from "vitest";
import type {
  RuntimeProviderPreset,
  RuntimeProviderPresetModel,
} from "@multica/core/types";
import {
  parseProviderSeatModelString,
  providerPresetModelLabel,
  providerSeatModelDisplay,
  providerSeatModelString,
  seatModelIsExactly,
} from "./provider-seat-model";

function preset(overrides: Partial<RuntimeProviderPreset> = {}): RuntimeProviderPreset {
  return {
    id: "command-code",
    api: "openai-completions",
    base_url: "https://api.example.test/v1",
    has_key: true,
    models: [{ id: "claude-sonnet-5" }],
    ...overrides,
  };
}

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

  // The seat-side model pickers render catalog rows straight from the runtime's
  // model list and fetch no preset list, so they call this with the presets
  // omitted. That path still has to decode: the option they show for a DSH
  // gateway is `deepseek-official/deepseek-v4%2Fflash`, and a user searching
  // for the model they picked types `deepseek/deepseek-v4.1-flash` (DENE-684).
  it("decodes a catalog row with no preset list to hand", () => {
    const catalogId = "command-code2/deepseek%2Fdeepseek-v4.1-flash";
    expect(providerSeatModelDisplay(catalogId)).toBe(`command-code2 · ${rawId}`);
    expect(providerSeatModelDisplay(catalogId)).not.toContain("%2F");
    // A runtime whose ids carry no provider prefix is untouched by the rule.
    expect(providerSeatModelDisplay("claude-fable-5")).toBe("claude-fable-5");
  });
});

describe("providerPresetModelLabel", () => {
  it("labels a row by name and falls back to the id", () => {
    const catalog: RuntimeProviderPresetModel[] = [
      { id: "deepseek/deepseek-v4.1-flash", name: "DeepSeek V4.1 Flash" },
      { id: "claude-sonnet-5" },
      { id: "gpt-5.6-sol", name: "  " },
    ];
    expect(providerPresetModelLabel(catalog[0]!)).toBe("DeepSeek V4.1 Flash");
    expect(providerPresetModelLabel(catalog[1]!)).toBe("claude-sonnet-5");
    expect(providerPresetModelLabel(catalog[2]!)).toBe("gpt-5.6-sol");
  });
});

describe("seatModelIsExactly", () => {
  const encoded = "command-code2/deepseek%2Fdeepseek-v4.1-flash";

  it("recognises the decoded model id a user types", () => {
    expect(seatModelIsExactly(encoded, "deepseek/deepseek-v4.1-flash")).toBe(true);
    expect(seatModelIsExactly(encoded, " deepseek/deepseek-v4.1-flash ")).toBe(true);
  });

  it("recognises the stored string and the displayed pair", () => {
    expect(seatModelIsExactly(encoded, encoded)).toBe(true);
    expect(
      seatModelIsExactly(encoded, "command-code2 · deepseek/deepseek-v4.1-flash"),
    ).toBe(true);
  });

  it("does not match a different model or an empty search", () => {
    expect(seatModelIsExactly(encoded, "deepseek/deepseek-v4.1")).toBe(false);
    expect(seatModelIsExactly(encoded, "  ")).toBe(false);
    expect(seatModelIsExactly("claude-opus-5", "claude-opus-5")).toBe(true);
    expect(seatModelIsExactly("claude-opus-5", "claude-opus")).toBe(false);
  });
});
