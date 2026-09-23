// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { mkdtempSync, readFileSync, rmSync, writeFileSync, existsSync } from "node:fs";
import { readdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

import {
  downloadInstaller,
  fetchInstallerForVersion,
  installerCacheDir,
  installerFileName,
  parseUpdateFeedConfig,
  pruneInstallerCache,
  resolveInstallerUrl,
} from "./mac-installer";

const GITHUB_FEED = `version: 0.5.4
provider: github
owner: jeff-kunkun
repo: multica
updaterCacheDirName: multica-desktop-updater
`;

function respondWith(body: Uint8Array, headers: Record<string, string> = {}) {
  return new Response(body.buffer as ArrayBuffer, {
    status: 200,
    headers: { "content-length": String(body.byteLength), ...headers },
  });
}

describe("parseUpdateFeedConfig", () => {
  it("reads the provider coordinates electron-builder writes", () => {
    expect(parseUpdateFeedConfig(GITHUB_FEED)).toEqual({
      provider: "github",
      owner: "jeff-kunkun",
      repo: "multica",
      url: undefined,
    });
  });

  it("reads a generic feed url", () => {
    expect(
      parseUpdateFeedConfig('provider: generic\nurl: "http://127.0.0.1:8080/"\n'),
    ).toMatchObject({ provider: "generic", url: "http://127.0.0.1:8080/" });
  });

  it("ignores nested values instead of mis-reading them as top-level keys", () => {
    const feed = parseUpdateFeedConfig(
      "provider: github\nowner: jeff-kunkun\nrepo: multica\nnested:\n  repo: someone-else\n",
    );

    expect(feed?.repo).toBe("multica");
  });

  it("returns null when there is no provider to act on", () => {
    expect(parseUpdateFeedConfig("# just a comment\n")).toBeNull();
  });
});

describe("resolveInstallerUrl", () => {
  it("points at the release asset for a github feed", () => {
    expect(
      resolveInstallerUrl(parseUpdateFeedConfig(GITHUB_FEED), "0.5.5", "arm64"),
    ).toBe(
      "https://github.com/jeff-kunkun/multica/releases/download/v0.5.5/multica-desktop-0.5.5-mac-arm64.dmg",
    );
  });

  it("serves a generic feed from its own root", () => {
    expect(
      resolveInstallerUrl(
        { provider: "generic", url: "http://127.0.0.1:8080/" },
        "0.5.5",
        "x64",
      ),
    ).toBe("http://127.0.0.1:8080/multica-desktop-0.5.5-mac-x64.dmg");
  });

  it("gives up rather than guessing for a provider without asset coordinates", () => {
    expect(resolveInstallerUrl({ provider: "s3" }, "0.5.5", "arm64")).toBeNull();
    expect(resolveInstallerUrl({ provider: "github" }, "0.5.5", "arm64")).toBeNull();
    expect(resolveInstallerUrl(null, "0.5.5", "arm64")).toBeNull();
  });

  it("matches the artifact name electron-builder produces", () => {
    expect(installerFileName("v0.5.5", "arm64")).toBe(
      "multica-desktop-0.5.5-mac-arm64.dmg",
    );
  });
});

describe("downloadInstaller", () => {
  let dir: string;

  beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), "multica-installer-test-"));
  });

  afterEach(() => {
    rmSync(dir, { recursive: true, force: true });
  });

  it("streams the installer to disk and reports progress", async () => {
    const body = new Uint8Array(400).fill(7);
    const percents: number[] = [];

    const result = await downloadInstaller({
      url: "https://example.test/multica.dmg",
      destDir: dir,
      fileName: "multica.dmg",
      onProgress: (percent) => percents.push(percent),
      fetchImpl: async () => respondWith(body),
    });

    expect(result).toMatchObject({ fileName: "multica.dmg", bytes: 400 });
    expect(readFileSync(result.path)).toHaveLength(400);
    expect(percents.at(-1)).toBe(100);
  });

  it("never leaves a truncated file behind under the final name", async () => {
    const fetchImpl = async () =>
      new Response(
        new ReadableStream<Uint8Array>({
          start(controller) {
            controller.enqueue(new Uint8Array(10));
            controller.error(new Error("connection reset"));
          },
        }),
        { headers: { "content-length": "400" } },
      );

    await expect(
      downloadInstaller({
        url: "https://example.test/multica.dmg",
        destDir: dir,
        fileName: "multica.dmg",
        fetchImpl,
      }),
    ).rejects.toThrow("connection reset");

    expect(existsSync(join(dir, "multica.dmg"))).toBe(false);
  });

  it("reuses a complete download instead of pulling 220 MB again", async () => {
    // Same size, different bytes: if the body were streamed over the existing
    // file, the content check below would see the replacement.
    writeFileSync(join(dir, "multica.dmg"), new Uint8Array(400).fill(7));
    const percents: number[] = [];

    const result = await downloadInstaller({
      url: "https://example.test/multica.dmg",
      destDir: dir,
      fileName: "multica.dmg",
      onProgress: (percent) => percents.push(percent),
      fetchImpl: async () => respondWith(new Uint8Array(400).fill(9)),
    });

    expect(result.bytes).toBe(400);
    expect(readFileSync(result.path).every((byte) => byte === 7)).toBe(true);
    expect(percents).toEqual([100]);
  });

  it("reports an HTTP failure with the URL that produced it", async () => {
    await expect(
      downloadInstaller({
        url: "https://example.test/missing.dmg",
        destDir: dir,
        fileName: "missing.dmg",
        fetchImpl: async () => new Response("nope", { status: 404 }),
      }),
    ).rejects.toThrow("HTTP 404 for https://example.test/missing.dmg");
  });
});

