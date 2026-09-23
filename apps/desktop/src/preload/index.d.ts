import { ElectronAPI } from "@electron-toolkit/preload";
import type {
  RuntimeConfigResult,
  RuntimeConfigSwitchResult,
} from "../shared/runtime-config";
import type { NavigationGesture } from "../shared/navigation-gestures";
import type { RendererRouteContextInput } from "../shared/renderer-route-context";
import type { FreezeBreadcrumb } from "../shared/freeze-breadcrumb";
import type {
  DesktopWindowContext,
  IssueWindowRequest,
} from "../shared/issue-window";
import type {
  ManualUpdateCheckResult,
  UpdaterPreferences,
} from "../shared/updater-types";
import type {
  WorktreeCleanupResult,
  WorktreeCleanupSettings,
} from "../main/worktree-cleanup";
import type {
  SharedScratchResult,
  SharedScratchSettings,
} from "../main/shared-scratch";
import type {
  DaemonStatus,
  DaemonPrefs,
  LocalRuntimeProbe,
} from "../shared/daemon-types";
import type { TabSelectionShortcutKey } from "../shared/main-renderer-messages";
import type {
  TransferJobState,
  TransferPickPathResult,
  TransferProgressEvent,
  TransferRunRequest,
  TransferRunResult,
} from "../shared/workspace-transfer";

