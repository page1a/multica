import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import {
  chmodSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import test from "node:test";

import {
  assetRank,
  collectLocalAssets,
  compareReleaseAssets,
  orderAssetsForUpload,
  parseArgs,
  readReleaseAssets,
  uploadWaves,
  releaseVersionFromTag,
  zombieAssets,
} from "./desktop-release-assets.mjs";

const script = resolve("scripts/desktop-release-assets.mjs");

function tempDir() {
  return mkdtempSync(join(tmpdir(), "desktop-release-assets-"));
}

function writeFile(path, bytes) {
  writeFileSync(path, "x".repeat(bytes));
}

/** A fake `gh` that replays canned responses keyed by the argv it receives. */
function fakeGh(dir, { assets = [], apiStatus = 0 } = {}) {
  const path = join(dir, "fake-gh");
  const body = [
    "#!/usr/bin/env node",
    'const fs = require("node:fs");',
    'if (process.env.FAKE_GH_RECORD) fs.appendFileSync(process.env.FAKE_GH_RECORD, JSON.stringify(process.argv.slice(2)) + "\\n");',
    `const status = ${apiStatus};`,
    `const assets = ${JSON.stringify(assets)};`,
    "const args = process.argv.slice(2).join(' ');",
    'if (status !== 0) { console.error("HTTP 404: Not Found"); process.exit(1); }',
    '// Release lookup answers with the id; the asset list answers with the TSV.',
    'if (!args.includes("/assets")) { process.stdout.write("424242\\n"); process.exit(0); }',
    'process.stdout.write(assets.map((a) => [a.id, a.name, a.state, a.size, a.digest ?? "-"].join("\\t")).join("\\n") + "\\n");',
  ].join("\n");
  writeFileSync(path, body);
  chmodSync(path, 0o755);
  return path;
}

test("treats test feeds as metadata and leaves stable feed names unchanged", () => {
  for (const name of [
    "latest.yml",
    "latest-mac.yml",
    "latest-x64-mac.yml",
    "latest-arm64.yml",
    "latest-linux.yml",
    "latest-linux-arm64.yml",
    "test.yml",
    "test-mac.yml",
    "test-x64-mac.yml",
    "test-arm64.yml",
    "test-linux.yml",
    "test-linux-arm64.yml",
  ]) {
    assert.equal(assetRank(name), 2, name);
  }
  assert.equal(assetRank("builder-debug.yml"), -1);
  assert.equal(assetRank("beta-mac.yml"), -1);
  assert.equal(assetRank("latest-mac.yml.blockmap"), 1);
});

test("uploads payloads before blockmaps and feed metadata last", () => {
  const ordered = orderAssetsForUpload([
    { name: "latest-mac.yml" },
    { name: "multica-desktop-0.4.57-mac-arm64.dmg.blockmap" },
    { name: "multica-desktop-0.4.57-mac-arm64.zip" },
    { name: "multica-desktop-0.4.57-mac-arm64.dmg" },
  ]).map((asset) => asset.name);
  assert.deepEqual(ordered, [
    "multica-desktop-0.4.57-mac-arm64.dmg",
    "multica-desktop-0.4.57-mac-arm64.zip",
    "multica-desktop-0.4.57-mac-arm64.dmg.blockmap",
    "latest-mac.yml",
  ]);
});

test("collects only publishable files, recursively and by basename", () => {
  const dir = tempDir();
  try {
    writeFile(join(dir, "multica-desktop-0.4.57-mac-arm64.dmg"), 8);
    writeFile(join(dir, "latest-mac.yml"), 3);
    mkdirSync(join(dir, "mac-arm64"));
    writeFile(join(dir, "mac-arm64", "multica-desktop-0.4.57-mac-arm64.zip"), 5);
    writeFile(join(dir, "builder-debug.yml"), 2);
    writeFile(join(dir, "notes.txt"), 2);

    const assets = collectLocalAssets(dir);
    assert.deepEqual(
      assets.map((asset) => [asset.name, asset.size]),
      [
        ["latest-mac.yml", 3],
        ["multica-desktop-0.4.57-mac-arm64.dmg", 8],
        ["multica-desktop-0.4.57-mac-arm64.zip", 5],
      ],
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("refuses an ambiguous or empty build output instead of guessing", () => {
  const dir = tempDir();
  try {
    mkdirSync(join(dir, "one"));
    mkdirSync(join(dir, "two"));
    writeFile(join(dir, "one", "latest-mac.yml"), 3);
    writeFile(join(dir, "two", "latest-mac.yml"), 3);
    assert.throws(() => collectLocalAssets(dir), /duplicate asset name/);

    const empty = join(dir, "empty");
    mkdirSync(empty);
    assert.throws(() => collectLocalAssets(empty), /no release assets found/);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("fails when a truncated upload left a starter asset behind", () => {
  // The v0.4.57 shape: metadata uploaded, payload stuck in state=starter.
  const local = [
    { name: "multica-desktop-0.4.57-mac-arm64.dmg", size: 231433659, sha256: "a".repeat(64) },
    { name: "multica-desktop-0.4.57-mac-arm64.zip", size: 221627956, sha256: "b".repeat(64) },
    { name: "latest-mac.yml", size: 537, sha256: "c".repeat(64) },
  ];
  const remote = [
    { id: 1, name: "multica-desktop-0.4.57-mac-arm64.dmg", state: "starter", size: 231433659, digest: null },
    { id: 2, name: "multica-desktop-0.4.57-mac-arm64.zip", state: "starter", size: 221627956, digest: null },
    { id: 3, name: "latest-mac.yml", state: "uploaded", size: 537, digest: `sha256:${"c".repeat(64)}` },
  ];

  const comparison = compareReleaseAssets(local, remote);
  assert.equal(comparison.ok, false);
  assert.deepEqual(
    comparison.failures.map((row) => [row.name, row.status]),
    [
      ["multica-desktop-0.4.57-mac-arm64.dmg", "not-uploaded"],
      ["multica-desktop-0.4.57-mac-arm64.zip", "not-uploaded"],
    ],
  );
  assert.deepEqual(
    zombieAssets(remote).map((asset) => asset.name),
    ["multica-desktop-0.4.57-mac-arm64.dmg", "multica-desktop-0.4.57-mac-arm64.zip"],
  );
});

test("a zero exit code from gh is not enough: size and digest must match", () => {
  const local = [
    { name: "multica-desktop-0.4.57-mac-arm64.dmg", size: 100, sha256: "a".repeat(64) },
  ];
  const truncated = compareReleaseAssets(local, [
    { id: 1, name: "multica-desktop-0.4.57-mac-arm64.dmg", state: "uploaded", size: 64, digest: null },
  ]);
  assert.equal(truncated.ok, false);
  assert.equal(truncated.failures[0].status, "size-mismatch");

  const rewritten = compareReleaseAssets(local, [
    { id: 1, name: "multica-desktop-0.4.57-mac-arm64.dmg", state: "uploaded", size: 100, digest: `sha256:${"b".repeat(64)}` },
  ]);
  assert.equal(rewritten.ok, false);
  assert.equal(rewritten.failures[0].status, "digest-mismatch");

  const missing = compareReleaseAssets(local, []);
  assert.equal(missing.failures[0].status, "missing");
});

test("passes only when every expected asset is uploaded with a matching size", () => {
  const local = [
    { name: "multica-desktop-0.4.55-mac-arm64.dmg", size: 230712949, sha256: "a".repeat(64) },
    { name: "latest-mac.yml", size: 537, sha256: "b".repeat(64) },
  ];
  const comparison = compareReleaseAssets(local, [
    { id: 1, name: "multica-desktop-0.4.55-mac-arm64.dmg", state: "uploaded", size: 230712949, digest: `sha256:${"a".repeat(64)}` },
    { id: 2, name: "latest-mac.yml", state: "uploaded", size: 537, digest: null },
    { id: 3, name: "multica-desktop-0.4.55-linux-x64.AppImage", state: "uploaded", size: 10, digest: null },
  ]);
  assert.equal(comparison.ok, true);
  // An uploaded asset this build does not produce is reported, not failed.
  assert.equal(
    comparison.rows.find((row) => row.name.endsWith(".AppImage")).status,
    "extra",
  );
});

test("parses the CLI contract and rejects a tag that cannot be a release", () => {
  const options = parseArgs(["check", "--tag", "v0.4.57", "--dist=d", "--attempts", "3"]);
  assert.equal(options.command, "check");
  assert.equal(options.tag, "v0.4.57");
  assert.equal(options.dist, "d");
  assert.equal(options.attempts, 3);

  assert.throws(() => parseArgs(["check"]), /--tag is required/);
  assert.throws(() => parseArgs(["check", "--tag", "0.4.57"]), /must look like vX.Y.Z/);
  assert.throws(() => parseArgs(["nope", "--tag", "v0.4.57"]), /unknown subcommand/);
  assert.throws(() => parseArgs(["check", "--tag", "v0.4.57", "--nope", "x"]), /unknown flag/);
  assert.equal(releaseVersionFromTag("v0.4.57"), "0.4.57");
});

test("readReleaseAssets surfaces zombies hidden by `gh release view` and 404s as null", () => {
  const dir = tempDir();
  const record = join(dir, "record.jsonl");
  process.env.FAKE_GH_RECORD = record;
  try {
    const ghBin = fakeGh(dir, {
      assets: [
        { id: 7, name: "latest-mac.yml", state: "uploaded", size: 537, digest: `sha256:${"c".repeat(64)}` },
        { id: 8, name: "multica-desktop-0.4.57-mac-arm64.dmg", state: "starter", size: 231433659 },
      ],
    });
    const assets = readReleaseAssets({ repo: "o/r", tag: "v0.4.57", ghBin });
    assert.deepEqual(assets, [
      { id: 7, name: "latest-mac.yml", state: "uploaded", size: 537, digest: `sha256:${"c".repeat(64)}` },
      { id: 8, name: "multica-desktop-0.4.57-mac-arm64.dmg", state: "starter", size: 231433659, digest: null },
    ]);

    // The asset list only exists under the numeric release id;
    // `/releases/tags/<tag>/assets` answers 404 and would look like "no release".
    const recorded = readFileSync(record, "utf-8")
      .trim()
      .split("\n")
      .map((line) => JSON.parse(line).join(" "));
    assert.ok(recorded.some((call) => call === "api repos/o/r/releases/tags/v0.4.57 --jq .id"));
    assert.ok(recorded.some((call) => call.includes("repos/o/r/releases/424242/assets")));

    const missing = fakeGh(tempDir(), { apiStatus: 1 });
    assert.equal(readReleaseAssets({ repo: "o/r", tag: "v9.9.9", ghBin: missing }), null);
  } finally {
    delete process.env.FAKE_GH_RECORD;
    rmSync(dir, { recursive: true, force: true });
  }
});

test("check fails on a truncated release and passes on a complete one", () => {
  const dir = tempDir();
  try {
    const dist = join(dir, "dist");
    mkdirSync(dist);
    writeFile(join(dist, "multica-desktop-0.4.57-mac-arm64.dmg"), 128);
    writeFile(join(dist, "latest-mac.yml"), 16);

    const brokenGh = fakeGh(dir, {
      assets: [
        { id: 1, name: "multica-desktop-0.4.57-mac-arm64.dmg", state: "starter", size: 128 },
        { id: 2, name: "latest-mac.yml", state: "uploaded", size: 16 },
      ],
    });
    let output = "";
    let status = 0;
    try {
      output = execFileSync(
        process.execPath,
        [script, "check", "--tag", "v0.4.57", "--repo", "o/r", "--dist", dist],
        { encoding: "utf-8", env: { ...process.env, DESKTOP_RELEASE_GH: brokenGh } },
      );
    } catch (error) {
      status = error.status;
      output = `${error.stdout}${error.stderr}`;
    }
    assert.equal(status, 1);
    assert.match(output, /FAILED: 1 of 2 asset check\(s\) did not pass/);
    assert.match(output, /state=starter/);

    const goodGh = fakeGh(dir, {
      assets: [
        { id: 1, name: "multica-desktop-0.4.57-mac-arm64.dmg", state: "uploaded", size: 128 },
        { id: 2, name: "latest-mac.yml", state: "uploaded", size: 16 },
      ],
    });
    output = execFileSync(
      process.execPath,
      [script, "check", "--tag", "v0.4.57", "--repo", "o/r", "--dist", dist],
      { encoding: "utf-8", env: { ...process.env, DESKTOP_RELEASE_GH: goodGh } },
    );
    assert.match(output, /OK: every one of the 2 asset\(s\)/);

    const garbage = execFileSync(
      process.execPath,
      [script, "check", "--tag", "v0.4.56", "--repo", "o/r", "--dist", dist],
      { encoding: "utf-8", env: { ...process.env, DESKTOP_RELEASE_GH: goodGh } },
    );
    assert.match(garbage, /do not carry the tag version 0.4.56/);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("dry-run prints one upload command per wave, feed metadata last", () => {
  const dir = tempDir();
  try {
    const dist = join(dir, "dist");
    mkdirSync(dist);
    writeFile(join(dist, "multica-desktop-0.4.57-mac-arm64.dmg"), 128);
    writeFile(join(dist, "multica-desktop-0.4.57-mac-arm64.dmg.blockmap"), 32);
    writeFile(join(dist, "latest-mac.yml"), 16);
    const output = execFileSync(
      process.execPath,
      [script, "upload", "--tag", "v0.4.57", "--repo", "o/r", "--dist", dist, "--dry-run"],
      { encoding: "utf-8" },
    );
    const commands = output
      .trim()
      .split("\n")
      .filter((line) => line.includes("release upload"));
    assert.equal(commands.length, 3);
    assert.match(commands[0], /gh release upload v0\.4\.57 --repo o\/r --clobber .*\.dmg$/);
    assert.match(commands[1], /\.dmg\.blockmap$/);
    assert.match(commands[2], /latest-mac\.yml$/);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

// `gh release upload` uploads its file arguments with five concurrent workers,
// so a single invocation would let the 16 KB feed land before the 230 MB DMG.
// This is the regression that would re-publish a manifest pointing at bytes
// that never arrived (DENE-352).
test("upload issues a separate gh call per wave so the feed cannot land first", () => {
  const dir = tempDir();
  try {
    const dist = join(dir, "dist");
    mkdirSync(dist);
    writeFile(join(dist, "multica-desktop-0.4.57-mac-arm64.dmg"), 128);
    writeFile(join(dist, "multica-desktop-0.4.57-mac-arm64.zip"), 96);
    writeFile(join(dist, "multica-desktop-0.4.57-mac-arm64.dmg.blockmap"), 32);
    writeFile(join(dist, "latest-mac.yml"), 16);
    const record = join(dir, "gh-calls.log");
    const gh = fakeGh(dir);
    execFileSync(
      process.execPath,
      [script, "upload", "--tag", "v0.4.57", "--repo", "o/r", "--dist", dist],
      {
        encoding: "utf-8",
        env: { ...process.env, DESKTOP_RELEASE_GH: gh, FAKE_GH_RECORD: record },
      },
    );
    const calls = readFileSync(record, "utf-8")
      .trim()
      .split("\n")
      .map((line) => JSON.parse(line));
    assert.equal(calls.length, 3, "one gh release upload per rank");
    const names = calls.map((call) =>
      call.filter((arg) => arg.includes(dist)).map((arg) => arg.split("/").pop()),
    );
    assert.deepEqual(names[0].sort(), [
      "multica-desktop-0.4.57-mac-arm64.dmg",
      "multica-desktop-0.4.57-mac-arm64.zip",
    ]);
    assert.deepEqual(names[1], ["multica-desktop-0.4.57-mac-arm64.dmg.blockmap"]);
    assert.deepEqual(names[2], ["latest-mac.yml"]);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("uploadWaves groups payloads, blockmaps and feeds in that order", () => {
  const waves = uploadWaves([
    { name: "latest-mac.yml" },
    { name: "a.dmg.blockmap" },
    { name: "a.zip" },
    { name: "a.dmg" },
  ]).map((wave) => wave.map((asset) => asset.name));
  assert.deepEqual(waves, [["a.dmg", "a.zip"], ["a.dmg.blockmap"], ["latest-mac.yml"]]);
});

test("rejects a retry budget that is not a number", () => {
  assert.throws(
    () => parseArgs(["check", "--tag", "v0.4.57", "--attempts", "soon"]),
    /--attempts needs a non-negative number/,
  );
});

test("clean deletes only the assets a failed upload left behind", () => {
  const dir = tempDir();
  try {
    const record = join(dir, "record.jsonl");
    const ghBin = fakeGh(dir, {
      assets: [
        { id: 11, name: "latest-mac.yml", state: "uploaded", size: 537 },
        { id: 12, name: "multica-desktop-0.4.57-mac-arm64.zip", state: "starter", size: 221627956 },
      ],
    });
    const output = execFileSync(
      process.execPath,
      [script, "clean", "--tag", "v0.4.57", "--repo", "o/r"],
      { encoding: "utf-8", env: { ...process.env, DESKTOP_RELEASE_GH: ghBin, FAKE_GH_RECORD: record } },
    );
    assert.match(output, /deleted multica-desktop-0\.4\.57-mac-arm64\.zip \(state=starter\)/);
    const calls = execFileSync("cat", [record], { encoding: "utf-8" })
      .trim()
      .split("\n")
      .map((line) => JSON.parse(line));
    assert.deepEqual(calls.at(-1), [
      "api",
      "-X",
      "DELETE",
      "repos/o/r/releases/assets/12",
    ]);
    assert.equal(calls.some((call) => call.join(" ").includes("assets/11")), false);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("--scope-dist leaves a sibling packaging job's in-flight upload alone", () => {
  // The matrix shape: the Linux job finishes while the macOS job is still
  // streaming its DMG. Unscoped, the Linux job's check fails on a zombie it
  // does not own — and its clean would delete those bytes mid-transfer.
  const local = [
    { name: "multica-desktop-0.4.60-linux-x86_64.AppImage", size: 120, sha256: "a".repeat(64) },
    { name: "latest-linux.yml", size: 40, sha256: "b".repeat(64) },
  ];
  const remote = [
    { id: 1, name: "multica-desktop-0.4.60-linux-x86_64.AppImage", state: "uploaded", size: 120, digest: `sha256:${"a".repeat(64)}` },
    { id: 2, name: "latest-linux.yml", state: "uploaded", size: 40, digest: null },
    { id: 3, name: "multica-desktop-0.4.60-mac-arm64.dmg", state: "starter", size: 0, digest: null },
  ];

  assert.equal(compareReleaseAssets(local, remote).ok, false);

  const scoped = compareReleaseAssets(local, remote, { scopeToLocal: true });
  assert.equal(scoped.ok, true);
  assert.deepEqual(
    scoped.rows.map((row) => row.name),
    local.map((asset) => asset.name),
  );
});

test("--scope-dist still fails on this job's own truncated upload", () => {
  const local = [
    { name: "multica-desktop-0.4.60-windows-x64.exe", size: 100, sha256: "a".repeat(64) },
  ];
  const scoped = compareReleaseAssets(
    local,
    [
      { id: 1, name: "multica-desktop-0.4.60-windows-x64.exe", state: "starter", size: 100, digest: null },
      { id: 2, name: "multica-desktop-0.4.60-mac-arm64.dmg", state: "starter", size: 0, digest: null },
    ],
    { scopeToLocal: true },
  );
  assert.equal(scoped.ok, false);
  assert.deepEqual(
    scoped.failures.map((row) => [row.name, row.status]),
    [["multica-desktop-0.4.60-windows-x64.exe", "not-uploaded"]],
  );
});

test("--scope-dist is off unless asked for", () => {
  assert.equal(parseArgs(["check", "--tag", "v0.4.60"]).scopeDist, false);
  assert.equal(
    parseArgs(["clean", "--tag", "v0.4.60", "--scope-dist"]).scopeDist,
    true,
  );
});

test("skips electron-builder staging trees so a two-arch Windows build is unambiguous", () => {
  const dir = tempDir();
  try {
    // What `package.mjs --win --x64 --arm64` leaves behind: the installers at
    // the root, plus one unpacked app tree per arch, each carrying the same
    // `Multica.exe`.
    writeFile(join(dir, "multica-desktop-0.4.64-win-x64.exe"), 9);
    writeFile(join(dir, "multica-desktop-0.4.64-win-arm64.exe"), 7);
    for (const staging of ["win-unpacked", "win-arm64-unpacked"]) {
      mkdirSync(join(dir, staging, "resources"), { recursive: true });
      writeFile(join(dir, staging, "Multica.exe"), 4);
      writeFile(join(dir, staging, "resources", "app.asar.unpacked.zip"), 4);
    }
    // macOS stages the same way, inside a bundle rather than a `-unpacked` dir.
    mkdirSync(join(dir, "mac-arm64", "Multica.app", "Contents"), {
      recursive: true,
    });
    writeFile(
      join(dir, "mac-arm64", "Multica.app", "Contents", "helper.zip"),
      4,
    );

    assert.deepEqual(
      collectLocalAssets(dir).map((asset) => asset.name),
      [
        "multica-desktop-0.4.64-win-arm64.exe",
        "multica-desktop-0.4.64-win-x64.exe",
      ],
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
