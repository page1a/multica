"use client";

import type { Issue } from "@multica/core/types";
import {
  closeProtocolNextOwnerId,
  closeProtocolWaitingOn,
  readCloseProtocol,
} from "@multica/core/issues";
import { useActorName } from "@multica/core/workspace/hooks";
import { useT, useTimeAgo } from "../../i18n";
import { ReadonlyContent } from "../../editor";
import { useCloseConclusionLabel } from "./close-conclusion-label";

/**
 * Sidebar close record (DENE-1002).
 *
 * Which conclusion closed this ticket, the status it wrote, who continues and
 * — from the evidence comment the record points at — why. Renders nothing
 * until the eight `close.*` keys are all present, so an old comment-only
 * wrap-up does not grow a half-record.
 */
export function IssueCloseRecordSection({
  issue,
  evidenceBody,
}: {
  issue: Issue;
  evidenceBody?: string | null;
}) {
  const { t } = useT("issues");
  const timeAgo = useTimeAgo();
  const { getActorName } = useActorName();
  const conclusionLabel = useCloseConclusionLabel();
  const close = readCloseProtocol(issue.metadata, issue.status);
  if (!close.complete || close.conclusion === null) return null;

  const nextOwnerId = closeProtocolNextOwnerId(close.nextOwnerType, close.nextOwnerId);
  const nextOwnerLabel =
    close.nextOwnerType === null
      ? null
      : close.nextOwnerType === "none" || nextOwnerId === null
        ? t(($) => $.close_protocol.next_owner_none)
        : t(($) => $.close_protocol.next_owner, {
            name: getActorName(close.nextOwnerType, nextOwnerId),
          });
  const waitingOn = closeProtocolWaitingOn(close.waitingOn);

  return (
    <section data-testid="issue-close-record" className="text-caption">
      <div className="mb-2 flex w-full items-center gap-1 rounded-md px-2 py-1 text-caption font-medium text-muted-foreground">
        {t(($) => $.close_protocol.section_title)}
      </div>
      <div className="flex flex-col gap-1.5 pl-2">
        <Field
          label={t(($) => $.close_protocol.conclusion)}
          value={conclusionLabel(close.conclusion) ?? close.conclusion}
        />
        <Field
          label={t(($) => $.close_protocol.status)}
          value={close.closeStatus ?? issue.status}
        />
        {nextOwnerLabel !== null && <Field label="" value={nextOwnerLabel} />}
        {waitingOn !== null && (
          <Field label="" value={t(($) => $.close_protocol.waiting_on, { id: waitingOn })} />
        )}
        {close.at !== null && <Field label="" value={timeAgo(close.at)} />}
        <div className="flex flex-col gap-1">
          <span className="text-muted-foreground">
            {t(($) => $.close_protocol.evidence)}
          </span>
          {evidenceBody && evidenceBody.trim() !== "" ? (
            // The recorded reason is the comment itself, rendered the same way
            // the timeline renders it, so the sidebar and the thread agree.
            <div className="max-h-40 overflow-auto rounded-md bg-muted/60 px-2 py-1">
              <ReadonlyContent content={evidenceBody} />
            </div>
          ) : (
            <p className="rounded-md bg-muted/60 px-2 py-1 text-foreground">
              {t(($) => $.close_protocol.evidence_missing)}
            </p>
          )}
        </div>
      </div>
    </section>
  );
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-start gap-1.5">
      {label !== "" && <span className="shrink-0 text-muted-foreground">{label}</span>}
      <span className="min-w-0 break-words text-foreground">{value}</span>
    </div>
  );
}
