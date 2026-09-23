import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import {
  installUpdaterBridge,
  MAC_UNSIGNED_CAPABILITIES,
  NO_UPDATE_PATH_CAPABILITIES,
  type UpdaterBridgeFake,
} from "../test/updater-bridge";

const mocks = vi.hoisted(() => ({
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}));

const translations = {
  auto_save: { toast_saved: "Settings saved" },
  desktop: {
    updates: {
      title: "Updates",
      description: "Update preferences",
      current_version: "Current version",
      automatic_updates_title: "Automatic background updates",
      automatic_updates_description: "Download updates in the background",
      automatic_updates_save_failed: "Failed to save update settings",
      release_channel_title: "Update channel",
      release_channel_description:
        "Stable and test use the same install. Switching checks the selected line right away.",
      release_channel_stable: "Stable",
      release_channel_test: "Test (updates more often, may be unstable)",
      release_channel_save_failed: "Failed to save the update channel",
      check_section_title: "Check for updates",
      check_section_description: "Check manually",
      up_to_date: "Up to date",
      check_now: "Check now",
      checking: "Checking",
      available: "v{{version}} is available.",
      available_manual: "v{{version}} is available — download it from the release page.",
      download: "Download",
      downloading_progress: "Downloading v{{version}} · {{percent}}%",
      downloaded: "v{{version}} downloaded — restart to install.",
      installer_ready: "v{{version}} downloaded — open it and drag Multica into Applications.",
      open_installer: "Open installer",
      reveal_installer: "Show in Finder",
      restart_now: "Restart now",
      failed: "Update failed: {{error}}",
      retry_download: "Retry download",
      manual_only_title: "Manual download only",
      manual_only_description: "Unsigned build",
      assisted_install_title: "Installed by hand",
      assisted_install_description: "This build downloads the installer for you.",
      open_release_page: "Open release page",
      last_check_label: "Last check",
      last_check_never: "not yet",
      last_check_available: "v{{version}} found",
      last_check_up_to_date: "up to date",
      last_check_failed: "failed: {{error}}",
      last_check_trigger: { startup: "at launch", periodic: "hourly", manual: "manual" },
      log_title: "Updater log",
      open_log: "Open log",
    },
  },
};

vi.mock("@multica/views/i18n", () => ({
  useT: () => ({
    t: (
      selector: (resources: typeof translations) => string,
      values?: Record<string, string>,
    ) => {
      const template = selector(translations);
      return Object.entries(values ?? {}).reduce(
        (result, [key, value]) => result.replace(`{{${key}}}`, value),
        template,
      );
    },
  }),
}));

vi.mock("sonner", () => ({
  toast: {
    success: mocks.toastSuccess,
    error: mocks.toastError,
  },
}));

import { UpdatesSettingsTab } from "./updates-settings-tab";

