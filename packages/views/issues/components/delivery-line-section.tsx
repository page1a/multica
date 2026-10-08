"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronRight } from "lucide-react";
import { issueDeliveryLinesOptions } from "@multica/core/issues";
import { useWorkspacePaths } from "@multica/core/paths";
import type { DeliveryLineCommit } from "@multica/core/api/schemas";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";

const VISIBLE_COMMITS = 5;

/**
 * Sub-issues deliver onto their parent's branch instead of opening their own
 * PR (DENE-1537). On such a sub-issue this shows which parent's branch it
 * delivers to and what it merged back; on the parent it lists each
 * sub-issue's commits. Hidden on every other issue.
 */
export function DeliveryLineSection({ wsId, issueId }: { wsId: string; issueId: string }) {
  const { t } = useT("issues");
  const paths = useWorkspacePaths();
  const [open, setOpen] = useState(true);
  const { data } = useQuery(issueDeliveryLinesOptions(wsId, issueId));
  const line = data?.line ?? null;
  const contributions = data?.contributions ?? [];

  if (!line && contributions.length === 0) return null;

  const statusText = (status: string, commits: number) => {
    if (status === "merged") return t(($) => $.detail.delivery_line.status_merged, { count: commits });
    if (status === "conflict") return t(($) => $.detail.delivery_line.status_conflict);
    return t(($) => $.detail.delivery_line.status_open);
  };

  return (
    <div>
      <button
        type="button"
        aria-expanded={open}
        className={`mb-2 flex w-full items-center gap-1 rounded-md px-2 py-1 text-caption font-medium transition-colors hover:bg-accent/70 ${open ? "" : "text-muted-foreground hover:text-foreground"}`}
        onClick={() => setOpen(!open)}
      >
        <span className="truncate">
          {line ? t(($) => $.detail.delivery_line.section) : t(($) => $.detail.delivery_line.section_children)}
        </span>
        <ChevronRight className={`!size-3 shrink-0 stroke-[2.5] text-muted-foreground transition-transform ${open ? "rotate-90" : ""}`} />
      </button>
      {open && line && (
        <div className="space-y-1 pl-2 text-xs">
          <AppLink
            href={paths.issueDetail(line.owner_issue_id)}
            className="block truncate text-foreground hover:underline"
          >
            {t(($) => $.detail.delivery_line.delivers_to, { identifier: line.owner_identifier })}
          </AppLink>
          {line.branch ? <div className="truncate font-mono text-muted-foreground" title={line.branch}>{line.branch}</div> : null}
          <div className={line.status === "conflict" ? "text-destructive" : "text-muted-foreground"}>
            {statusText(line.status, line.commits.length)}
          </div>
          {line.status === "conflict" && (line.conflict_files ?? []).length > 0 ? (
            <ul className="space-y-0.5 font-mono text-muted-foreground">
              {(line.conflict_files ?? []).map((file) => (
                <li key={file} className="truncate" title={file}>{file}</li>
              ))}
            </ul>
          ) : null}
          <CommitList commits={line.commits} />
        </div>
      )}
      {open && !line && (
        <ul className="space-y-1 pl-2 text-xs">
          {contributions.map((c) => (
            <ContributionRow
              key={c.issue_id}
              href={paths.issueDetail(c.issue_id)}
              identifier={c.identifier}
              title={c.title}
              status={c.status}
              statusText={statusText(c.status, c.commits.length)}
              commits={c.commits}
              conflictFiles={c.conflict_files ?? []}
            />
          ))}
        </ul>
      )}
    </div>
  );
}

function ContributionRow({
  href,
  identifier,
  title,
  status,
  statusText,
  commits,
  conflictFiles,
}: {
  href: string;
  identifier: string;
  title: string;
  status: string;
  statusText: string;
  commits: DeliveryLineCommit[];
  conflictFiles: string[];
}) {
  const [expanded, setExpanded] = useState(false);
  const expandable = commits.length > 0 || conflictFiles.length > 0;
  return (
    <li>
      <div className="flex min-h-7 items-center gap-1.5">
        <button
          type="button"
          aria-expanded={expanded}
          disabled={!expandable}
          aria-label={identifier}
          onClick={() => setExpanded(!expanded)}
          className="flex size-5 shrink-0 items-center justify-center rounded-xs text-muted-foreground hover:bg-accent disabled:opacity-30"
        >
          <ChevronRight className={`!size-3 stroke-[2.5] transition-transform ${expanded ? "rotate-90" : ""}`} />
        </button>
        <AppLink href={href} className="shrink-0 font-medium text-foreground hover:underline">
          {identifier}
        </AppLink>
        <span className="min-w-0 flex-1 truncate text-muted-foreground" title={title}>{title}</span>
        <span className={`shrink-0 ${status === "conflict" ? "text-destructive" : "text-muted-foreground"}`}>{statusText}</span>
      </div>
      {expanded && (
        <div className="space-y-0.5 pb-1 pl-6">
          {conflictFiles.map((file) => (
            <div key={file} className="truncate font-mono text-destructive" title={file}>{file}</div>
          ))}
          <CommitList commits={commits} />
        </div>
      )}
    </li>
  );
}

function CommitList({ commits }: { commits: DeliveryLineCommit[] }) {
  const { t } = useT("issues");
  const [all, setAll] = useState(false);
  if (commits.length === 0) return null;
  const shown = all ? commits : commits.slice(0, VISIBLE_COMMITS);
  return (
    <ul className="space-y-0.5">
      {shown.map((c) => (
        <li key={c.sha} className="flex min-w-0 gap-1.5" title={c.subject}>
          <span className="shrink-0 font-mono text-muted-foreground">{c.sha.slice(0, 7)}</span>
          <span className="truncate">{c.subject}</span>
        </li>
      ))}
      {!all && commits.length > VISIBLE_COMMITS ? (
        <li>
          <button type="button" className="text-muted-foreground hover:text-foreground" onClick={() => setAll(true)}>
            {t(($) => $.detail.delivery_line.show_all, { count: commits.length })}
          </button>
        </li>
      ) : null}
    </ul>
  );
}
