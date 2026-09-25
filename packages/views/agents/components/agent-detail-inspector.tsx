"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import type {
  Agent,
  AgentRuntime,
  MemberWithUser,
} from "@multica/core/types";
import {
  AGENT_DESCRIPTION_MAX_LENGTH,
  AGENT_MAX_CONCURRENT_TASKS_MAX,
  AGENT_MAX_CONCURRENT_TASKS_MIN,
  isAgentAutoRetryEnabled,
  isAgentWorkEnabled,
} from "@multica/core/agents";
import {
  isRuntimeUsableForUser,
  runtimeModelsOptions,
} from "@multica/core/runtimes";
import { isImeComposing } from "@multica/core/utils";
import { Input } from "@multica/ui/components/ui/input";
import { Switch } from "@multica/ui/components/ui/switch";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { AvatarUploadControl } from "../../common/avatar-upload-control";
import {
  SettingsCard,
  SettingsRow,
  SettingsSaveState,
  SettingsSection,
} from "../../settings/components/settings-layout";
import { useAutoSave } from "../../settings/components/use-auto-save";
import { useT } from "../../i18n";
import { CharCounter } from "./char-counter";
import { ModelPicker } from "./inspector/model-picker";
import {
  buildModelChangeUpdate,
  type ModelCatalog,
} from "./inspector/model-change-cleanup";
import { RuntimePicker } from "./inspector/runtime-picker";
import { ThinkingSettingField } from "./inspector/thinking-prop-row";
import { ServiceTierSettingField } from "./inspector/service-tier-setting-field";
import { RoutingTierSettingField } from "./inspector/routing-tier-setting-field";
import {
  canEditRuntimeProfile,
  runtimeInheritanceState,
} from "../specialization";

interface InspectorProps {
  agent: Agent;
  runtime: AgentRuntime | null;
  runtimes: AgentRuntime[];
  members: MemberWithUser[];
  currentUserId: string | null;
  canEdit: boolean;
  onUpdate: (id: string, data: Record<string, unknown>) => Promise<void>;
}

interface ProfileDraft {
  name: string;
  description: string;
}

function profileDraftsEqual(left: ProfileDraft, right: ProfileDraft) {
  return left.name === right.name && left.description === right.description;
}

/**
 * Full-width General settings form. Every editable value is presented as an
 * explicit field; compact inspector chips are used only through their
 * settings-field variants, where the whole control is a visible click target.
 */
