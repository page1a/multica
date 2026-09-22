"use client";

import { ALLOW, type Decision } from "./types";
import { canWrite } from "./rules";
import { useCurrentMember } from "./use-current-member";

/**
 * Workspace write gate for the signed-in member.
 *
 * While membership is still loading the result is `allowed` with
 * `isGuest: false` — the loading frame must match a normal member's, so we
 * do not disable controls or show the guest badge until the role is known.
 */
export function useCanWrite(wsId: string): {
  decision: Decision;
  isGuest: boolean;
  isLoading: boolean;
} {
  const { userId, role, isLoading } = useCurrentMember(wsId);
  if (isLoading) {
    return { decision: ALLOW, isGuest: false, isLoading: true };
  }
  return {
    decision: canWrite({ userId, role }),
    isGuest: role === "guest",
    isLoading: false,
  };
}
