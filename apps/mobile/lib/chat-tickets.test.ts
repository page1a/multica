import { describe, expect, it } from "vitest";
import type { ChatTicket } from "@multica/core/types";
import { chatProgress, groupChatTickets } from "./chat-tickets";

const ticket = (
  id: string,
  created_at: string,
  rest: Partial<ChatTicket> = {},
): ChatTicket => ({
  id,
  source: "created",
  linked_at: created_at,
  identifier: id.toUpperCase(),
  title: id,
  status: "todo",
  priority: "none",
  assignee_type: null,
  assignee_id: null,
  created_at,
  updated_at: created_at,
  changed_at: created_at,
  phase: "in_progress",
  needs_you: false,
  ...rest,
});

describe("groupChatTickets", () => {
  const messages = [
    { id: "u1", role: "user", created_at: "2026-10-01T10:00:00Z" },
    { id: "a1", role: "assistant", created_at: "2026-10-01T10:01:00Z" },
    { id: "u2", role: "user", created_at: "2026-10-01T10:02:00Z" },
    { id: "a2", role: "assistant", created_at: "2026-10-01T10:03:00Z" },
  ];

  it("hangs each ticket under the first reply at or after it", () => {
    const { byMessage, tail } = groupChatTickets(messages, [
      ticket("t1", "2026-10-01T10:00:30Z"),
      ticket("t2", "2026-10-01T10:01:00Z"),
      ticket("t3", "2026-10-01T10:02:30Z"),
    ]);
    expect(byMessage.get("a1")?.map((t) => t.id)).toEqual(["t1", "t2"]);
    expect(byMessage.get("a2")?.map((t) => t.id)).toEqual(["t3"]);
    expect(byMessage.has("u1")).toBe(false);
    expect(tail).toEqual([]);
  });

  it("sends tickets from a still-running turn to the tail", () => {
    const { byMessage, tail } = groupChatTickets(messages, [
      ticket("t4", "2026-10-01T10:04:00Z"),
    ]);
    expect(byMessage.size).toBe(0);
    expect(tail.map((t) => t.id)).toEqual(["t4"]);
  });

  it("hangs a followed old issue under the reply of the turn that touched it", () => {
    const { byMessage } = groupChatTickets(messages, [
      ticket("old", "2026-09-01T00:00:00Z", {
        source: "auto",
        linked_at: "2026-10-01T10:02:30Z",
      }),
    ]);
    expect(byMessage.has("a1")).toBe(false);
    expect(byMessage.get("a2")?.map((t) => t.id)).toEqual(["old"]);
  });

  it("falls back to created_at when linked_at is empty", () => {
    const { byMessage } = groupChatTickets(messages, [
      ticket("t5", "2026-10-01T10:00:30Z", { linked_at: "" }),
    ]);
    expect(byMessage.get("a1")?.map((t) => t.id)).toEqual(["t5"]);
  });
});

describe("chatProgress", () => {
  const at = (min: number) => new Date(Date.UTC(2026, 0, 1, 12, min)).toISOString();
  const tk = (id: string, rest: Partial<ChatTicket>): ChatTicket => ({
    id, source: "created", linked_at: at(0), identifier: id, title: id, status: "todo", priority: "none",
    assignee_type: null, assignee_id: null, created_at: at(0), updated_at: at(0),
    changed_at: at(0), phase: "in_progress", needs_you: false, ...rest,
  });

  it("puts moves after the last look first, then waits-on-you, in progress, done", () => {
    const seenAt = Date.parse(at(30));
    const { rows, counts, fresh } = chatProgress(
      [
        tk("done-old", { from_status: "todo", status: "done", changed_at: at(10), phase: "done" }),
        tk("never-moved", {}),
        tk("fresh-a", { from_status: "todo", status: "in_progress", changed_at: at(40) }),
        tk("waiting", { from_status: "todo", status: "in_review", changed_at: at(20), phase: "waiting_you", needs_you: true }),
        tk("fresh-b", { from_status: "in_progress", status: "in_review", changed_at: at(50), phase: "waiting_you" }),
      ],
      seenAt,
    );
    expect(rows.map((r) => r.id)).toEqual(["fresh-b", "fresh-a", "waiting", "never-moved", "done-old"]);
    expect(fresh).toBe(2);
    expect(counts).toEqual({ waiting_you: 2, in_progress: 2, done: 1 });
  });
});
