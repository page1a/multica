// @vitest-environment jsdom

import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import type { TaskLogExport, TaskLogExportBundle } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { LogExportDialog } from "./log-export-dialog";

// The dialog is the visible half of DENE-599, so these tests drive it through
// the real hooks and a stubbed API client: what is asserted is what the reader
// sees after a real request resolves, fails, or comes back empty — not a
// hand-fed prop.

function bundle(overrides: Partial<TaskLogExportBundle> = {}): TaskLogExportBundle {
  return {
    format: "multica.log-export",
    version: 1,
    generated_at: "2026-09-21T12:00:00Z",
    task: {
      id: "task-1",
      issue_id: "issue-9",
      issue_identifier: "DENE-599",
      issue_title: "任务日志一键导出并上报",
      agent_name: "孙悟饭",
      status: "failed",
      exit_code: 1,
      scope: { kind: "run" },
      window: { from: "", to: "" },
    },
    run_count: 1,
    entry_count: 3,
    truncated: false,
    runs: [],
    entries: [],
    summary_markdown: "## AI 摘要\n\n运行以退出码 1 结束。",
    ...overrides,
  };
}

function exported(overrides: Partial<TaskLogExportBundle> = {}): TaskLogExport {
  return {
    artifact: '{"format":"multica.log-export"}\n',
    filename: "log-export-DENE-599.json",
    bundle: bundle(overrides),
  };
}

const PUSH = {
  pushed: true,
  filename: "log-export-DENE-599.json",
  path: "logs/log-export-DENE-599.json",
  url: "https://github.com/o/r/blob/kun/logs/log-export-DENE-599.json",
  branch: "kun",
  repo: "https://github.com/o/r",
  summary_markdown: "## AI 摘要\n\n推送的产物。",
  entry_count: 3,
  run_count: 1,
  size_bytes: 900,
  redaction_complete: true,
  truncated: false,
};

let api: {
  exportTaskLogs: ReturnType<typeof vi.fn>;
  pushTaskLogExport: ReturnType<typeof vi.fn>;
  uploadFile: ReturnType<typeof vi.fn>;
  createComment: ReturnType<typeof vi.fn>;
  getIssue: ReturnType<typeof vi.fn>;
};

