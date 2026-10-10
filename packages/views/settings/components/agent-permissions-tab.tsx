"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Input } from "@multica/ui/components/ui/input";
import { Switch } from "@multica/ui/components/ui/switch";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { useAuthStore } from "@multica/core/auth";
import { api } from "@multica/core/api";
import { useCurrentWorkspace } from "@multica/core/paths";
import { memberListOptions, workspaceKeys } from "@multica/core/workspace/queries";
import {
  agentSpawnOptions,
  useUpdateAgentSpawn,
  type AgentSpawnCell,
  type AgentSpawnPolicy,
} from "@multica/core/workspace/agent-spawn";
import type { Workspace } from "@multica/core/types";
import { useT } from "../../i18n";
import { SettingsCard, SettingsRow, SettingsSection, SettingsTab } from "./settings-layout";
import { useAutoSave } from "./use-auto-save";
import {
  AUTOMATION_LIMIT_PRESETS,
  automationLimitsEqual,
  parseAutomationLimits,
  parseLimitInput,
  type AutomationLimits,
} from "./automation-limits";

type SpawnKey = keyof AgentSpawnPolicy;

/**
 * Blank means unlimited (0); junk keeps the previous count. A required count
 * has no unlimited: blank or below 1 keeps the previous one.
 */
export function parseSpawnCount(text: string, previous: number, required = false): number {
  if (text.trim() === "") return required ? previous : 0;
  const parsed = Number.parseInt(text, 10);
  if (!Number.isFinite(parsed) || parsed < (required ? 1 : 0)) return previous;
  return Math.min(parsed, 1000);
}

/**
 * What agents may create while running, and how long they may run. The
 * table governs agent runs only; people are never limited by it.
 */
export function AgentPermissionsTab() {
  const { t } = useT("settings");
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const user = useAuthStore((s) => s.user);
  const { data: members = [] } = useQuery({ ...memberListOptions(wsId), enabled: !!wsId });
  const role = members.find((m) => m.user_id === user?.id)?.role;
  const canManage = role === "owner" || role === "admin";
  const { data: policy } = useQuery(agentSpawnOptions(wsId));
  const update = useUpdateAgentSpawn(wsId);
  const [confirmOff, setConfirmOff] = useState(false);

  const save = (key: SpawnKey, patch: Partial<AgentSpawnCell>) =>
    update.mutate(
      { [key]: patch },
      {
        onSuccess: () => toast.success(t(($) => $.agent_permissions.saved), { id: "settings-auto-save" }),
        onError: (error) =>
          toast.error(error instanceof Error ? error.message : t(($) => $.agent_permissions.save_failed)),
      },
    );

  const spawnRows: { key: Exclude<SpawnKey, "consult">; label: string; description: string; perChat: boolean }[] = [
    {
      key: "chat_issue",
      label: t(($) => $.agent_permissions.chat_issue_label),
      description: t(($) => $.agent_permissions.chat_issue_description),
      perChat: false,
    },
    {
      key: "issue_issue",
      label: t(($) => $.agent_permissions.issue_issue_label),
      description: t(($) => $.agent_permissions.issue_issue_description),
      perChat: false,
    },
    {
      key: "chat_chat",
      label: t(($) => $.agent_permissions.chat_chat_label),
      description: t(($) => $.agent_permissions.chat_chat_description),
      perChat: true,
    },
  ];

  return (
    <SettingsTab
      title={t(($) => $.page.tabs.agent_permissions)}
      description={t(($) => $.agent_permissions.description)}
    >
      <SettingsSection anchor="create" title={t(($) => $.agent_permissions.create_title)}>
        <SettingsCard>
          {spawnRows.map((row) => (
            <SpawnRow
              key={row.key}
              anchor={row.key.replace("_", "-")}
              label={row.label}
              description={row.description}
              cell={policy?.[row.key]}
              showPerChat={row.perChat}
              disabled={!canManage || !policy}
              onToggle={(enabled) => {
                if (!enabled && row.key === "issue_issue") {
                  setConfirmOff(true);
                  return;
                }
                save(row.key, { enabled });
              }}
              onCount={(patch) => save(row.key, patch)}
            />
          ))}
          <SettingsRow
            anchor="issue-chat"
            label={<span className="text-muted-foreground">{t(($) => $.agent_permissions.issue_chat_label)}</span>}
            description={t(($) => $.agent_permissions.issue_chat_description)}
          >
            <Switch checked={false} disabled aria-label={t(($) => $.agent_permissions.issue_chat_label)} />
          </SettingsRow>
        </SettingsCard>
      </SettingsSection>

      <SettingsSection anchor="consult" title={t(($) => $.agent_permissions.consult_title)}>
        <SettingsCard>
          <SettingsRow
            anchor="consult"
            label={t(($) => $.agent_permissions.consult_label)}
            description={t(($) => $.agent_permissions.consult_description)}
            size="text"
          >
            <div className="flex flex-wrap items-center gap-x-4 gap-y-2 sm:justify-end">
              <CountInput
                label={t(($) => $.agent_permissions.per_issue)}
                value={policy?.consult?.per_issue ?? 3}
                disabled={!canManage || !policy || !policy.consult?.enabled}
                required
                onCommit={(per_issue) => save("consult", { per_issue })}
              />
              <Switch
                checked={policy?.consult?.enabled ?? false}
                disabled={!canManage || !policy}
                aria-label={`${t(($) => $.agent_permissions.consult_label)} · ${t(($) => $.agent_permissions.enabled)}`}
                onCheckedChange={(enabled) => save("consult", { enabled })}
              />
            </div>
          </SettingsRow>
        </SettingsCard>
      </SettingsSection>

      {workspace && <RunLimitsSection key={workspace.id} workspace={workspace} canManage={canManage} />}

      <AlertDialog open={confirmOff} onOpenChange={setConfirmOff}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.agent_permissions.issue_issue_off_title)}</AlertDialogTitle>
            <AlertDialogDescription>{t(($) => $.agent_permissions.issue_issue_off_description)}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t(($) => $.agent_permissions.cancel)}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                save("issue_issue", { enabled: false });
                setConfirmOff(false);
              }}
            >
              {t(($) => $.agent_permissions.confirm_off)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsTab>
  );
}

