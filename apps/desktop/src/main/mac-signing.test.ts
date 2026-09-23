// @vitest-environment node
import { describe, expect, it } from "vitest";
import { detectMacSigning, parseCodesignOutput } from "./mac-signing";

const DEVELOPER_ID = `Executable=/Applications/Multica.app/Contents/MacOS/Multica
Identifier=ai.multica.desktop
Format=app bundle with Mach-O thin (arm64)
CodeDirectory v=20500 size=1234 flags=0x10000(runtime) hashes=30+7 location=embedded
Signature size=9000
Authority=Developer ID Application: Example Corp (ABCDE12345)
Authority=Developer ID Certification Authority
Authority=Apple Root CA
TeamIdentifier=ABCDE12345
Runtime Version=14.0.0
`;

const ADHOC = `Executable=/Applications/Multica.app/Contents/MacOS/Multica
Identifier=ai.multica.desktop
Format=app bundle with Mach-O thin (arm64)
CodeDirectory v=20400 size=1234 flags=0x2(adhoc) hashes=30+7 location=embedded
Signature=adhoc
Info.plist entries=30
TeamIdentifier=not set
`;

// Self-signed certificates also report TeamIdentifier=not set. The Authority
// line is the stable identity; classifying this as ad-hoc would turn off
// auto-update for every kun release signed with CSC_LINK.
const SELF_SIGNED = `Executable=/Applications/Multica.app/Contents/MacOS/Multica
Identifier=ai.multica.desktop
Format=app bundle with Mach-O thin (arm64)
CodeDirectory v=20400 size=1234 flags=0x0(none) hashes=30+7 location=embedded
Signature size=4800
Authority=Multica Kun Self-Signed
Signed Time=Sep 23, 2026 at 16:00:00
Info.plist entries=30
TeamIdentifier=not set
`;

const ADHOC_LEGACY = `Executable=/Applications/Multica.app/Contents/MacOS/Multica
Identifier=ai.multica.desktop
Format=app bundle with Mach-O thin (arm64)
CodeDirectory v=20400 size=1234 flags=0x2(adhoc) hashes=30+7 location=embedded
Info.plist entries=30
TeamIdentifier=not set
`;

describe("parseCodesignOutput", () => {
  it("recognises a Developer ID signature as a stable identity", () => {
    expect(parseCodesignOutput(DEVELOPER_ID)).toBe("identity");
  });

  it("recognises a self-signed certificate as a stable identity", () => {
    expect(parseCodesignOutput(SELF_SIGNED)).toBe("identity");
  });

  it("recognises an ad-hoc signature (what CI ships without a certificate)", () => {
    expect(parseCodesignOutput(ADHOC)).toBe("adhoc");
  });

  it("treats an old ad-hoc report without Signature=adhoc as ad-hoc", () => {
    expect(parseCodesignOutput(ADHOC_LEGACY)).toBe("adhoc");
  });

  it("recognises an unsigned binary from codesign's failure text", () => {
    expect(
      parseCodesignOutput(
        "/tmp/Multica.app: code object is not signed at all\nIn architecture: arm64\n",
      ),
    ).toBe("unsigned");
  });

  it("falls back to unknown on unexpected output", () => {
    expect(parseCodesignOutput("")).toBe("unknown");
    expect(parseCodesignOutput("codesign: command not found")).toBe("unknown");
  });
});

describe("detectMacSigning", () => {
  it("classifies whatever the runner returns", async () => {
    await expect(detectMacSigning("/x", async () => ADHOC)).resolves.toBe("adhoc");
  });

  it("never throws when the runner fails", async () => {
    await expect(
      detectMacSigning("/x", async () => {
        throw new Error("spawn codesign ENOENT");
      }),
    ).resolves.toBe("unknown");
  });
});
