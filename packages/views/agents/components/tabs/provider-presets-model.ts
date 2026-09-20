// Pure logic behind the agent accounts tab's "providers" section (DENE-348).
//
// A provider preset is one route the agent CLI can send a request down: an
// endpoint, the protocol it speaks, the credential that authenticates it and
// the models it offers. The daemon owns the files; this module owns the form
// rules, the view states and the shape of the body the daemon is handed.
//
// The one invariant that shapes everything here: THE KEY IS WRITE-ONLY. No
// read type in this file has a field for a key value, the form always starts
// with an empty key box (never the mask), and a blank box means "leave the
// stored credential alone" — never "clear it". Pre-filling the mask would make
// the next submit write `sk-…0000` itself as the new credential, which is the
// one mistake this surface cannot recover from: the real key is gone and
// nothing on screen would say so.
//
// Canonical tests: `provider-presets-model.test.ts`. The component suite keeps
// the happy path and the wiring only.

import {
  PROVIDER_PRESET_APIS,
  PROVIDER_PRESET_DEFAULT_API,
} from "@multica/core/runtimes";
import type {
  RuntimeProviderPreset,
  RuntimeProviderPresetModel,
  RuntimeProviderPresetUpsertInput,
} from "@multica/core/types";

/**
 * Runtime providers whose CLI this section can configure.
 *
 * One entry today. The daemon rejects an id it has no driver for rather than
 * guessing (`server/internal/daemon/dsh_providers.go`), so a second CLI is a
 * backend change that arrives with its own UI — this set follows it, it does
 * not predict it.
 */
const PRESET_CAPABLE_PROVIDERS = new Set(["dsh"]);

/**
 * Whether the runtime behind this agent has a provider-preset driver.
 *
 * The section is not rendered at all for anything else, rather than rendered
 * disabled: a machine with no driver has nothing to show and no action to
 * offer, and a greyed-out block would read as "not set up yet".
 */
export function supportsProviderPresets(provider: string | undefined): boolean {
  return PRESET_CAPABLE_PROVIDERS.has((provider ?? "").trim().toLowerCase());
}

export type ProviderPresetsViewState =
  | { kind: "loading" }
  | { kind: "error"; message: string }
  | { kind: "empty" }
  | { kind: "ready"; presets: RuntimeProviderPreset[] };

export interface ProviderPresetsViewInput {
  presets: RuntimeProviderPreset[] | undefined;
  loading: boolean;
  error: string;
}

/**
 * The four states the section can be in, in the order they preempt each other.
 *
 * `error` outranks having stale rows on purpose: the list drives an activate
 * and a delete, and acting on a list we could not refresh would target a
 * preset the machine may no longer have. An untrustworthy list therefore
 * offers no editing affordance at all — the same rule the account half of this
 * tab follows (DENE-305).
 */
export function providerPresetsViewState(
  input: ProviderPresetsViewInput,
): ProviderPresetsViewState {
  if (input.error) return { kind: "error", message: input.error };
  if (input.loading) return { kind: "loading" };
  const presets = input.presets ?? [];
  if (presets.length === 0) return { kind: "empty" };
  return { kind: "ready", presets };
}

/** Whether the section may offer add / edit / activate / delete. */
export function canManageProviderPresets(
  state: ProviderPresetsViewState,
): boolean {
  return state.kind === "ready" || state.kind === "empty";
}

/** One model row being edited. Kept as strings — it is form state. */
export interface ProviderPresetModelDraft {
  id: string;
  name: string;
}

/**
 * The form's state.
 *
 * `apiKey` is write-only and therefore always starts empty; `hasKey` and
 * `keyMask` are the read-side facts about the STORED credential and are
 * display-only. Keeping them in three separate fields is what makes it
 * impossible to accidentally submit the mask: nothing ever assigns `keyMask`
 * into `apiKey`.
 */
export interface ProviderPresetForm {
  /** Empty when adding; the preset's id when editing (the id is its identity). */
  editingId: string;
  id: string;
  baseUrl: string;
  api: string;
  apiKeyEnv: string;
  apiKey: string;
  hasKey: boolean;
  keyMask: string;
  models: ProviderPresetModelDraft[];
}

/** A blank form for "add a provider". */
export function emptyProviderPresetForm(): ProviderPresetForm {
  return {
    editingId: "",
    id: "",
    baseUrl: "",
    api: PROVIDER_PRESET_DEFAULT_API,
    apiKeyEnv: "",
    apiKey: "",
    hasKey: false,
    keyMask: "",
    models: [{ id: "", name: "" }],
  };
}

/**
 * Fill the form from an existing preset.
 *
 * Every field is carried over except the credential: `apiKey` is hardcoded to
 * `""` here and there is no branch that can change that. See the file header
 * for why.
 */
