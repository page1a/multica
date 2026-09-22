"use client";

import { useState } from "react";
import { EyeOff, Layers } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import type { Agent } from "@multica/core/types";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { workspaceKeys } from "@multica/core/workspace/queries";
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
import { useT } from "../../i18n";
import { ActorAvatar } from "../../common/actor-avatar";
import { solidifyTargets } from "../specialization";

/**
 * The escape hatch behind the archive refusal (DENE-301/304).
 *
 * Archiving a base role while specialisations hang off it is refused by the
 * server with `agent_has_children`, because those children would keep pointing
 * at a hidden parent. The refusal is not a dead end: solidifying each child
 * bakes the base role's *current* prompt into the child's own `instructions`
 * and detaches it, which is exactly what the child was running with a moment
 * earlier. Only then does the archive go through.
 *
 * Solidify is per-child and its refusals are meaningful (the caller may not be
 * able to read the parent's prompt, the parent may have no prompt to fold in),
 * so a partial failure is reported as such instead of as one opaque error —
 * the children that WERE solidified are already detached, and the parent stays
 * unarchived because the guard is still in force for the rest.
 *
 * The list is driven by the refusal's own names (DENE-384) rather than by the
 * caller's agent list: the server counts every active child, including ones
 * the caller cannot see, so a list built from visible rows only could show
 * fewer rows than the guard is actually blocking on.
 */
export function SolidifyUnbindDialog({
  parent,
  childAgents,
  serverChildNames = [],
  onClose,
  onArchived,
}: {
  parent: Agent;
  /** Active specialisations of `parent`, already resolved by the caller. */
  childAgents: readonly Agent[];
  /**
   * Child names as the archive refusal reported them, when the caller caught
   * one. Empty means the server named none (an older backend) — the dialog
   * then lists exactly the rows it was handed.
   */
  serverChildNames?: readonly string[];
  onClose: () => void;
  /** Runs after the archive succeeded, so callers can navigate or refetch. */
  onArchived?: () => void;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const [working, setWorking] = useState(false);

  const targets = solidifyTargets(childAgents, serverChildNames);
  const hiddenCount = targets.filter((target) => !target.agent).length;
  const names = targets.map((target) => target.name).join(", ");

  const handleConfirm = async () => {
    setWorking(true);
    let failed: Agent | null = null;
    let failureMessage = "";
    for (const child of childAgents) {
      try {
        await api.solidifyAgent(child.id);
      } catch (error) {
        failed = child;
        failureMessage =
          error instanceof Error ? error.message : String(error ?? "");
        break;
      }
    }
    if (failed) {
      await qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
      toast.error(
        t(($) => $.specialization.archive_block_failed_toast, {
          name: failed.name,
          error: failureMessage,
        }),
      );
      setWorking(false);
      return;
    }
    try {
      await api.archiveAgent(parent.id);
    } catch (error) {
      await qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.row_actions.archive_failed_toast),
      );
      setWorking(false);
      return;
    }
    await qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
    toast.success(
      t(($) => $.specialization.archive_block_solidified_toast, {
        count: childAgents.length,
        name: parent.name,
      }),
    );
    setWorking(false);
    onClose();
    onArchived?.();
  };

  return (
    <AlertDialog open onOpenChange={(open) => !open && onClose()}>
      <AlertDialogContent data-testid="solidify-unbind-dialog">
        <AlertDialogHeader>
          <div className="flex items-start gap-3">
            <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-destructive/10">
              <Layers className="h-5 w-5 text-destructive" />
            </div>
            <div className="flex-1">
              <AlertDialogTitle>
                {t(($) => $.specialization.archive_block_title, {
                  name: parent.name,
                  count: targets.length,
                })}
              </AlertDialogTitle>
              <AlertDialogDescription>
                {t(($) => $.specialization.archive_block_description)}
              </AlertDialogDescription>
            </div>
          </div>
        </AlertDialogHeader>

        <div className="rounded-lg border bg-muted/30 px-3 py-2">
          <div className="text-caption font-medium text-muted-foreground">
            {t(($) => $.specialization.archive_block_list_title)}
          </div>
          <ul className="mt-1.5 space-y-1.5">
            {targets.map((target, index) => (
              <li
                key={target.agent?.id ?? `unlisted-${index}-${target.name}`}
                data-testid="solidify-unbind-child"
                className="flex min-w-0 items-center gap-2"
              >
                {target.agent ? (
                  <ActorAvatar
                    actorType="agent"
                    actorId={target.agent.id}
                    size="sm"
                    className="shrink-0"
                  />
                ) : (
                  <EyeOff
                    aria-hidden="true"
                    className="size-3.5 shrink-0 text-muted-foreground"
                  />
                )}
                <span className="min-w-0 truncate text-body">
                  {target.name}
                </span>
                {!target.agent && (
                  <span className="shrink-0 text-micro text-muted-foreground">
                    {t(($) => $.specialization.archive_block_hidden_row)}
                  </span>
                )}
              </li>
            ))}
          </ul>
          {hiddenCount > 0 && (
            <p
              data-testid="solidify-unbind-hidden-note"
              className="mt-2 text-caption leading-snug text-muted-foreground"
            >
              {t(($) => $.specialization.archive_block_hidden_note, {
                count: hiddenCount,
              })}
            </p>
          )}
          <span className="sr-only">{names}</span>
        </div>

        <AlertDialogFooter>
          <AlertDialogCancel>{t(($) => $.specialization.archive_block_cancel)}</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={working}
            // `AlertDialogAction` is a plain button here (it does not close the
            // dialog itself), so the dialog stays mounted while the requests
            // run and this component remains the one reporting their outcome.
            onClick={() => void handleConfirm()}
          >
            {t(($) => $.specialization.archive_block_confirm)}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
