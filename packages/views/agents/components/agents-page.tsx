"use client";

import { useCallback, useMemo, useRef, useState } from "react";
import {
  AlertCircle,
  Bot,
  ChevronRight,
  Lock,
  Plus,
  Users,
} from "lucide-react";
import { useQueries, useQuery } from "@tanstack/react-query";
import { useVirtualizer } from "@tanstack/react-virtual";
import type {
  Agent,
  AgentRuntime,
  MemberWithUser,
} from "@multica/core/types";
import {
  type AgentActivity,
  agentRunCounts30dOptions,
  effectiveAccessScope,
  isAgentRuntimeBound,
  isAgentWorkEnabled,
  useWorkspaceActivityMap,
  useWorkspacePresenceMap,
  VISIBILITY_TOOLTIP,
  type AgentPresenceDetail,
} from "@multica/core/agents";
import {
  type AgentListFilters,
  useAgentsViewStore,
  AGENT_DEFAULT_HIDDEN_COLUMNS,
  AGENT_GROUPINGS,
  AGENT_SCOPES,
  type AgentColumnKey,
  type AgentsScope,
  type AgentSortField,
} from "@multica/core/agents/stores";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  agentListOptions,
  memberListOptions,
  squadListOptions,
  squadMembersOptions,
} from "@multica/core/workspace/queries";
import { runtimeDisplayLabel, runtimeListOptions } from "@multica/core/runtimes";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import {
  LIST_GRID_BOTTOM_CLEARANCE,
  ListGrid,
  ListGridBody,
  ListGridCell,
  ListGridHeader,
  ListGridHeaderCell,
  ListGridRow,
  type ListGridSortDirection,
} from "@multica/ui/components/ui/list-grid";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { AppLink, useNavigation, useRowLink } from "../../navigation";
import { ActorAvatar } from "../../common/actor-avatar";
import { ProviderLogo } from "../../runtimes/components/provider-logo";
import {
  CollectionPageHeader,
  CollectionPageHeaderAction,
  CollectionPageState,
} from "../../layout/collection-page";
import { availabilityConfig } from "../presence";
import { AgentRowActions } from "./agent-row-actions";
import { providerSeatModelDisplay } from "./provider-seat-model";
import {
  flattenSpecializationItems,
  SPECIALIZATION_DERIVE_ROW_HEIGHT,
} from "./agents-page-specializations";
import { isSpecialization, runtimeInheritanceState } from "../specialization";
import {
  AgentListToolbar,
  countActiveFilterDimensions,
} from "./agent-list-toolbar";
import { useLocale, useT } from "../../i18n";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import {
  buildAgentSquadsMap,
  flattenAgentListItems,
  GROUP_HEADER_HEIGHT,
  needsSquadMembersFetch,
  resolveSquadRosters,
  rowMatchesSquadFilter,
  type AgentSquadGroup,
  type FetchedSquadMembers,
} from "./agents-page-squads";

// Column template — single source of truth for header, rows, and skeletons.
// Same conventions as the skills/autopilots lists (see list-grid.tsx):
// deterministic var-width tracks, two-zone responsiveness (≥@2xl WYSIWYG
// with min-width + horizontal-scroll escape valve; <@2xl static core set of
// name + status, toggles don't apply).
//
// Agents are identity-type entities (few, avatar + persona), so rows are
// the TWO-LINE form: avatar left, name + description right, 64px tall —
// the documented exception to the single-line management-list rule.
const GRID_COLS =
  "grid-cols-[0.75rem_minmax(120px,1fr)_var(--agc-status-mobile)_3.75rem_0.75rem] " +
  "@2xl:grid-cols-[0.75rem_1rem_minmax(200px,1fr)_var(--agc-status-desktop)_var(--agc-owner)_var(--agc-access)_var(--agc-runtime)_var(--agc-lastactive)_var(--agc-runs)_var(--agc-model)_var(--agc-created)_3.75rem_0.75rem]";

// Two-line rows; the virtualizer's fixed-size contract.
const ROW_HEIGHT = 64;

// Single source for hideable column widths: track vars and the grid's
// min-width derive from the same numbers.
const COLUMN_WIDTHS: Record<AgentColumnKey, number> = {
  // Sized for the worst case "Online · 2 tasks" (~140px incl. padding);
  // idle rows show only the dot + label and leave some in-track slack.
  status: 144,
  owner: 144,
  // Fits the longest label "Specific people" (~120px incl. padding).
  access: 132,
  runtime: 144,
  lastActive: 120,
  runs: 88,
  model: 120,
  created: 104,
};

// Fixed tracks (edges 12+12, checkbox 16, name min 200, switch+kebab 60)
// plus the 11 gap-x-3 gaps between the wide template's 12 tracks
// (zero-width tracks still carry gaps).
const FIXED_TRACKS_WIDTH = 300 + 11 * 12;

function columnTrackVars(
  isVisible: (key: AgentColumnKey) => boolean,
): React.CSSProperties {
  const width = (key: AgentColumnKey) =>
    isVisible(key) ? `${COLUMN_WIDTHS[key]}px` : "0px";
  const minWidth =
    FIXED_TRACKS_WIDTH +
    (Object.keys(COLUMN_WIDTHS) as AgentColumnKey[]).reduce(
      (sum, key) => sum + (isVisible(key) ? COLUMN_WIDTHS[key] : 0),
      0,
    );
  return {
    "--agc-status-mobile": isVisible("status") ? "96px" : "0px",
    "--agc-status-desktop": width("status"),
    "--agc-owner": width("owner"),
    "--agc-access": width("access"),
    "--agc-runtime": width("runtime"),
    "--agc-lastactive": width("lastActive"),
    "--agc-runs": width("runs"),
    "--agc-model": width("model"),
    "--agc-created": width("created"),
    "--agc-minw": `${minWidth}px`,
  } as React.CSSProperties;
}

export interface AgentListRow {
  agent: Agent;
  runtime: AgentRuntime | null;
  presence: AgentPresenceDetail | null;
  activity: AgentActivity | null;
  runCount: number;
  /** Days since the last bucket with runs; null = nothing in the window. */
  lastActiveDays: number | null;
  owner: MemberWithUser | null;
  isOwnedByMe: boolean;
  canManage: boolean;
  /** Active squad ids this agent belongs to. Empty = no squad. */
  squadIds: string[];
  /**
   * False while an active squad roster is still unknown and this row is
   * not already placed in a known squad. Empty `squadIds` then must not
   * be treated as "no squad".
   */
  squadMembershipKnown?: boolean;
}