export function providerPresetFormFrom(
  preset: RuntimeProviderPreset,
): ProviderPresetForm {
  return {
    editingId: preset.id,
    id: preset.id,
    baseUrl: preset.base_url ?? "",
    api: knownPresetApi(preset.api),
    apiKeyEnv: preset.api_key_env ?? "",
    apiKey: "",
    hasKey: preset.has_key === true,
    keyMask: preset.key_mask ?? "",
    models:
      preset.models.length > 0
        ? preset.models.map((model) => ({ id: model.id, name: model.name ?? "" }))
        : [{ id: "", name: "" }],
  };
}

/**
 * Narrow a reported protocol to one the select can show.
 *
 * A daemon newer than this build may report a protocol this list does not
 * have. Falling back to the default keeps the select controlled, and the
 * validator below refuses a protocol it does not know — so an unknown value
 * cannot be silently written back over the real one.
 */
function knownPresetApi(api: string | undefined): string {
  const value = (api ?? "").trim();
  return (PROVIDER_PRESET_APIS as readonly string[]).includes(value)
    ? value
    : PROVIDER_PRESET_DEFAULT_API;
}

/** Which fields a submit would reject, as translation-key suffixes. */
export type ProviderPresetFieldError =
  | "id_required"
  | "id_invalid"
  | "base_url_required"
  | "base_url_invalid"
  | "api_invalid"
  | "api_key_env_invalid"
  | "models_required";

/**
 * A preset id becomes a YAML mapping key under `llm-pi-ai.providers` and is
 * written verbatim into `agent-default-model.provider`, so it is restricted to
 * characters that need no quoting and cannot traverse into another node.
 */
const PRESET_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;

/** Env var names are `[A-Z_][A-Z0-9_]*` — the shell's own rule, uppercased. */
const ENV_NAME_PATTERN = /^[A-Za-z_][A-Za-z0-9_]*$/;

export function validateProviderPresetForm(
  form: ProviderPresetForm,
): ProviderPresetFieldError[] {
  const errors: ProviderPresetFieldError[] = [];

  const id = form.id.trim();
  if (!id) errors.push("id_required");
  else if (!PRESET_ID_PATTERN.test(id)) errors.push("id_invalid");

  const baseUrl = form.baseUrl.trim();
  if (!baseUrl) errors.push("base_url_required");
  else if (!isHttpUrl(baseUrl)) errors.push("base_url_invalid");

  if (!(PROVIDER_PRESET_APIS as readonly string[]).includes(form.api.trim())) {
    errors.push("api_invalid");
  }

  const env = form.apiKeyEnv.trim();
  // Empty is valid and means "let the daemon derive one from the id".
  if (env && !ENV_NAME_PATTERN.test(env)) errors.push("api_key_env_invalid");

  // A save is a health check, and the check runs one completion against a
  // model — so a preset with no model has nothing to verify and the daemon
  // refuses to write it. Catch that here, where it is a field error, instead
  // of turning it into a toast after a round trip through the user's machine.
  if (providerPresetModels(form).length === 0) errors.push("models_required");

  return errors;
}

/**
 * `http`/`https` only.
 *
 * Not a cosmetic check: this value decides where the stored key is sent. A
 * `file:` or `data:` endpoint is not something the CLI can call, and letting
 * one through would write a route that fails at request time instead of at the
 * form.
 */
function isHttpUrl(value: string): boolean {
  try {
    const url = new URL(value);
    return url.protocol === "http:" || url.protocol === "https:";
  } catch {
    return false;
  }
}

/** The model rows that survive submission — blank rows are scaffolding. */
export function providerPresetModels(
  form: ProviderPresetForm,
): RuntimeProviderPresetModel[] {
  return form.models
    .map((model) => ({ id: model.id.trim(), name: model.name.trim() }))
    .filter((model) => model.id !== "")
    .map((model) => (model.name ? model : { id: model.id }));
}

/**
 * The upsert body.
 *
 * `api_key` is present ONLY when the user typed one. A blank box produces no
 * field at all, which the daemon reads as "keep the stored credential" — the
 * behaviour a user editing an endpoint without retyping their key expects.
 */
export function providerPresetUpsertInput(
  form: ProviderPresetForm,
): RuntimeProviderPresetUpsertInput {
  const input: RuntimeProviderPresetUpsertInput = {
    id: form.id.trim(),
    api: form.api.trim(),
    base_url: form.baseUrl.trim(),
    api_key_env: form.apiKeyEnv.trim(),
    models: providerPresetModels(form),
  };
  const typed = form.apiKey.trim();
  if (typed) input.api_key = typed;
  return input;
}