interface DesktopAPI {
  /** Absolute home directory captured in the preload process. */
  homeDir: string;
  /** App version + normalized OS, captured synchronously at preload time. */
  appInfo: {
    version: string;
    os: "macos" | "windows" | "linux" | "unknown";
  };
  /** OS-preferred locale (BCP 47) injected by main via additionalArguments. */
  systemLocale: string;
  /** Subscribe to OS language changes detected after boot. Returns an unsubscribe function. */
  onSystemLocaleChanged: (callback: (locale: string) => void) => () => void;
  /** Validated runtime endpoint config, or a blocking config error. */
  runtimeConfig: RuntimeConfigResult;
  /**
   * Write ~/.multica/desktop.json for a server switch.
   * Pass `null` to return to official cloud (written explicitly; the
   * absent-file fallback is the self-hosted entry default). The running
   * session is unchanged until a full quit and reopen.
   */
  switchServer: (url: string | null) => Promise<RuntimeConfigSwitchResult>;
  /** Main tabbed window or a dedicated issue-only window. */
  windowContext: DesktopWindowContext;
  /** Read any freeze/crash breadcrumb from a previous session, so the renderer
   *  can flush it to telemetry on boot. Null when nothing's pending. Reading
   *  does not consume it — acknowledge with `ackFreeze`. */
  getLastFreeze: () => FreezeBreadcrumb | null;
  /** Retire the breadcrumb with this exact timestamp once its event has been
   *  handed to analytics. Unacknowledged breadcrumbs are retried next boot. */
  ackFreeze: (ts: number) => void;
  /** Report the resolved account identity so stale issue windows can close. */
  reportAuthSession: (userId: string | null) => void;
  /** Listen for auth token delivered via deep link. Returns an unsubscribe function. */
  onAuthToken: (callback: (token: string) => void) => () => void;
  /** Listen for invitation IDs delivered via deep link. Returns an unsubscribe function. */
  onInviteOpen: (callback: (invitationId: string) => void) => () => void;
  /** Open a URL in the default browser. */
  openExternal: (url: string) => Promise<void>;
  /** Download a file by URL through Electron's native download system.
   *  Shows a native save dialog. On non-desktop platforms this is undefined. */
  downloadURL: (url: string) => Promise<void>;
  /** Hide macOS traffic lights for full-screen modals; restore when false. */
  setImmersiveMode: (immersive: boolean) => Promise<void>;
  /** Show a native OS notification for a new inbox item. */
  showNotification: (payload: {
    slug: string;
    itemId: string;
    issueKey: string;
    title: string;
    body: string;
  }) => void;
  /** Update the OS dock / taskbar unread badge. Pass 0 to clear. */
  setUnreadBadge: (count: number) => void;
  /** Listen for "open inbox row" requests from notification clicks. Returns an unsubscribe function. */
  onInboxOpen: (
    callback: (payload: {
      slug: string;
      itemId: string;
      issueKey: string;
    }) => void,
  ) => () => void;
  /** Listen for native macOS back/forward swipe gestures. Returns an unsubscribe function. */
  onNavigationGesture: (callback: (gesture: NavigationGesture) => void) => () => void;
  /** Report the renderer's memory-router path for recovery diagnostics. */
  setRendererRouteContext: (context: RendererRouteContextInput) => void;
  /** Open the OS folder picker and return the chosen absolute path.
   *  Used by the Project settings "Add local directory" flow. */
  pickDirectory: (
    defaultPath?: string,
  ) => Promise<{
    ok: boolean;
    path?: string;
    basename?: string;
    reason?: "cancelled" | "no_window" | "error";
    error?: string;
  }>;
  /** Validate that a path is an existing readable+writable directory.
   *  Mirrors the daemon's runtime check so the user sees errors before submit. */
  validateLocalDirectory: (
    path: string,
  ) => Promise<{
    ok: boolean;
    reason?:
      | "not_absolute"
      | "not_found"
      | "not_a_directory"
      | "not_readable"
      | "not_writable"
      | "error";
    error?: string;
    /** Whether the path sits inside a git working tree. Only set when ok=true.
     *  Drives the worktree execution-mode option in the resource UI. */
    is_git_repo?: boolean;
    /** Symlink-resolved absolute path — the directory's identity for the
     *  "one row per directory" rule (DENE-617). */
    real_path?: string;
    /** Normalized identity of the repository this directory holds, from its
     *  `origin` remote. Absent when there is none to identify. */
    repo_key?: string;
    /** Where parallel mode would put working copies by default: the
     *  repository's sibling. Previewed before the user picks that mode. */
    default_worktree_root?: string;
    /** The repository root containing the directory, when there is one. */
    git_root?: string;
  }>;
  /** Create a local Git repository in a plain folder. Nothing is uploaded. */
  initLocalGit: (path: string) => Promise<{
    ok: boolean;
    reason?: "not_absolute" | "not_a_directory" | "inside_repo" | "error";
    error?: string;
  }>;
  /** Whether `path` (or its nearest existing ancestor) is writable. Used for
   *  worktree_root, which the first task often creates. */
  validateWritablePath: (path: string) => Promise<{ ok: boolean }>;
  /** Report on this machine's parallel-mode working copies and the cleanup
   *  policy in force. Served by the local daemon (DENE-617). */
  worktreeCleanupReport: () => Promise<WorktreeCleanupResult>;
  /** Save this machine's cleanup policy and return a fresh report. */
  saveWorktreeCleanupSettings: (
    settings: WorktreeCleanupSettings,
  ) => Promise<WorktreeCleanupResult>;
  /** Remove one working copy now. The daemon still applies every keep rule. */
  removeWorktreeCopy: (path: string) => Promise<WorktreeCleanupResult>;
  /** Where the shared session folder is, how big it is, and what is in it. */
  sharedScratchReport: () => Promise<SharedScratchResult>;
  saveSharedScratchSettings: (
    settings: SharedScratchSettings,
  ) => Promise<SharedScratchResult>;
  /** Remove one idle session folder. The daemon still applies every keep rule. */
  removeSharedSession: (path: string) => Promise<SharedScratchResult>;
  /** Remove session folders the retention window already calls expired. */
  cleanExpiredSharedSessions: () => Promise<SharedScratchResult>;
  /** Local skip-mutex overrides for folders stored as in_place on a server
   *  that does not accept execution_mode=shared. */
  listLocalDirectorySharedOverrides: () => Promise<
    Array<{ daemonId: string; localPath: string }>
  >;
  setLocalDirectorySharedOverride: (input: {
    daemonId: string;
    localPath: string;
    enabled: boolean;
  }) => Promise<{ ok: boolean; error?: string }>;
  /** Listen for Cmd/Ctrl+W tab-close requests from the main process.
   *  Returns an unsubscribe function. */
  onCloseActiveTab: (callback: () => void) => () => void;
  /** Listen for Cmd/Ctrl+, requests to open Settings, delivered to the main
   *  window whichever window had focus. Returns an unsubscribe function. */
  onOpenSettings: (callback: () => void) => () => void;
  /** Listen for Cmd/Ctrl+1..9 tab-selection requests, delivered to the main
   *  window whichever window had focus. Returns an unsubscribe function. */
  onSelectTabShortcut: (
    callback: (key: TabSelectionShortcutKey) => void,
  ) => () => void;
  /** Ask the main process to close the window. */
  closeWindow: () => void;
  /** Open an issue-detail tab in a dedicated native window. */
  openIssueWindow: (
    request: IssueWindowRequest,
  ) => Promise<{ ok: true } | { ok: false; reason: "invalid_request" }>;
  pickTransferExportPath: (input?: {
    slug?: string;
  }) => Promise<TransferPickPathResult>;
  pickTransferImportPath: () => Promise<TransferPickPathResult>;
  runWorkspaceTransfer: (request: TransferRunRequest) => Promise<TransferRunResult>;
  onTransferProgress: (
    callback: (event: TransferProgressEvent) => void,
  ) => () => void;
  getTransferJobState: () => Promise<TransferJobState>;
  onTransferJobState: (
    callback: (state: TransferJobState) => void,
  ) => () => void;
}