describe("pruneInstallerCache", () => {
  it("drops installers for versions that were never installed", async () => {
    const dir = mkdtempSync(join(tmpdir(), "multica-installer-prune-"));
    writeFileSync(join(dir, "multica-desktop-0.5.4-mac-arm64.dmg"), "old");
    writeFileSync(join(dir, "multica-desktop-0.5.5-mac-arm64.dmg"), "new");

    await pruneInstallerCache(dir, "multica-desktop-0.5.5-mac-arm64.dmg");

    await expect(readdir(dir)).resolves.toEqual([
      "multica-desktop-0.5.5-mac-arm64.dmg",
    ]);
    rmSync(dir, { recursive: true, force: true });
  });

  it("is a no-op before the first download has created the directory", async () => {
    await expect(pruneInstallerCache("/no/such/dir", "x.dmg")).resolves.toBeUndefined();
  });
});

describe("fetchInstallerForVersion", () => {
  let userData: string;

  beforeEach(() => {
    userData = mkdtempSync(join(tmpdir(), "multica-userdata-"));
  });

  afterEach(() => {
    rmSync(userData, { recursive: true, force: true });
  });

  it("downloads the .dmg the running build's own feed points at", async () => {
    const fetchImpl = vi.fn(async () => respondWith(new Uint8Array(64)));

    const result = await fetchInstallerForVersion({
      version: "0.5.5",
      arch: "arm64",
      configPath: "/Applications/Multica.app/Contents/Resources/app-update.yml",
      userDataPath: userData,
      readConfig: async () => GITHUB_FEED,
      fetchImpl,
    });

    expect(fetchImpl).toHaveBeenCalledWith(
      "https://github.com/jeff-kunkun/multica/releases/download/v0.5.5/multica-desktop-0.5.5-mac-arm64.dmg",
      expect.anything(),
    );
    expect(result.path).toBe(
      join(installerCacheDir(userData), "multica-desktop-0.5.5-mac-arm64.dmg"),
    );
  });

  it("fails loudly when the feed cannot be read, rather than guessing a URL", async () => {
    const fetchImpl = vi.fn();

    await expect(
      fetchInstallerForVersion({
        version: "0.5.5",
        arch: "arm64",
        configPath: "/nope/app-update.yml",
        userDataPath: userData,
        readConfig: async () => {
          throw new Error("ENOENT");
        },
        fetchImpl: fetchImpl as unknown as typeof fetch,
      }),
    ).rejects.toThrow("unreadable");
    expect(fetchImpl).not.toHaveBeenCalled();
  });
});
