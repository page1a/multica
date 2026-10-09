"use client";

import { useState, type ReactNode } from "react";
import { AlertCircle, CheckCircle2 } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { projectMemoryMonitorOptions, projectMemoryOptions } from "@multica/core/projects";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type { KnowledgeAuditChange, KnowledgeSediment, MonitorWrite } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { AppLink } from "../../navigation";
import { useT, useTimeAgo } from "../../i18n";

function formatObservedAt(value: string | null | undefined, fallback: string) {
	if (!value) return fallback;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

const SECTION_LIMIT = 5;

/** At most five rows until expanded, so every row stays one tap from its source. */
function ExpandableRows<T>({ items, render }: { items: T[]; render: (item: T) => ReactNode }) {
  const { t } = useT("projects");
  const [expanded, setExpanded] = useState(false);
  return (
    <>
      {(expanded ? items : items.slice(0, SECTION_LIMIT)).map(render)}
      {items.length > SECTION_LIMIT ? (
        <button
          type="button"
          className="min-h-11 text-caption text-primary hover:underline sm:min-h-0"
          onClick={() => setExpanded((value) => !value)}
        >
          {expanded
            ? t(($) => $.detail.memory_show_less)
            : t(($) => $.detail.memory_show_all, { count: items.length })}
        </button>
      ) : null}
    </>
  );
}

/** One monitor section: a heading with its count, then its rows. */
function MonitorSection<T>({
  id, title, count, items, render,
}: {
  id: string;
  title: string;
  count?: ReactNode;
  items: T[];
  render: (item: T) => ReactNode;
}) {
  return (
    <div className="mt-3 space-y-1.5" aria-labelledby={id}>
      <p id={id} className="flex items-baseline gap-2 text-caption text-muted-foreground">
        <span className="min-w-0 flex-1">{title}</span>
        {count !== undefined ? <span className="shrink-0">{count}</span> : null}
      </p>
      <ExpandableRows items={items} render={render} />
    </div>
  );
}

export function ProjectMemoryCard({ projectId }: { projectId: string }) {
	const { t } = useT("projects");
  const workspaceId = useWorkspaceId();
  const workspacePaths = useWorkspacePaths();
  const timeAgo = useTimeAgo();
  const { data, isLoading } = useQuery(projectMemoryOptions(workspaceId, projectId));
  const { data: monitor } = useQuery(projectMemoryMonitorOptions(workspaceId, projectId));
  const issueHref = (id: string) => workspacePaths.issueDetail?.(id) ?? "#";
  const chatHref = (id: string) => workspacePaths.chatSession?.(id) ?? "#";
  // The monitor window carries deletions; older servers only send recent_sediments.
  const writes: (KnowledgeSediment & Partial<MonitorWrite>)[] = monitor?.writes ?? data?.recent_sediments ?? [];
  const actionLabel = (action: NonNullable<KnowledgeAuditChange["action"]>, entry: string) => {
    switch (action) {
      case "update":
        return t(($) => $.detail.memory_action_update, { entry });
      case "merge":
        return t(($) => $.detail.memory_action_merge, { entry });
      case "supersede":
        return t(($) => $.detail.memory_action_supersede, { entry });
      default:
        return t(($) => $.detail.memory_action_new);
    }
  };

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
          {(data?.locations ?? []).map((location) => {
            const behind = !location.exists && location.mainline_ref ? location.mainline_ref : null;
            return (
              <div key={location.key} className="flex items-center gap-2 text-caption">
                {location.exists ? <CheckCircle2 className="size-4 text-emerald-600" /> : <AlertCircle className="size-4 text-amber-600" />}
                <span className="min-w-0 flex-1 truncate" title={location.path}>{location.path}</span>
                <span
                  className={cn("shrink-0", location.exists ? "text-emerald-700" : "text-amber-700")}
                  title={behind ? t(($) => $.detail.memory_behind_title, { ref: behind }) : undefined}
                >
                  {location.exists
                    ? t(($) => $.detail.memory_present)
                    : behind
                      ? t(($) => $.detail.memory_behind, { ref: behind })
                      : t(($) => $.detail.memory_missing)}
                </span>
              </div>
            );
          })}
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
      {writes.length > 0 ? (
        <MonitorSection
          id="project-memory-recent-heading"
          title={monitor
            ? t(($) => $.detail.memory_recent_window, { days: monitor.days })
            : t(($) => $.detail.memory_recent)}
          count={monitor ? writes.length : undefined}
          items={writes}
          render={(sediment) => {
            const changes = sediment.changes.map((change) => {
              const files = (change.files?.length ? change.files : [change.location]).join(", ");
              const action = change.action ? actionLabel(change.action, change.entry ?? "") : "";
              return action ? `${action} · ${files}` : files;
            });
            const rolledUp = (sediment.sources ?? [])
              .map((src) => src.identifier ?? src.title)
              .filter(Boolean)
              .join(", ");
            const href = sediment.issue_id
              ? workspacePaths.issueDetail?.(sediment.issue_id)
              : sediment.chat_session_id
                ? workspacePaths.chatSession?.(sediment.chat_session_id)
                : undefined;
            const source = sediment.issue_identifier
              ?? (sediment.source_accessible === false
                ? t(($) => $.detail.memory_chat_private)
                : t(($) => $.detail.memory_recent_chat, { title: sediment.source_title || sediment.chat_session_id?.slice(0, 8) || "" }));
            return (
              <div key={sediment.id} className="text-caption">
                <div className="flex items-baseline gap-2">
                  <AppLink
                    href={href ?? "#"}
                    className="min-w-0 flex-1 truncate text-primary hover:underline"
                    title={sediment.source_title || undefined}
                  >
                    {source}
                  </AppLink>
                  {!sediment.verified ? (
                    <span className="shrink-0 text-amber-700" title={t(($) => $.detail.memory_recent_unverified_title)}>
                      {t(($) => $.detail.memory_recent_unverified)}
                    </span>
                  ) : null}
                  {sediment.deleted_lines ? (
                    <span className="shrink-0 text-muted-foreground">
                      {t(($) => $.detail.memory_deleted_lines, { count: sediment.deleted_lines })}
                    </span>
                  ) : null}
                  <span className="shrink-0 text-muted-foreground">{timeAgo(sediment.created_at)}</span>
                </div>
                {rolledUp ? (
                  <p className="text-muted-foreground [overflow-wrap:anywhere]">
                    {t(($) => $.detail.memory_recent_from, { sources: rolledUp })}
                  </p>
                ) : null}
                {/* Own wrapping lines: a phone has no hover to read a cut-off path. */}
                {changes.map((line, i) => (
                  <p key={i} className="text-muted-foreground [overflow-wrap:anywhere]">{line}</p>
                ))}
              </div>
            );
          }}
        />
      ) : data?.latest_sediment_at ? (
        <p className="mt-3 text-caption text-muted-foreground">
          {t(($) => $.detail.memory_last_sediment, { time: formatObservedAt(data.latest_sediment_at, t(($) => $.detail.memory_no_check)) })}
        </p>
      ) : null}
      {monitor ? (
        <>
          <MonitorSection
            id="project-memory-unsettled-heading"
            title={t(($) => $.detail.memory_unsettled)}
            count={monitor.unsettled.length}
            items={monitor.unsettled}
            render={(item) => (
              <div key={item.issue_id} className="flex items-baseline gap-2 text-caption">
                <AppLink href={issueHref(item.issue_id)} className="min-w-0 flex-1 text-primary hover:underline [overflow-wrap:anywhere]">
                  {item.identifier} {item.title}
                </AppLink>
                <span className="shrink-0 text-amber-700">
                  {item.reason === "none"
                    ? t(($) => $.detail.memory_unsettled_none)
                    : t(($) => $.detail.memory_unsettled_unaudited)}
                </span>
              </div>
            )}
          />
          <MonitorSection
            id="project-memory-rounds-heading"
            title={t(($) => $.detail.memory_rounds)}
            count={t(($) => $.detail.memory_rounds_summary, {
              opened: monitor.rounds.opened, open: monitor.rounds.open, idle: monitor.rounds.idle,
            })}
            items={monitor.rounds.items}
            render={(round) => (
              <div key={round.issue_id} className="flex items-baseline gap-2 text-caption">
                <AppLink href={issueHref(round.issue_id)} className="min-w-0 flex-1 text-primary hover:underline [overflow-wrap:anywhere]">
                  {round.identifier} {round.title}
                </AppLink>
                <span className={cn("shrink-0", round.idle ? "text-amber-700" : "text-muted-foreground")}>
                  {round.idle
                    ? t(($) => $.detail.memory_round_idle)
                    : round.wrote
                      ? t(($) => $.detail.memory_round_wrote)
                      : t(($) => $.detail.memory_round_open)}
                </span>
              </div>
            )}
          />
          <MonitorSection
            id="project-memory-chats-heading"
            title={t(($) => $.detail.memory_chats)}
            count={monitor.chats.length}
            items={monitor.chats}
            render={(chat) => (
              <div key={chat.chat_session_id} className="text-caption">
                {chat.accessible ? (
                  <AppLink href={chatHref(chat.chat_session_id)} className="block text-primary hover:underline [overflow-wrap:anywhere]">
                    {t(($) => $.detail.memory_recent_chat, { title: chat.title || chat.chat_session_id.slice(0, 8) })}
                  </AppLink>
                ) : (
                  <p className="truncate text-muted-foreground">{t(($) => $.detail.memory_chat_private)}</p>
                )}
                <p className="text-muted-foreground">
                  {t(($) => $.detail.memory_chat_summary, {
                    dispatched: chat.dispatched, reported: chat.reported,
                    no_conclusion: chat.no_conclusion, open: chat.open,
                  })}
                </p>
                <ExpandableRows items={chat.tickets} render={(ticket) => (
                  <div key={ticket.issue_id} className="flex items-baseline gap-2 pl-3">
                    <AppLink href={issueHref(ticket.issue_id)} className="min-w-0 flex-1 text-primary hover:underline [overflow-wrap:anywhere]">
                      {ticket.identifier} {ticket.title}
                    </AppLink>
                    <span className={cn("shrink-0", ticket.flow === "no_conclusion" ? "text-amber-700" : "text-muted-foreground")}>
                      {ticket.flow === "reported"
                        ? t(($) => $.detail.memory_flow_reported)
                        : ticket.flow === "no_conclusion"
                          ? t(($) => $.detail.memory_flow_no_conclusion)
                          : t(($) => $.detail.memory_flow_open)}
                    </span>
                  </div>
                )} />
              </div>
            )}
          />
        </>
      ) : null}
    </section>
  );
}
