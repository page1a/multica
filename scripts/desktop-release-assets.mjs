#!/usr/bin/env node
/**
 * Makes a Desktop release's asset set verifiable instead of assumed.
 *
 * `gh release upload` exits 0 even when GitHub truncates the transfer: the
 * failed asset is left behind in `state=starter` (a zombie that also blocks a
 * same-name re-upload until it is deleted) while the small metadata files land
 * fine. That is exactly how v0.4.56/v0.4.57 shipped `latest-mac.yml` pointing
 * at a DMG and a ZIP that were never uploaded, leaving installed clients with
 * a dead auto-update feed while every command along the way reported success.
 *
 * The upload path therefore ends with an explicit per-asset comparison against
 * the local build output: every expected file must exist on the release, be
 * `state=uploaded`, and match the local byte size (and the server-side sha256
 * digest when GitHub reports one). A truncated upload fails the run loudly.
 *
 * Subcommands (all accept --repo, defaulting to $GITHUB_REPOSITORY and then to
 * the owner/repo in apps/desktop/electron-builder.yml):
 *
 *   check  --tag <tag> --dist <dir>   verify the release against a local build
 *   clean  --tag <tag>                delete non-uploaded (zombie) assets
 *   upload --tag <tag> --dist <dir>   upload every asset, update feeds last
 *
 * `upload --dry-run` prints the exact `gh` invocations instead of running them.
 *
 * `--scope-dist` narrows `check` and `clean` to the assets the local build
 * actually produced. The macOS, Linux and Windows packaging jobs publish to one
 * release in parallel, so every remote asset a job did not build is another
 * job's business: unscoped, one job's `clean` deletes a sibling's 200 MB upload
 * while it is still streaming, and one job's `check` fails on a sibling's
 * in-flight `state=starter` asset. Scoped, each job judges and repairs only its
 * own artifacts. Leave it off for a single-job release, where an unexplained
 * zombie really is this run's debris.
 *
 * The `gh` binary is overridable through DESKTOP_RELEASE_GH so tests can drive
 * the command with a fake executable.
 */

import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { readdirSync, readFileSync, statSync, existsSync } from "node:fs";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

/** Installer payloads: the files a user or the updater actually downloads. */
const PAYLOAD_PATTERN = /\.(?:dmg|zip|exe|AppImage|deb|rpm)$/;
/** electron-updater block maps, published alongside each payload. */
const BLOCKMAP_PATTERN = /\.blockmap$/;
/**
 * electron-updater feed metadata. Stable clients already fetch `latest*.yml`
 * (`latest-mac.yml`, `latest-x64-mac.yml`, `latest-arm64.yml`, ...). The test
 * channel publishes the same shapes with a `test` prefix.
 */
const FEED_PATTERN = /^(?:latest|test)(?:-[A-Za-z0-9._-]+)?\.ya?ml$/;

const DEFAULT_REPO = "jeff-kunkun/multica";

/**
 * Upload order. Payloads go first and the feed metadata last: the feed
 * advertises the sizes and sha512 values of the payloads, so publishing it
 * before they exist would hand clients a manifest that 404s — the exact state
 * v0.4.56/v0.4.57 are stuck in.
 */
export function assetRank(name) {
  if (FEED_PATTERN.test(name)) return 2;
  if (BLOCKMAP_PATTERN.test(name)) return 1;
  if (PAYLOAD_PATTERN.test(name)) return 0;
  return -1;
}

export function isReleaseAssetName(name) {
  return assetRank(name) >= 0;
}

/** Order assets so the feed metadata is uploaded last. */
export function orderAssetsForUpload(assets) {
  return [...assets].sort((left, right) => {
    const rank = assetRank(left.name) - assetRank(right.name);
    if (rank !== 0) return rank;
    return left.name < right.name ? -1 : left.name > right.name ? 1 : 0;
  });
}

/**
 * Every release asset in a build output directory, by basename. electron-builder
 * writes single-target builds at the output root and multi-target builds into
 * per-target subdirectories, so the walk is recursive; a basename that appears
 * twice would make the asset list ambiguous and is rejected rather than picked
 * arbitrarily.
 *
 * The recursion skips electron-builder's staging trees (see
 * `isStagingDirectory`). They are inputs to the installers, not assets — and on
 * Windows they are the reason a `--x64 --arm64` run cannot be walked naively:
 * `win-unpacked/` and `win-arm64-unpacked/` each hold a `Multica.exe`, whose
 * extension makes it look like an installer payload, so the second one hits the
 * duplicate-name guard and fails the job after the packaging already succeeded
 * (v0.4.64).
 */
