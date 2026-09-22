// The seat model string — how a model the user picked is stored on an agent,
// and how that same choice is named on screen.
//
// A seat's model is `providerId/modelId`, and Multica splits it at the FIRST
// slash. A gateway's own model id may contain slashes, so each part is
// percent-encoded and the pair is joined by a literal slash. That makes
// `command-code2/deepseek%2Fdeepseek-v4.1-flash` mean provider `command-code2`
// and model `deepseek/deepseek-v4.1-flash`, where the unescaped
// `command-code2/deepseek/deepseek-v4.1-flash` would be read as provider
// `command-code2` and model `deepseek` — the exact 400 that cost five seats
// two days (DENE-680).
//
// The escaping is a WRITE-time transform this module owns. Nothing rendered on
// screen may show the encoded form: a user who sees `%2F` is invited to type it
// themselves, and typing it by hand is how the prefix went missing. Display
// always goes through `providerSeatModelDisplay`, which resolves the pair back
// to `provider · modelName`.
//
// This module sits next to the components, not inside the provider-presets form
// that first needed it, because both seat-side model pickers render the same
// strings: it is the shared vocabulary for a seat's model, not a detail of the
// presets panel.
//
// Canonical tests: `provider-seat-model.test.ts`.

import type {
  RuntimeProviderPreset,
  RuntimeProviderPresetModel,
} from "@multica/core/types";

/** The display name for a preset model row: its name when it has one, else its id. */
export function providerPresetModelLabel(
  model: RuntimeProviderPresetModel,
): string {
  return model.name?.trim() || model.id;
}

export interface ParsedSeatModelString {
  providerId: string;
  modelId: string;
}

function decodeSeatModelSegment(segment: string): string {
  try {
    return decodeURIComponent(segment);
  } catch {
    // A literal `%` that is not an escape sequence. Returning it unchanged
    // keeps an unencodable id round-tripping instead of throwing on display.
    return segment;
  }
}

/** The encoded seat model string for one preset + the gateway's own model id. */
export function providerSeatModelString(
  providerId: string,
  modelId: string,
): string {
  const provider = providerId.trim();
  const model = modelId.trim();
  if (!provider || !model) return "";
  return `${provider}/${encodeURIComponent(model)}`;
}

/**
 * Read a seat model string back into its provider and the gateway's raw model
 * id. Null when the value cannot be one — no slash, or an empty side — so a
 * caller renders it verbatim rather than inventing a pair.
 */
export function parseProviderSeatModelString(
  seatModel: string,
): ParsedSeatModelString | null {
  const raw = seatModel.trim();
  const cut = raw.indexOf("/");
  if (cut <= 0 || cut === raw.length - 1) return null;
  const providerId = decodeSeatModelSegment(raw.slice(0, cut));
  const modelId = decodeSeatModelSegment(raw.slice(cut + 1));
  if (!providerId || !modelId) return null;
  return { providerId, modelId };
}

/**
 * Render a seat model string for the screen as `provider · model name`.
 *
 * `presets` is what turns the encoded model id back into the name the user
 * picked; without the preset in hand the raw id is the honest fallback. It is
 * optional because a model picker renders rows straight from the runtime's
 * model catalog and fetching the preset list there would put a daemon round
 * trip in front of a dropdown just to name a row — the decoded id is already
 * the pair the user picked, and it is what a search is typed against. The
 * returned string never contains the escape — that is the whole point.
 */
export function providerSeatModelDisplay(
  seatModel: string,
  presets: readonly RuntimeProviderPreset[] = [],
): string {
  const parsed = parseProviderSeatModelString(seatModel);
  if (!parsed) return seatModel;
  const preset = presets.find((candidate) => candidate.id === parsed.providerId);
  const model = preset?.models.find((entry) => entry.id === parsed.modelId);
  const name = model ? providerPresetModelLabel(model) : parsed.modelId;
  return `${parsed.providerId} · ${name}`;
}

/**
 * Whether a catalog row is the one a user means when they type `text` exactly.
 *
 * A picker's search matches the decoded spelling, so `deepseek/deepseek-v4.1-flash`
 * now finds the row stored as `command-code2/deepseek%2Fdeepseek-v4.1-flash`.
 * The "use custom model" affordance must recognise that same spelling as an
 * existing row: offering to save what the user typed would store the
 * unescaped pair, which the seat splits at the first slash into provider
 * `deepseek` — the DENE-680 400, re-invited by the very spelling DENE-684
 * taught the picker to display.
 */
export function seatModelIsExactly(seatModel: string, text: string): boolean {
  const needle = text.trim();
  if (!needle) return false;
  if (seatModel === needle) return true;
  if (providerSeatModelDisplay(seatModel) === needle) return true;
  return parseProviderSeatModelString(seatModel)?.modelId === needle;
}
