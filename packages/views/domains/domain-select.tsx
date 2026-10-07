"use client";

import { useState } from "react";
import { Check, Compass } from "lucide-react";
import { Popover, PopoverTrigger, PopoverContent } from "@multica/ui/components/ui/popover";
import { Sheet, SheetContent, SheetTitle } from "@multica/ui/components/ui/sheet";
import { useIsMobile } from "@multica/ui/hooks/use-mobile";
import { cn } from "@multica/ui/lib/utils";
import { PICKER_TRIGGER_CLASS } from "../issues/components/pickers/property-picker";
import { useGuestReadOnly } from "../layout/guest-readonly";
import { useT } from "../i18n";

export interface DomainOption {
  /** Domain id; "" is 通用 (no domain). */
  id: string;
  name: string;
  /** Set when the option cannot be picked; shown as the row's reason. */
  disabledReason?: string;
}

/**
 * Domain choice for projects (several), issues and specialisations (one each).
 * A popover on desktop, a bottom sheet on phones so the list never clips.
 */
export function DomainSelect({
  options,
  selected,
  multiple = false,
  header,
  onChange,
  disabled = false,
  triggerClassName,
}: {
  options: DomainOption[];
  selected: string[];
  multiple?: boolean;
  header: string;
  onChange: (next: string[]) => void;
  disabled?: boolean;
  triggerClassName?: string;
}) {
  const { t } = useT("common");
  const isMobile = useIsMobile();
  const { isGuest } = useGuestReadOnly();
  const [open, setOpen] = useState(false);
  const locked = disabled || isGuest;

  const names = selected
    .map((id) => options.find((o) => o.id === id)?.name)
    .filter((n): n is string => Boolean(n));
  const label = names.length > 0 ? names.join("、") : t(($) => $.domain.generic);

  const pick = (id: string) => {
    if (!multiple) {
      onChange(id ? [id] : []);
      setOpen(false);
      return;
    }
    onChange(selected.includes(id) ? selected.filter((s) => s !== id) : [...selected, id]);
  };

  const list = (
    <div role="listbox" aria-multiselectable={multiple || undefined} className="p-1 max-h-72 overflow-y-auto">
      {options.map((o) => {
        const isSelected = o.id === "" ? selected.length === 0 : selected.includes(o.id);
        return (
          <button
            key={o.id || "generic"}
            type="button"
            role="option"
            aria-selected={isSelected}
            disabled={Boolean(o.disabledReason)}
            onClick={() => pick(o.id)}
            className="flex w-full min-h-11 md:min-h-8 items-center gap-2 rounded-sm px-2 text-left text-body hover:bg-accent disabled:cursor-not-allowed disabled:opacity-50 disabled:hover:bg-transparent"
          >
            <span className="flex-1 truncate">{o.name}</span>
            {o.disabledReason && (
              <span className="shrink-0 text-caption text-muted-foreground">{o.disabledReason}</span>
            )}
            {isSelected && <Check className="size-3.5 shrink-0" />}
          </button>
        );
      })}
    </div>
  );
  const head = <div className="border-b px-3 py-2 text-caption text-muted-foreground">{header}</div>;

  const triggerContent = (
    <>
      <Compass className="size-3.5 shrink-0 text-muted-foreground" />
      <span className={cn("truncate", names.length === 0 && "text-muted-foreground")}>{label}</span>
    </>
  );
  const triggerClass = cn(
    triggerClassName ?? PICKER_TRIGGER_CLASS,
    locked && "cursor-not-allowed opacity-60",
  );

  if (isMobile) {
    return (
      <>
        <button type="button" className={triggerClass} disabled={locked} onClick={() => setOpen(true)}>
          {triggerContent}
        </button>
        <Sheet open={open} onOpenChange={setOpen}>
          <SheetContent side="bottom" showCloseButton={false} className="gap-0 px-2 pt-2">
            <SheetTitle className="sr-only">{t(($) => $.domain.label)}</SheetTitle>
            {head}
            {list}
          </SheetContent>
        </Sheet>
      </>
    );
  }

  return (
    <Popover open={locked ? false : open} onOpenChange={(next) => !locked && setOpen(next)}>
      <PopoverTrigger className={triggerClass} disabled={locked}>
        {triggerContent}
      </PopoverTrigger>
      <PopoverContent align="start" className="w-56 gap-0 p-0">
        {head}
        {list}
      </PopoverContent>
    </Popover>
  );
}
