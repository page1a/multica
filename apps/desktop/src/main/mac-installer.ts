import { mkdir, open, readFile, readdir, rename, rm, stat } from "node:fs/promises";
import { join } from "node:path";

/**
 * The macOS fallback when Squirrel cannot install an update in place.
 *
 * An ad-hoc signed build can never satisfy the next version's designated
 * requirement (see mac-signing.ts), so `autoUpdater` is not allowed to stage a
 * package it would fail to apply. That used to end the story at "open the
 * release page" — the user then had to find the right asset among six of them
 * and download it in a browser.
 *
 * This module keeps the download inside the app: it resolves the `.dmg` that
 * belongs to the offered version from the very feed electron-updater is
 * already pointed at, streams it to disk with progress, and hands back a local
 * path the renderer can open. The user's only remaining job is the drag into
 * Applications, which macOS treats as explicit consent and therefore does not
 * subject to the same-origin check that blocks the silent path.
 */

/** Shape of the packaged `app-update.yml`, narrowed to what a URL needs. */
export interface UpdateFeedConfig {
  provider: string;
  owner?: string;
  repo?: string;
  url?: string;
}

/**
 * Parse the subset of `app-update.yml` that matters here. electron-builder
 * writes a flat scalar map (provider/owner/repo/url/updaterCacheDirName), so a
 * line reader is enough and keeps a YAML dependency out of the main process.
 * Nested or list values are ignored rather than mis-parsed.
 */
