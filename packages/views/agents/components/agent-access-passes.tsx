"use client";

import { useMemo, useState } from "react";
import { Loader2, Ticket } from "lucide-react";
import type { Agent, MemberWithUser } from "@multica/core/types";
import {
  agentAccessPassExpiry,
  type AgentAccessPassPreset,
  useAgentAccessPasses,
  useCreateAgentAccessPass,
  useRevokeAgentAccessPass,
} from "@multica/core/agents";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Switch } from "@multica/ui/components/ui/switch";
import { toast } from "sonner";
import { ActorAvatar } from "../../common/actor-avatar";
import { useT } from "../../i18n";
import {
  formatAccessExpiry,
  localDateTimeInputValue,
} from "../../inbox/components/agent-access-format";
import { SettingsCard, SettingsRow, SettingsSection } from "../../settings/components/settings-layout";

/**
 * Owner-only borrowing controls for one agent (DENE-808): the doorbell switch
 * and the timed passes currently letting other members invoke it.
 *
 * Passes are server data (the server clock decides when one lapses), so this
 * reads them through TanStack Query and never caches them client-side.
 */
export function AgentAccessPasses({
  agent,
  members,
  canEdit,
  onUpdate,
}: {
  agent: Agent;
  members: MemberWithUser[];
  canEdit: boolean;
  onUpdate: (id: string, data: Record<string, unknown>) => Promise<void>;
}) {
  const { t } = useT("agents");
  const passes = useAgentAccessPasses(agent.id, canEdit);
  const create = useCreateAgentAccessPass(agent.id);
  const revoke = useRevokeAgentAccessPass(agent.id);

  const [doorbellSaving, setDoorbellSaving] = useState(false);
  const [userId, setUserId] = useState<string>("");
  const [preset, setPreset] = useState<AgentAccessPassPreset>("2h");
  const [custom, setCustom] = useState<string>(() => localDateTimeInputValue(2 * 60 * 60 * 1000));

  const candidates = useMemo(
    () => members.filter((m) => m.user_id !== agent.owner_id),
    [members, agent.owner_id],
  );
  const active = useMemo(() => (passes.data ?? []).filter((p) => p.active), [passes.data]);

  if (!canEdit) return null;

  const toggleDoorbell = async (checked: boolean) => {
    setDoorbellSaving(true);
    try {
      await onUpdate(agent.id, { doorbell_enabled: checked });
    } catch {
      toast.error(t(($) => $.access.doorbell_save_failed));
    } finally {
      setDoorbellSaving(false);
    }
  };

  const issue = async () => {
    if (!userId) {
      toast.error(t(($) => $.access.pass_member_required));
      return;
    }
    const expiry = agentAccessPassExpiry(preset, new Date(custom));
    if (!expiry) {
      toast.error(t(($) => $.access.pass_expiry_invalid));
      return;
    }
    try {
      const pass = await create.mutateAsync({ user_id: userId, ...expiry });
      toast.success(
        t(($) => $.access.pass_issued, {
          name: pass.user_name,
          until: formatAccessExpiry(pass.expires_at),
        }),
      );
      setUserId("");
    } catch {
      toast.error(t(($) => $.access.pass_issue_failed));
    }
  };

  const presetLabel = (p: AgentAccessPassPreset) =>
    p === "2h"
      ? t(($) => $.access.pass_2h)
      : p === "today"
        ? t(($) => $.access.pass_today)
        : t(($) => $.access.pass_custom);

  return (
    <SettingsSection title={t(($) => $.access.borrow_section_title)}>
      <SettingsCard>
        <SettingsRow
          label={t(($) => $.access.doorbell_title)}
          description={t(($) => $.access.doorbell_desc)}
        >
          <Switch
            checked={agent.doorbell_enabled === true}
            disabled={doorbellSaving}
            onCheckedChange={(checked) => void toggleDoorbell(checked)}
            aria-label={t(($) => $.access.doorbell_title)}
            data-testid="doorbell-switch"
          />
        </SettingsRow>
        <SettingsRow
          label={t(($) => $.access.pass_issue_title)}
          description={t(($) => $.access.pass_issue_desc)}
          size="none"
          align="start"
        >
          <div className="flex flex-wrap items-center gap-2">
            <Select
              items={candidates.map((m) => ({ value: m.user_id, label: m.name }))}
              value={userId || null}
              onValueChange={(next: string | null) => setUserId(next ?? "")}
            >
              <SelectTrigger size="sm" className="min-w-40" aria-label={t(($) => $.access.pass_member_label)}>
                <SelectValue>
                  {candidates.find((m) => m.user_id === userId)?.name ??
                    t(($) => $.access.pass_member_placeholder)}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                {candidates.map((m) => (
                  <SelectItem key={m.user_id} value={m.user_id}>
                    {m.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {(["2h", "today", "custom"] as const).map((p) => (
              <Button
                key={p}
                type="button"
                size="xs"
                variant={preset === p ? "secondary" : "outline"}
                aria-pressed={preset === p}
                onClick={() => setPreset(p)}
              >
                {presetLabel(p)}
              </Button>
            ))}
            {preset === "custom" ? (
              <Input
                type="datetime-local"
                className="h-7 w-auto text-caption"
                value={custom}
                min={localDateTimeInputValue(60_000)}
                aria-label={t(($) => $.access.pass_custom)}
                onChange={(e) => setCustom(e.target.value)}
              />
            ) : null}
            <Button
              size="sm"
              disabled={create.isPending || candidates.length === 0}
              data-testid="issue-pass"
              onClick={() => void issue()}
            >
              {create.isPending ? <Loader2 className="size-3.5 animate-spin" /> : <Ticket className="size-3.5" />}
              {t(($) => $.access.pass_issue_action)}
            </Button>
          </div>
        </SettingsRow>
        <div className="px-4 py-3.5">
          <div className="text-body font-medium">{t(($) => $.access.pass_active_title)}</div>
          {passes.isLoading ? (
            <div className="mt-2 text-caption text-muted-foreground">…</div>
          ) : active.length === 0 ? (
            <div className="mt-2 text-caption text-muted-foreground" data-testid="passes-empty">
              {t(($) => $.access.pass_active_empty)}
            </div>
          ) : (
            <ul className="mt-2 flex flex-col divide-y" data-testid="passes-list">
              {active.map((pass) => (
                <li key={pass.id} className="flex items-center gap-3 py-2">
                  <ActorAvatar
                    actorType="member"
                    actorId={pass.user_id}
                    name={pass.user_name}
                    avatarUrl={pass.user_avatar_url}
                    size="xs"
                  />
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-body">{pass.user_name}</div>
                    <div className="text-caption text-muted-foreground">
                      {t(($) => $.access.pass_until, { until: formatAccessExpiry(pass.expires_at) })}
                    </div>
                  </div>
                  <Button
                    size="xs"
                    variant="outline"
                    disabled={revoke.isPending}
                    data-testid={`revoke-pass-${pass.id}`}
                    onClick={() => {
                      revoke.mutate(pass.id, {
                        onSuccess: () => toast.success(t(($) => $.access.pass_revoked)),
                        onError: () => toast.error(t(($) => $.access.pass_revoke_failed)),
                      });
                    }}
                  >
                    {t(($) => $.access.pass_revoke)}
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </div>
      </SettingsCard>
    </SettingsSection>
  );
}
