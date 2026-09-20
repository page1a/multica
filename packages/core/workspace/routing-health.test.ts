// @vitest-environment node
import { describe, expect, it } from "vitest";

import {
  normalizeRoutingState,
  parseRoutingHealth,
  UNKNOWN_ROUTING_HEALTH,
} from "./routing-health";

describe("parseRoutingHealth", () => {
  it("reads a well-formed report", () => {
    expect(
      parseRoutingHealth({
        state: "ineffective",
        usable: false,
        reason: "the routing model rejected our credentials (401)",
        retry_after_seconds: 240,
        last_success_at: 1_700_000_000,
        last_failure_at: 1_700_000_300,
        model: "gpt-5.6-luna",
        threshold: 0.8,
      }),
    ).toEqual({
      state: "ineffective",
      usable: false,
      reason: "the routing model rejected our credentials (401)",
      retry_after_seconds: 240,
      last_success_at: 1_700_000_000,
      last_failure_at: 1_700_000_300,
      model: "gpt-5.6-luna",
      threshold: 0.8,
      gateway_host: "",
      gateway_default_model: "",
      gateway_configured: true,
      // A backend that predates the workspace gateway omits all three; the
      // defaults have to read as "the deployment endpoint, no workspace key",
      // which is what such a backend means.
      gateway_scope: "deployment",
      gateway_protocol: "openai",
      gateway_key_set: false,
      workspace_key_storable: false,
    });
  });

  it("narrows an unrecognised gateway protocol to openai", () => {
    // Every endpoint that existed before this field was OpenAI-compatible, so
    // an older backend omitting it means "openai". Claiming System One off an
    // unrecognised value would tell a reader their tickets are judged by Jev
    // when they are not.
    expect(
      parseRoutingHealth({ state: "enabled", gateway_protocol: "anthropic" })
        .gateway_protocol,
    ).toBe("openai");
    expect(parseRoutingHealth({ state: "enabled" }).gateway_protocol).toBe(
      "openai",
    );
    expect(
      parseRoutingHealth({ state: "enabled", gateway_protocol: "systemone" })
        .gateway_protocol,
    ).toBe("systemone");
  });

  it("narrows an unrecognised gateway scope to the deployment", () => {
    expect(
      parseRoutingHealth({ state: "enabled", gateway_scope: "tenant" })
        .gateway_scope,
    ).toBe("deployment");
    expect(
      parseRoutingHealth({ state: "enabled", gateway_scope: "workspace" })
        .gateway_scope,
    ).toBe("workspace");
  });

  it("never reads a non-boolean key flag as a stored key", () => {
    // `=== true`, not truthy: a server that starts sending this as a string
    // must not put a green "a key is saved" over a workspace that has none.
    expect(
      parseRoutingHealth({ state: "enabled", gateway_key_set: "yes" })
        .gateway_key_set,
    ).toBe(false);
  });

  it("reports the gateway the model id is actually sent to", () => {
    const health = parseRoutingHealth({
      state: "enabled",
      usable: true,
      model: "gpt-5.6-luna",
      gateway_host: "api.openai.com",
      gateway_default_model: "gpt-5.6-mini",
      gateway_configured: true,
    });
    expect(health.gateway_host).toBe("api.openai.com");
    expect(health.gateway_default_model).toBe("gpt-5.6-mini");
    expect(health.gateway_configured).toBe(true);
  });

  it("only calls the deployment unconfigured when the server says so", () => {
    // Absent is not "no LLM": an older backend omits the field entirely, and
    // reading that as unconfigured would put a red warning under a section
    // that works. Only an explicit false counts.
    expect(parseRoutingHealth({ state: "enabled" }).gateway_configured).toBe(
      true,
    );
    expect(
      parseRoutingHealth({ state: "enabled", gateway_configured: false })
        .gateway_configured,
    ).toBe(false);
  });

  // The malformed-response matrix. Every one of these must degrade to "we do
  // not know", never to a green chip: this endpoint is the only place a broken
  // routing model is visible, so a client that guesses "enabled" from a reply
  // it could not read would hide exactly the failure it exists to show.
  it.each([
    ["null", null],
    ["undefined", undefined],
    ["a string", "enabled"],
    ["an array", [{ state: "enabled" }]],
    ["a missing state", { usable: true }],
    ["a numeric state", { state: 1, usable: true }],
  ])("falls back on %s", (_label, raw) => {
    expect(parseRoutingHealth(raw)).toEqual(UNKNOWN_ROUTING_HEALTH);
  });

  it("does not read a truthy non-boolean as usable", () => {
    // A backend that starts sending "true" as a string must not light the
    // green chip. `=== true`, per the API compatibility rules.
    const health = parseRoutingHealth({ state: "enabled", usable: "true" });
    expect(health.usable).toBe(false);
  });

  it("treats a state this build does not know as off rather than enabled", () => {
    // An installed desktop build against a newer backend. Unknown is not a
    // licence to claim routing is working.
    const health = parseRoutingHealth({ state: "degraded", usable: true });
    expect(health.state).toBe("off");
  });

  it("drops nonsense timestamps instead of rendering them", () => {
    const health = parseRoutingHealth({
      state: "enabled",
      usable: true,
      last_success_at: -5,
      retry_after_seconds: Number.NaN,
    });
    expect(health.last_success_at).toBe(0);
    expect(health.retry_after_seconds).toBe(0);
  });
});

describe("normalizeRoutingState", () => {
  it("passes the four known states through", () => {
    for (const s of ["off", "incomplete", "enabled", "ineffective"] as const) {
      expect(normalizeRoutingState(s)).toBe(s);
    }
  });
});
