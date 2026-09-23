"use client";

import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";

/**
 * The skippable suggestion shown when a picked folder has no Git.
 * Declining is the surrounding confirm ("add the folder anyway"); this
 * block only offers to create the repository.
 */
export function PlainFolderGitOffer({
  onInit,
  pending = false,
  error,
}: {
  onInit: () => void;
  pending?: boolean;
  error?: string;
}) {
  const { t } = useT("projects");
  return (
    <div className="rounded-md border border-amber-500/30 bg-amber-500/5 px-2.5 py-2 space-y-1.5">
      <p className="text-caption text-foreground">
        {t(($) => $.resources.plain_folder_offer)}
      </p>
      <p className="text-micro text-muted-foreground">
        {t(($) => $.resources.plain_folder_offer_skip)}
      </p>
      {error && <p className="text-micro text-destructive">{error}</p>}
      <Button
        type="button"
        size="sm"
        variant="outline"
        className="h-7 text-caption"
        onClick={onInit}
        disabled={pending}
      >
        {t(($) => $.resources.plain_folder_init)}
      </Button>
    </div>
  );
}
