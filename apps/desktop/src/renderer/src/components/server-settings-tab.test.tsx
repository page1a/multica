import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import {
  DEFAULT_RUNTIME_CONFIG,
  SELF_HOSTED_PRESET_URL,
} from "../../../shared/runtime-config";

const mocks = vi.hoisted(() => ({
  switchServer: vi.fn(),
  getPrefs: vi.fn(),
}));

const translations = {
  desktop: {
    server: {
      title: "Server",
      description: "Switch servers",
      current_title: "Current server",
      host: "Host",
      kind: "Type",
      kind_official: "Official cloud",
      kind_self_hosted: "Self-hosted",
      profile: "Profile",
      api_url: "API URL",
      switch_title: "Switch server",
      switch_description: "Quit after switching",
      preset_official: "Official cloud",
      preset_official_description: "multica.ai",
      current_badge: "Current",
      custom_url: "Custom URL",
      custom_url_description: "http or https only",
      custom_url_placeholder: "https://",
      switch: "Switch",
      switch_to_official: "Switch to official cloud",
      confirm_title: "Switch server?",
      confirm_description: "Quit with {{shortcut}} and reopen.",
      confirm_autostop:
        "Auto-stop is on. Quitting will stop this machine's daemon.",
      confirm_switch_back: "Use Official cloud to switch back.",
      confirm_export_first:
        "Chats on official cloud do not move when you switch. Export first from Settings → Workspace → Migrate across environments.",
      confirm_cancel: "Cancel",
      confirm_action: "Switch",
      restart_title: "Fully quit and reopen",
      restart_description: "Quit with {{shortcut}} and reopen Multica Desktop.",
      invalid_url: "Enter a valid http or https URL.",
      switch_failed: "Could not switch server",
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

import { TRANSFER_EXPORT_COMPLETED_KEY } from "@multica/views/platform";
import { ServerSettingsTab } from "./server-settings-tab";

describe("ServerSettingsTab", () => {
  beforeEach(() => {
    mocks.switchServer.mockReset();
    mocks.getPrefs.mockReset().mockResolvedValue({ autoStart: true, autoStop: true });
    window.localStorage.removeItem(TRANSFER_EXPORT_COMPLETED_KEY);

    Object.defineProperty(window, "desktopAPI", {
      configurable: true,
      value: {
        appInfo: { os: "macos", version: "1.0.0" },
        runtimeConfig: { ok: true, config: DEFAULT_RUNTIME_CONFIG },
        switchServer: mocks.switchServer,
      },
    });
    Object.defineProperty(window, "daemonAPI", {
      configurable: true,
      value: { getPrefs: mocks.getPrefs },
    });
  });

  it("shows the official cloud host, type, and profile", async () => {
    render(<ServerSettingsTab />);

    expect(screen.getAllByText("multica.ai").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Official cloud").length).toBeGreaterThan(0);
    expect(screen.getByText("desktop-api.multica.ai")).toBeInTheDocument();
    expect(screen.getByText("https://api.multica.ai")).toBeInTheDocument();
    await waitFor(() => expect(mocks.getPrefs).toHaveBeenCalled());
  });

  it("offers only official cloud and a custom URL, with no self-hosted preset", async () => {
    render(<ServerSettingsTab />);

    expect(screen.queryByText("Default entry")).not.toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Switch" })).toHaveLength(1);
    expect(screen.getByLabelText("Custom URL")).toBeInTheDocument();
    await waitFor(() => expect(mocks.getPrefs).toHaveBeenCalled());
  });

  it("rejects an invalid custom URL without calling switchServer", async () => {
    render(<ServerSettingsTab />);

    fireEvent.change(screen.getByLabelText("Custom URL"), {
      target: { value: "ftp://evil.example" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Switch" }));

    expect(
      await screen.findByText("Enter a valid http or https URL."),
    ).toBeInTheDocument();
    expect(mocks.switchServer).not.toHaveBeenCalled();
    expect(screen.queryByText("Switch server?")).not.toBeInTheDocument();
  });

  it("warns about auto-stop, writes the custom URL, and asks for a full quit", async () => {
    mocks.switchServer.mockResolvedValue({
      ok: true,
      config: {
        schemaVersion: 1,
        apiUrl: "https://ai.ferryway.cc",
        wsUrl: "wss://ai.ferryway.cc/ws",
        appUrl: "https://ai.ferryway.cc",
      },
    });
    render(<ServerSettingsTab />);

    fireEvent.change(screen.getByLabelText("Custom URL"), {
      target: { value: SELF_HOSTED_PRESET_URL },
    });
    fireEvent.click(screen.getByRole("button", { name: "Switch" }));

    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText("Switch server?")).toBeInTheDocument();
    expect(
      within(dialog).getByText(
        "Auto-stop is on. Quitting will stop this machine's daemon.",
      ),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByText("Use Official cloud to switch back."),
    ).toBeInTheDocument();
    expect(within(dialog).getByTestId("server-switch-export-hint")).toHaveTextContent(
      "Chats on official cloud do not move when you switch. Export first from Settings → Workspace → Migrate across environments.",
    );

    fireEvent.click(within(dialog).getByRole("button", { name: "Switch" }));

    await waitFor(() => {
      expect(mocks.switchServer).toHaveBeenCalledWith(SELF_HOSTED_PRESET_URL);
    });
    expect(
      await screen.findByText("Fully quit and reopen"),
    ).toBeInTheDocument();
    expect(screen.getByText("Quit with ⌘Q and reopen Multica Desktop.")).toBeInTheDocument();
    expect(screen.getAllByText("ai.ferryway.cc").length).toBeGreaterThan(0);
    expect(screen.getByText("desktop-ai.ferryway.cc")).toBeInTheDocument();
  });

  it("switches back to official cloud with null", async () => {
    mocks.getPrefs.mockResolvedValue({ autoStart: true, autoStop: false });
    Object.defineProperty(window, "desktopAPI", {
      configurable: true,
      value: {
        appInfo: { os: "macos", version: "1.0.0" },
        runtimeConfig: {
          ok: true,
          config: {
            schemaVersion: 1,
            apiUrl: "https://ai.ferryway.cc",
            wsUrl: "wss://ai.ferryway.cc/ws",
            appUrl: "https://ai.ferryway.cc",
          },
        },
        switchServer: mocks.switchServer,
      },
    });
    mocks.switchServer.mockResolvedValue({
      ok: true,
      config: DEFAULT_RUNTIME_CONFIG,
    });

    render(<ServerSettingsTab />);

    fireEvent.click(
      screen.getByRole("button", { name: "Switch to official cloud" }),
    );
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText("Switch server?")).toBeInTheDocument();
    expect(
      within(dialog).queryByText(
        "Auto-stop is on. Quitting will stop this machine's daemon.",
      ),
    ).not.toBeInTheDocument();

    fireEvent.click(within(dialog).getByRole("button", { name: "Switch" }));

    await waitFor(() => {
      expect(mocks.switchServer).toHaveBeenCalledWith(null);
    });
  });

  it("hides the export-first hint after a transfer pack has been saved", async () => {
    window.localStorage.setItem(TRANSFER_EXPORT_COMPLETED_KEY, "1");
    render(<ServerSettingsTab />);

    fireEvent.change(screen.getByLabelText("Custom URL"), {
      target: { value: SELF_HOSTED_PRESET_URL },
    });
    fireEvent.click(screen.getByRole("button", { name: "Switch" }));

    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText("Switch server?")).toBeInTheDocument();
    expect(
      within(dialog).queryByTestId("server-switch-export-hint"),
    ).not.toBeInTheDocument();
  });
});
