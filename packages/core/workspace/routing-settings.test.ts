// @vitest-environment node
//
// Canonical layer for the routing settings contract. The four-state matrix and
// every malformed-payload case live here; the settings section's own suite
// keeps the happy path and the wiring and does not re-run this matrix through
// a DOM mount.
import { describe, expect, it } from "vitest";
import {
  DEFAULT_CONFIDENCE_THRESHOLD,
  DEFAULT_STALE_REVIEW_HOURS,
  parseRoutingSettings,
  normalizeStaleReviewHours,
  normalizeThreshold,
  routingGatewayIsComplete,
  routingIsActive,
  routingState,
  withRoutingSettings,
} from "./routing-settings";

describe("parseRoutingSettings", () => {
  it("reads a complete block", () => {
    expect(
      parseRoutingSettings({
        routing: {
          enabled: true,
          model: "gpt-5.6-luna",
          confidence_threshold: 0.85,
          stale_review_hours: 8,
          base_url: "",
        },
      }),
    ).toEqual({
      enabled: true,
      model: "gpt-5.6-luna",
      confidence_threshold: 0.85,
      stale_review_hours: 8,
      base_url: "",
      policy_prompt: "",
    });
  });

  // Every one of these must read as switched off: a payload the client cannot
  // interpret must never render as enabled.
  it.each([
    ["missing settings", undefined],
    ["null settings", null],
    ["empty settings", {}],
    ["null block", { routing: null }],
    ["block is a string", { routing: "on" }],
    ["block is an array", { routing: [] }],
    ["unrelated settings only", { theme: "dark" }],
  ])("falls back to off for %s", (_label, settings) => {
    const parsed = parseRoutingSettings(
      settings as Record<string, unknown> | null | undefined,
    );
    expect(parsed.enabled).toBe(false);
    expect(routingState(parsed)).toBe("off");
  });

  it("treats a truthy non-boolean enabled as off", () => {
    // Explicit === true, not truthiness: a server field that drifted to a
    // string must not silently switch routing on.
    expect(parseRoutingSettings({ routing: { enabled: "yes" } }).enabled).toBe(false);
    expect(parseRoutingSettings({ routing: { enabled: 1 } }).enabled).toBe(false);
  });

  it("ignores a non-string model", () => {
    expect(parseRoutingSettings({ routing: { enabled: true, model: 42 } }).model).toBe("");
  });
});

describe("normalizeStaleReviewHours", () => {
  // The fallback direction for a threshold that can lead to a status write is
  // "look at fewer tickets, later", never "sweep everything now".
  it.each([0, -3, Number.NaN, Number.POSITIVE_INFINITY, 24 * 365 + 1, "8", null, undefined])(
    "falls back to the default for %s",
    (value) => {
      expect(normalizeStaleReviewHours(value)).toBe(DEFAULT_STALE_REVIEW_HOURS);
    },
  );

  it("keeps a value inside the range", () => {
    expect(normalizeStaleReviewHours(6)).toBe(6);
    expect(normalizeStaleReviewHours(0.5)).toBe(0.5);
  });

  it("defaults a block that predates the field", () => {
    // Every workspace configured before DENE-712 has no stale_review_hours,
    // and must read as the default rather than as zero.
    expect(
      parseRoutingSettings({ routing: { enabled: true, model: "m" } }).stale_review_hours,
    ).toBe(DEFAULT_STALE_REVIEW_HOURS);
  });
});

describe("normalizeThreshold", () => {
  it.each([0, -1, 1.5, Number.NaN, Number.POSITIVE_INFINITY, "0.8", null, undefined])(
    "falls back to the default for %s",
    (value) => {
      expect(normalizeThreshold(value)).toBe(DEFAULT_CONFIDENCE_THRESHOLD);
    },
  );

  it("keeps a value inside the range", () => {
    expect(normalizeThreshold(0.5)).toBe(0.5);
    expect(normalizeThreshold(1)).toBe(1);
  });
});