/**
 * electron-builder staging output: the unpacked application trees it assembles
 * before wrapping them into installers (`win-unpacked`, `linux-arm64-unpacked`,
 * `mac-arm64/Multica.app`, ...). Nothing inside one is ever published, and the
 * app executable's name collides across architectures.
 */
export function isStagingDirectory(name) {
  return name.endsWith("-unpacked") || name.endsWith(".app");
}

export function collectLocalAssets(distDir) {
  const root = resolve(distDir);
  if (!existsSync(root) || !statSync(root).isDirectory()) {
    throw new Error(`[assets] build output directory not found: ${root}`);
  }

  const found = new Map();
  const walk = (directory) => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const path = join(directory, entry.name);
      if (entry.isSymbolicLink()) continue;
      if (entry.isDirectory()) {
        if (!isStagingDirectory(entry.name)) walk(path);
        continue;
      }
      if (!entry.isFile() || !isReleaseAssetName(entry.name)) continue;
      if (found.has(entry.name)) {
        throw new Error(
          `[assets] duplicate asset name in the build output: ${entry.name}`,
        );
      }
      const { size } = statSync(path);
      if (size === 0) {
        throw new Error(`[assets] refusing to publish empty asset: ${path}`);
      }
      found.set(entry.name, { name: entry.name, path, size });
    }
  };
  walk(root);

  if (found.size === 0) {
    throw new Error(
      `[assets] no release assets found under ${root}; expected installers, blockmaps, or latest*.yml`,
    );
  }
  return [...found.values()].sort((left, right) =>
    left.name < right.name ? -1 : 1,
  );
}

export function sha256File(path) {
  return createHash("sha256").update(readFileSync(path)).digest("hex");
}

/** `v0.4.57` → `0.4.57`; used to sanity-check the tag against the artifacts. */
export function releaseVersionFromTag(tag) {
  return tag.replace(/^v/, "");
}

function runGh(args, { ghBin = process.env.DESKTOP_RELEASE_GH || "gh" } = {}) {
  try {
    const stdout = execFileSync(ghBin, args, {
      encoding: "utf-8",
      stdio: ["ignore", "pipe", "pipe"],
      maxBuffer: 64 * 1024 * 1024,
    });
    return { ok: true, stdout, stderr: "" };
  } catch (error) {
    const stderr = `${error.stderr ?? ""}`;
    const stdout = `${error.stdout ?? ""}`;
    return { ok: false, stdout, stderr: stderr || `${error.message ?? ""}` };
  }
}

function isNotFound(result) {
  return /HTTP 404|Not Found/i.test(`${result.stderr}${result.stdout}`);
}

/**
 * Assets attached to the release for `tag`, including `state=starter` zombies
 * that `gh release view` hides. Returns null when the repo has no such release.
 *
 * The release id is looked up first because the asset list lives under
 * `/releases/{id}/assets`; `/releases/tags/{tag}/assets` does not exist and
 * answers 404, which would read as "no release" and silently skip the check.
 */
export function readReleaseAssets({ repo, tag, ghBin } = {}) {
  const release = runGh(
    ["api", `repos/${repo}/releases/tags/${tag}`, "--jq", ".id"],
    { ghBin },
  );
  if (!release.ok) {
    if (isNotFound(release)) return null;
    throw new Error(
      `[assets] gh api failed for ${repo} ${tag}: ${release.stderr.trim() || release.stdout.trim()}`,
    );
  }
  const releaseId = release.stdout.trim();

  const result = runGh(
    [
      "api",
      "--paginate",
      `repos/${repo}/releases/${releaseId}/assets`,
      "--jq",
      `.[] | [.id, .name, .state, .size, (.digest // "-")] | @tsv`,
    ],
    { ghBin },
  );
  if (!result.ok) {
    if (isNotFound(result)) return [];
    throw new Error(
      `[assets] gh api failed for ${repo} release ${releaseId}: ${result.stderr.trim() || result.stdout.trim()}`,
    );
  }

  return result.stdout
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line.length > 0)
    .map((line) => {
      const [id, name, state, size, digest] = line.split("\t");
      return {
        id: Number(id),
        name,
        state,
        size: Number(size),
        digest: digest === "-" ? null : digest,
      };
    });
}

function formatBytes(value) {
  if (!Number.isFinite(value)) return "unknown";
  return `${value} B`;
}

/**
 * The comparison that turns a silent truncation into a failed run.
 *
 * A local asset is `ok` only when the release carries it, is `state=uploaded`,
 * and matches on size — plus the sha256 digest when GitHub reports one. Any
 * remote asset still in a non-uploaded state is reported as a zombie even when
 * its name is not part of this build, because it is the debris a failed upload
 * leaves behind and it blocks the next same-name upload.
 */
