import { paths } from "./paths";

/**
 * A phone: a narrow viewport driven by a finger. Both halves matter — a narrow
 * desktop window keeps its mouse, and a tablet is wide enough for the desktop
 * shell — so neither alone decides. No user-agent sniffing.
 */
export const PHONE_MEDIA_QUERY = "(max-width: 767px) and (pointer: coarse)";

/** Whether this browser is a phone right now. False wherever there is no `matchMedia`. */
export function isPhoneViewport(): boolean {
  const matchMedia = (globalThis as { matchMedia?: (query: string) => { matches: boolean } })
    .matchMedia;
  if (typeof matchMedia !== "function") return false;
  return matchMedia(PHONE_MEDIA_QUERY).matches;
}

/**
 * Where opening a workspace lands: Chat on a phone, where chatting and
 * dispatching is most of the work, and the issue list everywhere else.
 */
export function workspaceLandingPath(slug: string, phone = isPhoneViewport()): string {
  const ws = paths.workspace(slug);
  return phone ? ws.chat() : ws.issues();
}
