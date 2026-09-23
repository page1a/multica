import type { ChatMessage, IssueDraftChild, IssueDraftPayload } from "../types";
import {
  ISSUE_DRAFT_MAX_CHILDREN,
  normalizeIssueDraftChildren,
} from "./group";

/**
 * Wire format between the alignment page and the hidden `issue_draft:*` carrier.
 *
 * The carrier's instructions are fixed server-side (the policy registry in
 * server/internal/handler/issue_draft_policy.go) and mandate exactly one
 * `<issue_draft>{...}</issue_draft>` block at the end of every reply, so the
 * block is a contract, not a rendering detail: it has to be parsed into the
 * preview and stripped before the message reaches the screen.
 *
 * Outbound is the mirror image. The instructions say to "preserve good existing
 * draft fields supplied in the user's message", so each turn re-states the
 * current draft alongside the question — otherwise every reply would rebuild
 * the draft from the last message alone and quietly drop what was already
 * agreed.
 *
 * Both directions parse defensively. A CLI-backed model can emit slightly
 * malformed JSON, and a block that fails to parse must degrade to "no preview
 * update" rather than breaking the conversation.
 */

const DRAFT_INPUT_PREFIX = "MULTICA_ISSUE_DRAFT_INPUT\n";

/**
 * The two machine-readable blocks the carrier is told to emit, draft first so a
 * still-open draft block takes the rest of the reply with it before the question
 * block is looked for. Separate kinds because they mean different things: the
 * draft is a partial update to a structured object, while a question is a
 * turn-level affordance that has to disappear once it is answered.
 */
const DIRECTIVE_TAGS = ["issue_draft", "issue_draft_question"] as const;

/** A complete `<tag>…</tag>` block, with the whitespace separating it from prose. */
function completeBlockPattern(tag: string): RegExp {
  return new RegExp(`\\s*<${tag}>[\\s\\S]*?</${tag}>`, "g");
}

/**
 * An unterminated block: the reply is still streaming, or the model never
 * closed it. Everything after the opening tag is machine-readable, so it goes
 * too — a streaming reply must not leak markup into the transcript.
 */
function openBlockPattern(tag: string): RegExp {
  return new RegExp(`\\s*<${tag}>[\\s\\S]*$`);
}

/** A Markdown fence: ``` or ~~~ plus whatever info string follows it. */
const FENCE = "[\\x60\\x7e]{3,}";

/**
 * A fence that now encloses nothing. The instructions ask the model not to wrap
 * its blocks in Markdown fences; a CLI-backed one does it anyway, and removing
 * only the block would leave an empty code box in the transcript — the same
 * artifact the removal exists to prevent. Only fences this pass emptied are
 * matched, so an ordinary fenced code block in the prose survives.
 */
const EMPTY_FENCE_PAIR = new RegExp(
  `\\s*${FENCE}[^\\n]*\\n(?:[ \\t]*\\n)*[ \\t]*${FENCE}[ \\t]*(?=\\n|$)`,
  "g",
);

/** An opening fence left dangling at the end by a block that never closed. */
const DANGLING_FENCE_OPEN = new RegExp(`\\s*${FENCE}[^\\n]*[ \\t]*$`);

/** At most this many answers are offered per question. */
const MAX_QUESTION_OPTIONS = 6;

/**
 * Fields the carrier may revise. Every one is optional: the block is a partial
 * update, and an omitted or empty field means "no opinion", never "clear it".
 * That distinction is what stops a model that only restates the title from
 * wiping the description the user already approved.
 */
export type IssueDraftPatch = Partial<IssueDraftPayload>;

/**
 * The last `<issue_draft>` block in a reply, parsed. The last one wins because
 * a model that thinks out loud may draft twice; the instructions ask for the
 * final state.
 */
export function parseIssueDraftBlock(content: string): IssueDraftPatch | null {
  const matches = [...content.matchAll(completeBlockPattern("issue_draft"))];
  const raw = matches[matches.length - 1]?.[0];
  if (!raw) return null;
  const inner = raw.replace(/^\s*<issue_draft>/, "").replace(/<\/issue_draft>\s*$/, "");
  const parsed = parseJsonObject(inner);
  if (!parsed) return null;
  const patch: IssueDraftPatch = {};
  for (const field of ["title", "description", "status", "priority"] as const) {
    const value = parsed[field];
    if (typeof value === "string" && value.trim().length > 0) {
      patch[field] = value;
    }
  }
  const children = parseIssueDraftChildren(parsed.children);
  if (children !== undefined) patch.children = children;
  return patch;
}

