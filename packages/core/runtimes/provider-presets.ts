import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type {
  RuntimeProviderPreset,
  RuntimeProviderPresetAction,
  RuntimeProviderPresetModel,
  RuntimeProviderPresetRequest,
  RuntimeProviderPresetStatus,
  RuntimeProviderPresetTicket,
  RuntimeProviderPresetUpsertInput,
  RuntimeProviderPresetsResult,
} from "../types";

// Provider presets are the routes the agent CLI picks a default model from.
// The daemon owns the files, so this module is the client half of the
// park-then-poll contract: a POST enqueues one action for the runtime's daemon,
// a GET carries the outcome, and every successful action answers with the
// refreshed list so the caller never has to follow up with a separate read.
//
// The provider dimension is not a parameter. The daemon registers exactly one
// driver today (`server/internal/daemon/dsh_providers.go`) and rejects an id it
// has no driver for rather than guessing, so a second provider is a backend
// change that lands with its own UI — not something this client should be able
// to name speculatively.
export const PROVIDER_PRESET_PROVIDER = "dsh";

// The wire protocols a preset may declare, in the order the CLI itself lists
// them (`supportedProtocols()` in `@deepseek-ai/dsh-llm-pi-ai`). That order is
// stable and its first entry is the default DSH presents for a new route, so
// the form's default and this list cannot drift apart.
export const PROVIDER_PRESET_APIS = [
  "openai-completions",
  "openai-responses",
  "anthropic-messages",
] as const;

export const PROVIDER_PRESET_DEFAULT_API = PROVIDER_PRESET_APIS[0];

export const runtimeProviderPresetsKeys = {
  all: () => ["runtimes", "provider-presets"] as const,
  forRuntime: (runtimeId: string) =>
    [...runtimeProviderPresetsKeys.all(), runtimeId] as const,
};

const POLL_INTERVAL_MS = 500;

// The client must not give up on a request the server still considers live.
// Those two windows are sequential, not overlapping
// (server/internal/handler/runtime_provider_presets.go):
//
//   - pending: measured from CreatedAt, so the daemon has up to 30s to CLAIM
//     the request off a heartbeat.
//   - running: measured from RunStartedAt, which PopPending sets at claim
//     time. Heartbeat pickup happens before the claim and is bounded by the
//     pending window, so it does not eat into this 60s.
//
// Budgeting for the sum lets the server's own timeout text ("daemon did not
// respond within 30 seconds") win instead of ours — it is the more specific of
// the two, and it is the one that says whether the daemon was ever reached.
const SERVER_PENDING_TIMEOUT_MS = 30_000;
const SERVER_RUNNING_TIMEOUT_MS = 60_000;
// Slack for the poll interval, the store's own sweep granularity and network
// latency on the final GET — without it the client can still expire in the same
// tick the server transitions the record.
const POLL_SLACK_MS = 10_000;
const POLL_TIMEOUT_MS =
  SERVER_PENDING_TIMEOUT_MS + SERVER_RUNNING_TIMEOUT_MS + POLL_SLACK_MS;

// A preset action is a write to the user's own configuration file, so a live
// answer is what the section renders. The list is short-lived on purpose: the
// files can also be edited outside this app, and a stale list would let the UI
// offer an activate against a route that no longer exists.
const PRESETS_STALE_TIME_MS = 30_000;

// knownPresetStatus narrows the wire status to the states this client models.
// A status we do not know is reported as `failed` rather than treated as
// success: the poll loop would otherwise run forever on a newer server's
// intermediate state, and `completed` is the only value that licenses reading
// the reply as a configuration.
function knownPresetStatus(status: string): RuntimeProviderPresetStatus {
  switch (status) {
    case "pending":
    case "running":
    case "completed":
    case "failed":
    case "timeout":
      return status;
    default:
      return "failed";
  }
}

/**
 * A terminal provider-preset failure, carrying the daemon's machine-readable
 * classification next to its English sentence.
 *
 * The kind is why this is a class rather than a plain `Error`: "the quota is
 * exhausted" and "this key is bound to a cancelled billing cycle" are the same
 * HTTP status and lead the user in opposite directions, and only the kind plus
 * its parameters tells a localized surface which copy to render.
 */
export class ProviderPresetActionError extends Error {
  readonly kind: string;
  readonly params: Record<string, string>;

  constructor(
    message: string,
    kind = "",
    params: Record<string, string> = {},
  ) {
    super(message);
    this.name = "ProviderPresetActionError";
    this.kind = kind;
    this.params = params;
  }
}

/** The failure kind on a preset error, or "" when the error carries none. */
export function providerPresetErrorKind(error: unknown): string {
  return error instanceof ProviderPresetActionError ? error.kind : "";
}

