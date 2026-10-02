import type { Progress } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";

export type ProgressDot = "working" | "waiting" | "stuck" | "done";

const DOT_CLASS: Record<ProgressDot, string> = {
  working: "bg-blue-500",
  waiting: "bg-amber-500",
  stuck: "bg-destructive",
  done: "bg-emerald-500",
};

/** The dot a line carries: its tone, or — for lines written before tones
 *  existed — a guess from who wrote it. */
export function progressDot(progress: Progress): ProgressDot {
  if (progress.tone) return progress.tone;
  if (progress.source === "close") return "done";
  if (progress.source === "parking") return "waiting";
  return "working";
}

export function ProgressDotMark({ dot, className }: { dot: ProgressDot; className?: string }) {
  return <span aria-hidden="true" className={cn("size-1.5 shrink-0 rounded-full", DOT_CLASS[dot], className)} />;
}

/**
 * The second line under an issue or chat title (DENE-1037). `status` is a
 * live state (正在处理 / 等待中 / 失败) shown before the text; `dot` overrides
 * the colour when that live state is the more current truth.
 */
export function ProgressLine({
  progress,
  status,
  dot,
  className,
}: {
  progress?: Progress | null;
  status?: string;
  dot?: ProgressDot;
  className?: string;
}) {
  const text = progress?.text?.trim() ?? "";
  if (!text && !status) return null;
  const colour = dot ?? (progress ? progressDot(progress) : "working");
  const full = status && text ? `${status} · ${text}` : status || text;
  return (
    <span className={cn("flex min-w-0 items-center gap-1.5 text-caption text-muted-foreground", className)} title={full}>
      <ProgressDotMark dot={colour} />
      <span className="truncate">
        {status && <span className={cn("font-medium", colour === "stuck" ? "text-destructive" : "text-foreground")}>{status}</span>}
        {status && text && " · "}
        {text}
      </span>
    </span>
  );
}
