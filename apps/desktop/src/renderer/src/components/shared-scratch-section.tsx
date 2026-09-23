import { useCallback, useEffect, useRef, useState } from "react";
import { HardDrive, Loader2, Trash2 } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Switch } from "@multica/ui/components/ui/switch";
import { toast } from "sonner";
import { SettingsCard, SettingsRow, SettingsSection } from "@multica/views/settings";
import { useT } from "@multica/views/i18n";
import type {
  SharedScratchReport,
  SharedScratchSettings,
} from "../../../main/shared-scratch";
import { daysSince, formatBytes } from "./worktree-cleanup-view";
import {
  canRemove,
  scratchTotals,
  sortSessions,
} from "./shared-scratch-view";

/**
 * This machine's shared session folder (DENE-622).
 *
 * Questions with nowhere else to go leave their files here, one subdirectory
 * per conversation, instead of a new task directory every time. Desktop-only:
 * the folder is on this computer, and the web app cannot see it.
 *
 * Automatic cleanup is on, because the folder is Multica's. The list is what
 * would go, and a folder a task is in — or one Multica did not create — has
 * no button.
 */
export function SharedScratchSection() {
  const { t } = useT("settings");
  const [report, setReport] = useState<SharedScratchReport | null>(null);
  const [loading, setLoading] = useState(true);
  const [unavailable, setUnavailable] = useState(false);
  const [saving, setSaving] = useState(false);
  const [removing, setRemoving] = useState<string | null>(null);
  const [retentionDraft, setRetentionDraft] = useState("");

  const tRef = useRef(t);
  tRef.current = t;

  const apply = useCallback(
    (result: Awaited<ReturnType<typeof window.desktopAPI.sharedScratchReport>>) => {
      if (result.ok) {
        setReport(result.report);
        setUnavailable(false);
        setRetentionDraft(String(result.report.settings.retention_days ?? 14));
        return true;
      }
      if (result.reason === "daemon_unavailable") {
        setUnavailable(true);
        return false;
      }
      toast.error(result.error ?? tRef.current(($) => $.desktop.shared_scratch.load_failed));
      return false;
    },
    [],
  );

  useEffect(() => {
    let cancelled = false;
    const read = window.desktopAPI?.sharedScratchReport;
    if (typeof read !== "function") {
      setUnavailable(true);
      setLoading(false);
      return;
    }
    void read().then((result) => {
      if (cancelled) return;
      apply(result);
      setLoading(false);
    });
    return () => {
      cancelled = true;
    };
  }, [apply]);

  const saveSettings = useCallback(
    async (next: SharedScratchSettings) => {
      setSaving(true);
      try {
        apply(await window.desktopAPI.saveSharedScratchSettings(next));
      } finally {
        setSaving(false);
      }
    },
    [apply],
  );

  const removeOne = useCallback(
    async (path: string) => {
      setRemoving(path);
      try {
        if (apply(await window.desktopAPI.removeSharedSession(path))) {
          toast.success(tRef.current(($) => $.desktop.shared_scratch.removed));
        }
      } finally {
        setRemoving(null);
      }
    },
    [apply],
  );

  const cleanExpired = useCallback(async () => {
    setRemoving("*");
    try {
      if (apply(await window.desktopAPI.cleanExpiredSharedSessions())) {
        toast.success(tRef.current(($) => $.desktop.shared_scratch.cleaned));
      }
    } finally {
      setRemoving(null);
    }
  }, [apply]);

  if (loading) {
    return (
      <SettingsSection title={t(($) => $.desktop.shared_scratch.title)}>
        <div className="flex items-center gap-2 px-1 py-3 text-body text-muted-foreground">
          <Loader2 className="size-4 animate-spin" />
          {t(($) => $.desktop.shared_scratch.scanning)}
        </div>
      </SettingsSection>
    );
  }

  if (unavailable || !report) {
    return (
      <SettingsSection title={t(($) => $.desktop.shared_scratch.title)}>
        <p className="px-1 py-3 text-body text-muted-foreground">
          {t(($) => $.desktop.shared_scratch.daemon_offline)}
        </p>
      </SettingsSection>
    );
  }

  const sessions = sortSessions(report.sessions ?? []);
  const totals = scratchTotals(sessions);
  const now = new Date();

  return (
    <SettingsSection
      title={t(($) => $.desktop.shared_scratch.title)}
      description={t(($) => $.desktop.shared_scratch.description)}
    >
      <SettingsCard>
        <SettingsRow
          label={t(($) => $.desktop.shared_scratch.location_title)}
          description={t(($) => $.desktop.shared_scratch.location_description)}
        >
          <span className="max-w-md truncate font-mono text-caption" title={report.root}>
            {report.root || "—"}
          </span>
        </SettingsRow>
        <SettingsRow
          label={t(($) => $.desktop.shared_scratch.size_title)}
          description={t(($) => $.desktop.shared_scratch.summary, {
            count: totals.count,
            total: formatBytes(report.size_bytes),
            reclaimable: formatBytes(totals.reclaimable),
          })}
        >
          <span className="inline-flex items-center gap-1.5 text-body">
            <HardDrive className="size-3.5 text-muted-foreground" />
            {formatBytes(report.size_bytes)}
          </span>
        </SettingsRow>
        <SettingsRow
          label={t(($) => $.desktop.shared_scratch.enable_title)}
          description={t(($) => $.desktop.shared_scratch.enable_description)}
        >
          <Switch
            checked={report.settings.enabled !== false}
            disabled={saving}
            onCheckedChange={(checked) =>
              void saveSettings({ ...report.settings, enabled: checked })
            }
          />
        </SettingsRow>
        <SettingsRow
          label={t(($) => $.desktop.shared_scratch.retention_title)}
          description={t(($) => $.desktop.shared_scratch.retention_description)}
        >
          <Input
            type="number"
            min={1}
            className="w-24"
            value={retentionDraft}
            disabled={saving}
            onChange={(e) => setRetentionDraft(e.target.value)}
            onBlur={() => {
              const parsed = Number.parseInt(retentionDraft, 10);
              if (!Number.isFinite(parsed) || parsed < 1) {
                setRetentionDraft(String(report.settings.retention_days ?? 14));
                return;
              }
              if (parsed === report.settings.retention_days) return;
              void saveSettings({ ...report.settings, retention_days: parsed });
            }}
          />
        </SettingsRow>
      </SettingsCard>

      <div className="mt-3 flex justify-end">
        <Button
          variant="outline"
          size="sm"
          disabled={removing !== null || totals.reclaimable === 0}
          onClick={() => void cleanExpired()}
        >
          {t(($) => $.desktop.shared_scratch.clean_expired)}
        </Button>
      </div>

      {sessions.length === 0 ? (
        <p className="px-1 py-3 text-body text-muted-foreground">
          {t(($) => $.desktop.shared_scratch.empty)}
        </p>
      ) : (
        <ul className="mt-2 divide-y rounded-lg border">
          {sessions.map((session) => {
            const days = daysSince(session.last_used ?? "", now);
            const label = session.session_id || session.path;
            return (
              <li key={session.path} className="flex items-center gap-3 px-3 py-2">
                <div className="min-w-0 flex-1">
                  <div className="truncate font-mono text-caption" title={session.path}>
                    {label}
                  </div>
                  <div className="text-caption text-muted-foreground">
                    {formatBytes(session.size_bytes)}
                    {days !== null
                      ? ` · ${t(($) => $.desktop.shared_scratch.last_used, { days })}`
                      : ""}
                    {session.in_use
                      ? ` · ${t(($) => $.desktop.shared_scratch.in_use)}`
                      : ""}
                    {!session.ours
                      ? ` · ${t(($) => $.desktop.shared_scratch.not_ours)}`
                      : ""}
                  </div>
                </div>
                {canRemove(session) ? (
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={removing !== null}
                    onClick={() => void removeOne(session.path)}
                  >
                    <Trash2 className="size-3.5" />
                    {t(($) => $.desktop.shared_scratch.remove)}
                  </Button>
                ) : null}
              </li>
            );
          })}
        </ul>
      )}
    </SettingsSection>
  );
}
