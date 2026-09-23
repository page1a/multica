// @vitest-environment node

import { mkdir, mkdtemp, rm, writeFile } from "fs/promises";
import { tmpdir } from "os";
import { join } from "path";
import { afterEach, describe, expect, it } from "vitest";
import { execFile } from "child_process";
import { promisify } from "util";
import { initLocalGit } from "./local-git";

const run = promisify(execFile);
const dirs: string[] = [];

afterEach(async () => {
  await Promise.all(dirs.splice(0).map((dir) => rm(dir, { recursive: true, force: true })));
});

async function tempDir(): Promise<string> {
  const dir = await mkdtemp(join(tmpdir(), "multica-local-git-"));
  dirs.push(dir);
  return dir;
}

describe("initLocalGit", () => {
  it("makes a plain folder a repository with a commit, and does not add a remote", async () => {
    const dir = await tempDir();
    await writeFile(join(dir, "notes.txt"), "hello\n");

    const result = await initLocalGit(dir);
    expect(result).toEqual({ ok: true });

    const { stdout: head } = await run("git", ["-C", dir, "rev-parse", "--verify", "HEAD"]);
    expect(head.trim()).not.toBe("");
    await expect(run("git", ["-C", dir, "remote"])).resolves.toMatchObject({ stdout: "" });
    // The file that was already there stays untracked. Creating the
    // repository is not permission to snapshot the folder.
    const { stdout: status } = await run("git", ["-C", dir, "status", "--porcelain"]);
    expect(status).toContain("notes.txt");
  });

  it("refuses a folder that is already inside another repository", async () => {
    const outer = await tempDir();
    await run("git", ["init", "--", outer]);
    await run("git", [
      "-C",
      outer,
      "-c",
      "user.name=Test",
      "-c",
      "user.email=test@localhost",
      "commit",
      "--allow-empty",
      "-m",
      "outer",
    ]);
    const inner = join(outer, "nested");
    await mkdir(inner);

    const result = await initLocalGit(inner);
    expect(result.ok).toBe(false);
    expect(result.reason).toBe("inside_repo");
  });

  it("refuses a path that is not a directory", async () => {
    expect(await initLocalGit("notes")).toEqual({ ok: false, reason: "not_absolute" });
  });
});