describe("UpdatesSettingsTab", () => {
  let bridge: UpdaterBridgeFake;

  beforeEach(() => {
    mocks.toastSuccess.mockReset();
    mocks.toastError.mockReset();
    bridge = installUpdaterBridge();
  });

  it("loads the persisted preference and saves changes from the switch", async () => {
    bridge.fns.getPreferences.mockResolvedValue({
      automaticUpdates: false,
      releaseChannel: "stable",
    });
    bridge.fns.setAutomaticUpdates.mockResolvedValue({
      automaticUpdates: true,
      releaseChannel: "stable",
    });
    render(<UpdatesSettingsTab />);

    const toggle = screen.getByRole("switch", {
      name: "Automatic background updates",
    });
    // The switch renders as <span role="switch">, so jest-dom's toBeEnabled()
    // treats it as always enabled and does not actually wait for getPreferences
    // to resolve. Wait on the persisted value being reflected instead, which
    // deterministically holds until the loaded preference (false) is applied.
    await waitFor(() => expect(toggle).not.toBeChecked());

    fireEvent.click(toggle);

    await waitFor(() => {
      expect(bridge.fns.setAutomaticUpdates).toHaveBeenCalledWith(true);
      expect(toggle).toBeChecked();
    });
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Settings saved", {
      id: "settings-auto-save",
    });
  });

  it("shows the last automatic check, including failures, instead of staying silent", async () => {
    bridge = installUpdaterBridge({
      lastCheck: {
        checkedAt: "2026-09-23T08:00:00Z",
        trigger: "startup",
        ok: false,
        error: "HttpError: 404",
      },
    });
    render(<UpdatesSettingsTab />);

    await waitFor(() =>
      expect(screen.getByTestId("updater-last-check")).toHaveTextContent(
        "at launch · failed: HttpError: 404",
      ),
    );

    act(() =>
      bridge.emit.checkResult({
        checkedAt: "2026-09-23T09:00:00Z",
        trigger: "periodic",
        ok: true,
        available: true,
        latestVersion: "1.3.0",
      }),
    );

    expect(screen.getByTestId("updater-last-check")).toHaveTextContent(
      "hourly · v1.3.0 found",
    );
    expect(screen.getByText("v1.3.0 is available.")).toBeInTheDocument();
  });

  it("runs a manual check and shows the download button when one is available", async () => {
    bridge.fns.checkForUpdates.mockResolvedValue({
      ok: true,
      currentVersion: "1.2.3",
      latestVersion: "1.3.0",
      available: true,
    });
    render(<UpdatesSettingsTab />);

    fireEvent.click(screen.getByRole("button", { name: "Check now" }));

    await waitFor(() =>
      expect(screen.getByText("v1.3.0 is available.")).toBeInTheDocument(),
    );
    fireEvent.click(screen.getByRole("button", { name: "Download" }));

    expect(bridge.fns.downloadUpdate).toHaveBeenCalledOnce();
    expect(screen.getByText("Downloading v1.3.0 · 0%")).toBeInTheDocument();
  });

  it("drives the progress bar from download-progress events", async () => {
    render(<UpdatesSettingsTab />);
    act(() => bridge.emit.updateAvailable({ version: "1.3.0" }));

    act(() => bridge.emit.downloadProgress({ percent: 33.3 }));

    expect(screen.getByText("Downloading v1.3.0 · 33%")).toBeInTheDocument();
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "33");

    act(() => bridge.emit.updateDownloaded({ version: "1.3.0" }));
    expect(screen.getByText("v1.3.0 downloaded — restart to install.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Restart now" }));
    expect(bridge.fns.installUpdate).toHaveBeenCalledOnce();
  });

  it("renders the failed state from an error event and offers a retry", () => {
    render(<UpdatesSettingsTab />);
    act(() => bridge.emit.updateAvailable({ version: "1.3.0" }));
    act(() => bridge.emit.downloadProgress({ percent: 50 }));

    act(() => bridge.emit.error({ message: "disk full" }));

    expect(screen.getByText("Update failed: disk full")).toBeInTheDocument();
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Retry download" }));
    expect(bridge.fns.downloadUpdate).toHaveBeenCalledOnce();
  });

  it("reports a failed manual check", async () => {
    bridge.fns.checkForUpdates.mockResolvedValue({ ok: false, error: "offline" });
    render(<UpdatesSettingsTab />);

    fireEvent.click(screen.getByRole("button", { name: "Check now" }));

    await waitFor(() =>
      expect(screen.getByText("Update failed: offline")).toBeInTheDocument(),
    );
    expect(screen.getByTestId("updater-last-check")).toHaveTextContent(
      "manual · failed: offline",
    );
  });

  it("switches to the manual-download path when nothing can be fetched", async () => {
    bridge = installUpdaterBridge({ capabilities: NO_UPDATE_PATH_CAPABILITIES });
    render(<UpdatesSettingsTab />);

    await waitFor(() =>
      expect(screen.getByText("Manual download only")).toBeInTheDocument(),
    );
    act(() => bridge.emit.updateAvailable({ version: "1.3.0" }));

    expect(
      screen.getByText("v1.3.0 is available — download it from the release page."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Download" })).not.toBeInTheDocument();
    fireEvent.click(screen.getAllByRole("button", { name: "Open release page" })[1]);
    await act(async () => {});
    expect(bridge.fns.openExternal).toHaveBeenCalledWith(
      "https://github.com/jeff-kunkun/multica/releases/latest",
    );
  });

  it("keeps the in-app download on an unsigned macOS build", async () => {
    bridge = installUpdaterBridge({ capabilities: MAC_UNSIGNED_CAPABILITIES });
    render(<UpdatesSettingsTab />);

    await waitFor(() =>
      expect(screen.getByText("Installed by hand")).toBeInTheDocument(),
    );
    expect(screen.queryByText("Manual download only")).not.toBeInTheDocument();

    act(() => bridge.emit.updateAvailable({ version: "1.3.0" }));

    expect(screen.getByText("v1.3.0 is available.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Download" }));
    expect(bridge.fns.downloadUpdate).toHaveBeenCalledOnce();
  });

  it("shows where the installer landed and how to finish the install", async () => {
    bridge = installUpdaterBridge({ capabilities: MAC_UNSIGNED_CAPABILITIES });
    render(<UpdatesSettingsTab />);
    await act(async () => {});

    act(() =>
      bridge.emit.installerReady({
        version: "1.3.0",
        fileName: "multica-desktop-1.3.0-mac-arm64.dmg",
        path: "/Users/x/Library/Application Support/Multica/installers/multica-desktop-1.3.0-mac-arm64.dmg",
      }),
    );

    expect(
      screen.getByText("v1.3.0 downloaded — open it and drag Multica into Applications."),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "/Users/x/Library/Application Support/Multica/installers/multica-desktop-1.3.0-mac-arm64.dmg",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Restart now" })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Open installer" }));
    await act(async () => {});
    expect(bridge.fns.openInstaller).toHaveBeenCalledOnce();

    fireEvent.click(screen.getByRole("button", { name: "Show in Finder" }));
    await act(async () => {});
    expect(bridge.fns.revealInstaller).toHaveBeenCalledOnce();
  });

  it("opens the updater log from the settings page", async () => {
    render(<UpdatesSettingsTab />);

    const button = await screen.findByRole("button", { name: "Open log" });
    fireEvent.click(button);

    expect(bridge.fns.openLogFile).toHaveBeenCalledOnce();
  });

  it("lets the user pick the update channel and hands the choice to the main process", async () => {
    bridge.fns.getPreferences.mockResolvedValue({
      automaticUpdates: true,
      releaseChannel: "test",
    });
    bridge.fns.setReleaseChannel.mockResolvedValue({
      automaticUpdates: true,
      releaseChannel: "stable",
    });
    render(<UpdatesSettingsTab />);

    const channel = screen.getByRole("combobox", { name: "Update channel" });
    await waitFor(() => expect(channel).toBeEnabled());
    expect(channel).toHaveValue("test");
    expect(
      screen.getByRole("option", {
        name: "Test (updates more often, may be unstable)",
      }),
    ).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Stable" })).toBeInTheDocument();
    // Same install, no reinstall: the copy must not scare the user off.
    expect(screen.getByText(/same install/i)).toBeInTheDocument();
    expect(screen.queryByText(/reinstall/i)).not.toBeInTheDocument();

    fireEvent.change(channel, { target: { value: "stable" } });

    await waitFor(() => {
      expect(bridge.fns.setReleaseChannel).toHaveBeenCalledWith("stable");
      expect(channel).toHaveValue("stable");
    });
    expect(mocks.toastSuccess).toHaveBeenCalled();
    // The main process re-checks on its own; the renderer must not double up.
    expect(bridge.fns.checkForUpdates).not.toHaveBeenCalled();
  });

  it("keeps the previous channel and reports when saving it fails", async () => {
    bridge.fns.setReleaseChannel.mockRejectedValue(new Error("disk"));
    render(<UpdatesSettingsTab />);

    const channel = screen.getByRole("combobox", { name: "Update channel" });
    await waitFor(() => expect(channel).toBeEnabled());
    fireEvent.change(channel, { target: { value: "test" } });

    await waitFor(() =>
      expect(mocks.toastError).toHaveBeenCalledWith("Failed to save the update channel"),
    );
    expect(channel).toHaveValue("stable");
  });
});
