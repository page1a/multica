import { mkdir, mkdtemp, rm, stat, writeFile } from "fs/promises";
import { tmpdir } from "os";
import { join } from "path";
import { afterEach, describe, expect, it } from "vitest";
import { provisionProjectDirectory, removeProvisionedDirectory } from "./provision-project-dir";

const roots: string[] = [];

afterEach(async () => {
  await Promise.all(roots.splice(0).map((dir) => rm(dir, { recursive: true, force: true })));
});

async function root(): Promise<string> {
  const dir = await mkdtemp(join(tmpdir(), "proj-dir-"));
  roots.push(dir);
  return dir;
}

describe("provisionProjectDirectory", () => {
  it("creates a subdirectory and an initial commit", async () => {
    const base = await root();
    const result = await provisionProjectDirectory({
      root: base,
      dirName: "通力电梯",
      gitInit: true,
    });
    expect(result.ok).toBe(true);
    const git = await stat(join(base, "通力电梯", ".git"));
    expect(git.isDirectory()).toBe(true);
  });

  it("leaves an existing path alone instead of replacing it", async () => {
    const base = await root();
    // A file where the subdirectory should be makes mkdir fail before git,
    // which is the "exists" path and must not delete the file.
    await writeFile(join(base, "taken"), "keep");
    const blocked = await provisionProjectDirectory({
      root: base,
      dirName: "taken",
      gitInit: true,
    });
    expect(blocked).toMatchObject({ ok: false, reason: "exists" });
    expect((await stat(join(base, "taken"))).isFile()).toBe(true);
  });

  it("refuses a name that is not a single segment", async () => {
    const base = await root();
    const result = await provisionProjectDirectory({
      root: base,
      dirName: "../outside",
      gitInit: false,
    });
    expect(result.ok).toBe(false);
  });
});

describe("removeProvisionedDirectory", () => {
  it("removes only a direct child of the root", async () => {
    const base = await root();
    const created = await provisionProjectDirectory({
      root: base,
      dirName: "child",
      gitInit: false,
    });
    expect(created.ok).toBe(true);
    const removed = await removeProvisionedDirectory({ root: base, path: created.path! });
    expect(removed.ok).toBe(true);
    await expect(stat(created.path!)).rejects.toThrow();

    await mkdir(join(base, "nested", "inner"), { recursive: true });
    const refused = await removeProvisionedDirectory({
      root: base,
      path: join(base, "nested", "inner"),
    });
    expect(refused.ok).toBe(false);
    expect((await stat(join(base, "nested", "inner"))).isDirectory()).toBe(true);

    const rootItself = await removeProvisionedDirectory({ root: base, path: base });
    expect(rootItself.ok).toBe(false);
  });
});