describe("routingState", () => {
  it("is off while the switch is off, whatever else is set", () => {
    expect(
      routingState({ enabled: false, model: "m", confidence_threshold: 0.7, stale_review_hours: 24, base_url: "" }),
    ).toBe("off");
  });

  it("is incomplete when the switch is on with no model", () => {
    // The state that exists because the product must not look enabled when it
    // is doing nothing.
    expect(
      routingState({ enabled: true, model: "", confidence_threshold: 0.7, stale_review_hours: 24, base_url: "" }),
    ).toBe("incomplete");
    expect(
      routingState({ enabled: true, model: "   ", confidence_threshold: 0.7, stale_review_hours: 24, base_url: "" }),
    ).toBe("incomplete");
  });

  it("is enabled when the switch is on and a model is chosen", () => {
    expect(
      routingState({ enabled: true, model: "m", confidence_threshold: 0.7, stale_review_hours: 24, base_url: "" }),
    ).toBe("enabled");
  });

  it("is ineffective only when the server says ineffective in so many words", () => {
    const configured = { enabled: true, model: "m", confidence_threshold: 0.7, stale_review_hours: 24, base_url: "" };
    expect(routingState(configured, { state: "ineffective" })).toBe("ineffective");
    expect(routingState(configured, { state: "enabled" })).toBe("enabled");
    expect(routingState(configured, null)).toBe("enabled");
  });

  // The cases that used to be misread as a broken model. Each of these is a
  // health report whose `usable` is false for a reason that has nothing to do
  // with the model: two describe settings this client can already see are
  // newer, and one is the fallback for a response it could not read at all.
  it.each([
    ["health still describing the switched-off workspace", "off"],
    ["health still describing the workspace before a model was typed", "incomplete"],
    ["the fallback used when the response cannot be read", "off"],
  ])("does not report a fault for %s", (_label, state) => {
    expect(
      routingState({ enabled: true, model: "m", confidence_threshold: 0.7, stale_review_hours: 24, base_url: "" }, { state }),
    ).toBe("enabled");
  });

  it("ignores a state name this client does not know", () => {
    // Forward compatibility: a newer server growing a fifth state must not
    // light the red chip on a guess.
    expect(
      routingState(
        { enabled: true, model: "m", confidence_threshold: 0.7, stale_review_hours: 24, base_url: "" },
        { state: "degraded" },
      ),
    ).toBe("enabled");
  });

  it("treats only enabled as active", () => {
    expect(routingIsActive("enabled")).toBe(true);
    for (const state of ["off", "incomplete", "ineffective"] as const) {
      expect(routingIsActive(state)).toBe(false);
    }
  });
});

describe("withRoutingSettings", () => {
  it("carries the rest of the settings column through untouched", () => {
    expect(
      withRoutingSettings(
        { theme: "dark", other: { a: 1 } },
        { enabled: true, model: " m ", confidence_threshold: 0.9, stale_review_hours: 24, base_url: "" },
      ),
    ).toEqual({
      theme: "dark",
      other: { a: 1 },
      routing: { enabled: true, model: "m", confidence_threshold: 0.9, stale_review_hours: 24, base_url: "", policy_prompt: "" },
    });
  });

  // DENE-706: the project -> direction table lives in the same block and is
  // written by the CLI. A save from this form must not erase it.
  it("carries routing fields this form does not own through a save", () => {
    const out = withRoutingSettings(
      { routing: { enabled: false, model: "old", projects: { tarot: "出海" }, future: 1 } },
      { enabled: true, model: "m", confidence_threshold: 0.7, stale_review_hours: 24, base_url: "" },
    );
    expect(out.routing).toEqual({
      enabled: true,
      model: "m",
      confidence_threshold: 0.7,
      stale_review_hours: 24,
      base_url: "",
      policy_prompt: "",
      projects: { tarot: "出海" },
      future: 1,
    });
  });

  // The key is write-only and lives outside RoutingSettings: the three cases
  // are what keeps an unrelated settings save from deleting a stored key.
  it("omits the key field entirely when no key was typed", () => {
    const out = withRoutingSettings(null, {
      enabled: true,
      model: "m",
      confidence_threshold: 0.7,
      stale_review_hours: 24,
      base_url: "https://gw.example/v1",
    });
    expect("api_key" in (out.routing as Record<string, unknown>)).toBe(false);
  });

  it("sends an empty key only when one was explicitly passed", () => {
    const cleared = withRoutingSettings(
      null,
      { enabled: true, model: "m", confidence_threshold: 0.7, stale_review_hours: 24, base_url: "" },
      "",
    );
    expect((cleared.routing as Record<string, unknown>).api_key).toBe("");
    const set = withRoutingSettings(
      null,
      { enabled: true, model: "m", confidence_threshold: 0.7, stale_review_hours: 24, base_url: "" },
      "  sk-live  ",
    );
    expect((set.routing as Record<string, unknown>).api_key).toBe("sk-live");
  });

  it("normalizes an out-of-range threshold before it is stored", () => {
    const out = withRoutingSettings(null, {
      enabled: true,
      model: "m",
      confidence_threshold: 9,
      stale_review_hours: 0,
      base_url: "",
    });
    expect((out.routing as { confidence_threshold: number }).confidence_threshold).toBe(
      DEFAULT_CONFIDENCE_THRESHOLD,
    );
    expect((out.routing as { stale_review_hours: number }).stale_review_hours).toBe(
      DEFAULT_STALE_REVIEW_HOURS,
    );
  });

  it("treats a half-filled endpoint pair as incomplete", () => {
    expect(routingGatewayIsComplete("https://gw.example/v1", true)).toBe(true);
    expect(routingGatewayIsComplete("https://gw.example/v1", false)).toBe(false);
    expect(routingGatewayIsComplete("", true)).toBe(false);
    // A key typed but not yet saved still counts: the button that saves it
    // and the note that warns about a half-filled pair must not disagree.
    expect(routingGatewayIsComplete("https://gw.example/v1", false, "sk-new")).toBe(true);
    // An explicit clear un-completes the pair even with one stored.
    expect(routingGatewayIsComplete("https://gw.example/v1", true, "")).toBe(false);
  });

  it("round-trips through parse", () => {
    const next = { enabled: true, model: "m", confidence_threshold: 0.42, stale_review_hours: 24, base_url: "", policy_prompt: "" };
    expect(parseRoutingSettings(withRoutingSettings({}, next))).toEqual(next);
  });
});
