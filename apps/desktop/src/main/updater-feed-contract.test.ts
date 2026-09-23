// @vitest-environment node
//
// Contract test against the real electron-updater providers.
//
// The channel name is a two-sided contract: `publishChannelForTarget` in
// scripts/package.mjs decides which `*.yml` a release carries, and
// `feedNameForReleaseChannel` decides which one an installed client asks for.
// Unit-testing those two against each other only proves they agree with our
// own idea of the providers — the mistake that first shipped a `beta*.yml`
// test channel while the client requested `test*.yml`. So this file hands the
// installed electron-updater the exact state `applyStableFeed` /
// `applyTestFeed` install and asserts the file names it reaches for.
import { describe, expect, it, vi } from "vitest";

vi.mock("electron", () => ({
  app: { getVersion: () => "0.0.0", getPath: () => "" },
  BrowserWindow: class BrowserWindow {},
  ipcMain: { handle: vi.fn() },
  shell: { openPath: vi.fn(), openExternal: vi.fn() },
}));
vi.mock("electron-updater", () => ({
  autoUpdater: {
    channel: null,
    allowDowngrade: false,
    allowPrerelease: false,
    autoDownload: true,
    autoInstallOnAppQuit: true,
    setFeedURL: vi.fn(),
    on: vi.fn(),
  },
}));
// electron-log/main loads the Electron binary on import, which a
// node-environment test has no business doing.
vi.mock("electron-log/main", () => ({
  default: {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    transports: { file: { getFile: () => null, resolvePathFn: null } },
  },
}));

import { GenericProvider } from "electron-updater/out/providers/GenericProvider.js";
import { GitHubProvider } from "electron-updater/out/providers/GitHubProvider.js";
import type { AppUpdater } from "electron-updater";
import type { ProviderRuntimeOptions } from "electron-updater/out/providers/Provider.js";
import { publishChannelForTarget } from "../../scripts/package.mjs";
import {
  applyStableFeed,
  applyTestFeed,
  type ChannelConfigurableUpdater,
  type UpdateFeedOptions,
} from "./updater";

const OWNER = "jeff-kunkun";
const REPO = "multica";
const TAG = "v0.5.5-test.3";

/** What GitHub serves at /<owner>/<repo>/releases.atom, newest first. */
const RELEASES_ATOM = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  ${[TAG, "v0.5.4", "v0.5.3"]
    .map(
      (tag) =>
        `<entry><link rel="alternate" type="text/html" href="https://github.com/${OWNER}/${REPO}/releases/tag/${tag}"/><title>${tag}</title><content>notes</content></entry>`,
    )
    .join("")}
