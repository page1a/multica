import { mkdir, rm, realpath, stat } from "fs/promises";
import { isAbsolute, join, relative, sep } from "path";
import { initLocalGit } from "./local-git";

export type ProvisionProjectDirectoryResult = {
  ok: boolean;
  path?: string;
  reason?: "not_absolute" | "bad_name" | "exists" | "error";
  error?: string;
};

/**
 * Create one subdirectory of a business-project root, and optionally make it
 * a git repository with an empty first commit.
 *
 * The root is created if it is missing. An existing subdirectory is left
 * untouched and reported as `exists` — this never deletes a folder it did not
 * just create. If git init fails, the subdirectory this call created is
 * removed before the error returns.
 */
export async function provisionProjectDirectory(input: {
  root: string;
  dirName: string;
  gitInit: boolean;
}): Promise<ProvisionProjectDirectoryResult> {
  const root = input.root.trim();
  const dirName = input.dirName.trim();
  if (!root || !isAbsolute(root) || !dirName || !isAbsoluteSafeName(dirName)) {
    return { ok: false, reason: !dirName || !isAbsoluteSafeName(dirName) ? "bad_name" : "not_absolute" };
  }
  if (!isAbsolute(root)) return { ok: false, reason: "not_absolute" };

  const target = join(root, dirName);
  try {
    await mkdir(root, { recursive: true });
    await mkdir(target);
  } catch (err) {
    if (isAlreadyExists(err)) return { ok: false, reason: "exists" };
    return { ok: false, reason: "error", error: errorMessage(err) };
  }

  if (!input.gitInit) return { ok: true, path: target };

  const git = await initLocalGit(target);
  if (!git.ok) {
    await rm(target, { recursive: true, force: true }).catch(() => undefined);
    return { ok: false, reason: "error", error: git.error ?? git.reason };
  }
  return { ok: true, path: target };
}

/**
 * Remove a subdirectory this flow created. Refuses anything that is not a
 * direct child of the root, so a failure can never delete the root itself
 * or a path outside it.
 */
export async function removeProvisionedDirectory(input: {
  root: string;
  path: string;
}): Promise<{ ok: boolean; reason?: string }> {
  const root = input.root.trim();
  const path = input.path.trim();
  if (!isAbsolute(root) || !isAbsolute(path)) return { ok: false, reason: "not_absolute" };
  let rootReal = root;
  let pathReal = path;
  try {
    rootReal = await realpath(root);
    const st = await stat(path);
    if (!st.isDirectory()) return { ok: false, reason: "not_a_directory" };
    pathReal = await realpath(path);
  } catch (err) {
    return { ok: false, reason: errorMessage(err) };
  }
  const rel = relative(rootReal, pathReal);
  if (rel === "" || rel.startsWith("..") || rel.includes(sep)) {
    return { ok: false, reason: "outside_root" };
  }
  try {
    await rm(pathReal, { recursive: true, force: true });
  } catch (err) {
    return { ok: false, reason: errorMessage(err) };
  }
  return { ok: true };
}

function isAbsoluteSafeName(name: string): boolean {
  if (name === "." || name === "..") return false;
  if (name.includes("/") || name.includes("\\") || name.includes("\0")) return false;
  return true;
}

function isAlreadyExists(err: unknown): boolean {
  return !!err && typeof err === "object" && "code" in err && (err as { code?: string }).code === "EEXIST";
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