export function compareReleaseAssets(local, remote, { scopeToLocal = false } = {}) {
  const remoteByName = new Map(remote.map((asset) => [asset.name, asset]));
  const rows = [];

  for (const asset of local) {
    const published = remoteByName.get(asset.name);
    if (!published) {
      rows.push({
        name: asset.name,
        status: "missing",
        detail: "missing from the release",
        localSize: asset.size,
      });
      continue;
    }
    remoteByName.delete(asset.name);
    if (published.state !== "uploaded") {
      rows.push({
        name: asset.name,
        status: "not-uploaded",
        detail: `release asset is state=${published.state} (truncated upload)`,
        localSize: asset.size,
        remoteSize: published.size,
        remoteState: published.state,
      });
      continue;
    }
    if (published.size !== asset.size) {
      rows.push({
        name: asset.name,
        status: "size-mismatch",
        detail: `size ${formatBytes(published.size)} on the release vs ${formatBytes(asset.size)} locally`,
        localSize: asset.size,
        remoteSize: published.size,
        remoteState: published.state,
      });
      continue;
    }
    const localDigest = asset.sha256 ? `sha256:${asset.sha256}` : null;
    if (published.digest && localDigest && published.digest !== localDigest) {
      rows.push({
        name: asset.name,
        status: "digest-mismatch",
        detail: `sha256 ${published.digest} on the release vs ${localDigest} locally`,
        localSize: asset.size,
        remoteSize: published.size,
        remoteState: published.state,
      });
      continue;
    }
    rows.push({
      name: asset.name,
      status: "ok",
      detail: `state=uploaded, ${formatBytes(published.size)}`,
      localSize: asset.size,
      remoteSize: published.size,
      remoteState: published.state,
    });
  }

  // Scoped runs stop here: what is left in `remoteByName` belongs to a
  // sibling packaging job, and its upload state is not this job's verdict to
  // give.
  if (scopeToLocal) {
    const failures = rows.filter((row) => row.status !== "ok");
    return { ok: failures.length === 0, rows, failures };
  }

  for (const leftover of remoteByName.values()) {
    if (leftover.state !== "uploaded") {
      rows.push({
        name: leftover.name,
        status: "zombie",
        detail: `stale asset left by a failed upload (state=${leftover.state})`,
        remoteSize: leftover.size,
        remoteState: leftover.state,
      });
    } else {
      rows.push({
        name: leftover.name,
        status: "extra",
        detail: `uploaded asset that this build does not produce (state=uploaded)`,
        remoteSize: leftover.size,
        remoteState: leftover.state,
      });
    }
  }

  const failures = rows.filter(
    (row) => row.status !== "ok" && row.status !== "extra",
  );
  return { ok: failures.length === 0, rows, failures };
}

/** Remote assets that never finished uploading. */
export function zombieAssets(remote) {
  return remote.filter((asset) => asset.state !== "uploaded");
}

export function deleteAsset({ repo, id, ghBin }) {
  const result = runGh(
    ["api", "-X", "DELETE", `repos/${repo}/releases/assets/${id}`],
    { ghBin },
  );
  // A concurrent run may have deleted it first; that is the desired end state.
  if (!result.ok && !/HTTP 404|Not Found/i.test(result.stderr)) {
    throw new Error(
      `[assets] failed to delete asset ${id} on ${repo}: ${result.stderr.trim()}`,
    );
  }
  return result.ok;
}

/**
 * A numeric flag has to be rejected at the boundary: a NaN retry budget would
 * make the check loop run zero times and report nothing at all.
 */
function positiveNumber(flag, value) {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed < 0) {
    throw new Error(`[assets] ${flag} needs a non-negative number, got ${value}`);
  }
  return parsed;
}

