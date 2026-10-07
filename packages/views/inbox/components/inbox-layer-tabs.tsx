"use client";

import { useWorkspacePaths } from "@multica/core/paths";
import { cn } from "@multica/ui/lib/utils";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { ACTIVITY_LAYER_PARAM, LAYER_PARAM } from "./inbox-view";

/**
 * Compact-width switch between the inbox's two layers. Wide screens show the
 * board beside the list, so only phones need a visible way across. Switching
 * replaces the entry, so Back leaves the inbox instead of toggling layers.
 */
export function InboxLayerTabs({ active }: { active: "board" | "activity" }) {
  const { t } = useT("inbox");
  const wsPaths = useWorkspacePaths();
  const { replace } = useNavigation();
  const tabs = [
    { key: "board" as const, label: t(($) => $.board.back_to_board), href: wsPaths.inbox() },
    {
      key: "activity" as const,
      label: t(($) => $.board.activity_link),
      href: `${wsPaths.inbox()}?${LAYER_PARAM}=${ACTIVITY_LAYER_PARAM}`,
    },
  ];

  return (
    <div role="tablist" className="flex shrink-0 border-b px-2" data-slot="inbox-layer-tabs">
      {tabs.map((tab) => {
        const selected = tab.key === active;
        return (
          <button
            key={tab.key}
            type="button"
            role="tab"
            aria-selected={selected}
            className={cn(
              "-mb-px flex-1 border-b-2 py-2.5 text-body",
              selected
                ? "border-foreground font-medium text-foreground"
                : "border-transparent text-muted-foreground",
            )}
            onClick={() => {
              if (!selected) replace(tab.href);
            }}
          >
            {tab.label}
          </button>
        );
      })}
    </div>
  );
}
