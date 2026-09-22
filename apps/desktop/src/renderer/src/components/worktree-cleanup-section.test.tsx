import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";

// Wiring for the storage screen (DENE-617). The reading rules — order,
// totals, formatting — are the canonical table in worktree-cleanup-view.test.ts
// and are not re-run through a DOM mount here.

const mocks = vi.hoisted(() => ({
  report: vi.fn(),
  saveSettings: vi.fn(),
  remove: vi.fn(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}));

const translations = {
  desktop: {
    worktree_cleanup: {
      title: "Working copies on this machine",
      description: "Parallel mode makes one copy per task.",
      scanning: "Looking for working copies",
      daemon_offline: "Start the local daemon",
      load_failed: "Could not read the working copies",
      enable_title: "Clear out merged copies automatically",
      enable_description: "Off by default",
      min_age_title: "Keep for at least",
      min_age_description: "Days",
      trunk_title: "Merge into",
      trunk_description: "Branch",
      trunk_placeholder: "main",
      summary: "{{count}} copies · {{total}} on disk · {{reclaimable}} would be cleared",
      advisory: "Automatic cleanup is off, so nothing below will be removed.",
      empty: "No working copies yet",
      last_run: "last run {{days}}d ago",
      remove_now: "Clear now",
      removed: "Working copy cleared",
      reason_removable: "Merged, clean and idle",
      reason_removable_squash: "Squashed into trunk, clean and idle",
      reason_uncommitted: "Kept: it still has uncommitted changes",
      reason_unmerged: "Kept: its branch is not in the trunk branch yet",
      reason_too_recent: "Kept: its last run is too recent",
      reason_in_use: "Kept: a task is running in it right now",
      reason_not_ours: "Kept: Multica did not create this one",
      reason_unknown: "Kept: its state could not be read",
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

import { WorktreeCleanupSection } from "./worktree-cleanup-section";

function copy(over: Record<string, unknown> = {}) {
  return {
    path: "/Users/me/code/app.multica-worktrees/task-1",
    git_root: "/Users/me/code/app",
    branch: "agent/j/task-1",
    multica_created: true,
    in_use: false,
    last_run_at: "2026-08-01T00:00:00Z",
    dirty: false,
    merged: true,
    unknown: false,
    size_bytes: 3_500_000_000,
    ...over,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  (window as unknown as { desktopAPI: unknown }).desktopAPI = {
    worktreeCleanupReport: mocks.report,
    saveWorktreeCleanupSettings: mocks.saveSettings,
    removeWorktreeCopy: mocks.remove,
  };
});

describe("WorktreeCleanupSection", () => {
  // Invariant 6: the preview is shown while cleanup is OFF, and says plainly
  // that nothing will happen. That is what lets the feature default to off and
  // still be something a user can evaluate.
  it("previews what cleanup would do while it is switched off", async () => {
    mocks.report.mockResolvedValue({
      ok: true,
      report: {
        settings: { enabled: false, min_age_days: 14 },
        items: [copy(), copy({ path: "/kept", keep_reason: "uncommitted_changes" })],
      },
    });

    render(<WorktreeCleanupSection />);

    await screen.findByText(/would be cleared/);
    expect(
      screen.getByText(/Automatic cleanup is off, so nothing below will be removed/),
    ).toBeInTheDocument();
    expect(screen.getByRole("switch")).not.toBeChecked();
    // Every copy is listed with its verdict, including the ones being kept.
    expect(screen.getByText("Merged, clean and idle")).toBeInTheDocument();
    expect(
      screen.getByText("Kept: it still has uncommitted changes"),
    ).toBeInTheDocument();
  });

  // DENE-647: in a squash-merge repository the branch's commits are never in
  // trunk, so a copy that qualifies must not be described as "merged" — a user
  // who checks `git log` would find nothing and stop believing the list.
  it("says a squash-merged copy qualifies on its content, not its commits", async () => {
    mocks.report.mockResolvedValue({
      ok: true,
      report: {
        settings: { enabled: true, min_age_days: 14 },
        items: [
          copy({ path: "/squashed", merged_via: "squash" }),
          copy({ path: "/plain", merged_via: "ancestor" }),
        ],
      },
    });

    render(<WorktreeCleanupSection />);
    await screen.findByText(/would be cleared/);

    expect(
      screen.getByText("Squashed into trunk, clean and idle"),
    ).toBeInTheDocument();
    expect(screen.getByText("Merged, clean and idle")).toBeInTheDocument();
  });

  // Invariant 7's user-facing half: the button that removes a copy is simply
  // not offered for one the policy keeps. There is no affordance to click past
  // the rule, in either direction.
  it("offers Clear now only for copies the policy would remove", async () => {
    mocks.report.mockResolvedValue({
      ok: true,
      report: {
        settings: { enabled: true, min_age_days: 14 },
        items: [
          copy({ path: "/removable" }),
          copy({ path: "/dirty", keep_reason: "uncommitted_changes" }),
          copy({ path: "/busy", keep_reason: "in_use" }),
        ],
      },
    });

    render(<WorktreeCleanupSection />);
    await screen.findByText(/would be cleared/);

    const buttons = screen.getAllByRole("button", { name: /clear now/i });
    expect(buttons).toHaveLength(3);
    const enabled = buttons.filter((b) => !b.hasAttribute("disabled"));
    expect(enabled).toHaveLength(1);

    mocks.remove.mockResolvedValue({
      ok: true,
      report: { settings: { enabled: true, min_age_days: 14 }, items: [] },
    });
    fireEvent.click(enabled[0]!);
    await waitFor(() => expect(mocks.remove).toHaveBeenCalledWith("/removable"));
    expect(mocks.toastSuccess).toHaveBeenCalled();
    expect(await screen.findByText("No working copies yet")).toBeInTheDocument();
  });

  it("saves the switch and renders the report the daemon answers with", async () => {
    mocks.report.mockResolvedValue({
      ok: true,
      report: { settings: { enabled: false, min_age_days: 14 }, items: [copy()] },
    });
    mocks.saveSettings.mockResolvedValue({
      ok: true,
      report: { settings: { enabled: true, min_age_days: 14 }, items: [copy()] },
    });

    render(<WorktreeCleanupSection />);
    await screen.findByText(/would be cleared/);
    fireEvent.click(screen.getByRole("switch"));

    await waitFor(() =>
      expect(mocks.saveSettings).toHaveBeenCalledWith(
        expect.objectContaining({ enabled: true }),
      ),
    );
    // The advisory line is gone because the daemon's fresh report says the
    // policy is on now — the screen never predicts that itself.
    await waitFor(() =>
      expect(
        screen.queryByText(/Automatic cleanup is off/),
      ).not.toBeInTheDocument(),
    );
  });

  // A daemon that is not running is a state the user fixes, not an error to
  // apologise for — and it must not be reported as "no working copies", which
  // would say their disk is clear when nobody looked.
  it("says the daemon is offline instead of reporting an empty disk", async () => {
    mocks.report.mockResolvedValue({ ok: false, reason: "daemon_unavailable" });
    render(<WorktreeCleanupSection />);
    expect(await screen.findByText("Start the local daemon")).toBeInTheDocument();
    expect(screen.queryByText("No working copies yet")).not.toBeInTheDocument();
    expect(mocks.toastError).not.toHaveBeenCalled();
  });

  // A refusal carries a rule the user needs to read, so it surfaces rather
  // than being swallowed into a silent no-op.
  it("surfaces a refusal from the daemon", async () => {
    mocks.report.mockResolvedValue({
      ok: true,
      report: { settings: { enabled: true, min_age_days: 14 }, items: [copy()] },
    });
    mocks.remove.mockResolvedValue({
      ok: false,
      reason: "error",
      error: "refusing to remove: uncommitted_changes",
    });

    render(<WorktreeCleanupSection />);
    await screen.findByText(/would be cleared/);
    fireEvent.click(screen.getByRole("button", { name: /clear now/i }));

    await waitFor(() =>
      expect(mocks.toastError).toHaveBeenCalledWith(
        "refusing to remove: uncommitted_changes",
      ),
    );
    expect(mocks.toastSuccess).not.toHaveBeenCalled();
  });
});
