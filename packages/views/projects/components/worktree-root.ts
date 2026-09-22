/**
 * Where parallel mode puts a directory's working copies, as the picker sees it
 * (DENE-617).
 *
 * The rule is execenv's — `ResolveWorktreeRoot` in
 * `server/internal/daemon/execenv/local_worktree_root.go` — and the daemon is
 * what enforces it. This module exists so the user finds out while the dialog
 * is still open, instead of from a task that fails later. The two must agree,
 * which is why both are stated the same way: absolute, and not inside the
 * repository working tree.
 */

export type WorktreeRootProblem =
  | "not_absolute"
  | "inside_repo"
  | "conflicts_with_binding";

/** Absolute on POSIX, on a Windows drive, or a UNC path — the union, because
 *  the directory lives on a machine whose OS this code does not know. */
export function isAbsolutePath(path: string): boolean {
  const s = (path ?? "").trim();
  if (!s) return false;
  return s.startsWith("/") || s.startsWith("\\\\") || /^[a-zA-Z]:[\\/]/.test(s);
}

/** Normalizes separators and drops trailing slashes so two spellings of one
 *  directory compare equal. Not a realpath — no filesystem here. */
function normalize(path: string): string {
  const s = (path ?? "").trim().replace(/\\/g, "/");
  const trimmed = s.replace(/\/+$/, "");
  return trimmed || s;
}

/**
 * Whether `candidate` is the repository or lives under it.
 *
 * Compared segment-wise, not by string prefix: `/repo-backup` starts with
 * `/repo` as text while being a completely different directory, and refusing
 * it would block a perfectly good choice.
 */
export function isInsideRepo(candidate: string, gitRoot: string): boolean {
  const root = normalize(gitRoot);
  const target = normalize(candidate);
  if (!root || !target) return false;
  if (target === root) return true;
  return target.startsWith(root + "/");
}

/**
 * The problem with a user-typed worktree root, or undefined when it is fine.
 *
 * An empty value is fine: it means "use the default", which is the
 * repository's sibling. An unknown `gitRoot` (an older desktop build, or an
 * existing resource whose path was validated at pick time) skips the
 * containment check rather than guessing — the daemon re-checks it
 * authoritatively before the first task runs.
 */
export function worktreeRootProblem(
  path: string,
  gitRoot: string | undefined,
  boundIdentities: string[] = [],
): WorktreeRootProblem | undefined {
  const value = (path ?? "").trim();
  if (!value) return undefined;
  if (!isAbsolutePath(value)) return "not_absolute";
  if (gitRoot && isInsideRepo(value, gitRoot)) return "inside_repo";
  if (worktreeRootConflictsWith(value, boundIdentities)) {
    return "conflicts_with_binding";
  }
  return undefined;
}

/** True when `root` is (or contains, or lives inside) another bound directory. */
export function worktreeRootConflictsWith(
  root: string,
  boundIdentities: string[],
): boolean {
  const value = (root ?? "").trim();
  if (!value) return false;
  return boundIdentities.some(
    (bound) => isInsideRepo(value, bound) || isInsideRepo(bound, value),
  );
}

/**
 * What to store on the resource for a chosen root.
 *
 * The default is stored as ABSENT rather than as its own literal path. The
 * default is "beside the repository", and a repository the user later moves
 * takes its copies with it — a stored literal would keep pointing at where the
 * repository used to be. Only a location the user actually chose is worth
 * pinning.
 */
export function worktreeRootForSave(
  path: string,
  defaultRoot: string | undefined,
): string | undefined {
  const value = (path ?? "").trim();
  if (!value) return undefined;
  if (defaultRoot && normalize(value) === normalize(defaultRoot)) return undefined;
  return value;
}
