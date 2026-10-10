/**
 * "Is there a Multica page behind this one?" for the web navigation adapter.
 *
 * Callers that want to step back (deleting an issue returns the user to the
 * list they opened it from) must not step back off the app when the current
 * page was opened cold — a shared link, a pasted URL, a new tab.
 *
 * The Navigation API answers this directly: its `entries()` spans just the
 * contiguous same-origin run around the current entry, so arriving from an external site reports `canGoBack` false
 * even though the browser technically has somewhere to go. `history.length`
 * is no substitute — it counts other origins' entries too.
 *
 * Browsers without that API (Safari before 26.2) get the answer from the
 * history entries themselves: every committed `pushState` / `replaceState`
 * goes through `installInAppHistoryDepth`, which stamps the entry's state
 * with how many app entries sit behind it. A push is one deeper
 * than the entry it leaves, a replace keeps the depth, and an entry the app
 * never wrote (a cold load, a pasted URL, arriving from another site) reads
 * as depth 0. The stamp lives on the entry, so Back, Forward and reload all
 * read the depth of the entry actually showing.
 *
 * Counting the adapter's own `router.push` calls instead is not a fallback: a
 * push is a request, not a committed history entry. Next drops it to
 * `replaceState` when the canonical URL is unchanged (`app-router.js`, the
 * `pendingPush && href !== canonicalUrl` branch), and pushes can also be
 * superseded or abandoned mid-transition. The stamp is written by the
 * history call that commits, so it cannot claim an entry that does not exist.
 * Anything it cannot read is depth 0, and callers take their fallback path.
 */

/** Minimal shape of the Navigation API — not in TypeScript's DOM lib yet. */
type NavigationApi = { canGoBack: boolean };

const DEPTH_KEY = "__multicaDepth";
const INSTALLED = Symbol.for("multica.inAppHistoryDepth");

function depthOf(state: unknown): number {
  if (state === null || typeof state !== "object") return 0;
  const depth = (state as Record<string, unknown>)[DEPTH_KEY];
  return typeof depth === "number" && Number.isInteger(depth) && depth > 0
    ? depth
    : 0;
}

/** Copy of `data` carrying `depth`; non-object state is left unstamped. */
function stamp(data: unknown, depth: number): unknown {
  if (data === null || data === undefined) return { [DEPTH_KEY]: depth };
  if (typeof data !== "object" || Array.isArray(data)) return data;
  return { ...(data as Record<string, unknown>), [DEPTH_KEY]: depth };
}

/**
 * Wrap `history.pushState` / `replaceState` so every committed entry records
 * its in-app depth. Idempotent; safe to call from an effect on every mount.
 */
export function installInAppHistoryDepth(): void {
  if (typeof window === "undefined") return;
  const history = window.history as History & { [INSTALLED]?: true };
  if (history[INSTALLED]) return;
  history[INSTALLED] = true;
  const push = history.pushState;
  const replace = history.replaceState;
  history.pushState = function pushState(data, unused, url) {
    return push.call(this, stamp(data, depthOf(history.state) + 1), unused, url);
  };
  history.replaceState = function replaceState(data, unused, url) {
    return replace.call(this, stamp(data, depthOf(history.state)), unused, url);
  };
}

/**
 * Whether a step back stays inside the app: the Navigation API's answer when
 * the browser has one, otherwise the depth stamped on the current entry.
 */
export function canGoBackInApp(): boolean {
  if (typeof window === "undefined") return false;
  const navigation = (window as { navigation?: unknown }).navigation as
    | NavigationApi
    | undefined;
  if (navigation && typeof navigation === "object" && "canGoBack" in navigation) {
    return navigation.canGoBack === true;
  }
  return depthOf(window.history.state) > 0;
}
