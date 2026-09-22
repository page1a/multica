/**
 * @vitest-environment jsdom
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import type { TaskLogExport } from "../types";
import { useReportTaskLogExport } from "./mutations";

function createWrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  };
}

const ARTIFACT = '{\n  "format": "multica.log-export"\n}\n';

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

  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    uploadFile = vi.fn().mockResolvedValue({ id: "attachment-7" });
    createComment = vi.fn().mockResolvedValue({ id: "comment-11" });
    setApiInstance({ uploadFile, createComment } as unknown as ApiClient);
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
});
