// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { isRetryableUploadError, retryUpload } from "./upload-retry";

class HttpError extends Error {
  constructor(public status: number) {
    super(`HTTP ${status}`);
  }
}

describe("isRetryableUploadError", () => {
  it("treats network failures, timeouts and 5xx as transient", () => {
    expect(isRetryableUploadError(new TypeError("Failed to fetch"))).toBe(true);
    expect(isRetryableUploadError(new HttpError(524))).toBe(true);
    expect(isRetryableUploadError(new HttpError(429))).toBe(true);
    expect(isRetryableUploadError(new HttpError(413))).toBe(false);
    expect(isRetryableUploadError(Object.assign(new Error("x"), { name: "AbortError" }))).toBe(false);
  });
});

describe("retryUpload", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("keeps retrying a visible, online page well past the old 2.5s window", async () => {
    // Offline for ~40s with the browser still reporting online: every attempt
    // fails with a 524 / network error until the connection comes back.
    let calls = 0;
    const attempt = vi.fn(async () => {
      calls += 1;
      if (calls <= 8) throw calls % 2 ? new TypeError("Failed to fetch") : new HttpError(524);
      return "ok";
    });

    const result = retryUpload(attempt);
    await vi.advanceTimersByTimeAsync(60_000);

    await expect(result).resolves.toBe("ok");
    expect(calls).toBe(9);
  });

  it("gives up only after the stall window passes with no new bytes", async () => {
    const attempt = vi.fn(async () => {
      throw new TypeError("Failed to fetch");
    });
    const result = retryUpload(attempt, { stallTimeoutMs: 30_000 });
    const settled = expect(result).rejects.toThrow("Failed to fetch");

    await vi.advanceTimersByTimeAsync(29_000);
    const before = attempt.mock.calls.length;
    await vi.advanceTimersByTimeAsync(30_000);
    await settled;
    expect(before).toBeGreaterThan(3);
  });

  it("resets the stall clock whenever new bytes land", async () => {
    let uploaded = 0;
    const attempt = vi.fn(async (onProgress: (u: number, t: number) => void) => {
      uploaded += 1;
      onProgress(uploaded, 100);
      if (uploaded < 10) throw new TypeError("Failed to fetch");
      return "done";
    });
    const result = retryUpload(attempt, { stallTimeoutMs: 5_000, delaysMs: [4_000] });
    await vi.advanceTimersByTimeAsync(60_000);
    await expect(result).resolves.toBe("done");
  });

  it("fails immediately on a permanent error", async () => {
    const attempt = vi.fn(async () => {
      throw new HttpError(413);
    });
    await expect(retryUpload(attempt)).rejects.toThrow("HTTP 413");
    expect(attempt).toHaveBeenCalledTimes(1);
  });

  it("stops waiting when aborted", async () => {
    const controller = new AbortController();
    const attempt = vi.fn(async () => {
      throw new TypeError("Failed to fetch");
    });
    const result = retryUpload(attempt, { signal: controller.signal });
    const settled = expect(result).rejects.toMatchObject({ name: "AbortError" });
    await vi.advanceTimersByTimeAsync(100);
    controller.abort();
    await settled;
  });

  it("retries at once when the page comes back online", async () => {
    let calls = 0;
    const attempt = vi.fn(async () => {
      calls += 1;
      if (calls === 1) throw new TypeError("Failed to fetch");
      return "ok";
    });
    const result = retryUpload(attempt, { delaysMs: [60_000] });
    await vi.advanceTimersByTimeAsync(0);
    window.dispatchEvent(new Event("online"));
    await vi.advanceTimersByTimeAsync(0);
    await expect(result).resolves.toBe("ok");
  });

  describe("time away from the page", () => {
    let hidden = false;
    const setHidden = (value: boolean) => {
      hidden = value;
      document.dispatchEvent(new Event("visibilitychange"));
    };
    beforeEach(() => {
      hidden = false;
      Object.defineProperty(document, "hidden", { configurable: true, get: () => hidden });
    });
    afterEach(() => {
      delete (document as { hidden?: boolean }).hidden;
    });

    it("does not count a 15-minute background stint toward the stall window", async () => {
      let offline = true;
      const attempt = vi.fn(async () => {
        if (offline) throw new TypeError("Failed to fetch");
        return "ok";
      });
      const result = retryUpload(attempt);
      await vi.advanceTimersByTimeAsync(2_000);

      setHidden(true);
      await vi.advanceTimersByTimeAsync(15 * 60_000);
      // Back in the foreground: the immediate retry still fails (network not
      // back yet) and must keep the upload alive.
      setHidden(false);
      await vi.advanceTimersByTimeAsync(60_000);
      offline = false;
      await vi.advanceTimersByTimeAsync(20_000);
      await expect(result).resolves.toBe("ok");
    });

    it("ignores a suspension whose failure surfaces before visibilitychange", async () => {
      // A locked phone freezes timers; the pending request only fails once the
      // page wakes up, possibly before the visibility event is handled.
      let rejectPending: ((error: Error) => void) | undefined;
      let calls = 0;
      const attempt = vi.fn(() => {
        calls += 1;
        if (calls === 1) return new Promise<string>((_, reject) => { rejectPending = reject; });
        return Promise.resolve("ok");
      });
      const result = retryUpload(attempt, { stallTimeoutMs: 10 * 60_000 });
      await vi.advanceTimersByTimeAsync(1_000);
      vi.setSystemTime(Date.now() + 20 * 60_000);
      rejectPending!(new TypeError("Failed to fetch"));
      await vi.advanceTimersByTimeAsync(1_000);
      await expect(result).resolves.toBe("ok");
    });

    it("still gives up after 10 foreground minutes with no new bytes", async () => {
      const attempt = vi.fn(async () => {
        throw new TypeError("Failed to fetch");
      });
      const result = retryUpload(attempt);
      const settled = expect(result).rejects.toThrow("Failed to fetch");
      setHidden(true);
      await vi.advanceTimersByTimeAsync(30 * 60_000);
      setHidden(false);
      await vi.advanceTimersByTimeAsync(9 * 60_000);
      const before = attempt.mock.calls.length;
      await vi.advanceTimersByTimeAsync(2 * 60_000);
      await settled;
      expect(attempt.mock.calls.length).toBeGreaterThan(before);
    });
  });
});
