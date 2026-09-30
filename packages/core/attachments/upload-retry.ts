/**
 * One retry policy for every web/desktop upload path (the draft coordinator
 * behind chat and comments, and the plain `useFileUpload` hook behind the
 * issue description).
 *
 * A dropped connection must not cost the user their placeholder: the chunked
 * upload remembers its server session, so each retry only sends the chunks
 * the server is still missing. We therefore keep retrying transient failures
 * (network error, timeout, 5xx such as Cloudflare's 524, 408/425/429) for as
 * long as the upload keeps making progress, and only give up after
 * `stallTimeoutMs` of failures with no new bytes arriving. Coming back online
 * or returning to the foreground skips the remaining backoff.
 *
 * The stall window only counts time the person actually spends on the page:
 * a locked phone or a backgrounded tab must not burn it, otherwise coming back
 * after a long break would drop the placeholder on the first failed retry.
 */

export type UploadProgress = (uploadedBytes: number, totalBytes: number) => void;

export const UPLOAD_RETRY_DELAYS_MS = [250, 750, 1500, 3000, 5000, 10000, 15000] as const;
export const UPLOAD_STALL_TIMEOUT_MS = 10 * 60 * 1000;

export function isRetryableUploadError(error: unknown): boolean {
  if (!(error instanceof Error) || error.name === "AbortError") return false;
  const candidate = error as Error & { retryable?: boolean; status?: number };
  if (candidate.retryable === false) return false;
  if (candidate.retryable === true) return true;
  if (typeof candidate.status === "number") {
    return candidate.status === 408 || candidate.status === 425 || candidate.status === 429 || candidate.status >= 500;
  }
  return /network|fetch|timeout|offline|connection|temporarily unavailable/i.test(error.message) || error.name === "TypeError";
}

/**
 * Wait `delayMs`, or less if the page comes back online / to the foreground.
 * While the page is hidden or offline the timer does not run: the retry waits
 * for the `visibilitychange` / `online` event instead. Safari can keep
 * `navigator.onLine === true` after the radio has dropped, so a visible,
 * "online" page still retries on the timer. Resolves false when aborted.
 */
export function waitForUploadRecovery(delayMs: number, signal?: AbortSignal): Promise<boolean> {
  if (signal?.aborted) return Promise.resolve(false);
  return new Promise((resolve) => {
    const hasDom = typeof document !== "undefined" && typeof window !== "undefined";
    let timer: ReturnType<typeof setTimeout> | undefined;
    let settled = false;
    const finish = (ok: boolean) => {
      if (settled) return;
      settled = true;
      if (timer !== undefined) clearTimeout(timer);
      if (hasDom) {
        document.removeEventListener("visibilitychange", resume);
        window.removeEventListener("online", resume);
      }
      signal?.removeEventListener("abort", aborted);
      resolve(ok);
    };
    const reachable = () => !hasDom || (!document.hidden && navigator.onLine !== false);
    const resume = () => {
      if (reachable()) finish(true);
    };
    const aborted = () => finish(false);
    signal?.addEventListener("abort", aborted, { once: true });
    if (hasDom) {
      document.addEventListener("visibilitychange", resume);
      window.addEventListener("online", resume);
    }
    if (reachable()) timer = setTimeout(() => finish(true), delayMs);
  });
}

// A gap between two foreground-clock samples longer than this means the page
// was suspended (locked phone, sleeping laptop) rather than sitting visible.
const FOREGROUND_TICK_MS = 1000;
const FOREGROUND_MAX_GAP_MS = 5000;

/**
 * Milliseconds the page has spent visible since the clock started. Hidden
 * time, and any gap where timers were frozen, is left out — including the
 * case where a failure surfaces right after returning, before the
 * `visibilitychange` event has been handled.
 */
function startForegroundClock(now: () => number) {
  const hasDom = typeof document !== "undefined";
  let elapsed = 0;
  let last = now();
  const sample = () => {
    const t = now();
    const gap = t - last;
    last = t;
    if (gap > 0 && gap <= FOREGROUND_MAX_GAP_MS && !(hasDom && document.hidden)) elapsed += gap;
    return elapsed;
  };
  const timer = setInterval(sample, FOREGROUND_TICK_MS);
  if (hasDom) document.addEventListener("visibilitychange", sample);
  return {
    read: sample,
    stop: () => {
      clearInterval(timer);
      if (hasDom) document.removeEventListener("visibilitychange", sample);
    },
  };
}

export interface RetryUploadOptions {
  signal?: AbortSignal;
  onProgress?: UploadProgress;
  stallTimeoutMs?: number;
  delaysMs?: readonly number[];
  now?: () => number;
}

/**
 * Run `attempt` until it succeeds, fails permanently, or stalls. Throws the
 * last error when giving up; throws an AbortError when `signal` aborts.
 */
export async function retryUpload<T>(
  attempt: (onProgress: UploadProgress) => Promise<T>,
  {
    signal,
    onProgress,
    stallTimeoutMs = UPLOAD_STALL_TIMEOUT_MS,
    delaysMs = UPLOAD_RETRY_DELAYS_MS,
    now = Date.now,
  }: RetryUploadOptions = {},
): Promise<T> {
  const clock = startForegroundClock(now);
  let bestUploaded = -1;
  let lastProgressAt = clock.read();
  let retries = 0;
  const track: UploadProgress = (uploaded, total) => {
    if (uploaded > bestUploaded) {
      bestUploaded = uploaded;
      lastProgressAt = clock.read();
      // New bytes landed: a later failure starts its backoff from the top.
      retries = 0;
    }
    onProgress?.(uploaded, total);
  };
  try {
    for (;;) {
      try {
        return await attempt(track);
      } catch (error) {
        if (signal?.aborted) throw error;
        if (!isRetryableUploadError(error) || clock.read() - lastProgressAt >= stallTimeoutMs) throw error;
        const delay = delaysMs[Math.min(retries, delaysMs.length - 1)]!;
        retries += 1;
        if (!(await waitForUploadRecovery(delay, signal))) {
          throw Object.assign(new Error("Upload aborted"), { name: "AbortError" });
        }
      }
    }
  } finally {
    clock.stop();
  }
}