export function parseArgs(argv) {
  const options = {
    command: null,
    tag: null,
    repo: process.env.GITHUB_REPOSITORY || DEFAULT_REPO,
    dist: "apps/desktop/dist",
    attempts: 1,
    delayMs: 5000,
    dryRun: false,
    json: false,
    sizeOnly: false,
    scopeDist: false,
  };

  const positional = [];
  for (let i = 0; i < argv.length; i += 1) {
    const token = argv[i];
    if (token === "--dry-run") {
      options.dryRun = true;
      continue;
    }
    if (token === "--json") {
      options.json = true;
      continue;
    }
    if (token === "--size-only") {
      options.sizeOnly = true;
      continue;
    }
    if (token === "--scope-dist") {
      options.scopeDist = true;
      continue;
    }
    if (token === "--help" || token === "-h") {
      options.help = true;
      continue;
    }
    if (token.startsWith("--")) {
      const [flag, inline] = token.includes("=")
        ? [token.slice(0, token.indexOf("=")), token.slice(token.indexOf("=") + 1)]
        : [token, null];
      const value = inline ?? argv[++i];
      if (value === undefined) throw new Error(`[assets] ${flag} needs a value`);
      if (flag === "--tag") options.tag = value;
      else if (flag === "--repo") options.repo = value;
      else if (flag === "--dist") options.dist = value;
      else if (flag === "--attempts") options.attempts = positiveNumber(flag, value);
      else if (flag === "--delay-ms") options.delayMs = positiveNumber(flag, value);
      else throw new Error(`[assets] unknown flag: ${flag}`);
      continue;
    }
    positional.push(token);
  }

  options.command = positional[0] ?? null;
  if (positional.length > 1) {
    throw new Error(`[assets] unexpected argument: ${positional[1]}`);
  }
  if (options.command && !options.help) {
    if (!["check", "clean", "upload"].includes(options.command)) {
      throw new Error(`[assets] unknown subcommand: ${options.command}`);
    }
    if (!options.tag) throw new Error("[assets] --tag is required");
    if (!/^v[0-9]+\.[0-9]+\.[0-9]+/.test(options.tag)) {
      throw new Error(`[assets] --tag must look like vX.Y.Z, got ${options.tag}`);
    }
  }
  return options;
}

function usage() {
  return [
    "Usage:",
    "  node scripts/desktop-release-assets.mjs check  --tag vX.Y.Z [--dist <dir>] [--repo <owner/name>] [--attempts N] [--delay-ms N] [--size-only] [--scope-dist]",
    "  node scripts/desktop-release-assets.mjs clean  --tag vX.Y.Z [--repo <owner/name>] [--dist <dir>] [--scope-dist]",
    "  node scripts/desktop-release-assets.mjs upload --tag vX.Y.Z [--dist <dir>] [--repo <owner/name>] [--dry-run]",
    "",
    "check   fails unless every locally built asset is state=uploaded, matches the local size,",
    "        and matches the server-side sha256 when GitHub reports one (--size-only skips the hash)",
    "clean   deletes non-uploaded (zombie) release assets so a re-upload can reuse the name",
    "upload  uploads every locally built asset with --clobber, feed metadata last",
    "",
    "--scope-dist  limit check/clean to the assets under --dist; required when several",
    "              packaging jobs publish to one release in parallel",
  ].join("\n");
}

function reportRows(rows, { stream = console.log } = {}) {
  for (const row of rows) {
    const label = row.status === "ok" ? "ok  " : row.status === "extra" ? "note" : "FAIL";
    stream(`${label}  ${row.name} — ${row.detail}`);
  }
}

function sleepSync(ms) {
  if (ms <= 0) return;
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);
}

function checkCommand(options) {
  const local = collectLocalAssets(options.dist).map((asset) => ({
    ...asset,
    // Skipping the hash is for auditing a release whose original build output
    // is gone; the CI gate always hashes, so a same-size rewrite cannot pass.
    sha256: options.sizeOnly ? null : sha256File(asset.path),
  }));
  const version = releaseVersionFromTag(options.tag);
  // A build whose artifacts carry a different version than the tag is usually a
  // stale `dist/` from an earlier run; publishing it would ship an installer the
  // update feed for this tag does not describe.
  const offVersion = local.filter(
    (asset) => !FEED_PATTERN.test(asset.name) && !asset.name.includes(version),
  );
  if (offVersion.length > 0) {
    console.log(
      `[assets] warning: ${offVersion.length} asset(s) do not carry the tag version ${version}: ${offVersion
        .map((asset) => asset.name)
        .join(", ")}`,
    );
  }

  let comparison = null;
  for (let attempt = 1; attempt <= Math.max(1, options.attempts); attempt += 1) {
    const remote = readReleaseAssets(options);
    if (remote === null) {
      comparison = {
        ok: false,
        rows: [],
        failures: [
          {
            name: options.tag,
            status: "missing",
            detail: `no release exists for ${options.tag} on ${options.repo}`,
          },
        ],
      };
    } else {
      comparison = compareReleaseAssets(local, remote, {
        scopeToLocal: options.scopeDist,
      });
    }
    if (comparison.ok) break;
    if (attempt < options.attempts) {
      console.log(
        `[assets] attempt ${attempt}/${options.attempts} not clean yet; retrying in ${options.delayMs}ms`,
      );
      sleepSync(options.delayMs);
    }
  }

  console.log(
    `[assets] ${options.tag} on ${options.repo} → ${local.length} locally built asset(s) from ${resolve(options.dist)}`,
  );
  reportRows(comparison.rows);
  if (options.json) {
    console.log(JSON.stringify({ tag: options.tag, repo: options.repo, ...comparison }, null, 2));
  }

  if (!comparison.ok) {
    console.error(
      `[assets] FAILED: ${comparison.failures.length} of ${comparison.rows.length} asset check(s) did not pass for ${options.tag}`,
    );
    return 1;
  }
  console.log(
    `[assets] OK: every one of the ${local.length} asset(s) for ${options.tag} is state=uploaded with a verified size`,
  );
  return 0;
}

