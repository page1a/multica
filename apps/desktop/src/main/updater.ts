import { autoUpdater, type UpdateDownloadedEvent } from "electron-updater";
import { app, type BrowserWindow, ipcMain, shell } from "electron";
import log from "electron-log/main";
import { join } from "node:path";
import {
  RELEASE_REPO,
  releasePageUrl,
  type InstallerReadyPayload,
  type ManualUpdateCheckResult,
  type OpenInstallerResult,
  type ReleaseChannel,
  type UpdateCheckRecord,
  type UpdateCheckTrigger,
  type UpdaterCapabilities,
  type UpdaterPreferences,
} from "../shared/updater-types";
import {
  DEFAULT_UPDATER_PREFERENCES,
  loadUpdaterPreferences,
  saveUpdaterPreferences,
  updaterPreferencesPath,
} from "./updater-preferences";
import { detectMacSigning, type MacSigningStatus } from "./mac-signing";
import {
  fetchInstallerForVersion,
  type DownloadedInstaller,
} from "./mac-installer";

// Background updates: electron-updater downloads on its own as soon as
// `update-available` fires (see resolveCapabilities for the macOS exception,
// which flips this off when the install step cannot succeed). The renderer
// mirrors every phase — checking, available, downloading, downloaded, error —
// so nothing in this chain is silent anymore.
autoUpdater.autoDownload = true;
autoUpdater.autoInstallOnAppQuit = true;

/**
 * The slice of electron-updater's AppUpdater the channel logic touches.
 * `setFeedURL` is what swaps the provider: the test line cannot be served by
 * the GitHub provider (see `prepareFeedForCheck`), so it points a generic
 * provider at one release's download directory instead.
 */
export interface ChannelConfigurableUpdater {
  channel: string | null;
  allowDowngrade: boolean;
  allowPrerelease: boolean;
  setFeedURL: (options: UpdateFeedOptions) => void;
}

export type UpdateFeedOptions =
  | { provider: "github"; owner: string; repo: string }
  | {
      provider: "generic";
      url: string;
      channel: string;
      useMultipleRangeRequest: boolean;
    };

/**
 * `vX.Y.Z-test.N` is the test line. A distance suffix after that tag
 * (`0.5.5-test.3-2-gabcdef`) is still that line. Stable tags and the
 * describe form `0.5.4-14-gabcdef` are not.
 */
