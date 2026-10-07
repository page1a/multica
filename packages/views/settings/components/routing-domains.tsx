"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import type { Domain } from "@multica/core/types";
import {
  domainListOptions,
  useCreateDomain,
  useDeleteDomain,
  useRenameDomain,
} from "@multica/core/domains";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../../i18n";
import { SettingsCard, SettingsRow, SettingsSection } from "./settings-layout";

/**
 * The workspace domain list (DENE-1451): projects and specialisations pick
 * their domain from here. A domain still in use cannot be deleted.
 */
export function RoutingDomainsSection({ wsId, canManage }: { wsId: string; canManage: boolean }) {
  const { t } = useT("common");
  const { data: domains = [] } = useQuery(domainListOptions(wsId));
  const create = useCreateDomain(wsId);
  const [adding, setAdding] = useState(false);
  const [newName, setNewName] = useState("");

  const fail = (err: unknown) =>
    toast.error(err instanceof Error ? err.message : t(($) => $.domain.save_failed));

  const add = () => {
    const name = newName.trim();
    if (!name) return;
    create.mutate(name, {
      onSuccess: () => {
        setNewName("");
        setAdding(false);
      },
      onError: fail,
    });
  };

  return (
    <SettingsSection
      title={t(($) => $.domain.settings_title)}
      description={t(($) => $.domain.settings_desc)}
      action={
        canManage && !adding ? (
          <Button type="button" variant="ghost" size="sm" onClick={() => setAdding(true)}>
            + {t(($) => $.domain.add)}
          </Button>
        ) : null
      }
    >
      <SettingsCard>
        {domains.map((d) => (
          <DomainRow key={d.id} wsId={wsId} domain={d} canManage={canManage} onError={fail} />
        ))}
        {adding ? (
          <form
            className="flex items-center gap-2 px-4 py-3"
            onSubmit={(e) => {
              e.preventDefault();
              add();
            }}
          >
            <Input
              autoFocus
              value={newName}
              placeholder={t(($) => $.domain.name_placeholder)}
              aria-label={t(($) => $.domain.name_placeholder)}
              onChange={(e) => setNewName(e.target.value)}
              className="flex-1"
            />
            <Button type="submit" size="sm" disabled={!newName.trim() || create.isPending}>
              {t(($) => $.save)}
            </Button>
            <Button type="button" variant="ghost" size="sm" onClick={() => setAdding(false)}>
              {t(($) => $.cancel)}
            </Button>
          </form>
        ) : null}
      </SettingsCard>
    </SettingsSection>
  );
}

function DomainRow({
  wsId,
  domain,
  canManage,
  onError,
}: {
  wsId: string;
  domain: Domain;
  canManage: boolean;
  onError: (err: unknown) => void;
}) {
  const { t } = useT("common");
  const rename = useRenameDomain(wsId);
  const remove = useDeleteDomain(wsId);
  const [editing, setEditing] = useState(false);
  const [name, setName] = useState(domain.name);
  const inUse = domain.project_count > 0 || domain.agent_count > 0;

  const usage =
    domain.project_count > 0 && domain.agent_count > 0
      ? t(($) => $.domain.usage_both, { projects: domain.project_count, agents: domain.agent_count })
      : domain.agent_count > 0
        ? t(($) => $.domain.usage_no_projects, { agents: domain.agent_count })
        : domain.project_count > 0
          ? t(($) => $.domain.usage_no_agents, { projects: domain.project_count })
          : t(($) => $.domain.usage_none);

  if (editing) {
    return (
      <form
        className="flex items-center gap-2 px-4 py-3"
        onSubmit={(e) => {
          e.preventDefault();
          const next = name.trim();
          if (!next || next === domain.name) {
            setEditing(false);
            return;
          }
          rename.mutate({ id: domain.id, name: next }, { onSuccess: () => setEditing(false), onError });
        }}
      >
        <Input
          autoFocus
          value={name}
          aria-label={t(($) => $.domain.name_placeholder)}
          onChange={(e) => setName(e.target.value)}
          className="flex-1"
        />
        <Button type="submit" size="sm" disabled={!name.trim() || rename.isPending}>
          {t(($) => $.save)}
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={() => {
            setName(domain.name);
            setEditing(false);
          }}
        >
          {t(($) => $.cancel)}
        </Button>
      </form>
    );
  }

  return (
    <SettingsRow label={domain.name} description={usage}>
      {canManage ? (
        <div className="flex items-center gap-1">
          <Button type="button" variant="ghost" size="sm" onClick={() => setEditing(true)}>
            {t(($) => $.domain.rename)}
          </Button>
          {/* A disabled button swallows hover, so the reason sits on a wrapper. */}
          <span title={inUse ? t(($) => $.domain.delete_in_use) : undefined}>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              disabled={inUse || remove.isPending}
              onClick={() => remove.mutate(domain.id, { onError })}
            >
              {t(($) => $.domain.delete)}
            </Button>
          </span>
        </div>
      ) : null}
    </SettingsRow>
  );
}
