"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { clientErrorMessage } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCloseIssue } from "@multica/core/issues/mutations";
import type { CloseOutcome, KnowledgeAudit } from "@multica/core/types";
import { projectMemoryLocationsOptions } from "@multica/core/projects/queries";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../i18n";

const OUTCOMES: readonly CloseOutcome[] = [
  "done",
  "in_review",
  "blocked",
  "cancelled",
  "backlog",
  "todo",
  "in_progress",
];

const fieldClass =
  "w-full rounded-md border border-border bg-background px-2 py-1.5 text-body";

// The one "waiting on / continues with" field takes either an issue reference
// or a person. The server keeps those apart: an issue goes to `blocked_by`, a
// person to `needs_human` — and `needs_human` is also what summons them. A
// member id sent as `blocked_by` closed the ticket with a non-issue UUID in
// `close.waiting_on` and nobody woken (DENE-1002 review), so route on the
// token's shape before submitting.
const MEMBER_ID_RE =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

// waitTarget splits the single field value into the two request fields the
// server actually reads. Empty means "nothing written", which the server
// treats as no continuation.
function waitTarget(value: string): { blocked_by?: string; needs_human?: string } {
  const token = value.trim();
  if (token === "") return {};
  return MEMBER_ID_RE.test(token) ? { needs_human: token } : { blocked_by: token };
}

