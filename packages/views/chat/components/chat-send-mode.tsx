"use client";

import { useState, type SyntheticEvent } from "react";
import { ChevronDown, Loader2 } from "lucide-react";
import type { ChatSendMode } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import {
  Sheet,
  SheetContent,
  SheetTitle,
} from "@multica/ui/components/ui/sheet";
import { useIsMobile } from "@multica/ui/hooks/use-mobile";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

const MODES: ChatSendMode[] = ["steer", "queue", "restart"];

/** The mode Enter uses while a reply is running: steer when the CLI can take it. */
export function defaultChatSendMode(steerSupported: boolean | undefined): ChatSendMode {
  return steerSupported ? "steer" : "queue";
}

/** A chosen mode that is no longer available falls back to the default. */
export function effectiveChatSendMode(
  chosen: ChatSendMode | null,
  steerSupported: boolean | undefined,
): ChatSendMode {
  if (chosen === "steer" && !steerSupported) return "queue";
  return chosen ?? defaultChatSendMode(steerSupported);
}

function cliName(provider: string): string {
  return provider ? provider.charAt(0).toUpperCase() + provider.slice(1) : provider;
}

/** Keep the composer focused so tapping the main button does not drop the keyboard. */
function keepFocusInComposer(event: SyntheticEvent<HTMLButtonElement>) {
  event.preventDefault();
}

interface ChatSendModeButtonProps {
  mode: ChatSendMode;
  steerSupported?: boolean;
  steerProvider?: string;
  /** "restart" when steering stops the CLI and resumes its session. */
  steerMode?: string;
  /** False while the draft is empty or a send is in flight. */
  canSend: boolean;
  loading?: boolean;
  onModeChange: (mode: ChatSendMode) => void;
  onSend: (mode: ChatSendMode) => void;
}

/**
 * Split send button shown while a reply is running (DENE-1346): the main half
 * sends with the current mode, ▾ picks another. Picking a row with a draft
 * sends it right away; with an empty draft it only sets the mode. Desktop gets
 * a menu, phones a bottom sheet with 44px rows.
 */
export function ChatSendModeButton({
  mode,
  steerSupported,
  steerProvider,
  steerMode,
  canSend,
  loading,
  onModeChange,
  onSend,
}: ChatSendModeButtonProps) {
  const { t } = useT("chat");
  const isMobile = useIsMobile();
  const [sheetOpen, setSheetOpen] = useState(false);

  const label = (m: ChatSendMode) =>
    m === "steer"
      ? t(($) => $.input.mode_steer)
      : m === "queue"
        ? t(($) => $.input.mode_queue)
        : t(($) => $.input.mode_restart);
  const description = (m: ChatSendMode) =>
    m === "steer"
      ? steerMode === "restart"
        ? t(($) => $.input.mode_steer_restart_desc)
        : t(($) => $.input.mode_steer_desc)
      : m === "queue"
        ? t(($) => $.input.mode_queue_desc)
        : t(($) => $.input.mode_restart_desc);
  const processLine = (m: ChatSendMode) =>
    m === "steer"
      ? steerMode === "restart"
        ? t(($) => $.input.mode_steer_restart_process)
        : t(($) => $.input.mode_steer_process)
      : m === "queue"
        ? t(($) => $.input.mode_queue_process)
        : t(($) => $.input.mode_restart_process);
  const steerReason = steerProvider
    ? t(($) => $.input.mode_steer_unsupported, { cli: cliName(steerProvider) })
    : t(($) => $.input.mode_steer_unsupported_generic);

  const pick = (next: ChatSendMode) => {
    if (next === "steer" && !steerSupported) return;
    onModeChange(next);
    setSheetOpen(false);
    if (canSend) onSend(next);
  };

  const rowText = (m: ChatSendMode) => {
    const disabled = m === "steer" && !steerSupported;
    return (
      <span className="flex min-w-0 flex-col gap-0.5 text-left">
        <span>{label(m)}</span>
        <span className="text-caption text-muted-foreground">
          {disabled ? steerReason : description(m)}
        </span>
        {!disabled && (
          <span className="text-caption text-muted-foreground">{processLine(m)}</span>
        )}
      </span>
    );
  };

  const trigger = (
    <Button
      type="button"
      size="icon-sm"
      variant="default"
      className="w-6 rounded-l-none rounded-r-full border-l border-primary-foreground/20"
      aria-label={t(($) => $.input.mode_menu)}
      onClick={isMobile ? () => setSheetOpen(true) : undefined}
    >
      <ChevronDown aria-hidden="true" />
    </Button>
  );

  return (
    <div className="flex items-center" data-slot="chat-send-mode">
      <Button
        type="button"
        size="sm"
        className="h-7 rounded-l-full rounded-r-none pl-3 pr-2"
        disabled={!canSend || loading}
        aria-busy={loading || undefined}
        onPointerDown={keepFocusInComposer}
        onMouseDown={keepFocusInComposer}
        onClick={() => onSend(mode)}
      >
        {loading ? <Loader2 className="animate-spin" aria-hidden="true" /> : label(mode)}
      </Button>
      {isMobile ? (
        <>
          {trigger}
          <Sheet open={sheetOpen} onOpenChange={setSheetOpen}>
            <SheetContent side="bottom" showCloseButton={false} className="gap-0 px-2 pt-2 pb-[max(0.5rem,env(safe-area-inset-bottom))]">
              <SheetTitle className="sr-only">{t(($) => $.input.mode_menu)}</SheetTitle>
              {MODES.map((m) => {
                const disabled = m === "steer" && !steerSupported;
                return (
                  <button
                    key={m}
                    type="button"
                    disabled={disabled}
                    aria-pressed={m === mode}
                    onClick={() => pick(m)}
                    className={cn(
                      "flex min-h-11 w-full items-center rounded-md px-3 py-2 text-body",
                      m === mode && "bg-accent",
                      disabled && "opacity-60",
                    )}
                  >
                    {rowText(m)}
                  </button>
                );
              })}
            </SheetContent>
          </Sheet>
        </>
      ) : (
        <DropdownMenu>
          <DropdownMenuTrigger render={trigger} />
          <DropdownMenuContent side="top" align="end" className="w-72">
            <DropdownMenuRadioGroup
              value={mode}
              onValueChange={(value) => pick(value as ChatSendMode)}
            >
              {MODES.map((m) => (
                <DropdownMenuRadioItem
                  key={m}
                  value={m}
                  disabled={m === "steer" && !steerSupported}
                  className="items-start py-1.5"
                >
                  {rowText(m)}
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>
      )}
    </div>
  );
}