/**
 * The sub-issue array of a draft block, parsed and normalized.
 *
 * `undefined` means "this block said nothing about sub-issues", which is NOT the
 * same as "there are none": the block is a partial update, and a carrier that
 * only restated the title must not delete the group the user already has. An
 * explicit `[]` is a statement — an alignment that turned out to be one issue —
 * and it does clear them, which is what makes "the conversation went back to a
 * single issue" expressible at all.
 *
 * Two things are repaired rather than trusted, because both would otherwise
 * cost the user a row at confirm time:
 *
 *   - a missing key is minted (`c1`, `c2`, …). The identity model needs a key
 *     per sub-issue, and the server rejects a keyless one — an empty row we can
 *     still repair is better than a 400 the user cannot act on.
 *   - a missing title drops that entry. A sub-issue with no title is one the
 *     server refuses, and unlike a missing key there is nothing to guess; the
 *     row would be a line in the preview that can never be created.
 *
 * An assignee the carrier names is ignored. It has no roster and its
 * instructions forbid it; the preview panel is the only thing that may choose a
 * real assignee. A wrong id that reaches the server is refused by
 * `validateAssigneePair`, and since DENE-694 that refusal costs only the one
 * row — the issue is created unassigned and reported back as an assignment
 * warning — but the carrier still never gets to name a seat.
 */
function parseIssueDraftChildren(raw: unknown): IssueDraftChild[] | undefined {
  if (!Array.isArray(raw)) return undefined;
  const children: IssueDraftChild[] = [];
  for (const entry of raw) {
    if (children.length >= ISSUE_DRAFT_MAX_CHILDREN) break;
    if (!entry || typeof entry !== "object" || Array.isArray(entry)) continue;
    const child = entry as Record<string, unknown>;
    const title = typeof child.title === "string" ? child.title.trim() : "";
    if (title.length === 0) continue;
    children.push({
      key: typeof child.key === "string" ? child.key.trim() : "",
      title,
      description: typeof child.description === "string" ? child.description : "",
      status: "",
      priority: "",
      assignee_type: null,
      assignee_id: null,
      stage:
        typeof child.stage === "number" && Number.isInteger(child.stage)
          ? child.stage
          : null,
      assignee_hint:
        typeof child.assignee_hint === "string"
          ? child.assignee_hint.trim()
          : null,
    });
  }
  // A block that named sub-issues but produced none we can create is a
  // malformed array, not a decision to delete the group — only an empty array
  // says that. Clearing here would turn the carrier's broken JSON into lost
  // user work.
  if (raw.length > 0 && children.length === 0) return undefined;
  return normalizeIssueDraftChildren(children);
}

/** The reply with every machine-readable block removed — what the conversation shows. */
export function stripIssueDraftDirectives(content: string): string {
  let out = content;
  for (const tag of DIRECTIVE_TAGS) {
    out = out
      .replace(completeBlockPattern(tag), "")
      // Only after the complete blocks are gone: "the model never closed this"
      // means "everything after it is machine-readable" once the closed blocks
      // after it have been taken out.
      .replace(openBlockPattern(tag), "");
  }
  out = trimPartialTag(out);
  // Either the block was fenced, or it was not: with no block removed there is
  // nothing a fence could have been emptied by, and ordinary code stays put.
  if (out !== content) {
    out = out.replace(EMPTY_FENCE_PAIR, "");
    // Only when the removal left the text inside a fence nobody closed. A
    // trailing fence with a matching opener above it closes prose the model
    // wrote, and dropping it would leave the transcript in an open code block.
    if (endsInsideFence(out)) out = out.replace(DANGLING_FENCE_OPEN, "");
  }
  return out.trim();
}

/** True when an odd number of fence delimiters leaves the text inside one. */
function endsInsideFence(content: string): boolean {
  const fences = content.match(new RegExp(`^[ \\t]*${FENCE}`, "gm"));
  return (fences?.length ?? 0) % 2 === 1;
}

/**
 * Drops a fragment the reply was cut off in the middle of, such as
 * `<issue_draft_quest`. It is not yet a tag, so no block pattern can see it,
 * and a streaming reply would otherwise print the fragment.
 */
