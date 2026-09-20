"use client";

// The "providers" half of the agent accounts tab (DENE-348).
//
// The account half above it answers "which CLI configuration DIRECTORY is in
// effect". This one answers a different question — "which endpoint and key
// does the CLI send requests to" — and the two are deliberately siblings
// rather than merged: a user can keep one account directory and swap the
// route under it, or keep the route and swap accounts.
//
// Nothing here is optimistic. All four actions write files on the user's own
// machine, they fail for ordinary reasons (a settings file another process is
// holding, an unparseable credentials file), the user stays on this screen,
// and the receipt carries the machine's real state — so there is nothing worth
// predicting and a wrong guess would draw a configuration the machine does not
// have. Every button waits for the daemon and redraws from its answer.
//
// The key is write-only end to end: the daemon reports a mask, no type in this
// tree has a field for a key value, and the input starts empty and stays empty
// unless the user types. `provider-presets-model.ts` holds the rules and their
// canonical tests.

import { useEffect, useMemo, useReducer, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  Check,
  ChevronDown,
  ExternalLink,
  KeyRound,
  Loader2,
  Plus,
  RefreshCw,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import {
  runtimeProviderPresetsKeys,
  runtimeProviderPresetsOptions,
  useProviderPresetMutation,
  fetchProviderPresetModels,
  PROVIDER_PRESET_APIS,
} from "@multica/core/runtimes";
import type {
  RuntimeDevice,
  RuntimeProviderPreset,
  RuntimeProviderPresetModel,
} from "@multica/core/types";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@multica/ui/components/ui/field";
import { Input } from "@multica/ui/components/ui/input";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../../i18n";
import {
  IDLE_PROVIDER_PRESET_SAVE,
  PROVIDER_PRESET_FAILURE_I18N_KEYS,
  type ProviderPresetFailure,
  type ProviderPresetFieldError,
  type ProviderPresetForm,
  canFetchProviderPresetModels,
  canManageProviderPresets,
  emptyProviderPresetForm,
  filterProviderPresetModels,
  isKnownProviderPresetFailure,
  providerConsoleUrl,
  providerPresetContextWindow,
  providerPresetFailureFrom,
  providerPresetFormFrom,
  providerPresetKeyState,
  providerPresetModelLabel,
  providerPresetModels,
  providerPresetNeedsKeyRegeneration,
  providerPresetSummaryLine,
  providerPresetUpsertInput,
  providerPresetsViewState,
  providerSeatModelDisplay,
  providerSeatModelString,
  reduceProviderPresetSave,
  supportsProviderPresets,
  validateProviderPresetForm,
} from "./provider-presets-model";

export interface AgentProviderPresetsSectionProps {
  runtimeDevice?: RuntimeDevice;
}

/**
 * Renders nothing unless the agent's runtime runs a CLI the daemon has a
 * preset driver for. Not a disabled block — see `supportsProviderPresets`.
 */
export function AgentProviderPresetsSection({
  runtimeDevice,
}: AgentProviderPresetsSectionProps) {
  if (!runtimeDevice || !supportsProviderPresets(runtimeDevice.provider)) {
    return null;
  }
  return <ProviderPresets runtimeId={runtimeDevice.id} />;
}

function ProviderPresets({ runtimeId }: { runtimeId: string }) {
  const { t } = useT("agents");
  const queryClient = useQueryClient();
  const query = useQuery(runtimeProviderPresetsOptions(runtimeId));
  const mutation = useProviderPresetMutation(runtimeId);

  const [editing, setEditing] = useState<ProviderPresetForm | null>(null);
  const [pendingDelete, setPendingDelete] = useState<RuntimeProviderPreset | null>(
    null,
  );
  // Which row's activate is in flight, so only that button shows a spinner
  // instead of every row going busy at once.
  const [activatingId, setActivatingId] = useState<string | null>(null);

  const state = useMemo(
    () =>
      providerPresetsViewState({
        presets: query.data?.presets,
        loading: query.isPending,
        error: query.isError
          ? query.error instanceof Error && query.error.message
            ? query.error.message
            : t(($) => $.tab_body.providers.error_description)
          : "",
      }),
    [query.data, query.isPending, query.isError, query.error, t],
  );

  const manageable = canManageProviderPresets(state);

  // The seat's model string, rebuilt from the pair the daemon reported. Empty
  // when either half is missing: a half-reported pair is not a seat value and
  // rendering one would invent a route.
  const active = query.data?.active ?? null;
  const activeSeatModel =
    active && active.provider.trim() && active.model.trim()
      ? providerSeatModelString(active.provider, active.model)
      : "";

  const handleActivate = async (preset: RuntimeProviderPreset) => {
    setActivatingId(preset.id);
    try {
      await mutation.mutateAsync({ action: "activate", id: preset.id });
      toast.success(t(($) => $.tab_body.providers.activated_toast, { name: preset.id }));
    } catch (err) {
      toast.error(errorText(err, t(($) => $.tab_body.providers.activate_failed_toast)));
    } finally {
      setActivatingId(null);
    }
  };

  const handleDelete = async (preset: RuntimeProviderPreset) => {
    try {
      const result = await mutation.mutateAsync({ action: "delete", id: preset.id });
      // `cleared_active` is the daemon telling us the machine now has no
      // default model at all. That is a consequence the user has to be told
      // about out loud, not something to infer from a row disappearing.
      toast.success(
        result.clearedActive
          ? t(($) => $.tab_body.providers.deleted_cleared_toast, { name: preset.id })
          : t(($) => $.tab_body.providers.deleted_toast, { name: preset.id }),
      );
    } catch (err) {
      toast.error(errorText(err, t(($) => $.tab_body.providers.delete_failed_toast)));
    } finally {
      setPendingDelete(null);
    }
  };

  const handleSubmit = async (form: ProviderPresetForm) => {
    // Saving IS the verification: the daemon runs both probes before it writes
    // anything and a failure leaves the file untouched. The dialog owns the
    // resulting state, so this only reports success upward. It deliberately
    // does not toast a failure — a failed save belongs in the form beside the
    // fields that caused it, with the localized reason and, for a dead billing
    // cycle, the one actionable next step.
    await mutation.mutateAsync({
      action: "upsert",
      preset: providerPresetUpsertInput(form),
    });
    toast.success(
      form.editingId
        ? t(($) => $.tab_body.providers.updated_toast, { name: form.id.trim() })
        : t(($) => $.tab_body.providers.created_toast, { name: form.id.trim() }),
    );
  };

  const handleRetry = () => {
    void queryClient.invalidateQueries({
      queryKey: runtimeProviderPresetsKeys.forRuntime(runtimeId),
    });
  };

  return (
    <section className="space-y-3" aria-label={t(($) => $.tab_body.providers.title)}>
      <div className="flex flex-wrap items-start gap-x-3 gap-y-2">
        <div className="min-w-0 flex-1">
          <h3 className="text-body font-medium">
            {t(($) => $.tab_body.providers.title)}
          </h3>
          <p className="mt-0.5 max-w-2xl text-pretty text-caption leading-5 text-muted-foreground">
            {t(($) => $.tab_body.providers.intro)}
          </p>
        </div>
        {manageable ? (
          <Button
            type="button"
            size="sm"
            variant="outline"
            className="shrink-0"
            onClick={() => setEditing(emptyProviderPresetForm())}
          >
            <Plus className="size-3.5" aria-hidden="true" />
            {t(($) => $.tab_body.providers.add_action)}
          </Button>
        ) : null}
      </div>

      {state.kind === "loading" ? <ProviderPresetsLoading /> : null}

      {state.kind === "error" ? (
        <div className="rounded-lg border border-destructive/30 bg-background p-5 text-center">
          <span className="mx-auto flex size-9 items-center justify-center rounded-lg bg-destructive/10 text-destructive">
            <AlertTriangle className="size-4" aria-hidden="true" />
          </span>
          <p className="mt-3 text-body font-medium">
            {t(($) => $.tab_body.providers.error_title)}
          </p>
          <p className="mx-auto mt-1 max-w-lg text-pretty text-caption leading-5 text-muted-foreground">
            {t(($) => $.tab_body.providers.error_description)}
          </p>
          <p
            className="mx-auto mt-2 max-w-lg truncate font-mono text-micro text-muted-foreground"
            translate="no"
          >
            {state.message}
          </p>
          <Button type="button" size="sm" className="mt-4" onClick={handleRetry}>
            {t(($) => $.tab_body.providers.error_retry_action)}
          </Button>
        </div>
      ) : null}

      {state.kind === "empty" ? (
        <div className="rounded-lg border border-border bg-background p-6 text-center">
          <span className="mx-auto flex size-10 items-center justify-center rounded-lg bg-muted text-muted-foreground">
            <KeyRound className="size-4" aria-hidden="true" />
          </span>
          <p className="mt-3 text-body font-medium">
            {t(($) => $.tab_body.providers.empty_title)}
          </p>
          <p className="mx-auto mt-1 max-w-lg text-pretty text-caption leading-5 text-muted-foreground">
            {t(($) => $.tab_body.providers.empty_description)}
          </p>
        </div>
      ) : null}

      {state.kind === "ready" ? (
        <ul className="divide-y divide-border overflow-hidden rounded-lg border border-border bg-surface">
          {state.presets.map((preset) => (
            <ProviderPresetRow
              key={preset.id}
              preset={preset}
              busy={mutation.isPending}
              activating={activatingId === preset.id}
              onActivate={() => void handleActivate(preset)}
              onEdit={() => setEditing(providerPresetFormFrom(preset))}
              onDelete={() => setPendingDelete(preset)}
            />
          ))}
        </ul>
      ) : null}

      {/* What the default model actually is, said in the two names the user
          picked. The seat's own value is `provider/encoded-model`; rendering
          that verbatim would put `%2F` on screen and invite a hand edit, which
          is how the prefix went missing in DENE-680. */}
      {state.kind === "ready" && activeSeatModel ? (
        <p
          className="text-caption text-muted-foreground"
          data-testid="provider-presets-active-model"
          translate="no"
        >
          {t(($) => $.tab_body.providers.active_model, {
            model: providerSeatModelDisplay(activeSeatModel, state.presets),
          })}
        </p>
      ) : null}

      {editing ? (
        <ProviderPresetDialog
          runtimeId={runtimeId}
          form={editing}
          onChange={setEditing}
          onClose={() => setEditing(null)}
          onSubmit={() => handleSubmit(editing)}
        />
      ) : null}

      {pendingDelete ? (
        <DeletePresetDialog
          preset={pendingDelete}
          onCancel={() => setPendingDelete(null)}
          onConfirm={() => void handleDelete(pendingDelete)}
        />
      ) : null}
    </section>
  );
}

function ProviderPresetRow({
  preset,
  busy,
  activating,
  onActivate,
  onEdit,
  onDelete,
}: {
  preset: RuntimeProviderPreset;
  busy: boolean;
  activating: boolean;
  onActivate: () => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const { t } = useT("agents");
  const active = preset.active === true;
  const keyState = providerPresetKeyState(preset);

  return (
    <li
      data-active={active ? "true" : undefined}
      className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2 p-3.5 transition-colors hover:bg-muted/50"
    >
      <div className="min-w-0 flex-1 basis-56">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
          {/* The active row is marked by weight and text colour, which hover
              does not touch, plus a pill — so selecting stays legible while
              the pointer is on the row. */}
          <span
            className={cn(
              "min-w-0 truncate text-caption",
              active ? "font-semibold text-foreground" : "font-medium",
            )}
            translate="no"
          >
            {preset.id}
          </span>
          {active ? (
            <span className="inline-flex shrink-0 items-center rounded-full bg-brand/10 px-2 py-0.5 text-micro font-medium text-brand">
              {t(($) => $.tab_body.providers.active_badge)}
            </span>
          ) : null}
        </div>
        <p
          className="mt-0.5 truncate font-mono text-micro text-muted-foreground"
          translate="no"
        >
          {providerPresetSummaryLine(preset)}
        </p>
        <p className="mt-0.5 truncate text-micro text-muted-foreground">
          {keyState === "masked"
            ? t(($) => $.tab_body.providers.key_masked, { mask: preset.key_mask ?? "" })
            : keyState === "stored"
              ? t(($) => $.tab_body.providers.key_stored)
              : t(($) => $.tab_body.providers.key_absent)}
          {preset.models.length > 0
            ? ` · ${t(($) => $.tab_body.providers.model_count, { count: preset.models.length })}`
            : ""}
        </p>
      </div>
      <div className="ms-auto flex shrink-0 items-center gap-2">
        {active ? null : (
          <Button type="button" size="sm" disabled={busy} onClick={onActivate}>
            {activating ? (
              <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
            ) : null}
            {t(($) => $.tab_body.providers.activate_action)}
          </Button>
        )}
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={busy}
          onClick={onEdit}
        >
          {t(($) => $.tab_body.providers.edit_action)}
        </Button>
        <Button
          type="button"
          size="sm"
          variant="ghost"
          disabled={busy}
          onClick={onDelete}
          aria-label={t(($) => $.tab_body.providers.delete_aria, { name: preset.id })}
        >
          <Trash2 className="size-3.5" aria-hidden="true" />
        </Button>
      </div>
    </li>
  );
}

function ProviderPresetDialog({
  runtimeId,
  form,
  onChange,
  onClose,
  onSubmit,
}: {
  runtimeId: string;
  form: ProviderPresetForm;
  onChange: (next: ProviderPresetForm) => void;
  onClose: () => void;
  /** Resolves once the daemon accepted the write; rejects on a failed probe. */
  onSubmit: () => Promise<void>;
}) {
  const { t } = useT("agents");
  const [showErrors, setShowErrors] = useState(false);
  // The save flow's own state. A failed verification lands in `failed` and the
  // dialog stays open; only a `succeeded` transition closes it.
  const [save, dispatchSave] = useReducer(
    reduceProviderPresetSave,
    IDLE_PROVIDER_PRESET_SAVE,
  );
  const errors = validateProviderPresetForm(form);
  const editing = form.editingId !== "";
  const saving = save.phase === "verifying";

  const set = <K extends keyof ProviderPresetForm>(
    key: K,
    value: ProviderPresetForm[K],
  ) => onChange({ ...form, [key]: value });

  const has = (error: ProviderPresetFieldError) =>
    showErrors && errors.includes(error);

  const handleSubmit = async () => {
    if (errors.length > 0) {
      setShowErrors(true);
      return;
    }
    dispatchSave({ type: "begin" });
    try {
      await onSubmit();
      dispatchSave({ type: "succeeded" });
      onClose();
    } catch (error) {
      // No write landed — the daemon runs both probes before it touches the
      // file. The reason belongs in the form, beside the fields that produced
      // it, not in a toast the user has to hold in their head.
      dispatchSave({
        type: "failed",
        failure: providerPresetFailureFrom(
          error,
          t(($) => $.tab_body.providers.save_failed_toast),
        ),
      });
    }
  };

  return (
    <Dialog open onOpenChange={(open) => (open ? undefined : onClose())}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>
            {editing
              ? t(($) => $.tab_body.providers.form_title_edit)
              : t(($) => $.tab_body.providers.form_title_add)}
          </DialogTitle>
          <DialogDescription>
            {t(($) => $.tab_body.providers.form_description)}
          </DialogDescription>
        </DialogHeader>

        <FieldGroup>
          <Field>
            <FieldLabel htmlFor="preset-id">
              {t(($) => $.tab_body.providers.field_id)}
            </FieldLabel>
            <Input
              id="preset-id"
              value={form.id}
              // The id is the preset's identity in the daemon's files, so an
              // edit keeps it fixed: renaming would be an add plus a delete,
              // and doing that silently could strand the active pointer.
              disabled={editing}
              onChange={(event) => set("id", event.target.value)}
              // eslint-disable-next-line no-restricted-syntax -- sample id, not prose: it shows the allowed character set, which is the same in every locale.
              placeholder="my-provider"
              aria-invalid={has("id_required") || has("id_invalid")}
            />
            <FieldDescription>
              {t(($) => $.tab_body.providers.field_id_hint)}
            </FieldDescription>
            {has("id_required") ? (
              <FieldError>{t(($) => $.tab_body.providers.error_id_required)}</FieldError>
            ) : null}
            {has("id_invalid") ? (
              <FieldError>{t(($) => $.tab_body.providers.error_id_invalid)}</FieldError>
            ) : null}
          </Field>

          <Field>
            <FieldLabel htmlFor="preset-base-url">
              {t(($) => $.tab_body.providers.field_base_url)}
            </FieldLabel>
            <Input
              id="preset-base-url"
              value={form.baseUrl}
              onChange={(event) => set("baseUrl", event.target.value)}
              placeholder="https://api.example.com/v1"
              aria-invalid={has("base_url_required") || has("base_url_invalid")}
            />
            {has("base_url_required") ? (
              <FieldError>
                {t(($) => $.tab_body.providers.error_base_url_required)}
              </FieldError>
            ) : null}
            {has("base_url_invalid") ? (
              <FieldError>
                {t(($) => $.tab_body.providers.error_base_url_invalid)}
              </FieldError>
            ) : null}
          </Field>

          <Field>
            <FieldLabel htmlFor="preset-api">
              {t(($) => $.tab_body.providers.field_api)}
            </FieldLabel>
            <Select
              items={PROVIDER_PRESET_APIS.map((api) => ({ value: api, label: api }))}
              value={form.api}
              onValueChange={(value) => value && set("api", value)}
            >
              <SelectTrigger id="preset-api">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {PROVIDER_PRESET_APIS.map((api) => (
                  <SelectItem key={api} value={api}>
                    {api}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {/* The gateway is the authority on its own wire protocol: the save
                records what its `supported_endpoints` say, and this select is
                only consulted when it does not describe them. Saying so is what
                keeps "I chose A" and "the file says B" from being a surprise. */}
            <FieldDescription>
              {t(($) => $.tab_body.providers.field_api_hint)}
            </FieldDescription>
          </Field>

          <Field>
            <FieldLabel htmlFor="preset-api-key">
              {t(($) => $.tab_body.providers.field_api_key)}
            </FieldLabel>
            {/* Write-only. The value box starts empty and is never seeded from
                the mask — the stored credential's state is reported beside it
                as text instead, so a submit without typing carries no key. */}
            <Input
              id="preset-api-key"
              type="password"
              autoComplete="off"
              value={form.apiKey}
              onChange={(event) => set("apiKey", event.target.value)}
              placeholder={t(($) => $.tab_body.providers.field_api_key_placeholder)}
            />
            <FieldDescription>
              {form.hasKey
                ? form.keyMask
                  ? t(($) => $.tab_body.providers.field_api_key_hint_masked, {
                      mask: form.keyMask,
                    })
                  : t(($) => $.tab_body.providers.field_api_key_hint_stored)
                : t(($) => $.tab_body.providers.field_api_key_hint_absent)}
            </FieldDescription>
          </Field>

          <Field>
            <FieldLabel htmlFor="preset-api-key-env">
              {t(($) => $.tab_body.providers.field_api_key_env)}
            </FieldLabel>
            <Input
              id="preset-api-key-env"
              value={form.apiKeyEnv}
              onChange={(event) => set("apiKeyEnv", event.target.value)}
              // eslint-disable-next-line no-restricted-syntax -- sample environment variable name, not prose: shell variable names are not translated.
              placeholder="MY_PROVIDER_API_KEY"
              aria-invalid={has("api_key_env_invalid")}
            />
            <FieldDescription>
              {t(($) => $.tab_body.providers.field_api_key_env_hint)}
            </FieldDescription>
            {has("api_key_env_invalid") ? (
              <FieldError>
                {t(($) => $.tab_body.providers.error_api_key_env_invalid)}
              </FieldError>
            ) : null}
          </Field>

          <ProviderPresetModelsField
            runtimeId={runtimeId}
            form={form}
            onChange={onChange}
            showErrors={showErrors}
          />
        </FieldGroup>

        {saving ? (
          <div
            role="status"
            data-testid="preset-verify-progress"
            className="mt-3 flex items-start gap-2 rounded-md border border-border bg-muted/40 p-2.5"
          >
            <Loader2
              className="mt-0.5 size-4 shrink-0 animate-spin text-muted-foreground"
              aria-hidden="true"
            />
            <div className="min-w-0">
              <p className="text-caption font-medium">
                {t(($) => $.tab_body.providers.verifying_title)}
              </p>
              <p className="mt-0.5 text-pretty text-caption leading-5 text-muted-foreground">
                {t(($) => $.tab_body.providers.verifying_hint)}
              </p>
            </div>
          </div>
        ) : null}

        {save.phase === "failed" && save.failure ? (
          <ProviderPresetFailureNote failure={save.failure} baseUrl={form.baseUrl} />
        ) : null}

        <DialogFooter>
          <Button type="button" variant="outline" disabled={saving} onClick={onClose}>
            {t(($) => $.tab_body.providers.cancel_action)}
          </Button>
          <Button type="button" disabled={saving} onClick={() => void handleSubmit()}>
            {saving ? (
              <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
            ) : null}
            {t(($) => $.tab_body.providers.save_action)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/**
 * The model cell: a fetched, searchable catalog when the endpoint has one, and
 * the hand-typed id as the fallback for a gateway that does not.
 *
 * The catalog is the endpoint's own answer and is never cached — a stale list
 * would offer a model the route no longer serves. Manual entry survives on
 * purpose (`upsert` still probes the model it names), so a gateway with no
 * `/models` is not worse off than before this section existed.
 */
function ProviderPresetModelsField({
  runtimeId,
  form,
  onChange,
  showErrors,
}: {
  runtimeId: string;
  form: ProviderPresetForm;
  onChange: (next: ProviderPresetForm) => void;
  showErrors: boolean;
}) {
  const { t } = useT("agents");
  const [catalog, setCatalog] = useState<RuntimeProviderPresetModel[] | null>(
    null,
  );
  const [catalogFailure, setCatalogFailure] = useState<ProviderPresetFailure | null>(
    null,
  );
  const [fetching, setFetching] = useState(false);
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [manualId, setManualId] = useState("");

  // The fetch is a round trip through the user's machine, so the form can move
  // under it. Reading the latest value through a ref — updated after commit —
  // keeps a detected protocol from being written over whatever was typed while
  // the request was in flight.
  const latestForm = useRef(form);
  useEffect(() => {
    latestForm.current = form;
  }, [form]);

  const canFetch = canFetchProviderPresetModels(form);
  const selected = providerPresetModels(form);
  const selectedIds = new Set(selected.map((model) => model.id));
  const visible = catalog ? filterProviderPresetModels(catalog, search) : [];

  const fetchModels = async () => {
    setFetching(true);
    setCatalogFailure(null);
    try {
      const result = await fetchProviderPresetModels(runtimeId, {
        id: form.editingId || undefined,
        baseUrl: form.baseUrl.trim(),
        api: form.api.trim(),
        apiKey: form.apiKey.trim() || undefined,
      });
      setCatalog(result.models);
      // The endpoint answered under the other auth convention. Adopt it, so a
      // save on a gateway that never publishes its endpoints does not record a
      // protocol the route cannot speak.
      if (result.api !== latestForm.current.api) {
        onChange({ ...latestForm.current, api: result.api });
      }
    } catch (error) {
      setCatalog(null);
      setCatalogFailure(
        providerPresetFailureFrom(
          error,
          t(($) => $.tab_body.providers.fetch_models_failed),
        ),
      );
    } finally {
      setFetching(false);
    }
  };

  const toggle = (model: RuntimeProviderPresetModel) => {
    if (selectedIds.has(model.id)) {
      onChange({
        ...form,
        models: form.models.filter((row) => row.id.trim() !== model.id),
      });
      return;
    }
    // Drop the blank scaffolding row and any other empty row, then append the
    // picked model with the display name the endpoint gave it.
    onChange({
      ...form,
      models: [
        ...form.models.filter(
          (row) => row.id.trim() !== "" && row.id.trim() !== model.id,
        ),
        { id: model.id, name: model.name ?? "" },
      ],
    });
  };

  const removeSelected = (id: string) =>
    onChange({
      ...form,
      models: form.models.filter((row) => row.id.trim() !== id),
    });

  const addManual = () => {
    const id = manualId.trim();
    if (!id) return;
    setManualId("");
    if (selectedIds.has(id)) return;
    onChange({
      ...form,
      models: [
        ...form.models.filter((row) => row.id.trim() !== ""),
        { id, name: "" },
      ],
    });
  };

  return (
    <Field>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <FieldLabel>{t(($) => $.tab_body.providers.field_models)}</FieldLabel>
        <Button
          type="button"
          size="sm"
          variant="outline"
          className="shrink-0"
          disabled={!canFetch || fetching}
          onClick={() => void fetchModels()}
        >
          {fetching ? (
            <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
          ) : (
            <RefreshCw className="size-3.5" aria-hidden="true" />
          )}
          {fetching
            ? t(($) => $.tab_body.providers.fetch_models_loading)
            : t(($) => $.tab_body.providers.fetch_models_action)}
        </Button>
      </div>

      {!canFetch && !fetching ? (
        <FieldDescription>
          {t(($) => $.tab_body.providers.fetch_models_needs_credentials)}
        </FieldDescription>
      ) : null}

      {catalogFailure ? (
        <ProviderPresetFailureNote
          failure={catalogFailure}
          baseUrl={form.baseUrl}
        />
      ) : null}

      {catalog && catalog.length === 0 ? (
        <FieldDescription data-testid="preset-model-catalog-empty">
          {t(($) => $.tab_body.providers.fetch_models_empty)}
        </FieldDescription>
      ) : null}

      {catalog && catalog.length > 0 ? (
        <Popover open={open} onOpenChange={setOpen}>
          <PopoverTrigger
            data-testid="preset-model-catalog-trigger"
            className="mt-1.5 flex h-9 w-full items-center justify-between gap-2 rounded-lg border border-input bg-transparent px-3 text-body transition-colors outline-none hover:bg-muted/40 focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50"
          >
            <span className="truncate">
              {selected.length > 0
                ? t(($) => $.tab_body.providers.models_selected, {
                    count: selected.length,
                  })
                : t(($) => $.tab_body.providers.models_choose)}
            </span>
            <ChevronDown className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
          </PopoverTrigger>
          <PopoverContent
            align="start"
            className="w-[var(--anchor-width)] gap-0 p-2"
            data-testid="preset-model-catalog"
          >
            <Input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder={t(($) => $.tab_body.providers.models_search_placeholder)}
              aria-label={t(($) => $.tab_body.providers.models_search_placeholder)}
              autoComplete="off"
            />
            <div className="mt-1.5 max-h-64 overflow-y-auto">
              {visible.length === 0 ? (
                <p className="px-2 py-4 text-center text-caption text-muted-foreground">
                  {t(($) => $.tab_body.providers.models_search_empty)}
                </p>
              ) : (
                visible.map((model) => {
                  const isSelected = selectedIds.has(model.id);
                  const context = providerPresetContextWindow(model);
                  return (
                    <button
                      key={model.id}
                      type="button"
                      aria-pressed={isSelected}
                      data-testid={`preset-model-catalog-row-${model.id}`}
                      onClick={() => toggle(model)}
                      className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-body transition-colors hover:bg-muted/60"
                    >
                      <span
                        aria-hidden="true"
                        className={cn(
                          "flex size-4 shrink-0 items-center justify-center rounded-xs border",
                          isSelected
                            ? "border-primary bg-primary text-primary-foreground"
                            : "border-input",
                        )}
                      >
                        {isSelected ? <Check className="size-3" /> : null}
                      </span>
                      <span className="min-w-0 flex-1">
                        <span className="block truncate font-medium" translate="no">
                          {providerPresetModelLabel(model)}
                        </span>
                        <span
                          className="block truncate font-mono text-micro text-muted-foreground"
                          translate="no"
                        >
                          {model.id}
                        </span>
                      </span>
                      {context ? (
                        <span className="shrink-0 text-micro text-muted-foreground">
                          {t(($) => $.tab_body.providers.model_context, {
                            tokens: context.toLocaleString(),
                          })}
                        </span>
                      ) : null}
                    </button>
                  );
                })
              )}
            </div>
          </PopoverContent>
        </Popover>
      ) : null}

      {selected.length > 0 ? (
        <ul className="mt-2 space-y-1.5" data-testid="preset-selected-models">
          {selected.map((model, index) => {
            const entry = catalog?.find((candidate) => candidate.id === model.id);
            const context = entry ? providerPresetContextWindow(entry) : null;
            return (
              <li
                key={model.id}
                className="flex items-center gap-2 rounded-md border border-border bg-surface px-2.5 py-1.5"
              >
                <div className="min-w-0 flex-1">
                  <p className="truncate text-caption font-medium" translate="no">
                    {providerPresetModelLabel(model)}
                  </p>
                  {model.name ? (
                    <p
                      className="truncate font-mono text-micro text-muted-foreground"
                      translate="no"
                    >
                      {model.id}
                    </p>
                  ) : null}
                </div>
                {context ? (
                  <span className="shrink-0 text-micro text-muted-foreground">
                    {t(($) => $.tab_body.providers.model_context, {
                      tokens: context.toLocaleString(),
                    })}
                  </span>
                ) : null}
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  aria-label={t(($) => $.tab_body.providers.remove_model_aria, {
                    n: index + 1,
                  })}
                  onClick={() => removeSelected(model.id)}
                >
                  <Trash2 className="size-3.5" aria-hidden="true" />
                </Button>
              </li>
            );
          })}
        </ul>
      ) : null}

      {/* The fallback. A gateway with no `/models` still has to be
          configurable, and the save probes the typed id the same way. */}
      <div className="mt-2 space-y-2 rounded-md border border-dashed border-border p-2.5">
        <p className="text-caption text-muted-foreground">
          {t(($) => $.tab_body.providers.manual_model_hint)}
        </p>
        <div className="flex flex-wrap items-center gap-2">
          <Input
            className="min-w-0 flex-1 basis-48"
            value={manualId}
            onChange={(event) => setManualId(event.target.value)}
            placeholder={t(($) => $.tab_body.providers.field_model_id)}
            aria-label={t(($) => $.tab_body.providers.field_model_id)}
            autoComplete="off"
          />
          <Button
            type="button"
            size="sm"
            variant="outline"
            disabled={!manualId.trim()}
            onClick={addManual}
          >
            <Plus className="size-3.5" aria-hidden="true" />
            {t(($) => $.tab_body.providers.add_model_action)}
          </Button>
        </div>
      </div>

      {showErrors && selected.length === 0 ? (
        <FieldError>
          {t(($) => $.tab_body.providers.error_models_required)}
        </FieldError>
      ) : null}
    </Field>
  );
}

/**
 * One failed probe, rendered from its kind rather than the daemon's English
 * sentence. A kind this build does not know falls back to that sentence: an
 * empty box would hide the reason entirely.
 */
function ProviderPresetFailureNote({
  failure,
  baseUrl,
}: {
  failure: ProviderPresetFailure;
  baseUrl: string;
}) {
  const { t } = useT("agents");
  const dashboard = providerPresetNeedsKeyRegeneration(failure)
    ? providerConsoleUrl(baseUrl)
    : null;

  return (
    <div
      role="alert"
      data-testid="preset-verify-failure"
      className="mt-2 rounded-md border border-destructive/30 bg-destructive/5 p-2.5"
    >
      <div className="flex items-start gap-2">
        <AlertTriangle
          className="mt-0.5 size-4 shrink-0 text-destructive"
          aria-hidden="true"
        />
        <div className="min-w-0 space-y-1">
          <p className="text-caption font-medium">
            {t(($) => $.tab_body.providers.verify_failed_title)}
          </p>
          <p
            className="text-pretty text-caption leading-5 text-muted-foreground"
            data-testid="preset-verify-failure-message"
          >
            {providerPresetFailureMessage(failure, t)}
          </p>
          {/* The reset instant is only present on a rate-limited failure, and it
              is only useful next to the "regenerate the key" sentence — waiting
              for it is the dead end that copy exists to close. */}
          {failure.params.reset_at_local ? (
            <p className="text-caption text-muted-foreground">
              {t(($) => $.tab_body.providers.reset_at_local_hint, {
                time: failure.params.reset_at_local,
              })}
            </p>
          ) : null}
          {dashboard ? (
            <a
              href={dashboard}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 text-caption font-medium text-brand underline"
            >
              {t(($) => $.tab_body.providers.open_dashboard_action)}
              <ExternalLink className="size-3" aria-hidden="true" />
            </a>
          ) : null}
        </div>
      </div>
    </div>
  );
}

type AgentsTranslator = ReturnType<typeof useT<"agents">>["t"];

/** Localized copy for a failure kind, or the daemon's sentence as the fallback. */
function providerPresetFailureMessage(
  failure: ProviderPresetFailure,
  t: AgentsTranslator,
): string {
  if (!isKnownProviderPresetFailure(failure.kind)) return failure.message;
  const key = PROVIDER_PRESET_FAILURE_I18N_KEYS[failure.kind];
  return t(($) => $.tab_body.providers[key], { ...failure.params });
}

function DeletePresetDialog({
  preset,
  onCancel,
  onConfirm,
}: {
  preset: RuntimeProviderPreset;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const { t } = useT("agents");
  const active = preset.active === true;

  return (
    <AlertDialog open onOpenChange={(open) => (open ? undefined : onCancel())}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            {t(($) => $.tab_body.providers.delete_title, { name: preset.id })}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {/* Deleting the preset in effect leaves the CLI with no default
                model at all, which the user has to be told BEFORE the click,
                not discover afterwards from a failing run. */}
            {active
              ? t(($) => $.tab_body.providers.delete_active_description)
              : t(($) => $.tab_body.providers.delete_description)}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>
            {t(($) => $.tab_body.providers.cancel_action)}
          </AlertDialogCancel>
          <AlertDialogAction onClick={onConfirm}>
            {t(($) => $.tab_body.providers.delete_confirm_action)}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

function ProviderPresetsLoading() {
  const { t } = useT("agents");
  return (
    <div className="rounded-lg border border-border bg-background p-4">
      <div className="space-y-3">
        {[0, 1].map((index) => (
          <div key={index} className="flex items-center gap-3">
            <div className="min-w-0 flex-1 space-y-1.5">
              <Skeleton className="h-3 w-1/3" />
              <Skeleton className="h-2.5 w-1/2" />
            </div>
            <Skeleton className="h-7 w-16 shrink-0 rounded-md" />
          </div>
        ))}
      </div>
      <p className="mt-3 text-caption text-muted-foreground">
        {t(($) => $.tab_body.providers.loading_text)}
      </p>
    </div>
  );
}

function errorText(err: unknown, fallback: string): string {
  return err instanceof Error && err.message ? err.message : fallback;
}
