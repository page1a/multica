// @vitest-environment node
import { delimiter, join } from "path";
import { describe, expect, it } from "vitest";

import { applyFallbackPathDirs, fallbackPathDirs } from "./path-fallback";

describe("fallbackPathDirs", () => {
  it("prepends only the user-local install dir", () => {
    // DENE-507: a Homebrew/npm global copy in /opt/homebrew/bin used to shadow
    // the newer standalone install in ~/.local/bin, because the daemon
    // resolves agent CLIs with the first PATH match.
    expect(fallbackPathDirs("/home/u")).toEqual([
      join("/home/u", ".local/bin"),
    ]);
  });
});

describe("applyFallbackPathDirs", () => {
  it("puts the user-local dir ahead of the machine-wide ones", () => {
    // DENE-507, read off the only ordering the daemon actually sees: a
    // standalone install in ~/.local/bin must win over Homebrew's older copy.
    const result = applyFallbackPathDirs(
      "/usr/bin:/bin:/opt/homebrew/bin",
      "/home/u",
    );
    expect(result.split(delimiter)).toEqual([
      join("/home/u", ".local/bin"),
      "/usr/bin",
      "/bin",
      "/opt/homebrew/bin",
      "/usr/local/bin",
    ]);
  });

  it("never prepends a machine-wide dir — recovered nvm Node stays ahead of /usr/local/bin", () => {
    // MUL-7312: prepending /usr/local/bin over a recovered login PATH shadows
    // nvm/fnm Node with a stale system binary, which breaks shebang CLIs
    // (`#!/usr/bin/env node`) during daemon --version probes.
    const result = applyFallbackPathDirs(
      "/Users/me/.nvm/versions/node/v22.0.0/bin:/usr/bin",
      "/Users/me",
    );
    expect(result.split(delimiter)).toEqual([
      join("/Users/me", ".local/bin"),
      "/Users/me/.nvm/versions/node/v22.0.0/bin",
      "/usr/bin",
      "/opt/homebrew/bin",
      "/usr/local/bin",
    ]);
  });

  it("does not repeat a machine-wide dir the login shell already provided", () => {
    const current = [
      "/Users/me/.nvm/versions/node/v22.0.0/bin",
      "/opt/homebrew/bin",
      "/usr/local/bin",
      "/usr/bin",
    ].join(delimiter);
    const result = applyFallbackPathDirs(current, "/Users/me");
    expect(result.split(delimiter)).toEqual([
      join("/Users/me", ".local/bin"),
      "/Users/me/.nvm/versions/node/v22.0.0/bin",
      "/opt/homebrew/bin",
      "/usr/local/bin",
      "/usr/bin",
    ]);
  });

  it("returns just the fallbacks for an unset or empty PATH, with no empty segment", () => {
    const expected = [
      join("/home/u", ".local/bin"),
      "/opt/homebrew/bin",
      "/usr/local/bin",
    ].join(delimiter);
    expect(applyFallbackPathDirs(undefined, "/home/u")).toBe(expected);
    expect(applyFallbackPathDirs("", "/home/u")).toBe(expected);
  });
});