function trimPartialTag(content: string): string {
  const start = content.lastIndexOf("<");
  if (start === -1) return content;
  const tail = content.slice(start);
  // A complete tag of any kind ends the search; and a lone "<" is prose
  // ("a < b"), not the start of a directive.
  if (tail.length < 2 || tail.includes(">")) return content;
  const isDirectiveStart = DIRECTIVE_TAGS.some((tag) =>
    `<${tag}>`.startsWith(tail),
  );
  return isDirectiveStart ? content.slice(0, start) : content;
}

/** One answer the guided policy proposes for its question. */
export interface IssueDraftQuestionOption {
  /** Short text on the answer chip. */
  label: string;
  /** What sending this answer actually says, phrased as the user would. */
  value: string;
  /** The option the carrier would pick itself. At most one per question. */
  recommended: boolean;
}

/** The single question an alignment turn is waiting on. */
export interface IssueDraftQuestion {
  question: string;
  options: IssueDraftQuestionOption[];
}

/**
 * The question block of a reply, parsed. Same rules as the draft block: the
 * last complete block wins, and anything malformed means "no question" rather
 * than a broken turn.
 *
 * Options are validated individually instead of all-or-nothing: a model that
 * emits one malformed option alongside three usable ones has still asked a
 * usable question, and the composer is always there for a free-text answer.
 */
export function parseIssueDraftQuestion(
  content: string,
): IssueDraftQuestion | null {
  const matches = [
    ...content.matchAll(completeBlockPattern("issue_draft_question")),
  ];
  const raw = matches[matches.length - 1]?.[0];
  if (!raw) return null;
  const inner = raw
    .replace(/^\s*<issue_draft_question>/, "")
    .replace(/<\/issue_draft_question>\s*$/, "");
  const parsed = parseJsonObject(inner);
  if (!parsed) return null;

  const question = parsed.question;
  if (typeof question !== "string" || question.trim().length === 0) return null;

  const options: IssueDraftQuestionOption[] = [];
  const rawOptions = parsed.options;
  if (Array.isArray(rawOptions)) {
    for (const entry of rawOptions) {
      if (options.length >= MAX_QUESTION_OPTIONS) break;
      if (!entry || typeof entry !== "object" || Array.isArray(entry)) continue;
      const option = entry as Record<string, unknown>;
      const label = option.label;
      const value = option.value;
      if (typeof label !== "string" || label.trim().length === 0) continue;
      if (typeof value !== "string" || value.trim().length === 0) continue;
      options.push({
        label: label.trim(),
        value: value.trim(),
        recommended: option.recommended === true,
      });
    }
  }
  return { question: question.trim(), options };
}

/**
 * The question the alignment is currently waiting on: the LAST message in the
 * transcript, when the carrier wrote it and it asks something.
 *
 * "Last message" is the whole rule, and it is why an answered question
 * disappears on its own — the user's next turn becomes the last message, so the
 * chips are gone before the carrier has replied, and a reply that asks nothing
 * clears them too. Only the last block of that message counts: a model that
 * thinks out loud may emit two.
 */
export function issueDraftPendingQuestion(
  messages: readonly ChatMessage[],
): { messageId: string; question: IssueDraftQuestion } | null {
  const last = messages[messages.length - 1];
  if (!last || last.role !== "assistant") return null;
  const question = parseIssueDraftQuestion(last.content);
  if (!question) return null;
  return { messageId: last.id, question };
}

/** One turn: what the user asked, plus the draft they are asking about. */
export function encodeIssueDraftInput(
  request: string,
  draft: IssueDraftPayload,
): string {
  // The group travels with the draft for the same reason the flat fields do:
  // the carrier is told to preserve what it is given, and its instructions say
  // a key it has already emitted must come back unchanged. Sending no children
  // is how the envelope says "there are none" — an empty array would read as a
  // statement that the group is empty, which is `[]`'s meaning on the way back.
  const children = (draft.children ?? []).map((child) => ({
    key: child.key,
    title: child.title,
    description: child.description,
    stage: child.stage ?? null,
    assignee_hint: child.assignee_hint ?? "",
  }));
  return (
    DRAFT_INPUT_PREFIX +
    JSON.stringify({
      user_request: request,
      current_draft: {
        title: draft.title,
        description: draft.description,
        status: draft.status,
        priority: draft.priority,
        ...(children.length > 0 ? { children } : {}),
      },
    })
  );
}

/**
 * Recovers the user's own words from a stored turn. Anything that is not an
 * envelope is returned unchanged, so a message written by an older build — or
 * by a person — still renders as itself.
 */
