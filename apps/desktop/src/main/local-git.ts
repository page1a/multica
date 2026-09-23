import { execFile } from "child_process";
import { realpath, stat } from "fs/promises";
import { isAbsolute } from "path";
import { promisify } from "util";

const run = promisify(execFile);

export type InitLocalGitResult = {
  ok: boolean;
  reason?: "not_absolute" | "not_a_directory" | "inside_repo" | "error";
  error?: string;
};

/**
 * Create a local Git repository in a plain folder.
 *
 * Nothing is pushed and no remote is added. An empty first commit is what
 * makes the folder a repository the rest of Multica can recognise: a `git
 * init` with no commit is still reported as "not a repo", because parallel
 * mode needs a commit to branch from. Existing files are left untracked —
 * committing a folder the user did not ask to snapshot would sweep in
 * dependencies and secrets.
 *
 * A folder that already sits inside another repository is refused. Nesting a
 * second `.git` there would hide the outer one from the tools that walk up.
 */
export async function initLocalGit(path: string): Promise<InitLocalGitResult> {
  if (!path || !isAbsolute(path)) {
    return { ok: false, reason: "not_absolute" };
  }
  let resolved = path;
  try {
    const st = await stat(path);
    if (!st.isDirectory()) return { ok: false, reason: "not_a_directory" };
    resolved = await realpath(path);
  } catch (err) {
    return { ok: false, reason: "error", error: errorMessage(err) };
  }

  const top = await gitTopLevel(resolved);
  if (top) {
    let topReal = top;
    try {
      topReal = await realpath(top);
    } catch {
      topReal = top;
    }
    if (topReal !== resolved) {
      return { ok: false, reason: "inside_repo" };
    }
  } else {
    try {
      await run("git", ["init", "--", resolved], { timeout: 15000 });
    } catch (err) {
      return { ok: false, reason: "error", error: errorMessage(err) };
    }
  }

  if (await gitHasCommit(resolved)) {
    return { ok: true };
  }
  try {
    await run(
      "git",
      [
        "-C",
        resolved,
        "-c",
        "user.name=Multica",
        "-c",
        "user.email=multica@localhost",
        "commit",
        "--allow-empty",
        "-m",
        "Initial commit",
      ],
      { timeout: 15000 },
    );
  } catch (err) {
    return { ok: false, reason: "error", error: errorMessage(err) };
  }
  return { ok: true };
}

async function gitTopLevel(path: string): Promise<string> {
  try {
    const { stdout } = await run("git", ["-C", path, "rev-parse", "--show-toplevel"], {
      timeout: 5000,
    });
    return stdout.trim();
  } catch {
    return "";
  }
}

async function gitHasCommit(path: string): Promise<boolean> {
  try {
    await run("git", ["-C", path, "rev-parse", "--verify", "HEAD"], { timeout: 5000 });
    return true;
  } catch {
    return false;
  }
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
