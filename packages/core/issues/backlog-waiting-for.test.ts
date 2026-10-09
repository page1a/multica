// @vitest-environment node
import { describe, expect, it } from "vitest";
import { backlogWaitingFor } from "./backlog-waiting-for";

const metadata = { "backlog.waiting_for": "等公司注册办好" };

describe("backlogWaitingFor", () => {
  it("shows the reason on a backlog ticket", () => {
    expect(backlogWaitingFor({ status: "backlog", status_category: "unstarted", metadata })).toBe("等公司注册办好");
  });

  it("shows it on a custom status in the unstarted category", () => {
    expect(backlogWaitingFor({ status: "parked", status_category: "unstarted", metadata })).toBe("等公司注册办好");
  });

  it("hides a stale reason once the ticket is todo or started", () => {
    expect(backlogWaitingFor({ status: "todo", status_category: "unstarted", metadata })).toBe("");
    expect(backlogWaitingFor({ status: "in_progress", status_category: "started", metadata })).toBe("");
  });

  it("is empty without a reason", () => {
    expect(backlogWaitingFor({ status: "backlog", metadata: {} })).toBe("");
    expect(backlogWaitingFor({ status: "backlog", metadata: { "backlog.waiting_for": " " } })).toBe("");
  });
});