function SpawnRow({
  anchor,
  label,
  description,
  cell,
  showPerChat,
  disabled,
  onToggle,
  onCount,
}: {
  anchor: string;
  label: string;
  description: string;
  cell: AgentSpawnCell | undefined;
  showPerChat: boolean;
  disabled: boolean;
  onToggle: (enabled: boolean) => void;
  onCount: (patch: Partial<AgentSpawnCell>) => void;
}) {
  const { t } = useT("settings");
  const enabled = cell?.enabled ?? false;
  const countsDisabled = disabled || !enabled;
  return (
    <SettingsRow anchor={anchor} label={label} description={description} size="text">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 sm:justify-end">
        {showPerChat && (
          <CountInput
            label={t(($) => $.agent_permissions.per_chat)}
            value={cell?.per_chat ?? 0}
            disabled={countsDisabled}
            onCommit={(per_chat) => onCount({ per_chat })}
          />
        )}
        <CountInput
          label={t(($) => $.agent_permissions.per_run)}
          value={cell?.per_run ?? 0}
          disabled={countsDisabled}
          onCommit={(per_run) => onCount({ per_run })}
        />
        <Switch
          checked={enabled}
          disabled={disabled}
          aria-label={`${label} · ${t(($) => $.agent_permissions.enabled)}`}
          onCheckedChange={onToggle}
        />
      </div>
    </SettingsRow>
  );
}

function CountInput({
  label,
  value,
  disabled,
  required = false,
  onCommit,
}: {
  label: string;
  value: number;
  disabled: boolean;
  required?: boolean;
  onCommit: (value: number) => void;
}) {
  const { t } = useT("settings");
  const [draft, setDraft] = useState(value === 0 ? "" : String(value));
  // A save elsewhere replaces an untouched draft.
  useEffect(() => setDraft(value === 0 ? "" : String(value)), [value]);
  return (
    <label className="flex items-center gap-2 text-caption text-muted-foreground">
      {label}
      <Input
        type="number"
        inputMode="numeric"
        min={required ? 1 : 0}
        step={1}
        aria-label={label}
        value={draft}
        placeholder={required ? undefined : t(($) => $.agent_permissions.unlimited)}
        disabled={disabled}
        className="w-20"
        onChange={(event) => setDraft(event.target.value)}
        onBlur={() => {
          const next = parseSpawnCount(draft, value, required);
          setDraft(next === 0 ? "" : String(next));
          if (next !== value) onCommit(next);
        }}
        onKeyDown={(event) => {
          if (event.key === "Enter") event.currentTarget.blur();
        }}
      />
    </label>
  );
}