// presetsFrom renders one reply as the cache entry. `cleared_active` is read
// with `=== true` rather than truthiness: a backend that predates the field
// omits it, and "absent" must not read as "the active model was just cleared".
function presetsFrom(
  request: RuntimeProviderPresetRequest,
): RuntimeProviderPresetsResult {
  return {
    presets: request.providers ?? [],
    active: request.active ?? null,
    clearedActive: request.cleared_active === true,
  };
}

// awaitProviderPreset polls one parked action to a terminal status.
//
// The POST answers with a ticket, not the record, so the loop is seeded from
// the ticket's status and reads the record on each pass. Only an explicit
// `completed` is a result: `failed`, `timeout`, and a status this client does
// not know all surface as errors, because an unrecognised status rendered as an
// empty list would look like an authoritative "this machine has no presets".
async function awaitProviderPreset(
  runtimeId: string,
  action: RuntimeProviderPresetAction,
  ticket: RuntimeProviderPresetTicket,
): Promise<RuntimeProviderPresetRequest> {
  const start = Date.now();
  let current: RuntimeProviderPresetRequest = {
    id: ticket.id,
    runtime_id: runtimeId,
    provider: PROVIDER_PRESET_PROVIDER,
    action,
    status: knownPresetStatus(ticket.status),
    created_at: "",
    updated_at: "",
  };

  while (current.status === "pending" || current.status === "running") {
    if (Date.now() - start > POLL_TIMEOUT_MS) {
      throw new Error("provider preset action timed out");
    }
    await new Promise((resolve) => setTimeout(resolve, POLL_INTERVAL_MS));
    current = await api.getProviderPresetResult(runtimeId, ticket.id);
    current.status = knownPresetStatus(current.status);
  }

  if (current.status !== "completed") {
    throw new ProviderPresetActionError(
      current.error || `provider preset ${action} failed (status: ${current.status})`,
      current.error_kind ?? "",
      current.error_params ?? {},
    );
  }
  return current;
}

// providerPresetPayload builds the daemon-side action body. It is the only
// place the write-only key is put on the wire, and the only place that decides
// whether it goes at all.
function providerPresetPayload(
  input: ProviderPresetActionInput,
): Record<string, unknown> {
  switch (input.action) {
    case "upsert": {
      const payload: Record<string, unknown> = {
        id: input.preset.id,
        api: input.preset.api,
        base_url: input.preset.base_url,
        api_key_env: input.preset.api_key_env ?? "",
        models: input.preset.models,
      };
      // Omitted, never blanked: an edit that leaves the key field empty must
      // keep the stored credential, and the field can only be empty when the
      // user did not type one (the mask is never pre-filled into it).
      if (input.preset.api_key) payload.api_key = input.preset.api_key;
      return payload;
    }
    case "delete":
      return { id: input.id };
    case "activate":
      // An empty model asks the daemon for the preset's first model, which is
      // what "use this provider" means when the user did not pick one.
      return input.model ? { id: input.id, model: input.model } : { id: input.id };
    case "models": {
      // Only what the caller actually has. A blank field falls back on the
      // daemon side to whatever the named preset already stores — endpoint,
      // credential and protocol — which is what makes "fetch with the key I
      // already saved" work without ever sending the key back to the browser.
      const payload: Record<string, unknown> = {
        base_url: input.baseUrl.trim(),
        api: input.api.trim(),
      };
      if (input.id?.trim()) payload.id = input.id.trim();
      // Same write-only channel as an upsert: present only when typed.
      if (input.apiKey?.trim()) payload.api_key = input.apiKey.trim();
      return payload;
    }
  }
}

/** What a catalog fetch needs from the form. */
export interface ProviderPresetModelsQuery {
  /** Existing preset whose stored endpoint, key and protocol fill blank fields. */
  id?: string;
  baseUrl: string;
  api: string;
  /** Write-only, exactly like an upsert's key. */
  apiKey?: string;
}

export type ProviderPresetActionInput =
  | { action: "upsert"; preset: RuntimeProviderPresetUpsertInput }
  | { action: "delete"; id: string }
  | { action: "activate"; id: string; model?: string }
  | ({ action: "models" } & ProviderPresetModelsQuery);

/**
 * Park one preset action and poll it to a terminal record. Kept separate from
 * `runProviderPresetAction` so a caller that needs a field other than the
 * preset list — `models` answers with a catalog — can read the record itself.
 */
async function runProviderPresetActionRequest(
  runtimeId: string,
  input: ProviderPresetActionInput,
): Promise<RuntimeProviderPresetRequest> {
  const ticket = await api.initiateProviderPresetAction(
    runtimeId,
    PROVIDER_PRESET_PROVIDER,
    input.action,
    providerPresetPayload(input),
  );
  return awaitProviderPreset(runtimeId, input.action, ticket);
}

