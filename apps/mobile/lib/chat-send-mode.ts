/**
 * Send modes while a chat reply is running (DENE-1346, native in DENE-1362).
 * Mirrors `packages/views/chat/components/chat-send-mode.tsx` and
 * `send-chat-message.ts`: steer is read mid-reply by the same process, queue
 * runs after the reply, restart stops the reply and starts over from the
 * message. Pure so the vitest lane covers it.
 */
import type {
  ChatSendMode,
  SendChatMessageResponse,
} from "@multica/core/types";

export const CHAT_SEND_MODES: ChatSendMode[] = ["steer", "queue", "restart"];

/** The mode the send button uses while a reply is running: steer when the CLI can take it. */
export function defaultChatSendMode(
  steerSupported: boolean | undefined,
): ChatSendMode {
  return steerSupported ? "steer" : "queue";
}

/** A chosen mode that is no longer available falls back to the default. */
export function effectiveChatSendMode(
  chosen: ChatSendMode | null,
  steerSupported: boolean | undefined,
): ChatSendMode {
  if (chosen === "steer" && !steerSupported) return "queue";
  return chosen ?? defaultChatSendMode(steerSupported);
}

/** Capitalised CLI name for the "can't steer" line, e.g. `codex` → `Codex`. */
export function steerCliName(provider: string): string {
  return provider ? provider.charAt(0).toUpperCase() + provider.slice(1) : provider;
}

function isSteerUnsupported(err: unknown): boolean {
  const body = (err as { body?: unknown } | null)?.body;
  return (
    !!body &&
    typeof body === "object" &&
    (body as { code?: unknown }).code === "steer_unsupported"
  );
}

/**
 * Sends in the chosen mode. A steer the running reply cannot take is refused
 * before anything is stored, so it is resent as a queued follow-up rather than
 * lost; `steerFellBack` lets the caller say so.
 */
export async function sendChatMessageInMode(
  send: (mode: ChatSendMode | undefined) => Promise<SendChatMessageResponse>,
  mode: ChatSendMode | undefined,
): Promise<{ result: SendChatMessageResponse; steerFellBack: boolean }> {
  try {
    return { result: await send(mode), steerFellBack: false };
  } catch (err) {
    if (mode !== "steer" || !isSteerUnsupported(err)) throw err;
    return { result: await send("queue"), steerFellBack: true };
  }
}
