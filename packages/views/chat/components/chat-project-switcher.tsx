"use client";

import { useEffect, useMemo, useState } from "react";
import {
  Command,
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@multica/ui/components/ui/command";
import type { ChatProjectFilter } from "@multica/core/chat/project-bar";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import { useT } from "../../i18n";

/**
 * Searchable project jump list. Recent chats come first (the caller orders
 * them); All and the no-project view stay pinned at the top so leaving a
 * project is one keystroke, not a search.
 */
export function ChatProjectSwitcher({
  open,
  onOpenChange,
  projects,
  filter,
  onSelect,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  projects: readonly { id: string; title: string }[];
  filter: ChatProjectFilter;
  onSelect: (filter: ChatProjectFilter) => void;
}) {
  const { t } = useT("chat");
  const [query, setQuery] = useState("");

  useEffect(() => {
    if (open) setQuery("");
  }, [open]);

  const allLabel = t(($) => $.project_bar.all);
  const noneLabel = t(($) => $.project_bar.none);
  const rows = useMemo(() => {
    const fixed: { key: string; title: string; filter: ChatProjectFilter }[] = [
      { key: "all", title: allLabel, filter: { type: "all" } },
      { key: "none", title: noneLabel, filter: { type: "none" } },
      ...projects.map((project) => ({
        key: project.id,
        title: project.title,
        filter: { type: "project" as const, id: project.id },
      })),
    ];
    const text = query.trim().toLowerCase();
    if (!text) return fixed;
    return fixed.filter(
      (row) => row.title.toLowerCase().includes(text) || matchesPinyin(row.title, text),
    );
  }, [allLabel, noneLabel, projects, query]);

  const currentKey =
    filter.type === "project" ? filter.id : filter.type === "none" ? "none" : "all";

  const pick = (next: ChatProjectFilter) => {
    onSelect(next);
    onOpenChange(false);
  };

  return (
    <CommandDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t(($) => $.project_bar.switch)}
      description={t(($) => $.project_bar.switch_search)}
    >
      <Command shouldFilter={false}>
        <CommandInput
          placeholder={t(($) => $.project_bar.switch_search)}
          value={query}
          onValueChange={setQuery}
          aria-label={t(($) => $.project_bar.switch_search)}
        />
        <CommandList>
          {rows.length === 0 ? (
            <CommandEmpty>{t(($) => $.project_bar.switch_empty)}</CommandEmpty>
          ) : (
            <CommandGroup>
              {rows.map((row) => (
                <CommandItem
                  key={row.key}
                  value={row.key}
                  data-checked={row.key === currentKey ? true : undefined}
                  onSelect={() => pick(row.filter)}
                >
                  <span className="truncate">{row.title}</span>
                </CommandItem>
              ))}
            </CommandGroup>
          )}
        </CommandList>
      </Command>
    </CommandDialog>
  );
}
