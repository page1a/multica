import { api, errorCode } from "@multica/core/api";
import type { ChatSendMode, SendChatMessageResponse } from "@multica/core/types";

/**
 * Sends a chat message in the chosen mode (DENE-1346). A steer the running
 * reply cannot take is refused before anything is stored, so it is resent as
 * a queued follow-up rather than lost; `steerFellBack` lets the caller say so.
 */
export async function sendChatMessageInMode(
  sessionId: string,
  content: string,
  attachmentIds: string[] | undefined,
  mode: ChatSendMode | undefined,
): Promise<{ result: SendChatMessageResponse; steerFellBack: boolean }> {
  try {
    return { result: await api.sendChatMessage(sessionId, content, attachmentIds, mode), steerFellBack: false };
  } catch (err) {
    if (mode !== "steer" || errorCode(err) !== "steer_unsupported") throw err;
    return { result: await api.sendChatMessage(sessionId, content, attachmentIds, "queue"), steerFellBack: true };
  }
}