export function AgentDetailInspector({
  agent,
  runtime,
  runtimes,
  members,
  currentUserId,
  canEdit,
  onUpdate,
}: InspectorProps) {
  const { t } = useT("agents");
  const { t: ts } = useT("settings");
  const update = useCallback(
    (data: Record<string, unknown>) => onUpdate(agent.id, data),
    [agent.id, onUpdate],
  );

  const [name, setName] = useState(agent.name);
  const [description, setDescription] = useState(agent.description ?? "");

  useEffect(() => {
    setName(agent.name);
    setDescription(agent.description ?? "");
    // Reset only when moving to another agent. Cache updates from this form
    // must not erase a newer local draft while an autosave is in flight.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [agent.id]);

  const profileDraft = useMemo(
    () => ({ name: name.trim(), description }),
    [description, name],
  );
  const savedProfile = useMemo(
    () => ({
      name: agent.name,
      description: agent.description ?? "",
    }),
    [agent.description, agent.name],
  );
  const saveProfile = useCallback(
    async (next: ProfileDraft) => {
      await update({ name: next.name, description: next.description });
    },
    [update],
  );
  const profileAutoSave = useAutoSave({
    value: profileDraft,
    savedValue: savedProfile,
    onSave: saveProfile,
    enabled:
      canEdit &&
      profileDraft.name.length > 0 &&
      profileDraft.description.length <= AGENT_DESCRIPTION_MAX_LENGTH,
    isEqual: profileDraftsEqual,
  });

  const isOnline = runtime?.status === "online";
  const canReadRuntime =
    runtime != null && isRuntimeUsableForUser(runtime, currentUserId);
  const canDiscoverRuntimeModels = isOnline && canReadRuntime;
  const nameInvalid = name.trim().length === 0;

  // Runtime inheritance (DENE-505). `unknown` covers both a base role — which
  // cannot follow anything — and a backend that predates the flag; neither may
  // be offered a switch. While following, the runtime-profile controls below
  // stay visible (they are the values this agent actually runs with) but
  // read-only: the server 400s a runtime field sent alongside
  // `runtime_inherited: true`, so the toggle is the only honest way out.
  const runtimeInheritance = runtimeInheritanceState(agent);
  const runtimeInherited = runtimeInheritance === "inherited";
  const canEditRuntime = canEditRuntimeProfile(agent, canEdit);
  const [inheritanceSaving, setInheritanceSaving] = useState(false);
  const setRuntimeInheritance = useCallback(
    async (inherited: boolean) => {
      setInheritanceSaving(true);
      try {
        // The switch is driven by server state and the page patches the cache
        // optimistically, then rolls it back and toasts on failure — so a
        // refused write (a base role, a private base runtime) simply leaves the
        // switch where it was. Not re-thrown: nothing here can recover.
        await update({ runtime_inherited: inherited });
      } catch {
        // Handled by the page (`handleUpdate`).
      } finally {
        setInheritanceSaving(false);
      }
    },
    [update],
  );

  // Same query the Thinking / Speed fields already use, so switching model
  // costs no extra request. `null` = not authoritative (offline runtime, still
  // loading, or discovery failed) and must not trigger any clearing.
  const modelsQuery = useQuery(
    runtimeModelsOptions(
      canDiscoverRuntimeModels ? agent.runtime_id : null,
      agent.id,
    ),
  );
  const modelCatalog = useMemo<ModelCatalog>(
    () =>
      modelsQuery.isSuccess
        ? modelsQuery.data.supported
          ? modelsQuery.data.models
          : []
        : null,
    [modelsQuery.data, modelsQuery.isSuccess],
  );
  const handleModelChange = useCallback(
    (model: string) =>
      update(
        buildModelChangeUpdate({
          provider: runtime?.provider ?? "",
          model,
          thinkingLevel: agent.thinking_level ?? "",
          serviceTier: agent.service_tier ?? "",
          catalog: modelCatalog,
        }),
      ),
    [agent.service_tier, agent.thinking_level, modelCatalog, runtime?.provider, update],
  );

  return (
    <div className="space-y-8">
      <SettingsSection
        title={t(($) => $.inspector.section_profile)}
        action={
          <SettingsSaveState
            status={profileAutoSave.status}
            savingLabel={ts(($) => $.auto_save.saving)}
            savedLabel={ts(($) => $.auto_save.saved)}
            errorLabel={ts(($) => $.auto_save.failed)}
          />
        }
      >
        <SettingsCard>
          <SettingsRow
            label={t(($) => $.inspector.avatar_label)}
            size="none"
          >
            <div className="flex justify-start sm:justify-end">
              <AvatarUploadControl
                variant="agent"
                value={agent.avatar_url ?? null}
                name={agent.name}
                size={56}
                disabled={!canEdit}
                onUploaded={(url) => update({ avatar_url: url })}
                onEmojiSelected={(value) => update({ avatar_url: value })}
              />
            </div>
          </SettingsRow>

          <SettingsRow
            label={t(($) => $.inspector.name_label)}
            size="text"
          >
            <div>
              <Input
                type="text"
                name="agent-name"
                autoComplete="off"
                aria-label={t(($) => $.inspector.name_label)}
                value={name}
                onChange={(event) => setName(event.target.value)}
                onBlur={profileAutoSave.flush}
                disabled={!canEdit}
                aria-invalid={nameInvalid || undefined}
              />
              {nameInvalid ? (
                <p className="mt-1 text-caption text-destructive">
                  {t(($) => $.inspector.rename_required)}
                </p>
              ) : null}
            </div>
          </SettingsRow>

          <SettingsRow
            label={t(($) => $.inspector.description_label)}
            size="text"
            align="start"
          >
            <div>
              <Textarea
                name="agent-description"
                autoComplete="off"
                aria-label={t(($) => $.inspector.description_label)}
                value={description}
                onChange={(event) => setDescription(event.target.value)}
                onBlur={profileAutoSave.flush}
                disabled={!canEdit}
                rows={5}
                maxLength={AGENT_DESCRIPTION_MAX_LENGTH}
                className="resize-y"
                placeholder={t(($) => $.inspector.description_placeholder)}
              />
              <CharCounter
                length={[...description].length}
                max={AGENT_DESCRIPTION_MAX_LENGTH}
              />
            </div>
          </SettingsRow>
        </SettingsCard>
      </SettingsSection>

      <SettingsSection
        title={t(($) => $.inspector.section_execution)}
      >
        <SettingsCard>
          {runtimeInheritance !== "unknown" && (
            <SettingsRow
              label={t(($) => $.inspector.prop_runtime_inherit)}
              description={
                runtimeInherited
                  ? t(($) => $.inspector.prop_runtime_inherit_hint_on)
                  : t(($) => $.inspector.prop_runtime_inherit_hint_off)
              }
            >
              <Switch
                checked={runtimeInherited}
                disabled={!canEdit || inheritanceSaving}
                onCheckedChange={(checked) => {
                  void setRuntimeInheritance(checked);
                }}
                aria-label={t(($) => $.inspector.prop_runtime_inherit)}
              />
            </SettingsRow>
          )}
          <SettingsRow
            label={t(($) => $.inspector.prop_runtime)}
            size="select-wide"
          >
            <RuntimePicker
              variant="field"
              showLabel={false}
              value={agent.runtime_id}
              runtimes={runtimes}
              members={members}
              currentUserId={currentUserId}
              canEdit={canEditRuntime}
              // Model, thinking level, and service tier are runtime/model
              // native. Clear them together so the new runtime resolves its
              // own defaults instead of inheriting incompatible tokens.
              onChange={(id) =>
                update({
                  runtime_id: id,
                  model: "",
                  thinking_level: "",
                  service_tier: "",
                })
              }
            />
          </SettingsRow>
          <SettingsRow
            label={t(($) => $.inspector.prop_model)}
            size="select-wide"
          >
            <ModelPicker
              variant="field"
              showLabel={false}
              runtimeId={agent.runtime_id}
              runtimeOnline={canDiscoverRuntimeModels}
              agentId={agent.id}
              value={agent.model ?? ""}
              canEdit={canEditRuntime}
              onChange={handleModelChange}
            />
          </SettingsRow>
          <ThinkingSettingField
            label={t(($) => $.inspector.prop_thinking)}
            runtimeId={agent.runtime_id}
            runtimeOnline={canDiscoverRuntimeModels}
            agentId={agent.id}
            provider={runtime?.provider ?? ""}
            model={agent.model ?? ""}
            value={agent.thinking_level ?? ""}
            canEdit={canEditRuntime}
            onChange={(thinkingLevel) =>
              update({ thinking_level: thinkingLevel })
            }
          />
          <ServiceTierSettingField
            label={t(($) => $.inspector.prop_speed)}
            runtimeId={agent.runtime_id}
            runtimeOnline={canDiscoverRuntimeModels}
            agentId={agent.id}
            provider={runtime?.provider ?? ""}
            model={agent.model ?? ""}
            value={agent.service_tier ?? ""}
            canEdit={canEditRuntime}
            onChange={(serviceTier) => update({ service_tier: serviceTier })}
          />
          <RoutingTierSettingField
            label={t(($) => $.inspector.prop_routing_tier)}
            description={t(($) => $.inspector.prop_routing_tier_hint)}
            value={agent.routing_tier ?? ""}
            canEdit={canEdit}
            onChange={(routingTier) => update({ routing_tier: routingTier })}
          />
          <SettingsRow
            label={t(($) => $.inspector.prop_concurrency)}
            size="select-wide"
          >
            <ConcurrencyField
              value={agent.max_concurrent_tasks}
              canEdit={canEdit}
              onSave={(next) => update({ max_concurrent_tasks: next })}
            />
          </SettingsRow>
          <SettingsRow
            label={t(($) => $.inspector.prop_work_enabled)}
            description={
              isAgentWorkEnabled(agent) || !agent.work_pause
                ? t(($) => $.inspector.prop_work_enabled_hint)
                : agent.work_pause.reason === "balance_exhausted"
                  ? t(($) => $.inspector.prop_work_pause_balance)
                  : t(($) => $.inspector.prop_work_pause_quota)
            }
          >
            <Switch
              checked={isAgentWorkEnabled(agent)}
              disabled={!canEdit}
              onCheckedChange={(checked) => {
                void update({ work_enabled: checked });
              }}
              aria-label={t(($) => $.inspector.prop_work_enabled)}
            />
          </SettingsRow>
          <SettingsRow
            label={t(($) => $.inspector.prop_auto_retry)}
            description={t(($) => $.inspector.prop_auto_retry_hint)}
          >
            <Switch
              checked={isAgentAutoRetryEnabled(agent)}
              disabled={!canEdit}
              onCheckedChange={(checked) => {
                void update({ auto_retry_enabled: checked });
              }}
              aria-label={t(($) => $.inspector.prop_auto_retry)}
            />
          </SettingsRow>
        </SettingsCard>
      </SettingsSection>
    </div>
  );
}

function ConcurrencyField({
  value,
  canEdit,
  onSave,
}: {
  value: number;
  canEdit: boolean;
  onSave: (next: number) => Promise<void>;
}) {
  const { t } = useT("agents");
  const [draft, setDraft] = useState(String(value));

  useEffect(() => setDraft(String(value)), [value]);

  const commit = () => {
    const next = Number(draft);
    if (
      !Number.isInteger(next) ||
      next < AGENT_MAX_CONCURRENT_TASKS_MIN ||
      next > AGENT_MAX_CONCURRENT_TASKS_MAX
    ) {
      setDraft(String(value));
      return;
    }
    if (next !== value) void onSave(next);
  };

  return (
    <div>
      <Input
        id="agent-concurrency"
        type="number"
        name="agent-concurrency"
        autoComplete="off"
        inputMode="numeric"
        min={AGENT_MAX_CONCURRENT_TASKS_MIN}
        max={AGENT_MAX_CONCURRENT_TASKS_MAX}
        value={draft}
        onChange={(event) => setDraft(event.target.value)}
        onBlur={commit}
        onKeyDown={(event) => {
          if (isImeComposing(event)) return;
          if (event.key === "Enter") {
            event.preventDefault();
            commit();
          }
        }}
        disabled={!canEdit}
        aria-label={t(($) => $.inspector.prop_concurrency)}
        className="font-mono tabular-nums"
      />
      <p className="mt-1 text-caption text-muted-foreground">
        {t(($) => $.pickers.concurrency_range, {
          min: AGENT_MAX_CONCURRENT_TASKS_MIN,
          max: AGENT_MAX_CONCURRENT_TASKS_MAX,
        })}
      </p>
    </div>
  );
}
