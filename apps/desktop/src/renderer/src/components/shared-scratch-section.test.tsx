import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";

const mocks = vi.hoisted(() => ({
  report: vi.fn(),
  saveSettings: vi.fn(),
  remove: vi.fn(),
  cleanExpired: vi.fn(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}));

const translations = {
  desktop: {
    shared_scratch: {
      title: "Shared session folder",
      description: "Questions with no code land here.",
      scanning: "Looking",
      daemon_offline: "Start the local daemon",
      load_failed: "Could not read",
      location_title: "Where it is",
      location_description: "One folder",
      size_title: "Using",
      summary: "{{count}} conversations · {{total}} on disk · {{reclaimable}} idle",
      enable_title: "Clear idle conversations automatically",
      enable_description: "On by default",
      retention_title: "Keep for at least",
      retention_description: "Days",
      empty: "No shared conversations yet",
      last_used: "last used {{days}}d ago",
      in_use: "in use, left alone",
      not_ours: "not created by Multica, left alone",
      remove: "Clear",
      removed: "Conversation folder cleared",
      clean_expired: "Clear expired",
      cleaned: "Expired conversation folders cleared",
    },
  },
};

vi.mock("@multica/views/i18n", () => ({
  useT: () => ({
    t: (
      selector: (resources: typeof translations) => string,
      values?: Record<string, string | number>,
    ) => {
      const template = selector(translations);
      return Object.entries(values ?? {}).reduce(
        (result, [key, value]) => result.replace(`{{${key}}}`, String(value)),
        template,
      );
    },
  }),
}));

vi.mock("sonner", () => ({
  toast: { success: mocks.toastSuccess, error: mocks.toastError },
}));

import { SharedScratchSection } from "./shared-scratch-section";

function session(over: Record<string, unknown> = {}) {
  return {
    path: "/Users/me/.multica/workspaces/.sessions/ws-1/sessions/chat-1",
    session_id: "chat-1",
    size_bytes: 2048,
    in_use: false,
    ours: true,
    expired: true,
    last_used: "2026-08-01T00:00:00Z",
    ...over,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  (window as unknown as { desktopAPI: unknown }).desktopAPI = {
    sharedScratchReport: mocks.report,
    saveSharedScratchSettings: mocks.saveSettings,
    removeSharedSession: mocks.remove,
    cleanExpiredSharedSessions: mocks.cleanExpired,
  };
});

describe("SharedScratchSection", () => {
  it("shows where the folder is, how big it is, and refuses to clear a live one", async () => {
    mocks.report.mockResolvedValue({
      ok: true,
      report: {
        root: "/Users/me/.multica/workspaces/.sessions",
        size_bytes: 4096,
        settings: { enabled: true, retention_days: 14 },
        sessions: [
          session(),
          session({
            path: "/Users/me/.multica/workspaces/.sessions/ws-1/sessions/chat-live",
            session_id: "chat-live",
            in_use: true,
            expired: false,
          }),
        ],
      },
    });

    render(<SharedScratchSection />);

    expect(
      await screen.findByText("/Users/me/.multica/workspaces/.sessions"),
    ).toBeInTheDocument();
    expect(screen.getByText("4.0 KB")).toBeInTheDocument();
    expect(screen.getByRole("switch")).toBeChecked();
    // The idle conversation can be cleared. The live one cannot.
    expect(screen.getAllByRole("button", { name: "Clear" })).toHaveLength(1);
    expect(screen.getByText(/in use, left alone/)).toBeInTheDocument();
  });

  it("clears expired conversations from the button, not by guessing a path", async () => {
    const report = {
      root: "/sessions",
      size_bytes: 10,
      settings: { enabled: true, retention_days: 14 },
      sessions: [session()],
    };
    mocks.report.mockResolvedValue({ ok: true, report });
    mocks.cleanExpired.mockResolvedValue({
      ok: true,
      report: { ...report, sessions: [], size_bytes: 0 },
    });

    render(<SharedScratchSection />);
    fireEvent.click(await screen.findByRole("button", { name: "Clear expired" }));

    expect(mocks.cleanExpired).toHaveBeenCalledOnce();
    expect(mocks.remove).not.toHaveBeenCalled();
    expect(await screen.findByText("No shared conversations yet")).toBeInTheDocument();
  });
});
