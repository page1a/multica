// @vitest-environment node
import { describe, expect, it, vi } from "vitest";

vi.mock("electron", () => ({ ipcMain: { handle: vi.fn() }, shell: {} }));
vi.mock("./daemon-manager", () => ({ activeDaemonHealthPort: vi.fn() }));

import { createLocalAttachments, isAttachmentId } from "./local-attachments";

const ID = "01a11579-ea96-76ba-8a38-13dba24d52ce";
const COPY = `/Users/kun/.multica/outputs/${ID}/report.html`;

function setup(answer: { status: number; body?: unknown } = { status: 200, body: { path: COPY } }) {
  const fetch = vi.fn(async () =>
    new Response(JSON.stringify(answer.body ?? {}), { status: answer.status }),
  );
  const openPath = vi.fn(async () => "");
  const showItemInFolder = vi.fn();
  const local = createLocalAttachments({
    daemonPort: async () => 19514,
    fetch: fetch as unknown as typeof globalThis.fetch,
    openPath,
    showItemInFolder,
  });
  return { local, fetch, openPath, showItemInFolder };
}

describe("isAttachmentId", () => {
  it("accepts attachment ids only", () => {
    expect(isAttachmentId(ID)).toBe(true);
    expect(isAttachmentId(ID.toUpperCase())).toBe(true);
    for (const bad of ["", "abc", COPY, "../etc/passwd", `${ID}/../x`, `${ID} `, null, 42, { id: ID }]) {
      expect(isAttachmentId(bad)).toBe(false);
    }
  });
});

describe("createLocalAttachments", () => {
  it("opens and reveals the copy the daemon reports, by id", async () => {
    const { local, fetch, openPath, showItemInFolder } = setup();
    expect(await local.status(ID)).toEqual({ available: true });
    expect(await local.open(ID)).toEqual({ ok: true });
    expect(openPath).toHaveBeenCalledWith(COPY);
    expect(await local.reveal(ID)).toEqual({ ok: true });
    expect(showItemInFolder).toHaveBeenCalledWith(COPY);
    expect(fetch).toHaveBeenCalledWith(
      `http://127.0.0.1:19514/outputs?attachment_id=${ID}`,
      expect.objectContaining({ method: "GET" }),
    );
  });

  it("never reports the path to the caller", async () => {
    const { local } = setup();
    expect(JSON.stringify(await local.status(ID))).not.toContain("/Users");
  });

  it("refuses paths and anything that is not an attachment id without asking the daemon", async () => {
    const { local, fetch, openPath, showItemInFolder } = setup();
    for (const bad of [COPY, "/etc/passwd", "../outputs", "", undefined, ["x"]]) {
      expect(await local.open(bad)).toEqual({ ok: false, reason: "invalid_id" });
      expect(await local.reveal(bad)).toEqual({ ok: false, reason: "invalid_id" });
      expect(await local.status(bad)).toEqual({ available: false });
    }
    expect(fetch).not.toHaveBeenCalled();
    expect(openPath).not.toHaveBeenCalled();
    expect(showItemInFolder).not.toHaveBeenCalled();
  });

  it("does not open a path outside the id's own entry", async () => {
    for (const path of ["/etc/passwd", `/tmp/other/${ID}x/a`, "relative/a.txt"]) {
      const { local, openPath } = setup({ status: 200, body: { path } });
      expect(await local.open(ID)).toEqual({ ok: false, reason: "not_found" });
      expect(openPath).not.toHaveBeenCalled();
    }
  });

  it("reports no copy when the daemon has none (deleted or changed)", async () => {
    const { local, openPath } = setup({ status: 404 });
    expect(await local.status(ID)).toEqual({ available: false });
    expect(await local.open(ID)).toEqual({ ok: false, reason: "not_found" });
    expect(openPath).not.toHaveBeenCalled();
  });

  it("reports no copy when the daemon is not running", async () => {
    const local = createLocalAttachments({
      daemonPort: async () => null,
      fetch: vi.fn() as unknown as typeof fetch,
      openPath: vi.fn(),
      showItemInFolder: vi.fn(),
    });
    expect(await local.status(ID)).toEqual({ available: false });
  });

  it("surfaces an OS open failure", async () => {
    const { local, openPath } = setup();
    openPath.mockResolvedValueOnce("No application knows how to open this");
    expect(await local.open(ID)).toEqual({ ok: false, reason: "open_failed" });
  });
});