/**
 * What the credential line says about a preset. Three answers, because
 * "stored, and here is its mask" and "stored, but this daemon reported no
 * mask" are the same fact and must not read as "nothing stored".
 */
export type ProviderPresetKeyState = "masked" | "stored" | "absent";

export function providerPresetKeyState(
  preset: RuntimeProviderPreset,
): ProviderPresetKeyState {
  if (preset.has_key !== true) return "absent";
  return preset.key_mask ? "masked" : "stored";
}

/** `POST https://host/v1 · 2 models`-style secondary line, protocol first. */
export function providerPresetSummaryLine(preset: RuntimeProviderPreset): string {
  const parts = [preset.base_url ?? "", preset.api ?? ""].filter(Boolean);
  return parts.join(" · ");
}

/**
 * Whether deleting this preset needs the "you will have no default model"
 * confirmation rather than the ordinary one.
 */
export function deletingActivePreset(preset: RuntimeProviderPreset): boolean {
  return preset.active === true;
}

// ---------------------------------------------------------------------------
// The fetched catalog
// ---------------------------------------------------------------------------

/** Narrow a fetched catalog by a free-text query over its id and display name. */
export function filterProviderPresetModels(
  models: readonly RuntimeProviderPresetModel[],
  query: string,
): RuntimeProviderPresetModel[] {
  const needle = query.trim().toLowerCase();
  if (!needle) return [...models];
  return models.filter(
    (model) =>
      model.id.toLowerCase().includes(needle) ||
      (model.name ?? "").toLowerCase().includes(needle),
  );
}

/**
 * The context window a catalog row reports, or null when it named none.
 *
 * Both spellings are read because a gateway may answer either and only the
 * daemon's normalisation is guaranteed to pick one; a size is a display fact,
 * so a row that cannot produce one simply shows no size.
 */
export function providerPresetContextWindow(
  model: RuntimeProviderPresetModel,
): number | null {
  const value = model.context_window ?? model.context_length;
  return typeof value === "number" && Number.isFinite(value) && value > 0
    ? value
    : null;
}

/** The display name for a model row: its name when it has one, else its id. */
export function providerPresetModelLabel(
  model: RuntimeProviderPresetModel,
): string {
  return model.name?.trim() || model.id;
}

/**
 * Whether the form can ask the endpoint for its catalog.
 *
 * Needs an endpoint and a credential: either one just typed, or — when editing
 * — the one the preset already stores. Requiring the key to be retyped would
 * be a second dead end on top of the one this ticket removes.
 */
export function canFetchProviderPresetModels(form: ProviderPresetForm): boolean {
  if (!form.baseUrl.trim()) return false;
  if (form.apiKey.trim() !== "") return true;
  return form.editingId !== "" && form.hasKey;
}

// ---------------------------------------------------------------------------
// Seat model strings
// ---------------------------------------------------------------------------

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
// screen may show the encoded form: a user who sees `%2F` is invited to type
// it themselves, and typing it by hand is how the prefix went missing. Display
// always goes through `providerSeatModelDisplay`, which resolves the pair back
// to `provider · modelName`.

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
 * picked; without the preset in hand the raw id is the honest fallback. The
 * returned string never contains the escape — that is the whole point.
 */
export function providerSeatModelDisplay(
  seatModel: string,
  presets: readonly RuntimeProviderPreset[],
): string {
  const parsed = parseProviderSeatModelString(seatModel);
  if (!parsed) return seatModel;
  const preset = presets.find((candidate) => candidate.id === parsed.providerId);
  const model = preset?.models.find((entry) => entry.id === parsed.modelId);
  const name = model ? providerPresetModelLabel(model) : parsed.modelId;
  return `${parsed.providerId} · ${name}`;
}

// ---------------------------------------------------------------------------
// Failure translation and the save state machine
// ---------------------------------------------------------------------------

/**
 * A failed preset action, split into what a localized surface needs: the kind
 * and its parameters, plus the daemon's own sentence as the fallback for a kind
 * this build does not know.
 */
export interface ProviderPresetFailure {
  kind: string;
  params: Record<string, string>;
  message: string;
}

/**
 * Stable locale-key suffixes for the daemon's failure kinds
 * (`server/internal/daemon/dsh_provider_probe.go`). The wire value is open
 * string: a newer daemon may add a kind, and `providerPresetFailureFrom` plus
 * the caller's default branch render its English sentence instead of an empty
 * box.
 */