// Most recent activity bucket with runs, as "days ago" (0 = today).
// Day-granularity by design — derived from the same 30-day buckets the
// detail page charts, no extra API.
function lastActiveDaysAgo(activity: AgentActivity | null): number | null {
  if (!activity) return null;
  for (let i = activity.buckets.length - 1; i >= 0; i--) {
    const bucket = activity.buckets[i];
    if (bucket && bucket.total > 0) return activity.buckets.length - 1 - i;
  }
  return null;
}

function matchesAgentSearch(row: AgentListRow, query: string): boolean {
  if (!query) return true;
  const { agent } = row;
  return (
    agent.name.toLowerCase().includes(query) ||
    matchesPinyin(agent.name, query) ||
    (agent.description?.toLowerCase().includes(query) ?? false) ||
    (agent.description ? matchesPinyin(agent.description, query) : false)
  );
}

/**
 * Pure row-filter predicate: returns true if the row matches all active
 * filter dimensions. Empty filter arrays are inactive (the row passes). The
 * `access` dimension derives its key via `effectiveAccessScope` so the column
 * and the filter share one derivation. Exported for testing — the page wires
 * it inside its `useMemo`.
 */
export function rowMatchesFilters(
  row: AgentListRow,
  filters: AgentListFilters,
  query: string,
): boolean {
  if (!matchesAgentSearch(row, query.trim().toLowerCase())) return false;
  if (
    filters.availability.length > 0 &&
    (!row.presence || !filters.availability.includes(row.presence.availability))
  ) {
    return false;
  }
  if (
    filters.runtimes.length > 0 &&
    !filters.runtimes.includes(row.agent.runtime_id)
  ) {
    return false;
  }
  if (
    filters.owners.length > 0 &&
    (!row.agent.owner_id || !filters.owners.includes(row.agent.owner_id))
  ) {
    return false;
  }
  if (
    filters.models.length > 0 &&
    !filters.models.includes(row.agent.model)
  ) {
    return false;
  }
  if (
    filters.access.length > 0 &&
    !filters.access.includes(
      effectiveAccessScope(
        row.agent.permission_mode,
        row.agent.invocation_targets,
      ),
    )
  ) {
    return false;
  }
  if (
    !rowMatchesSquadFilter(
      row.squadIds,
      filters.squads,
      row.squadMembershipKnown !== false,
    )
  ) {
    return false;
  }
  return true;
}

/**
 * Bulk-access dialog confirm-button enablement is centralized in
 * `@multica/core/agents` as `isAccessChangeReady` (MUL-3963). The dialog
 * consumes it; the picker also gates its internal Save button on the same
 * predicate (its own Save button is hidden via `hideFooter` in the bulk flow).
 */
import { isAccessChangeReady } from "@multica/core/agents";
import { AgentBatchToolbar } from "./agent-batch-toolbar";
export { isAccessChangeReady };

export interface AgentsPageProps {
  /** Desktop-only daemon wiring, currently unused by the list (kept for
   *  platform-layer compatibility; the runtime filter lists runtimes by
   *  name rather than grouped machines). */
  localDaemonId?: string | null;
  localMachineName?: string | null;
  hasLocalMachine?: boolean;
}

// ---------------------------------------------------------------------------
// Page header
// ---------------------------------------------------------------------------

function PageHeaderBar({
  totalCount,
  onCreate,
}: {
  totalCount: number;
  onCreate: () => void;
}) {
  const { t } = useT("agents");
  return (
    <CollectionPageHeader
      icon={Bot}
      title={t(($) => $.page.title)}
      count={totalCount}
      description={t(($) => $.page.tagline)}
      learnMore={{
        href: "https://multica.ai/docs/agents",
        label: t(($) => $.page.learn_more),
      }}
      actions={
        <CollectionPageHeaderAction
          icon={Plus}
          label={t(($) => $.page.new_agent)}
          onClick={onCreate}
        />
      }
    />
  );
}

function ListError({
  onCreate,
  listError,
  onRetry,
}: {
  onCreate: () => void;
  listError: unknown;
  onRetry: () => void;
}) {
  const { t } = useT("agents");
  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <PageHeaderBar totalCount={0} onCreate={onCreate} />
      <CollectionPageState
        role="alert"
        tone="destructive"
        icon={AlertCircle}
        title={t(($) => $.page.list_load_failed)}
        description={
          listError instanceof Error
            ? listError.message
            : t(($) => $.page.list_load_failed_default)
        }
        actions={
          <Button type="button" variant="outline" size="sm" onClick={onRetry}>
            {t(($) => $.page.try_again)}
          </Button>
        }
      />
    </div>
  );
}

function EmptyState({ onCreate }: { onCreate: () => void }) {
  const { t } = useT("agents");
  return (
    <CollectionPageState
      icon={Bot}
      title={t(($) => $.empty.title)}
      description={t(($) => $.empty.description)}
      actions={
        <Button type="button" onClick={onCreate} size="sm">
          <Plus aria-hidden="true" className="size-3" />
          {t(($) => $.page.new_agent)}
        </Button>
      }
    />
  );
}

function SquadGroupHeader({
  group,
  href,
}: {
  group: AgentSquadGroup<AgentListRow>;
  href: string | null;
}) {
  const { t } = useT("agents");
  const name = group.squad?.name ?? t(($) => $.toolbar.no_squad);
  const count = group.rows.length;
  return (
    <div
      role="row"
      data-testid="agent-squad-group"
      data-squad-id={group.id}
      className="sticky top-9 z-[9] col-span-full flex h-9 items-center gap-2 bg-background px-3"
    >
      {group.squad ? (
        <ActorAvatar
          actorType="squad"
          actorId={group.squad.id}
          size="sm"
          className="shrink-0"
        />
      ) : (
        <Users
          aria-hidden="true"
          className="size-4 shrink-0 text-muted-foreground"
        />
      )}
      {href ? (
        <AppLink
          href={href}
          newTabTitle={name}
          className="min-w-0 truncate text-caption font-medium text-foreground hover:underline"
        >
          {name}
        </AppLink>
      ) : (
        <span className="min-w-0 truncate text-caption font-medium">
          {name}
        </span>
      )}
      <span className="ml-0.5 tabular-nums text-caption text-muted-foreground">
        {count}
      </span>
    </div>
  );
}