export function CloseIssueDialog({
  onClose,
  data,
}: {
  onClose: () => void;
  data: Record<string, unknown> | null;
}) {
  const { t } = useT("modals");
  const issueId = typeof data?.issueId === "string" ? data.issueId : "";
  const identifier = typeof data?.identifier === "string" ? data.identifier : "";
  const wsId = useWorkspaceId();
  const closeIssue = useCloseIssue();
  const locations = useQuery(projectMemoryLocationsOptions(wsId));
  const [outcome, setOutcome] = useState<CloseOutcome>("done");
  const [evidence, setEvidence] = useState("");
  const [summary, setSummary] = useState("");
  const [noCode, setNoCode] = useState("");
  const [waitingOn, setWaitingOn] = useState("");
  const [continuesWith, setContinuesWith] = useState("");
  const [wakeAt, setWakeAt] = useState("");
  const [mode, setMode] = useState<"none" | "changes" | null>(null);
  const [drafts, setDrafts] = useState<Record<string, { on: boolean; summary: string }>>({});
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const outcomeLabel = (key: CloseOutcome) => {
    switch (key) {
      case "done":
        return t(($) => $.close_issue.outcome_done);
      case "in_review":
        return t(($) => $.close_issue.outcome_in_review);
      case "blocked":
        return t(($) => $.close_issue.outcome_blocked);
      case "cancelled":
        return t(($) => $.close_issue.outcome_cancelled);
      case "backlog":
        return t(($) => $.close_issue.outcome_backlog);
      case "todo":
        return t(($) => $.close_issue.outcome_todo);
      case "in_progress":
        return t(($) => $.close_issue.outcome_in_progress);
    }
  };

  // What the picked conclusion needs, so the picker itself teaches the rule
  // (DENE-1002). The server still enforces it; this only saves a round trip.
  const outcomeRequirement = (key: CloseOutcome) => {
    switch (key) {
      case "done":
        return t(($) => $.close_issue.requires_done);
      case "in_review":
        return t(($) => $.close_issue.requires_in_review);
      case "blocked":
        return t(($) => $.close_issue.requires_blocked);
      case "cancelled":
        return t(($) => $.close_issue.requires_cancelled);
      case "backlog":
        return t(($) => $.close_issue.requires_backlog);
      case "todo":
        return t(($) => $.close_issue.requires_todo);
      case "in_progress":
        return t(($) => $.close_issue.requires_in_progress);
    }
  };

  // Say out loud how the token will be read, so the field is not a black box.
  const waitTargetHint = (value: string) => {
    const token = value.trim();
    if (token === "") return t(($) => $.close_issue.wait_target_hint);
    return MEMBER_ID_RE.test(token)
      ? t(($) => $.close_issue.wait_target_person)
      : t(($) => $.close_issue.wait_target_issue);
  };

  const audit = (): KnowledgeAudit | undefined => {
    if (mode === "none") return { none: true };
    if (mode !== "changes") return undefined;
    const changes = (locations.data?.locations ?? [])
      .filter((location) => drafts[location.key]?.on)
      .map((location) => ({
        location: location.key,
        summary: drafts[location.key]?.summary ?? "",
      }));
    return { changes };
  };

  const submit = async () => {
    if (!issueId || submitting) return;
    setSubmitting(true);
    setError(null);
    // The who-continues field is shared between blocked and in_progress, and
    // either may name a ticket or a person; waitTarget routes it to the field
    // the server reads.
    const target =
      outcome === "blocked"
        ? waitTarget(waitingOn)
        : outcome === "in_progress"
          ? waitTarget(continuesWith)
          : {};
    try {
      await closeIssue.mutateAsync({
        id: issueId,
        outcome,
        evidence: evidence.trim(),
        summary: summary.trim() || undefined,
        no_code_reason:
          outcome === "done" || outcome === "in_review" ? noCode.trim() || undefined : undefined,
        blocked_by: target.blocked_by,
        needs_human: target.needs_human,
        wake_at: outcome === "in_progress" ? wakeAt.trim() || undefined : undefined,
        knowledge_audit: audit(),
      });
      onClose();
    } catch (err) {
      setError(clientErrorMessage(err) ?? t(($) => $.close_issue.failed));
      setSubmitting(false);
    }
  };

  return (
    <Dialog open onOpenChange={(open) => { if (!open && !submitting) onClose(); }}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t(($) => $.close_issue.title, { identifier })}</DialogTitle>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
        >
          <label className="flex flex-col gap-1 text-body">
            {t(($) => $.close_issue.outcome)}
            <select
              className={fieldClass}
              value={outcome}
              onChange={(event) => setOutcome(event.target.value as CloseOutcome)}
            >
              {OUTCOMES.map((key) => (
                <option key={key} value={key}>
                  {outcomeLabel(key)}
                </option>
              ))}
            </select>
            <span className="text-caption text-muted-foreground">
              {outcomeRequirement(outcome)}
            </span>
          </label>
          <label className="flex flex-col gap-1 text-body">
            {t(($) => $.close_issue.evidence)}
            <Textarea
              value={evidence}
              onChange={(event) => setEvidence(event.target.value)}
              placeholder={t(($) => $.close_issue.evidence_placeholder)}
              required
            />
          </label>
          <label className="flex flex-col gap-1 text-body">
            {t(($) => $.close_issue.summary)}
            <Textarea
              value={summary}
              onChange={(event) => setSummary(event.target.value)}
              placeholder={t(($) => $.close_issue.summary_placeholder)}
            />
          </label>
          {(outcome === "done" || outcome === "in_review") && (
            <label className="flex flex-col gap-1 text-body">
              {t(($) => $.close_issue.no_code)}
              <Textarea value={noCode} onChange={(event) => setNoCode(event.target.value)} />
            </label>
          )}
          {outcome === "blocked" && (
            <label className="flex flex-col gap-1 text-body">
              {t(($) => $.close_issue.blocked_by)}
              <input
                className={fieldClass}
                value={waitingOn}
                placeholder={t(($) => $.close_issue.blocked_by_placeholder)}
                onChange={(event) => setWaitingOn(event.target.value)}
              />
              <span className="text-caption text-muted-foreground">
                {waitTargetHint(waitingOn)}
              </span>
            </label>
          )}
          {outcome === "in_progress" && (
            <>
              <label className="flex flex-col gap-1 text-body">
                {t(($) => $.close_issue.wake_at)}
                <input
                  className={fieldClass}
                  value={wakeAt}
                  placeholder={t(($) => $.close_issue.wake_at_placeholder)}
                  onChange={(event) => setWakeAt(event.target.value)}
                />
              </label>
              <label className="flex flex-col gap-1 text-body">
                {t(($) => $.close_issue.continues_with)}
                <input
                  className={fieldClass}
                  value={continuesWith}
                  placeholder={t(($) => $.close_issue.blocked_by_placeholder)}
                  onChange={(event) => setContinuesWith(event.target.value)}
                />
                <span className="text-caption text-muted-foreground">
                  {waitTargetHint(continuesWith)}
                </span>
              </label>
            </>
          )}
          <fieldset className="flex flex-col gap-2">
            <legend className="text-body">{t(($) => $.close_issue.knowledge)}</legend>
            <label className="flex items-center gap-2 text-body">
              <input
                type="radio"
                name="knowledge-mode"
                checked={mode === "none"}
                onChange={() => setMode("none")}
              />
              {t(($) => $.close_issue.knowledge_none)}
            </label>
            <label className="flex items-center gap-2 text-body">
              <input
                type="radio"
                name="knowledge-mode"
                checked={mode === "changes"}
                onChange={() => setMode("changes")}
              />
              {t(($) => $.close_issue.knowledge_changes)}
            </label>
            {mode === "changes" && locations.isPending && (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.close_issue.locations_loading)}
              </p>
            )}
            {mode === "changes" && locations.isError && (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.close_issue.locations_unavailable)}
              </p>
            )}
            {mode === "changes" &&
              (locations.data?.locations ?? []).map((location) => {
                const draft = drafts[location.key] ?? { on: false, summary: "" };
                return (
                  <div key={location.key} className="flex flex-col gap-1 pl-1">
                    <label className="flex items-center gap-2 text-body">
                      <input
                        type="checkbox"
                        checked={draft.on}
                        onChange={(event) =>
                          setDrafts((prev) => ({
                            ...prev,
                            [location.key]: { on: event.target.checked, summary: draft.summary },
                          }))
                        }
                      />
                      <span>{location.path}</span>
                      <span className="text-caption text-muted-foreground">{location.key}</span>
                    </label>
                    {draft.on && (
                      <Textarea
                        aria-label={location.key}
                        value={draft.summary}
                        placeholder={t(($) => $.close_issue.knowledge_summary_placeholder)}
                        onChange={(event) =>
                          setDrafts((prev) => ({
                            ...prev,
                            [location.key]: { on: true, summary: event.target.value },
                          }))
                        }
                      />
                    )}
                  </div>
                );
              })}
          </fieldset>
          {error && (
            <p role="alert" className="text-body text-destructive">
              {error}
            </p>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" disabled={submitting} onClick={onClose}>
              {t(($) => $.close_issue.cancel)}
            </Button>
            <Button type="submit" disabled={submitting || evidence.trim() === ""}>
              {submitting ? t(($) => $.close_issue.submitting) : t(($) => $.close_issue.submit)}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
