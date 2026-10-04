"use client";

import { useEffect, useId, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { projectMemoryLocationsOptions } from "@multica/core/projects/queries";
import { workspaceKeys } from "@multica/core/workspace/queries";
import type { Workspace } from "@multica/core/types";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../../i18n";

const MAX_INSTRUCTION_BYTES = 4000;

function storedInstruction(workspace: Workspace): string {
  const memory = workspace.settings?.memory as Record<string, unknown> | undefined;
  const value = memory?.sediment_instruction;
  return typeof value === "string" ? value.trim() : "";
}

/**
 * The prompt that opens every automatic sediment ticket. Empty means the
 * server's built-in text; the reason the round opened is appended either way.
 * Like the rest of the workspace page it saves on blur, with no Save button.
 */
export function SedimentInstructionForm({
  workspace,
  canManage,
}: {
  workspace: Workspace;
  canManage: boolean;
}) {
  const { t } = useT("settings");
  const qc = useQueryClient();
  const id = useId();
  const { data: locations } = useQuery(projectMemoryLocationsOptions(workspace.id));
  const stored = storedInstruction(workspace);
  const [instruction, setInstruction] = useState(stored);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  // A save elsewhere (another tab, the CLI) replaces an untouched draft.
  useEffect(() => setInstruction(stored), [stored]);
  const builtin = locations?.builtin_sediment_instruction;

  const save = async () => {
    const next = instruction.trim();
    if (next === stored || saving) return;
    if (new TextEncoder().encode(next).length > MAX_INSTRUCTION_BYTES) {
      setError(t(($) => $.workspace.sediment_instruction_invalid));
      return;
    }
    setSaving(true);
    try {
      const updated = await api.updateWorkspace(workspace.id, {
        settings: {
          ...(workspace.settings ?? {}),
          memory: {
            ...((workspace.settings?.memory as Record<string, unknown> | undefined) ?? {}),
            sediment_instruction: next,
          },
        },
      });
      qc.setQueryData(workspaceKeys.list(), (old: Workspace[] | undefined) =>
        old?.map((ws) => (ws.id === updated.id ? updated : ws)),
      );
      toast.success(t(($) => $.workspace.toast_saved), { id: "settings-auto-save" });
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t(($) => $.workspace.toast_save_failed));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="space-y-2 px-4 py-3.5">
      <label htmlFor={`${id}-instruction`} className="text-body font-medium">
        {t(($) => $.workspace.sediment_instruction_label)}
      </label>
      <p id={`${id}-hint`} className="text-caption leading-5 text-muted-foreground">
        {t(($) => $.workspace.sediment_instruction_hint)}
      </p>
      <Textarea
        id={`${id}-instruction`}
        rows={3}
        value={instruction}
        placeholder={t(($) => $.workspace.sediment_instruction_placeholder)}
        disabled={!canManage || saving}
        aria-describedby={error ? `${id}-hint ${id}-error` : `${id}-hint`}
        aria-invalid={!!error}
        className="resize-y text-base md:text-body"
        onChange={(event) => {
          setInstruction(event.target.value);
          setError("");
        }}
        onBlur={() => void save()}
        onKeyDown={(event) => {
          if ((event.metaKey || event.ctrlKey) && event.key === "Enter" && !event.nativeEvent.isComposing) {
            event.preventDefault();
            void save();
          }
        }}
      />
      {error && (
        <p id={`${id}-error`} role="alert" className="text-caption text-destructive">
          {error}
        </p>
      )}
      {builtin && (
        <div className="rounded-md bg-muted/60 px-3 py-2 text-caption leading-5 text-muted-foreground">
          <p className="font-medium">{t(($) => $.workspace.sediment_instruction_builtin)}</p>
          <p className="mt-0.5 whitespace-pre-wrap break-words">{builtin}</p>
        </div>
      )}
    </div>
  );
}
