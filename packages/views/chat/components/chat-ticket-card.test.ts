import { describe, expect, it } from "vitest";
import type { ChatTicket } from "@multica/core/types";
import { groupChatTickets } from "./chat-ticket-card";

const ticket = (id: string, created_at: string): ChatTicket => ({
  id,
  identifier: id,
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
  source: "created",
  linked_at: created_at,
});

describe("groupChatTickets", () => {
  const messages = [
    { id: "u1", role: "user", created_at: "2026-10-08T10:00:00Z" },
    { id: "a1", role: "assistant", created_at: "2026-10-08T10:01:00Z" },
    { id: "u2", role: "user", created_at: "2026-10-08T10:02:00Z" },
    { id: "a2", role: "assistant", created_at: "2026-10-08T10:05:00Z" },
  ];

  it("hangs each ticket under the reply of the turn that opened it", () => {
    const { byMessage, tail } = groupChatTickets(messages, [
      ticket("T1", "2026-10-08T10:00:30Z"),
      ticket("T2", "2026-10-08T10:03:00Z"),
      ticket("T3", "2026-10-08T10:04:00Z"),
    ]);
    expect(byMessage.get("a1")?.map((t) => t.id)).toEqual(["T1"]);
    expect(byMessage.get("a2")?.map((t) => t.id)).toEqual(["T2", "T3"]);
    expect(byMessage.has("u2")).toBe(false);
    expect(tail).toEqual([]);
  });

  it("keeps a running turn's tickets for the list's last row", () => {
    const { byMessage, tail } = groupChatTickets(messages, [ticket("T4", "2026-10-08T10:06:00Z")]);
    expect(byMessage.size).toBe(0);
    expect(tail.map((t) => t.id)).toEqual(["T4"]);
  });

  it("hangs a followed old issue under the turn that touched it, not its birth", () => {
    const old: ChatTicket = { ...ticket("T5", "2026-09-01T00:00:00Z"), source: "auto", linked_at: "2026-10-08T10:03:00Z" };
    const { byMessage } = groupChatTickets(messages, [old]);
    expect(byMessage.get("a2")?.map((t) => t.id)).toEqual(["T5"]);
  });
});