/** Moved here from General; still stored in workspace.settings. */
function RunLimitsSection({ workspace, canManage }: { workspace: Workspace; canManage: boolean }) {
  const { t } = useT("settings");
  const qc = useQueryClient();
  const savedLimits = useMemo(() => parseAutomationLimits(workspace.settings), [workspace.settings]);
  const [limits, setLimits] = useState<AutomationLimits>(savedLimits);
  const autoSave = useAutoSave({
    value: limits,
    savedValue: savedLimits,
    onSave: async (next) => {
      const updated = await api.updateWorkspace(workspace.id, {
        settings: { ...(workspace.settings ?? {}), ...next },
      });
      qc.setQueryData(workspaceKeys.list(), (old: Workspace[] | undefined) =>
        old?.map((ws) => (ws.id === updated.id ? updated : ws)),
      );
    },
    onSuccess: () => toast.success(t(($) => $.agent_permissions.saved), { id: "settings-auto-save" }),
    onError: (error) =>
      toast.error(error instanceof Error ? error.message : t(($) => $.agent_permissions.save_failed)),
    enabled: canManage,
    isEqual: automationLimitsEqual,
  });

  const rows: {
    key: keyof AutomationLimits;
    anchor: string;
    label: string;
    description: string;
    unit: string;
  }[] = [
    {
      key: "agent_chain_budget",
      anchor: "chain-budget",
      label: t(($) => $.agent_permissions.chain_budget_label),
      description: t(($) => $.agent_permissions.chain_budget_description),
      unit: t(($) => $.agent_permissions.chain_budget_unit),
    },
    {
      key: "agent_task_timeout_minutes",
      anchor: "timeout",
      label: t(($) => $.agent_permissions.timeout_label),
      description: t(($) => $.agent_permissions.timeout_description),
      unit: t(($) => $.agent_permissions.timeout_unit),
    },
  ];

  return (
    <SettingsSection anchor="run-limits" title={t(($) => $.agent_permissions.limits_title)}>
      <SettingsCard>
        {rows.map((row) => {
          const unlimited = limits[row.key] === 0;
          return (
            <SettingsRow
              key={row.key}
              anchor={row.anchor}
              label={row.label}
              description={row.description}
              size="text"
            >
              <div className="flex flex-wrap items-center gap-x-4 gap-y-2 sm:justify-end">
                <div className="flex items-center gap-2">
                  <Input
                    type="number"
                    inputMode="numeric"
                    min={1}
                    step={1}
                    aria-label={row.label}
                    value={unlimited ? "" : String(limits[row.key])}
                    placeholder="∞"
                    onChange={(event) =>
                      setLimits((current) => ({
                        ...current,
                        [row.key]: parseLimitInput(event.target.value, current[row.key]),
                      }))
                    }
                    onBlur={autoSave.flush}
                    disabled={!canManage || unlimited}
                    className="w-20"
                  />
                  <span className="text-caption text-muted-foreground">{row.unit}</span>
                </div>
                <label className="flex items-center gap-2 text-caption text-muted-foreground">
                  <Switch
                    checked={unlimited}
                    onCheckedChange={(checked) =>
                      setLimits((current) => ({
                        ...current,
                        [row.key]: checked ? 0 : AUTOMATION_LIMIT_PRESETS[row.key],
                      }))
                    }
                    disabled={!canManage}
                    aria-label={`${row.label} · ${t(($) => $.agent_permissions.unlimited)}`}
                  />
                  {t(($) => $.agent_permissions.unlimited)}
                </label>
              </div>
            </SettingsRow>
          );
        })}
      </SettingsCard>
    </SettingsSection>
  );
}
