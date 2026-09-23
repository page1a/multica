import type { IssueDraftCapabilityKey } from "./capabilities";
import { DEFAULT_ISSUE_DRAFT_CAPABILITIES } from "./capabilities";
import { defaultStorage } from "../platform/storage";

const STORAGE_KEY = "multica_alignment_capabilities";

function storageKey(userId?: string | null): string {
  return userId ? `${STORAGE_KEY}:${userId}` : STORAGE_KEY;
}

function systemDefault(): IssueDraftCapabilityKey[] {
  return [...DEFAULT_ISSUE_DRAFT_CAPABILITIES];
}

/**
 * The combination the alignment entry should open on.
 *
 * One record per signed-in user, with no workspace in the key: the same person
 * sees the same last-used methods in every workspace. `failed` is a storage
 * read that could not be trusted — the caller shows the system default and one
 * notice, and still lets the conversation start.
 */
export interface IssueDraftCapabilityPreference {
  capabilities: IssueDraftCapabilityKey[];
  failed: boolean;
}

/** Read the last selection for one signed-in user, independent of workspace. */
export function readIssueDraftCapabilityPreference(
  userId?: string | null,
): IssueDraftCapabilityPreference {
  try {
    const raw = defaultStorage.getItem(storageKey(userId));
    // No record yet is the first-use case, not a failure.
    if (!raw) return { capabilities: systemDefault(), failed: false };
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return { capabilities: systemDefault(), failed: true };
    const allowed = new Set<IssueDraftCapabilityKey>(DEFAULT_ISSUE_DRAFT_CAPABILITIES);
    const known = parsed.filter(
      (key): key is IssueDraftCapabilityKey =>
        typeof key === "string" && allowed.has(key as IssueDraftCapabilityKey),
    );
    // An empty array is a real choice ("none of them"). A record that names
    // only keys this client no longer offers is unusable history: show the
    // system default rather than a blank picker.
    if (known.length === 0 && parsed.length > 0) {
      return { capabilities: systemDefault(), failed: false };
    }
    return { capabilities: known, failed: false };
  } catch {
    return { capabilities: systemDefault(), failed: true };
  }
}

export function writeIssueDraftCapabilityPreference(
  capabilities: readonly IssueDraftCapabilityKey[],
  userId?: string | null,
): void {
  try {
    defaultStorage.setItem(storageKey(userId), JSON.stringify([...capabilities]));
  } catch {
    // Preferences are an enhancement; a storage failure must not block opening.
  }
}
