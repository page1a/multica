// Desktop-only helpers for the project_resource local_directory flow.
//
// These wrap the preload `desktopAPI` surface so view components can
// SSR-render on web (where `window.desktopAPI` is undefined) and degrade
// gracefully to no-op promises instead of crashing.

export type PickDirectoryResult = {
  ok: boolean;
  path?: string;
  basename?: string;
  reason?: "cancelled" | "no_window" | "error" | "unsupported";
  error?: string;
};

export type LocalDirectorySharedOverride = {
  daemonId: string;
  localPath: string;
};

export type SetLocalDirectorySharedOverrideResult = {
  ok: boolean;
  error?: string;
};

export type ValidateLocalDirectoryResult = {
  ok: boolean;
  reason?:
    | "not_absolute"
    | "not_found"
    | "not_a_directory"
    | "not_readable"
    | "not_writable"
    | "error"
    | "unsupported";
  error?: string;
  /**
   * Whether the directory sits inside a git working tree. Only meaningful when
   * ok=true; absent from an older desktop build, which is why callers must
   * treat `undefined` as "unknown" rather than "not a repo".
   */
  is_git_repo?: boolean;
  /**
   * The symlink-resolved absolute path — the directory's identity for the
   * server's "one row per directory" rule (DENE-617). Absent from an older
   * desktop build, in which case the server falls back to the typed path,
   * which is what it compared before the field existed.
   */
  real_path?: string;
  /** Normalized identity of the repository the directory holds, or absent
   *  when there is none to identify. Drives duplicate detection only. */
  repo_key?: string;
  /** Where parallel mode would put working copies by default: the
   *  repository's sibling. A preview — the daemon decides. */
  default_worktree_root?: string;
  /** The repository root containing the directory, when there is one. Lets
   *  the picker refuse a worktree root inside the user's own repository. */
  git_root?: string;
};

export type InitLocalGitResult = {
  ok: boolean;
  reason?: "not_absolute" | "not_a_directory" | "inside_repo" | "error" | "unsupported";
  error?: string;
};

export type ProvisionProjectDirectoryResult = {
  ok: boolean;
  path?: string;
  reason?: "not_absolute" | "bad_name" | "exists" | "error" | "unsupported";
  error?: string;
};

interface DesktopLocalDirectoryAPI {
  pickDirectory?: (defaultPath?: string) => Promise<PickDirectoryResult>;
  validateLocalDirectory?: (
    path: string,
  ) => Promise<ValidateLocalDirectoryResult>;
  validateWritablePath?: (path: string) => Promise<{ ok: boolean }>;
  initLocalGit?: (path: string) => Promise<InitLocalGitResult>;
  provisionProjectDirectory?: (input: {
    root: string;
    dirName: string;
    gitInit: boolean;
  }) => Promise<ProvisionProjectDirectoryResult>;
  removeProvisionedDirectory?: (input: {
    root: string;
    path: string;
  }) => Promise<{ ok: boolean; reason?: string }>;
  listLocalDirectorySharedOverrides?: () => Promise<
    LocalDirectorySharedOverride[]
  >;
  setLocalDirectorySharedOverride?: (input: {
    daemonId: string;
    localPath: string;
    enabled: boolean;
  }) => Promise<SetLocalDirectorySharedOverrideResult>;
}

function readDesktopAPI(): DesktopLocalDirectoryAPI | undefined {
  if (typeof window === "undefined") return undefined;
  const api = (window as unknown as { desktopAPI?: DesktopLocalDirectoryAPI })
    .desktopAPI;
  return api;
}

/** True when the renderer is running inside the Electron desktop shell, as
 *  evidenced by the preload-exposed pickDirectory bridge. Avoids hard-coding
 *  navigator/process checks — those vary across electron-vite + jsdom tests. */
export function isDesktopShell(): boolean {
  const api = readDesktopAPI();
  return typeof api?.pickDirectory === "function";
}

export async function pickDirectory(
  defaultPath?: string,
): Promise<PickDirectoryResult> {
  const api = readDesktopAPI();
  if (!api?.pickDirectory) return { ok: false, reason: "unsupported" };
  return api.pickDirectory(defaultPath);
}

export async function validateLocalDirectory(
  path: string,
): Promise<ValidateLocalDirectoryResult> {
  const api = readDesktopAPI();
  if (!api?.validateLocalDirectory) return { ok: false, reason: "unsupported" };
  return api.validateLocalDirectory(path);
}

/** Whether `path` (or its nearest existing ancestor) is writable. Web and
 *  older desktop builds return true: they cannot check, and the daemon
 *  still refuses an unwritable root at task time. */
/** Create a local Git repository in a plain folder. Web and older desktop
 *  builds report unsupported — they cannot touch the disk. */
export async function initLocalGit(path: string): Promise<InitLocalGitResult> {
  const api = readDesktopAPI();
  if (!api?.initLocalGit) return { ok: false, reason: "unsupported" };
  return api.initLocalGit(path);
}

/** Create a project subdirectory on this machine. Web reports unsupported. */
export async function provisionProjectDirectory(input: {
  root: string;
  dirName: string;
  gitInit: boolean;
}): Promise<ProvisionProjectDirectoryResult> {
  const api = readDesktopAPI();
  if (!api?.provisionProjectDirectory) return { ok: false, reason: "unsupported" };
  return api.provisionProjectDirectory(input);
}

/** Remove a subdirectory this flow created. Web reports unsupported. */
export async function removeProvisionedDirectory(input: {
  root: string;
  path: string;
}): Promise<{ ok: boolean; reason?: string }> {
  const api = readDesktopAPI();
  if (!api?.removeProvisionedDirectory) return { ok: false, reason: "unsupported" };
  return api.removeProvisionedDirectory(input);
}

export async function validateWritablePath(path: string): Promise<boolean> {
  const api = readDesktopAPI();
  if (!api?.validateWritablePath) return true;
  try {
    const result = await api.validateWritablePath(path);
    return result.ok === true;
  } catch {
    return true;
  }
}

/** True when this desktop build can persist a skip-mutex override locally. */
export function canSetLocalDirectorySharedOverride(): boolean {
  const api = readDesktopAPI();
  return typeof api?.setLocalDirectorySharedOverride === "function";
}

export async function listLocalDirectorySharedOverrides(): Promise<
  LocalDirectorySharedOverride[]
> {
  const api = readDesktopAPI();
  if (!api?.listLocalDirectorySharedOverrides) return [];
  try {
    const rows = await api.listLocalDirectorySharedOverrides();
    return Array.isArray(rows) ? rows : [];
  } catch {
    return [];
  }
}

export async function setLocalDirectorySharedOverride(input: {
  daemonId: string;
  localPath: string;
  enabled: boolean;
}): Promise<SetLocalDirectorySharedOverrideResult> {
  const api = readDesktopAPI();
  if (!api?.setLocalDirectorySharedOverride) {
    return { ok: false, error: "unsupported" };
  }
  return api.setLocalDirectorySharedOverride(input);
}

export function localDirectoryOverrideKey(
  daemonId: string,
  localPath: string,
): string {
  return `${daemonId}\n${normalizeLocalDirectoryOverridePath(localPath)}`;
}

export function normalizeLocalDirectoryOverridePath(path: string): string {
  return path.replace(/[\\/]+$/, "") || path;
}