export function parseUpdateFeedConfig(text: string): UpdateFeedConfig | null {
  const entries = new Map<string, string>();
  for (const rawLine of text.split(/\r?\n/)) {
    const line = rawLine.replace(/\s+#.*$/, "");
    if (/^\s/.test(line)) continue; // nested value — not one of ours
    const match = /^([A-Za-z_][A-Za-z0-9_-]*):\s*(.*)$/.exec(line);
    if (!match) continue;
    const value = match[2].trim().replace(/^["']|["']$/g, "");
    if (value === "") continue;
    entries.set(match[1], value);
  }

  const provider = entries.get("provider");
  if (!provider) return null;
  return {
    provider,
    owner: entries.get("owner"),
    repo: entries.get("repo"),
    url: entries.get("url"),
  };
}

export async function loadUpdateFeedConfig(
  configPath: string,
  read: (path: string) => Promise<string> = (path) => readFile(path, "utf-8"),
): Promise<UpdateFeedConfig | null> {
  try {
    return parseUpdateFeedConfig(await read(configPath));
  } catch {
    return null;
  }
}

/**
 * Mirrors `mac.artifactName` in apps/desktop/electron-builder.yml. Keep the two
 * in step: a rename there without a rename here turns every assisted download
 * into a 404.
 */
export function installerFileName(version: string, arch: string): string {
  return `multica-desktop-${version.replace(/^v/, "")}-mac-${arch}.dmg`;
}

/**
 * Where the offered version's `.dmg` lives on the feed the running build
 * already trusts for update metadata. Returns null for a provider whose asset
 * layout we cannot derive, so the caller falls back to the release page rather
 * than downloading from a guessed URL.
 */
export function resolveInstallerUrl(
  feed: UpdateFeedConfig | null,
  version: string,
  arch: string,
): string | null {
  if (!feed) return null;
  const fileName = installerFileName(version, arch);
  const tag = `v${version.replace(/^v/, "")}`;

  if (feed.provider === "github" && feed.owner && feed.repo) {
    return `https://github.com/${feed.owner}/${feed.repo}/releases/download/${tag}/${fileName}`;
  }
  if (feed.provider === "generic" && feed.url) {
    return `${feed.url.replace(/\/+$/, "")}/${fileName}`;
  }
  return null;
}

/** Directory the downloaded installers live in, inside the app's own data. */
export function installerCacheDir(userDataPath: string): string {
  // Deliberately not ~/Downloads: that folder is TCC-protected on recent
  // macOS, and a permission prompt in the middle of a background download is
  // exactly the interruption this path exists to avoid. The renderer opens and
  // reveals the file by absolute path, so its location never has to be typed.
  return join(userDataPath, "installers");
}

export interface DownloadedInstaller {
  path: string;
  fileName: string;
  bytes: number;
}

export interface DownloadInstallerOptions {
  url: string;
  destDir: string;
  fileName: string;
  /** Percent 0–100, emitted only when the rounded value changes. */
  onProgress?: (percent: number) => void;
  fetchImpl?: typeof fetch;
  signal?: AbortSignal;
}

/**
 * Stream `url` into `destDir/fileName`. Downloads to a `.part` sibling and
 * renames on success so an interrupted run can never leave a truncated file
 * that looks complete. A previously completed download of the same name is
 * reused when its size matches what the server reports.
 */
export async function downloadInstaller({
  url,
  destDir,
  fileName,
  onProgress,
  fetchImpl = fetch,
  signal,
}: DownloadInstallerOptions): Promise<DownloadedInstaller> {
  await mkdir(destDir, { recursive: true });
  const finalPath = join(destDir, fileName);
  const partPath = `${finalPath}.part`;

  const response = await fetchImpl(url, { signal, redirect: "follow" });
  if (!response.ok) {
    throw new Error(`Installer download failed: HTTP ${response.status} for ${url}`);
  }

  const total = Number(response.headers.get("content-length") ?? 0);
  const existing = await fileSize(finalPath);
  if (existing !== null && total > 0 && existing === total) {
    onProgress?.(100);
    await response.body?.cancel().catch(() => undefined);
    return { path: finalPath, fileName, bytes: existing };
  }

  if (!response.body) throw new Error(`Installer download returned no body: ${url}`);

  const handle = await open(partPath, "w");
  let received = 0;
  let lastPercent = -1;
  try {
    for await (const chunk of response.body as unknown as AsyncIterable<Uint8Array>) {
      await handle.write(chunk);
      received += chunk.byteLength;
      if (total > 0) {
        const percent = Math.min(100, Math.round((received / total) * 100));
        if (percent !== lastPercent) {
          lastPercent = percent;
          onProgress?.(percent);
        }
      }
    }
  } finally {
    await handle.close();
  }

  await rm(finalPath, { force: true });
  await rename(partPath, finalPath);
  if (lastPercent !== 100) onProgress?.(100);
  return { path: finalPath, fileName, bytes: received };
}

/**
 * Drop installers left over from earlier offers. Without this the cache grows
 * by ~220 MB per release the user never installed.
 */
export async function pruneInstallerCache(
  destDir: string,
  keepFileName: string,
): Promise<void> {
  let names: string[];
  try {
    names = await readdir(destDir);
  } catch {
    return;
  }
  await Promise.all(
    names
      .filter((name) => name !== keepFileName)
      .map((name) => rm(join(destDir, name), { force: true, recursive: true })),
  );
}

export interface FetchInstallerOptions {
  version: string;
  arch: string;
  /** Packaged `app-update.yml` — the same feed electron-updater reads. */
  configPath: string;
  userDataPath: string;
  onProgress?: (percent: number) => void;
  fetchImpl?: typeof fetch;
  signal?: AbortSignal;
  /** Test seams. */
  readConfig?: (path: string) => Promise<string>;
  download?: typeof downloadInstaller;
}

/**
 * The whole assisted path in one call: read the update feed the running build
 * was packaged against, derive the `.dmg` for `version`, and stream it into
 * the app's installer cache.
 */
export async function fetchInstallerForVersion({
  version,
  arch,
  configPath,
  userDataPath,
  onProgress,
  fetchImpl,
  signal,
  readConfig,
  download = downloadInstaller,
}: FetchInstallerOptions): Promise<DownloadedInstaller> {
  const feed = await loadUpdateFeedConfig(configPath, readConfig);
  const url = resolveInstallerUrl(feed, version, arch);
  if (!url) {
    throw new Error(
      `No macOS installer URL for v${version}: update feed at ${configPath} is ${
        feed ? `a "${feed.provider}" provider without asset coordinates` : "unreadable"
      }`,
    );
  }

  const fileName = installerFileName(version, arch);
  const destDir = installerCacheDir(userDataPath);
  await pruneInstallerCache(destDir, fileName);
  return download({ url, destDir, fileName, onProgress, fetchImpl, signal });
}

async function fileSize(path: string): Promise<number | null> {
  try {
    return (await stat(path)).size;
  } catch {
    return null;
  }
}
