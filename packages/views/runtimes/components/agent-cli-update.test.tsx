// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { AgentRuntime } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import { api } from "@multica/core/api";
import enCommon from "../../locales/en/common.json";
import enRuntimes from "../../locales/en/runtimes.json";
import {
  AgentCLIUpdateControls,
  readAgentCLIUpdate,
} from "./agent-cli-update";

vi.mock("@multica/core/api", () => ({
  api: {
    setAgentCLIFollow: vi.fn(),
    requestAgentCLIUpdate: vi.fn(),
  },
}));

const TEST_RESOURCES = { en: { common: enCommon, runtimes: enRuntimes } };

function runtime(metadata: Record<string, unknown>): AgentRuntime {
  return {
    id: "rt-1",
    workspace_id: "ws-1",
    daemon_id: null,
    name: "Claude Code",
    runtime_mode: "local",
    provider: "claude",
    launch_header: "",
    status: "online",
    device_info: "",
    metadata,
    owner_id: "user-1",
    visibility: "private",
    last_seen_at: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
}

function controlsTree(metadata: Record<string, unknown>, canManage = true) {
  return (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <AgentCLIUpdateControls
        runtime={runtime(metadata)}
        canManage={canManage}
      />
    </I18nProvider>
  );
}

function renderControls(metadata: Record<string, unknown>, canManage = true) {
  return render(controlsTree(metadata, canManage));
}

const snapshot = {
  cli_update: {
    current_version: "2.1.5",
    latest_version: "2.1.9",
    auto_follow: true,
    phase: "available",
    binary_path: "/usr/local/bin/claude",
    error: "",
  },
};

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("agent CLI update controls", () => {
  it("shows the current and latest versions and which copy will be updated", () => {
    renderControls(snapshot);
    expect(screen.getByText("2.1.5 / 2.1.9")).toBeInTheDocument();
    expect(screen.getByText("Update available")).toBeInTheDocument();
    expect(screen.getByText("This copy: /usr/local/bin/claude")).toBeInTheDocument();
  });

  it("shows the failure reason", () => {
    renderControls({
      cli_update: {
        ...snapshot.cli_update,
        phase: "failed",
        error: "network is down",
      },
    });
    expect(screen.getByText("network is down")).toBeInTheDocument();
  });

  it("shows that an upgrade is waiting for a running task", () => {
    renderControls({
      cli_update: { ...snapshot.cli_update, phase: "waiting" },
    });
    expect(
      screen.getByText("Queued until running tasks finish"),
    ).toBeInTheDocument();
  });

  it("shows a queued upgrade in grey with how many tasks it waits for", () => {
    renderControls({
      cli_update: {
        ...snapshot.cli_update,
        phase: "waiting",
        waiting_tasks: 2,
        claims_paused: true,
        wait_reason: "tasks",
        // An older daemon put its waiting note here; it is not an error.
        error: "waiting until this machine has no task running",
      },
    });
    const queued = screen.getByText("Queued: waiting for 2 Claude task(s) to finish");
    expect(queued).toHaveClass("text-muted-foreground");
    expect(
      screen.getByText(
        "New Claude tasks wait until the update is done; other CLIs keep working",
      ),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("waiting until this machine has no task running"),
    ).not.toBeInTheDocument();
    expect(screen.queryByText("Update available")).not.toBeInTheDocument();
  });

  it("says when a hold ran out and the CLI takes tasks again", () => {
    renderControls({
      cli_update: {
        ...snapshot.cli_update,
        phase: "waiting",
        waiting_tasks: 1,
        wait_reason: "hold_expired",
      },
    });
    expect(
      screen.getByText(
        "Paused too long: Claude takes tasks again and updates once idle (1 running)",
      ),
    ).toBeInTheDocument();
  });

  it("keeps showing the update in progress until the daemon reports otherwise", () => {
    renderControls({ cli_update: { ...snapshot.cli_update, phase: "updating" } });
    expect(screen.getAllByText("Updating…").length).toBeGreaterThan(0);
    expect(screen.getByRole("button", { name: "Updating…" })).toBeDisabled();
  });

  it("names the version it just updated to", () => {
    renderControls({
      cli_update: {
        ...snapshot.cli_update,
        phase: "current",
        current_version: "2.1.9",
        updated_at: "2026-09-30T00:00:00Z",
      },
    });
    expect(screen.getByText("Updated to 2.1.9")).toHaveClass("text-success");
  });

  it("shows the failure reason in the list cell, not only on hover", () => {
    render(
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <AgentCLIUpdateControls
          runtime={runtime({
            cli_update: { ...snapshot.cli_update, phase: "failed", error: "npm: EACCES" },
          })}
          canManage
          compact
        />
      </I18nProvider>,
    );
    expect(screen.getByText("npm: EACCES")).toHaveClass("text-destructive");
  });

  it("shows the click as queued until the daemon's next report", async () => {
    vi.mocked(api.requestAgentCLIUpdate).mockResolvedValue(undefined);
    const view = renderControls(snapshot);
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Update now" }));
    });
    expect(
      screen.getByText("Update requested, waiting for this machine to respond"),
    ).toBeInTheDocument();
    expect(screen.queryByText("Update available")).not.toBeInTheDocument();

    view.rerender(
      controlsTree({
        cli_update: {
          ...snapshot.cli_update,
          phase: "waiting",
          waiting_tasks: 1,
          claims_paused: true,
          wait_reason: "tasks",
        },
      }),
    );
    expect(
      screen.getByText("Queued: waiting for 1 Claude task(s) to finish"),
    ).toBeInTheDocument();
  });

  it("asks the daemon to update this CLI", async () => {
    vi.mocked(api.requestAgentCLIUpdate).mockResolvedValue(undefined);
    renderControls(snapshot);
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Update now" }));
    });
    expect(api.requestAgentCLIUpdate).toHaveBeenCalledWith("rt-1");
  });

  it("follows the daemon after an update request instead of spinning", async () => {
    vi.mocked(api.requestAgentCLIUpdate).mockResolvedValue(undefined);
    const view = renderControls(snapshot);
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Update now" }));
    });

    view.rerender(
      controlsTree({
        cli_update: { ...snapshot.cli_update, phase: "waiting", error: "" },
      }),
    );
    expect(
      screen.getByText("Queued until running tasks finish"),
    ).toBeInTheDocument();
    expect(screen.queryByText("Updating…")).not.toBeInTheDocument();

    view.rerender(
      controlsTree({
        cli_update: {
          ...snapshot.cli_update,
          phase: "failed",
          error: "network is down",
        },
      }),
    );
    expect(screen.getByRole("button", { name: "Update now" })).toBeEnabled();
    expect(screen.getByText("network is down")).toBeInTheDocument();
    expect(screen.queryByText("Updating…")).not.toBeInTheDocument();

    view.rerender(
      controlsTree({
        cli_update: {
          ...snapshot.cli_update,
          phase: "current",
          current_version: "2.1.9",
          error: "",
        },
      }),
    );
    expect(screen.getByText("Up to date")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Update now" })).toBeEnabled();
  });

  it("turns auto-follow off", async () => {
    vi.mocked(api.setAgentCLIFollow).mockResolvedValue(undefined);
    renderControls(snapshot);
    await act(async () => {
      fireEvent.click(screen.getByRole("switch"));
    });
    expect(api.setAgentCLIFollow).toHaveBeenCalledWith("rt-1", false);
  });

  it("keeps the compact list cell to two lines and moves the rest into a tooltip", () => {
    const { container } = render(
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <AgentCLIUpdateControls
          runtime={runtime({
            cli_update: {
              ...snapshot.cli_update,
              phase: "unsupported",
              error: "no updater for the copy at /usr/local/bin/claude",
            },
          })}
          canManage
          compact
        />
      </I18nProvider>,
    );
    // No stacked path/error paragraphs that would overflow the h-12 row.
    expect(container.querySelectorAll("p")).toHaveLength(0);
    // An unsupported CLI shows why, not a disabled switch and button.
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(screen.queryByRole("switch")).not.toBeInTheDocument();
    expect(screen.getByText("2.1.5 / 2.1.9").closest("[title]")).toHaveAttribute(
      "title",
      "/usr/local/bin/claude\nno updater for the copy at /usr/local/bin/claude",
    );
  });

  it("does not invent a snapshot the daemon has not reported", () => {
    expect(readAgentCLIUpdate({ version: "2.1.5" })).toBeNull();
  });
});
