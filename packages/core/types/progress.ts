/** Dot colour of a progress line (DENE-1037): blue / yellow / red / green. */
export type ProgressTone = "working" | "waiting" | "stuck" | "done";

/**
 * The second line under an issue or chat title. `source` says who wrote it:
 * `agent` (the agent's own report), `close` (a close summary), `parking` (the
 * stall patrol), `model` (chat recap summary) or `reply` (the reply's first
 * line when no model is configured).
 */
export interface Progress {
  text: string;
  source: string;
  /** Empty on lines written before tones existed. */
  tone?: ProgressTone | "";
  author_type: string;
  author_id?: string;
  updated_at: string;
}
