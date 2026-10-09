"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Switch } from "@multica/ui/components/ui/switch";
import { Badge } from "@multica/ui/components/ui/badge";
import { api } from "@multica/core/api";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useCurrentMember } from "@multica/core/permissions";
import { runtimeListOptions } from "@multica/core/runtimes";
import {
  routingHealthOptions,
  workspaceKeys,
  workspaceListOptions,
  memberListOptions,
} from "@multica/core/workspace/queries";
import {
  roleHealth,
  type RoutingHealth,
  type RoutingRoleHealth,
  type RoutingRule,
  type RoutingRuleTable,
} from "@multica/core/workspace/routing-health";
import { DEFAULT_ROUTING_POLICY_PROMPT } from "@multica/core/workspace/routing-policy-prompt";
import {
  normalizeStaleReviewHours,
  normalizeThreshold,
  parseRoutingSettings,
  routingGatewayIsComplete,
  routingMode,
  routingState,
  withRoutingSettings,
  type RoutingRole,
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
import { RoutingSeatsTable } from "./routing-seats-table";
import { RoutingDomainsSection } from "./routing-domains";
import { RuntimePicker } from "../../agents/components/runtime-picker";
import { ModelDropdown } from "../../agents/components/model-dropdown";
import { ThinkingSettingField } from "../../agents/components/inspector/thinking-prop-row";

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
 *
 * Two model roles, each with its own switch, endpoint and key (DENE-923): the
 * analysis model reads the whole ticket and reduces it to facts; the judge
 * picks the tier. Either, both or neither can be on, and the section says
 * which combination is in effect.
 */
export function RoutingTab() {
  const { t } = useT("settings");
  const qc = useQueryClient();
  const workspace = useCurrentWorkspace();
  // Definitions are owner/admin work, like every other workspace-level
  // setting. Members see the section and its state but cannot change it.
  const { role, userId } = useCurrentMember(workspace?.id ?? "");
  const canManage = role === "owner" || role === "admin";
  const { data: runtimes = [], isLoading: runtimesLoading } = useQuery(
    runtimeListOptions(workspace?.id ?? ""),
  );
  const { data: members = [] } = useQuery(memberListOptions(workspace?.id ?? ""));

  const saved = useMemo(
    () => parseRoutingSettings(workspace?.settings),
    [workspace?.settings],
  );

  const [enabled, setEnabled] = useState(saved.enabled);
  const [model, setModel] = useState(saved.model);
  const [judgeEnabled, setJudgeEnabled] = useState(saved.judge_enabled);
  const [analysisEnabled, setAnalysisEnabled] = useState(saved.analysis.enabled);
  const [analysisModel, setAnalysisModel] = useState(saved.analysis.model);
  const [analysisBaseUrl, setAnalysisBaseUrl] = useState(saved.analysis.base_url);
  const [analysisSource, setAnalysisSource] = useState(saved.analysis.source ?? "api_gateway");
  const [analysisRuntimeId, setAnalysisRuntimeId] = useState(saved.analysis.runtime_id ?? "");
  const analysisRuntime = runtimes.find((runtime) => runtime.id === analysisRuntimeId) ?? null;
  const [analysisThinkingLevel, setAnalysisThinkingLevel] = useState(saved.analysis.thinking_level ?? "low");
  const [threshold, setThreshold] = useState(String(saved.confidence_threshold));
  const [staleHours, setStaleHours] = useState(String(saved.stale_review_hours));
  const [baseUrl, setBaseUrl] = useState(saved.base_url);
  const [policyPrompt, setPolicyPrompt] = useState(saved.policy_prompt ?? "");
  const [usagePriority, setUsagePriority] = useState(saved.usage_priority);
  const [allowUpshift, setAllowUpshift] = useState(saved.allow_upshift);
  const [preferContinuation, setPreferContinuation] = useState(saved.prefer_continuation);
  const [preferIdle, setPreferIdle] = useState(saved.prefer_idle);
  const [judgedReview, setJudgedReview] = useState(saved.judged_review);
  const [availableModels, setAvailableModels] = useState<string[]>([]);
  const autoDiscoverKey = useRef("");
  const autoFilledModel = useRef("");
  // Held apart from the auto-saved draft on purpose. Auto-save fires while
  // somebody is still typing, and a half-typed credential saved to the server
  // is both useless and a real key sitting in a row nobody will think to
  // clear. The key is committed by an explicit button instead.
  const [keyInput, setKeyInput] = useState("");
  const [analysisKeyInput, setAnalysisKeyInput] = useState("");

  // Reset only when the workspace changes, not on every cached-object
  // replacement — an unrelated mutation must not wipe an unsaved edit.
  useEffect(() => {
    const next = parseRoutingSettings(workspace?.settings);
    setEnabled(next.enabled);
    setModel(next.model);
    setJudgeEnabled(next.judge_enabled);
    setAnalysisEnabled(next.analysis.enabled);
    setAnalysisModel(next.analysis.model);
    setAnalysisBaseUrl(next.analysis.base_url);
    setAnalysisSource(next.analysis.source ?? "api_gateway");
    setAnalysisRuntimeId(next.analysis.runtime_id ?? "");
    setAnalysisThinkingLevel(next.analysis.thinking_level ?? "low");
    setThreshold(String(next.confidence_threshold));
    setStaleHours(String(next.stale_review_hours));
    setBaseUrl(next.base_url);
    setPolicyPrompt(next.policy_prompt ?? "");
    setUsagePriority(next.usage_priority);
    setAllowUpshift(next.allow_upshift);
    setPreferContinuation(next.prefer_continuation);
    setPreferIdle(next.prefer_idle);
    setJudgedReview(next.judged_review);
    setKeyInput("");
    setAnalysisKeyInput("");
    setAvailableModels([]);
    autoDiscoverKey.current = "";
    autoFilledModel.current = "";
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspace?.id]);

  const draft: RoutingSettings = useMemo(
    () => ({
      enabled,
      model,
      judge_enabled: judgeEnabled,
      analysis: {
        enabled: analysisEnabled,
        model: analysisModel,
        base_url: analysisBaseUrl,
        source: analysisSource,
        runtime_id: analysisRuntimeId,
        thinking_level: analysisThinkingLevel,
      },
      confidence_threshold: normalizeThreshold(Number(threshold)),
      stale_review_hours: normalizeStaleReviewHours(Number(staleHours)),
      base_url: baseUrl,
      policy_prompt: policyPrompt,
      usage_priority: usagePriority,
      allow_upshift: allowUpshift,
      prefer_continuation: preferContinuation,
      prefer_idle: preferIdle,
      judged_review: judgedReview,
    }),
    [
      enabled,
      model,
      judgeEnabled,
      analysisEnabled,
      analysisModel,
      analysisBaseUrl,
      analysisSource,
      analysisRuntimeId,
      analysisThinkingLevel,
      threshold,
      staleHours,
      baseUrl,
      policyPrompt,
      usagePriority,
      allowUpshift,
      preferContinuation,
      preferIdle,
      judgedReview,
    ],
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
      a.judge_enabled === b.judge_enabled &&
      a.analysis.enabled === b.analysis.enabled &&
      a.analysis.model.trim() === b.analysis.model.trim() &&
      a.analysis.base_url.trim() === b.analysis.base_url.trim() &&
      a.analysis.source === b.analysis.source &&
      (a.analysis.runtime_id ?? "").trim() === (b.analysis.runtime_id ?? "").trim() &&
      (a.analysis.thinking_level ?? "low") === (b.analysis.thinking_level ?? "low") &&
      a.confidence_threshold === b.confidence_threshold &&
      a.stale_review_hours === b.stale_review_hours &&
      a.base_url.trim() === b.base_url.trim() &&
      (a.policy_prompt ?? "").trim() === (b.policy_prompt ?? "").trim() &&
      a.usage_priority === b.usage_priority &&
      a.allow_upshift === b.allow_upshift &&
      a.prefer_continuation === b.prefer_continuation &&
      a.prefer_idle === b.prefer_idle &&
      a.judged_review === b.judged_review,
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
  const judgeHealth = roleHealth(health.data, "judge");
  const analysisHealth = roleHealth(health.data, "analysis");
  useEffect(() => {
    // The catalog is the judge endpoint's: that is the endpoint the model
    // box above it is sent to.
    const healthData = judgeHealth;
    if (
      !workspace ||
      !canManage ||
      !enabled ||
      !judgeEnabled ||
      !healthData?.gateway_host ||
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
    judgeEnabled,
    judgeHealth,
    model,
    saved.base_url,
    workspace,
  ]);

  const recheck = useMutation({
    mutationFn: () => api.checkRoutingHealth(workspace?.id ?? ""),
    onSuccess: (next) => {
      qc.setQueryData(workspaceKeys.routingHealth(workspace?.id ?? ""), next);
    },
    onError: (error) =>
      toast.error(error instanceof Error && error.message ? error.message : t(($) => $.routing.health_recheck_failed)),
  });

  // The key write. Explicit rather than auto-saved (see keyInput), and it
  // sends the rest of the draft along because the server replaces the whole
  // settings column — sending the key alone would revert an unsaved model or
  // endpoint edit made in the same sitting.
  const saveKey = useMutation({
    mutationFn: async ({ role, key }: { role: RoutingRole; key: string }) => {
      if (!workspace) return;
      const updated = await api.updateWorkspace(workspace.id, {
        settings:
          role === "judge"
            ? withRoutingSettings(workspace.settings, draft, key)
            : withRoutingSettings(workspace.settings, draft, undefined, key),
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
    // The box is emptied either way (below), so a failure must say so —
    // otherwise an empty box reads as a stored key.
    onError: (error) =>
      toast.error(error instanceof Error && error.message ? error.message : t(($) => $.routing.gateway_key_save_error)),
    // Cleared whichever way it went: on success the key is stored and there
    // is nothing to show, and on failure leaving a credential in a text box
    // behind a red message is not something to do to somebody.
    onSettled: (_data, _err, { role }) =>
      role === "judge" ? setKeyInput("") : setAnalysisKeyInput(""),
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

          <SettingsRow
            label={t(($) => $.routing.usage_priority_label)}
            description={t(($) => $.routing.usage_priority_description)}
          >
            <Switch
              checked={usagePriority}
              disabled={!canManage || !enabled}
              onCheckedChange={setUsagePriority}
              aria-label={t(($) => $.routing.usage_priority_label)}
            />
          </SettingsRow>

          <SettingsRow
            label={t(($) => $.routing.allow_upshift_label)}
            description={t(($) => $.routing.allow_upshift_description)}
          >
            <Switch
              // Upshift picks by usage, so it has nothing to go on while
              // usage priority is off.
              checked={allowUpshift && usagePriority}
              disabled={!canManage || !enabled || !usagePriority}
              onCheckedChange={setAllowUpshift}
              aria-label={t(($) => $.routing.allow_upshift_label)}
            />
          </SettingsRow>

          <SettingsRow
            label={t(($) => $.routing.continuation_label)}
            description={
              preferContinuation
                ? t(($) => $.routing.continuation_description_on)
                : t(($) => $.routing.continuation_description_shadow)
            }
          >
            <Switch
              checked={preferContinuation}
              disabled={!canManage || !enabled}
              onCheckedChange={setPreferContinuation}
              aria-label={t(($) => $.routing.continuation_label)}
            />
          </SettingsRow>

          <SettingsRow
            label={t(($) => $.routing.load_label)}
            description={
              preferIdle
                ? t(($) => $.routing.load_description_on)
                : t(($) => $.routing.load_description_shadow)
            }
          >
            <Switch
              checked={preferIdle}
              disabled={!canManage || !enabled}
              onCheckedChange={setPreferIdle}
              aria-label={t(($) => $.routing.load_label)}
            />
          </SettingsRow>

        </SettingsCard>
        <p
          className="px-0.5 text-caption leading-5 text-muted-foreground"
          data-mode={routingMode(draft)}
        >
          {t(($) => $.routing.modes[routingMode(draft)])}
        </p>
      </SettingsSection>

      <RuleTableSection table={health.data?.rules} />

      <SettingsSection
        title={t(($) => $.routing.analysis_title)}
        description={t(($) => $.routing.analysis_description)}
      >
        <SettingsCard>
          <SettingsRow label={t(($) => $.routing.analysis_enabled_label)}>
            <Switch
              checked={analysisEnabled}
              disabled={!canManage}
              onCheckedChange={setAnalysisEnabled}
              aria-label={t(($) => $.routing.analysis_enabled_label)}
            />
          </SettingsRow>
          {analysisSource === "api_gateway" ? <SettingsRow
            label={t(($) => $.routing.analysis_model_label)}
            description={t(($) => $.routing.analysis_model_description)}
            size="text"
          >
            <Input
              value={analysisModel}
              disabled={!canManage || !analysisEnabled}
              placeholder={t(($) => $.routing.model_placeholder)}
              onChange={(e) => setAnalysisModel(e.target.value)}
              aria-label={t(($) => $.routing.analysis_model_label)}
            />
          </SettingsRow> : null}
          <SettingsRow label={t(($) => $.routing.analysis_source_label)} description={t(($) => $.routing.analysis_source_description)} size="text">
            <select
              className="h-9 w-full rounded-md border border-border bg-background px-3 text-sm"
              value={analysisSource}
              disabled={!canManage || !analysisEnabled}
              onChange={(e) => setAnalysisSource(e.target.value as typeof analysisSource)}
              aria-label={t(($) => $.routing.analysis_source_label)}
            >
              <option value="api_gateway">{t(($) => $.routing.analysis_source_api_gateway)}</option>
              <option value="runtime_subscription">{t(($) => $.routing.analysis_source_runtime)}</option>
            </select>
          </SettingsRow>
          {analysisSource === "runtime_subscription" ? (
            <>
              <RuntimePicker runtimes={runtimes} runtimesLoading={runtimesLoading} members={members} currentUserId={userId} selectedRuntimeId={analysisRuntimeId} onSelect={setAnalysisRuntimeId} disabled={!canManage || !analysisEnabled} />
              <ModelDropdown runtimeId={analysisRuntime?.id ?? null} runtimeOnline={analysisRuntime?.status === "online"} value={analysisModel} onChange={setAnalysisModel} disabled={!canManage || !analysisEnabled || !analysisRuntime} />
              <ThinkingSettingField label={t(($) => $.routing.analysis_thinking_label)} runtimeId={analysisRuntime?.id ?? null} runtimeOnline={analysisRuntime?.status === "online"} provider={analysisRuntime?.provider ?? ""} model={analysisModel} value={analysisThinkingLevel} canEdit={canManage && analysisEnabled && !!analysisRuntime} onChange={setAnalysisThinkingLevel} />
            </>
          ) : null}
          {analysisSource === "api_gateway" ? <EndpointRows
            urlLabel={t(($) => $.routing.analysis_url_label)}
            keyLabel={t(($) => $.routing.analysis_key_label)}
            baseUrl={analysisBaseUrl}
            onBaseUrl={setAnalysisBaseUrl}
            keyInput={analysisKeyInput}
            onKeyInput={setAnalysisKeyInput}
            endpoint={analysisHealth}
            health={health.data}
            canManage={canManage}
            saving={saveKey.isPending}
            onSaveKey={(key) => saveKey.mutate({ role: "analysis", key })}
          /> : null}
        </SettingsCard>
        <RoleEndpointNote health={health.data} role={analysisHealth} />
        <GatewayPairNote
          baseUrl={analysisBaseUrl}
          keyStored={analysisHealth?.gateway_key_set === true}
        />
      </SettingsSection>

      <SettingsSection
        title={t(($) => $.routing.judge_title)}
        description={t(($) => $.routing.judge_description)}
      >
        <SettingsCard>
          <SettingsRow label={t(($) => $.routing.judge_enabled_label)}>
            <Switch
              checked={judgeEnabled}
              disabled={!canManage}
              onCheckedChange={setJudgeEnabled}
              aria-label={t(($) => $.routing.judge_enabled_label)}
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
              disabled={!canManage || !judgeEnabled}
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

          <EndpointRows
            urlLabel={t(($) => $.routing.gateway_url_label)}
            urlDescription={t(($) => $.routing.gateway_url_description)}
            keyLabel={t(($) => $.routing.gateway_key_label)}
            baseUrl={baseUrl}
            onBaseUrl={setBaseUrl}
            keyInput={keyInput}
            onKeyInput={setKeyInput}
            endpoint={judgeHealth}
            health={health.data}
            canManage={canManage}
            saving={saveKey.isPending}
            onSaveKey={(key) => saveKey.mutate({ role: "judge", key })}
          />
        </SettingsCard>
        <GatewayNote health={health.data} role={judgeHealth} />
        <GatewayPairNote
          baseUrl={baseUrl}
          keyStored={judgeHealth?.gateway_key_set === true}
        />
      </SettingsSection>

      <SettingsSection
        title={t(($) => $.routing.policy_label)}
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

      <RoutingDomainsSection wsId={workspace?.id ?? ""} canManage={canManage} />

      <SettingsSection
        title={t(($) => $.routing.seats_title)}
        description={t(($) => $.routing.filters_placeholder)}
      >
        <SettingsCard>
          <RoutingSeatsTable wsId={workspace?.id ?? ""} canManage={canManage} />
        </SettingsCard>
      </SettingsSection>
    </SettingsTab>
  );
}

/**
 * One role's endpoint pair: URL with the ordinary fields, key behind its own
 * button. Shared by both roles so the two cards cannot drift apart.
 */
function EndpointRows({
  urlLabel,
  urlDescription,
  keyLabel,
  baseUrl,
  onBaseUrl,
  keyInput,
  onKeyInput,
  endpoint,
  health,
  canManage,
  saving,
  onSaveKey,
}: {
  urlLabel: string;
  urlDescription?: string;
  keyLabel: string;
  baseUrl: string;
  onBaseUrl: (v: string) => void;
  keyInput: string;
  onKeyInput: (v: string) => void;
  endpoint?: RoutingRoleHealth;
  health?: RoutingHealth;
  canManage: boolean;
  saving: boolean;
  onSaveKey: (key: string) => void;
}) {
  const { t } = useT("settings");
  const keySet = endpoint?.gateway_key_set === true;
  const unstorable = health?.workspace_key_storable === false;
  return (
    <>
      <SettingsRow
        label={urlLabel}
        description={urlDescription ?? t(($) => $.routing.analysis_url_description)}
        size="text"
      >
        <Input
          value={baseUrl}
          disabled={!canManage}
          placeholder={t(($) => $.routing.gateway_url_placeholder)}
          onChange={(e) => onBaseUrl(e.target.value)}
          aria-label={urlLabel}
        />
      </SettingsRow>

      <SettingsRow
        label={keyLabel}
        description={
          unstorable
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
            disabled={!canManage || unstorable || saving}
            // The placeholder is the whole readback: the stored key never
            // leaves the server, so "a key is saved" is the most this box
            // can honestly say.
            placeholder={
              keySet
                ? t(($) => $.routing.gateway_key_stored)
                : t(($) => $.routing.gateway_key_placeholder)
            }
            onChange={(e) => onKeyInput(e.target.value)}
            aria-label={keyLabel}
          />
          <Button
            type="button"
            size="sm"
            disabled={!canManage || keyInput.trim() === "" || saving}
            onClick={() => onSaveKey(keyInput)}
            className="shrink-0"
          >
            {t(($) => $.routing.gateway_key_save)}
          </Button>
          {keySet && canManage ? (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              disabled={saving}
              onClick={() => onSaveKey("")}
              className="shrink-0"
            >
              {t(($) => $.routing.gateway_key_clear)}
            </Button>
          ) : null}
        </div>
      </SettingsRow>
    </>
  );
}

/** Where the analysis model's calls go. */
function RoleEndpointNote({
  health,
  role,
}: {
  health?: RoutingHealth;
  role?: RoutingRoleHealth;
}) {
  const { t } = useT("settings");
  if (!health || !role?.gateway_host) return null;
  return (
    <p className="px-0.5 text-caption leading-5 text-muted-foreground">
      {role.gateway_scope === "workspace"
        ? t(($) => $.routing.gateway_endpoint_workspace, { host: role.gateway_host })
        : t(($) => $.routing.gateway_endpoint_deployment, { host: role.gateway_host })}
    </p>
  );
}

/**
 * The rule table that sets the tier (DENE-1677), read-only: it ships with the
 * server, so there is nothing here to edit. Hidden on a backend that predates
 * it rather than shown empty.
 */
function RuleTableSection({ table }: { table: RoutingRuleTable | undefined }) {
  const { t } = useT("settings");
  // `?.`: a report that skipped the parser (a mocked client) has no table.
  if (!table?.rules?.length) return null;
  return (
    <SettingsSection
      title={t(($) => $.routing.rules_title)}
      description={t(($) => $.routing.rules_description)}
    >
      <SettingsCard>
        {table.rules.map((rule) => (
          <SettingsRow
            key={rule.id}
            label={rule.label}
            description={ruleCondition(rule, table, {
              anyUnknown: t(($) => $.routing.rules_any_unknown),
              catchAll: t(($) => $.routing.rules_catch_all),
            })}
          >
            <span className="whitespace-nowrap text-body text-muted-foreground">
              {rule.reviewer === "none"
                ? t(($) => $.routing.rules_no_review, { tier: rule.tier_label })
                : t(($) => $.routing.rules_review, { tier: rule.tier_label })}
            </span>
          </SettingsRow>
        ))}
      </SettingsCard>
    </SettingsSection>
  );
}

/** A row's condition in the questions' own words, in question order. */
export function ruleCondition(
  rule: RoutingRule,
  table: RoutingRuleTable,
  words: { anyUnknown: string; catchAll: string },
): string {
  if (rule.any_unknown) return words.anyUnknown;
  const parts = table.questions.flatMap((q) => {
    const values = rule.when[q.key];
    if (!values?.length) return [];
    const labels = values.map((v) => q.options.find((o) => o.value === v)?.label ?? v);
    return [`${q.label}：${labels.join(" / ")}`];
  });
  return parts.length ? parts.join(" · ") : words.catchAll;
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
function GatewayNote({
  health,
  role,
}: {
  health?: RoutingHealth;
  role?: RoutingRoleHealth;
}) {
  const { t } = useT("settings");
  if (health && health.gateway_configured === false && !role?.gateway_host) {
    return (
      <p className="px-0.5 text-caption leading-5 text-destructive">
        {t(($) => $.routing.gateway_unset)}
      </p>
    );
  }
  const host = role?.gateway_host ?? "";
  const scoped =
    role?.gateway_scope === "workspace"
      ? t(($) => $.routing.gateway_endpoint_workspace, { host })
      : t(($) => $.routing.gateway_endpoint_deployment, { host });
  return (
    <div className="flex flex-col gap-1 px-0.5">
      {host ? (
        <p className="text-caption leading-5 text-muted-foreground">{scoped}</p>
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
