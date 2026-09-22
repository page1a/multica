import { delimiter, join } from "path";

/**
 * PATH padding for macOS/Linux GUI launches.
 *
 * A GUI-launched app inherits launchd's minimal PATH, which omits everything
 * only a login shell would add (Homebrew, nvm/fnm shims, ~/.local/bin). The
 * app runs `fix-path` first to recover the login shell's PATH; this module is
 * the fallback it applies afterwards, for when that recovery comes up short
 * (broken rc file, non-interactive $SHELL, an entry the user never added).
 *
 * The daemon is spawned with this environment and resolves agent CLIs with
 * exec.LookPath, so this list *is* the daemon's PATH until the user's own
 * shell config can be recovered.
 *
 * Where an entry goes is the contract, not a detail, and the two halves differ:
 *
 * - The user's own install directory is PREPENDED. User-local installs must
 *   win, because the official standalone installers (Codex, Claude, …) write
 *   to ~/.local/bin while a Homebrew/npm global install puts a *separate,
 *   older* copy of the same CLI in /opt/homebrew/bin. Listing Homebrew first
 *   made the daemon pin the stale copy — /opt/homebrew/bin/codex 0.148.0
 *   shadowed ~/.local/bin/codex 0.154.0, so the model picker never offered
 *   gpt-6-astra even after `codex debug models` was re-run (DENE-507).
 * - The machine-wide directories are APPENDED, never prepended. Prepending
 *   /usr/local/bin over a recovered login PATH shadows nvm/fnm Node with a
 *   stale system binary (e.g. Node 12), which breaks shebang CLIs
 *   (`#!/usr/bin/env node`) such as CodeBuddy and OpenClaw during daemon
 *   --version probes (MUL-7312).
 *
 * Kept free of electron imports so the ordering invariant stays unit-tested.
 */

/**
 * The user's own install directory, PREPENDED so it beats every machine-wide
 * install of the same CLI.
 */
export function fallbackPathDirs(home: string): string[] {
  return [join(home, ".local/bin")];
}

/**
 * Machine-wide package managers, APPENDED so a recovered login-shell PATH —
 * an nvm/fnm Node above all — keeps its place ahead of them. Apple-silicon
 * Homebrew comes before the Intel/manual /usr/local.
 */
export function appendedPathDirs(): string[] {
  return ["/opt/homebrew/bin", "/usr/local/bin"];
}

/**
 * Apply the fallback directories to a PATH value: prepend the user-local
 * directory, then append the machine-wide ones that no earlier segment already
 * provides — a recovered nvm/fnm Node therefore stays ahead of
 * /usr/local/bin.
 *
 * Repeating an appended directory the login shell already contributed would be
 * noise, so those are left where they are. An unset/empty PATH yields only the
 * fallback entries: a trailing separator would leave an empty segment, which
 * some tools read as the current directory.
 */
export function applyFallbackPathDirs(
  path: string | undefined,
  home: string,
): string {
  const segments = (path ?? "").split(delimiter).filter(Boolean);
  const prepended = fallbackPathDirs(home);
  const missing = appendedPathDirs().filter(
    (dir) => !prepended.includes(dir) && !segments.includes(dir),
  );
  return [...prepended, ...segments, ...missing].join(delimiter);
}