/**
 * Run one preset action to completion and return the refreshed configuration.
 *
 * Deliberately not optimistic. These four actions write the user's own
 * configuration file, they fail for ordinary reasons (an unreadable settings
 * file, a credentials file written by another process), and the reply itself
 * carries the machine's real state — so there is nothing to predict locally and
 * a wrong guess would render a preset list the machine does not have.
 */
export async function runProviderPresetAction(
  runtimeId: string,
  input: ProviderPresetActionInput,
): Promise<RuntimeProviderPresetsResult> {
  return presetsFrom(await runProviderPresetActionRequest(runtimeId, input));
}

/** The endpoint's own catalog, plus the protocol whose auth convention worked. */
export interface ProviderPresetModelsResult {
  /** Ids verbatim, exactly as the gateway spelled them — never escaped. */
  models: RuntimeProviderPresetModel[];
  /**
   * The protocol under which the endpoint accepted the credential. Equal to the
   * declared one unless the wrong auth convention was tried first and the other
   * was retried (see `fetchProviderPresetModels`).
   */
  api: string;
}

// The two auth conventions a route can want. Anthropic-shaped gateways read the
// key from `x-api-key`, everything else from `Authorization: Bearer`, and a
// preset created before its protocol was set defaults to the OpenAI shape.
// Picking the wrong one reads as a rejected key, which is indistinguishable
// from a genuinely bad one until the other convention is tried.
function otherAuthConvention(api: string): string {
  return api.startsWith("anthropic")
    ? PROVIDER_PRESET_DEFAULT_API
    : "anthropic-messages";
}

/**
 * Fetch a preset's model catalog without writing anything.
 *
 * The declared protocol is tried first; on a rejected credential the one other
 * auth convention is tried exactly once. That is what removes the
 * chicken-and-egg for a new anthropic gateway: its catalog can be fetched
 * without first switching the form's protocol by hand, and the protocol that
 * answered is returned so the form can reflect it.
 *
 * Deliberately not optimistic and deliberately not cached: the catalog is the
 * endpoint's live answer, and a stale one would offer a model the route no
 * longer serves.
 */
export async function fetchProviderPresetModels(
  runtimeId: string,
  query: ProviderPresetModelsQuery,
): Promise<ProviderPresetModelsResult> {
  const declared = query.api.trim() || PROVIDER_PRESET_DEFAULT_API;
  try {
    const request = await runProviderPresetActionRequest(runtimeId, {
      action: "models",
      ...query,
      api: declared,
    });
    return { models: request.models ?? [], api: declared };
  } catch (error) {
    // Only a rejected credential is worth a second convention. An unreachable
    // endpoint, a gateway with no catalog and a rate-limited account answer the
    // same way to both, so a retry would only double the wait.
    if (providerPresetErrorKind(error) !== "invalid_credential") throw error;
    const fallback = otherAuthConvention(declared);
    const request = await runProviderPresetActionRequest(runtimeId, {
      action: "models",
      ...query,
      api: fallback,
    });
    return { models: request.models ?? [], api: fallback };
  }
}

/** Read the machine's presets and which one is in effect. */
export async function resolveRuntimeProviderPresets(
  runtimeId: string,
): Promise<RuntimeProviderPresetsResult> {
  const ticket = await api.initiateProviderPresetAction(
    runtimeId,
    PROVIDER_PRESET_PROVIDER,
    "list",
  );
  return presetsFrom(await awaitProviderPreset(runtimeId, "list", ticket));
}

export function runtimeProviderPresetsOptions(
  runtimeId: string | null | undefined,
) {
  return queryOptions({
    queryKey: runtimeId
      ? runtimeProviderPresetsKeys.forRuntime(runtimeId)
      : runtimeProviderPresetsKeys.all(),
    queryFn: () => resolveRuntimeProviderPresets(runtimeId as string),
    enabled: Boolean(runtimeId),
    staleTime: PRESETS_STALE_TIME_MS,
    retry: false,
  });
}

/**
 * One preset mutation. Every successful action answers with the full refreshed
 * list, so the receipt is written straight into the cache — that IS the
 * refresh. A follow-up list would be a second round trip to the user's machine
 * for data already in hand, and it could lose the race and briefly redraw the
 * pre-action configuration.
 *
 * The mutation result carries `clearedActive`, which the caller needs to say
 * out loud: deleting the preset that was in effect leaves DSH with no default
 * model.
 */
export function useProviderPresetMutation(runtimeId: string | null | undefined) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: ProviderPresetActionInput) =>
      runProviderPresetAction(runtimeId as string, input),
    onSuccess: (result) => {
      if (!runtimeId) return;
      queryClient.setQueryData(
        runtimeProviderPresetsKeys.forRuntime(runtimeId),
        result,
      );
    },
  });
}

/** The preset `agent-default-model` currently points at, when the reply names one. */
export function activeProviderPreset(
  presets: readonly RuntimeProviderPreset[],
): RuntimeProviderPreset | null {
  return presets.find((preset) => preset.active === true) ?? null;
}
