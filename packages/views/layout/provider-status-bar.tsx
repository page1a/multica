"use client";

import { useQuery } from "@tanstack/react-query";
import { ChevronDown } from "lucide-react";
import { useCurrentWorkspace } from "@multica/core/paths";
import {
  providerDisplayName,
  runtimeDisplayName,
  useProviderQuotaSourceStore,
} from "@multica/core/runtimes";
import { runtimeListOptions } from "@multica/core/runtimes/queries";
import type {
  AgentRuntime,
  PlanLimitWindow,
  PlanLimitsSnapshot,
} from "@multica/core/types";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { ProviderLogo } from "../runtimes/components/provider-logo";
import {
  displayPlanLimits,
  formatPlanLimitRemaining,
  planLimitWindowShortLabel,
} from "../runtimes/components/plan-limits";
import { runtimeDeviceName } from "../runtimes/components/runtime-machines";
import { formatDeviceInfo } from "../runtimes/utils";
import { useLocale, useT } from "../i18n";

export interface ProviderQuotaRuntime {
  runtimeId: string;
  sourceLabel: string;
  deviceLabel: string | null;
  snapshot: PlanLimitsSnapshot;
  windows: PlanLimitWindow[];
}

export interface ProviderQuotaSummary {
  provider: string;
  runtimes: ProviderQuotaRuntime[];
}

/** Compact identity for a quota snapshot: alias, then hostname, then device. */
export function quotaSourceLabel(
  runtime: Pick<AgentRuntime, "name" | "custom_name" | "device_info">,
): string {
  if (runtime.custom_name?.trim()) return runtimeDisplayName(runtime);
  return runtimeDeviceName(runtime) ?? "—";
}

export function resolveSelectedRuntime<T extends { runtimeId: string }>(
  runtimes: readonly T[],
  selectedId: string | undefined,
): T | undefined {
  if (runtimes.length === 0) return undefined;
  if (selectedId) {
    const match = runtimes.find((runtime) => runtime.runtimeId === selectedId);
    if (match) return match;
  }
  return runtimes[0];
}

/** Group runtimes by provider, keeping every current snapshot. Newest first. */
export function collectProviderQuotas(
  runtimes: readonly AgentRuntime[],
  nowMs = Date.now(),
): ProviderQuotaSummary[] {
  const byProvider = new Map<string, ProviderQuotaRuntime[]>();

  for (const runtime of runtimes) {
    const display = displayPlanLimits(runtime.plan_limits, nowMs);
    if (!display) continue;

    const provider = runtime.provider.trim().toLowerCase();
    const list = byProvider.get(provider) ?? [];
    const sourceLabel = quotaSourceLabel(runtime);
    const deviceLabel = formatDeviceInfo(runtime.device_info ?? null);
    list.push({
      runtimeId: runtime.id,
      sourceLabel,
      deviceLabel:
        deviceLabel && deviceLabel !== sourceLabel ? deviceLabel : null,
      snapshot: display.snapshot,
      windows: display.windows,
    });
    byProvider.set(provider, list);
  }

  return [...byProvider.entries()]
    .map(([provider, items]) => ({
      provider,
      runtimes: items.sort((a, b) => {
        if (b.snapshot.observed_at !== a.snapshot.observed_at) {
          return b.snapshot.observed_at - a.snapshot.observed_at;
        }
        return a.sourceLabel.localeCompare(b.sourceLabel);
      }),
    }))
    .sort((a, b) =>
      providerDisplayName(a.provider).localeCompare(
        providerDisplayName(b.provider),
      ),
    );
}

export function ProviderStatusBar() {
  const workspace = useCurrentWorkspace();
  const { data: runtimes = [] } = useQuery({
    ...runtimeListOptions(workspace?.id ?? ""),
    enabled: Boolean(workspace),
  });
  const selectedByProvider = useProviderQuotaSourceStore(
    (state) => state.selectedByProvider,
  );
  const setSelectedRuntime = useProviderQuotaSourceStore(
    (state) => state.setSelectedRuntime,
  );

  return (
    <ProviderStatusBarView
      providers={collectProviderQuotas(runtimes)}
      selectedByProvider={selectedByProvider}
      onSelectRuntime={setSelectedRuntime}
    />
  );
}

