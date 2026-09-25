"use client";

import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronDown, Cpu, Loader2, Plus } from "lucide-react";
import {
  refreshRuntimeModels,
  runtimeModelsOptions,
} from "@multica/core/runtimes";
import { Label } from "@multica/ui/components/ui/label";
import {
  PickerItem,
  PropertyPicker,
} from "../../../issues/components/pickers";
import { CHIP_CLASS } from "./chip";
import { useT } from "../../../i18n";
import { UnavailableModelsNote } from "../unavailable-models-note";
import { ModelSearchHeader } from "../model-search-header";
import {
  providerSeatModelDisplay,
  seatModelIsExactly,
} from "../provider-seat-model";

/**
 * Inline model picker for the agent inspector. Lighter cousin of
 * `ModelDropdown` (which is used in the create-agent dialog) — same data
 * source via `runtimeModelsOptions`, but renders inside a PropertyPicker so
 * it fits a single PropRow. Drops the "select a runtime first" state because
 * the inspector only renders this picker after a runtime is bound.
 *
 * Providers whose runtime ignores per-agent model selection report
 * `supported=false` and render an inert italic "Managed by runtime" label
 * instead of a clickable picker. No built-in provider sets this today
 * (Antigravity gained `--model` in agy 1.0.6), but the branch stays for any
 * future model-less runtime.
 *
 * Every model string this component shows goes through
 * `providerSeatModelDisplay`. The catalog id is a seat model string
 * (`preset/encodeURIComponent(modelId)`), and a DSH gateway's ids carry
 * slashes — rendering one verbatim would put `%2F` on screen (DENE-684).
 */
