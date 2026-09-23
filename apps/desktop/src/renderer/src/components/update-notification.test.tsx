import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";

import { UpdateNotification } from "./update-notification";
import {
  installUpdaterBridge,
  MAC_UNSIGNED_CAPABILITIES,
  NO_UPDATE_PATH_CAPABILITIES,
  type UpdaterBridgeFake,
} from "../test/updater-bridge";

describe("UpdateNotification", () => {
  let bridge: UpdaterBridgeFake;

  beforeEach(() => {
    bridge = installUpdaterBridge();
  });

  it("stays hidden while idle, checking, or up to date", () => {
    const { container } = render(<UpdateNotification />);

    act(() => bridge.emit.checking({ trigger: "startup" }));
    expect(container).toBeEmptyDOMElement();

    act(() =>
      bridge.emit.checkResult({
        checkedAt: "2026-09-23T08:00:00Z",
        trigger: "startup",
        ok: true,
        available: false,
        latestVersion: "1.2.3",
      }),
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("offers a manual download once an update is available", () => {
    render(<UpdateNotification />);

    act(() => bridge.emit.updateAvailable({ version: "0.4.27" }));

    expect(screen.getByText("Update available")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Download" }));

    expect(bridge.fns.downloadUpdate).toHaveBeenCalledOnce();
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "0");
  });

  it("advances the progress bar from download-progress events", () => {
    render(<UpdateNotification />);
    act(() => bridge.emit.updateAvailable({ version: "0.4.27" }));

    act(() => bridge.emit.downloadProgress({ percent: 12.4 }));
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "12");
    expect(screen.getByText("v0.4.27 · 12%")).toBeInTheDocument();

    act(() => bridge.emit.downloadProgress({ percent: 87.6 }));
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "88");
    expect(screen.queryByRole("button", { name: "Download" })).not.toBeInTheDocument();
  });

  it("renders the failure and lets the user retry the download", () => {
    render(<UpdateNotification />);
    act(() => bridge.emit.updateAvailable({ version: "0.4.27" }));
    act(() => bridge.emit.downloadProgress({ percent: 40 }));

    act(() => bridge.emit.error({ message: "net::ERR_CONNECTION_RESET" }));

    expect(screen.getByText("Update failed")).toBeInTheDocument();
    expect(screen.getByText("net::ERR_CONNECTION_RESET")).toBeInTheDocument();
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Retry download" }));
    expect(bridge.fns.downloadUpdate).toHaveBeenCalledOnce();
    expect(screen.getByRole("progressbar")).toBeInTheDocument();
  });

  it("opens the release page from a failure with no version to retry", async () => {
    render(<UpdateNotification />);

    act(() => bridge.emit.error({ message: "Cannot find latest-mac.yml" }));

    expect(screen.queryByRole("button", { name: "Retry download" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Open release page" }));
    await act(async () => {});

    expect(bridge.fns.openExternal).toHaveBeenCalledWith(
      "https://github.com/jeff-kunkun/multica/releases/latest",
    );
  });

  it("points at the browser only when there is no in-app path at all", async () => {
    bridge = installUpdaterBridge({ capabilities: NO_UPDATE_PATH_CAPABILITIES });
    render(<UpdateNotification />);
    await act(async () => {});

    act(() => bridge.emit.updateAvailable({ version: "0.4.27" }));

    expect(screen.getByText("Manual download required")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Download" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Open release page" }));
    await act(async () => {});

    expect(bridge.fns.openExternal).toHaveBeenCalledWith(
      "https://github.com/jeff-kunkun/multica/releases/latest",
    );
    expect(bridge.fns.downloadUpdate).not.toHaveBeenCalled();
  });

  it("opens the downloaded version's changelog from the update prompt", () => {
    render(<UpdateNotification />);
    act(() => bridge.emit.updateDownloaded({ version: "0.4.27" }));

    expect(screen.queryByRole("button", { name: "Later" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "See changelog" }));

    expect(bridge.fns.openExternal).toHaveBeenCalledWith(
      "https://multica.ai/changelog#release-0-4-27",
    );
  });

  it("still installs the update immediately from the primary action", () => {
    render(<UpdateNotification />);
    act(() => bridge.emit.updateDownloaded({ version: "0.4.27" }));

    fireEvent.click(screen.getByRole("button", { name: "Restart now" }));

    expect(bridge.fns.installUpdate).toHaveBeenCalledOnce();
  });

  it("re-shows the card after dismissal once the phase moves on", () => {
    render(<UpdateNotification />);
    act(() => bridge.emit.updateAvailable({ version: "0.4.27" }));
    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByText("Update available")).not.toBeInTheDocument();

    act(() => bridge.emit.updateDownloaded({ version: "0.4.27" }));

    expect(screen.getByText("Update ready")).toBeInTheDocument();
  });

  // The default macOS release has no certificate: it cannot install in place,
  // but it does fetch the .dmg, so the card must offer a download rather than
  // a browser link.
  it("still offers the download on an unsigned macOS build", async () => {
    bridge = installUpdaterBridge({ capabilities: MAC_UNSIGNED_CAPABILITIES });
    render(<UpdateNotification />);
    await act(async () => {});

    act(() => bridge.emit.updateAvailable({ version: "0.4.27" }));

    expect(screen.queryByText("Manual download required")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Download" }));
    expect(bridge.fns.downloadUpdate).toHaveBeenCalledOnce();
  });

  it("asks for the drag into Applications once the installer is on disk", async () => {
    bridge = installUpdaterBridge({ capabilities: MAC_UNSIGNED_CAPABILITIES });
    render(<UpdateNotification />);
    await act(async () => {});

    act(() =>
      bridge.emit.installerReady({
        version: "0.4.27",
        fileName: "multica-desktop-0.4.27-mac-arm64.dmg",
        path: "/Users/x/Library/Application Support/Multica/installers/multica-desktop-0.4.27-mac-arm64.dmg",
      }),
    );

    expect(screen.getByText("Installer downloaded")).toBeInTheDocument();
    expect(
      screen.getByText(/drag Multica into Applications/i),
    ).toBeInTheDocument();
    // Nothing is staged, so a restart would do nothing but close the app.
    expect(screen.queryByRole("button", { name: "Restart now" })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /Open installer/ }));
    await act(async () => {});
    expect(bridge.fns.openInstaller).toHaveBeenCalledOnce();

    fireEvent.click(screen.getByRole("button", { name: /Show in Finder/ }));
    await act(async () => {});
    expect(bridge.fns.revealInstaller).toHaveBeenCalledOnce();
  });

  it("reports why the installer would not open", async () => {
    bridge = installUpdaterBridge({ capabilities: MAC_UNSIGNED_CAPABILITIES });
    bridge.fns.openInstaller.mockResolvedValue({
      success: false,
      error: "No installer downloaded",
    });
    render(<UpdateNotification />);
    await act(async () => {});
    act(() =>
      bridge.emit.installerReady({
        version: "0.4.27",
        fileName: "multica-desktop-0.4.27-mac-arm64.dmg",
        path: "/tmp/multica-desktop-0.4.27-mac-arm64.dmg",
      }),
    );

    fireEvent.click(screen.getByRole("button", { name: /Open installer/ }));
    await act(async () => {});

    expect(screen.getByText("Update failed")).toBeInTheDocument();
    expect(screen.getByText("No installer downloaded")).toBeInTheDocument();
  });

  it("restores the installer card after a window reload", async () => {
    bridge = installUpdaterBridge({
      capabilities: MAC_UNSIGNED_CAPABILITIES,
      installer: {
        version: "0.4.27",
        fileName: "multica-desktop-0.4.27-mac-arm64.dmg",
        path: "/tmp/multica-desktop-0.4.27-mac-arm64.dmg",
      },
    });
    render(<UpdateNotification />);
    await act(async () => {});

    expect(screen.getByText("Installer downloaded")).toBeInTheDocument();
  });

  it("detaches the IPC listeners when the last consumer unmounts", () => {
    const { unmount } = render(<UpdateNotification />);
    expect(bridge.listenerCounts()["download-progress"]).toBe(1);

    unmount();

    expect(bridge.listenerCounts()["download-progress"]).toBe(0);
  });
});
