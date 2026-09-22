import { useCallback, useEffect, useRef, useState } from "react";
import { HardDrive, Loader2, Trash2 } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Switch } from "@multica/ui/components/ui/switch";
import { toast } from "sonner";
import { SettingsCard, SettingsRow, SettingsSection } from "@multica/views/settings";
import { useT } from "@multica/views/i18n";
import type {
  WorktreeCleanupReport,
  WorktreeCleanupSettings,
} from "../../../main/worktree-cleanup";
import {
  daysSince,
  formatBytes,
  isRemovable,
  previewIsAdvisory,
  sortForDisplay,
  totalsOf,
  type WorktreeKeepReason,
} from "./worktree-cleanup-view";

/**
 * This machine's parallel-mode working copies, and the policy for removing
 * them (DENE-617).
 *
 * Desktop-only, deliberately. Everything here — the directories, their sizes,
 * their git state — exists on this computer and nowhere else, so there is no
 * web equivalent to share with; the web app cannot see any of it and would
 * have nothing to show.
 *
 * The screen is built around one idea: the list is always computed, and the
 * switch only decides whether anything acts on it. A user can therefore read
 * exactly what cleanup would do to their disk BEFORE agreeing to it, which is
 * what makes "off by default" an honest default rather than a hidden feature.
 *
 * The reading rules (order, totals, formatting) live in
 * `worktree-cleanup-view.ts` next door; the verdicts come from the daemon,
 * which re-checks them against the live filesystem before removing anything.
 */
