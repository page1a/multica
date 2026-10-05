"use client";

import { useState } from "react";
import type { Issue, IssueDisposeAction, IssueDriver } from "@multica/core/types";
import { useDisposeIssue } from "@multica/core/issues/mutations";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { cn } from "@multica/ui/lib/utils";
import { toast } from "sonner";
import { useT } from "../../i18n";

// Driver of an open issue (ADR-0006, DENE-1342). The server computes it on
// detail and children responses; `none` means nothing will move the issue on
// its own, and the row below is where a person disposes of it through the same
// `POST /dispose` the CLI uses.

export function isUndriven(driver: IssueDriver | undefined): driver is IssueDriver {
  return driver?.kind === "none";
}

/** Small mark on a sub-issue row; the reason rides in the tooltip. */
export function UndrivenRowMark({ driver }: { driver: IssueDriver | undefined }) {
  const { t } = useT("issues");
  if (!isUndriven(driver)) return null;
  return (
    <span
      data-testid="sub-issue-undriven"
      title={driver.reason}
      className="shrink-0 text-micro font-medium text-destructive"
    >
      {t(($) => $.driver.none)}
    </span>
  );
}

type Pending = Extract<IssueDisposeAction, "split" | "cancel">;

/**
 * One line under the title: "没人在推进", why, what the patrol already tried,
 * and the four dispositions. Split and cancel need one more word from the
 * person, so they open an input in place instead of a dialog.
 */
export function IssueUndrivenRow({ issue, readOnly = false }: { issue: Issue; readOnly?: boolean }) {
  const { t } = useT("issues");
  const dispose = useDisposeIssue(issue.id, issue.workspace_id);
  const [pending, setPending] = useState<Pending | null>(null);
  const [text, setText] = useState("");
  const driver = issue.driver;
  if (!isUndriven(driver)) return null;

  const tried = [
    driver.revives ? t(($) => $.driver.revived, { count: driver.revives }) : null,
    driver.escalated ? t(($) => $.driver.escalated) : null,
  ].filter(Boolean);

  const run = (action: IssueDisposeAction, extra?: { reason?: string; into?: string[] }) => {
    dispose.mutate(
      { action, ...extra },
      {
        onSuccess: () => {
          setPending(null);
          setText("");
          toast.success(t(($) => $.driver.done));
        },
        onError: (error) =>
          toast.error(error instanceof Error ? error.message : t(($) => $.driver.failed)),
      },
    );
  };

  const submit = () => {
    const value = text.trim();
    if (!value || !pending) return;
    if (pending === "split") run("split", { into: [value] });
    else run("cancel", { reason: value });
  };

  return (
    <div data-testid="issue-undriven" className="mt-2 flex flex-wrap items-center gap-x-2 gap-y-1 text-caption">
      <span className="flex min-w-0 items-center gap-1.5">
        <span className="size-1.5 shrink-0 rounded-full bg-destructive" aria-hidden />
        <span className="shrink-0 font-medium text-destructive">{t(($) => $.driver.none)}</span>
        <span className="min-w-0 text-muted-foreground">
          {[driver.reason, ...tried].filter(Boolean).join(" · ")}
        </span>
      </span>
      {!readOnly && pending === null && (
        <span className="ml-auto flex flex-wrap items-center gap-0.5">
          <DisposeButton disabled={dispose.isPending} onClick={() => run("rerun")}>
            {t(($) => $.driver.rerun)}
          </DisposeButton>
          <DisposeButton disabled={dispose.isPending} onClick={() => run("reroute")}>
            {t(($) => $.driver.reroute)}
          </DisposeButton>
          <DisposeButton disabled={dispose.isPending} onClick={() => setPending("split")}>
            {t(($) => $.driver.split)}
          </DisposeButton>
          <DisposeButton disabled={dispose.isPending} onClick={() => setPending("cancel")} destructive>
            {t(($) => $.driver.cancel)}
          </DisposeButton>
        </span>
      )}
      {!readOnly && pending !== null && (
        <form
          className="flex w-full items-center gap-1.5"
          onSubmit={(event) => {
            event.preventDefault();
            submit();
          }}
        >
          <Input
            autoFocus
            value={text}
            onChange={(event) => setText(event.target.value)}
            placeholder={pending === "split" ? t(($) => $.driver.split_placeholder) : t(($) => $.driver.cancel_placeholder)}
            className="h-8 min-w-0 flex-1"
          />
          <Button type="submit" size="sm" disabled={dispose.isPending || !text.trim()}>
            {pending === "split" ? t(($) => $.driver.split) : t(($) => $.driver.cancel)}
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            onClick={() => {
              setPending(null);
              setText("");
            }}
          >
            {t(($) => $.driver.back)}
          </Button>
        </form>
      )}
    </div>
  );
}

function DisposeButton({
  children,
  onClick,
  disabled,
  destructive = false,
}: {
  children: React.ReactNode;
  onClick: () => void;
  disabled: boolean;
  destructive?: boolean;
}) {
  return (
    <button
      type="button"
      disabled={disabled}
      onClick={onClick}
      className={cn(
        "inline-flex min-h-8 items-center rounded-md px-2 text-caption transition-colors hover:bg-accent disabled:cursor-not-allowed disabled:opacity-50",
        destructive ? "text-destructive hover:bg-destructive/10" : "text-muted-foreground hover:text-foreground",
      )}
    >
      {children}
    </button>
  );
}
