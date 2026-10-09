"use client";

import { useState } from "react";
import { FolderKanban } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { projectListOptions } from "@multica/core/projects/queries";
import {
  PICKER_TRIGGER_CLASS,
  PickerEmpty,
  PickerItem,
  PropertyPicker,
} from "../../../issues/components/pickers/property-picker";
import { matchesPinyin } from "../../../editor/extensions/pinyin-match";
import { ProjectIcon } from "../../../projects/components/project-icon";
import { useT } from "../../../i18n";

/**
 * The projects automatic dispatch is limited to (DENE-1648). Empty means
 * every project. Each toggle saves; the menu stays open so several projects
 * can be picked in one go.
 */
export function DispatchProjectsField({
  wsId,
  value,
  canEdit,
  onChange,
}: {
  wsId: string;
  value: string[] | undefined;
  canEdit: boolean;
  onChange: (next: string[]) => Promise<void> | void;
}) {
  const { t } = useT("agents");
  const { data: projects = [] } = useQuery(projectListOptions(wsId));
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState("");
  // The clicked set while a write is in flight, so rows tick immediately.
  const [pending, setPending] = useState<string[] | null>(null);
  const saved = value ?? [];
  const selected = pending ?? saved;
  // A deleted project can linger in the saved list; it is shown as nothing.
  const known = projects.filter((p) => selected.includes(p.id));

  const save = async (next: string[]) => {
    if (!canEdit) return;
    setPending(next);
    try {
      await onChange(next);
    } finally {
      setPending(null);
    }
  };
  const toggle = (id: string) =>
    void save(selected.includes(id) ? selected.filter((x) => x !== id) : [...selected, id]);

  const query = filter.trim().toLowerCase();
  const filtered = projects.filter(
    (p) => p.title.toLowerCase().includes(query) || matchesPinyin(p.title, query),
  );

  const first = known[0];
  const triggerLabel = !first
    ? t(($) => $.inspector.dispatch_projects_all)
    : known.length === 1
      ? first.title
      : t(($) => $.inspector.dispatch_projects_some, { first: first.title, count: known.length });

  return (
    <PropertyPicker
      open={canEdit && open}
      onOpenChange={(next) => setOpen(canEdit && next)}
      width="w-60"
      align="end"
      searchable
      searchPlaceholder={t(($) => $.inspector.dispatch_projects_search)}
      onSearchChange={setFilter}
      triggerRender={
        <button
          type="button"
          disabled={!canEdit}
          aria-label={t(($) => $.inspector.prop_dispatch_projects)}
          className={`${PICKER_TRIGGER_CLASS} max-w-48 text-caption disabled:cursor-not-allowed`}
        />
      }
      trigger={
        <>
          {first ? (
            <ProjectIcon project={first} size="sm" />
          ) : (
            <FolderKanban className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          )}
          <span className="truncate">{triggerLabel}</span>
        </>
      }
    >
      <PickerItem emptyValue selected={known.length === 0} onClick={() => void save([])}>
        <FolderKanban className="h-3.5 w-3.5 text-muted-foreground" />
        <span className="text-muted-foreground">{t(($) => $.inspector.dispatch_projects_all)}</span>
      </PickerItem>
      {filtered.map((p) => (
        <PickerItem key={p.id} selected={selected.includes(p.id)} onClick={() => toggle(p.id)}>
          <ProjectIcon project={p} size="sm" />
          <span className="truncate">{p.title}</span>
        </PickerItem>
      ))}
      {projects.length === 0 && (
        <div className="px-2 py-1.5 text-caption text-muted-foreground">
          {t(($) => $.inspector.dispatch_projects_empty)}
        </div>
      )}
      {projects.length > 0 && filtered.length === 0 && query && <PickerEmpty />}
    </PropertyPicker>
  );
}
