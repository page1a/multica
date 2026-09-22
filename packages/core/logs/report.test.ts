// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { TaskLogExportBundle } from "../types";
import { buildLogExportReportComment, logExportOwnerMention } from "./report";

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
      agent_id: "agent-1",
      agent_name: "孙悟饭",
      status: "failed",
      exit_code: 1,
      scope: { kind: "run" },
      window: { from: "2026-09-21T11:59:00Z", to: "2026-09-21T12:00:00Z" },
    },
    run_count: 1,
    entry_count: 3,
    truncated: false,
    runs: [],
    entries: [],
    summary_markdown: "## AI 摘要\n\nDENE-599 的运行 task-1 以失败结束。",
    ...overrides,
  };
}

describe("logExportOwnerMention", () => {
  it("builds a member mention link", () => {
    expect(logExportOwnerMention("member", "user-9")).toBe("[@负责人](mention://member/user-9)");
  });

  it("builds an agent mention link with a custom label", () => {
    expect(logExportOwnerMention("agent", "agent-3", "孙悟饭")).toBe(
      "[@孙悟饭](mention://agent/agent-3)",
    );
  });
});

describe("buildLogExportReportComment", () => {
  it("stacks the summary and the mention with one blank line between", () => {
    const body = buildLogExportReportComment(bundle(), logExportOwnerMention("member", "user-9"));
    expect(body).toBe(
      "## AI 摘要\n\nDENE-599 的运行 task-1 以失败结束。\n\n[@负责人](mention://member/user-9)",
    );
  });

  it("accepts a plain bundle as well as a fetched export", () => {
    const direct = buildLogExportReportComment(bundle());
    const wrapped = buildLogExportReportComment({ bundle: bundle() });
    expect(direct).toBe(wrapped);
  });

  it("omits the mention when none was resolved", () => {
    const body = buildLogExportReportComment(bundle(), "   ");
    expect(body).not.toContain("mention://");
    expect(body.endsWith("\n")).toBe(false);
  });

  it("falls back to the issue identifier when the server sent no summary", () => {
    const body = buildLogExportReportComment(bundle({ summary_markdown: "  " }));
    expect(body).toBe("运行日志导出 · DENE-599");
  });

  it("never returns an empty body", () => {
    const empty = bundle({
      summary_markdown: "",
      task: { ...bundle().task, issue_identifier: "", id: "" },
    });
    expect(buildLogExportReportComment(empty)).toBe("运行日志导出");
  });
});
