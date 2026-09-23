"use client";

import { useQuery } from "@tanstack/react-query";
import { DEFAULT_WATCHED_PROVIDERS } from "@multica/core/workspace/routing-policy-prompt";
import { routingHealthOptions } from "@multica/core/workspace/queries";
import type { ProviderQuotaSummary } from "@multica/core/workspace/routing-health";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useT } from "../../i18n";

const LABELS: Record<string, string> = {
  claude: "Claude",
  codex: "Codex",
  grok: "Grok",
};

/**
 * The three provider quotas under the chat composer. Numbers come from the
 * server snapshot. A missing or stale reading stays "unknown" — this strip
 * does not guess a percentage or decide that a seat is dead.
 */
export function ProviderQuotaStrip() {
  const workspace = useCurrentWorkspace();
  const { t } = useT("chat");
  const health = useQuery({
    ...routingHealthOptions(workspace?.id ?? ""),
    enabled: !!workspace?.id,
  });
  const reported = new Map(
    (health.data?.provider_quotas ?? []).map((quota) => [quota.provider, quota]),
  );
  const rows = DEFAULT_WATCHED_PROVIDERS.map((provider) => {
    return reported.get(provider) ?? {
      provider,
      status: "unknown",
      used_percent: null,
      remaining: null,
      reset_at: null,
      observed_at: null,
    };
  });

  return (
    <div
      className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 px-1 text-micro text-muted-foreground"
      aria-label={t(($) => $.provider_quota.label)}
    >
      {rows.map((quota) => (
        <ProviderQuotaItem key={quota.provider} quota={quota} />
      ))}
    </div>
  );
}

function ProviderQuotaItem({ quota }: { quota: ProviderQuotaSummary }) {
  const { t } = useT("chat");
  const name = LABELS[quota.provider] ?? quota.provider;
  let value = t(($) => $.provider_quota.unknown);
  if (quota.status === "quota_exhausted") {
    value = t(($) => $.provider_quota.exhausted);
  } else if (quota.status === "available" && quota.used_percent != null) {
    value = t(($) => $.provider_quota.used, { percent: Math.round(quota.used_percent) });
  }
  const when = quota.reset_at ? formatStamp(quota.reset_at) : "";
  const updated = quota.observed_at ? formatStamp(quota.observed_at) : "";
  return (
    <span className="inline-flex min-w-0 items-center gap-1">
      <span className="text-foreground">{name}</span>
      <span className="font-mono tabular-nums">{value}</span>
      {when ? <span>{t(($) => $.provider_quota.resets, { when })}</span> : null}
      {updated ? <span>{t(($) => $.provider_quota.updated, { when: updated })}</span> : null}
    </span>
  );
}

function formatStamp(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleString(undefined, {
    month: "numeric",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}