function cleanCommand(options) {
  const remote = readReleaseAssets(options);
  if (remote === null) {
    console.log(`[assets] no release for ${options.tag} on ${options.repo}; nothing to clean`);
    return 0;
  }
  const scope = options.scopeDist
    ? new Set(collectLocalAssets(options.dist).map((asset) => asset.name))
    : null;
  const zombies = zombieAssets(remote).filter(
    (asset) => scope === null || scope.has(asset.name),
  );
  if (zombies.length === 0) {
    console.log(`[assets] no zombie assets on ${options.tag}`);
    return 0;
  }
  for (const asset of zombies) {
    if (options.dryRun) {
      console.log(`[assets] would delete ${asset.name} (state=${asset.state})`);
      continue;
    }
    deleteAsset({ repo: options.repo, id: asset.id, ghBin: process.env.DESKTOP_RELEASE_GH });
    console.log(`[assets] deleted ${asset.name} (state=${asset.state})`);
  }
  return 0;
}

/**
 * Assets grouped into upload waves, payloads first and feed metadata last.
 *
 * The ordering has to survive `gh release upload` itself: that command uploads
 * the files it is given with five concurrent workers, so handing it all five
 * assets in one invocation lets the 16 KB `latest-mac.yml` finish long before
 * the 230 MB DMG — the manifest goes live pointing at a payload that may still
 * fail. One invocation per rank, awaited in turn, is what actually keeps the
 * feed behind the bytes it describes.
 */
export function uploadWaves(assets) {
  const waves = new Map();
  for (const asset of orderAssetsForUpload(assets)) {
    const rank = assetRank(asset.name);
    if (!waves.has(rank)) waves.set(rank, []);
    waves.get(rank).push(asset);
  }
  return [...waves.entries()]
    .sort((left, right) => left[0] - right[0])
    .map(([, group]) => group);
}

function uploadArgs(options, wave) {
  return [
    "release",
    "upload",
    options.tag,
    "--repo",
    options.repo,
    "--clobber",
    ...wave.map((asset) => asset.path),
  ];
}

function uploadCommand(options) {
  const waves = uploadWaves(collectLocalAssets(options.dist));
  const ghBin = process.env.DESKTOP_RELEASE_GH || "gh";
  let uploaded = 0;

  for (const wave of waves) {
    const args = uploadArgs(options, wave);
    if (options.dryRun) {
      console.log(
        `[assets] ${ghBin} ${args.map((arg) => (arg.includes(" ") ? JSON.stringify(arg) : arg)).join(" ")}`,
      );
      uploaded += wave.length;
      continue;
    }
    const result = runGh(args, { ghBin });
    if (!result.ok) {
      console.error(
        `[assets] gh release upload failed for ${wave.map((asset) => asset.name).join(", ")}: ${result.stderr.trim()}`,
      );
      return 1;
    }
    uploaded += wave.length;
    console.log(
      `[assets] uploaded ${wave.map((asset) => asset.name).join(", ")}`,
    );
  }

  console.log(
    `[assets] requested upload of ${uploaded} asset(s) in ${waves.length} wave(s); feed metadata last`,
  );
  return 0;
}

function main(argv) {
  let options;
  try {
    options = parseArgs(argv);
  } catch (error) {
    console.error(error.message);
    console.error(usage());
    return 2;
  }

  if (options.help || !options.command) {
    console.log(usage());
    return options.help ? 0 : 2;
  }

  try {
    if (options.command === "check") return checkCommand(options);
    if (options.command === "clean") return cleanCommand(options);
    return uploadCommand(options);
  } catch (error) {
    console.error(error.message);
    return 1;
  }
}

if (resolve(process.argv[1] ?? "") === fileURLToPath(import.meta.url)) {
  process.exit(main(process.argv.slice(2)));
}
