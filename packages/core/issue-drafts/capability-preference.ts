import type { IssueDraftCapabilityKey } from "./capabilities";
import { DEFAULT_ISSUE_DRAFT_CAPABILITIES } from "./capabilities";
import { defaultStorage } from "../platform/storage";

const STORAGE_KEY = "multica_alignment_capabilities";

function storageKey(userId?: string | null): string {
  return userId ? `${STORAGE_KEY}:${userId}` : STORAGE_KEY;
}

/** Read the last selection for one signed-in user, independent of workspace. */
export function readIssueDraftCapabilityPreference(userId?: string | null): IssueDraftCapabilityKey[] {
  try {
    const raw = defaultStorage.getItem(storageKey(userId));
    if (!raw) return [...DEFAULT_ISSUE_DRAFT_CAPABILITIES];
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [...DEFAULT_ISSUE_DRAFT_CAPABILITIES];
    const allowed = new Set<IssueDraftCapabilityKey>(DEFAULT_ISSUE_DRAFT_CAPABILITIES);
    return parsed.filter((key): key is IssueDraftCapabilityKey =>
      typeof key === "string" && allowed.has(key as IssueDraftCapabilityKey),
    );
  } catch {
    return [...DEFAULT_ISSUE_DRAFT_CAPABILITIES];
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
