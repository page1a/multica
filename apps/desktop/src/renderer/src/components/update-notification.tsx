import { useEffect, useState } from "react";
import { AlertCircle, ArrowDownToLine, FolderOpen, PackageOpen, RefreshCw, X } from "lucide-react";
import { useUpdater } from "../hooks/use-updater";
import type { UpdatePhase } from "../stores/updater-store";

// Rendered outside CoreProvider (see App.tsx) so it cannot use the i18n
// hooks; copy stays English like the rest of the pre-provider shell.

function changelogUrl(version: string): string {
  return `https://multica.ai/changelog#release-${version.replace(/\./g, "-")}`;
}

// A card is only worth interrupting for when the user can act or must know
// something went wrong. Checking / up-to-date stay silent here; Settings →
// Updates shows them.
function isActionable(phase: UpdatePhase): boolean {
  return (
    phase.status === "available" ||
    phase.status === "downloading" ||
    phase.status === "downloaded" ||
    phase.status === "installer-ready" ||
    phase.status === "error"
  );
}

const secondaryButton =
  "inline-flex items-center rounded-md border border-border bg-background px-3 py-1.5 text-caption font-medium text-foreground hover:bg-accent transition-colors";
const primaryButton =
  "inline-flex items-center rounded-md bg-primary px-3 py-1.5 text-caption font-medium text-primary-foreground hover:bg-primary/90 transition-colors";

export function UpdateNotification() {
  const {
    phase,
    autoUpdateSupported,
    assistedInstallSupported,
    download,
    install,
    openReleasePage,
    openInstaller,
    revealInstaller,
  } = useUpdater();
  const [dismissedPhase, setDismissedPhase] = useState<UpdatePhase["status"] | null>(
    null,
  );

  // A dismissal only covers the phase it was made in: closing "available"
  // must not also hide the later "downloaded" or "error" card.
  useEffect(() => {
    if (dismissedPhase !== null && dismissedPhase !== phase.status) {
      setDismissedPhase(null);
    }
  }, [phase.status, dismissedPhase]);

  if (!isActionable(phase)) return null;
  if (dismissedPhase === phase.status) return null;

  return (
    <div
      role="status"
      aria-live="polite"
      className="fixed bottom-4 right-4 z-50 w-80 rounded-lg border border-border bg-background p-4 shadow-lg animate-in slide-in-from-bottom-2 fade-in duration-300"
    >
      <button
        type="button"
        aria-label="Dismiss"
        onClick={() => setDismissedPhase(phase.status)}
        className="absolute top-2 right-2 rounded-md p-1 text-muted-foreground hover:text-foreground transition-colors"
      >
        <X className="size-3.5" />
      </button>

      {/* Only a build that can neither install itself nor fetch its own
          installer has nothing to offer but a browser link. */}
      {phase.status === "available" &&
        !autoUpdateSupported &&
        !assistedInstallSupported && (
          <Card
            icon={<ArrowDownToLine className="size-4 text-primary" />}
            tone="bg-primary/10"
            title="Manual download required"
            body={`v${phase.version} is available, but this build can't download or install it on its own.`}
          >
            <button type="button" onClick={() => void openReleasePage()} className={primaryButton}>
              Open release page
            </button>
          </Card>
        )}

      {phase.status === "available" &&
        (autoUpdateSupported || assistedInstallSupported) && (
          <Card
            icon={<ArrowDownToLine className="size-4 text-primary" />}
            tone="bg-primary/10"
            title="Update available"
            body={`v${phase.version} is ready to download.`}
          >
            <button type="button" onClick={() => void download()} className={primaryButton}>
              Download
            </button>
          </Card>
        )}

      {phase.status === "downloading" && (
        <Card
          icon={<ArrowDownToLine className="size-4 text-primary" />}
          tone="bg-primary/10"
          title="Downloading update"
          body={`v${phase.version} · ${Math.round(phase.percent)}%`}
        >
          <DownloadProgressBar percent={phase.percent} />
        </Card>
      )}

      {phase.status === "downloaded" && (
        <Card
          icon={<RefreshCw className="size-4 text-success" />}
          tone="bg-success/10"
          title="Update ready"
          body={`v${phase.version} will be applied on next launch.`}
        >
          <button
            type="button"
            onClick={() => window.desktopAPI.openExternal(changelogUrl(phase.version))}
            className={secondaryButton}
          >
            See changelog
          </button>
          <button type="button" onClick={() => void install()} className={primaryButton}>
            Restart now
          </button>
        </Card>
      )}

      {/* The assisted finish line: the .dmg is on disk, and the one step this
          build is not allowed to take on its own is the drag into
          Applications. Opening the image puts Finder's own drag window on
          screen, which is the clearest instruction available. */}
      {phase.status === "installer-ready" && (
        <Card
          icon={<PackageOpen className="size-4 text-success" />}
          tone="bg-success/10"
          title="Installer downloaded"
          body={`v${phase.version} is downloaded. Open it, then drag Multica into Applications and replace the old copy.`}
        >
          <button
            type="button"
            onClick={() => void revealInstaller()}
            className={secondaryButton}
          >
            <FolderOpen className="size-3.5 mr-1" />
            Show in Finder
          </button>
          <button
            type="button"
            onClick={() => void openInstaller()}
            className={primaryButton}
          >
            Open installer
          </button>
        </Card>
      )}

      {phase.status === "error" && (
        <Card
          icon={<AlertCircle className="size-4 text-destructive" />}
          tone="bg-destructive/10"
          title="Update failed"
          body={phase.message}
        >
          {phase.version && (autoUpdateSupported || assistedInstallSupported) && (
            <button type="button" onClick={() => void download()} className={primaryButton}>
              Retry download
            </button>
          )}
          <button type="button" onClick={() => void openReleasePage()} className={secondaryButton}>
            Open release page
          </button>
        </Card>
      )}
    </div>
  );
}

export function DownloadProgressBar({ percent }: { percent: number }) {
  const clamped = Math.max(0, Math.min(100, percent));
  return (
    <div
      role="progressbar"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round(clamped)}
      className="relative h-1 w-full overflow-hidden rounded-full bg-muted"
    >
      <div
        className="h-full bg-primary transition-all"
        style={{ width: `${clamped}%` }}
      />
    </div>
  );
}

function Card({
  icon,
  tone,
  title,
  body,
  children,
}: {
  icon: React.ReactNode;
  tone: string;
  title: string;
  body: string;
  children?: React.ReactNode;
}) {
  return (
    <div className="flex items-start gap-3">
      <div className={`mt-0.5 rounded-md p-1.5 ${tone}`}>{icon}</div>
      <div className="flex-1 min-w-0">
        <p className="text-body font-medium">{title}</p>
        <p className="text-caption text-muted-foreground mt-0.5 break-words">{body}</p>
        {children && <div className="mt-2 flex items-center gap-1.5">{children}</div>}
      </div>
    </div>
  );
}
