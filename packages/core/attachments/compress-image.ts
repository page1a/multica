// Client-side photo downscaling before upload.
//
// A phone photo is 3–8 MB straight off the camera. On the self-hosted
// instance the uplink is slow enough that anything past a few MB either hits
// Cloudflare's 100 s edge timeout or gets cut off when Safari suspends the
// tab, so every upload of an original photo is a coin toss. Re-encoding to a
// 2560 px JPEG keeps it readable (and zoomable) at a fraction of the bytes.
//
// Scope is deliberately narrow: only lossy photo formats are touched. PNG
// (screenshots, transparency), GIF (animation), SVG and every non-image file
// go up byte-for-byte. The re-encoded file is used only when it is actually
// smaller; any decode/encode failure falls back to the original file, so this
// can never make an upload fail that would otherwise have succeeded.

export const COMPRESS_MIN_BYTES = 1.5 * 1024 * 1024;
export const COMPRESS_MAX_EDGE = 2560;
export const COMPRESS_QUALITY = 0.85;

const COMPRESSIBLE_TYPES = new Set([
  "image/jpeg",
  "image/jpg",
  "image/webp",
  "image/heic",
  "image/heif",
]);

export function shouldCompressImage(file: Pick<File, "type" | "size">): boolean {
  return COMPRESSIBLE_TYPES.has(file.type.toLowerCase()) && file.size >= COMPRESS_MIN_BYTES;
}

// Fit (width, height) inside a COMPRESS_MAX_EDGE box, preserving aspect ratio
// and never upscaling.
export function fitWithin(
  width: number,
  height: number,
  maxEdge: number = COMPRESS_MAX_EDGE,
): { width: number; height: number } {
  const longest = Math.max(width, height);
  if (longest <= maxEdge) return { width, height };
  const scale = maxEdge / longest;
  return {
    width: Math.max(1, Math.round(width * scale)),
    height: Math.max(1, Math.round(height * scale)),
  };
}

export function jpegFileName(name: string): string {
  const base = name.replace(/\.[^./\\]+$/, "");
  return `${base || "image"}.jpg`;
}

async function encodeJpeg(bitmap: ImageBitmap, width: number, height: number): Promise<Blob | null> {
  if (typeof OffscreenCanvas !== "undefined") {
    const canvas = new OffscreenCanvas(width, height);
    const ctx = canvas.getContext("2d");
    if (!ctx) return null;
    ctx.drawImage(bitmap, 0, 0, width, height);
    return canvas.convertToBlob({ type: "image/jpeg", quality: COMPRESS_QUALITY });
  }
  if (typeof document === "undefined") return null;
  const canvas = document.createElement("canvas");
  canvas.width = width;
  canvas.height = height;
  const ctx = canvas.getContext("2d");
  if (!ctx) return null;
  ctx.drawImage(bitmap, 0, 0, width, height);
  return new Promise((resolve) => canvas.toBlob(resolve, "image/jpeg", COMPRESS_QUALITY));
}

// Returns a smaller JPEG rendition of `file` when it is a large photo, or
// `file` itself otherwise. Never throws.
export async function compressImageForUpload(file: File): Promise<File> {
  if (!shouldCompressImage(file) || typeof createImageBitmap === "undefined") return file;
  let bitmap: ImageBitmap | undefined;
  try {
    // "from-image" applies the EXIF rotation so portrait photos stay upright
    // once the metadata is dropped by the re-encode.
    bitmap = await createImageBitmap(file, { imageOrientation: "from-image" });
    const { width, height } = fitWithin(bitmap.width, bitmap.height);
    const blob = await encodeJpeg(bitmap, width, height);
    if (!blob || blob.size >= file.size) return file;
    return new File([blob], jpegFileName(file.name), {
      type: "image/jpeg",
      lastModified: file.lastModified,
    });
  } catch {
    return file;
  } finally {
    bitmap?.close();
  }
}
