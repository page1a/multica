import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import type {
  ReleaseChannel,
  UpdaterPreferences,
} from "../shared/updater-types";

export const DEFAULT_UPDATER_PREFERENCES: UpdaterPreferences = {
  automaticUpdates: true,
  releaseChannel: "stable",
};

function isReleaseChannel(value: unknown): value is ReleaseChannel {
  return value === "stable" || value === "test";
}

export function updaterPreferencesPath(userDataPath: string): string {
  return join(userDataPath, "updater-preferences.json");
}

function parseUpdaterPreferences(value: unknown): UpdaterPreferences {
  const candidate = value as {
    automaticUpdates?: unknown;
    releaseChannel?: unknown;
  } | null;
  if (
    typeof value !== "object" ||
    value === null ||
    typeof candidate?.automaticUpdates !== "boolean"
  ) {
    return { ...DEFAULT_UPDATER_PREFERENCES };
  }

  // Older files predate the channel field. Missing or unknown values stay on
  // the stable feed so installed clients keep checking the same manifests.
  return {
    automaticUpdates: candidate.automaticUpdates,
    releaseChannel: isReleaseChannel(candidate.releaseChannel)
      ? candidate.releaseChannel
      : DEFAULT_UPDATER_PREFERENCES.releaseChannel,
  };
}

export async function loadUpdaterPreferences(
  filePath: string,
): Promise<UpdaterPreferences> {
  try {
    return parseUpdaterPreferences(JSON.parse(await readFile(filePath, "utf-8")));
  } catch {
    return { ...DEFAULT_UPDATER_PREFERENCES };
  }
}

export async function saveUpdaterPreferences(
  filePath: string,
  preferences: UpdaterPreferences,
): Promise<void> {
  await mkdir(dirname(filePath), { recursive: true });
  const temporaryPath = `${filePath}.tmp`;
  await writeFile(temporaryPath, JSON.stringify(preferences, null, 2), "utf-8");
  await rename(temporaryPath, filePath);
}