type DaemonReauthResult =
  | { ok: true }
  | { ok: false; reason: "session_invalid" }
  | { ok: false; reason: "transient"; message: string };

interface DaemonAPI {
  start: () => Promise<{ success: boolean; error?: string }>;
  stop: () => Promise<{ success: boolean; error?: string }>;
  restart: () => Promise<{ success: boolean; error?: string }>;
  getStatus: () => Promise<DaemonStatus>;
  probeRuntimes: () => Promise<LocalRuntimeProbe>;
  getHostName: () => Promise<string>;
  onStatusChange: (callback: (status: DaemonStatus) => void) => () => void;
  setTargetApiUrl: (url: string) => Promise<void>;
  syncToken: (token: string, userId: string) => Promise<void>;
  clearToken: () => Promise<void>;
  reauthenticate: (
    token: string,
    userId: string,
  ) => Promise<DaemonReauthResult>;
  isCliInstalled: () => Promise<boolean>;
  getPrefs: () => Promise<DaemonPrefs>;
  setPrefs: (prefs: Partial<DaemonPrefs>) => Promise<DaemonPrefs>;
  autoStart: () => Promise<void>;
  retryInstall: () => Promise<void>;
  startLogStream: () => void;
  stopLogStream: () => void;
  onLogLine: (callback: (line: string) => void) => () => void;
  openLogFile: () => Promise<{ success: boolean; error?: string }>;
}

interface UpdaterAPI {
  onUpdateAvailable: (callback: (info: { version: string; releaseNotes?: string }) => void) => () => void;
  onDownloadProgress: (callback: (progress: { percent: number }) => void) => () => void;
  onUpdateDownloaded: (
    callback: (info: { version: string; releaseNotes?: string }) => void,
  ) => () => void;
  downloadUpdate: () => Promise<void>;
  installUpdate: () => Promise<void>;
  getPreferences: () => Promise<UpdaterPreferences>;
  setAutomaticUpdates: (enabled: boolean) => Promise<UpdaterPreferences>;
  checkForUpdates: () => Promise<ManualUpdateCheckResult>;
}

declare global {
  interface Window {
    electron: ElectronAPI;
    desktopAPI: DesktopAPI;
    daemonAPI: DaemonAPI;
    updater: UpdaterAPI;
  }
}

export {};