export function ProviderStatusBarView({
  providers,
  selectedByProvider = {},
  onSelectRuntime,
}: {
  providers: ProviderQuotaSummary[];
  selectedByProvider?: Record<string, string>;
  onSelectRuntime?: (provider: string, runtimeId: string) => void;
}) {
  const { t } = useT("runtimes");
  if (providers.length === 0) return null;

  return (
    <footer
      aria-label={t(($) => $.plan_limits.title)}
      className="flex h-10 shrink-0 items-center gap-4 overflow-x-auto border-t px-3 pe-chat-launcher"
    >
      {providers.map((provider) => (
        <ProviderStatusEntry
          key={provider.provider}
          provider={provider}
          selectedId={selectedByProvider[provider.provider]}
          onSelectRuntime={onSelectRuntime}
        />
      ))}
    </footer>
  );
}

function ProviderStatusEntry({
  provider,
  selectedId,
  onSelectRuntime,
}: {
  provider: ProviderQuotaSummary;
  selectedId: string | undefined;
  onSelectRuntime?: (provider: string, runtimeId: string) => void;
}) {
  const { t } = useT("runtimes");
  const locale = useLocale();
  const selected = resolveSelectedRuntime(provider.runtimes, selectedId);
  if (!selected) return null;

  const name = providerDisplayName(provider.provider);
  const hasMultiple = provider.runtimes.length > 1;
  const switchLabel = t(($) => $.plan_limits.switch_source, { provider: name });

  const summary = (
    <ProviderStatusSummary
      provider={provider.provider}
      name={name}
      selected={selected}
      hasMultiple={hasMultiple}
    />
  );

  if (!hasMultiple) {
    return (
      <Tooltip>
        <TooltipTrigger render={<div className="flex shrink-0 items-center gap-2 text-caption">{summary}</div>} />
        <TooltipContent side="top">
          <QuotaDetails
            name={name}
            selected={selected}
            locale={locale}
          />
        </TooltipContent>
      </Tooltip>
    );
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <button
            type="button"
            aria-label={switchLabel}
            className="flex h-8 shrink-0 items-center gap-2 rounded-md px-1.5 text-caption hover:bg-muted hover:text-foreground aria-expanded:bg-muted aria-expanded:text-foreground"
          >
            {summary}
          </button>
        }
      />
      <DropdownMenuContent align="start" side="top" className="min-w-48">
        <DropdownMenuRadioGroup
          value={selected.runtimeId}
          onValueChange={(runtimeId) =>
            onSelectRuntime?.(provider.provider, runtimeId)
          }
        >
          {provider.runtimes.map((runtime) => (
            <DropdownMenuRadioItem
              key={runtime.runtimeId}
              value={runtime.runtimeId}
              className="items-start py-1.5"
            >
              <span className="flex min-w-0 flex-1 flex-col">
                <span className="truncate font-medium">{runtime.sourceLabel}</span>
                {runtime.deviceLabel ? (
                  <span className="truncate text-caption text-muted-foreground">
                    {runtime.deviceLabel}
                  </span>
                ) : null}
              </span>
              <span className="ps-3 text-caption tabular-nums text-muted-foreground">
                {compactRemaining(runtime.windows, t)}
              </span>
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function ProviderStatusSummary({
  provider,
  name,
  selected,
  hasMultiple,
}: {
  provider: string;
  name: string;
  selected: ProviderQuotaRuntime;
  hasMultiple: boolean;
}) {
  const { t } = useT("runtimes");
  const locale = useLocale();

  return (
    <>
      <ProviderLogo provider={provider} className="h-3.5 w-3.5" />
      <span className="font-medium">{name}</span>
      {selected.windows.length === 0 ? (
        <span className="font-medium text-destructive">
          {t(($) => $.plan_limits.limit_reached)}
        </span>
      ) : (
        selected.windows.map((window) => (
          <WindowSummary key={window.name} window={window} locale={locale} />
        ))
      )}
      <span className="max-w-28 truncate text-muted-foreground">
        {selected.sourceLabel}
      </span>
      {hasMultiple ? (
        <ChevronDown className="size-3 text-muted-foreground" />
      ) : null}
    </>
  );
}

function QuotaDetails({
  name,
  selected,
  locale,
}: {
  name: string;
  selected: ProviderQuotaRuntime;
  locale: string;
}) {
  const { t } = useT("runtimes");

  return (
    <div className="space-y-1">
      <div className="font-medium">{name}</div>
      <div className="text-muted-foreground">{selected.sourceLabel}</div>
      {selected.windows.length === 0 ? (
        <div>{t(($) => $.plan_limits.limit_reached)}</div>
      ) : (
        selected.windows.map((window) => (
          <WindowDetail key={window.name} window={window} locale={locale} />
        ))
      )}
    </div>
  );
}

function WindowSummary({
  window,
  locale,
}: {
  window: PlanLimitWindow;
  locale: string;
}) {
  const remaining = remainingLabel(window);
  return (
    <span className={remainingTone(window)}>
      {planLimitWindowShortLabel(window)} {remaining ?? "—"}
      {window.resets_at ? ` · ${relativeReset(window.resets_at, locale)}` : ""}
    </span>
  );
}

function WindowDetail({
  window,
  locale,
}: {
  window: PlanLimitWindow;
  locale: string;
}) {
  const { t } = useT("runtimes");
  const remaining = remainingLabel(window);
  const reset = window.resets_at
    ? relativeReset(window.resets_at, locale)
    : null;

  return (
    <div>
      {planLimitWindowShortLabel(window)}: {remaining ?? t(($) => $.plan_limits.limit_reached)}
      {reset ? ` · ${t(($) => $.plan_limits.resets, { when: reset })}` : ""}
    </div>
  );
}

function compactRemaining(
  windows: PlanLimitWindow[],
  t: ReturnType<typeof useT<"runtimes">>["t"],
): string {
  if (windows.length === 0) return t(($) => $.plan_limits.limit_reached);
  return windows
    .map((window) => remainingLabel(window) ?? "—")
    .join(" · ");
}

function remainingLabel(window: PlanLimitWindow): string | null {
  const prepaid = formatPlanLimitRemaining(window);
  if (prepaid) return prepaid;
  if (window.used_percent == null) return null;
  return `${Math.max(0, Math.round(100 - window.used_percent))}%`;
}

function remainingTone(window: PlanLimitWindow): string {
  if (window.used_percent == null) return "text-muted-foreground";
  const remaining = 100 - window.used_percent;
  if (remaining <= 0) return "text-destructive";
  if (remaining <= 20) return "text-warning";
  return "text-foreground";
}

function relativeReset(timestamp: number, locale: string): string {
  const deltaMs = timestamp * 1000 - Date.now();
  const absoluteMinutes = Math.max(1, Math.round(Math.abs(deltaMs) / 60_000));
  if (absoluteMinutes < 60) {
    return new Intl.RelativeTimeFormat(locale, { numeric: "auto" }).format(
      Math.sign(deltaMs) * absoluteMinutes,
      "minute",
    );
  }
  const absoluteHours = Math.max(1, Math.round(absoluteMinutes / 60));
  if (absoluteHours < 24) {
    return new Intl.RelativeTimeFormat(locale, { numeric: "auto" }).format(
      Math.sign(deltaMs) * absoluteHours,
      "hour",
    );
  }
  const absoluteDays = Math.max(1, Math.round(absoluteHours / 24));
  return new Intl.RelativeTimeFormat(locale, { numeric: "auto" }).format(
    Math.sign(deltaMs) * absoluteDays,
    "day",
  );
}
