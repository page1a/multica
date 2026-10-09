import { describe, expect, it } from "vitest";
import {
  ChatPendingTaskSchema,
  ChatTicketsResponseSchema,
  SendChatMessageResponseSchema,
} from "./schemas";

describe("ChatPendingTaskSchema", () => {
  it("keeps the pending head and valid queue rows when one row is malformed", () => {
    const parsed = ChatPendingTaskSchema.parse({
      task_id: "task-active",
      status: "running",
      queued_tasks: [
        {
          task_id: "task-next",
          status: "queued",
          created_at: "2026-08-03T00:00:00Z",
        },
        { status: "queued" },
      ],
    });

    expect(parsed.task_id).toBe("task-active");
    expect(parsed.queued_tasks).toEqual([
      {
        task_id: "task-next",
        status: "queued",
        created_at: "2026-08-03T00:00:00Z",
      },
    ]);
  });
});

describe("SendChatMessageResponseSchema", () => {
  const base = {
    message_id: "message-1",
    task_id: "task-1",
    created_at: "2026-08-05T00:00:00Z",
  };

  it("keeps queue position parity with web", () => {
    expect(SendChatMessageResponseSchema.parse({ ...base, queued: false }).queued).toBe(false);
  });

  it("ignores a malformed additive queue position", () => {
    expect(SendChatMessageResponseSchema.parse({ ...base, queued: "no" }).queued).toBeUndefined();
  });
});

describe("ChatTicketsResponseSchema", () => {
  it("downgrades an unknown assignee_type to unassigned", () => {
    const parsed = ChatTicketsResponseSchema.parse({
      chat_session_id: "s1",
      tickets: [
        {
          id: "i1",
          identifier: "DENE-1",
          title: "Fix it",
          status: "todo",
          priority: "high",
          assignee_type: "robot",
          assignee_id: "x",
          created_at: "2026-10-01T00:00:00Z",
          updated_at: "2026-10-01T00:00:00Z",
        },
      ],
    });
    expect(parsed.tickets[0].assignee_type).toBeNull();
    expect(parsed.tickets[0].goal).toBeUndefined();
  });
});
