import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import type { GitHubAppStatus } from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enSettings from "../../locales/en/settings.json";
import { GitHubAppPanel, launchGitHubAppSetup } from "./github-app-panel";

const platform = vi.hoisted(() => ({
  isDesktopShell: vi.fn(() => false),
  openExternal: vi.fn(),
}));
vi.mock("../../platform", () => platform);

const resources = { en: { common: enCommon, settings: enSettings } };

function wrap({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="en" resources={resources}>
      {children}
    </I18nProvider>
  );
}

function status(partial: Partial<GitHubAppStatus>): GitHubAppStatus {
  return {
    source: "none",
    configured: false,
    read_only: false,
    can_create: false,
    ...partial,
  };
}

function renderPanel(value: GitHubAppStatus, onCreate = vi.fn()) {
  render(
    <GitHubAppPanel
      status={value}
      org=""
      onOrg={vi.fn()}
      creating={false}
      onCreate={onCreate}
    />,
    { wrapper: wrap },
  );
  return onCreate;
}

describe("GitHub App status", () => {
  it("lets an owner start creation when nothing is configured", async () => {
    const onCreate = renderPanel(status({ can_create: true }));
    expect(screen.getByText("This server has no GitHub App yet.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Create the GitHub App" }));
    expect(onCreate).toHaveBeenCalledOnce();
  });

  it("tells a non-owner that the owner has to turn it on", () => {
    renderPanel(status({ block_reason: "not_owner" }));
    expect(screen.getByText("A workspace owner has to turn this on.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Create the GitHub App" })).toBeNull();
  });

  it("shows an environment-configured app as read-only", () => {
    renderPanel(
      status({
        source: "env",
        configured: true,
        read_only: true,
        app_name: "multica-env",
        manage_url: "https://github.com/settings/apps/multica-env",
      }),
    );
    expect(screen.getByText("Configured on the server, read-only.")).toBeInTheDocument();
    expect(screen.getByText("multica-env")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "GitHub settings" })).toHaveAttribute(
      "href",
      "https://github.com/settings/apps/multica-env",
    );
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("shows an app created from Settings with its management link", () => {
    renderPanel(
      status({
        source: "database",
        configured: true,
        app_name: "Multica (app.example)",
        manage_url: "https://github.com/settings/apps/multica-app",
      }),
    );
    expect(screen.getByText("Created from Settings.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "GitHub settings" })).toHaveAttribute(
      "href",
      "https://github.com/settings/apps/multica-app",
    );
  });

  it("explains when the server cannot store the app or has no public address", () => {
    const { rerender } = render(
      <GitHubAppPanel
        status={status({ block_reason: "secret_unavailable" })}
        org=""
        onOrg={vi.fn()}
        creating={false}
        onCreate={vi.fn()}
      />,
      { wrapper: wrap },
    );
    expect(screen.getByText("This server cannot store a GitHub App yet.")).toBeInTheDocument();
    rerender(
      <GitHubAppPanel
        status={status({ block_reason: "public_url_missing" })}
        org=""
        onOrg={vi.fn()}
        creating={false}
        onCreate={vi.fn()}
      />,
    );
    expect(
      screen.getByText("This server has no public address, so GitHub cannot call back."),
    ).toBeInTheDocument();
  });
});

describe("launchGitHubAppSetup", () => {
  const setup = {
    action_url: "https://github.com/settings/apps/new",
    manifest: { name: "Multica" },
    launch_url: "https://api.example.test/api/github/app/launch?token=abc",
  };

  afterEach(() => {
    vi.restoreAllMocks();
    platform.isDesktopShell.mockReset().mockReturnValue(false);
    platform.openExternal.mockReset();
    document.body.innerHTML = "";
  });

  it("posts the manifest form in this tab on web", () => {
    const submit = vi.spyOn(HTMLFormElement.prototype, "submit").mockImplementation(() => {});
    expect(launchGitHubAppSetup(setup)).toBe("form");
    expect(submit).toHaveBeenCalledOnce();
    const form = document.querySelector("form");
    expect(form?.getAttribute("action")).toBe(setup.action_url);
    expect(form?.getAttribute("method")).toBe("post");
    expect(platform.openExternal).not.toHaveBeenCalled();
  });

  it("opens the single-use launch link in the system browser on desktop", () => {
    platform.isDesktopShell.mockReturnValue(true);
    const submit = vi.spyOn(HTMLFormElement.prototype, "submit").mockImplementation(() => {});
    expect(launchGitHubAppSetup(setup)).toBe("browser");
    expect(platform.openExternal).toHaveBeenCalledWith(setup.launch_url);
    expect(submit).not.toHaveBeenCalled();
    expect(document.querySelector("form")).toBeNull();
  });
});
