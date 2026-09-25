"use client";

import { useMemo, useState } from "react";
import { BellRing, Loader2 } from "lucide-react";
import type { AgentAccessRequestStatus, InboxItem } from "@multica/core/types";
import {
  agentAccessPassExpiry,
  type AgentAccessPassPreset,
  useAgentAccessRequests,
  useApproveAgentAccessRequest,
  useDeclineAgentAccessRequest,
} from "@multica/core/agents";
import { ApiError, errorCode } from "@multica/core/api";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { toast } from "sonner";
import { ActorAvatar } from "../../common/actor-avatar";
import { useT } from "../../i18n";
import { formatAccessExpiry, localDateTimeInputValue } from "./agent-access-format";

export function isAgentAccessRequestNotice(type: InboxItem["type"]): boolean {
  return type === "agent_access_request";
}

const KNOWN_STATUSES: AgentAccessRequestStatus[] = ["pending", "approved", "declined", "expired"];

function asStatus(value: string | undefined): AgentAccessRequestStatus | null {
  return KNOWN_STATUSES.find((s) => s === value) ?? null;
}

/**
 * Owner-side card for a doorbell request (DENE-808). The inbox row only
 * carries the snapshot taken when the bell rang; the live status comes from
 * the request list so a decision made from another session (or an expiry on
 * the server clock) shows here without a reload.
 */
