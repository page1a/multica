"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { Brain, Check, ChevronDown, Cpu, Loader2, Monitor, Sparkles } from "lucide-react";
import {
  ISSUE_DRAFT_CAPABILITIES,
  type IssueDraftCapabilityKey,
} from "@multica/core/issue-drafts";
import {
  runtimeDisplayName,
  runtimeModelsOptions,
} from "@multica/core/runtimes";
import type { MemberWithUser, RuntimeDevice } from "@multica/core/types";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";
import {
  RuntimeFilterToggle,
  RuntimeMachineList,
  type RuntimeFilter,
} from "../../runtimes/components/runtime-machine-list";

/**
 * The whole configuration of an alignment conversation, in one panel: which
 * machine runs it, which model that machine's CLI is asked for, how hard it is
 * asked to think, and which built-in alignment capabilities the carrier runs.
 *
 * One panel rather than four adjacent pills (DENE-514). The first three are not
 * independent — the model list belongs to the machine, and the reasoning levels
 * belong to the model — so choosing them in one place is what makes the
 * dependency visible instead of leaving the user to discover it by picking a
 * model that then has no levels. The capabilities are in the same panel because
 * they are the same decision: "what kind of alignment do I want" and "what runs
 * it" are answered together at the moment the conversation starts.
 *
 * It is the create-time face of choices the page exposes afterwards. The machine
 * and — since DENE-513 — the policy can be changed on a live conversation; the
 * model and the reasoning level cannot, because the daemon reads them off the
 * carrier's agent row, which is written once when the session is created and
 * never updated. The capabilities are assembled into the carrier's prompt at
 * creation too, which is why they are sent once rather than switched.
 *
 * The trigger is a pill, because it sits on the dialog's toolbar beside the
 * project pill — and it summarises all four choices, because a pill that only
 * said "Configuration" would hide the one thing worth knowing at a glance:
 * what this conversation is about to run on.
 *
 * Note the two axes stay separate. Which alignment STYLE the conversation runs
 * (guided questions / plain dialogue / front-end look) is the policy picker's
 * radio group and is deliberately not here; the checkboxes below are the
 * capability axis, which any policy can carry.
 */