export function ModelPicker({
  runtimeId,
  runtimeOnline,
  agentId,
  value,
  canEdit = true,
  variant = "chip",
  showLabel = true,
  onChange,
}: {
  runtimeId: string | null;
  runtimeOnline: boolean;
  /** Saved agent whose custom_env should win over the machine config. */
  agentId?: string | null;
  value: string;
  /** When false, render a static read-only display and skip the popover. */
  canEdit?: boolean;
  variant?: "chip" | "field";
  showLabel?: boolean;
  onChange: (next: string) => Promise<void> | void;
}) {
  const { t } = useT("agents");
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");

  const modelsQuery = useQuery(
    runtimeModelsOptions(runtimeOnline ? runtimeId : null, agentId),
  );
  const supported = modelsQuery.data?.supported ?? true;
  // Memoise the model list so every downstream useMemo gets a stable
  // reference; `?? []` would mint a fresh array on every render and
  // invalidate filters needlessly.
  // Advisory only — never merged into `models`, so no row below can select one.
  const unavailableModels = useMemo(
    () => modelsQuery.data?.unavailableModels ?? [],
    [modelsQuery.data],
  );
  const models = useMemo(
    () => modelsQuery.data?.models ?? [],
    [modelsQuery.data],
  );

  const filtered = useMemo(() => {
    const s = search.trim().toLowerCase();
    if (!s) return models;
    return models.filter(
      (m) =>
        m.id.toLowerCase().includes(s) ||
        m.label.toLowerCase().includes(s) ||
        // A DSH catalog id is percent-encoded (`preset/deepseek%2Fv4-flash`),
        // but the string a user remembers and types to find it is the decoded
        // one. Matching the stored id alone leaves the row reachable by every
        // spelling except the one a person knows (DENE-684).
        providerSeatModelDisplay(m.id).toLowerCase().includes(s),
    );
  }, [models, search]);

  const trimmedSearch = search.trim();
  const exactMatch = models.some(
    (m) => seatModelIsExactly(m.id, trimmedSearch) || m.label === trimmedSearch,
  );
  const canCreate = trimmedSearch.length > 0 && !exactMatch;

  // The stored value is the seat model string (`preset/encodeURIComponent(id)`),
  // which must never reach the screen: a user who reads `%2F` on the chip is
  // being shown a string they cannot find in any picker (DENE-684).
  const triggerLabel = value
    ? providerSeatModelDisplay(value)
    : t(($) => $.pickers.model_default);
  const triggerTitle = t(($) => $.pickers.model_tooltip, { value: triggerLabel });

  const select = async (id: string) => {
    setOpen(false);
    setSearch("");
    if (id !== value) await onChange(id);
  };

  const refresh = () => {
    if (!runtimeId || !runtimeOnline) return;
    void refreshRuntimeModels(
      queryClient,
      runtimeId,
      ...(agentId ? [agentId] : []),
    ).catch(() => {
      // React Query retains the last catalog and owns the error state. Avoid
      // turning a failed button action into an unhandled promise rejection.
    });
  };

  if (!supported && !modelsQuery.isLoading) {
    if (variant === "field") {
      const control = (
        <div className="flex min-h-10 items-center gap-2 rounded-lg border border-dashed border-input bg-input/50 px-3 text-body text-muted-foreground">
          <Cpu className="h-4 w-4 shrink-0" aria-hidden="true" />
          <span className="truncate italic">
            {t(($) => $.pickers.model_managed_by_runtime)}
          </span>
        </div>
      );
      if (!showLabel) return control;
      return (
        <div className="flex min-w-0 flex-col">
          <Label>{t(($) => $.inspector.prop_model)}</Label>
          <div className="mt-1.5">{control}</div>
        </div>
      );
    }
    return (
      <span className="truncate italic text-muted-foreground">
        {t(($) => $.pickers.model_managed_by_runtime)}
      </span>
    );
  }

  if (!canEdit) {
    if (variant === "field") {
      const control = (
        <div className="flex min-h-10 items-center gap-2 rounded-lg border border-input bg-input/50 px-3 text-body text-muted-foreground">
          <Cpu className="h-4 w-4 shrink-0" aria-hidden="true" />
          <span className="min-w-0 truncate font-mono">{triggerLabel}</span>
        </div>
      );
      if (!showLabel) return control;
      return (
        <div className="flex min-w-0 flex-col">
          <Label>{t(($) => $.inspector.prop_model)}</Label>
          <div className="mt-1.5">{control}</div>
        </div>
      );
    }
    return (
      <span
        className="min-w-0 truncate px-1.5 py-0.5 font-mono text-micro text-muted-foreground"
        title={triggerTitle}
      >
        {triggerLabel}
      </span>
    );
  }

  const picker = (
    <PropertyPicker
      open={open}
      onOpenChange={setOpen}
      width={
        variant === "field"
          ? "w-[var(--anchor-width)] min-w-[16rem] max-w-md"
          : "w-auto min-w-[16rem] max-w-md"
      }
      align="start"
      tooltip={triggerTitle}
      triggerRender={
        <button
          type="button"
          className={
            variant === "field"
              ? `${showLabel ? "mt-1.5 " : ""}flex min-h-10 w-full min-w-0 items-center gap-2 rounded-lg border border-input bg-transparent px-3 text-left text-body transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-3 focus-visible:ring-ring/50`
              : CHIP_CLASS
          }
          aria-label={triggerTitle}
        />
      }
      trigger={
        <>
          {variant === "field" ? (
            <Cpu
              className="h-4 w-4 shrink-0 text-muted-foreground"
              aria-hidden="true"
            />
          ) : null}
          <span
            className={
              variant === "field"
                ? "min-w-0 flex-1 truncate font-mono"
                : "min-w-0 truncate font-mono text-micro"
            }
          >
            {triggerLabel}
          </span>
          {variant === "field" ? (
            <ChevronDown
              className={`h-4 w-4 shrink-0 text-muted-foreground transition-transform ${
                open ? "rotate-180" : ""
              }`}
              aria-hidden="true"
            />
          ) : null}
        </>
      }
      header={
        <ModelSearchHeader
          value={search}
          onChange={setSearch}
          onRefresh={refresh}
          refreshing={modelsQuery.isFetching}
          refreshDisabled={!runtimeOnline || !runtimeId}
          compact
        />
      }
    >
      {modelsQuery.isLoading && (
        <div className="flex items-center gap-2 p-3 text-caption text-muted-foreground">
          <Loader2
            className="h-3 w-3 animate-spin motion-reduce:animate-none"
            aria-hidden="true"
          />
          {t(($) => $.pickers.model_discovering)}
        </div>
      )}

      {!modelsQuery.isLoading &&
        filtered.map((m) => {
          // A catalog row's id is the seat model string. Its secondary line is
          // that same pair decoded — `preset · model id` — so the escape never
          // reaches the screen. The picker fetches no preset list (that is a
          // daemon round trip), so the model half is the id the runtime
          // advertised rather than the preset's own name for it.
          const pair = providerSeatModelDisplay(m.id);
          // A row whose label IS its id has nothing friendlier to say, so the
          // decoded pair becomes the title instead of a subtitle repeating it.
          const title = m.label === m.id ? pair : m.label;
          return (
            <PickerItem
              key={m.id}
              selected={m.id === value}
              onClick={() => void select(m.id)}
              // Tooltip carries the row's canonical identity in its decoded
              // form, so a label truncated by the popover's width is still
              // readable — and never as the escaped catalog id.
              tooltip={pair}
            >
              {/* PickerItem wraps children in a flex `<span>`. Putting a
                  `<div>` inside that <span> is block-in-inline (invalid
                  HTML5) and triggers the browser-default centering quirk
                  that pushes descendants off-axis (model IDs floated to the
                  center instead of left-aligning under their labels). Use
                  `<span block text-left>` to keep layout deterministic —
                  matches the fix already applied in thinking-picker.tsx. */}
              <span className="block min-w-0 flex-1 text-left">
                <span className="block truncate text-label font-medium">{title}</span>
                {m.label !== m.id && (
                  <span className="mt-0.5 block truncate font-mono text-micro leading-snug text-muted-foreground">
                    {pair}
                  </span>
                )}
              </span>
            </PickerItem>
          );
        })}

      {!modelsQuery.isLoading && filtered.length === 0 && !canCreate && (
        <p className="px-3 py-3 text-center text-caption text-muted-foreground">
          {t(($) => $.pickers.model_empty)}
        </p>
      )}

      {!modelsQuery.isLoading && (
        <UnavailableModelsNote
          models={unavailableModels}
          title={t(($) => $.pickers.model_unavailable_heading)}
        />
      )}

      {canCreate && (
        <PickerItem
          selected={false}
          onClick={() => void select(trimmedSearch)}
          tooltip={t(($) => $.pickers.model_custom_tooltip, { value: trimmedSearch })}
        >
          <Plus className="h-3.5 w-3.5 shrink-0 text-primary" />
          <span className="truncate text-primary">
            {t(($) => $.pickers.model_custom_use, { value: trimmedSearch })}
          </span>
        </PickerItem>
      )}

      {value && (
        <button
          type="button"
          onClick={() => void select("")}
          className="mt-1 flex w-full items-center border-t px-3 py-2 text-left text-caption text-muted-foreground transition-colors hover:bg-accent/50"
          title={t(($) => $.pickers.model_clear_title)}
        >
          {t(($) => $.pickers.model_clear)}
        </button>
      )}
    </PropertyPicker>
  );

  if (variant === "field") {
    if (!showLabel) return picker;
    return (
      <div className="flex min-w-0 flex-col">
        <Label>{t(($) => $.inspector.prop_model)}</Label>
        {picker}
      </div>
    );
  }

  return picker;
}