export function WorktreeCleanupSection() {
  const { t } = useT("settings");
  const [report, setReport] = useState<WorktreeCleanupReport | null>(null);
  const [loading, setLoading] = useState(true);
  const [unavailable, setUnavailable] = useState(false);
  const [saving, setSaving] = useState(false);
  const [removing, setRemoving] = useState<string | null>(null);
  const [minAgeDraft, setMinAgeDraft] = useState("");

  // `t` is a fresh function on every render. Depending on it would make
  // `apply` — and through it the load effect below — a new value each pass,
  // so the effect would re-fetch on every render and overwrite whatever the
  // last save or removal just returned. A ref keeps the current translator
  // available without putting render identity in a dependency list.
  const tRef = useRef(t);
  tRef.current = t;

  const apply = useCallback(
    (
      result: Awaited<ReturnType<typeof window.desktopAPI.worktreeCleanupReport>>,
    ) => {
      if (result.ok) {
        setReport(result.report);
        setUnavailable(false);
        setMinAgeDraft(String(result.report.settings.min_age_days ?? 14));
        return true;
      }
      if (result.reason === "daemon_unavailable") {
        setUnavailable(true);
        return false;
      }
      toast.error(
        result.error ?? tRef.current(($) => $.desktop.worktree_cleanup.load_failed),
      );
      return false;
    },
    [],
  );

  useEffect(() => {
    let cancelled = false;
    // A preload that predates this bridge has no such method. Treat that like
    // a daemon that is not answering rather than throwing: this section sits
    // inside the daemon settings tab, and a missing optional feature must not
    // take the whole tab down with it.
    const read = window.desktopAPI?.worktreeCleanupReport;
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
    async (next: WorktreeCleanupSettings) => {
      setSaving(true);
      try {
        apply(await window.desktopAPI.saveWorktreeCleanupSettings(next));
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
        // The daemon still applies every keep rule. An explicit click says
        // WHICH copy, not "ignore the rules" — a copy that became dirty since
        // the list was drawn is refused here with the reason.
        if (apply(await window.desktopAPI.removeWorktreeCopy(path))) {
          toast.success(tRef.current(($) => $.desktop.worktree_cleanup.removed));
        }
      } finally {
        setRemoving(null);
      }
    },
    [apply],
  );

  if (loading) {
    return (
      <SettingsSection title={t(($) => $.desktop.worktree_cleanup.title)}>
        <div className="flex items-center gap-2 px-1 py-3 text-body text-muted-foreground">
          <Loader2 className="size-4 animate-spin" />
          {t(($) => $.desktop.worktree_cleanup.scanning)}
        </div>
      </SettingsSection>
    );
  }

  if (unavailable || !report) {
    return (
      <SettingsSection title={t(($) => $.desktop.worktree_cleanup.title)}>
        <p className="px-1 py-3 text-body text-muted-foreground">
          {t(($) => $.desktop.worktree_cleanup.daemon_offline)}
        </p>
      </SettingsSection>
    );
  }

  const items = sortForDisplay(report.items);
  const totals = totalsOf(report.items);
  const advisory = previewIsAdvisory(report);
  const now = new Date();

  /**
   * The one sentence a kept copy gets. Every branch is a reason to KEEP,
   * phrased as what the user would have to change — "still has uncommitted
   * changes" rather than "ineligible" — because the list's job is to explain a
   * decision, not to score it.
   *
   * Local to the component so `t` keeps the settings namespace it was narrowed
   * to; lifting it out would widen `t` back to every namespace and lose the
   * key checking that makes a typo here a build error.
   */
  const keepReasonLabel = (reason: WorktreeKeepReason | undefined): string => {
    switch (reason) {
      case "uncommitted_changes":
        return t(($) => $.desktop.worktree_cleanup.reason_uncommitted);
      case "branch_not_merged":
        return t(($) => $.desktop.worktree_cleanup.reason_unmerged);
      case "too_recent":
        return t(($) => $.desktop.worktree_cleanup.reason_too_recent);
      case "in_use":
        return t(($) => $.desktop.worktree_cleanup.reason_in_use);
      case "not_multica_created":
        return t(($) => $.desktop.worktree_cleanup.reason_not_ours);
      case "status_unknown":
        return t(($) => $.desktop.worktree_cleanup.reason_unknown);
      default:
        // A reason this build does not know is still a reason to keep. Saying
        // so beats rendering nothing, which would read as "no obstacle".
        return t(($) => $.desktop.worktree_cleanup.reason_unknown);
    }
  };

  /**
   * The one sentence a qualifying copy gets. A squash-merged branch gets its
   * own: its commits are NOT in trunk and never will be, so a user who reads
   * "merged" and checks `git log` would find nothing and stop trusting the
   * list. Saying "its changes are in trunk, as a squash" is the claim the
   * daemon actually verified.
   */
  const removableLabel = (item: (typeof items)[number]): string =>
    item.merged_via === "squash"
      ? t(($) => $.desktop.worktree_cleanup.reason_removable_squash)
      : t(($) => $.desktop.worktree_cleanup.reason_removable);

  return (
    <SettingsSection
      title={t(($) => $.desktop.worktree_cleanup.title)}
      description={t(($) => $.desktop.worktree_cleanup.description)}
    >
      <SettingsCard>
        <SettingsRow
          label={t(($) => $.desktop.worktree_cleanup.enable_title)}
          description={t(($) => $.desktop.worktree_cleanup.enable_description)}
        >
          <Switch
            checked={report.settings.enabled === true}
            disabled={saving}
            onCheckedChange={(checked) =>
              void saveSettings({ ...report.settings, enabled: checked })
            }
          />
        </SettingsRow>

        <SettingsRow
          label={t(($) => $.desktop.worktree_cleanup.min_age_title)}
          description={t(($) => $.desktop.worktree_cleanup.min_age_description)}
        >
          <Input
            type="number"
            min={1}
            className="w-24"
            value={minAgeDraft}
            disabled={saving}
            onChange={(e) => setMinAgeDraft(e.target.value)}
            // Committed on blur rather than per keystroke: every save triggers
            // a full rescan, and "1" on the way to "14" is a policy nobody
            // chose.
            onBlur={() => {
              const parsed = Number.parseInt(minAgeDraft, 10);
              if (!Number.isFinite(parsed) || parsed < 1) {
                setMinAgeDraft(String(report.settings.min_age_days ?? 14));
                return;
              }
              if (parsed === report.settings.min_age_days) return;
              void saveSettings({ ...report.settings, min_age_days: parsed });
            }}
          />
        </SettingsRow>

        <SettingsRow
          label={t(($) => $.desktop.worktree_cleanup.trunk_title)}
          description={t(($) => $.desktop.worktree_cleanup.trunk_description)}
        >
          <Input
            className="w-40"
            placeholder={t(($) => $.desktop.worktree_cleanup.trunk_placeholder)}
            defaultValue={report.settings.trunk_branch ?? ""}
            disabled={saving}
            onBlur={(e) => {
              const next = e.target.value.trim();
              if (next === (report.settings.trunk_branch ?? "")) return;
              void saveSettings({ ...report.settings, trunk_branch: next });
            }}
          />
        </SettingsRow>
      </SettingsCard>

      <div className="mt-4">
        <div className="flex items-center gap-2 px-1 pb-2 text-caption text-muted-foreground">
          <HardDrive className="size-3.5" />
          <span>
            {t(($) => $.desktop.worktree_cleanup.summary, {
              count: totals.count,
              total: formatBytes(totals.bytes),
              reclaimable: formatBytes(totals.removableBytes),
            })}
          </span>
        </div>
        {advisory && totals.removableCount > 0 && (
          <p className="px-1 pb-2 text-caption text-muted-foreground">
            {t(($) => $.desktop.worktree_cleanup.advisory)}
          </p>
        )}

        {items.length === 0 ? (
          <p className="px-1 py-3 text-body text-muted-foreground">
            {t(($) => $.desktop.worktree_cleanup.empty)}
          </p>
        ) : (
          <SettingsCard>
            {items.map((item) => {
              const age = daysSince(item.last_run_at, now);
              return (
                <div
                  key={item.path}
                  className="flex items-start gap-3 px-4 py-3"
                  data-testid="worktree-copy"
                >
                  <div className="min-w-0 flex-1">
                    <p className="truncate font-mono text-caption" title={item.path}>
                      {item.path}
                    </p>
                    <p className="mt-0.5 text-caption text-muted-foreground">
                      {formatBytes(item.size_bytes)}
                      {item.branch ? ` · ${item.branch}` : ""}
                      {age === null
                        ? ""
                        : ` · ${t(($) => $.desktop.worktree_cleanup.last_run, { days: age })}`}
                    </p>
                    <p className="mt-0.5 text-caption text-muted-foreground">
                      {isRemovable(item)
                        ? removableLabel(item)
                        : keepReasonLabel(item.keep_reason)}
                    </p>
                  </div>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="shrink-0"
                    // Only ever offered for a copy the policy already
                    // qualifies. The button is a shortcut past the WAIT, not
                    // past the rules.
                    disabled={!isRemovable(item) || removing !== null}
                    onClick={() => void removeOne(item.path)}
                  >
                    {removing === item.path ? (
                      <Loader2 className="size-3.5 animate-spin" />
                    ) : (
                      <Trash2 className="size-3.5" />
                    )}
                    {t(($) => $.desktop.worktree_cleanup.remove_now)}
                  </Button>
                </div>
              );
            })}
          </SettingsCard>
        )}

        {report.errors?.map((error) => (
          <p key={error} className="px-1 pt-2 text-caption text-destructive">
            {error}
          </p>
        ))}
      </div>
    </SettingsSection>
  );
}
