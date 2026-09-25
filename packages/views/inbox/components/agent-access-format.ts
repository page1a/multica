/** Date helpers shared by the doorbell card and the pass settings (DENE-808). */

export function formatAccessExpiry(value: string | null | undefined, locale?: string): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(date);
}

/**
 * `<input type="datetime-local">` wants a zone-less `YYYY-MM-DDTHH:mm` in the
 * viewer's local time; `toISOString()` would shift it to UTC.
 */
export function localDateTimeInputValue(offsetMs: number, now: Date = new Date()): string {
  const d = new Date(now.getTime() + offsetMs);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
