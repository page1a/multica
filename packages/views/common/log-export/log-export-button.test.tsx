// @vitest-environment jsdom

import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import type { AgentTask } from "@multica/core/types/agent";
import { renderWithI18n } from "../../test/i18n";
import { IssueLogExportButton, TaskLogExportButton } from "./log-export-button";

function task(overrides: Partial<AgentTask> = {}): AgentTask {
  return {
    id: "task-1",
    agent_id: "agent-1",
    runtime_id: "runtime-1",
    issue_id: "issue-9",
    status: "completed",
    priority: 0,
    dispatched_at: null,
    started_at: "2026-09-21T11:00:00Z",
    completed_at: "2026-09-21T11:05:00Z",
    created_at: "2026-09-21T10:59:00Z",
    result: null,
    error: null,
    ...overrides,
  } as AgentTask;
}

let listTasksByIssue: ReturnType<typeof vi.fn>;
let exportTaskLogs: ReturnType<typeof vi.fn>;

beforeEach(() => {
  listTasksByIssue = vi.fn().mockResolvedValue([]);
  exportTaskLogs = vi.fn().mockResolvedValue({
    artifact: "{}\n",
    filename: "log-export.json",
    bundle: {
      format: "multica.log-export",
      version: 1,
      generated_at: "",
      task: {
        id: "task-2",
        issue_id: "issue-9",
        issue_identifier: "DENE-599",
        scope: { kind: "run" },
        window: {},
      },
      run_count: 1,
      entry_count: 0,
      truncated: false,
      runs: [],
      entries: [],
      summary_markdown: "",
    },
  });
  setApiInstance({ listTasksByIssue, exportTaskLogs } as unknown as ApiClient);
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

function wrap(ui: React.ReactElement) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return renderWithI18n(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>);
}

describe("TaskLogExportButton", () => {
  it("opens the dialog for its own run", async () => {
    wrap(<TaskLogExportButton task={task({ id: "task-7" })} />);

    fireEvent.click(screen.getByRole("button", { name: "Export logs" }));
    fireEvent.click(screen.getByRole("button", { name: "Export logs" }));

    await waitFor(() =>
      expect(exportTaskLogs).toHaveBeenCalledWith(
        "task-7",
        expect.objectContaining({ scope: "run" }),
      ),
    );
  });
});

describe("IssueLogExportButton", () => {
  it("renders nothing while the issue has no runs", async () => {
    wrap(<IssueLogExportButton issueId="issue-9" />);

    await waitFor(() => expect(listTasksByIssue).toHaveBeenCalledWith("issue-9"));
    expect(screen.queryByRole("button", { name: "Export logs" })).not.toBeInTheDocument();
  });

  it("exports the newest run, not the first row", async () => {
    // The API returns rows in its own order; the button must sort by the
    // timestamp the row actually carries.
    listTasksByIssue.mockResolvedValue([
      task({ id: "task-new", started_at: "2026-09-21T18:00:00Z" }),
      task({ id: "task-old", started_at: "2026-09-21T09:00:00Z" }),
    ]);
    wrap(<IssueLogExportButton issueId="issue-9" />);

    const button = await screen.findByRole("button", { name: "Export logs" });
    fireEvent.click(button);
    fireEvent.click(screen.getByRole("button", { name: "Export logs" }));

    await waitFor(() =>
      expect(exportTaskLogs).toHaveBeenCalledWith(
        "task-new",
        expect.objectContaining({ scope: "run" }),
      ),
    );
  });
});
