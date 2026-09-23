"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Switch } from "@multica/ui/components/ui/switch";
import { Badge } from "@multica/ui/components/ui/badge";
import { api } from "@multica/core/api";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useCurrentMember } from "@multica/core/permissions";
import {
  routingHealthOptions,
  workspaceKeys,
  workspaceListOptions,
} from "@multica/core/workspace/queries";
import type { RoutingHealth } from "@multica/core/workspace/routing-health";
import { DEFAULT_ROUTING_POLICY_PROMPT } from "@multica/core/workspace/routing-policy-prompt";
import {
  normalizeStaleReviewHours,
  normalizeThreshold,
  parseRoutingSettings,
  routingGatewayIsComplete,
  routingState,
  withRoutingSettings,
  type RoutingSettings,
  type RoutingState,
} from "@multica/core/workspace/routing-settings";
import type { Workspace } from "@multica/core/types";
import { useT } from "../../i18n";
import {
  SettingsCard,
  SettingsRow,
  SettingsSaveState,
  SettingsSection,
  SettingsTab,
} from "./settings-layout";
import { useAutoSave } from "./use-auto-save";

/**
 * The routing section — the ONLY screen this feature adds.
 *
 * The 验收席 slot renders through the existing custom-property panel and the
 * routing decisions render as ordinary comments, so neither needed a change.
 * Hiding the slot while routing is off is done by archiving the property
 * definition, which the property panel already honours.
 *
 * The model field accepts manual ids but also gets a catalog from the selected
 * gateway. The server keeps the credential on its side, returns ids only, and
 * the first id fills an empty field; manual entry stays available for gateways
 * that do not implement `/models`.
 *
 * Two protocols can be on the other end. An OpenAI-compatible gateway is
 * asked for JSON; a TypeSafe System One endpoint (Jev) is asked for a typed
 * judgment with its probability distribution. The section names which one is
 * in use, because the confidence the threshold gates on means different things
 * on the two: measured on one, self-reported on the other.
 *
 * Which gateway is now a workspace decision. It did not used to be: the judge
 * borrowed the deployment's MULTICA_LLM_* configuration, which is fine for one
 * operator on their own box and stops being fine the moment the instance is
 * shared with a team — "ask whoever runs the server to edit an env var and
 * restart" is not a setting, and it is the same answer for every workspace on
 * the machine. Both fields stay optional: leave them empty and the deployment
 * gateway is used exactly as before.
 *
 * The key is the one field here that is write-only. It is sealed server-side
 * and stripped from every response, so there is nothing to render back — the
 * box shows whether a key is stored, never which.
 */
