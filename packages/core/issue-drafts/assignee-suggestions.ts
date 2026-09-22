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
 * Fills unassigned rows from the suggestions, once per row.
 *
 * `offered` records the rows that have already been offered a seat, and is
 * mutated here. A row is only ever offered once: a person who clears a
 * suggestion back to "unassigned" has answered, and re-filling it on the next
 * render would make the field impossible to empty. Returns the same object
 * when nothing changed.
 */
export function applyIssueDraftAssigneeSuggestions(
  draft: IssueDraftPayload,
  suggestions: readonly (DraftAssigneeSuggestion | null)[],
  offered: Set<string>,
): IssueDraftPayload {
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
