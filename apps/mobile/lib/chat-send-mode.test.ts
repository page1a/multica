import { describe, expect, it, vi } from "vitest";
import {
  defaultChatSendMode,
  effectiveChatSendMode,
  sendChatMessageInMode,
  steerCliName,
} from "./chat-send-mode";

const ok = { message_id: "m1", task_id: "t1", created_at: "2026-10-05T00:00:00Z" };

describe("chat send mode", () => {
  it("defaults to steer only when the running CLI supports it", () => {
    expect(defaultChatSendMode(true)).toBe("steer");
    expect(defaultChatSendMode(false)).toBe("queue");
    expect(defaultChatSendMode(undefined)).toBe("queue");
  });

  it("drops a chosen steer once it is no longer supported", () => {
    expect(effectiveChatSendMode("steer", false)).toBe("queue");
    expect(effectiveChatSendMode("restart", false)).toBe("restart");
    expect(effectiveChatSendMode(null, true)).toBe("steer");
  });

  it("names the CLI", () => {
    expect(steerCliName("codex")).toBe("Codex");
    expect(steerCliName("")).toBe("");
  });
});

describe("sendChatMessageInMode", () => {
  it("resends a refused steer as queue", async () => {
    const send = vi
      .fn()
      .mockRejectedValueOnce({ status: 409, body: { code: "steer_unsupported" } })
      .mockResolvedValueOnce(ok);
    const sent = await sendChatMessageInMode(send, "steer");
    expect(send).toHaveBeenNthCalledWith(1, "steer");
    expect(send).toHaveBeenNthCalledWith(2, "queue");
    expect(sent).toEqual({ result: ok, steerFellBack: true });
  });

  it("does not retry other failures", async () => {
    const err = { status: 403, body: { reason_code: "invocation_not_allowed" } };
    const send = vi.fn().mockRejectedValue(err);
    await expect(sendChatMessageInMode(send, "steer")).rejects.toBe(err);
    expect(send).toHaveBeenCalledTimes(1);
  });

  it("does not retry a refused queue or restart", async () => {
    const err = { status: 409, body: { code: "steer_unsupported" } };
    const send = vi.fn().mockRejectedValue(err);
    await expect(sendChatMessageInMode(send, "restart")).rejects.toBe(err);
    expect(send).toHaveBeenCalledTimes(1);
  });
});