export function RoutingTab() {
  const { t } = useT("settings");
  const qc = useQueryClient();
  const workspace = useCurrentWorkspace();
  // Definitions are owner/admin work, like every other workspace-level
  // setting. Members see the section and its state but cannot change it.
  const { role } = useCurrentMember(workspace?.id ?? "");
  const canManage = role === "owner" || role === "admin";

  const saved = useMemo(
    () => parseRoutingSettings(workspace?.settings),
    [workspace?.settings],
  );

  const [enabled, setEnabled] = useState(saved.enabled);
  const [model, setModel] = useState(saved.model);
  const [threshold, setThreshold] = useState(String(saved.confidence_threshold));
  const [staleHours, setStaleHours] = useState(String(saved.stale_review_hours));
  const [baseUrl, setBaseUrl] = useState(saved.base_url);
  const [policyPrompt, setPolicyPrompt] = useState(saved.policy_prompt ?? "");
  const [availableModels, setAvailableModels] = useState<string[]>([]);
  const autoDiscoverKey = useRef("");
  const autoFilledModel = useRef("");
  // Held apart from the auto-saved draft on purpose. Auto-save fires while
  // somebody is still typing, and a half-typed credential saved to the server
  // is both useless and a real key sitting in a row nobody will think to
  // clear. The key is committed by an explicit button instead.
  const [keyInput, setKeyInput] = useState("");

  // Reset only when the workspace changes, not on every cached-object
  // replacement — an unrelated mutation must not wipe an unsaved edit.
  useEffect(() => {
    const next = parseRoutingSettings(workspace?.settings);
    setEnabled(next.enabled);
    setModel(next.model);
    setThreshold(String(next.confidence_threshold));
    setStaleHours(String(next.stale_review_hours));
    setBaseUrl(next.base_url);
    setPolicyPrompt(next.policy_prompt ?? "");
    setKeyInput("");
    setAvailableModels([]);
    autoDiscoverKey.current = "";
    autoFilledModel.current = "";
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspace?.id]);

  const draft: RoutingSettings = useMemo(
    () => ({
      enabled,
      model,
      confidence_threshold: normalizeThreshold(Number(threshold)),
      stale_review_hours: normalizeStaleReviewHours(Number(staleHours)),
      base_url: baseUrl,
      policy_prompt: policyPrompt,
    }),
    [enabled, model, threshold, staleHours, baseUrl, policyPrompt],
  );

  const discoverModels = useMutation({
    mutationFn: async () => {
      if (!workspace) throw new Error("workspace is not selected");
      return api.listRoutingModels(workspace.id);
    },
    onSuccess: (result) => {
      setAvailableModels(result.models);
      // Discovery is an aid, not an override: keep a model somebody already
      // chose, and only fill the empty state the user asked us to complete.
      if (result.models.length > 0) {
        const first = result.models[0];
        if (first) {
          setModel((current) => {
            if (current.trim() !== "") return current;
            autoFilledModel.current = first;
            return first;
          });
        }
      }
    },
    onError: () => setAvailableModels([]),
  });

  const autoSave = useAutoSave({
    value: draft,
    savedValue: saved,
    onSave: async (next) => {
      if (!workspace) return;
      const updated = await api.updateWorkspace(workspace.id, {
        settings: withRoutingSettings(workspace.settings, next),
      });
      qc.setQueryData(
        workspaceListOptions().queryKey,
        (old: Workspace[] | undefined) =>
          old?.map((ws) => (ws.id === updated.id ? updated : ws)),
      );
      // The health report is derived from the settings that were just
      // replaced, so the cached copy is stale the moment this resolves.
      // Without this the chip keeps describing the previous configuration for
      // up to a refetch interval.
      await qc.invalidateQueries({
        queryKey: workspaceKeys.routingHealth(workspace.id),
      });
    },
    enabled: !!workspace && canManage,
    isEqual: (a, b) =>
      a.enabled === b.enabled &&
      a.model.trim() === b.model.trim() &&
      a.confidence_threshold === b.confidence_threshold &&
      a.stale_review_hours === b.stale_review_hours &&
      a.base_url.trim() === b.base_url.trim() &&
      (a.policy_prompt ?? "").trim() === (b.policy_prompt ?? "").trim(),
  });

  // Live health from the server. Without it the fourth state is unreachable:
  // the stored fields cannot know that the model is rejected or cooling down,
  // and the routing design deliberately keeps that off every ticket — so a
  // failure the section did not show would be visible nowhere but the server
  // log (DENE-633 review, F2).
  const health = useQuery({
    ...routingHealthOptions(workspace?.id ?? ""),
    enabled: !!workspace?.id,
  });

  // A workspace that already has URL + key saved should not need to touch the
  // form again after an app restart. Discover once for the current target;
  // manual re-entry remains available after a provider that has no /models.
  useEffect(() => {
    const healthData = health.data;
    if (
      !workspace ||
      !canManage ||
      !enabled ||
      !healthData?.gateway_configured ||
      (saved.base_url.trim() !== "" && !healthData.gateway_key_set) ||
      discoverModels.isPending
    ) {
      return;
    }
    const key = [
      workspace.id,
      healthData.gateway_scope,
      healthData.gateway_host,
      healthData.gateway_key_set,
      saved.base_url,
    ].join("|");
    if (autoDiscoverKey.current === key) return;
    autoDiscoverKey.current = key;
    // A half-filled custom pair falls back to the deployment gateway. Do not
    // fill from that fallback, because the next key save would silently leave
    // a deployment-only model selected for the new workspace gateway.
    if (model.trim() !== "" && autoFilledModel.current !== model.trim()) return;
    if (autoFilledModel.current === model.trim()) setModel("");
    discoverModels.mutate();
  }, [
    canManage,
    discoverModels,
    enabled,
    health.data,
    model,
    saved.base_url,
    workspace,
  ]);

  const recheck = useMutation({
    mutationFn: () => api.checkRoutingHealth(workspace?.id ?? ""),
    onSuccess: (next) => {
      qc.setQueryData(workspaceKeys.routingHealth(workspace?.id ?? ""), next);
    },
  });

  // The key write. Explicit rather than auto-saved (see keyInput), and it
  // sends the rest of the draft along because the server replaces the whole
  // settings column — sending the key alone would revert an unsaved model or
  // endpoint edit made in the same sitting.
  const saveKey = useMutation({
    mutationFn: async (nextKey: string) => {
      if (!workspace) return;
      const updated = await api.updateWorkspace(workspace.id, {
        settings: withRoutingSettings(workspace.settings, draft, nextKey),
      });
      qc.setQueryData(
        workspaceListOptions().queryKey,
        (old: Workspace[] | undefined) =>
          old?.map((ws) => (ws.id === updated.id ? updated : ws)),
      );
      await qc.invalidateQueries({
        queryKey: workspaceKeys.routingHealth(workspace.id),
      });
    },
    // Cleared whichever way it went: on success the key is stored and there
    // is nothing to show, and on failure leaving a credential in a text box
    // behind a red message is not something to do to somebody.
    onSettled: () => setKeyInput(""),
  });

  // The draft wins over the server report while an edit is in flight: a person
  // who just switched routing off should not keep reading a red chip about the
  // model they stopped using. Health only decides the enabled/ineffective
  // split, and only once the draft itself says enabled — and only on the
  // server's explicit `ineffective`, so a health report that has not caught up
  // with the switch yet cannot report a fault that does not exist.
  const state = routingState(draft, health.data);
  const defaultPrompt =
    health.data?.default_policy_prompt?.trim() || DEFAULT_ROUTING_POLICY_PROMPT;
  const shownPrompt = policyPrompt.trim() === "" ? defaultPrompt : policyPrompt;

  return (
    <SettingsTab
      title={t(($) => $.routing.title)}
      description={t(($) => $.routing.description)}
    >
      <SettingsSection>
        <StateBanner
          state={state}
          health={health.data}
          canManage={canManage}
          checking={recheck.isPending}
          onRecheck={() => recheck.mutate()}
        />
      </SettingsSection>

      <SettingsSection title={t(($) => $.routing.section_title)}>
        <SettingsCard>
          <SettingsRow
            label={t(($) => $.routing.enabled_label)}
            description={t(($) => $.routing.enabled_description)}
          >
            <Switch
              checked={enabled}
              disabled={!canManage}
              onCheckedChange={setEnabled}
              aria-label={t(($) => $.routing.enabled_label)}
            />
          </SettingsRow>

          <SettingsRow
            label={t(($) => $.routing.model_label)}
            description={t(($) => $.routing.model_description)}
            size="text"
          >
            <Input
              list="routing-model-options"
              value={model}
              disabled={!canManage}
              placeholder={t(($) => $.routing.model_placeholder)}
              onChange={(e) => setModel(e.target.value)}
              aria-label={t(($) => $.routing.model_label)}
            />
            {availableModels.length > 0 ? (
              <datalist id="routing-model-options">
                {availableModels.map((availableModel) => (
                  <option key={availableModel} value={availableModel} />
                ))}
              </datalist>
            ) : null}
            <p className="text-micro text-muted-foreground">
              {discoverModels.isPending
                ? t(($) => $.routing.model_discovering)
                : discoverModels.isError
                  ? t(($) => $.routing.model_discover_failed)
                  : discoverModels.isSuccess && availableModels.length === 0
                    ? t(($) => $.routing.model_discover_empty)
                    : availableModels.length > 0
                      ? t(($) => $.routing.model_discover_loaded, {
                          count: availableModels.length,
                        })
                      : null}
            </p>
          </SettingsRow>

          <SettingsRow
            label={t(($) => $.routing.threshold_label)}
            description={t(($) => $.routing.threshold_description)}
            size="code"
          >
            <Input
              type="number"
              min={0.05}
              max={1}
              step={0.05}
              value={threshold}
              // Greyed out while routing is off, so the number cannot look
              // like it is doing something it is not.
              disabled={!canManage || !enabled}
              onChange={(e) => setThreshold(e.target.value)}
              aria-label={t(($) => $.routing.threshold_label)}
            />
          </SettingsRow>

          <SettingsRow
            label={t(($) => $.routing.stale_hours_label)}
            description={t(($) => $.routing.stale_hours_description)}
            size="code"
          >
            <Input
              type="number"
              min={1}
              max={720}
              step={1}
              value={staleHours}
              // Same gate as the threshold: this row runs only while routing
              // is on, and it is deliberately not a switch of its own.
              disabled={!canManage || !enabled}
              onChange={(e) => setStaleHours(e.target.value)}
              aria-label={t(($) => $.routing.stale_hours_label)}
            />
          </SettingsRow>
        </SettingsCard>
        <GatewayNote health={health.data} />
      </SettingsSection>

      <SettingsSection title={t(($) => $.routing.gateway_title)}>
        <SettingsCard>
          <SettingsRow
            label={t(($) => $.routing.gateway_url_label)}
            description={t(($) => $.routing.gateway_url_description)}
            size="text"
          >
            <Input
              value={baseUrl}
              disabled={!canManage}
              placeholder={t(($) => $.routing.gateway_url_placeholder)}
              onChange={(e) => setBaseUrl(e.target.value)}
              aria-label={t(($) => $.routing.gateway_url_label)}
            />
          </SettingsRow>

          <SettingsRow
            label={t(($) => $.routing.gateway_key_label)}
            description={
              health.data?.workspace_key_storable === false
                ? t(($) => $.routing.gateway_key_unstorable)
                : t(($) => $.routing.gateway_key_description)
            }
            size="text"
          >
            <div className="flex w-full items-center gap-2">
              <Input
                type="password"
                value={keyInput}
                autoComplete="off"
                disabled={
                  !canManage ||
                  health.data?.workspace_key_storable === false ||
                  saveKey.isPending
                }
                // The placeholder is the whole readback: the stored key never
                // leaves the server, so "a key is saved" is the most this box
                // can honestly say.
                placeholder={
                  health.data?.gateway_key_set
                    ? t(($) => $.routing.gateway_key_stored)
                    : t(($) => $.routing.gateway_key_placeholder)
                }
                onChange={(e) => setKeyInput(e.target.value)}
                aria-label={t(($) => $.routing.gateway_key_label)}
              />
              <Button
                type="button"
                size="sm"
                disabled={
                  !canManage || keyInput.trim() === "" || saveKey.isPending
                }
                onClick={() => saveKey.mutate(keyInput)}
                className="shrink-0"
              >
                {t(($) => $.routing.gateway_key_save)}
              </Button>
              {health.data?.gateway_key_set && canManage ? (
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  disabled={saveKey.isPending}
                  onClick={() => saveKey.mutate("")}
                  className="shrink-0"
                >
                  {t(($) => $.routing.gateway_key_clear)}
                </Button>
              ) : null}
            </div>
          </SettingsRow>
        </SettingsCard>
        <GatewayPairNote
          baseUrl={baseUrl}
          keyStored={health.data?.gateway_key_set === true}
        />
      </SettingsSection>

      <SettingsSection
        title={t(($) => $.routing.policy_label)}
        description={t(($) => $.routing.policy_description)}
        action={
          <div className="flex items-center gap-2">
            <SettingsSaveState
              status={autoSave.status}
              savingLabel={t(($) => $.routing.policy_saving)}
              savedLabel={t(($) => $.routing.policy_saved)}
              errorLabel={t(($) => $.routing.policy_save_error)}
            />
            {canManage ? (
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => setPolicyPrompt("")}
              >
                {t(($) => $.routing.policy_restore)}
              </Button>
            ) : null}
          </div>
        }
      >
        <Textarea
          value={shownPrompt}
          disabled={!canManage}
          rows={8}
          aria-label={t(($) => $.routing.policy_label)}
          onChange={(event) => {
            const next = event.target.value;
            setPolicyPrompt(next.trim() === defaultPrompt.trim() ? "" : next);
          }}
          className="min-h-36 text-caption leading-5"
        />
        {autoSave.status === "error" ? (
          <p className="text-caption text-destructive" role="alert">
            {t(($) => $.routing.policy_save_error)}
          </p>
        ) : null}
      </SettingsSection>

      <SettingsSection
        title={t(($) => $.routing.quota_title)}
        description={t(($) => $.routing.quota_description)}
      >
        <SettingsCard>
          {(health.data?.provider_quotas ?? []).map((quota) => {
            let value = t(($) => $.routing.quota_unknown);
            if (quota.status === "quota_exhausted") {
              value = t(($) => $.routing.quota_exhausted);
            } else if (quota.status === "available" && quota.used_percent != null) {
              value = t(($) => $.routing.quota_available, {
                percent: Math.round(quota.used_percent),
              });
            }
            const reset = quota.reset_at
              ? t(($) => $.routing.quota_resets, { when: quota.reset_at })
              : "";
            const updated = quota.observed_at
              ? t(($) => $.routing.quota_updated, { when: quota.observed_at })
              : "";
            return (
              <SettingsRow key={quota.provider} label={providerLabel(quota.provider)}>
                <span className="font-mono text-caption tabular-nums text-muted-foreground">
                  {[value, reset, updated].filter(Boolean).join(" · ")}
                </span>
              </SettingsRow>
            );
          })}
        </SettingsCard>
      </SettingsSection>

      <SettingsSection
        title={t(($) => $.routing.seats_title)}
        description={t(($) => $.routing.filters_placeholder)}
      >
        <SettingsCard>
          {(health.data?.seats ?? []).length === 0 ? (
            <p className="px-4 py-3 text-caption text-muted-foreground">
              {t(($) => $.routing.seats_empty)}
            </p>
          ) : (
            (health.data?.seats ?? []).map((seat) => (
              <SettingsRow key={seat.agent_id} label={seat.tier}>
                <span className="font-mono text-caption text-muted-foreground">
                  {seat.availability}
                </span>
              </SettingsRow>
            ))
          )}
        </SettingsCard>
      </SettingsSection>
    </SettingsTab>
  );
}

