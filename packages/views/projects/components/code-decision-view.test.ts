import { describe, expect, it } from "vitest";
import { isPlainFolder, viewOfCodeDecision } from "./code-decision-view";

describe("viewOfCodeDecision", () => {
  it("shows the directory the server named, for both in-place and shared", () => {
    expect(
      viewOfCodeDecision({ kind: "local_in_place", display_name: "app", path: "/Users/me/app" }),
    ).toEqual({ kind: "place", name: "app" });
    expect(
      viewOfCodeDecision({ kind: "local_shared", display_name: "workspace" }),
    ).toEqual({ kind: "place", name: "workspace" });
  });

  it("shows the landing folder the server computed for parallel mode", () => {
    expect(
      viewOfCodeDecision({
        kind: "local_worktree",
        display_name: "app",
        path: "/Users/me/app.multica-worktrees/task-preview",
        worktree_root: "/Users/me/app.multica-worktrees",
      }),
    ).toEqual({ kind: "worktree", root: "/Users/me/app.multica-worktrees" });
  });

  it("shows a checkout URL and the shared session folder as the server classified them", () => {
    expect(viewOfCodeDecision({ kind: "remote_cache", url: "https://github.com/o/r" })).toEqual({
      kind: "remote",
      url: "https://github.com/o/r",
    });
    expect(viewOfCodeDecision({ kind: "shared_scratch", path: "sessions/" })).toEqual({
      kind: "scratch",
    });
  });

  it("keeps an unresolvable code verbatim, including one this client has never seen", () => {
    expect(
      viewOfCodeDecision({
        kind: "unresolvable",
        code: "daemon_cannot_run_mode",
        reason: "the runtime did not advertise it",
      }),
    ).toEqual({
      kind: "unresolvable",
      code: "daemon_cannot_run_mode",
      reason: "the runtime did not advertise it",
    });
    expect(viewOfCodeDecision({ kind: "unresolvable", code: "from_a_newer_server" })).toEqual({
      kind: "unresolvable",
      code: "from_a_newer_server",
    });
  });

  it("does not invent a sentence for a kind it does not know", () => {
    expect(viewOfCodeDecision({ kind: "local_overlay", path: "/tmp/guess" })).toEqual({
      kind: "unknown",
      kindName: "local_overlay",
    });
  });
});

describe("isPlainFolder", () => {
  it("is only a folder the machine measured as having no git root", () => {
    expect(isPlainFolder(false, undefined)).toBe(true);
    expect(isPlainFolder(false, "")).toBe(true);
    expect(isPlainFolder(true, undefined)).toBe(false);
    expect(isPlainFolder(undefined, undefined)).toBe(false);
    expect(isPlainFolder(false, "/Users/me/repo")).toBe(false);
  });
});
