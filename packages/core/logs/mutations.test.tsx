/**
 * @vitest-environment jsdom
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import type { TaskLogExport, TaskLogExportReport } from "../types";
import { LogExportCommentError, useReportTaskLogExport } from "./mutations";

function createWrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  };
}

const ARTIFACT = '{\n  "format": "multica.log-export"\n}\n';

const PUSHED = {
  pushed: true,
  filename: "log-export-DENE-599.json",
  path: "logs/log-export-DENE-599.json",
  url: "https://github.com/o/r/blob/kun/logs/log-export-DENE-599.json",
  branch: "kun",
  repo: "https://github.com/o/r",
  summary_markdown: "## AI 摘要\n\n推送后的产物摘要。",
  entry_count: 3,
  run_count: 1,
  size_bytes: 10,
  redaction_complete: true,
  truncated: false,
};

function exported(): TaskLogExport {
  return {
    artifact: ARTIFACT,
    filename: "log-export-DENE-599.json",
    bundle: {
      format: "multica.log-export",
      version: 1,
      generated_at: "2026-09-21T12:00:00Z",
      task: {
        id: "task-1",
        issue_id: "issue-9",
        issue_identifier: "DENE-599",
        agent_name: "孙悟饭",
        exit_code: 1,
        scope: { kind: "run" },
        window: {},
      },
      run_count: 1,
      entry_count: 0,
      truncated: false,
      runs: [],
      entries: [],
      summary_markdown: "## AI 摘要\n\n运行以失败结束。",
    },
  };
}

describe("useReportTaskLogExport", () => {
  let qc: QueryClient;
  let uploadFile: ReturnType<typeof vi.fn>;
  let createComment: ReturnType<typeof vi.fn>;
  let pushTaskLogExport: ReturnType<typeof vi.fn>;
  let getIssue: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    uploadFile = vi.fn().mockResolvedValue({ id: "attachment-7" });
    createComment = vi.fn().mockResolvedValue({ id: "comment-11" });
    // Default to "no workspace repository": every pre-c4 expectation below
    // then describes the fallback path, which is still the ordinary one.
    pushTaskLogExport = vi.fn().mockRejectedValue(new Error("not configured"));
    getIssue = vi.fn().mockResolvedValue({
      assignee_type: "member",
      assignee_id: "user-9",
      creator_type: "member",
      creator_id: "user-1",
    });
    setApiInstance({ uploadFile, createComment, pushTaskLogExport, getIssue } as unknown as ApiClient);
  });

  afterEach(() => {
    qc.clear();
    vi.restoreAllMocks();
  });

  it("uploads the verbatim artifact and posts it as a comment attachment", async () => {
    const { result } = renderHook(() => useReportTaskLogExport(), {
      wrapper: createWrapper(qc),
    });

    await act(async () => {
      await result.current.mutateAsync({
        exported: exported(),
        issueId: "issue-9",
        mention: "[@负责人](mention://member/user-9)",
      });
    });

    expect(uploadFile).toHaveBeenCalledTimes(1);
    const [file, opts] = uploadFile.mock.calls[0] as [File, { issueId: string }];
    expect(file.name).toBe("log-export-DENE-599.json");
    expect(opts).toEqual({ issueId: "issue-9" });
    await expect(file.text()).resolves.toBe(ARTIFACT);

    expect(createComment).toHaveBeenCalledTimes(1);
    const [issueId, content, , , attachmentIds] = createComment.mock.calls[0] as [
      string,
      string,
      unknown,
      unknown,
      string[],
    ];
    expect(issueId).toBe("issue-9");
    expect(content).toContain("AI 摘要");
    expect(content).toContain("mention://member/user-9");
    expect(attachmentIds).toEqual(["attachment-7"]);
  });

  it("refreshes the issue timeline after reporting", async () => {
    const invalidate = vi.spyOn(qc, "invalidateQueries");
    const { result } = renderHook(() => useReportTaskLogExport(), {
      wrapper: createWrapper(qc),
    });

    await act(async () => {
      await result.current.mutateAsync({ exported: exported(), issueId: "issue-9" });
    });

    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["issues", "timeline", "issue-9"] });
  });

  it("fails loudly when the upload returns no attachment id", async () => {
    uploadFile.mockResolvedValue({ id: "" });
    const { result } = renderHook(() => useReportTaskLogExport(), {
      wrapper: createWrapper(qc),
    });

    await expect(
      act(async () => {
        await result.current.mutateAsync({ exported: exported(), issueId: "issue-9" });
      }),
    ).rejects.toThrow(/attachment id/i);
    expect(createComment).not.toHaveBeenCalled();
  });

  it("reports through the workspace repository when the push lands", async () => {
    pushTaskLogExport.mockResolvedValue({
      pushed: true,
      filename: "log-export-DENE-599.json",
      path: "logs/log-export-DENE-599.json",
      url: "https://github.com/o/r/blob/kun/logs/log-export-DENE-599.json",
      branch: "kun",
      repo: "https://github.com/o/r",
      summary_markdown: "## AI 摘要\n\n推送后的产物摘要。",
      entry_count: 3,
      run_count: 1,
      size_bytes: 10,
      redaction_complete: true,
      truncated: false,
    });
    const { result } = renderHook(() => useReportTaskLogExport(), {
      wrapper: createWrapper(qc),
    });

    let report: TaskLogExportReport | undefined;
    await act(async () => {
      report = await result.current.mutateAsync({
        exported: exported(),
        issueId: "issue-9",
        scope: "run",
      });
    });

    expect(report).toMatchObject({
      channel: "git",
      issueId: "issue-9",
      url: "https://github.com/o/r/blob/kun/logs/log-export-DENE-599.json",
    });
    // The push request carries the scope, never the artifact: the whole point
    // of this path is that a multi-megabyte body never crosses the upload path.
    expect(pushTaskLogExport).toHaveBeenCalledWith("task-1", { scope: "run", hours: undefined });
    expect(uploadFile).not.toHaveBeenCalled();
    const content = createComment.mock.calls[0]![1] as string;
    expect(content).toContain("推送后的产物摘要");
    expect(content).toContain("https://github.com/o/r/blob/kun/logs/log-export-DENE-599.json");
    expect(content).toContain("mention://member/user-9");
  });

  // The push landed, so the bytes are already in the repository. A comment
  // failure must not fall into the attachment path: that would POST the whole
  // multi-megabyte artifact over the very upload route the git channel exists
  // to avoid, and a failed upload would leave the committed file unlinked.
  it("hands back the pushed link when the comment fails, without uploading the artifact", async () => {
    pushTaskLogExport.mockResolvedValue(PUSHED);
    createComment.mockRejectedValue(new Error("comment request timed out"));
    const { result } = renderHook(() => useReportTaskLogExport(), {
      wrapper: createWrapper(qc),
    });

    let caught: unknown;
    await act(async () => {
      caught = await result.current
        .mutateAsync({ exported: exported(), issueId: "issue-9", scope: "run" })
        .catch((error: unknown) => error);
    });

    expect(caught).toBeInstanceOf(LogExportCommentError);
    const failure = caught as LogExportCommentError;
    expect(failure.push.url).toBe(PUSHED.url);
    expect(failure.reason).toContain("timed out");
    expect(uploadFile).not.toHaveBeenCalled();

    // The retry carries the landed push, so only the comment is attempted.
    createComment.mockResolvedValue({ id: "comment-12" });
    let report: TaskLogExportReport | undefined;
    await act(async () => {
      report = await result.current.mutateAsync({
        exported: exported(),
        issueId: "issue-9",
        scope: "run",
        pushed: PUSHED,
      });
    });

    expect(report).toMatchObject({
      channel: "git",
      commentId: "comment-12",
      url: PUSHED.url,
    });
    expect(pushTaskLogExport).toHaveBeenCalledTimes(1);
    expect(uploadFile).not.toHaveBeenCalled();
  });

  it("falls back to the comment attachment when the push fails, keeping the report", async () => {
    pushTaskLogExport.mockRejectedValue(new Error("push failed: remote rejected"));
    const { result } = renderHook(() => useReportTaskLogExport(), {
      wrapper: createWrapper(qc),
    });

    let report: TaskLogExportReport | undefined;
    await act(async () => {
      report = await result.current.mutateAsync({ exported: exported(), issueId: "issue-9" });
    });

    expect(report).toMatchObject({ channel: "attachment", issueId: "issue-9" });
    expect(report?.fallbackReason).toContain("remote rejected");
    const [file] = uploadFile.mock.calls[0] as [File];
    await expect(file.text()).resolves.toBe(ARTIFACT);
  });

  it("resolves the default mention from the issue and never names an agent", async () => {
    pushTaskLogExport.mockRejectedValue(new Error("not configured"));
    getIssue.mockResolvedValue({
      assignee_type: "agent",
      assignee_id: "agent-3",
      creator_type: "member",
      creator_id: "user-1",
    });
    const { result } = renderHook(() => useReportTaskLogExport(), {
      wrapper: createWrapper(qc),
    });

    await act(async () => {
      await result.current.mutateAsync({ exported: exported(), issueId: "issue-9" });
    });

    const content = createComment.mock.calls[0]![1] as string;
    expect(content).toContain("mention://member/user-1");
    expect(content).not.toContain("mention://agent/");
  });

  it("still reports when the issue lookup for the mention fails", async () => {
    getIssue.mockRejectedValue(new Error("offline"));
    const { result } = renderHook(() => useReportTaskLogExport(), {
      wrapper: createWrapper(qc),
    });

    await act(async () => {
      await result.current.mutateAsync({ exported: exported(), issueId: "issue-9" });
    });

    const content = createComment.mock.calls[0]![1] as string;
    expect(content).not.toContain("mention://");
    expect(createComment).toHaveBeenCalledTimes(1);
  });
});
