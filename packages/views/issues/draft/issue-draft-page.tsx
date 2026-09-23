"use client";

import { useCallback, useEffect, useMemo, useRef } from "react";
import { useQuery } from "@tanstack/react-query";
import { useDefaultLayout, type Layout } from "react-resizable-panels";
import { ArrowLeft, MoreHorizontal } from "lucide-react";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  decodeIssueDraftInput,
  issueDraftLandingIssueId,
  issueDraftParentIssueId,
  stripIssueDraftDirectives,
} from "@multica/core/issue-drafts";
import { useWorkspacePaths } from "@multica/core/paths";
import { runtimeListOptions } from "@multica/core/runtimes";
import { memberListOptions } from "@multica/core/workspace/queries";
import {
  issueDraftAssigneeSuggestionsOptions,
  issueDraftSuggestionRequest,
} from "@multica/core/issue-drafts";
import type { ChatMessage } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import {
  ResizableHandle,
  ResizablePanel,
  ResizablePanelGroup,
} from "@multica/ui/components/ui/resizable";
import { useBackOrReplace, useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { IssueDraftConversation } from "./issue-draft-conversation";
import { IssueDraftPolicyPicker } from "./issue-draft-policy-picker";
import { IssueDraftPreviewPanel } from "./issue-draft-preview-panel";
import { IssueDraftStageStrip } from "./issue-draft-stage-strip";
import { useIssueDraftSession } from "./use-issue-draft-session";

/**
 * The split this page opens in before anyone has dragged the divider: the
 * conversation takes the width, and the preview rides along as a sidecar.
 *
 * `useDefaultLayout` restores persisted layouts but offers no initial value of
 * its own, and the group can arrive at its first layout two ways — from this
 * `defaultLayout` prop, or from the panels' own `defaultSize` when the group is
 * measured only after mount. Both are declared here, in the same numbers, so the
 * page opens the same way either way. A layout the user has already dragged is
 * restored by the hook and takes precedence over both.
 */
const FIRST_RUN_LAYOUT: Layout = { conversation: 70, preview: 30 };

/**
 * One alignment conversation, addressed by its own draft id.
 *
 * The id is in the path rather than in component state because this
 * conversation outlives the screen that started it: a refresh, a back/forward,
 * a reopened desktop tab and a direct link all have to land back in the same
 * alignment. Confirming is the only thing that creates an issue.
 *
 * A FINISHED alignment is the same page in a read-only shape (DENE-371): the
 * transcript, what was agreed, and the issue it produced. Reusing this page
 * rather than the chat window is what keeps the wire format decoded — both
 * sides of the conversation are envelopes the chat window would render raw —
 * and what puts the agreed draft beside the conversation it came out of.
 */
export function IssueDraftPage({ draftId }: { draftId: string }) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const backOrReplace = useBackOrReplace();
  const currentUserId = useAuthStore((state) => state.user?.id ?? null);

  const session = useIssueDraftSession(draftId);
  const { defaultLayout, onLayoutChanged } = useDefaultLayout({
    id: "multica_issue_draft_layout",
  });
  // A record is final: there is no next turn to restyle, and the whole action
  // strip (confirm, abandon, save) is gone with it.
  const record = session.isRecord;
  // The header keeps one secondary entry instead of a control strip: the
  // alignment style is a setting someone reaches for deliberately, not a
  // decision to make before saying anything.
  const styleDisabled =
    record || session.pending || session.stage === "creating" || session.stage === "created";

  const runtimesQuery = useQuery(runtimeListOptions(wsId));
  const membersQuery = useQuery(memberListOptions(wsId));
  // Asked only once the draft has converged: every answer is a routing-model
  // call per row, and a draft still being aligned changes under it. Built from
  // the server-held draft, so typing in the panel never re-asks.
  const suggestionRequest = useMemo(
    () => (session.stage === "ready" && !record ? issueDraftSuggestionRequest(session.draft) : null),
    [record, session.draft, session.stage],
  );
  const assigneeSuggestionsQuery = useQuery(
    issueDraftAssigneeSuggestionsOptions(wsId, draftId, suggestionRequest),
  );

  /**
   * The transcript as a person should read it.
   *
   * Both directions of the carrier's wire format are undone here: the user's
   * own turns are JSON envelopes carrying the draft they were asking about, and
   * the carrier's replies end in an `<issue_draft>` block. Rendered raw, neither
   * is a conversation.
   */
  const displayMessages = useMemo<ChatMessage[]>(
    () =>
      session.messages.map((message) => ({
        ...message,
        content:
          message.role === "user"
            ? decodeIssueDraftInput(message.content)
            : stripIssueDraftDirectives(message.content),
      })),
    [session.messages],
  );

  const leave = useCallback(
    () => backOrReplace(paths.issues()),
    [backOrReplace, paths],
  );

  // The conversation is gone — discarded elsewhere, or a link that outlived it.
  // Replace rather than push: the address no longer resolves, so it must not
  // stay on the stack for a back to land on.
  useEffect(() => {
    if (session.missing) navigation.replace(paths.issues());
  }, [navigation, paths, session.missing]);

  // A confirmed draft is final. Replace, for the same reason: the alignment URL
  // is no longer an unfinished draft once the issue exists.
  //
  // Where it lands is `issueDraftLandingIssueId`: an alignment started from an
  // existing issue goes back to that issue — the group it produced is that
  // issue's children, and the person was working in its context (DENE-452) —
  // while a standalone alignment lands on the root it just created, which is
  // what this always did.
  //
  // Once per issue, not once per render. `paths` is rebuilt on every render and
  // a same-tick double click reports the same issue twice, so this effect runs
  // again and again while the page is still on screen — and a router told to
  // replace the same URL forty times never commits the navigation at all.
  //
  // A confirm that could not apply a seat stays put instead. The panel is the
  // only surface that shows it (the created issues themselves are ordinary
  // unassigned issues by the time this page is gone), so replacing the URL the
  // instant the rows are created would turn the notice into a frame nobody read
  // — it would satisfy the letter of "the issue was still created" while losing
  // the half of that requirement that says the person is told. The panel lists
  // the created rows and links to each, so the landing the replace would have
  // performed is one click away (DENE-694).
  const landingIssueId = issueDraftLandingIssueId(
    issueDraftParentIssueId(session.draft),
    session.createdIssueId,
  );
  const droppedSeats = session.assignmentWarnings.length > 0;
  const navigatedToIssueRef = useRef<string | null>(null);
  useEffect(() => {
    if (!landingIssueId) return;
    if (droppedSeats) return;
    if (navigatedToIssueRef.current === landingIssueId) return;
    navigatedToIssueRef.current = landingIssueId;
    navigation.replace(paths.issueDetail(landingIssueId));
  }, [droppedSeats, landingIssueId, navigation, paths]);

  if (session.missing) return null;

  return (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <header className="shrink-0">
        <div className="flex items-center gap-3 px-5 pt-4">
          <Button variant="ghost" size="sm" onClick={leave}>
            <ArrowLeft className="size-4" aria-hidden="true" />
            {t(($) => $.alignment.back)}
          </Button>
          <div className="min-w-0 flex-1">
            <h1 className="truncate text-title-sm font-semibold tracking-tight">
              {session.draft?.title.trim() || t(($) => $.alignment.title)}
            </h1>
            <p className="truncate text-caption text-muted-foreground">
              {record
                ? t(($) => $.alignment.record_subtitle)
                : session.continuation
                  ? t(($) => $.alignment.round_subtitle, { n: session.round })
                  : t(($) => $.alignment.subtitle)}
            </p>
          </div>
          {/* No policy on the row means the backend predates policies, so
              there is nothing to offer and no menu to open. A record is
              excluded for the other reason: there is no next reply for a
              policy to steer. */}
          {session.policy.key && !record ? (
            <DropdownMenu>
              <DropdownMenuTrigger
                render={
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    className="text-muted-foreground"
                    aria-label={t(($) => $.alignment.more_actions)}
                  />
                }
              >
                <MoreHorizontal className="size-4" aria-hidden="true" />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-72">
                <IssueDraftPolicyPicker
                  policy={session.policy}
                  switching={session.switchingPolicy}
                  // A conversation that is mid-reply or already confirmed has no
                  // next turn to change, so the picker is not offered one.
                  disabled={styleDisabled}
                  onChange={(policy) => void session.setPolicy(policy)}
                />
              </DropdownMenuContent>
            </DropdownMenu>
          ) : null}
        </div>
        {/* The strip reads off the live stage, and a record has none — its
            draft is `completed`/`abandoned`, which the stage helper folds into
            "aligning" because neither can be acted on. Rendering that would
            label a finished alignment as still in progress, so the outcome is
            stated by the panel instead. */}
        {!record ? (
          <IssueDraftStageStrip
            stage={session.stage}
            hasTitle={!!session.draft?.title.trim()}
          />
        ) : null}
      </header>

      {session.retired ? (
        <RetiredPanel onLeave={leave} />
      ) : session.loadFailed ? (
        <FailedPanel
          message={t(($) => $.alignment.load_failed)}
          onRetry={session.retry}
        />
      ) : (
        <ResizablePanelGroup
          orientation="horizontal"
          className="min-h-0 flex-1"
          defaultLayout={defaultLayout ?? FIRST_RUN_LAYOUT}
          onLayoutChanged={onLayoutChanged}
        >
          <ResizablePanel id="conversation" defaultSize="70%" minSize="30%">
            <IssueDraftConversation
              draftId={draftId}
              messages={displayMessages}
              loading={session.messagesLoading}
              pendingTask={session.pendingTask}
              runtimeOnline={session.runtimeOnline}
              sending={session.sending}
              onSend={session.send}
              onStop={() => void session.stop()}
              error={session.error}
              question={session.question}
              // A record has nothing left to say: no composer, no answer chips
              // for a question that was already settled, and no runtime badge
              // for a machine that will never run another turn.
              readOnly={record}
              // A settled reply is rendered from the carrier's task transcript,
              // not from `content`, so stripping the message above is not enough
              // on its own: the transcript's text rows have to be transformed
              // too, or the raw block comes back (DENE-319).
              transformContent={stripIssueDraftDirectives}
            />
          </ResizablePanel>
          <ResizableHandle />
          <ResizablePanel
            id="preview"
            defaultSize="30%"
            minSize={340}
            groupResizeBehavior="preserve-pixel-size"
          >
            <IssueDraftPreviewPanel
              draft={session.draft}
              stage={session.stage}
              canConfirm={session.canConfirm}
              saving={session.saving}
              saved={session.saved}
              confirming={session.confirming}
              abandoning={session.abandoning}
              runtime={session.runtime}
              runtimes={runtimesQuery.data ?? []}
              runtimesLoading={runtimesQuery.isLoading}
              members={membersQuery.data ?? []}
              assigneeSuggestions={assigneeSuggestionsQuery.data}
              assigneeSuggestionsLoading={assigneeSuggestionsQuery.isLoading}
              assigneeSuggestionsError={assigneeSuggestionsQuery.isError}
              currentUserId={currentUserId}
              switchingRuntime={session.switchingRuntime}
              pending={session.pending}
              attachments={session.attachments}
              readOnly={record}
              producedIssueId={session.producedIssueId}
              createdIssues={session.createdIssues}
              assignmentWarnings={session.assignmentWarnings}
              round={session.round}
              continuation={session.continuation}
              builtChildren={session.groupChildren}
              builtKeys={session.builtKeys}
              onDirtyChange={session.setLocalDirty}
              onSave={session.save}
              onGenerate={session.generatePreview}
              onConfirm={session.confirm}
              onAbandon={async () => {
                const abandoned = await session.abandon();
                if (abandoned) leave();
                return abandoned;
              }}
              onSwitchRuntime={session.switchRuntime}
            />
          </ResizablePanel>
        </ResizablePanelGroup>
      )}
    </div>
  );
}

function RetiredPanel({ onLeave }: { onLeave: () => void }) {
  const { t } = useT("issues");
  return (
    <div className="flex min-h-0 flex-1 items-center justify-center px-5 py-10">
      <div className="w-full max-w-md text-center">
        <h2 className="text-title-sm font-semibold">
          {t(($) => $.alignment.retired_title)}
        </h2>
        <p className="mt-2 text-body leading-6 text-muted-foreground">
          {t(($) => $.alignment.retired_description)}
        </p>
        <Button className="mt-5" onClick={onLeave}>
          {t(($) => $.alignment.back)}
        </Button>
      </div>
    </div>
  );
}

function FailedPanel({
  message,
  onRetry,
}: {
  message: string;
  onRetry: () => void;
}) {
  const { t } = useT("issues");
  return (
    <div className="flex min-h-0 flex-1 items-center justify-center px-5 py-10">
      <div className="w-full max-w-md text-center">
        <p role="alert" className="text-body text-destructive">
          {message}
        </p>
        <Button className="mt-5" variant="outline" onClick={onRetry}>
          {t(($) => $.alignment.retry)}
        </Button>
      </div>
    </div>
  );
}
