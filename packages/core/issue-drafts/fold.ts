import type { ChatMessage, IssueDraftPayload, IssueDraftStatus } from "../types";
import { sameIssueDraftChildren } from "./group";
import { mergeIssueDraftPayload, parseIssueDraftBlock } from "./protocol";

/**
 * Whether the carrier's latest reply should be written to the server draft, and
 * what that write would be.
 *
 * The alignment draft belongs to the server — it is what finalize reads — but
 * the carrier's proposals arrive in the browser as message content. "The
 * question was answered, so the draft changed" therefore has to be folded back
 * on its own; leaving it to a button press means a refresh loses an agreement
 * that was already visible on screen.
 *
 * The rules live here, as a pure function, because each one is a way to lose the
 * user's work if it is wrong, and the interesting cases (an edit in progress, a
 * reply already folded, a turn still running) are exactly the ones a rendered
 * test cannot easily produce:
 *
 *   - `localDirty` wins over everything. The preview panel is the user's own
 *     hand on the draft; a reply must never be written over an unsaved edit.
 *   - One fold per reply. Folding is idempotent in content but not in effect —
 *     each write bumps the revision — so a re-render must not write again.
 *   - A reply that changes nothing is not written at all, for the same reason.
 *   - `ready` is preserved. A conversation that already produced a confirmable
 *     draft keeps that place on the stage strip while it is refined further.
 */
export interface IssueDraftFold {
  /** The reply this fold came from — recorded so it is folded at most once. */
  replyId: string;
  /** The draft to persist: the carrier's proposal merged into what is stored. */
  draft: IssueDraftPayload;
  /**
   * The status to persist alongside it. Never downgrades `ready` to `draft`:
   * the carrier does not decide how far the alignment has got.
   */
  status: "draft" | "ready";
}

export function planIssueDraftFold(input: {
  messages: readonly ChatMessage[];
  /** The draft as the server holds it. */
  draft: IssueDraftPayload | null;
  /** Where the server says this draft has got to. */
  status: IssueDraftStatus;
  /** The revision the fold would be written against. */
  revision: number | null;
  /** The preview panel holds unsaved edits. */
  localDirty: boolean;
  /** The reply already folded, if any. */
  appliedReplyId: string | null;
  /** A turn is running, or a write of our own is already in flight. */
  busy: boolean;
}): IssueDraftFold | null {
  if (input.busy || input.localDirty) return null;
  if (input.draft === null || input.revision === null) return null;
  if (input.status !== "draft" && input.status !== "ready") return null;

  const reply = latestFoldableReply(input.messages);
  if (!reply || reply.id === input.appliedReplyId) return null;

  const merged = mergeIssueDraftPayload(
    input.draft,
    parseIssueDraftBlock(reply.content),
  );
  // A reply that changes none of the four fields produces no fold, so the
  // caller keeps no record of it and will consider it again on the next render.
  // That is deliberate: the decision stays stateless, and re-deciding costs one
  // regex match against a message that is already in memory.
  if (sameIssueDraftValues(merged, input.draft)) return null;

  return {
    replyId: reply.id,
    draft: merged,
    status: input.status === "ready" ? "ready" : "draft",
  };
}

/** The newest carrier reply that carries a draft proposal. */
function latestFoldableReply(
  messages: readonly ChatMessage[],
): ChatMessage | null {
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    const message = messages[index];
    if (!message || message.role !== "assistant") continue;
    if (parseIssueDraftBlock(message.content) !== null) return message;
  }
  return null;
}

/** Whether two drafts carry the same server-read fields. */
export function sameIssueDraftValues(
  a: IssueDraftPayload,
  b: IssueDraftPayload,
): boolean {
  return (
    a.title === b.title &&
    a.description === b.description &&
    a.status === b.status &&
    a.priority === b.priority &&
    // The group is one of those fields since DENE-411: a reply that only
    // rewrote the sub-issues changed the draft, and a fold that ignored it
    // would leave the preview showing a group the server does not have.
    sameIssueDraftChildren(a.children ?? [], b.children ?? []) &&
    sameProjectProposal(a.project_proposal, b.project_proposal)
  );
}

function sameProjectProposal(
  a: IssueDraftPayload["project_proposal"],
  b: IssueDraftPayload["project_proposal"],
): boolean {
  if (!a && !b) return true;
  if (!a || !b) return false;
  return (
    a.action === b.action &&
    a.name === b.name &&
    (a.icon ?? null) === (b.icon ?? null) &&
    (a.description ?? null) === (b.description ?? null)
  );
}