export function AgentAccessRequestNotice({ item }: { item: InboxItem }) {
  const { t } = useT("inbox");
  const details = item.details ?? {};
  const requestId = details.request_id ?? "";

  const requests = useAgentAccessRequests(!!requestId);
  const live = useMemo(
    () => requests.data?.incoming.find((r) => r.id === requestId) ?? null,
    [requests.data, requestId],
  );
  // Until the list arrives, fall back to the snapshot so the card never
  // renders with no verdict at all.
  const status: AgentAccessRequestStatus =
    live?.status ?? asStatus(details.status) ?? (requests.isFetched ? "expired" : "pending");
  const expiresAt = live?.expires_at ?? details.expires_at;
  const expired =
    status === "pending" && !!expiresAt && new Date(expiresAt).getTime() < Date.now();
  const effectiveStatus: AgentAccessRequestStatus = expired ? "expired" : status;

  const approve = useApproveAgentAccessRequest();
  const decline = useDeclineAgentAccessRequest();
  const busy = approve.isPending || decline.isPending;

  const [preset, setPreset] = useState<AgentAccessPassPreset | null>(null);
  const [custom, setCustom] = useState<string>(() => localDateTimeInputValue(2 * 60 * 60 * 1000));

  const requesterName = live?.requester_name ?? details.requester_name ?? "";
  const agentName = live?.agent_name ?? details.agent_name ?? "";
  const triggerKind = live?.trigger_kind ?? details.trigger_kind;

  const fail = (error: unknown) => {
    if (error instanceof ApiError && errorCode(error) === "request_not_pending") {
      toast.error(t(($) => $.errors.access_request_not_pending));
      return;
    }
    toast.error(t(($) => $.errors.access_request_failed));
  };

  const runApprove = async () => {
    const expiry = preset ? agentAccessPassExpiry(preset, new Date(custom)) : null;
    if (preset && !expiry) {
      toast.error(t(($) => $.errors.access_pass_expiry_invalid));
      return;
    }
    try {
      const res = await approve.mutateAsync({
        id: requestId,
        body: expiry
          ? {
              pass_expires_at: expiry.expires_at,
              pass_duration_minutes: expiry.duration_minutes,
            }
          : {},
      });
      if (res.replay && res.replay.status === "blocked") {
        toast.warning(t(($) => $.toasts.access_approved_but_blocked));
      } else if (res.pass) {
        toast.success(
          t(($) => $.toasts.access_approved_with_pass, {
            until: formatAccessExpiry(res.pass.expires_at),
          }),
        );
      } else {
        toast.success(t(($) => $.toasts.access_approved));
      }
    } catch (error) {
      fail(error);
    }
  };

  const runDecline = async () => {
    try {
      await decline.mutateAsync(requestId);
      toast.success(t(($) => $.toasts.access_declined));
    } catch (error) {
      fail(error);
    }
  };

  return (
    <div
      data-testid="agent-access-request-notice"
      className="mx-4 mt-3 shrink-0 rounded-lg border border-amber-500/40 bg-amber-500/5 p-4"
    >
      <div className="flex items-start gap-3">
        <div className="mt-0.5 shrink-0 text-amber-600 dark:text-amber-400">
          <BellRing className="size-4" />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            {details.requester_id ? (
              <ActorAvatar
                actorType="member"
                actorId={details.requester_id}
                name={requesterName}
                avatarUrl={live?.requester_avatar_url ?? null}
                size="xs"
              />
            ) : null}
            <span className="text-body font-medium">
              {triggerKind === "assign"
                ? t(($) => $.detail.access_request_assign_title, { requester: requesterName, agent: agentName })
                : t(($) => $.detail.access_request_mention_title, { requester: requesterName, agent: agentName })}
            </span>
          </div>
          {details.summary ? (
            <p className="mt-2 whitespace-pre-wrap text-body text-muted-foreground">{details.summary}</p>
          ) : null}
          <p className="mt-2 text-caption text-muted-foreground">
            {t(($) => $.detail.access_request_local_env_note)}
          </p>

          {effectiveStatus === "pending" ? (
            <>
              <div className="mt-3 flex flex-wrap items-center gap-2">
                <span className="text-caption text-muted-foreground">
                  {t(($) => $.detail.access_pass_prompt)}
                </span>
                {(["2h", "today", "custom"] as const).map((p) => (
                  <Button
                    key={p}
                    type="button"
                    size="xs"
                    variant={preset === p ? "secondary" : "outline"}
                    aria-pressed={preset === p}
                    disabled={busy}
                    onClick={() => setPreset((cur) => (cur === p ? null : p))}
                  >
                    {p === "2h"
                      ? t(($) => $.detail.access_pass_2h)
                      : p === "today"
                        ? t(($) => $.detail.access_pass_today)
                        : t(($) => $.detail.access_pass_custom)}
                  </Button>
                ))}
                {preset === "custom" ? (
                  <Input
                    type="datetime-local"
                    className="h-7 w-auto text-caption"
                    value={custom}
                    min={localDateTimeInputValue(60_000)}
                    disabled={busy}
                    aria-label={t(($) => $.detail.access_pass_custom)}
                    onChange={(e) => setCustom(e.target.value)}
                  />
                ) : null}
              </div>
              <div className="mt-3 flex flex-wrap gap-2">
                <Button size="sm" disabled={busy} data-testid="access-approve" onClick={() => void runApprove()}>
                  {approve.isPending ? <Loader2 className="size-3.5 animate-spin" /> : null}
                  {preset
                    ? t(($) => $.detail.access_approve_with_pass)
                    : t(($) => $.detail.access_approve_once)}
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={busy}
                  data-testid="access-decline"
                  onClick={() => void runDecline()}
                >
                  {t(($) => $.detail.access_decline)}
                </Button>
                {expiresAt ? (
                  <span className="self-center text-caption text-muted-foreground">
                    {t(($) => $.detail.access_request_expires, { until: formatAccessExpiry(expiresAt) })}
                  </span>
                ) : null}
              </div>
            </>
          ) : (
            <p className="mt-3 text-caption font-medium" data-testid="access-request-status">
              {effectiveStatus === "approved"
                ? t(($) => $.detail.access_status_approved)
                : effectiveStatus === "declined"
                  ? t(($) => $.detail.access_status_declined)
                  : t(($) => $.detail.access_status_expired)}
            </p>
          )}
        </div>
      </div>
    </div>
  );
}
