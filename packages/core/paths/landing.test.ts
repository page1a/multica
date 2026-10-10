import { afterEach, describe, expect, it, vi } from "vitest";
import { paths } from "./paths";
import { PHONE_MEDIA_QUERY, isPhoneViewport, workspaceLandingPath } from "./landing";
import { resolvePostAuthDestination } from "./resolve";
import type { Workspace } from "../types";

function stubMatchMedia(matching: string | null) {
  vi.stubGlobal("matchMedia", (query: string) => ({ matches: query === matching }));
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("workspace landing", () => {
  it("is Chat on a phone and Issues elsewhere", () => {
    expect(workspaceLandingPath("acme", true)).toBe(paths.workspace("acme").chat());
    expect(workspaceLandingPath("acme", false)).toBe(paths.workspace("acme").issues());
  });

  it("asks the browser for narrow and coarse together", () => {
    stubMatchMedia(PHONE_MEDIA_QUERY);
    expect(isPhoneViewport()).toBe(true);
    expect(workspaceLandingPath("acme")).toBe(paths.workspace("acme").chat());

    stubMatchMedia("(max-width: 767px)");
    expect(isPhoneViewport()).toBe(false);
    expect(workspaceLandingPath("acme")).toBe(paths.workspace("acme").issues());
  });

  it("is not a phone where there is no matchMedia", () => {
    vi.stubGlobal("matchMedia", undefined);
    expect(isPhoneViewport()).toBe(false);
  });

  it("sends a signed-in phone to its first workspace's Chat", () => {
    stubMatchMedia(PHONE_MEDIA_QUERY);
    const ws = { slug: "acme" } as Workspace;
    expect(resolvePostAuthDestination([ws], true)).toBe(paths.workspace("acme").chat());
  });
});
