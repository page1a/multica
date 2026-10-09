/**
 * A File the browser holds a handle to but cannot read.
 *
 * Telegram on macOS copies a photo as a reference into its own cache rather
 * than as image bytes; the browser gets a name and a size but every read
 * fails. Sending such a body makes `fetch` reject with the same `TypeError`
 * a dropped connection produces, so without this check the retry policy
 * would keep a 0% placeholder spinning for its whole stall window.
 */
export class UnreadableFileError extends Error {
  readonly retryable = false;
  constructor(filename: string) {
    super(`Cannot read ${filename}`);
    this.name = "UnreadableFileError";
  }
}

/** True when the first byte of `file` can be read (an empty file counts). */
export async function isFileReadable(file: Blob): Promise<boolean> {
  if (file.size === 0) return true;
  try {
    await file.slice(0, 1).arrayBuffer();
    return true;
  } catch {
    return false;
  }
}