function providerLabel(provider: string): string {
  switch (provider) {
    case "claude":
      return "Claude";
    case "codex":
      return "Codex";
    case "grok":
      return "Grok";
    default:
      return provider;
  }
}

/**
 * Where the model id above is sent.
 *
 * The box is a bare model identifier, which left the obvious question — "which
 * model is this, and on whose endpoint?" — answerable nowhere in the product.
 * Naming the host lets a reader tell at a glance whether the model they are
 * about to type exists on it.
 *
 * It also has to say WHOSE host it is. The same hostname means two different
 * things depending on scope: one the workspace can edit in the card below, one
 * only whoever runs the server can change. The unconfigured case is the
 * important one — it is the most common reason routing silently does nothing —
 * and it now has a fix on this very screen, so it points at it.
 */
function GatewayNote({ health }: { health?: RoutingHealth }) {
  const { t } = useT("settings");
  if (health && health.gateway_configured === false) {
    return (
      <p className="px-0.5 text-caption leading-5 text-destructive">
        {t(($) => $.routing.gateway_unset)}
      </p>
    );
  }
  const scoped =
    health?.gateway_scope === "workspace"
      ? t(($) => $.routing.gateway_endpoint_workspace, {
          host: health?.gateway_host ?? "",
        })
      : t(($) => $.routing.gateway_endpoint_deployment, {
          host: health?.gateway_host ?? "",
        });
  return (
    <div className="flex flex-col gap-1 px-0.5">
      {health?.gateway_host ? (
        <p className="text-caption leading-5 text-muted-foreground">{scoped}</p>
      ) : null}
      {health?.gateway_protocol === "systemone" ? (
        <p className="text-caption leading-5 text-muted-foreground">
          {t(($) => $.routing.gateway_protocol_systemone)}
        </p>
      ) : null}
      {health?.gateway_default_model ? (
        <p className="text-caption leading-5 text-muted-foreground">
          {t(($) => $.routing.gateway_default_model, {
            model: health.gateway_default_model,
          })}
        </p>
      ) : null}
    </div>
  );
}

