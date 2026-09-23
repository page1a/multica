import { execFile } from "node:child_process";

/**
 * How the running macOS bundle is signed, as reported by `codesign -dv`.
 *
 * - `identity`: a stable signing identity. Developer ID and the fork's
 *   self-signed certificate both qualify. Squirrel.Mac pins the designated
 *   requirement to that certificate, so a later build signed with the same
 *   cert can install.
 * - `adhoc`: `codesign -s -`. The requirement is the binary's cdhash, which
 *   changes every build, so an update can never match.
 * - `unsigned`: no signature at all.
 * - `unknown`: codesign was unavailable or printed something unexpected.
 *   Not a stable identity, so the updater stays on the manual path.
 */
export type MacSigningStatus = "identity" | "adhoc" | "unsigned" | "unknown";

/**
 * Parse `codesign -dv --verbose=4` output. codesign writes its report to
 * stderr and exits non-zero for unsigned binaries, so callers pass whatever
 * text they got regardless of exit status.
 *
 * A self-signed certificate reports `TeamIdentifier=not set` just like an
 * ad-hoc signature. The Authority line is what distinguishes them, so it is
 * checked before that fallback.
 */
export function parseCodesignOutput(output: string): MacSigningStatus {
  if (/code object is not signed at all/.test(output)) return "unsigned";
  if (/^Signature=adhoc$/m.test(output)) return "adhoc";
  if (/^Authority=.+/m.test(output)) return "identity";
  // A signed bundle without an Authority line only happens for ad-hoc
  // signatures on older codesign builds that omit the Signature= line.
  if (/^TeamIdentifier=not set$/m.test(output)) return "adhoc";
  return "unknown";
}

type CodesignRunner = (path: string) => Promise<string>;

const runCodesign: CodesignRunner = (path) =>
  new Promise((resolve) => {
    execFile(
      "codesign",
      ["-dv", "--verbose=4", path],
      { encoding: "utf-8", timeout: 10_000 },
      (_err, stdout, stderr) => {
        // codesign reports on stderr; a non-zero exit still carries the
        // "not signed at all" diagnostic we want to classify.
        resolve(`${stdout ?? ""}\n${stderr ?? ""}`);
      },
    );
  });

/**
 * Probe the signing status of the bundle that contains `executablePath`.
 * Never throws: an exec failure (no codesign on PATH, timeout) resolves to
 * `unknown` so the updater degrades to the manual path instead of crashing
 * the startup check.
 */
export async function detectMacSigning(
  executablePath: string,
  run: CodesignRunner = runCodesign,
): Promise<MacSigningStatus> {
  try {
    return parseCodesignOutput(await run(executablePath));
  } catch {
    return "unknown";
  }
}
