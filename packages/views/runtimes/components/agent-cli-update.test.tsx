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
      screen.getByText("Waiting until this machine is idle"),
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
      screen.getByText("Waiting until this machine is idle"),
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

  it("does not invent a snapshot the daemon has not reported", () => {
    expect(readAgentCLIUpdate({ version: "2.1.5" })).toBeNull();
  });
});
