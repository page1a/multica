// @vitest-environment node

import { describe, it, expect } from "vitest";
import {
  isAbsolutePath,
  isInsideRepo,
  worktreeRootForSave,
  worktreeRootProblem,
} from "./worktree-root";

// The canonical matrix for the worktree-root field. Its counterpart on the
// daemon is execenv.ResolveWorktreeRoot, which is what actually enforces the
// rule; this exists so the user hears about a bad path while the dialog is
// still open (DENE-617).

describe("worktree root", () => {
  it("accepts the absolute forms the three platforms use", () => {
    for (const path of ["/Users/me/copies", "C:\\copies", "c:/copies", "\\\\server\\share"]) {
      expect(isAbsolutePath(path), path).toBe(true);
    }
    for (const path of ["copies", "./copies", "~/copies", "", "   "]) {
      expect(isAbsolutePath(path), path).toBe(false);
    }
  });

  // Segment-wise, not string prefix: /repo-backup starts with /repo as text
  // while being a different directory, and refusing it would block a good
  // choice.
  it("compares containment by path segment, not by string prefix", () => {
    expect(isInsideRepo("/Users/me/repo", "/Users/me/repo")).toBe(true);
    expect(isInsideRepo("/Users/me/repo/.worktrees", "/Users/me/repo")).toBe(true);
    expect(isInsideRepo("/Users/me/repo-backup", "/Users/me/repo")).toBe(false);
    expect(isInsideRepo("/Users/me/repo.multica-worktrees", "/Users/me/repo")).toBe(false);
    expect(isInsideRepo("/Users/me/elsewhere", "/Users/me/repo")).toBe(false);
    // Trailing slashes are spelling, not meaning.
    expect(isInsideRepo("/Users/me/repo/", "/Users/me/repo")).toBe(true);
  });

  it("reports the problem with a typed root, and nothing for a good one", () => {
    expect(worktreeRootProblem("", "/Users/me/repo")).toBeUndefined();
    expect(worktreeRootProblem("/Users/me/copies", "/Users/me/repo")).toBeUndefined();
    expect(worktreeRootProblem("copies", "/Users/me/repo")).toBe("not_absolute");
    expect(worktreeRootProblem("/Users/me/repo/copies", "/Users/me/repo")).toBe("inside_repo");
    // Unknown repository root: check what can be checked, defer the rest to
    // the daemon rather than guessing.
    expect(worktreeRootProblem("/Users/me/anything", undefined)).toBeUndefined();
    expect(worktreeRootProblem("relative", undefined)).toBe("not_absolute");
  });

  it("refuses a root that overlaps another bound directory", () => {
    expect(
      worktreeRootProblem("/Users/me/code/app", undefined, ["/Users/me/code/app"]),
    ).toBe("conflicts_with_binding");
    expect(
      worktreeRootProblem("/Users/me/code/app/copies", undefined, [
        "/Users/me/code/app",
      ]),
    ).toBe("conflicts_with_binding");
    expect(
      worktreeRootProblem("/Users/me/copies", undefined, ["/Users/me/code/app"]),
    ).toBeUndefined();
  });

  // The default is stored as ABSENT, not as its own literal path: "beside the
  // repository" follows a repository the user moves, while a stored literal
  // would keep pointing at where it used to be.
  it("stores the default as absent and a chosen location as itself", () => {
    const def = "/Users/me/repo.multica-worktrees";
    expect(worktreeRootForSave("", def)).toBeUndefined();
    expect(worktreeRootForSave("   ", def)).toBeUndefined();
    expect(worktreeRootForSave(def, def)).toBeUndefined();
    expect(worktreeRootForSave(def + "/", def)).toBeUndefined();
    expect(worktreeRootForSave("/Volumes/Fast/copies", def)).toBe("/Volumes/Fast/copies");
    // No default known: anything the user typed is a choice.
    expect(worktreeRootForSave("/Volumes/Fast/copies", undefined)).toBe("/Volumes/Fast/copies");
  });
});
