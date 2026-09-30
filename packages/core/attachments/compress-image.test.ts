import { afterEach, describe, expect, it, vi } from "vitest";
import {
  COMPRESS_MIN_BYTES,
  compressImageForUpload,
  fitWithin,
  jpegFileName,
  shouldCompressImage,
} from "./compress-image";

const MB = 1024 * 1024;

describe("shouldCompressImage", () => {
  it("targets large lossy photos only", () => {
    expect(shouldCompressImage({ type: "image/jpeg", size: 4 * MB })).toBe(true);
    expect(shouldCompressImage({ type: "image/HEIC", size: 4 * MB })).toBe(true);
    expect(shouldCompressImage({ type: "image/jpeg", size: COMPRESS_MIN_BYTES - 1 })).toBe(false);
    expect(shouldCompressImage({ type: "image/png", size: 8 * MB })).toBe(false);
    expect(shouldCompressImage({ type: "image/gif", size: 8 * MB })).toBe(false);
    expect(shouldCompressImage({ type: "video/mp4", size: 8 * MB })).toBe(false);
  });
});

describe("fitWithin", () => {
  it("scales the long edge down and keeps the aspect ratio", () => {
    expect(fitWithin(4032, 3024)).toEqual({ width: 2560, height: 1920 });
    expect(fitWithin(3024, 4032)).toEqual({ width: 1920, height: 2560 });
  });
  it("never upscales", () => {
    expect(fitWithin(1200, 800)).toEqual({ width: 1200, height: 800 });
  });
});

describe("jpegFileName", () => {
  it("swaps the extension", () => {
    expect(jpegFileName("IMG_0001.HEIC")).toBe("IMG_0001.jpg");
    expect(jpegFileName("photo")).toBe("photo.jpg");
    expect(jpegFileName(".heic")).toBe("image.jpg");
  });
});

describe("compressImageForUpload", () => {
  afterEach(() => vi.unstubAllGlobals());

  const bigJpeg = () => new File([new Uint8Array(2 * MB)], "a.jpeg", { type: "image/jpeg" });

  it("passes non-photos through untouched", async () => {
    const file = new File([new Uint8Array(8 * MB)], "shot.png", { type: "image/png" });
    expect(await compressImageForUpload(file)).toBe(file);
  });

  it("falls back to the original when the browser cannot decode", async () => {
    vi.stubGlobal("createImageBitmap", vi.fn().mockRejectedValue(new Error("decode")));
    const file = bigJpeg();
    expect(await compressImageForUpload(file)).toBe(file);
  });

  it("uses the re-encoded JPEG when it is smaller", async () => {
    const close = vi.fn();
    vi.stubGlobal("createImageBitmap", vi.fn().mockResolvedValue({ width: 4032, height: 3024, close }));
    const drawImage = vi.fn();
    class FakeCanvas {
      constructor(public width: number, public height: number) {}
      getContext() {
        return { drawImage };
      }
      convertToBlob() {
        return Promise.resolve(new Blob([new Uint8Array(300 * 1024)], { type: "image/jpeg" }));
      }
    }
    vi.stubGlobal("OffscreenCanvas", FakeCanvas);

    const out = await compressImageForUpload(bigJpeg());
    expect(out.name).toBe("a.jpg");
    expect(out.type).toBe("image/jpeg");
    expect(out.size).toBe(300 * 1024);
    expect(drawImage).toHaveBeenCalledWith(expect.anything(), 0, 0, 2560, 1920);
    expect(close).toHaveBeenCalled();
  });

  it("keeps the original when re-encoding does not shrink it", async () => {
    vi.stubGlobal("createImageBitmap", vi.fn().mockResolvedValue({ width: 100, height: 100, close: vi.fn() }));
    class FakeCanvas {
      getContext() {
        return { drawImage: vi.fn() };
      }
      convertToBlob() {
        return Promise.resolve(new Blob([new Uint8Array(3 * MB)]));
      }
    }
    vi.stubGlobal("OffscreenCanvas", FakeCanvas);
    const file = bigJpeg();
    expect(await compressImageForUpload(file)).toBe(file);
  });
});