export function isTestReleaseVersion(version: string): boolean {
  const match = version
    .replace(/^v/, "")
    .match(/^\d+\.\d+\.\d+(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?/);
  if (!match?.[1]) return false;
  return match[1].split(".")[0] === "test";
}

/**
 * Channel name shared with `publishChannelForTarget` in scripts/package.mjs;
 * electron-updater appends `-mac` / `-linux[-arch]` / `.yml` itself.
 * `null` is the untouched electron-updater default (`latest` / `latest-mac.yml`
 * / `latest-linux*.yml`). The arch-specific stable names already installed
 * clients request — `latest-x64`, `latest-arm64` — stay byte-for-byte.
 *
 * The test prefix is `test`, not `beta`: it must equal the prerelease
 * segment of the `vX.Y.Z-test.N` tag, because that is the only channel name
 * electron-updater's GitHub provider will ever look up for a prerelease.
 */
export function feedNameForReleaseChannel(
  releaseChannel: ReleaseChannel,
  platform: NodeJS.Platform,
  arch: string,
): string | null {
  const prefix = releaseChannel === "test" ? "test" : "latest";
  if (platform === "win32" && arch === "arm64") return `${prefix}-arm64`;
  if (platform === "darwin" && arch === "x64") return `${prefix}-x64`;
  if (prefix === "latest") return null;
  return prefix;
}

export function shouldAllowStableDowngrade(
  releaseChannel: ReleaseChannel,
  currentVersion: string,
): boolean {
  // `0.5.5-test.3` → `0.5.4` is a downgrade. Leaving this off traps the user
  // on the test line. It is not the AppUpdater.channel setter's side effect:
  // that flag is assigned explicitly below, after the setter runs.
  return releaseChannel === "stable" && isTestReleaseVersion(currentVersion);
}

const TEST_TAG_PATTERN = /^v?(\d+)\.(\d+)\.(\d+)-test\.(\d+)$/;

/**
 * Newest `vX.Y.Z-test.N` tag in a GitHub releases Atom feed
 * (`https://github.com/<owner>/<repo>/releases.atom`). Ordered by version,
 * not feed position, so a republished older test release cannot win.
 * `null` when the feed carries no test release at all.
 */
export function pickNewestTestReleaseTag(atomXml: string): string | null {
  let best: { tag: string; key: number[] } | null = null;
  for (const match of atomXml.matchAll(/\/releases\/tag\/([^"'<>\s]+)/g)) {
    const tag = decodeURIComponent(match[1]);
    const parts = TEST_TAG_PATTERN.exec(tag);
    if (!parts) continue;
    const key = parts.slice(1, 5).map(Number);
    if (best === null || compareVersionKeys(key, best.key) > 0) {
      best = { tag, key };
    }
  }
  return best?.tag ?? null;
}

function compareVersionKeys(a: number[], b: number[]): number {
  for (let i = 0; i < a.length; i++) {
    if (a[i] !== b[i]) return a[i] - b[i];
  }
  return 0;
}

export function githubReleaseFeedOptions(): UpdateFeedOptions {
  const [owner, repo] = RELEASE_REPO.split("/");
  return { provider: "github", owner, repo };
}

export function testReleaseFeedOptions(
  tag: string,
  channel: string,
): UpdateFeedOptions {
  return {
    provider: "generic",
    url: `https://github.com/${RELEASE_REPO}/releases/download/${tag}`,
    channel,
    // GitHub serves release assets from S3, which rejects multi-range
    // requests; electron-updater's own GitHub provider pins the same flag.
    useMultipleRangeRequest: false,
  };
}

export function releasesAtomUrl(): string {
  return `https://github.com/${RELEASE_REPO}/releases.atom`;
}

/**
 * Point the updater at the stable feed: electron-updater's GitHub provider,
 * `latest*.yml` under whatever `/releases/latest` resolves to. This is the
 * path every installed client has been on; only the channel name and the
 * downgrade flag are set here, and the provider is only rebuilt when a test
 * feed replaced it earlier in this session.
 */
export function applyStableFeed(
  updater: ChannelConfigurableUpdater,
  currentVersion: string,
  platform: NodeJS.Platform = process.platform,
  arch: string = process.arch,
  restoreProvider = false,
): void {
  if (restoreProvider) updater.setFeedURL(githubReleaseFeedOptions());
  const feed = feedNameForReleaseChannel("stable", platform, arch);
  // Assigning `.channel` sets allowDowngrade to true. Once it is a string,
  // a later `null` throws, so the default stable feed is spelled `latest`
  // only after some other feed was selected. `latest` still resolves to
  // `latest-mac.yml` / `latest.yml` / `latest-linux*.yml`.
  if (feed != null) {
    updater.channel = feed;
  } else if (updater.channel != null) {
    updater.channel = "latest";
  }
  updater.allowDowngrade = shouldAllowStableDowngrade("stable", currentVersion);
  updater.allowPrerelease = false;
}

/**
 * Point the updater at one test release. electron-updater's GitHub provider
 * cannot serve this line: with `allowPrerelease` it derives the channel file
 * from the tag's prerelease segment alone (`test-mac.yml`, never
 * `test-x64-mac.yml`), and without it `/releases/latest` skips prereleases.
 * So the tag is resolved here from the releases feed and a generic provider
 * is aimed at that release's download directory with the exact channel name
 * package.mjs published.
 */
export function applyTestFeed(
  updater: ChannelConfigurableUpdater,
  tag: string,
  platform: NodeJS.Platform = process.platform,
  arch: string = process.arch,
): void {
  const feed = feedNameForReleaseChannel("test", platform, arch) ?? "test";
  updater.setFeedURL(testReleaseFeedOptions(tag, feed));
  updater.channel = feed;
  // Moving onto the test line is never a downgrade worth forcing: a stable
  // client newer than every test build simply sees "up to date".
  updater.allowDowngrade = false;
  updater.allowPrerelease = true;
}

// Pin the architecture feed before preferences load. The saved channel is
// applied again once preferences resolve, before the first check.
applyStableFeed(autoUpdater, "0.0.0");

const STARTUP_CHECK_DELAY_MS = 5_000;
const PERIODIC_CHECK_INTERVAL_MS = 60 * 60 * 1000; // 1 hour

type RendererChannel =
  | "updater:checking"
  | "updater:check-result"
  | "updater:update-available"
  | "updater:download-progress"
  | "updater:update-downloaded"
  | "updater:installer-ready"
  | "updater:error";

function isDestroyedObjectError(err: unknown): boolean {
  return err instanceof Error && err.message.includes("Object has been destroyed");
}

function sendToLiveRenderer(
  win: BrowserWindow | null,
  channel: RendererChannel,
  payload: unknown,
): void {
  if (!win || win.isDestroyed()) return;

  try {
    const { webContents } = win;
    if (webContents.isDestroyed()) return;
    webContents.send(channel, payload);
  } catch (err) {
    if (isDestroyedObjectError(err)) return;
    throw err;
  }
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

/**
 * Decide how far this build can carry an update on its own. Only macOS has a
 * hard blocker: Squirrel.Mac checks the downloaded bundle against the running
 * app's designated requirement. An ad-hoc signature pins that requirement to
 * this binary's cdhash, and an unsigned bundle has nothing to match, so those
 * two can never install a later build. Any stable identity (Developer ID or
 * the fork's self-signed certificate) pins the requirement to the certificate
 * instead, and a later build signed with the same cert matches. We ask
 * codesign rather than guessing from the version string.
 *
 * A build that fails that test is not sent away empty-handed: it still gets
 * `assistedInstallSupported`, where the app downloads the release `.dmg`
 * itself and the user only performs the drag into Applications.
 */
export async function resolveCapabilities(
  probe: {
    platform?: NodeJS.Platform;
    isPackaged?: boolean;
    executablePath?: string;
    detectSigning?: (executablePath: string) => Promise<MacSigningStatus>;
    logPath?: string | null;
    currentVersion?: string;
  } = {},
): Promise<UpdaterCapabilities> {
  const {
    platform = process.platform,
    isPackaged = app.isPackaged,
    executablePath = app.getPath("exe"),
    detectSigning = detectMacSigning,
    logPath = null,
  } = probe;
  const base = {
    releasePageUrl: releasePageUrl(),
    logPath,
  };

  // Dev runs never auto-update (electron-updater has no app-update.yml), so
  // there is nothing to block; report "supported" and let the check no-op.
  if (platform !== "darwin" || !isPackaged) {
    return {
      ...base,
      autoUpdateSupported: true,
      assistedInstallSupported: false,
      blocker: null,
    };
  }

  const signing = await detectSigning(executablePath);
  if (signing === "identity") {
    return {
      ...base,
      autoUpdateSupported: true,
      assistedInstallSupported: false,
      blocker: null,
    };
  }
  return {
    ...base,
    autoUpdateSupported: false,
    assistedInstallSupported: true,
    blocker: "mac-unsigned",
  };
}

export interface SetupAutoUpdaterOptions {
  /** Test seam: override the capability probe (signing check, platform). */
  resolveCapabilities?: () => Promise<UpdaterCapabilities>;
  /** Test seam: override the assisted `.dmg` fetch. */
  fetchInstaller?: (
    version: string,
    onProgress: (percent: number) => void,
  ) => Promise<DownloadedInstaller>;
  /** Test seam: fetch the GitHub releases Atom feed as text. */
  fetchText?: (url: string) => Promise<string>;
}

async function fetchTextWithFetch(url: string): Promise<string> {
  const response = await fetch(url, {
    headers: { accept: "application/atom+xml, application/xml, text/xml, */*" },
  });
  if (!response.ok) {
    throw new Error(`GET ${url} failed: HTTP ${response.status}`);
  }
  return response.text();
}

function updaterLogPath(): string | null {
  try {
    return log.transports.file.getFile().path;
  } catch {
    return null;
  }
}

/**
 * The `app-update.yml` electron-updater itself resolved. Reading the same file
 * keeps the assisted `.dmg` download pointed at whatever feed served the
 * metadata — including the override electron-updater honours in development.
 */
function updateConfigPath(): string {
  const configured = (autoUpdater as unknown as { updateConfigPath?: string | null })
    .updateConfigPath;
  return configured ?? join(process.resourcesPath, "app-update.yml");
}

export function setupAutoUpdater(
  getMainWindow: () => BrowserWindow | null,
  options: SetupAutoUpdaterOptions = {},
): void {
  // Route electron-updater's own diagnostics (feed URL, cache path, download
  // failures) to the on-disk log so a packaged build leaves evidence behind:
  // ~/Library/Logs/Multica/main.log on macOS, %APPDATA%/Multica/logs on
  // Windows, ~/.config/Multica/logs on Linux.
  autoUpdater.logger = log;

  const preferencesFilePath = updaterPreferencesPath(app.getPath("userData"));
  let automaticUpdatesEnabled =
    DEFAULT_UPDATER_PREFERENCES.automaticUpdates;
  let releaseChannel: ReleaseChannel =
    DEFAULT_UPDATER_PREFERENCES.releaseChannel;
  let startupCheckElapsed = false;
  let startupTimer: ReturnType<typeof setTimeout> | null = null;
  let periodicTimer: ReturnType<typeof setInterval> | null = null;
  let lastCheck: UpdateCheckRecord | null = null;
  const currentPreferences = (): UpdaterPreferences => ({
    automaticUpdates: automaticUpdatesEnabled,
    releaseChannel,
  });
  const fetchText = options.fetchText ?? fetchTextWithFetch;
  // Which provider autoUpdater currently holds. The stable GitHub provider is
  // only rebuilt after a test feed replaced it, so clients that never leave
  // stable keep the exact app-update.yml path they have always used.
  let activeFeed: "stable" | "test" = "stable";
  const preferencesReady = loadUpdaterPreferences(preferencesFilePath).then(
    (preferences) => {
      automaticUpdatesEnabled = preferences.automaticUpdates;
      releaseChannel = preferences.releaseChannel;
      if (releaseChannel === "stable") {
        applyStableFeed(autoUpdater, app.getVersion());
      }
      return preferences;
    },
  );

  // Aim autoUpdater at the feed for the selected channel right before a
  // check. The test line has to be re-resolved every time because the newest
  // `-test.N` tag moves; stable only needs the provider restored once.
  const prepareFeedForCheck = async (): Promise<void> => {
    if (releaseChannel === "test") {
      const tag = pickNewestTestReleaseTag(await fetchText(releasesAtomUrl()));
      if (tag) {
        applyTestFeed(autoUpdater, tag);
        activeFeed = "test";
        return;
      }
      log.info(
        "[updater] no vX.Y.Z-test.N release published yet; checking the stable feed instead",
      );
    }
    applyStableFeed(autoUpdater, app.getVersion(), process.platform, process.arch, activeFeed === "test");
    activeFeed = "stable";
  };

  const capabilitiesReady = (
    options.resolveCapabilities ??
    (() => resolveCapabilities({ logPath: updaterLogPath() }))
  )().then((capabilities) => {
    // Don't let Squirrel stage a package it would fail to apply: on an ad-hoc
    // or unsigned macOS build the assisted path below fetches the .dmg
    // instead, so the download still happens — just not through electron-updater.
    autoUpdater.autoDownload = capabilities.autoUpdateSupported;
    if (!capabilities.autoUpdateSupported) {
      log.warn(
        `[updater] in-place install unavailable (${capabilities.blocker}); ` +
          `${capabilities.assistedInstallSupported ? "downloading the installer for a manual drag into Applications" : "manual download only"}`,
      );
    }
    return capabilities;
  });

  // --- Assisted install (macOS without a stable signing identity) ----------
  // electron-updater is out of the picture here: it would stage a package
  // Squirrel refuses to apply. We fetch the release .dmg ourselves, report
  // progress on the same renderer channel as a normal download, and finish in
  // `installer-ready` instead of `update-downloaded` — the card there asks for
  // the drag into Applications rather than a restart.
  let installerReady: InstallerReadyPayload | null = null;
  let assistedDownload: Promise<void> | null = null;
  let offeredVersion: string | null = null;

  // `update-available` does not fire for a check whose result the renderer
  // already has (a repeat check for the same version), so the last check
  // record is the second source for "which version is on offer".
  const versionFromLastCheck = (): string | null =>
    lastCheck?.ok && lastCheck.available ? lastCheck.latestVersion : null;

  const fetchInstaller =
    options.fetchInstaller ??
    ((version: string, onProgress: (percent: number) => void) =>
      fetchInstallerForVersion({
        version,
        arch: process.arch,
        configPath: updateConfigPath(),
        userDataPath: app.getPath("userData"),
        onProgress,
      }));

  const startAssistedDownload = (version: string): Promise<void> => {
    if (assistedDownload) return assistedDownload;
    if (installerReady?.version === version) {
      sendToLiveRenderer(getMainWindow(), "updater:installer-ready", installerReady);
      return Promise.resolve();
    }

    sendToLiveRenderer(getMainWindow(), "updater:download-progress", { percent: 0 });
    const run = fetchInstaller(version, (percent) => {
      sendToLiveRenderer(getMainWindow(), "updater:download-progress", { percent });
    })
      .then((result) => {
        installerReady = {
          version,
          fileName: result.fileName,
          path: result.path,
        };
        log.info(`[updater] installer ready for manual install: ${result.path}`);
        sendToLiveRenderer(getMainWindow(), "updater:installer-ready", installerReady);
      })
      .catch((err) => {
        log.error("[updater] installer download failed:", err);
        sendToLiveRenderer(getMainWindow(), "updater:error", {
          message: errorMessage(err),
        });
      })
      .finally(() => {
        if (assistedDownload === run) assistedDownload = null;
      });
    assistedDownload = run;
    return run;
  };

  // Single-flight guard around checkForUpdates(). With autoDownload=true the
  // startup, periodic, and manual triggers can all kick off downloads, and
  // overlapping calls have caused duplicate download warnings in the past
  // (see electronjs.org/docs/latest/api/auto-updater). Coalesce concurrent
  // callers onto the same in-flight promise.
  let inFlightCheck: Promise<UpdateCheckRecord> | null = null;
  const checkForUpdatesOnce = (
    trigger: UpdateCheckTrigger,
  ): Promise<UpdateCheckRecord> => {
    if (inFlightCheck) return inFlightCheck;
    sendToLiveRenderer(getMainWindow(), "updater:checking", { trigger });
    const p = capabilitiesReady
      .then(() => prepareFeedForCheck())
      .then(() => autoUpdater.checkForUpdates())
      .then((result): UpdateCheckRecord => {
        // checkForUpdates resolves as soon as metadata is fetched; the actual
        // download (when autoDownload=true) is exposed on result.downloadPromise.
        // Without a handler a download failure becomes an unhandled rejection
        // in the main process — Node may terminate it on future versions. The
        // renderer hears about it through autoUpdater's own `error` event.
        void (result as { downloadPromise?: Promise<unknown> } | null)?.downloadPromise?.catch(
          (err) => {
            log.error("[updater] download failed:", err);
          },
        );
        const info = result as
          | { updateInfo: { version: string }; isUpdateAvailable?: boolean }
          | null;
        return {
          checkedAt: new Date().toISOString(),
          trigger,
          ok: true,
          // Trust electron-updater's own decision rather than re-deriving it
          // from a version-string compare. The two diverge for pre-release
          // channels, staged rollouts, downgrades, and minimum-system-version
          // gates — in those cases updateInfo.version differs from
          // app.getVersion() but no `update-available` event fires.
          available: info?.isUpdateAvailable ?? false,
          latestVersion: info?.updateInfo.version ?? app.getVersion(),
        };
      })
      .catch((err): UpdateCheckRecord => {
        log.error(`[updater] ${trigger} check failed:`, err);
        return {
          checkedAt: new Date().toISOString(),
          trigger,
          ok: false,
          error: errorMessage(err),
        };
      })
      .then((record) => {
        lastCheck = record;
        sendToLiveRenderer(getMainWindow(), "updater:check-result", record);
        return record;
      })
      .finally(() => {
        if (inFlightCheck === p) inFlightCheck = null;
      });
    inFlightCheck = p;
    return p;
  };

  const runAutomaticCheck = (trigger: "startup" | "periodic"): void => {
    void preferencesReady.then(() => {
      if (!automaticUpdatesEnabled) return;
      return checkForUpdatesOnce(trigger);
    });
  };

  // Arm the startup + periodic background checks. Idempotent: an already-armed
  // timer is left in place so re-enabling never stacks duplicate schedules.
  const scheduleBackgroundChecks = (): void => {
    if (startupTimer === null && !startupCheckElapsed) {
      // Initial check shortly after startup so we don't block boot.
      startupTimer = setTimeout(() => {
        startupTimer = null;
        startupCheckElapsed = true;
        runAutomaticCheck("startup");
      }, STARTUP_CHECK_DELAY_MS);
    }
    if (periodicTimer === null) {
      // Background poll so long-running sessions still pick up new releases
      // without requiring the user to restart the app.
      periodicTimer = setInterval(() => {
        runAutomaticCheck("periodic");
      }, PERIODIC_CHECK_INTERVAL_MS);
    }
  };

  // Tear down the scheduled checks outright when automatic updates are turned
  // off. Relying only on an in-callback preference guard leaves the timers
  // running and lets a tick that races the preference flip still fire a check;
  // clearing them makes "disabled" mean no future background work, full stop.
  const cancelBackgroundChecks = (): void => {
    if (startupTimer !== null) {
      clearTimeout(startupTimer);
      startupTimer = null;
    }
    if (periodicTimer !== null) {
      clearInterval(periodicTimer);
      periodicTimer = null;
    }
  };

  autoUpdater.on("update-available", (info) => {
    offeredVersion = info.version;
    sendToLiveRenderer(getMainWindow(), "updater:update-available", {
      version: info.version,
      releaseNotes: info.releaseNotes,
    });
    // Mirror electron-updater's own autoDownload on the assisted path: the
    // user should never have to ask for the bytes, only for the install.
    void capabilitiesReady.then((capabilities) => {
      if (!capabilities.assistedInstallSupported) return;
      return startAssistedDownload(info.version);
    });
  });

  autoUpdater.on("download-progress", (progress) => {
    sendToLiveRenderer(getMainWindow(), "updater:download-progress", {
      percent: progress.percent,
    });
  });

  autoUpdater.on("update-downloaded", (info: UpdateDownloadedEvent) => {
    sendToLiveRenderer(getMainWindow(), "updater:update-downloaded", {
      version: info.version,
      releaseNotes: info.releaseNotes,
    });
  });

  // electron-updater emits `error` for both metadata and download failures.
  // Forward it so the renderer can show a failed state with a retry, and
  // keep the full object in the on-disk log for post-mortem.
  autoUpdater.on("error", (err) => {
    log.error("[updater] error:", err);
    sendToLiveRenderer(getMainWindow(), "updater:error", {
      message: errorMessage(err),
    });
  });

  // Manual download: the "Download" / "Retry" button in the renderer. Routed
  // to whichever downloader this build can actually finish with.
  ipcMain.handle("updater:download", async () => {
    const capabilities = await capabilitiesReady;
    if (capabilities.assistedInstallSupported) {
      const version = offeredVersion ?? versionFromLastCheck();
      if (!version) throw new Error("No update version has been offered yet");
      await startAssistedDownload(version);
      return;
    }

    try {
      await autoUpdater.downloadUpdate();
    } catch (err) {
      // electron-updater already emitted `error` for this failure; rethrow so
      // the renderer's invoke() rejects too and the button can settle.
      throw new Error(errorMessage(err));
    }
  });

  ipcMain.handle("updater:install", () => {
    autoUpdater.quitAndInstall(false, true);
  });

  ipcMain.handle(
    "updater:get-installer",
    (): InstallerReadyPayload | null => installerReady,
  );

  // Mount the .dmg. Finder then shows the drag-to-Applications window that is
  // the whole point of this path, so no extra guidance has to be rendered on
  // top of the OS's own.
  ipcMain.handle("updater:open-installer", async (): Promise<OpenInstallerResult> => {
    if (!installerReady) return { success: false, error: "No installer downloaded" };
    const error = await shell.openPath(installerReady.path);
    return error ? { success: false, error } : { success: true };
  });

  ipcMain.handle("updater:reveal-installer", (): OpenInstallerResult => {
    if (!installerReady) return { success: false, error: "No installer downloaded" };
    shell.showItemInFolder(installerReady.path);
    return { success: true };
  });

  ipcMain.handle(
    "updater:get-capabilities",
    (): Promise<UpdaterCapabilities> => capabilitiesReady,
  );

  ipcMain.handle(
    "updater:get-last-check",
    (): UpdateCheckRecord | null => lastCheck,
  );

  ipcMain.handle("updater:open-log", async () => {
    const path = updaterLogPath();
    if (!path) return { success: false, error: "No updater log file" };
    // shell.openPath returns "" on success, error string on failure.
    const error = await shell.openPath(path);
    return error ? { success: false, error } : { success: true };
  });

  ipcMain.handle(
    "updater:get-preferences",
    async (): Promise<UpdaterPreferences> => {
      await preferencesReady;
      return currentPreferences();
    },
  );

  ipcMain.handle(
    "updater:set-automatic-updates",
    async (_event, enabled: unknown): Promise<UpdaterPreferences> => {
      if (typeof enabled !== "boolean") {
        throw new TypeError("automaticUpdates must be a boolean");
      }

      await preferencesReady;
      const wasEnabled = automaticUpdatesEnabled;
      automaticUpdatesEnabled = enabled;
      const preferences = currentPreferences();
      await saveUpdaterPreferences(preferencesFilePath, preferences);

      if (!enabled) {
        cancelBackgroundChecks();
      } else if (!wasEnabled) {
        // If the startup check has already passed while the preference was off,
        // enabling it should take effect now instead of waiting up to one hour.
        if (startupCheckElapsed) {
          runAutomaticCheck("startup");
        }
        scheduleBackgroundChecks();
      }

      return preferences;
    },
  );

  ipcMain.handle(
    "updater:set-release-channel",
    async (_event, channel: unknown): Promise<UpdaterPreferences> => {
      if (channel !== "stable" && channel !== "test") {
        throw new TypeError('releaseChannel must be "stable" or "test"');
      }

      await preferencesReady;
      releaseChannel = channel;
      const preferences = currentPreferences();
      await saveUpdaterPreferences(preferencesFilePath, preferences);
      // A check already in flight is for the previous feed. Wait it out, then
      // look up the feed just selected — don't wait for the hourly poll.
      // checkForUpdatesOnce never rejects: failures become a check record.
      const pending = inFlightCheck;
      const recheck = () => void checkForUpdatesOnce("manual");
      if (pending) void pending.finally(recheck);
      else recheck();
      return preferences;
    },
  );

  ipcMain.handle("updater:check", async (): Promise<ManualUpdateCheckResult> => {
    const record = await checkForUpdatesOnce("manual");
    if (!record.ok) return { ok: false, error: record.error };
    return {
      ok: true,
      currentVersion: app.getVersion(),
      latestVersion: record.latestVersion,
      available: record.available,
    };
  });

  // Initial check shortly after startup so we don't block boot, plus a
  // background poll for long-running sessions. Both are torn down when the
  // user disables automatic updates and re-armed when they turn them back on.
  scheduleBackgroundChecks();
}
