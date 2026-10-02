"use client";

import type { Issue } from "@multica/core/types";
import { useLocale, useTimeAgo } from "../../i18n";

type ProbeStatus = "ready" | "pending" | "failed" | "";

function textForStatus(status: ProbeStatus, chinese: boolean): string {
  if (chinese) {
    if (status === "ready") return "已就绪";
    if (status === "failed") return "出问题";
    if (status === "pending") return "仍在等待";
    return "未复查";
  }
  if (status === "ready") return "ready";
  if (status === "failed") return "failed";
  if (status === "pending") return "still waiting";
  return "not checked";
}

export function BlockWaitProbeBadge({ issue }: { issue: Issue }) {
  const locale = useLocale();
  const timeAgo = useTimeAgo();
  const metadata = issue.metadata;
  const probe = typeof metadata?.["block.wait_probe"] === "string"
    ? metadata["block.wait_probe"]
    : "";
  if (issue.status !== "blocked" || !probe) return null;

  const condition = typeof metadata?.["block.wait_condition"] === "string"
    ? metadata["block.wait_condition"]
    : probe;
  const status = typeof metadata?.["block.wait_probe_status"] === "string"
    ? metadata["block.wait_probe_status"] as ProbeStatus
    : "";
  const checkedAt = typeof metadata?.["block.wait_probe_at"] === "string"
    ? metadata["block.wait_probe_at"]
    : "";
  const chinese = locale.toLowerCase().startsWith("zh");
  const result = textForStatus(status, chinese);
  const checked = checkedAt ? timeAgo(checkedAt) : (chinese ? "尚未复查" : "not checked yet");
  const label = chinese
    ? `等待：${condition} · ${result} · ${checked}`
    : `Waiting: ${condition} · ${result} · ${checked}`;

  return (
    <span
      className="inline-flex max-w-[min(32rem,100%)] items-center truncate rounded-full bg-warning/10 px-1.5 py-0.5 text-micro text-warning-foreground"
      title={label}
      data-slot="block-wait-probe-badge"
    >
      <span className="truncate">{label}</span>
    </span>
  );
}
