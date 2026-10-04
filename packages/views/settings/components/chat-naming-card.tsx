"use client";

import { useState } from "react";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import type { WorkspaceNaming, WorkspaceNamingSource } from "@multica/core/types";
import { useT, useTimeAgo } from "../../i18n";
import { SettingsCard } from "./settings-layout";

const SOURCES: WorkspaceNamingSource[] = ["server_llm", "runtime", "rules"];

/**
 * Chat naming, status first: the card opens on whether naming is working for
 * the selected source, and keeps the source picker behind "Change".
 */
export function ChatNamingCard({
  naming,
  canManage,
  onChange,
}: {
  naming: WorkspaceNaming | undefined;
  canManage: boolean;
  onChange: (source: WorkspaceNamingSource) => void;
}) {
  const { t } = useT("settings");
  const timeAgo = useTimeAgo();
  const [picking, setPicking] = useState(false);

  if (!naming) return null;

  const source = naming.source;
  const health = naming.health ?? "idle";
  const serverOption = naming.options.find((option) => option.id === "server_llm");
  const serverReady = serverOption?.available ?? false;

  const sourceName = (id: WorkspaceNamingSource) =>
    id === "server_llm"
      ? t(($) => $.workspace.naming_source_server_llm)
      : id === "runtime"
        ? t(($) => $.workspace.naming_source_runtime)
        : t(($) => $.workspace.naming_source_rules);

  let headline: string;
  let detail: string | null = null;
  if (health === "ok") {
    headline =
      source === "server_llm"
        ? t(($) => $.workspace.naming_ok_server_llm)
        : source === "runtime"
          ? t(($) => $.workspace.naming_ok_runtime)
          : t(($) => $.workspace.naming_ok_rules);
    detail = naming.last
      ? t(($) => $.workspace.naming_ok_detail_last, {
          count: naming.stats.titled,
          title: naming.last.title,
          ago: timeAgo(naming.last.created_at),
        })
      : t(($) => $.workspace.naming_ok_detail, { count: naming.stats.titled });
  } else if (health === "degraded") {
    if (source === "server_llm" && !serverReady) {
      headline = t(($) => $.workspace.naming_degraded_no_key);
      detail = t(($) => $.workspace.naming_degraded_no_key_detail);
    } else if (source === "rules") {
      headline = t(($) => $.workspace.naming_degraded_rules);
      detail = t(($) => $.workspace.naming_degraded_rules_detail, { count: naming.stats.failed });
    } else {
      headline =
        source === "runtime"
          ? t(($) => $.workspace.naming_degraded_runtime)
          : t(($) => $.workspace.naming_degraded_server_llm);
      detail =
        source === "runtime"
          ? t(($) => $.workspace.naming_degraded_runtime_detail, { count: naming.stats.rules })
          : t(($) => $.workspace.naming_degraded_server_llm_detail, { count: naming.stats.rules });
    }
  } else {
    headline =
      source === "server_llm"
        ? t(($) => $.workspace.naming_idle_server_llm)
        : source === "runtime"
          ? t(($) => $.workspace.naming_idle_runtime)
          : t(($) => $.workspace.naming_ok_rules);
    detail = t(($) => $.workspace.naming_idle_detail);
  }

  return (
    <SettingsCard>
      <div className="flex items-start gap-3 px-4 py-3.5" role="status">
        <span
          aria-hidden
          className={cn(
            "mt-[7px] size-2 shrink-0 rounded-full ring-4",
            health === "ok" && "bg-success ring-success/15",
            health === "degraded" && "bg-warning ring-warning/20",
            health === "idle" && "bg-muted-foreground/40 ring-muted",
          )}
        />
        <div className="min-w-0 flex-1">
          <div className="text-body font-medium">{headline}</div>
          {detail ? (
            <div className="mt-0.5 text-caption leading-5 text-muted-foreground">{detail}</div>
          ) : null}
        </div>
        {canManage ? (
          <Button
            variant="outline"
            size="sm"
            aria-expanded={picking}
            onClick={() => setPicking((open) => !open)}
          >
            {picking ? t(($) => $.workspace.naming_done) : t(($) => $.workspace.naming_change)}
          </Button>
        ) : (
          <span className="shrink-0 pt-0.5 text-caption text-muted-foreground">
            {t(($) => $.workspace.naming_manage_hint)}
          </span>
        )}
      </div>
      {canManage && picking ? (
        <div className="px-4 py-3.5">
          <div
            role="radiogroup"
            aria-label={t(($) => $.workspace.naming_title)}
            className="inline-flex w-fit items-center gap-0.5 rounded-lg bg-muted p-0.5"
          >
            {SOURCES.map((id) => {
              const option = naming.options.find((item) => item.id === id);
              const available = option?.available ?? false;
              const selected = id === source;
              return (
                <button
                  key={id}
                  type="button"
                  role="radio"
                  aria-checked={selected}
                  disabled={!available}
                  onClick={() => {
                    if (!selected) onChange(id);
                  }}
                  className={cn(
                    "inline-flex h-7 items-center rounded-md px-2.5 text-label font-medium transition-colors focus-visible:outline-2 focus-visible:outline-ring",
                    selected
                      ? "bg-background text-foreground shadow-sm"
                      : "text-muted-foreground hover:text-foreground",
                    !available && "cursor-not-allowed text-faint-foreground hover:text-faint-foreground",
                  )}
                >
                  {sourceName(id)}
                </button>
              );
            })}
          </div>
          {!serverReady ? (
            <div className="mt-2 text-caption text-muted-foreground">
              {t(($) => $.workspace.naming_server_llm_unavailable)}
            </div>
          ) : null}
        </div>
      ) : null}
    </SettingsCard>
  );
}