export function decodeIssueDraftInput(content: string): string {
  if (!content.startsWith(DRAFT_INPUT_PREFIX)) return content;
  const parsed = parseJsonObject(content.slice(DRAFT_INPUT_PREFIX.length));
  const request = parsed?.user_request;
  return typeof request === "string" ? request : content;
}

/**
 * Folds a parsed block into the draft the user is looking at. Patch fields
 * overwrite; everything else is preserved, including the fields the carrier is
 * not told about (assignee, project, parent) which the preview panel owns.
 *
 * `children` is replaced as a SET, not merged item by item: the sub-issue list
 * is one judgement the carrier makes each turn, and merging it item-wise would
 * make a removal impossible to express. What survives by key, however, keeps
 * the fields the carrier is not allowed to choose — the assignee the user
 * picked for that row — so a reply can reorder or rewrite the group without
 * silently dropping the work someone already assigned.
 */
export function mergeIssueDraftPayload(
  current: IssueDraftPayload,
  patch: IssueDraftPatch | null,
): IssueDraftPayload {
  if (!patch) return current;
  return {
    ...current,
    ...(patch.title !== undefined ? { title: patch.title } : {}),
    ...(patch.description !== undefined
      ? { description: patch.description }
      : {}),
    ...(patch.status !== undefined ? { status: patch.status } : {}),
    ...(patch.priority !== undefined ? { priority: patch.priority } : {}),
    ...(patch.children !== undefined
      ? { children: mergeIssueDraftChildren(current.children, patch.children) }
      : {}),
  };
}

/** The incoming sub-issues, carrying over the client-owned fields of the rows
 *  that are still there (by key). */
function mergeIssueDraftChildren(
  current: readonly IssueDraftChild[] | undefined,
  incoming: readonly IssueDraftChild[],
): IssueDraftChild[] {
  const before = new Map((current ?? []).map((child) => [child.key, child]));
  return incoming.map((child) => {
    const previous = before.get(child.key);
    if (!previous) return child;
    return {
      ...child,
      assignee_type: child.assignee_type ?? previous.assignee_type ?? null,
      assignee_id: child.assignee_id ?? previous.assignee_id ?? null,
      assignee_hint: child.assignee_hint ?? previous.assignee_hint ?? null,
    };
  });
}

/**
 * Whether the draft is worth creating issues from. Only the title is
 * required: the server reads the rest out of the same object, and an empty
 * description is a legitimate issue. Every sub-issue's title is required too —
 * the server validates each node through the same gate as the root and refuses
 * the whole group for one empty title, so this is the client's own check that
 * the confirm button is never offered for something the server will refuse.
 */
export function issueDraftIsCreatable(draft: IssueDraftPayload): boolean {
  if (draft.title.trim().length === 0) return false;
  const children = draft.children ?? [];
  // The group is refused as a whole by the server when it is oversized, so the
  // client must not offer a confirm that cannot land. Deleting a row is the fix,
  // and it is right there in the panel.
  if (children.length > ISSUE_DRAFT_MAX_CHILDREN) return false;
  return children.every((child) => child.title.trim().length > 0);
}

function parseJsonObject(value: string): Record<string, unknown> | null {
  const attempts = [value];
  // Some CLI-backed models emit literal newlines inside a JSON string even
  // when told not to. Repair only JSON control characters inside strings —
  // object structure and every other syntax error still has to fail.
  attempts.push(escapeJsonStringControlCharacters(value));
  for (const attempt of attempts) {
    try {
      const parsed: unknown = JSON.parse(attempt);
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
        return parsed as Record<string, unknown>;
      }
    } catch {
      // Try the repaired form, then give up: an unparseable block means no
      // preview update, not a broken conversation.
    }
  }
  return null;
}

/**
 * Escapes raw control characters that appear *inside* a JSON string literal.
 * Structure outside strings is left untouched, so a genuinely malformed object
 * still fails to parse.
 */
function escapeJsonStringControlCharacters(value: string): string {
  let out = "";
  let inString = false;
  let escaped = false;
  for (const char of value) {
    if (escaped) {
      out += char;
      escaped = false;
      continue;
    }
    if (char === "\\") {
      out += char;
      escaped = true;
      continue;
    }
    if (char === '"') {
      inString = !inString;
      out += char;
      continue;
    }
    if (inString && char === "\n") {
      out += "\\n";
      continue;
    }
    if (inString && char === "\r") {
      out += "\\r";
      continue;
    }
    if (inString && char === "\t") {
      out += "\\t";
      continue;
    }
    out += char;
  }
  return out;
}