export const PROVIDER_PRESET_FAILURE_I18N_KEYS = {
  invalid_credential: "error_kind_invalid_credential",
  missing_credential: "error_kind_missing_credential",
  model_not_in_plan: "error_kind_model_not_in_plan",
  rate_limited: "error_kind_rate_limited",
  unknown_model: "error_kind_unknown_model",
  endpoint_mismatch: "error_kind_endpoint_mismatch",
  mixed_protocols: "error_kind_mixed_protocols",
  unreachable: "error_kind_unreachable",
  models_unavailable: "error_kind_models_unavailable",
  empty_model_list: "error_kind_empty_model_list",
  unsupported_protocol: "error_kind_unsupported_protocol",
  provider_error: "error_kind_provider_error",
} as const;

export type KnownProviderPresetFailure =
  keyof typeof PROVIDER_PRESET_FAILURE_I18N_KEYS;

/** Whether a failure kind has localized copy in this build. */
export function isKnownProviderPresetFailure(
  kind: string,
): kind is KnownProviderPresetFailure {
  return Object.prototype.hasOwnProperty.call(
    PROVIDER_PRESET_FAILURE_I18N_KEYS,
    kind,
  );
}

function isStringRecord(value: unknown): value is Record<string, string> {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }
  return Object.values(value as Record<string, unknown>).every(
    (entry) => typeof entry === "string",
  );
}

/**
 * Read a thrown preset action into the shape the form renders.
 *
 * Duck-typed rather than `instanceof` on the core error class: the section's
 * own test suite mocks `@multica/core/runtimes`, and a cross-module identity
 * check would silently stop matching there — turning every failure back into
 * the generic fallback in exactly the suite meant to guard the copy.
 */
export function providerPresetFailureFrom(
  error: unknown,
  fallback: string,
): ProviderPresetFailure {
  if (error instanceof Error) {
    const carrier = error as Error & { kind?: unknown; params?: unknown };
    return {
      kind: typeof carrier.kind === "string" ? carrier.kind : "",
      params: isStringRecord(carrier.params) ? carrier.params : {},
      message: error.message.trim() || fallback,
    };
  }
  if (typeof error === "string" && error.trim()) {
    return { kind: "", params: {}, message: error.trim() };
  }
  return { kind: "", params: {}, message: fallback };
}

/**
 * Whether the only way forward is a new key on the provider's own dashboard.
 *
 * `429 RATE_LIMITED` on a key bound to a cancelled billing cycle looks exactly
 * like an exhausted quota, and the provider's dashboard calls it healthy. The
 * daemon says which one it is with `action=regenerate_key`; the form turns that
 * into a link instead of a sentence the user can only agree with.
 */
export function providerPresetNeedsKeyRegeneration(
  failure: ProviderPresetFailure,
): boolean {
  return failure.kind === "rate_limited" && failure.params.action === "regenerate_key";
}

/**
 * The origin of the endpoint, used as the dashboard link's target.
 *
 * Not a promised console URL — the API host is the only provider host this app
 * has been told about, and offering it is still better than an instruction with
 * nothing to click. Null when the value is not an http(s) URL.
 */
export function providerConsoleUrl(baseUrl: string): string | null {
  try {
    const url = new URL(baseUrl.trim());
    if (url.protocol !== "http:" && url.protocol !== "https:") return null;
    return `${url.protocol}//${url.host}/`;
  } catch {
    return null;
  }
}

/**
 * The save flow as a small machine, so "a failed verification must not read as
 * saved" is a property of the transition table rather than of one call site's
 * `try`/`catch` ordering.
 *
 *   idle ──begin──▶ verifying ──succeeded──▶ saved
 *                      └──────failed───────▶ failed
 *
 * `succeeded` and `failed` are accepted only from `verifying`. A stray success
 * arriving after a failure is ignored, which is what keeps a late receipt from
 * flipping the form to "saved" while the failure is still on screen.
 */
export type ProviderPresetSavePhase = "idle" | "verifying" | "saved" | "failed";

export interface ProviderPresetSaveState {
  phase: ProviderPresetSavePhase;
  failure: ProviderPresetFailure | null;
}

export const IDLE_PROVIDER_PRESET_SAVE: ProviderPresetSaveState = {
  phase: "idle",
  failure: null,
};

export type ProviderPresetSaveEvent =
  | { type: "begin" }
  | { type: "succeeded" }
  | { type: "failed"; failure: ProviderPresetFailure };

export function reduceProviderPresetSave(
  state: ProviderPresetSaveState,
  event: ProviderPresetSaveEvent,
): ProviderPresetSaveState {
  switch (event.type) {
    case "begin":
      return { phase: "verifying", failure: null };
    case "succeeded":
      return state.phase === "verifying"
        ? { phase: "saved", failure: null }
        : state;
    case "failed":
      return state.phase === "verifying"
        ? { phase: "failed", failure: event.failure }
        : state;
  }
}
