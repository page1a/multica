import { useCallback, useEffect, useState } from "react";
import { AlertCircle, Info } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
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
import {
  SettingsCard,
  SettingsRow,
  SettingsSection,
  SettingsTab,
} from "@multica/views/settings";
import { useT } from "@multica/views/i18n";
import { hasCompletedTransferExport } from "@multica/views/platform";
import {
  DEFAULT_ENTRY_RUNTIME_CONFIG,
  desktopProfileName,
  isOfficialCloudConfig,
  runtimeConfigFromServerUrl,
  runtimeConfigHost,
  type RuntimeConfig,
} from "../../../shared/runtime-config";

type SwitchTarget = { type: "official" } | { type: "url"; url: string };

function bootConfig(): RuntimeConfig | null {
  const result = window.desktopAPI.runtimeConfig;
  return result.ok ? result.config : null;
}

function quitShortcut(): string {
  return window.desktopAPI.appInfo.os === "macos" ? "⌘Q" : "Ctrl+Q";
}

export function ServerSettingsTab() {
  const { t } = useT("settings");
  const [config, setConfig] = useState<RuntimeConfig | null>(bootConfig);
  const [customUrl, setCustomUrl] = useState("");
  const [autoStop, setAutoStop] = useState(false);
  const [pending, setPending] = useState<SwitchTarget | null>(null);
  const [restartRequired, setRestartRequired] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const shortcut = quitShortcut();

  useEffect(() => {
    let mounted = true;
    void window.daemonAPI
      .getPrefs()
      .then((prefs) => {
        if (mounted) setAutoStop(prefs.autoStop === true);
      })
      .catch(() => {
        // Prefs are only needed for the side-effect warning. Keep the
        // switcher usable if daemon IPC is unavailable.
      });
    return () => {
      mounted = false;
    };
  }, []);

  const openConfirm = useCallback(
    (target: SwitchTarget) => {
      setError(null);
      if (target.type === "url") {
        try {
          runtimeConfigFromServerUrl(target.url);
        } catch {
          setError(t(($) => $.desktop.server.invalid_url));
          return;
        }
      }
      setPending(target);
    },
    [t],
  );

  const handleConfirm = useCallback(async () => {
    if (!pending) return;
    setSaving(true);
    try {
      const result = await window.desktopAPI.switchServer(
        pending.type === "official" ? null : pending.url,
      );
      if (!result.ok) {
        setError(result.error || t(($) => $.desktop.server.switch_failed));
        return;
      }
      setConfig(result.config);
      setRestartRequired(true);
      setPending(null);
      setError(null);
    } catch (err) {
      setError(
        err instanceof Error
          ? err.message
          : t(($) => $.desktop.server.switch_failed),
      );
    } finally {
      setSaving(false);
    }
  }, [pending, t]);

  const displayed = config ?? DEFAULT_ENTRY_RUNTIME_CONFIG;
  const official = isOfficialCloudConfig(displayed);
  const showExportHint =
    pending !== null && !hasCompletedTransferExport();

  return (
    <SettingsTab
      title={t(($) => $.desktop.server.title)}
      description={t(($) => $.desktop.server.description)}
    >
      {restartRequired && (
        <div className="flex items-start gap-3 rounded-lg border border-warning/40 bg-warning/5 px-4 py-3">
          <Info className="mt-0.5 size-4 shrink-0 text-warning" />
          <div className="min-w-0">
            <p className="text-body font-medium">
              {t(($) => $.desktop.server.restart_title)}
            </p>
            <p className="mt-0.5 text-body text-muted-foreground">
              {t(($) => $.desktop.server.restart_description, { shortcut })}
            </p>
          </div>
        </div>
      )}

      <SettingsSection title={t(($) => $.desktop.server.current_title)}>
        <SettingsCard>
          <SettingsRow label={t(($) => $.desktop.server.host)}>
            <span className="font-mono text-caption text-muted-foreground">
              {runtimeConfigHost(displayed)}
            </span>
          </SettingsRow>
          <SettingsRow label={t(($) => $.desktop.server.kind)}>
            <span className="text-body text-muted-foreground">
              {official
                ? t(($) => $.desktop.server.kind_official)
                : t(($) => $.desktop.server.kind_self_hosted)}
            </span>
          </SettingsRow>
          <SettingsRow label={t(($) => $.desktop.server.profile)}>
            <span className="font-mono text-caption text-muted-foreground">
              {desktopProfileName(displayed.apiUrl)}
            </span>
          </SettingsRow>
          <SettingsRow label={t(($) => $.desktop.server.api_url)}>
            <span
              className="block truncate font-mono text-caption text-muted-foreground"
              title={displayed.apiUrl}
            >
              {displayed.apiUrl}
            </span>
          </SettingsRow>
        </SettingsCard>
      </SettingsSection>

      <SettingsSection
        title={t(($) => $.desktop.server.switch_title)}
        description={t(($) => $.desktop.server.switch_description)}
      >
        <SettingsCard>
          <SettingsRow
            label={t(($) => $.desktop.server.preset_official)}
            description={t(($) => $.desktop.server.preset_official_description)}
          >
            {official ? (
              <span className="text-caption text-muted-foreground">
                {t(($) => $.desktop.server.current_badge)}
              </span>
            ) : (
              <Button
                variant="outline"
                size="sm"
                onClick={() => openConfirm({ type: "official" })}
                disabled={saving}
              >
                {t(($) => $.desktop.server.switch_to_official)}
              </Button>
            )}
          </SettingsRow>

          <SettingsRow
            label={t(($) => $.desktop.server.custom_url)}
            description={t(($) => $.desktop.server.custom_url_description)}
            size="text"
            align="start"
          >
            <div className="flex flex-col gap-2 sm:flex-row">
              <Input
                type="url"
                value={customUrl}
                onChange={(event) => {
                  setCustomUrl(event.target.value);
                  setError(null);
                }}
                placeholder={t(($) => $.desktop.server.custom_url_placeholder)}
                spellCheck={false}
                autoComplete="off"
                aria-label={t(($) => $.desktop.server.custom_url)}
                className="font-mono"
              />
              <Button
                variant="outline"
                size="sm"
                onClick={() => openConfirm({ type: "url", url: customUrl })}
                disabled={saving || customUrl.trim().length === 0}
              >
                {t(($) => $.desktop.server.switch)}
              </Button>
            </div>
          </SettingsRow>
        </SettingsCard>

        {error && (
          <p className="flex items-center gap-1.5 px-0.5 text-caption text-destructive">
            <AlertCircle className="size-3.5 shrink-0" />
            {error}
          </p>
        )}
      </SettingsSection>

      <AlertDialog
        open={pending !== null}
        onOpenChange={(open) => {
          if (!open && !saving) setPending(null);
        }}
      >
        <AlertDialogContent className="sm:max-w-lg">
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.desktop.server.confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription className="space-y-2 text-pretty">
              <span className="block">
                {t(($) => $.desktop.server.confirm_description, { shortcut })}
              </span>
              {autoStop && (
                <span className="block">
                  {t(($) => $.desktop.server.confirm_autostop)}
                </span>
              )}
              <span className="block">
                {t(($) => $.desktop.server.confirm_switch_back)}
              </span>
              {showExportHint ? (
                <span
                  className="block"
                  data-testid="server-switch-export-hint"
                >
                  {t(($) => $.desktop.server.confirm_export_first)}
                </span>
              ) : null}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={saving}>
              {t(($) => $.desktop.server.confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              onClick={(event) => {
                event.preventDefault();
                void handleConfirm();
              }}
              disabled={saving}
            >
              {t(($) => $.desktop.server.confirm_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsTab>
  );
}