beforeEach(() => {
  api = {
    exportTaskLogs: vi.fn().mockResolvedValue(exported()),
    pushTaskLogExport: vi.fn().mockRejectedValue(new Error("not configured")),
    uploadFile: vi.fn().mockResolvedValue({ id: "attachment-7" }),
    createComment: vi.fn().mockResolvedValue({ id: "comment-11" }),
    getIssue: vi.fn().mockResolvedValue({
      assignee_type: "member",
      assignee_id: "user-9",
      creator_type: "member",
      creator_id: "user-1",
    }),
  };
  setApiInstance(api as unknown as ApiClient);
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

function renderDialog(props: Partial<Parameters<typeof LogExportDialog>[0]> = {}) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return renderWithI18n(
    <QueryClientProvider client={qc}>
      <LogExportDialog open onOpenChange={() => {}} taskId="task-1" {...props} />
    </QueryClientProvider>,
  );
}

const exportButton = () =>
  screen.getByRole("button", { name: "Export logs" });

describe("LogExportDialog", () => {
  it("starts on the prompt and shows the bundle card after exporting", async () => {
    renderDialog();

    expect(screen.getByText("Nothing exported yet")).toBeInTheDocument();
    expect(api.exportTaskLogs).not.toHaveBeenCalled();

    fireEvent.click(exportButton());

    await screen.findByText("log-export-DENE-599.json");
    expect(api.exportTaskLogs).toHaveBeenCalledWith(
      "task-1",
      expect.objectContaining({ scope: "run" }),
    );
    expect(screen.getByText("3 entries")).toBeInTheDocument();
    expect(screen.getByText("1 runs")).toBeInTheDocument();
    // The bundle's own summary is what the reader judges before sending it.
    expect(screen.getByText(/运行以退出码 1 结束/)).toBeInTheDocument();
  });

  it("exports the lookback window when the hours range is picked", async () => {
    renderDialog();

    fireEvent.click(screen.getByRole("button", { name: /Last 6h/ }));
    fireEvent.click(exportButton());

    await waitFor(() =>
      expect(api.exportTaskLogs).toHaveBeenCalledWith(
        "task-1",
        expect.objectContaining({ scope: "hours", hours: 6 }),
      ),
    );
  });

  it("never claims a bundle is clean when the server did not say so", async () => {
    api.exportTaskLogs.mockResolvedValue(
      exported({
        redaction: {
          pattern_rules: true,
          env_deny_list: false,
          complete: false,
          note: "部分 agent 的环境变量无法解析",
        },
      } as Partial<TaskLogExportBundle>),
    );
    renderDialog();

    fireEvent.click(exportButton());

    await screen.findByText("Redaction incomplete — do not forward as is");
    expect(screen.queryByText("Redacted automatically")).not.toBeInTheDocument();
  });

  it("shows the empty range with a way to widen it", async () => {
    api.exportTaskLogs
      .mockResolvedValueOnce(exported({ entry_count: 0, task: { ...bundle().task, scope: { kind: "hours", hours: 6 } } }))
      .mockResolvedValueOnce(exported());
    renderDialog();

    fireEvent.click(screen.getByRole("button", { name: /Last 6h/ }));
    fireEvent.click(exportButton());

    await screen.findByText("No logs in this range");
    fireEvent.click(screen.getByRole("button", { name: "Widen to the whole task" }));

    await screen.findByText("log-export-DENE-599.json");
    expect(api.exportTaskLogs).toHaveBeenLastCalledWith(
      "task-1",
      expect.objectContaining({ scope: "task" }),
    );
  });

  it("reports the failure and offers only the ways out that exist", async () => {
    api.exportTaskLogs.mockRejectedValue(new Error("导出服务暂时不可用"));
    renderDialog();

    fireEvent.click(exportButton());

    await screen.findByText("The export did not finish");
    expect(screen.getByText("导出服务暂时不可用")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
    // No earlier bundle exists, so there is no half-finished document to hand
    // over and the button is absent rather than present-and-useless.
    expect(
      screen.queryByRole("button", { name: "Export what was collected" }),
    ).not.toBeInTheDocument();
  });

  it("translates the browser's own network sentence instead of echoing it", async () => {
    // What a dropped connection actually throws. A localized panel must not
    // print the browser's English, but a server-supplied reason must survive.
    api.exportTaskLogs.mockRejectedValue(new TypeError("Failed to fetch"));
    renderDialog();

    fireEvent.click(exportButton());

    await screen.findByText("The export did not finish");
    expect(screen.getByText("Export failed")).toBeInTheDocument();
    expect(screen.queryByText("Failed to fetch")).not.toBeInTheDocument();
  });

  it("keeps a single filled action once a bundle is on screen", async () => {
    renderDialog();

    fireEvent.click(exportButton());
    await screen.findByText("log-export-DENE-599.json");

    // Reporting is the forward action now; re-exporting is the alternative.
    const report = screen.getByRole("button", {
      name: "Report as a comment attachment and @ the owner",
    });
    const reexport = screen.getByRole("button", { name: "Export again" });
    expect(report.className).toContain("bg-brand");
    expect(reexport.className).not.toContain("bg-brand");
  });

  it("keeps the last finished bundle reachable after a failed re-export", async () => {
    api.exportTaskLogs
      .mockResolvedValueOnce(exported())
      .mockRejectedValueOnce(new Error("boom"));
    renderDialog();

    fireEvent.click(exportButton());
    await screen.findByText("log-export-DENE-599.json");

    fireEvent.click(screen.getByRole("button", { name: "Export again" }));
    await screen.findByText("The export did not finish");

    fireEvent.click(screen.getByRole("button", { name: "Export what was collected" }));

    await screen.findByText("log-export-DENE-599.json");
    expect(screen.getByText("This is the last bundle that finished.")).toBeInTheDocument();
  });

  it("reports through the workspace repository and names where it landed", async () => {
    api.pushTaskLogExport.mockResolvedValue(PUSH);
    renderDialog();

    fireEvent.click(exportButton());
    await screen.findByText("log-export-DENE-599.json");
    fireEvent.click(
      screen.getByRole("button", {
        name: "Report as a comment attachment and @ the owner",
      }),
    );

    await screen.findByText(/Reported on DENE-599/);
    expect(api.pushTaskLogExport).toHaveBeenCalledWith("task-1", {
      scope: "run",
      hours: undefined,
    });
    // The repository path never uploads the artifact from the browser.
    expect(api.uploadFile).not.toHaveBeenCalled();
    expect(screen.getByRole("link", { name: "Open the bundle" })).toHaveAttribute(
      "href",
      PUSH.url,
    );
  });

  it("drops the bundle when the lookback hours change", async () => {
    renderDialog();

    fireEvent.click(screen.getByRole("button", { name: /Last 6h/ }));
    fireEvent.click(exportButton());
    await screen.findByText("log-export-DENE-599.json");

    // The card answers the 6h window. Changing the number to 24 without
    // re-exporting must not leave a 6h card that a report would send as 24h.
    fireEvent.change(screen.getByRole("spinbutton"), { target: { value: "24" } });

    expect(screen.queryByText("log-export-DENE-599.json")).not.toBeInTheDocument();
    expect(screen.getByText("Nothing exported yet")).toBeInTheDocument();

    fireEvent.click(exportButton());
    await waitFor(() =>
      expect(api.exportTaskLogs).toHaveBeenLastCalledWith(
        "task-1",
        expect.objectContaining({ scope: "hours", hours: 24 }),
      ),
    );
  });

  it("refuses a late result when the range changed while the export was in flight", async () => {
    let landExport: (value: TaskLogExport) => void = () => {};
    api.exportTaskLogs.mockReturnValueOnce(
      new Promise<TaskLogExport>((resolve) => {
        landExport = resolve;
      }),
    );
    renderDialog();

    // Ask for the 6h window, then edit the number while the request is still
    // on the wire. The response describes 6h; installing it under a 24h input
    // would leave a card the reader can copy and download while a report
    // rebuilds 24h on the server — the comment would carry a document nobody
    // reviewed.
    fireEvent.click(screen.getByRole("button", { name: /Last 6h/ }));
    fireEvent.click(exportButton());
    fireEvent.change(screen.getByRole("spinbutton"), { target: { value: "24" } });

    landExport(exported());

    await screen.findByText("Nothing exported yet");
    expect(screen.queryByText("log-export-DENE-599.json")).not.toBeInTheDocument();
    // No card means no report: the action only exists over a bundle the
    // reader can see.
    expect(
      screen.queryByRole("button", {
        name: "Report as a comment attachment and @ the owner",
      }),
    ).not.toBeInTheDocument();

    // Only a fresh export, which carries the number now on screen, may fill
    // the card back in.
    fireEvent.click(exportButton());
    await waitFor(() =>
      expect(api.exportTaskLogs).toHaveBeenLastCalledWith(
        "task-1",
        expect.objectContaining({ scope: "hours", hours: 24 }),
      ),
    );
  });

  it("keeps a bundle whose hours were clamped back to the same window", async () => {
    renderDialog();

    fireEvent.click(screen.getByRole("button", { name: /Last 6h/ }));
    fireEvent.click(exportButton());
    await screen.findByText("log-export-DENE-599.json");

    // 5.6 rounds back to the current 6, so the range is unchanged and the
    // keystroke must not throw the card away. (The input fires on every edit,
    // so a reader can land here by typing.)
    fireEvent.change(screen.getByRole("spinbutton"), { target: { value: "5.6" } });

    expect(screen.getByText("log-export-DENE-599.json")).toBeInTheDocument();
    expect(
      screen.getByRole("button", {
        name: "Report as a comment attachment and @ the owner",
      }),
    ).toBeInTheDocument();
  });

  it("keeps the pushed link and retries only the comment when the comment fails", async () => {
    api.pushTaskLogExport.mockResolvedValue(PUSH);
    api.createComment.mockRejectedValueOnce(new Error("评论请求超时"));
    renderDialog();

    fireEvent.click(exportButton());
    await screen.findByText("log-export-DENE-599.json");
    fireEvent.click(
      screen.getByRole("button", {
        name: "Report as a comment attachment and @ the owner",
      }),
    );

    // The bundle is committed already: nothing is uploaded, and the link the
    // reader would otherwise lose is on screen with the reason.
    await screen.findByText(/评论请求超时/);
    expect(api.uploadFile).not.toHaveBeenCalled();
    expect(screen.getByRole("link", { name: "Open the bundle" })).toHaveAttribute(
      "href",
      PUSH.url,
    );

    fireEvent.click(screen.getByRole("button", { name: "Resend the comment" }));
    await screen.findByText(/Reported on DENE-599/);
    // The retry sends the comment only — the repository is not contacted again
    // and the artifact never crosses the upload path.
    expect(api.pushTaskLogExport).toHaveBeenCalledTimes(1);
    expect(api.uploadFile).not.toHaveBeenCalled();
  });

  it("falls back to a comment attachment without losing the report", async () => {
    // The shape the server actually returns: a git stderr transcript whose
    // first line is the temp directory and whose last useful line is the
    // fatal clause. The panel must quote the clause, not the transcript.
    api.pushTaskLogExport.mockRejectedValue(
      new Error(
        "log export push failed: git clone failed: Cloning into " +
          "'/var/tmp/multica-log-export-3090098142'...\n" +
          "fatal: could not read Username for 'https://github.com': terminal prompts disabled",
      ),
    );
    renderDialog();

    fireEvent.click(exportButton());
    await screen.findByText("log-export-DENE-599.json");
    fireEvent.click(
      screen.getByRole("button", {
        name: "Report as a comment attachment and @ the owner",
      }),
    );

    await screen.findByText(/the bundle is a comment attachment/);
    expect(api.uploadFile).toHaveBeenCalledTimes(1);
    expect(
      screen.getByText(/could not read Username for 'https:\/\/github.com'/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/multica-log-export-3090098142/)).not.toBeInTheDocument();
  });

  it("says an unlinked run cannot be reported", async () => {
    api.exportTaskLogs.mockResolvedValue(
      exported({ task: { ...bundle().task, issue_id: "", issue_identifier: "" } }),
    );
    renderDialog({ issueId: undefined });

    fireEvent.click(exportButton());
    await screen.findByText("log-export-DENE-599.json");

    expect(
      screen.getByText("This run is not linked to an issue, so it cannot be reported."),
    ).toBeInTheDocument();
  });
});