</feed>`;

function manifestYaml(version: string): string {
  return [
    `version: ${version}`,
    "files:",
    `  - url: multica-desktop-${version}.zip`,
    "    sha512: c2hh",
    "    size: 1",
    `path: multica-desktop-${version}.zip`,
    "sha512: c2hh",
    "releaseDate: '2026-09-23T00:00:00.000Z'",
  ].join("\n");
}

/** A stand-in for AppUpdater carrying only the fields the helpers write. */
function fakeUpdater(): ChannelConfigurableUpdater & {
  feed: UpdateFeedOptions | null;
} {
  return {
    feed: null,
    channel: null,
    allowDowngrade: false,
    allowPrerelease: false,
    setFeedURL(options) {
      this.feed = options;
    },
  };
}

/** Every path the provider requested, in order. */
function recordingExecutor(body: (path: string) => string) {
  const requested: string[] = [];
  return {
    requested,
    executor: {
      request: async (options: { path?: string }) => {
        const path = options.path ?? "";
        requested.push(path);
        return body(path);
      },
    },
  };
}

// `platform` is what the providers read; the Linux manifest suffix comes from
// the *host* arch (`process.arch`), and TEST_UPDATER_ARCH is the override
// electron-updater ships for exactly this.
async function withArch<T>(arch: string, fn: () => Promise<T>): Promise<T> {
  const previous = process.env.TEST_UPDATER_ARCH;
  process.env.TEST_UPDATER_ARCH = arch;
  try {
    return await fn();
  } finally {
    if (previous == null) delete process.env.TEST_UPDATER_ARCH;
    else process.env.TEST_UPDATER_ARCH = previous;
  }
}

const CASES = [
  { platform: "darwin", builderPlatform: "mac", arch: "arm64", stable: "latest-mac.yml", test: "test-mac.yml" },
  { platform: "darwin", builderPlatform: "mac", arch: "x64", stable: "latest-x64-mac.yml", test: "test-x64-mac.yml" },
  { platform: "win32", builderPlatform: "win", arch: "x64", stable: "latest.yml", test: "test.yml" },
  { platform: "win32", builderPlatform: "win", arch: "arm64", stable: "latest-arm64.yml", test: "test-arm64.yml" },
  { platform: "linux", builderPlatform: "linux", arch: "x64", stable: "latest-linux.yml", test: "test-linux.yml" },
  { platform: "linux", builderPlatform: "linux", arch: "arm64", stable: "latest-linux-arm64.yml", test: "test-linux-arm64.yml" },
] as const;

describe("update feed contract", () => {
  // These names are what already-installed clients are polling right now.
  // Renaming any of them cuts every existing user off from updates.
  it.each(CASES)(
    "stable on $platform/$arch still asks for $stable",
    async ({ platform, arch, stable }) => {
      const updater = fakeUpdater();
      applyStableFeed(updater, "0.5.3", platform, arch);
      expect(updater.feed).toBeNull();

      const { requested, executor } = recordingExecutor((path) => {
        // The provider walks the releases atom feed first, then confirms the
        // newest non-prerelease tag through /releases/latest.
        if (path.endsWith(".atom")) return RELEASES_ATOM;
        if (path.endsWith("/releases/latest")) return JSON.stringify({ tag_name: "v0.5.4" });
        return manifestYaml("0.5.4");
      });
      const provider = new GitHubProvider(
        { provider: "github", owner: OWNER, repo: REPO },
        { ...updater, currentVersion: "0.5.3", fullChangelog: false } as unknown as AppUpdater,
        { platform, executor, isUseMultipleRangeRequest: false } as unknown as ProviderRuntimeOptions,
      );

      const info = await withArch(arch, () => provider.getLatestVersion());
      expect(info.version).toBe("0.5.4");
      expect(requested.at(-1)).toBe(`/${OWNER}/${REPO}/releases/download/v0.5.4/${stable}`);
    },
  );

  it.each(CASES)(
    "test on $platform/$arch asks for the $test that package.mjs publishes",
    async ({ platform, builderPlatform, arch, test: testFile }) => {
      const updater = fakeUpdater();
      applyTestFeed(updater, TAG, platform, arch);
      expect(updater.feed).toMatchObject({ provider: "generic" });

      const feed = updater.feed as Extract<UpdateFeedOptions, { provider: "generic" }>;
      const { requested, executor } = recordingExecutor(() => manifestYaml("0.5.5-test.3"));
      const provider = new GenericProvider(
        { provider: "generic", url: feed.url, channel: feed.channel },
        { ...updater, currentVersion: "0.5.4", isAddNoCacheQuery: false } as unknown as AppUpdater,
        { platform, executor, isUseMultipleRangeRequest: feed.useMultipleRangeRequest } as unknown as ProviderRuntimeOptions,
      );

      const info = await withArch(arch, () => provider.getLatestVersion());
      expect(info.version).toBe("0.5.5-test.3");
      expect(requested).toEqual([`/${OWNER}/${REPO}/releases/download/${TAG}/${testFile}`]);

      // The other half of the contract: electron-builder writes that exact
      // file only if package.mjs passes this publish channel.
      // package.mjs is plain JS, so annotate what it hands back rather than
      // letting the assertions below run against `any`.
      const publishChannel: string = publishChannelForTarget(
        "0.5.5-test.3",
        builderPlatform,
        arch,
      );
      expect(publishChannel).toBe(feed.channel);
      expect(testFile).toBe(`${publishChannel}${testFile.slice(publishChannel.length)}`);
      expect(testFile.startsWith(`${publishChannel}`)).toBe(true);
    },
  );
});
