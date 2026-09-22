"use client";

import { useMemo, useState, type CSSProperties } from "react";
import { useWorkspacePaths } from "@multica/core/paths";
import { issueUsageHref } from "../../common/issue-usage-link";
import { AppLink } from "../../navigation";
import { formatTokens, formatUsd } from "../../runtimes/utils";
import { useT } from "../../i18n";
import type { IssueCostRow } from "../utils";

// Ranked list of the issues this window spent the most on, and the workspace
// usage page's entry into an individual issue's Token cost view: every row
// links to the issue with `?usage=1`, which opens the run-level breakdown on
// arrival.
//
// Ranked by cost, not tokens, because cost is the question ("where is the
// money going") and the two do not order the same way once models and cache
// reads differ. The token column stays so the reader can tell an expensive
// model from a large volume.
const ISSUE_COST_LIMIT = 10;

// `minWidth: fit-content` mirrors the leaderboard: the flexible label keeps a
// readable floor, the bar keeps a 12:1 comparison length, and the two figure
// columns stay fixed while the container scrolls on a narrow window.
const ISSUE_COST_GRID_STYLE = {
  minWidth: "fit-content",
  gridTemplateColumns: "minmax(12rem, 1.6fr) minmax(6rem, 1fr) 5rem 5rem",
} satisfies CSSProperties;

const ISSUE_COST_GRID = "grid items-center gap-3";

export function IssueCostList({ rows }: { rows: IssueCostRow[] }) {
  const { t } = useT("usage");
  const wsPaths = useWorkspacePaths();
  const [showAll, setShowAll] = useState(false);

  // Measured across every row, not just the visible ones, so a bar means the
  // same thing collapsed and expanded and nothing re-scales when the tail
  // comes into view.
  const maxCost = useMemo(
    () => rows.reduce((max, row) => Math.max(max, row.cost), 0),
    [rows],
  );

  const visibleRows = showAll ? rows : rows.slice(0, ISSUE_COST_LIMIT);

  return (
    <div className="rounded-lg border bg-card">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b px-4 pt-4 pb-3">
        <div className="min-w-0">
          <h4 className="text-body font-semibold">{t(($) => $.issues.title)}</h4>
          <p className="mt-0.5 text-caption text-muted-foreground">
            {t(($) => $.issues.caption)}
          </p>
        </div>
        {rows.length > ISSUE_COST_LIMIT ? (
          <button
            type="button"
            onClick={() => setShowAll((v) => !v)}
            className="text-caption text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
          >
            {showAll
              ? t(($) => $.issues.show_less, { count: ISSUE_COST_LIMIT })
              : t(($) => $.issues.show_all)}
          </button>
        ) : null}
      </div>
      {rows.length === 0 ? (
        <p className="px-4 py-8 text-center text-caption text-muted-foreground">
          {t(($) => $.issues.no_data)}
        </p>
      ) : (
        <div
          role="region"
          aria-label={t(($) => $.issues.title)}
          tabIndex={0}
          className="overflow-x-auto overscroll-x-contain [-webkit-overflow-scrolling:touch]"
        >
          <div>
            <div
              className={`${ISSUE_COST_GRID} border-b px-4 py-2 text-caption font-medium text-muted-foreground`}
              style={ISSUE_COST_GRID_STYLE}
            >
              <span>{t(($) => $.issues.header_issue)}</span>
              <span />
              <span className="text-right">{t(($) => $.issues.header_tokens)}</span>
              <span className="text-right">{t(($) => $.issues.header_cost)}</span>
            </div>
            {/* A real list rather than a bag of divs: the rows are a truncated
                ranking, so the count and the item boundaries matter. */}
            <ul aria-label={t(($) => $.issues.title)} className="divide-y">
              {visibleRows.map((row) => {
                const pct = maxCost > 0 ? (row.cost / maxCost) * 100 : 0;
                // An older backend may not send the identifier; the issue route
                // resolves the UUID to the same page and rewrites the address
                // bar, so the row still lands where it should.
                const title = row.title || row.identifier || row.issueId;
                return (
                  <li
                    key={row.issueId}
                    className={`${ISSUE_COST_GRID} px-4 py-2`}
                    style={ISSUE_COST_GRID_STYLE}
                  >
                    <AppLink
                      href={issueUsageHref(
                        wsPaths.issueDetail(row.identifier || row.issueId),
                      )}
                      newTabTitle={title}
                      className="flex min-w-0 items-baseline gap-2 hover:underline"
                    >
                      <span className="shrink-0 text-caption tabular-nums text-muted-foreground">
                        {row.identifier || "—"}
                      </span>
                      <span className="min-w-0 truncate text-body font-medium">
                        {title}
                      </span>
                    </AppLink>
                    <div className="relative h-2 overflow-hidden rounded-full bg-muted">
                      <div
                        className="h-full rounded-full bg-chart-1 transition-[width] duration-300 ease-out"
                        style={{ width: `${pct}%` }}
                      />
                    </div>
                    <div className="text-right text-caption tabular-nums text-muted-foreground">
                      {formatTokens(row.tokens)}
                    </div>
                    <div className="text-right text-body font-medium tabular-nums">
                      {formatUsd(row.cost)}
                    </div>
                  </li>
                );
              })}
            </ul>
          </div>
        </div>
      )}
    </div>
  );
}