export function AlignmentConfigPicker({
  runtimes,
  runtimesLoading,
  members,
  currentUserId,
  runtimeId,
  onRuntimeChange,
  model,
  onModelChange,
  thinkingLevel,
  onThinkingLevelChange,
  capabilities,
  capabilitiesLoading,
  onCapabilitiesChange,
  disabled,
}: {
  runtimes: RuntimeDevice[];
  runtimesLoading?: boolean;
  members: MemberWithUser[];
  currentUserId: string | null;
  runtimeId: string;
  onRuntimeChange: (runtimeId: string) => void;
  /** Empty means "the CLI's own default", which is a legitimate choice. */
  model: string;
  onModelChange: (model: string) => void;
  /** Empty means "follow the local CLI config". */
  thinkingLevel: string;
  onThinkingLevelChange: (level: string) => void;
  /** The capabilities to switch on, in this client's render order. */
  capabilities: readonly IssueDraftCapabilityKey[];
  /** The user record is not ready yet. The section shows a placeholder instead
   *  of boxes, and the entry refuses to start. */
  capabilitiesLoading?: boolean;
  /** Every change is a fresh array; an EMPTY one is a real choice ("none of
   *  them") and travels to the server as `[]` rather than as an omitted field —
   *  see `encodeIssueDraftCapabilities`. */
  onCapabilitiesChange: (capabilities: readonly IssueDraftCapabilityKey[]) => void;
  disabled?: boolean;
}) {
  const { t } = useT("issues");
  const { t: tAgents } = useT("agents");
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState<RuntimeFilter>("mine");

  const selectedRuntime =
    runtimes.find((runtime) => runtime.id === runtimeId) ?? null;
  // The catalog is discovered against a LIVE machine: asking a runtime that is
  // not running is a round trip that cannot come back, so the query is disabled
  // and the section states why rather than spinning forever.
  const runtimeOnline = selectedRuntime?.status === "online";
  const modelsQuery = useQuery(
    runtimeModelsOptions(runtimeOnline ? runtimeId : null),
  );
  const models = useMemo(() => modelsQuery.data?.models ?? [], [modelsQuery.data]);
  const selectedModel = models.find((candidate) => candidate.id === model) ?? null;
  // The catalog is per model, and an unsupported model is a level the daemon
  // would drop. So the levels come from the CHOSEN model, not from the runtime —
  // which is exactly why the three controls share a panel.
  const levels = selectedModel?.thinking?.supported_levels ?? [];
  const selectedLevel = levels.find((level) => level.value === thinkingLevel) ?? null;

  const label = alignmentConfigSummary({
    runtime: selectedRuntime,
    model,
    thinkingLevel,
    thinkingLabel: selectedLevel?.label ?? "",
    capabilities,
    capabilitiesLoading,
    t,
    tAgents,
  });

  const toggleCapability = (key: IssueDraftCapabilityKey, next: boolean) => {
    const set = new Set<IssueDraftCapabilityKey>(capabilities);
    if (next) set.add(key);
    else set.delete(key);
    // An empty set is a request the server accepts — it is the picker's own
    // "none of them", sent as `[]` — so unlike a mandatory skill list this
    // control has no last-checkbox guard to explain.
    onCapabilitiesChange(
      ISSUE_DRAFT_CAPABILITIES.filter((candidate) => set.has(candidate)),
    );
  };

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        disabled={disabled}
        className={cn(
          "inline-flex h-7 max-w-[16rem] min-w-0 items-center gap-1.5 rounded-full border border-border bg-background px-2.5 text-caption",
          "hover:bg-accent/50 disabled:cursor-not-allowed disabled:opacity-50",
          open && "border-ring bg-accent/50",
        )}
        aria-label={t(($) => $.alignment.config_aria)}
        title={label}
      >
        <Monitor className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span className="truncate">{label}</span>
        <ChevronDown
          className={cn(
            "size-3 shrink-0 text-muted-foreground transition-transform",
            open && "rotate-180",
          )}
          aria-hidden="true"
        />
      </PopoverTrigger>
      {/* `w-80` and `align="start"`: the panel is anchored to a pill at the left
          end of a 576px dialog's toolbar, so it opens rightward and stays inside
          the dialog at both breakpoints. `side="top"` keeps it above the footer
          it was opened from, and the cap keeps all four sections reachable on a
          short window instead of letting the machine list push the capability
          boxes off the bottom — each section still scrolls its own body first. */}
      <PopoverContent
        align="start"
        side="top"
        className="max-h-[min(88dvh,44rem)] w-80 overflow-y-auto p-0"
      >
        <Section
          icon={<Monitor className="size-3.5" aria-hidden="true" />}
          title={t(($) => $.alignment.config_runtime)}
          hint={
            runtimesLoading
              ? t(($) => $.alignment.config_runtime_loading)
              : t(($) => $.alignment.config_runtime_hint, {
                  count: runtimes.filter((runtime) => runtime.status === "online")
                    .length,
                })
          }
        >
          {/* The mine/all toggle only when there is something to switch
              between: on a workspace where every machine is the viewer's own it
              is two buttons that do nothing (same rule as RuntimePicker). */}
          {runtimes.some((runtime) => runtime.owner_id !== currentUserId) ? (
            <div className="flex h-7 items-center justify-end px-1">
              <RuntimeFilterToggle
                filter={filter}
                onFilterChange={setFilter}
                disabled={disabled}
              />
            </div>
          ) : null}
          <RuntimeMachineList
            runtimes={runtimes}
            filter={filter}
            members={members}
            currentUserId={currentUserId}
            selectedRuntimeId={runtimeId}
            disabled={disabled}
            onSelect={(nextRuntimeId) => {
              if (nextRuntimeId === runtimeId) return;
              // Model ids are per runtime and levels are per model, so both
              // reset. Leaving them would show a model this machine may not
              // serve, which reads as a choice that was made when it was only
              // inherited (same rule as SwitchAgentBuilderRuntime).
              onRuntimeChange(nextRuntimeId);
              onModelChange("");
              onThinkingLevelChange("");
            }}
          />
        </Section>

        <Section
          icon={<Cpu className="size-3.5" aria-hidden="true" />}
          title={tAgents(($) => $.model_dropdown.label)}
          hint={
            !runtimeOnline
              ? t(($) => $.alignment.config_model_offline)
              : modelsQuery.isLoading
                ? tAgents(($) => $.pickers.model_discovering)
                : modelsQuery.data?.supported === false
                  ? t(($) => $.alignment.config_model_managed)
                  : t(($) => $.alignment.config_model_hint)
          }
        >
          {modelsQuery.isLoading ? (
            <p className="flex items-center gap-2 px-1 py-2 text-caption text-muted-foreground">
              <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
              {tAgents(($) => $.pickers.model_discovering)}
            </p>
          ) : modelsQuery.data?.supported === false ? (
            <p className="px-1 py-2 text-caption text-muted-foreground">
              {tAgents(($) => $.model_dropdown.managed_by_runtime_hint)}
            </p>
          ) : (
            <>
              <Row
                selected={model === ""}
                onClick={() => {
                  onModelChange("");
                  onThinkingLevelChange("");
                }}
              >
                <span className="min-w-0 flex-1 truncate">
                  {t(($) => $.alignment.config_model_default)}
                </span>
                {model === "" && (
                  <Check className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
                )}
              </Row>
              {models.map((candidate) => (
                <Row
                  key={candidate.id}
                  selected={candidate.id === model}
                  onClick={() => {
                    if (candidate.id === model) return;
                    // The levels belong to the model, so a level chosen for the
                    // previous one is cleared rather than carried across.
                    onModelChange(candidate.id);
                    onThinkingLevelChange("");
                  }}
                >
                  <span className="min-w-0 flex-1">
                    <span className="block truncate">{candidate.label}</span>
                    {candidate.label !== candidate.id && (
                      <span className="mono mt-0.5 block truncate text-muted-foreground">
                        {candidate.id}
                      </span>
                    )}
                  </span>
                  {candidate.id === model && (
                    <Check className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
                  )}
                </Row>
              ))}
            </>
          )}
        </Section>

        <Section
          icon={<Brain className="size-3.5" aria-hidden="true" />}
          title={tAgents(($) => $.inspector.prop_thinking)}
          hint={
            levels.length > 0
              ? t(($) => $.alignment.config_thinking_hint)
              : t(($) => $.alignment.config_thinking_unavailable)
          }
        >
          {levels.length === 0 ? (
            // Stated, not hidden: the panel is where the user looks for it, and
            // an absent control reads as a missing feature rather than as a
            // model that has no reasoning dial.
            <p className="px-1 py-2 text-caption text-muted-foreground">
              {t(($) => $.alignment.config_thinking_none)}
            </p>
          ) : (
            <div className="flex flex-wrap gap-1.5 px-1 py-1">
              <LevelChip
                selected={thinkingLevel === ""}
                label={tAgents(($) => $.pickers.thinking_default)}
                onClick={() => onThinkingLevelChange("")}
              />
              {levels.map((level) => (
                <LevelChip
                  key={level.value}
                  selected={level.value === thinkingLevel}
                  label={level.label}
                  title={level.description}
                  onClick={() => onThinkingLevelChange(level.value)}
                />
              ))}
            </div>
          )}
        </Section>

        <Section
          icon={<Sparkles className="size-3.5" aria-hidden="true" />}
          title={t(($) => $.alignment.capability_label)}
          hint={
            capabilitiesLoading
              ? t(($) => $.alignment.capability_preference_loading)
              : t(($) => $.alignment.config_capabilities_hint, {
                  count: capabilities.length,
                })
          }
        >
          {capabilitiesLoading ? (
            <p className="flex items-center gap-2 px-1 py-2 text-caption text-muted-foreground">
              <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
              {t(($) => $.alignment.capability_preference_loading)}
            </p>
          ) : ISSUE_DRAFT_CAPABILITIES.map((key) => {
            const checked = capabilities.includes(key);
            return (
              <label
                key={key}
                className="flex cursor-pointer items-start gap-2 rounded-md px-1.5 py-1.5 transition-colors hover:bg-accent/50"
              >
                <Checkbox
                  className="mt-0.5"
                  checked={checked}
                  disabled={disabled}
                  onCheckedChange={(value) => toggleCapability(key, value === true)}
                />
                <span className="min-w-0 flex-1">
                  <span className={cn("block", checked && "font-medium")}>
                    {t(($) => $.alignment[CAPABILITY_COPY[key]])}
                  </span>
                  <span className="mt-0.5 block text-caption leading-snug text-muted-foreground">
                    {t(($) => $.alignment[CAPABILITY_DESCRIPTION[key]])}
                  </span>
                </span>
              </label>
            );
          })}
        </Section>
      </PopoverContent>
    </Popover>
  );
}

