import { useCallback, useEffect } from "react";
import { useShallow } from "zustand/react/shallow";
import { useUpdaterStore, type UpdatePhase } from "../stores/updater-store";
import type {
  UpdateCheckRecord,
  UpdaterCapabilities,
} from "../../../shared/updater-types";

// Wire the preload updater events into the store exactly once no matter how
// many components consume the hook. Ref-counted so unmounting the last
// consumer detaches the IPC listeners (keeps tests hermetic and lets a
// window tear down cleanly).
let subscribers = 0;
let disconnect: (() => void) | null = null;

function connectUpdaterEvents(): void {
  const store = useUpdaterStore.getState();
  const cleanups = [
    window.updater.onChecking(({ trigger }) => store.checking(trigger)),
    window.updater.onCheckResult((record) => store.checkResult(record)),
    window.updater.onUpdateAvailable((info) => store.updateAvailable(info.version)),
    window.updater.onDownloadProgress(({ percent }) => store.downloadProgress(percent)),
    window.updater.onUpdateDownloaded((info) => store.updateDownloaded(info.version)),
    window.updater.onInstallerReady((installer) => store.installerReady(installer)),
    window.updater.onError(({ message }) => store.failed(message)),
  ];
  disconnect = () => {
    for (const cleanup of cleanups) cleanup();
  };
}

function acquireUpdaterEvents(): () => void {
  if (subscribers === 0) connectUpdaterEvents();
  subscribers += 1;
  return () => {
    subscribers -= 1;
    if (subscribers === 0) {
      disconnect?.();
      disconnect = null;
    }
  };
}

// Pull what happened before this component mounted: the main process keeps
// the last check record and the signing-derived capabilities, neither of
// which are replayed as events.
function hydrateFromMain(): void {
  const store = useUpdaterStore.getState();
  void window.updater
    .getCapabilities()
    .then((capabilities) => store.setCapabilities(capabilities))
    .catch(() => undefined);
  void window.updater
    .getInstaller()
    .then((installer) => {
      // A .dmg downloaded before this component mounted is not replayed as an
      // event; without this the card disappears on a window reload.
      if (installer && useUpdaterStore.getState().phase.status === "idle") {
        store.installerReady(installer);
      }
    })
    .catch(() => undefined);
  void window.updater
    .getLastCheck()
    .then((record) => {
      if (record && useUpdaterStore.getState().lastCheck === null) {
        store.setLastCheck(record);
      }
    })
    .catch(() => undefined);
}

export interface UpdaterView {
  phase: UpdatePhase;
  lastCheck: UpdateCheckRecord | null;
  capabilities: UpdaterCapabilities | null;
  /** False on an ad-hoc or unsigned macOS build: it cannot swap itself out. */
  autoUpdateSupported: boolean;
  /** True when such a build still downloads the .dmg for a manual install. */
  assistedInstallSupported: boolean;
  check: () => Promise<void>;
  download: () => Promise<void>;
  install: () => Promise<void>;
  openReleasePage: () => Promise<void>;
  openLogFile: () => Promise<void>;
  openInstaller: () => Promise<void>;
  revealInstaller: () => Promise<void>;
}

export function useUpdater(): UpdaterView {
  const { phase, lastCheck, capabilities } = useUpdaterStore(
    useShallow((state) => ({
      phase: state.phase,
      lastCheck: state.lastCheck,
      capabilities: state.capabilities,
    })),
  );

  useEffect(() => {
    const release = acquireUpdaterEvents();
    hydrateFromMain();
    return release;
  }, []);

  const check = useCallback(async () => {
    const store = useUpdaterStore.getState();
    store.checking("manual");
    // The main process also emits checking/check-result for this call; the
    // store transitions are idempotent so both paths land on the same phase.
    const result = await window.updater.checkForUpdates();
    if (!result.ok) {
      store.checkResult({
        checkedAt: new Date().toISOString(),
        trigger: "manual",
        ok: false,
        error: result.error,
      });
      return;
    }
    store.checkResult({
      checkedAt: new Date().toISOString(),
      trigger: "manual",
      ok: true,
      available: result.available,
      latestVersion: result.latestVersion,
    });
  }, []);

  const download = useCallback(async () => {
    const store = useUpdaterStore.getState();
    store.downloadStarted();
    try {
      await window.updater.downloadUpdate();
    } catch (err) {
      store.failed(err instanceof Error ? err.message : String(err));
    }
  }, []);

  const install = useCallback(async () => {
    await window.updater.installUpdate();
  }, []);

  const openReleasePage = useCallback(async () => {
    const url =
      useUpdaterStore.getState().capabilities?.releasePageUrl ??
      (await window.updater.getCapabilities()).releasePageUrl;
    await window.desktopAPI.openExternal(url);
  }, []);

  const openLogFile = useCallback(async () => {
    await window.updater.openLogFile();
  }, []);

  const openInstaller = useCallback(async () => {
    const result = await window.updater.openInstaller();
    if (!result.success) {
      useUpdaterStore.getState().failed(result.error);
    }
  }, []);

  const revealInstaller = useCallback(async () => {
    await window.updater.revealInstaller();
  }, []);

  return {
    phase,
    lastCheck,
    capabilities,
    autoUpdateSupported: capabilities?.autoUpdateSupported ?? true,
    assistedInstallSupported: capabilities?.assistedInstallSupported ?? false,
    check,
    download,
    install,
    openReleasePage,
    openLogFile,
    openInstaller,
    revealInstaller,
  };
}
