"use client";

import type { Agent, MemberWithUser } from "@multica/core/types";
import {
  SettingsCard,
  SettingsSection,
} from "../../settings/components/settings-layout";
import { useT } from "../../i18n";
import { AccessPicker } from "./inspector/access-picker";
import { AgentAccessPasses } from "./agent-access-passes";

export function AgentAccessSettings({
  agent,
  members,
  currentUserId,
  onDirtyChange,
  onUpdate,
}: {
  agent: Agent;
  members: MemberWithUser[];
  currentUserId: string | null;
  onDirtyChange?: (dirty: boolean) => void;
  onUpdate: (id: string, data: Record<string, unknown>) => Promise<void>;
}) {
  const { t } = useT("agents");
  const canEdit = currentUserId !== null && agent.owner_id === currentUserId;

  return (
    <>
    <SettingsSection
      title={t(($) => $.access.section_title)}
    >
      <SettingsCard>
        <AccessPicker
          permissionMode={agent.permission_mode}
          invocationTargets={agent.invocation_targets}
          visibility={agent.visibility}
          members={members}
          ownerId={agent.owner_id}
          canEdit={canEdit}
          hasComposioAllowlist={
            (agent.composio_toolkit_allowlist ?? []).length > 0
          }
          onDirtyChange={onDirtyChange}
          onChange={(next) => onUpdate(agent.id, next)}
        />
      </SettingsCard>
    </SettingsSection>
    <AgentAccessPasses
      agent={agent}
      members={members}
      canEdit={canEdit}
      onUpdate={onUpdate}
    />
    </>
  );
}