/**
 * The half-filled pair.
 *
 * An endpoint with no key, or a key with no endpoint, is not an error — it
 * falls back whole to the deployment gateway, because sending the deployment
 * key to a host the operator never chose, or a workspace key to the
 * deployment's host, are both worse than doing nothing. But a silent fallback
 * is how somebody spends an afternoon wondering why their own model is never
 * called, so it is stated.
 */
function GatewayPairNote({
  baseUrl,
  keyStored,
}: {
  baseUrl: string;
  keyStored: boolean;
}) {
  const { t } = useT("settings");
  const typedSomething = baseUrl.trim() !== "" || keyStored;
  if (!typedSomething || routingGatewayIsComplete(baseUrl, keyStored)) {
    return null;
  }
  return (
    <p className="px-0.5 text-caption leading-5 text-warning-foreground">
      {t(($) => $.routing.gateway_pair_incomplete)}
    </p>
  );
}

/**
 * The four states, each with its own chip and its own sentence.
 *
 * `incomplete` is the one that must never be collapsed into `enabled`:
 * somebody who flipped the switch and walked away will otherwise believe
 * routing is working while the product behaves exactly as it did before.
 */
function StateBanner({
  state,
  health,
  canManage,
  checking,
  onRecheck,
}: {
  state: RoutingState;
  health?: RoutingHealth;
  canManage: boolean;
  checking: boolean;
  onRecheck: () => void;
}) {
  const { t } = useT("settings");
  const chip: Record<
    RoutingState,
    { variant: "secondary" | "destructive" | "default"; className?: string }
  > = {
    off: { variant: "secondary" },
    incomplete: {
      variant: "secondary",
      className: "bg-warning/15 text-warning-foreground",
    },
    enabled: { variant: "default", className: "bg-success/15 text-success" },
    ineffective: { variant: "destructive" },
  };

  const lastCheckedLabel = (
    translate: typeof t,
    unixSeconds: number,
  ): string => {
    const { unit, n } = timeAgoBucket(unixSeconds);
    if (unit === "now") return translate(($) => $.routing.health_time_just_now);
    if (unit === "minutes")
      return translate(($) => $.routing.health_time_minutes_ago, { n });
    return translate(($) => $.routing.health_time_hours_ago, { n });
  };

  const detail =
    state === "ineffective" && health?.reason
      ? `${t(($) => $.routing.states.ineffective.detail)} ${t(($) => $.routing.health_reason_label)}: ${health.reason}`
      : t(($) => $.routing.states[state].detail);

  return (
    <div className="flex flex-col gap-2 rounded-lg border border-surface-border px-4 py-3 sm:flex-row sm:items-start sm:gap-4">
      <Badge
        variant={chip[state].variant}
        className={chip[state].className}
        data-state={state}
      >
        {t(($) => $.routing.states[state].chip)}
      </Badge>
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <p className="text-caption leading-5 text-muted-foreground">{detail}</p>
        {state === "enabled" && (
          <p className="text-caption leading-5 text-muted-foreground">
            {health?.last_success_at
              ? t(($) => $.routing.health_connected, {
                  when: lastCheckedLabel(t, health.last_success_at),
                })
              : t(($) => $.routing.health_never_checked)}
          </p>
        )}
        {state === "ineffective" && health?.retry_after_seconds ? (
          <p className="text-caption leading-5 text-muted-foreground">
            {t(($) => $.routing.health_retry_in, {
              minutes: Math.max(1, Math.round(health.retry_after_seconds / 60)),
            })}
          </p>
        ) : null}
      </div>
      {/* Offered for both live states, not just the broken one: a person who
          just typed a model identifier wants to know it works before they
          find out from a ticket that never got dispatched. */}
      {canManage && (state === "enabled" || state === "ineffective") && (
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={checking}
          onClick={onRecheck}
          className="shrink-0"
        >
          {checking
            ? t(($) => $.routing.health_checking)
            : t(($) => $.routing.health_recheck)}
        </Button>
      )}
    </div>
  );
}

/**
 * How long ago, as a unit plus a count. The caller resolves the copy, because
 * only it holds a properly typed `t`.
 *
 * Deliberately coarse: what this describes is "the last time the routing model
 * answered", and the reader wants to know whether that was recent, not when
 * exactly.
 */
export function timeAgoBucket(
  unixSeconds: number,
  now: number = Date.now(),
): { unit: "now" | "minutes" | "hours"; n: number } {
  const minutes = Math.floor((now / 1000 - unixSeconds) / 60);
  if (minutes < 1) return { unit: "now", n: 0 };
  if (minutes < 60) return { unit: "minutes", n: minutes };
  return { unit: "hours", n: Math.floor(minutes / 60) };
}
