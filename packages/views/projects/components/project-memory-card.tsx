"use client";

import { AlertCircle, CheckCircle2 } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { projectMemoryOptions } from "@multica/core/projects";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { cn } from "@multica/ui/lib/utils";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";

function formatObservedAt(value: string | null | undefined, fallback: string) {
	if (!value) return fallback;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

export function ProjectMemoryCard({ projectId }: { projectId: string }) {
	const { t } = useT("projects");
  const workspaceId = useWorkspaceId();
  const workspacePaths = useWorkspacePaths();
  const { data, isLoading } = useQuery(projectMemoryOptions(workspaceId, projectId));

  return (
    <section className="rounded-lg border bg-card p-4 shadow-xs" aria-labelledby="project-memory-heading">
      <div className="mb-3 flex items-start justify-between gap-3">
        <div>
          <h3 id="project-memory-heading" className="text-body font-medium">{t(($) => $.detail.memory_title)}</h3>
          <p className="text-caption text-muted-foreground">{t(($) => $.detail.memory_hint)}</p>
        </div>
        <span className="text-caption text-muted-foreground">
          {formatObservedAt(data?.observed_at, t(($) => $.detail.memory_no_check))}
        </span>
      </div>
      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 5 }, (_, index) => <Skeleton key={index} className="h-5 w-full" />)}
        </div>
      ) : (
        <div className="space-y-2">
          {(data?.locations ?? []).map((location) => (
            <div key={location.key} className="flex items-center gap-2 text-caption">
              {location.exists ? <CheckCircle2 className="size-4 text-emerald-600" /> : <AlertCircle className="size-4 text-amber-600" />}
              <span className="min-w-0 flex-1 truncate" title={location.path}>{location.path}</span>
              <span className={cn("shrink-0", location.exists ? "text-emerald-700" : "text-amber-700")}>
                {location.exists ? t(($) => $.detail.memory_present) : t(($) => $.detail.memory_missing)}
              </span>
            </div>
          ))}
        </div>
      )}
      {data?.sediment_issue ? (
        <AppLink
          href={workspacePaths.issueDetail ? workspacePaths.issueDetail(data.sediment_issue.id) : `#`}
          className="mt-3 inline-flex text-caption text-primary hover:underline"
        >
          {t(($) => $.detail.memory_ticket, { id: data.sediment_issue.identifier })}
        </AppLink>
      ) : !data?.sediment_agent_configured ? (
        <div className="mt-3 flex flex-wrap items-center gap-1.5 text-caption text-amber-700">
          <span>{data?.sediment_error ? `${data.sediment_error}。` : t(($) => $.detail.memory_unconfigured_error)}</span>
          <AppLink
            href={workspacePaths.settings ? workspacePaths.settings() : "#"}
            className="text-primary hover:underline font-medium"
          >
            {t(($) => $.detail.memory_configure_link)}
          </AppLink>
        </div>
      ) : data?.sediment_error ? (
        <p className="mt-3 text-caption text-amber-700">{data.sediment_error}</p>
      ) : null}
      {data?.latest_sediment_at ? (
        <p className="mt-3 text-caption text-muted-foreground">
          {t(($) => $.detail.memory_last_sediment, { time: formatObservedAt(data.latest_sediment_at, t(($) => $.detail.memory_no_check)) })}
        </p>
      ) : null}
    </section>
  );
}
