import type { IssueDraftPayload } from "../types";
import type { IssueAssigneeType } from "../types";

/**
 * The seat routing would put on one issue of a draft. Produced by the server's
 * routing module from the tier tags on the roster and the project's direction
 * — the same answer the post-create routing pass gives, shown before create.
 */
export interface DraftAssigneeSuggestion {
  assignee_type: IssueAssigneeType;
  assignee_id: string;
  name: string;
  tier: string;
}

export interface DraftAssigneeSuggestionRow {
  title: string;
  description: string;
  has_children: boolean;
}

export interface DraftAssigneeSuggestionRequest {
  project_id: string | null;
  rows: DraftAssigneeSuggestionRow[];
}

/** The parent row's key in the `offered` set; children use their own `key`. */
export const ISSUE_DRAFT_ROOT_ROW = "__root__";

/**
 * The rows to ask about, root first then children in order — the index
 * alignment the response relies on. Null when nothing is unassigned, so a
 * draft whose every row already has an assignee never costs a routing call.
 */
export function issueDraftSuggestionRequest(
  draft: IssueDraftPayload | null,
): DraftAssigneeSuggestionRequest | null {
  if (!draft || !draft.title.trim()) return null;
  const children = draft.children ?? [];
  const anyUnassigned =
    !draft.assignee_id || children.some((child) => !child.assignee_id);
  if (!anyUnassigned) return null;
  return {
    project_id: draft.project_id ?? null,
    rows: [
      {
        title: draft.title,
        description: draft.description ?? "",
        has_children: children.length > 0,
      },
      ...children.map((child) => ({
        title: child.title,
        description: child.description ?? "",
        has_children: false,
      })),
    ],
  };
}

/**
 * How a suggestion query reads to a surface that has to explain itself.
 *
 * `idle`    — there is nothing to ask about (every row already has a seat, or
 *             the draft is not far enough along to have one);
 * `loading` — asked, nothing back yet. The confirm stays available: the seats
 *             that are already saved are the ones that will be created.
 * `failed`  — the ask failed, or came back in a shape that cannot be trusted.
 *             Same handling as "no suggestion for this row": the rows stay
 *             unassigned and the user is told, rather than the confirm being
 *             blocked on a suggestion nobody needs.
 * `ready`   — one answer per row, safe to apply.
 *
 * An answer of the wrong length is NOT ready. The response is index-aligned
 * with the request, so a short or padded array would silently fill the wrong
 * rows — the one failure mode that turns a suggestion into an assignment
 * nobody chose.
 */
export type IssueDraftSuggestionPhase = "idle" | "loading" | "failed" | "ready";

export function issueDraftSuggestionPhase(input: {
  request: DraftAssigneeSuggestionRequest | null;
  data: readonly (DraftAssigneeSuggestion | null)[] | undefined;
  isError: boolean;
  isPending: boolean;
}): IssueDraftSuggestionPhase {
  if (!input.request) return "idle";
  if (input.isError) return "failed";
  if (input.data === undefined) {
    // `isPending` distinguishes "still asking" from "never asked", but both
    // read the same on screen, so the caller does not have to gate on it.
    return "loading";
  }
  return input.data.length === input.request.rows.length ? "ready" : "failed";
}

/**
 * Fills unassigned rows from the suggestions, once per row.
 *
 * `offered` records the rows that have already been offered a seat, and is
 * mutated here. A row is only ever offered once: a person who clears a
 * suggestion back to "unassigned" has answered, and re-filling it on the next
 * render would make the field impossible to empty. Returns the same object
 * when nothing changed.
 *
 * An array that does not line up with the payload's rows (root first, then the
 * sub-issues in order) is refused whole rather than applied up to the point it
 * ran out: the rows are read by index, so a partial answer would put a seat on
 * a row the answer never described.
 */
export function applyIssueDraftAssigneeSuggestions(
  draft: IssueDraftPayload,
  suggestions: readonly (DraftAssigneeSuggestion | null)[],
  offered: Set<string>,
): IssueDraftPayload {
  const expected = 1 + (draft.children ?? []).length;
  if (suggestions.length !== expected) return draft;
  let changed = false;
  let next = draft;
  const parent = suggestions[0];
  if (parent && !offered.has(ISSUE_DRAFT_ROOT_ROW)) {
    offered.add(ISSUE_DRAFT_ROOT_ROW);
    if (!draft.assignee_id) {
      next = { ...next, assignee_type: parent.assignee_type, assignee_id: parent.assignee_id };
      changed = true;
    }
  }
  const children = (draft.children ?? []).map((child, index) => {
    const suggestion = suggestions[index + 1];
    if (!suggestion || offered.has(child.key)) return child;
    offered.add(child.key);
    if (child.assignee_id) return child;
    changed = true;
    return { ...child, assignee_type: suggestion.assignee_type, assignee_id: suggestion.assignee_id };
  });
  return changed ? { ...next, children } : draft;
}