// The entry point that makes a base role able to have specialisations at all:
// creating one is otherwise a blank-slate form with a base-role dropdown, and
// the intent "add a variant of THIS agent" would have to be re-stated there.
// Navigating (rather than opening the dialog in place) keeps one create flow
// with one submit path; the base role travels as a query param the form seeds
// from, exactly like `?duplicate=`.
function DeriveSpecializationRow({
  baseName,
  onDerive,
}: {
  baseName: string;
  onDerive: () => void;
}) {
  const { t } = useT("agents");
  return (
    <div role="row" className="col-span-full px-3">
      <button
        type="button"
        data-testid="agents-derive-specialization"
        onClick={(event) => {
          event.stopPropagation();
          onDerive();
        }}
        style={{ height: SPECIALIZATION_DERIVE_ROW_HEIGHT - 4 }}
        className="flex w-full items-center gap-1.5 rounded-md pl-8 pr-2 text-left text-caption text-muted-foreground hover:bg-accent hover:text-accent-foreground"
      >
        <Plus aria-hidden="true" className="size-3 shrink-0" />
        <span className="min-w-0 truncate">
          {t(($) => $.specialization.derive_entry, { name: baseName })}
        </span>
      </button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Cells
// ---------------------------------------------------------------------------

function CheckboxCell({
  checked,
  onToggle,
}: {
  checked: boolean;
  onToggle: () => void;
}) {
  return (
    <ListGridCell className="hidden justify-center px-0 @2xl:flex">
      <button
        type="button"
        aria-pressed={checked}
        onClick={(e) => {
          e.stopPropagation();
          onToggle();
        }}
        className={`-m-1.5 flex items-center p-1.5 ${
          checked ? "" : "opacity-0 transition-opacity group-hover/row:opacity-100"
        }`}
      >
        <Checkbox
          checked={checked}
          tabIndex={-1}
          className="pointer-events-none"
        />
      </button>
    </ListGridCell>
  );
}

// Two-line identity cell: avatar left, name + description right. The
// documented exception to the single-line rule — agents are few and
// identity-rich, so this is the "team roster" form (GitHub org members,
// Slack member list).
//
// In the nested view (DENE-304) the same cell carries the relationship: a
// base role gets the fold control and its `N 个特化` count, a specialisation is
// indented behind a connector and labelled. The fold control lives here rather
// than in its own grid track so the column template — and every other cell —
// stays exactly what it is in the flat view.
function NameCell({
  row,
  nesting,
  nested = false,
}: {
  row: AgentListRow;
  nesting?: {
    childCount: number;
    expanded: boolean;
    onToggle: () => void;
  } | null;
  /**
   * True only when this row is actually rendered UNDER its base role. The
   * indent and its connector describe a position in the list, so they must
   * follow the layout, not the agent: a specialisation shown in the flat list
   * — or one whose base role a filter hid — would otherwise draw an elbow
   * pointing at a row that is not above it. The tag and the "from X" line are
   * facts about the agent and stay either way.
   */
  nested?: boolean;
}) {
  const { t } = useT("agents");
  const { agent, isOwnedByMe } = row;
  const isArchived = !!agent.archived_at;
  const isDisabled = !isAgentWorkEnabled(agent);
  const isPrivate = agent.visibility === "private";
  const isChild = isSpecialization(agent);
  return (
    <ListGridCell
      className={`gap-3 ${nested ? "relative pl-8" : ""}`}
      data-specialization={isChild ? "true" : undefined}
    >
      {nested && (
        // Indent guide: a short elbow from the base role's avatar column down
        // into the child's name. Decorative — the "特化" chip is what carries
        // the relationship for screen readers.
        <span
          aria-hidden="true"
          data-testid="agents-specialization-indent"
          className="absolute left-3 top-0 h-1/2 w-3 rounded-bl-sm border-b border-l border-border"
        />
      )}
      {nesting && (
        <button
          type="button"
          aria-expanded={nesting.expanded}
          aria-label={
            nesting.expanded
              ? t(($) => $.specialization.collapse, { count: nesting.childCount })
              : t(($) => $.specialization.expand, { count: nesting.childCount })
          }
          data-testid="agents-specialization-toggle"
          onClick={(event) => {
            event.stopPropagation();
            nesting.onToggle();
          }}
          className="-m-1 flex size-5 shrink-0 items-center justify-center rounded-sm text-muted-foreground hover:bg-accent hover:text-accent-foreground"
        >
          <ChevronRight
            aria-hidden="true"
            className={`size-3.5 transition-transform ${nesting.expanded ? "rotate-90" : ""}`}
          />
        </button>
      )}
      <ActorAvatar
        actorType="agent"
        actorId={agent.id}
        size="lg"
        className={`shrink-0 ${isArchived || isDisabled ? "opacity-50" : ""} ${isArchived ? "grayscale" : ""}`}
        showStatusDot
      />
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-2">
          <span
            className={`min-w-0 truncate text-body font-medium ${
              isArchived || isDisabled ? "text-muted-foreground" : ""
            }`}
          >
            {agent.name}
          </span>
          {isChild && (
            <span
              data-testid="agents-specialization-chip"
              className="shrink-0 rounded-xs border border-border px-1 text-micro text-muted-foreground"
            >
              {t(($) => $.specialization.tag)}
            </span>
          )}
          {nesting && nesting.childCount > 0 && (
            <span
              data-testid="agents-specialization-count"
              className="shrink-0 rounded-xs bg-muted px-1 text-micro tabular-nums text-muted-foreground"
            >
              {t(($) => $.specialization.count, { count: nesting.childCount })}
            </span>
          )}
          {isPrivate && !isArchived && (
            <Tooltip>
              <TooltipTrigger
                render={
                  <Lock className="h-3 w-3 shrink-0 text-faint-foreground" />
                }
              />
              <TooltipContent>{VISIBILITY_TOOLTIP.private}</TooltipContent>
            </Tooltip>
          )}
          {isOwnedByMe && (
            <span className="shrink-0 rounded-xs bg-muted px-1 text-micro font-medium text-muted-foreground">
              {t(($) => $.row.you)}
            </span>
          )}
        </div>
        <div className="mt-0.5 flex min-w-0 items-center gap-1.5">
          {isChild && agent.parent_agent_name ? (
            <span className="shrink-0 text-caption text-muted-foreground">
              {t(($) => $.specialization.derived_from, {
                name: agent.parent_agent_name,
              })}
            </span>
          ) : null}
          {agent.description ? (
            <span className="min-w-0 truncate text-caption text-muted-foreground">
              {agent.description}
            </span>
          ) : null}
        </div>
      </div>
    </ListGridCell>
  );
}

// Availability dot + label, with the workload folded in as a suffix
// ("Online · 2 tasks") — a 0-2 integer doesn't earn its own column.
function StatusCell({ row }: { row: AgentListRow }) {
  const { t } = useT("agents");
  const { agent, presence } = row;
  if (agent.archived_at) {
    return (
      <ListGridCell>
        <span className="text-caption text-muted-foreground">
          {t(($) => $.row.archived)}
        </span>
      </ListGridCell>
    );
  }
  if (!isAgentRuntimeBound(agent)) {
    return (
      <ListGridCell className="gap-1.5">
        <AlertCircle className="size-3.5 shrink-0 text-amber-500" />
        <span className="truncate text-caption text-amber-600 dark:text-amber-400">
          {t(($) => $.row.needs_runtime)}
        </span>
      </ListGridCell>
    );
  }
  if (!presence) {
    return (
      <ListGridCell>
        <span className="text-caption text-faint-foreground">—</span>
      </ListGridCell>
    );
  }
  const visual = availabilityConfig[presence.availability];
  const active = presence.runningCount + presence.queuedCount;
  return (
    <ListGridCell className="gap-1.5">
      <span className={`size-1.5 shrink-0 rounded-full ${visual.dotClass}`} />
      <span className={`truncate text-caption ${visual.textClass}`}>
        {t(($) => $.availability[presence.availability])}
        {active > 0 && (
          <span className="text-muted-foreground">
            {" · "}
            {t(($) => $.row.task_count, { count: active })}
          </span>
        )}
      </span>
    </ListGridCell>
  );
}

// Owner = the agent's owner_id, which is set to the creator at creation and
// never transferred (so owner ≡ creator). It carries management rights, so
// the column is "Owner", not "Created by".
function OwnerCell({ row }: { row: AgentListRow }) {
  const { agent, owner } = row;
  if (!agent.owner_id) {
    return (
      <ListGridCell className="hidden @2xl:flex">
        <span className="text-caption text-faint-foreground">—</span>
      </ListGridCell>
    );
  }
  return (
    <ListGridCell className="hidden gap-1.5 @2xl:flex">
      <ActorAvatar actorType="member" actorId={agent.owner_id} size="sm" />
      <span className="min-w-0 truncate text-caption text-muted-foreground">
        {owner?.name ?? agent.owner_id.slice(0, 8)}
      </span>
    </ListGridCell>
  );
}

// Effective access scope derived from permission_mode + invocation_targets
// (not the lossy derived `visibility`). Text label, not icon-only, so screen
// readers announce the scope.
export function AccessCell({ row }: { row: AgentListRow }) {
  const { t } = useT("agents");
  const scope = useMemo(
    () =>
      effectiveAccessScope(
        row.agent.permission_mode,
        row.agent.invocation_targets,
      ),
    [row.agent.permission_mode, row.agent.invocation_targets],
  );
  const label = t(($) =>
    scope === "workspace"
      ? $.access.scope_labels.workspace
      : scope === "specific-people"
        ? $.access.scope_labels.specific_people
        : $.access.scope_labels.owner_only,
  );
  return (
    <ListGridCell className="hidden @2xl:flex">
      <span className="min-w-0 truncate text-caption text-muted-foreground">
        {label}
      </span>
    </ListGridCell>
  );
}

function RuntimeCell({ row }: { row: AgentListRow }) {
  const { t } = useT("agents");
  if (!isAgentRuntimeBound(row.agent)) {
    return (
      <ListGridCell className="hidden @2xl:flex">
        <span className="truncate text-caption text-amber-600 dark:text-amber-400">
          {t(($) => $.row.needs_runtime)}
        </span>
      </ListGridCell>
    );
  }
  const runtime = row.runtime;
  // DENE-506: which runtime a row shows is only half the answer for a
  // specialisation — the other half is whether that value is its own choice or
  // the base role's, which is what tells the reader a later change there will
  // move this row too. The tag is omitted entirely when the backend does not
  // serve `runtime_inherited` (`unknown`), never guessed.
  const inheritance = runtimeInheritanceState(row.agent);
  const inheritanceTag =
    inheritance === "inherited"
      ? {
          label: t(($) => $.specialization.runtime_inherited_tag),
          hint: t(($) => $.specialization.runtime_inherited_tag_hint),
        }
      : inheritance === "independent"
        ? {
            label: t(($) => $.specialization.runtime_independent_tag),
            hint: t(($) => $.specialization.runtime_independent_tag_hint),
          }
        : null;
  return (
    <ListGridCell className="hidden @2xl:flex">
      <span className="inline-flex min-w-0 items-center gap-1.5">
        {runtime ? (
          // Provider mark before the label: scanning this column for "which of
          // these run on Codex" is a shape match, not a read.
          <>
            <ProviderLogo
              provider={runtime.provider}
              className="h-3.5 w-3.5 shrink-0"
            />
            <span className="min-w-0 truncate text-caption text-muted-foreground">
              {runtimeDisplayLabel(runtime)}
            </span>
          </>
        ) : (
          <span className="text-caption text-faint-foreground">—</span>
        )}
        {inheritanceTag ? (
          <span
            data-testid="agents-runtime-inheritance"
            data-inheritance={inheritance}
            title={inheritanceTag.hint}
            className="shrink-0 rounded-xs border border-border px-1 text-micro text-muted-foreground"
          >
            {inheritanceTag.label}
          </span>
        ) : null}
      </span>
    </ListGridCell>
  );
}

function LastActiveCell({ row }: { row: AgentListRow }) {
  const { t } = useT("agents");
  const days = row.lastActiveDays;
  return (
    <ListGridCell className="hidden @2xl:flex">
      {days === null ? (
        <span className="truncate text-caption text-muted-foreground">
          {row.agent.archived_at ? "—" : t(($) => $.last_active.none)}
        </span>
      ) : (
        <span className="whitespace-nowrap text-caption tabular-nums text-muted-foreground">
          {days === 0
            ? t(($) => $.last_active.today)
            : t(($) => $.last_active.days_ago, { count: days })}
        </span>
      )}
    </ListGridCell>
  );
}

// ---------------------------------------------------------------------------
// Header row + skeleton
// ---------------------------------------------------------------------------

function AgentListHeader({
  sortField,
  sortDirection,
  onSort,
  allSelected,
  someSelected,
  onToggleAll,
  isColVisible,
}: {
  sortField: AgentSortField;
  sortDirection: ListGridSortDirection;
  onSort: (field: AgentSortField) => void;
  allSelected: boolean;
  someSelected: boolean;
  onToggleAll: () => void;
  isColVisible: (key: AgentColumnKey) => boolean;
}) {
  const { t } = useT("agents");
  const sorted = (field: AgentSortField) =>
    sortField === field ? sortDirection : false;
  const anySelected = allSelected || someSelected;
  return (
    <ListGridHeader>
      <div className="hidden items-center justify-center @2xl:flex">
        <button
          type="button"
          aria-pressed={allSelected}
          onClick={onToggleAll}
          className={`-m-1.5 flex items-center p-1.5 ${
            anySelected
              ? ""
              : "opacity-0 transition-opacity group-hover/header:opacity-100"
          }`}
        >
          <Checkbox
            checked={allSelected}
            indeterminate={someSelected && !allSelected}
            tabIndex={-1}
            className="pointer-events-none"
          />
        </button>
      </div>
      <ListGridHeaderCell sorted={sorted("name")} onSort={() => onSort("name")}>
        {t(($) => $.columns.agent)}
      </ListGridHeaderCell>
      {isColVisible("status") ? (
        <ListGridHeaderCell>{t(($) => $.columns.status)}</ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="px-0" />
      )}
      {isColVisible("owner") ? (
        <ListGridHeaderCell className="hidden @2xl:flex">
          {t(($) => $.columns.owner)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
      )}
      {isColVisible("access") ? (
        <ListGridHeaderCell className="hidden @2xl:flex">
          {t(($) => $.columns.access)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
      )}
      {isColVisible("runtime") ? (
        <ListGridHeaderCell className="hidden @2xl:flex">
          {t(($) => $.columns.runtime)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
      )}
      {isColVisible("lastActive") ? (
        <ListGridHeaderCell
          className="hidden @2xl:flex"
          sorted={sorted("lastActive")}
          onSort={() => onSort("lastActive")}
        >
          {t(($) => $.columns.last_active)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
      )}
      {isColVisible("runs") ? (
        <ListGridHeaderCell
          className="hidden @2xl:flex"
          align="right"
          sorted={sorted("runs")}
          onSort={() => onSort("runs")}
        >
          {t(($) => $.columns.runs)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
      )}
      {isColVisible("model") ? (
        <ListGridHeaderCell className="hidden @2xl:flex">
          {t(($) => $.columns.model)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
      )}
      {isColVisible("created") ? (
        <ListGridHeaderCell
          className="hidden @2xl:flex"
          sorted={sorted("created")}
          onSort={() => onSort("created")}
        >
          {t(($) => $.columns.created)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
      )}
      <span aria-hidden="true" />
    </ListGridHeader>
  );
}

function LoadingSkeleton() {
  return (
    <ListGrid
      className={GRID_COLS}
      style={columnTrackVars(
        (key) => !AGENT_DEFAULT_HIDDEN_COLUMNS.includes(key),
      )}
    >
      <ListGridHeader>
        <span aria-hidden="true" className="hidden @2xl:inline" />
        <ListGridHeaderCell>
          <Skeleton className="h-3 w-12" />
        </ListGridHeaderCell>
        <ListGridHeaderCell>
          <Skeleton className="h-3 w-12" />
        </ListGridHeaderCell>
        <ListGridHeaderCell className="hidden @2xl:flex">
          <Skeleton className="h-3 w-14" />
        </ListGridHeaderCell>
        <ListGridHeaderCell className="hidden @2xl:flex">
          <Skeleton className="h-3 w-14" />
        </ListGridHeaderCell>
        <ListGridHeaderCell className="hidden @2xl:flex">
          <Skeleton className="h-3 w-14" />
        </ListGridHeaderCell>
        <ListGridHeaderCell className="hidden @2xl:flex">
          <Skeleton className="h-3 w-14" />
        </ListGridHeaderCell>
        <ListGridHeaderCell className="hidden @2xl:flex">
          <Skeleton className="h-3 w-10" />
        </ListGridHeaderCell>
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
        <span aria-hidden="true" />
      </ListGridHeader>
      {Array.from({ length: 5 }).map((_, i) => (
        <ListGridRow key={i} className="h-16 hover:bg-transparent">
          <span aria-hidden="true" className="hidden @2xl:inline" />
          <ListGridCell className="gap-3">
            <Skeleton className="size-8 rounded-full" />
            <div className="min-w-0 flex-1 space-y-1.5">
              <Skeleton className="h-3.5 w-32 max-w-full" />
              <Skeleton className="h-3 w-48 max-w-full" />
            </div>
          </ListGridCell>
          <ListGridCell>
            <Skeleton className="h-3 w-16" />
          </ListGridCell>
          <ListGridCell className="hidden gap-1.5 @2xl:flex">
            <Skeleton className="size-5 rounded-full" />
            <Skeleton className="h-3 w-12" />
          </ListGridCell>
          <ListGridCell className="hidden @2xl:flex">
            <Skeleton className="h-3 w-14" />
          </ListGridCell>
          <ListGridCell className="hidden @2xl:flex">
            <Skeleton className="h-3 w-16" />
          </ListGridCell>
          <ListGridCell className="hidden @2xl:flex">
            <Skeleton className="h-3 w-12" />
          </ListGridCell>
          <ListGridCell className="hidden justify-end @2xl:flex">
            <Skeleton className="h-3 w-8" />
          </ListGridCell>
          <ListGridCell className="hidden px-0 @2xl:flex" />
          <ListGridCell className="hidden px-0 @2xl:flex" />
          <span aria-hidden="true" />
        </ListGridRow>
      ))}
    </ListGrid>
  );
}

// ---------------------------------------------------------------------------
// Batch toolbar — archive (with confirm; archiving cancels active tasks) and
// Page
// ---------------------------------------------------------------------------

export function AgentsPage(_props: AgentsPageProps = {}) {
  const { t } = useT("agents");
  const locale = useLocale();
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const rowLink = useRowLink();
  const currentUser = useAuthStore((s) => s.user);

  const {
    data: agents = [],
    isLoading,
    error: listError,
    refetch: refetchList,
  } = useQuery(agentListOptions(wsId));
  const { data: runtimes = [] } = useQuery(
    runtimeListOptions(wsId),
  );
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: squads = [], isPending: squadsPending } = useQuery(
    squadListOptions(wsId),
  );
  const squadsNeedingRoster = useMemo(
    () => squads.filter(needsSquadMembersFetch),
    [squads],
  );
  const rosterQueryResults = useQueries({
    queries: squadsNeedingRoster.map((squad) =>
      squadMembersOptions(wsId, squad.id),
    ),
  });
  const fetchedMembers = useMemo(() => {
    const map = new Map<string, FetchedSquadMembers>();
    for (let i = 0; i < squadsNeedingRoster.length; i++) {
      const squad = squadsNeedingRoster[i];
      const result = rosterQueryResults[i];
      if (!squad) continue;
      map.set(squad.id, {
        status: result?.isError
          ? "error"
          : result?.isSuccess
            ? "success"
            : "pending",
        members: result?.data,
      });
    }
    return map;
  }, [squadsNeedingRoster, rosterQueryResults]);
  const squadRosters = useMemo(
    () => resolveSquadRosters(squads, fetchedMembers),
    [squads, fetchedMembers],
  );
  const rosterPending = rosterQueryResults.some((query) => query.isPending);
  const { data: runCountsRaw = [], isPending: runCountsPending } = useQuery(
    agentRunCounts30dOptions(wsId),
  );
  const { byAgent: presenceMap, loading: presenceLoading } =
    useWorkspacePresenceMap(wsId);
  const { byAgent: activityMap, loading: activityLoading } =
    useWorkspaceActivityMap(wsId);

  const [selectedIds, setSelectedIds] = useState<ReadonlySet<string>>(
    new Set(),
  );
  const [search, setSearch] = useState("");
  // Which base-role groups are folded in the nested view. Session-scoped like
  // row selection: folding is a momentary "hide this for now", not a view
  // preference worth persisting (the grouping mode itself is persisted).
  const [collapsedGroups, setCollapsedGroups] = useState<ReadonlySet<string>>(
    new Set(),
  );
  const toggleGroupExpanded = useCallback((groupId: string) => {
    setCollapsedGroups((prev) => {
      const next = new Set(prev);
      if (next.has(groupId)) next.delete(groupId);
      else next.add(groupId);
      return next;
    });
  }, []);

  const rawScope = useAgentsViewStore((s) => s.scope);
  const scope = AGENT_SCOPES.includes(rawScope) ? rawScope : "mine";
  const setScope = useAgentsViewStore((s) => s.setScope);
  const sortField = useAgentsViewStore((s) => s.sortField);
  const sortDirection = useAgentsViewStore((s) => s.sortDirection);
  const hiddenColumns = useAgentsViewStore((s) => s.hiddenColumns);
  const filters = useAgentsViewStore((s) => s.filters);
  const rawGrouping = useAgentsViewStore((s) => s.grouping);
  // A persisted value can predate (or postdate) this build's modes; fall back
  // to the flat list rather than rendering a grouping nothing implements.
  const grouping = AGENT_GROUPINGS.includes(rawGrouping) ? rawGrouping : "none";
  const setGrouping = useAgentsViewStore((s) => s.setGrouping);
  const handleSort = useAgentsViewStore((s) => s.toggleSort);
  const handleSortFieldSelect = useAgentsViewStore((s) => s.setSortField);
  const setSortDirection = useAgentsViewStore((s) => s.setSortDirection);
  const toggleColumn = useAgentsViewStore((s) => s.toggleColumn);
  const toggleFilter = useAgentsViewStore((s) => s.toggleFilter);
  const clearFilters = useAgentsViewStore((s) => s.clearFilters);

  const isColVisible = (key: AgentColumnKey) => !hiddenColumns.includes(key);

  const toggleSelected = (id: string) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const runtimesById = useMemo(() => {
    const m = new Map<string, AgentRuntime>();
    for (const r of runtimes) m.set(r.id, r);
    return m;
  }, [runtimes]);

  const runCountsById = useMemo(() => {
    const m = new Map<string, number>();
    for (const r of runCountsRaw) m.set(r.agent_id, r.run_count);
    return m;
  }, [runCountsRaw]);

  const membersById = useMemo(() => {
    const m = new Map<string, MemberWithUser>();
    for (const mem of members) m.set(mem.user_id, mem);
    return m;
  }, [members]);

  const agentSquadsMap = useMemo(
    () => buildAgentSquadsMap(squads, squadRosters),
    [squads, squadRosters],
  );
  // Active specialisations per base role, straight off the list payload (every
  // row carries parent_agent_id). Feeds the archive guard's "solidify &
  // unbind" action, which needs the child ids the 409 body does not carry.
  const childAgentsByParentId = useMemo(() => {
    const map = new Map<string, Agent[]>();
    for (const agent of agents) {
      const parentId = agent.parent_agent_id ?? "";
      if (!parentId || agent.archived_at) continue;
      const existing = map.get(parentId);
      if (existing) existing.push(agent);
      else map.set(parentId, [agent]);
    }
    return map;
  }, [agents]);
  const allActiveRostersKnown = useMemo(() => {
    for (const squad of squads) {
      if (squad.archived_at != null) continue;
      if (squadRosters.get(squad.id)?.kind !== "known") return false;
    }
    return true;
  }, [squads, squadRosters]);

  const isWorkspaceAdmin = useMemo(() => {
    if (!currentUser) return false;
    const me = members.find((m) => m.user_id === currentUser.id);
    return me?.role === "owner" || me?.role === "admin";
  }, [members, currentUser]);

  // Scope counts come from the FULL set (filters never affect them).
  // Archived ignores the ownership lens (see the view store comment).
  const scopeCounts = useMemo<Record<AgentsScope, number>>(() => {
    let mine = 0;
    let all = 0;
    let archived = 0;
    for (const a of agents) {
      if (a.archived_at) {
        archived++;
        continue;
      }
      all++;
      if (currentUser && a.owner_id === currentUser.id) mine++;
    }
    return { mine, all, archived };
  }, [agents, currentUser]);

  // Rows within the current scope, unfiltered, fully assembled — the
  // toolbar's option lists and the "n / total" denominator derive from
  // this; cells never pull their own queries.
  const scopeRows = useMemo<AgentListRow[]>(() => {
    const inScope = agents.filter((a) => {
      if (scope === "archived") return !!a.archived_at;
      if (a.archived_at) return false;
      if (scope === "mine") {
        return !!currentUser && a.owner_id === currentUser.id;
      }
      return true;
    });
    return inScope.map((agent) => {
      const isOwner = !!currentUser?.id && agent.owner_id === currentUser.id;
      const activity = activityMap.get(agent.id) ?? null;
      return {
        agent,
        runtime: runtimesById.get(agent.runtime_id) ?? null,
        presence: presenceMap.get(agent.id) ?? null,
        activity,
        runCount: runCountsById.get(agent.id) ?? 0,
        lastActiveDays: lastActiveDaysAgo(activity),
        owner: agent.owner_id ? membersById.get(agent.owner_id) ?? null : null,
        isOwnedByMe: isOwner,
        canManage: isWorkspaceAdmin || isOwner,
        squadIds: (agentSquadsMap.get(agent.id) ?? []).map((s) => s.id),
        squadMembershipKnown:
          (agentSquadsMap.get(agent.id)?.length ?? 0) > 0 ||
          allActiveRostersKnown,
      };
    });
  }, [
    agents,
    scope,
    currentUser,
    runtimesById,
    membersById,
    presenceMap,
    activityMap,
    runCountsById,
    isWorkspaceAdmin,
    agentSquadsMap,
    allActiveRostersKnown,
  ]);

  // Visible rows: local search + filters, then sort.
  const rows = useMemo<AgentListRow[]>(() => {
    const filtered = scopeRows.filter((row) => rowMatchesFilters(row, filters, search));

    const dir = sortDirection === "asc" ? 1 : -1;
    filtered.sort((a, b) => {
      if (sortField === "name") {
        return a.agent.name.localeCompare(b.agent.name) * dir;
      }
      if (sortField === "runs") {
        return (a.runCount - b.runCount) * dir ||
          a.agent.name.localeCompare(b.agent.name);
      }
      if (sortField === "created") {
        return (
          (Date.parse(a.agent.created_at) - Date.parse(b.agent.created_at)) *
          dir
        );
      }
      // lastActive: smaller daysAgo = more recent. "desc" (the default)
      // means most recently active first; never-active rows sort last in
      // both directions. Run count breaks ties.
      const av = a.lastActiveDays ?? Number.POSITIVE_INFINITY;
      const bv = b.lastActiveDays ?? Number.POSITIVE_INFINITY;
      const byDays = sortDirection === "desc" ? av - bv : bv - av;
      return (
        byDays || b.runCount - a.runCount ||
        a.agent.name.localeCompare(b.agent.name)
      );
    });
    return filtered;
  }, [scopeRows, search, filters, sortField, sortDirection]);

  const listItems = useMemo(() => {
    if (grouping === "specialization") {
      return flattenSpecializationItems(rows, collapsedGroups);
    }
    return flattenAgentListItems(rows, squads, grouping, squadRosters);
  }, [rows, squads, grouping, squadRosters, collapsedGroups]);
  const listItemsRef = useRef(listItems);
  listItemsRef.current = listItems;

  const noMatchText = useMemo(() => {
    const query = search.trim();
    if (query) {
      if (scope === "archived") {
        return t(($) => $.no_matches.search_archived, { query });
      }
      if (countActiveFilterDimensions(filters) > 0) {
        return t(($) => $.no_matches.search_active_filtered, { query });
      }
      return t(($) => $.no_matches.search_active, { query });
    }
    if (scope === "archived") return t(($) => $.no_matches.no_archived);
    if (countActiveFilterDimensions(filters) > 0) {
      return t(($) => $.no_matches.no_filter_match);
    }
    return t(($) => $.no_matches.title);
  }, [filters, scope, search, t]);

  // Row virtualization — headless math, offsets as padding on the rows
  // wrapper, fixed-height rows. The scroll element is the SINGLE outer
  // scroller (both axes): splitting horizontal scrolling (wrapper) from
  // vertical scrolling (an inner element) connected by an h-full
  // percentage bridge caused a non-converging layout loop (flickering
  // double scrollbars) and clipped the last row under the horizontal
  // scrollbar. The sticky header pins inside this scroller; the vertical
  // scrollbar spans the full pane height (Linear's structure).
  const listScrollRef = useRef<HTMLDivElement | null>(null);
  const rowVirtualizer = useVirtualizer({
    count: listItems.length,
    getScrollElement: () => listScrollRef.current,
    estimateSize: (index) => {
      const item = listItemsRef.current[index];
      if (item?.kind === "header" || item?.kind === "derive") {
        return item.kind === "derive"
          ? SPECIALIZATION_DERIVE_ROW_HEIGHT
          : GROUP_HEADER_HEIGHT;
      }
      return ROW_HEIGHT;
    },
    getItemKey: (index) => listItemsRef.current[index]?.key ?? index,
    overscan: 10,
  });

  // Straight to the manual form: a duplicate already has every field decided,
  // so the method chooser would be a step with nothing to choose.
  const duplicateHref = useCallback(
    (agent: Agent) =>
      `${paths.newAgentManual()}?duplicate=${encodeURIComponent(agent.id)}`,
    [paths],
  );

  const selectedRows = rows.filter((row) => selectedIds.has(row.agent.id));
  const allSelected = rows.length > 0 && selectedRows.length === rows.length;
  const someSelected = selectedRows.length > 0 && !allSelected;
  const handleToggleAll = () => {
    setSelectedIds(
      allSelected ? new Set() : new Set(rows.map((r) => r.agent.id)),
    );
  };

  const virtualItems = rowVirtualizer.getVirtualItems();
  const firstVirtual = virtualItems[0];
  const lastVirtual = virtualItems[virtualItems.length - 1];
  const virtualPadding = {
    top: firstVirtual ? firstVirtual.start : 0,
    bottom: lastVirtual
      ? rowVirtualizer.getTotalSize() - lastVirtual.end
      : 0,
  };

  if (listError) {
    return (
      <ListError
        onCreate={() => navigation.push(paths.newAgent())}
        listError={listError}
        onRetry={() => refetchList()}
      />
    );
  }

  const totalCount = agents.filter((a) => !a.archived_at).length;
  const showEmpty = !isLoading && agents.length === 0;

  // The active sort field / availability filter reads columns that arrive in
  // separate queries from the main agent list (activity → lastActiveDays,
  // run-counts → runCount, presence → availability). Rendering real rows
  // before those land would sort on placeholder values (lastActiveDays
  // null→Infinity, runCount 0) and visibly re-order the list once each query
  // resolves. Gate the first real paint on exactly the auxiliary queries the
  // current sort / filter depends on — nothing for name/created, run-counts
  // for runs, activity + run-counts (its tiebreaker) for the default
  // lastActive, plus presence whenever an availability filter is active. The
  // queries still run in parallel, so this defers the first paint by at most
  // one extra round-trip (shown as skeleton) and never serialises them. An
  // empty workspace (showEmpty) skips the gate so the empty state is never
  // blocked on the auxiliary queries.
  const needsRunCounts = sortField === "lastActive" || sortField === "runs";
  const needsActivity = sortField === "lastActive";
  const needsPresence = filters.availability.length > 0;
  const needsSquads = grouping === "squad" || filters.squads.length > 0;
  const listReady =
    (!needsActivity || !activityLoading) &&
    (!needsRunCounts || !runCountsPending) &&
    (!needsPresence || !presenceLoading) &&
    (!needsSquads || (!squadsPending && !rosterPending));

  return (
    // relative: positioning anchor for the batch toolbar (page-centered,
    // not viewport-centered).
    <div className="relative flex flex-1 min-h-0 flex-col">
      <PageHeaderBar
        totalCount={totalCount}
        onCreate={() => navigation.push(paths.newAgent())}
      />

      {isLoading || (!showEmpty && !listReady) ? (
        <div className="flex-1 overflow-y-auto @container">
          <LoadingSkeleton />
        </div>
      ) : showEmpty ? (
        <div className="flex flex-1 items-center justify-center">
          <EmptyState onCreate={() => navigation.push(paths.newAgent())} />
        </div>
      ) : (
        <>
          <AgentListToolbar
            scope={scope}
            onScopeChange={setScope}
            scopeCounts={scopeCounts}
            search={search}
            onSearchChange={setSearch}
            filters={filters}
            onToggleFilter={toggleFilter}
            onClearFilters={clearFilters}
            grouping={grouping}
            onGroupingChange={setGrouping}
            sortField={sortField}
            sortDirection={sortDirection}
            onSortFieldChange={handleSortFieldSelect}
            onSortDirectionChange={setSortDirection}
            hiddenColumns={hiddenColumns}
            onToggleColumn={toggleColumn}
            allRows={scopeRows}
            members={members}
            squads={squads}
            squadRosters={squadRosters}
            visibleCount={rows.length}
          />
          <div
            ref={listScrollRef}
            className="min-h-0 flex-1 overflow-auto @container"
          >
            <ListGrid
              className={`${GRID_COLS} @2xl:min-w-[var(--agc-minw)]`}
              style={columnTrackVars(isColVisible)}
            >
              <AgentListHeader
                sortField={sortField}
                sortDirection={sortDirection}
                onSort={handleSort}
                allSelected={allSelected}
                someSelected={someSelected}
                onToggleAll={handleToggleAll}
                isColVisible={isColVisible}
              />
              <ListGridBody
                style={{
                  paddingTop: virtualPadding.top,
                  paddingBottom:
                    virtualPadding.bottom + LIST_GRID_BOTTOM_CLEARANCE,
                }}
              >
                {rows.length === 0 && (
                  <div className="col-span-full py-16 text-center text-body text-muted-foreground">
                    {noMatchText}
                  </div>
                )}
                {virtualItems.map((vi) => {
                  const item = listItems[vi.index];
                  if (!item) return null;
                  if (item.kind === "header") {
                    return (
                      <SquadGroupHeader
                        key={item.key}
                        group={item.group}
                        href={
                          item.group.squad
                            ? paths.squadDetail(item.group.squad.id)
                            : null
                        }
                      />
                    );
                  }
                  if (item.kind === "derive") {
                    return (
                      <DeriveSpecializationRow
                        key={item.key}
                        baseName={item.base.agent.name}
                        onDerive={() =>
                          navigation.push(
                            `${paths.newAgentManual()}?parent=${encodeURIComponent(
                              item.base.agent.id,
                            )}`,
                          )
                        }
                      />
                    );
                  }
                  const row = item.row;
                  const isChild = item.kind === "specialization";
                  const isSelected = selectedIds.has(row.agent.id);
                  return (
                    <ListGridRow
                      key={item.key}
                      data-specialization-row={isChild ? "true" : undefined}
                      // The row's own hover (`hover:bg-accent/40` on
                      // ListGridRow) used to overwrite the selection tint and
                      // leave a selected row looking exactly like any hovered
                      // one (DENE-384). Selection is asserted as `data-active`
                      // and re-stated on hover in the compound variant, which
                      // out-specifies the shared hover rule, so a selected row
                      // stays selected-looking while the pointer is on it.
                      data-active={isSelected ? "true" : undefined}
                      className={`h-16 cursor-pointer ${
                        isSelected
                          ? "bg-surface-selected data-active:hover:bg-surface-selected"
                          : ""
                      }`}
                      {...rowLink(paths.agentDetail(row.agent.id), row.agent.name)}
                    >
                      <CheckboxCell
                        checked={isSelected}
                        onToggle={() => toggleSelected(row.agent.id)}
                      />
                      <NameCell
                        row={row}
                        nested={isChild}
                        nesting={
                          // A specialisation whose base role was filtered away
                          // is emitted as its own group head, but it can never
                          // hold children — giving it the fold control would
                          // render a chevron that toggles nothing. The same
                          // goes for a base role with zero specialisations
                          // (DENE-384): nothing to fold, so nothing to show.
                          item.kind === "base" &&
                          !isSpecialization(row.agent) &&
                          item.hasChildren
                            ? {
                                childCount: item.childCount,
                                expanded: item.expanded,
                                onToggle: () => toggleGroupExpanded(item.groupId),
                              }
                            : null
                        }
                      />
                      {isColVisible("status") ? (
                        <StatusCell row={row} />
                      ) : (
                        <ListGridCell className="px-0" />
                      )}
                      {isColVisible("owner") ? (
                        <OwnerCell row={row} />
                      ) : (
                        <ListGridCell className="hidden px-0 @2xl:flex" />
                      )}
                      {isColVisible("access") ? (
                        <AccessCell row={row} />
                      ) : (
                        <ListGridCell className="hidden px-0 @2xl:flex" />
                      )}
                      {isColVisible("runtime") ? (
                        <RuntimeCell row={row} />
                      ) : (
                        <ListGridCell className="hidden px-0 @2xl:flex" />
                      )}
                      {isColVisible("lastActive") ? (
                        <LastActiveCell row={row} />
                      ) : (
                        <ListGridCell className="hidden px-0 @2xl:flex" />
                      )}
                      {isColVisible("runs") ? (
                        <ListGridCell className="hidden justify-end font-mono text-caption tabular-nums text-muted-foreground @2xl:flex">
                          {row.runCount.toLocaleString(locale)}
                        </ListGridCell>
                      ) : (
                        <ListGridCell className="hidden px-0 @2xl:flex" />
                      )}
                      {isColVisible("model") ? (
                        <ListGridCell className="hidden @2xl:flex">
                          <span className="min-w-0 truncate text-caption text-muted-foreground">
                            {row.agent.model
                              ? providerSeatModelDisplay(row.agent.model)
                              : "—"}
                          </span>
                        </ListGridCell>
                      ) : (
                        <ListGridCell className="hidden px-0 @2xl:flex" />
                      )}
                      {isColVisible("created") ? (
                        <ListGridCell className="hidden whitespace-nowrap text-caption tabular-nums text-muted-foreground @2xl:flex">
                          {new Date(
                            row.agent.created_at,
                          ).toLocaleDateString(locale)}
                        </ListGridCell>
                      ) : (
                        <ListGridCell className="hidden px-0 @2xl:flex" />
                      )}
                      <ListGridCell className="justify-end px-0">
                        <span
                          onClick={(e) => e.stopPropagation()}
                          className="flex items-center gap-1"
                        >
                          <AgentRowActions
                            agent={row.agent}
                            presence={row.presence}
                            canManage={row.canManage}
                            duplicateHref={duplicateHref(row.agent)}
                            childAgents={childAgentsByParentId.get(row.agent.id)}
                          />
                        </span>
                      </ListGridCell>
                    </ListGridRow>
                  );
                })}
              </ListGridBody>
            </ListGrid>
          </div>
        </>
      )}

      <AgentBatchToolbar
        rows={selectedRows}
        members={members}
        currentUserId={currentUser?.id ?? null}
        onClear={() => setSelectedIds(new Set())}
      />

    </div>
  );
}
