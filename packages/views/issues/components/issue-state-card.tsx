"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Pencil, Plus, Trash2 } from "lucide-react";
import { issueContextOptions } from "@multica/core/issues/queries";
import { useIssueDecisionMutations } from "@multica/core/issues/mutations";
import { useWorkspacePaths } from "@multica/core/paths";
import type { StateCardChildReceipt, StateCardDecision } from "@multica/core/types";
import { useActorName } from "@multica/core/workspace/hooks";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { useT, useTimeAgo } from "../../i18n";
import { AppLink } from "../../navigation";
import { StatusIcon } from "./status-icon";

/**
 * Sidebar state card (DENE-1328): what was settled, the last baton, and the
 * threads new since the viewer was last here. The card is derived by the
 * server — the same one `multica issue context` prints for agents. Its goal
 * and "where it stands" parts are already on this page (title, goal section,
 * close record), so only the parts that are not render here — plus each
 * sub-task's receipt (DENE-1679).
 */
export function IssueStateCardSection({
  wsId,
  issueId,
  onJumpToThread,
}: {
  wsId: string;
  issueId: string;
  onJumpToThread?: (threadId: string) => void;
}) {
  const { t } = useT("issues");
  const timeAgo = useTimeAgo();
  const { getActorName } = useActorName();
  const { data: card } = useQuery(issueContextOptions(wsId, issueId));
  if (!card) return null;

  const baton = card.baton;
  const batonBy =
    baton?.by_type && baton.by_id ? getActorName(baton.by_type, baton.by_id) : null;
  const changes = card.changes;

  return (
    <section data-testid="issue-state-card" className="text-caption">
      <div className="mb-2 flex w-full items-center gap-1 rounded-md px-2 py-1 text-caption font-medium text-muted-foreground">
        {t(($) => $.state_card.section_title)}
      </div>
      <div className="flex flex-col gap-3 pl-2">
        {card.source?.excerpt ? (
          <Row label={t(($) => $.state_card.source_quote)}>
            <span className="break-words text-foreground" data-testid="issue-state-card-source">
              {card.source.excerpt}
            </span>
          </Row>
        ) : null}

        <DecisionList wsId={wsId} issueId={issueId} decisions={card.decisions} />

        <Row label={t(($) => $.state_card.baton)}>
          {baton && baton.summary ? (
            <div className="flex flex-col gap-0.5">
              <span className="break-words text-foreground">{baton.summary}</span>
              <span className="text-muted-foreground">
                {[
                  baton.kind === "handoff"
                    ? t(($) => $.state_card.baton_handoff, { to: baton.to ?? "" })
                    : t(($) => $.state_card.baton_close),
                  batonBy,
                  baton.at ? timeAgo(baton.at) : null,
                ]
                  .filter(Boolean)
                  .join(" · ")}
              </span>
            </div>
          ) : (
            <span className="text-muted-foreground">{t(($) => $.state_card.baton_none)}</span>
          )}
        </Row>

        {card.children && card.children.length > 0 ? <ChildReceiptList receipts={card.children} /> : null}

        <Row
          label={
            changes.anchor === "none"
              ? t(($) => $.state_card.changes_first)
              : t(($) => $.state_card.changes)
          }
        >
          {changes.threads.length === 0 ? (
            <span className="text-muted-foreground">{t(($) => $.state_card.changes_none)}</span>
          ) : (
            <ul className="flex flex-col gap-0.5" data-testid="issue-state-card-threads">
              {changes.threads.map((thread) => (
                <li key={thread.thread_id}>
                  <button
                    type="button"
                    onClick={() => onJumpToThread?.(thread.thread_id)}
                    className="flex min-h-7 w-full min-w-0 items-baseline gap-1.5 rounded-sm text-left hover:text-foreground"
                  >
                    <span className="min-w-0 flex-1 truncate text-foreground">{thread.title}</span>
                    <span className="shrink-0 text-muted-foreground">
                      {t(($) => $.state_card.changes_count, { count: thread.new_count })}
                    </span>
                  </button>
                </li>
              ))}
              {changes.more ? (
                <li className="text-muted-foreground">
                  {t(($) => $.state_card.changes_more, { count: changes.more })}
                </li>
              ) : null}
            </ul>
          )}
        </Row>
      </div>
    </section>
  );
}