/**
 * The message key each capability's name and description resolve to.
 *
 * Records rather than ternaries on the key: `ISSUE_DRAFT_CAPABILITIES` is the
 * whitelist this panel renders, so a capability added there without copy here is
 * a compile error rather than a checkbox with a raw registry key for a label.
 */
const CAPABILITY_COPY = {
  wayfinder: "capability_wayfinder",
  grill: "capability_grill",
  "grill-frontend-look": "capability_frontend",
} as const satisfies Record<IssueDraftCapabilityKey, string>;

const CAPABILITY_DESCRIPTION = {
  wayfinder: "capability_wayfinder_description",
  grill: "capability_grill_description",
  "grill-frontend-look": "capability_frontend_description",
} as const satisfies Record<IssueDraftCapabilityKey, string>;

/** The pill's one line: machine, model, effort, and how many capabilities are
 *  on. The stored value is what is shown for a level the catalog no longer
 *  lists, so the pill never claims "follow the CLI" for a token that will be
 *  sent. */
function alignmentConfigSummary({
  runtime,
  model,
  thinkingLevel,
  thinkingLabel,
  capabilities,
  capabilitiesLoading,
  t,
  tAgents,
}: {
  runtime: RuntimeDevice | null;
  model: string;
  thinkingLevel: string;
  thinkingLabel: string;
  capabilities: readonly IssueDraftCapabilityKey[];
  capabilitiesLoading?: boolean;
  t: TFunction<"issues">;
  tAgents: TFunction<"agents">;
}): string {
  if (!runtime) return t(($) => $.alignment.config_pick_runtime);
  const parts = [runtimeDisplayName(runtime)];
  parts.push(model || t(($) => $.alignment.config_model_short));
  if (thinkingLevel) parts.push(thinkingLabel || thinkingLevel);
  else parts.push(tAgents(($) => $.pickers.thinking_default));
  parts.push(
    capabilitiesLoading
      ? t(($) => $.alignment.capability_preference_loading)
      : t(($) => $.alignment.config_capabilities_count, { count: capabilities.length }),
  );
  return parts.join(" · ");
}