function ChildReceiptList({ receipts }: { receipts: StateCardChildReceipt[] }) {
  const { t } = useT("issues");
  const paths = useWorkspacePaths();
  return (
    <Row label={t(($) => $.state_card.children)}>
      <ul className="flex flex-col gap-1.5" data-testid="issue-state-card-children">
        {receipts.map((r) => (
          <li key={r.issue_id} className="flex min-w-0 items-start gap-1.5">
            <StatusIcon status={r.status} className="mt-0.5 size-3 shrink-0" />
            <div className="flex min-w-0 flex-1 flex-col gap-0.5">
              <AppLink href={paths.issueDetail(r.issue_id)} className="truncate text-foreground hover:underline">
                {r.identifier} {r.title}
              </AppLink>
              {r.summary ? <span className="line-clamp-2 break-words text-foreground">{r.summary}</span> : null}
              {r.pull_requests.length > 0 || r.knowledge ? (
                <span className="break-words text-muted-foreground">
                  {r.pull_requests.map((pr, i) => (
                    <span key={pr.url}>
                      {i > 0 ? "，" : null}
                      <a href={pr.url} target="_blank" rel="noreferrer" className="hover:text-foreground hover:underline">
                        {pr.number > 0 ? `#${pr.number}` : "PR"}
                      </a>
                      {pr.state === "merged" ? ` ${t(($) => $.state_card.children_pr_merged)}` : null}
                    </span>
                  ))}
                  {r.pull_requests.length > 0 && r.knowledge ? " · " : null}
                  {r.knowledge ? <span className="line-clamp-1">{r.knowledge}</span> : null}
                </span>
              ) : null}
            </div>
          </li>
        ))}
      </ul>
    </Row>
  );
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-1">
      <span className="text-muted-foreground">{label}</span>
      {children}
    </div>
  );
}

function errorReason(err: unknown): string {
  return err instanceof Error && err.message ? err.message : "";
}

function DecisionList({
  wsId,
  issueId,
  decisions,
}: {
  wsId: string;
  issueId: string;
  decisions: StateCardDecision[];
}) {
  const { t } = useT("issues");
  const { add, edit, remove } = useIssueDecisionMutations(issueId, wsId);
  // One editor at a time: "new" for the add row, a decision id for an edit.
  const [editing, setEditing] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const [error, setError] = useState<string | null>(null);

  const open = (id: string, text: string) => {
    setEditing(id);
    setDraft(text);
    setError(null);
  };
  const close = () => {
    setEditing(null);
    setDraft("");
    setError(null);
  };
  const save = async () => {
    const text = draft.trim();
    if (text === "" || editing === null) return;
    try {
      if (editing === "new") await add.mutateAsync(text);
      else await edit.mutateAsync({ id: editing, text });
      close();
    } catch (err) {
      setError(t(($) => $.state_card.decision_failed, { reason: errorReason(err) }));
    }
  };
  const del = async (id: string) => {
    try {
      await remove.mutateAsync(id);
      setError(null);
    } catch (err) {
      setError(t(($) => $.state_card.decision_failed, { reason: errorReason(err) }));
    }
  };
  const pending = add.isPending || edit.isPending;

  const editor = (
    <form
      className="flex flex-col gap-1.5"
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      <Input
        autoFocus
        value={draft}
        maxLength={300}
        placeholder={t(($) => $.state_card.decision_placeholder)}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Escape") close();
        }}
        className="h-8 text-caption"
        aria-label={t(($) => $.state_card.decision_placeholder)}
      />
      <div className="flex gap-1.5">
        <Button type="submit" size="sm" disabled={pending || draft.trim() === ""}>
          {t(($) => $.state_card.decision_save)}
        </Button>
        <Button type="button" size="sm" variant="ghost" onClick={close}>
          {t(($) => $.state_card.decision_cancel)}
        </Button>
      </div>
    </form>
  );

  return (
    <Row label={t(($) => $.state_card.decisions)}>
      <ul className="flex flex-col gap-0.5" data-testid="issue-state-card-decisions">
        {decisions.length === 0 && editing !== "new" && (
          <li className="text-muted-foreground">{t(($) => $.state_card.decisions_empty)}</li>
        )}
        {decisions.map((d) =>
          editing === d.id ? (
            <li key={d.id}>{editor}</li>
          ) : (
            <li key={d.id} className="group flex items-start gap-1">
              <span className="min-w-0 flex-1 break-words py-1 text-foreground">{d.text}</span>
              {/* Always visible below md (no hover on touch), revealed on hover above. */}
              <span className="flex shrink-0 md:opacity-0 md:group-hover:opacity-100 md:group-focus-within:opacity-100">
                <Button
                  type="button"
                  size="icon-sm"
                  variant="ghost"
                  aria-label={t(($) => $.state_card.decision_edit)}
                  onClick={() => open(d.id, d.text)}
                >
                  <Pencil className="size-3.5" />
                </Button>
                <Button
                  type="button"
                  size="icon-sm"
                  variant="ghost"
                  aria-label={t(($) => $.state_card.decision_delete)}
                  disabled={remove.isPending}
                  onClick={() => void del(d.id)}
                >
                  <Trash2 className="size-3.5" />
                </Button>
              </span>
            </li>
          ),
        )}
        {editing === "new" && <li>{editor}</li>}
      </ul>
      {error && <p className="text-destructive">{error}</p>}
      {editing === null && (
        <button
          type="button"
          onClick={() => open("new", "")}
          className="flex min-h-7 items-center gap-1 self-start text-muted-foreground hover:text-foreground"
        >
          <Plus className="size-3.5" />
          {t(($) => $.state_card.decision_add)}
        </button>
      )}
    </Row>
  );
}