function Section({
  icon,
  title,
  hint,
  children,
}: {
  icon: React.ReactNode;
  title: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="border-b border-border px-2 py-2 last:border-b-0">
      <div className="flex items-baseline justify-between gap-2 px-1 pb-1">
        <span className="flex items-center gap-1.5 text-caption font-medium">
          <span className="text-muted-foreground">{icon}</span>
          {title}
        </span>
        {hint ? (
          <span className="truncate text-caption text-muted-foreground">{hint}</span>
        ) : null}
      </div>
      {/* A flex column with the cap on the outside: the machine list scrolls
          its own rows while its search box and the section header stay put,
          and a section with a handful of rows never scrolls at all. */}
      <div className="flex max-h-44 flex-col overflow-y-auto">{children}</div>
    </div>
  );
}

function Row({
  selected,
  onClick,
  children,
}: {
  selected: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "flex w-full items-start gap-2 rounded-md px-1.5 py-1.5 text-left text-caption transition-colors hover:bg-accent/50",
        // Selection on weight + colour, so hovering the selected row cannot
        // make it look like a plain hover (UI rule).
        selected ? "font-medium text-foreground" : "text-muted-foreground",
      )}
    >
      {children}
    </button>
  );
}

function LevelChip({
  selected,
  label,
  title,
  onClick,
}: {
  selected: boolean;
  label: string;
  title?: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={title}
      aria-pressed={selected}
      className={cn(
        "rounded-full border px-2 py-0.5 text-caption transition-colors",
        selected
          ? "border-foreground bg-foreground font-medium text-background"
          : "border-border text-muted-foreground hover:bg-accent/50",
      )}
    >
      {label}
    </button>
  );
}
