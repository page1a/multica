import { describe, expect, it, vi, beforeEach } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import { ApiError } from "@multica/core/api";
import { useIssueDraftStore } from "@multica/core/issues/stores";
import type {
  ChatMessage,
  IssueDraftPayload,
  IssueDraftSummary,
  RuntimeDevice,
} from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enIssues from "../../locales/en/issues.json";
import { issueDraftNodeId } from "@multica/core/issue-drafts";
import { IssueDraftPage } from "./issue-draft-page";

/**
 * What this page must never do is create an issue before the user confirms —
 * that is the entire reason the alignment step exists. The rest of the suite
 * pins the states around that boundary: the four stages, a confirm that is
 * refused, and a draft that survives its own failed confirm.
 */

const mocks = vi.hoisted(() => ({
  drafts: [] as unknown[],
  draftsError: null as unknown,
  messages: [] as unknown[],
  messagesError: null as unknown,
  pendingTaskId: null as string | null,
  runtimes: [] as unknown[],
  listIssueDrafts: vi.fn(),
  listChatMessages: vi.fn(),
  getPendingChatTask: vi.fn(),
  listRuntimes: vi.fn(),
  listMembers: vi.fn(),
  updateIssueDraft: vi.fn(),
  finalizeIssueDraft: vi.fn(),
  abandonIssueDraft: vi.fn(),
  switchIssueDraftRuntime: vi.fn(),
  switchIssueDraftPolicy: vi.fn(),
  reopenIssueDraft: vi.fn(),
  listChildIssues: vi.fn(),
  sendChatMessage: vi.fn(),
  push: vi.fn(),
  replace: vi.fn(),
  transcriptProps: {} as { transformContent?: (content: string) => string },
  groupProps: {} as { defaultLayout?: unknown },
  panelSizes: {} as Record<string, unknown>,
}));

// The split layout's persisted half: this suite is about what the page asks the
// group to open with, not about the storage behind it.
const layoutStore = vi.hoisted(() => ({
  stored: undefined as Record<string, number> | undefined,
}));

vi.mock("react-resizable-panels", () => ({
  useDefaultLayout: () => ({
    defaultLayout: layoutStore.stored,
    onLayoutChanged: vi.fn(),
  }),
}));

vi.mock("@multica/core/api", async () => {
  const actual = await vi.importActual<typeof import("@multica/core/api")>("@multica/core/api");
  return {
    ...actual,
    api: {
      listIssueDrafts: mocks.listIssueDrafts,
      listChatMessages: mocks.listChatMessages,
      getPendingChatTask: mocks.getPendingChatTask,
      listRuntimes: mocks.listRuntimes,
      listMembers: mocks.listMembers,
      updateIssueDraft: mocks.updateIssueDraft,
      finalizeIssueDraft: mocks.finalizeIssueDraft,
      abandonIssueDraft: mocks.abandonIssueDraft,
      switchIssueDraftRuntime: mocks.switchIssueDraftRuntime,
      switchIssueDraftPolicy: mocks.switchIssueDraftPolicy,
      reopenIssueDraft: mocks.reopenIssueDraft,
      listChildIssues: mocks.listChildIssues,
      sendChatMessage: mocks.sendChatMessage,
    },
  };
});

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: { user: { id: string } }) => unknown) =>
    selector({ user: { id: "user-1" } }),
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    issues: () => "/acme/issues",
    issueDetail: (id: string) => `/acme/issues/${id}`,
    newIssueDraft: (id: string) => `/acme/issues/new/${id}`,
    runtimes: () => "/acme/runtimes",
  }),
}));

vi.mock("../../navigation", () => ({
  useNavigation: () => ({
    push: mocks.push,
    replace: mocks.replace,
    pathname: "/acme/issues/new/sess-1",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (path: string) => path,
  }),
  useBackOrReplace: () => (fallback: string) => mocks.replace(fallback),
  // A real anchor: the record's way across to the issue it produced is a
  // navigation, and the suite asserts on where it points rather than on a click.
  AppLink: ({
    href,
    children,
    ...rest
  }: {
    href: string;
    children: React.ReactNode;
  } & React.AnchorHTMLAttributes<HTMLAnchorElement>) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}));

// The chat surfaces, the pickers and the split layout are not what this suite
// is about; each is replaced by the smallest thing that keeps its contract.
vi.mock("../../chat/components/chat-input", () => ({
  ChatInput: ({
    onSend,
    disabled,
    uploadEnabled,
  }: {
    onSend: (
      content: string,
      ids: string[] | undefined,
      commit: () => void,
    ) => void;
    disabled?: boolean;
    uploadEnabled?: boolean;
  }) => (
    <div data-testid="composer" data-upload-enabled={String(!!uploadEnabled)}>
      <button
        type="button"
        disabled={disabled}
        onClick={() => onSend("please continue", undefined, () => {})}
      >
        send-turn
      </button>
      <button
        type="button"
        disabled={disabled}
        onClick={() => onSend("look at this", ["att-1", "att-2"], () => {})}
      >
        send-turn-with-files
      </button>
    </div>
  ),
}));

vi.mock("../../chat/components/chat-message-list", () => ({
  ChatMessageList: ({
    messages,
    transformContent,
  }: {
    messages: { id: string; content: string }[];
    transformContent?: (content: string) => string;
  }) => {
    mocks.transcriptProps.transformContent = transformContent;
    return (
      <div data-testid="transcript">
        {messages.map((message) => (
          <p key={message.id} data-testid="transcript-row">
            {transformContent ? transformContent(message.content) : message.content}
          </p>
        ))}
      </div>
    );
  },
  ChatMessageSkeleton: () => <div data-testid="transcript-loading" />,
}));

// The group itself is not what this suite is about, but the split it is asked
// for is: the page hands it a first-run layout, and the panels carry the same
// split for the path where the group is measured only after mount.
vi.mock("@multica/ui/components/ui/resizable", () => ({
  ResizablePanelGroup: ({
    children,
    defaultLayout,
  }: {
    children: React.ReactNode;
    defaultLayout?: unknown;
  }) => {
    mocks.groupProps.defaultLayout = defaultLayout;
    return <div>{children}</div>;
  },
  ResizablePanel: ({
    children,
    id,
    defaultSize,
  }: {
    children: React.ReactNode;
    id?: string;
    defaultSize?: unknown;
  }) => {
    if (id) mocks.panelSizes[id] = defaultSize;
    return <div>{children}</div>;
  },
  ResizableHandle: () => <div />,
}));

vi.mock("../../agents/components/runtime-picker", () => ({
  RuntimePicker: () => <div data-testid="runtime-picker" />,
}));

vi.mock("../components/pickers/status-picker", () => ({
  StatusPicker: () => <div data-testid="status-picker" />,
}));

vi.mock("../components/pickers/priority-picker", () => ({
  PriorityPicker: () => <div data-testid="priority-picker" />,
}));

// The group's own pickers need a workspace roster this suite does not seed; the
// panel suite is where what they hand back is pinned.
vi.mock("../components/pickers/stage-picker", () => ({
  StagePicker: ({ stage }: { stage: number | null }) => (
    <div data-testid="stage-picker">{String(stage)}</div>
  ),
}));

vi.mock("../components/pickers/assignee-picker", () => ({
  AssigneePicker: ({ assigneeId }: { assigneeId: string | null }) => (
    <div data-testid="assignee-picker">{String(assigneeId)}</div>
  ),
}));

const TEST_RESOURCES = { en: { common: enCommon, issues: enIssues } };

function draftSummary(overrides: Partial<IssueDraftSummary> = {}): IssueDraftSummary {
  return {
    chat_session_id: "sess-1",
    workspace_id: "ws-1",
    status: "draft",
    revision: 3,
    draft: { title: "Dark mode", description: "Add it.", status: "", priority: "" },
    issue_id: null,
    policy: { key: "question", version: "1", guided: true },
    capabilities: { keys: [], version: "" },
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    title: "Align a new issue",
    runtime_id: "rt-1",
    last_message_content: "",
    last_message_role: "",
    last_message_at: "",
    ...overrides,
  };
}

function chatMessage(overrides: Partial<ChatMessage> = {}): ChatMessage {
  return {
    id: "m1",
    chat_session_id: "sess-1",
    role: "assistant",
    content: "What does dark mode cover?",
    created_at: "2026-01-01T00:00:00Z",
    ...overrides,
  } as ChatMessage;
}

const ONLINE_RUNTIME = {
  id: "rt-1",
  name: "Local",
  status: "online",
  owner_id: "user-1",
  provider: "claude",
} as unknown as RuntimeDevice;

function renderPage(draftId = "sess-1") {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <IssueDraftPage draftId={draftId} />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.transcriptProps = {};
  mocks.groupProps = {};
  mocks.panelSizes = {};
  layoutStore.stored = undefined;
  mocks.drafts = [draftSummary()];
  mocks.draftsError = null;
  mocks.messages = [chatMessage()];
  mocks.messagesError = null;
  mocks.pendingTaskId = null;
  mocks.runtimes = [ONLINE_RUNTIME];
  mocks.listIssueDrafts.mockImplementation(() => {
    if (mocks.draftsError) return Promise.reject(mocks.draftsError);
    return Promise.resolve(mocks.drafts);
  });
  mocks.listChatMessages.mockImplementation(() => {
    if (mocks.messagesError) return Promise.reject(mocks.messagesError);
    return Promise.resolve(mocks.messages);
  });
  mocks.getPendingChatTask.mockImplementation(() =>
    Promise.resolve(mocks.pendingTaskId ? { task_id: mocks.pendingTaskId } : {}),
  );
  mocks.listRuntimes.mockImplementation(() => Promise.resolve(mocks.runtimes));
  mocks.listMembers.mockImplementation(() => Promise.resolve([]));
  mocks.reopenIssueDraft.mockImplementation(() => Promise.resolve(draftSummary()));
  mocks.listChildIssues.mockImplementation(() => Promise.resolve({ issues: [] }));
});

describe("IssueDraftPage stages", () => {
  it("shows 对齐中 while the draft is still being aligned", async () => {
    renderPage();
    expect(await screen.findByText("Aligning")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Confirm and create/ })).toBeDisabled();
  });

  it("offers the confirm only once the server says the draft is ready", async () => {
    mocks.drafts = [draftSummary({ status: "ready" })];
    renderPage();
    expect(await screen.findByText("Ready")).toBeTruthy();
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /Confirm and create/ })).toBeEnabled(),
    );
  });

  it("does not offer a create for a ready draft with no title", async () => {
    // The server reads the title straight out of the draft at finalize, so a
    // blank one is a create it would refuse; the button must not promise it.
    mocks.drafts = [
      draftSummary({
        status: "ready",
        draft: { title: "  ", description: "d", status: "", priority: "" },
      }),
    ];
    renderPage();
    await screen.findByText("Ready");
    expect(screen.getByRole("button", { name: /Confirm and create/ })).toBeDisabled();
  });

  it("decodes both directions of the carrier's wire format in the transcript", async () => {
    mocks.messages = [
      chatMessage({
        id: "m1",
        role: "user",
        content:
          'MULTICA_ISSUE_DRAFT_INPUT\n{"user_request":"add dark mode","current_draft":{"title":"","description":"","status":"","priority":""}}',
      }),
      chatMessage({
        id: "m2",
        role: "assistant",
        content: 'Which surfaces?\n<issue_draft>{"title":"Dark mode"}</issue_draft>',
      }),
    ];
    renderPage();
    await waitFor(() => expect(screen.getAllByTestId("transcript-row")).toHaveLength(2));
    const rows = screen.getAllByTestId("transcript-row").map((row) => row.textContent);
    expect(rows).toEqual(["add dark mode", "Which surfaces?"]);
  });

  it("strips the machine blocks on the way to the transcript, question block included", async () => {
    // A settled reply is drawn from the carrier's task transcript, not from the
    // message body the page already stripped, so the strip has to travel down
    // to the list as well — otherwise the raw block comes back in the bubble
    // (DENE-317). The shapes that strip has to survive are canonical in
    // packages/core/issue-drafts/protocol.test.ts; this is the wiring.
    renderPage();
    await waitFor(() => expect(mocks.transcriptProps.transformContent).toBeTypeOf("function"));
    const transform = mocks.transcriptProps.transformContent!;
    expect(transform('两处已落进草稿。\n\n<issue_draft>{"title":"T"}</issue_draft>')).toBe(
      "两处已落进草稿。",
    );
    expect(
      transform('Who runs it?\n<issue_draft_question>{"question":"Who runs it?"}</issue_draft_question>'),
    ).toBe("Who runs it?");
  });

  it("sends every follow-up turn as an envelope carrying the current draft", async () => {
    // The carrier is told to "preserve good existing draft fields supplied in
    // the user's message". A turn sent as bare text supplies none, so the next
    // reply rebuilds the draft from that one message and drops what was already
    // agreed — including anything the user edited by hand in the preview.
    mocks.sendChatMessage.mockResolvedValue({ message_id: "m9", task_id: "t9" });
    renderPage();
    const send = await screen.findByRole("button", { name: "send-turn" });
    await userEvent.click(send);
    await waitFor(() => expect(mocks.sendChatMessage).toHaveBeenCalledTimes(1));
    expect(mocks.sendChatMessage).toHaveBeenCalledWith(
      "sess-1",
      'MULTICA_ISSUE_DRAFT_INPUT\n{"user_request":"please continue","current_draft":{"title":"Dark mode","description":"Add it.","status":"","priority":""}}',
      // A text-only turn carries no attachment ids; the envelope is unchanged
      // by the attachment path existing (DENE-369).
      undefined,
    );
  });

  it("forwards the composer's attachment ids to the send", async () => {
    // The alignment composer is the chat composer, so a request that starts
    // as a screenshot or a spec file must be alignable without a second
    // upload surface. Before DENE-369 the page dropped the ids on the floor
    // and the carrier only ever saw the prose.
    mocks.sendChatMessage.mockResolvedValue({
      message_id: "m10",
      task_id: "t10",
      attachment_ids: ["att-1", "att-2"],
    });
    renderPage();
    const send = await screen.findByRole("button", { name: "send-turn-with-files" });
    await userEvent.click(send);
    await waitFor(() => expect(mocks.sendChatMessage).toHaveBeenCalledTimes(1));
    expect(mocks.sendChatMessage.mock.calls[0]?.[2]).toEqual(["att-1", "att-2"]);
  });

  it("offers the upload affordance only while the runtime can answer", async () => {
    renderPage();
    await waitFor(() =>
      expect(screen.getByTestId("composer")).toHaveAttribute(
        "data-upload-enabled",
        "true",
      ),
    );
  });

  it("withholds the upload affordance while the runtime is offline", async () => {
    mocks.runtimes = [{ ...ONLINE_RUNTIME, status: "offline" }];
    renderPage();
    await waitFor(() =>
      expect(screen.getByTestId("composer")).toHaveAttribute(
        "data-upload-enabled",
        "false",
      ),
    );
  });

  it("stays quiet on a server that predates attachment_ids", async () => {
    // An installed desktop client can talk to a backend that never echoes the
    // field. Reading its absence as "nothing bound" would warn on every
    // attachment send against that backend, so the check is skipped instead.
    mocks.sendChatMessage.mockResolvedValue({ message_id: "m12", task_id: "t12" });
    renderPage();
    const send = await screen.findByRole("button", { name: "send-turn-with-files" });
    await userEvent.click(send);
    await waitFor(() => expect(mocks.sendChatMessage).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("reports the attachments the server did not bind", async () => {
    // A silent bind failure otherwise shows up only as an assistant that never
    // mentions the file the user attached.
    mocks.sendChatMessage.mockResolvedValue({
      message_id: "m11",
      task_id: "t11",
      attachment_ids: ["att-1"],
    });
    renderPage();
    const send = await screen.findByRole("button", { name: "send-turn-with-files" });
    await userEvent.click(send);
    expect(await screen.findByRole("alert")).toHaveTextContent(
      /attachments/i,
    );
  });

  /**
   * DENE-422: the entry panel cannot say this itself — it closes as the
   * conversation opens — so it leaves the draft id in the create draft's align
   * slot. The composer's error slot is where the sentence belongs, because
   * resending from the composer right below it is the entire recovery.
   */
  it("says the first turn was lost when the entry panel handed one over", async () => {
    useIssueDraftStore.getState().setAlign({ seedFailedDraftId: "sess-1" });
    renderPage();

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "The first turn was not sent. Send it again below.",
    );
    // Read and cleared in the same pass: the sentence describes this arrival,
    // not every later visit to the conversation.
    expect(
      useIssueDraftStore.getState().draft.align.seedFailedDraftId,
    ).toBeUndefined();
  });

  it("ignores a lost-turn flag meant for another conversation", async () => {
    useIssueDraftStore.getState().setAlign({ seedFailedDraftId: "sess-other" });
    renderPage();
    await screen.findByText("Aligning");

    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("drops the lost-turn sentence once the turn is actually sent", async () => {
    useIssueDraftStore.getState().setAlign({ seedFailedDraftId: "sess-1" });
    mocks.sendChatMessage.mockResolvedValue({ message_id: "m12", task_id: "t12" });
    renderPage();
    await screen.findByRole("alert");

    await userEvent.click(screen.getByRole("button", { name: "send-turn" }));

    await waitFor(() => expect(mocks.sendChatMessage).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("still offers the structured preview once the draft is ready", async () => {
    // A converged draft the user keeps refining produces new carrier blocks;
    // with this disabled, retyping them by hand is the only way to apply them.
    mocks.drafts = [draftSummary({ status: "ready" })];
    renderPage();
    await screen.findByText("Ready");
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: /Generate preview/i }),
      ).toBeEnabled(),
    );
  });
});

describe("IssueDraftPage confirming", () => {
  it("creates nothing until confirm, then navigates to the issue it made", async () => {
    mocks.drafts = [draftSummary({ status: "ready" })];
    mocks.finalizeIssueDraft.mockResolvedValue({
      draft: draftSummary({ status: "completed", issue_id: "issue-9" }),
      issue_id: "issue-9",
    });
    renderPage();
    const confirm = await screen.findByRole("button", { name: /Confirm and create/ });
    await waitFor(() => expect(confirm).toBeEnabled());
    await userEvent.click(confirm);

    await waitFor(() => expect(mocks.finalizeIssueDraft).toHaveBeenCalledTimes(1));
    // The revision the user was looking at travels with the confirm: that is
    // what makes a confirm from a superseded view refuse instead of overwrite.
    expect(mocks.finalizeIssueDraft).toHaveBeenCalledWith("sess-1", {
      expected_revision: 3,
    });
    await waitFor(() => expect(mocks.replace).toHaveBeenCalledWith("/acme/issues/issue-9"));
  });

  it("lands back on the issue this alignment was started from", async () => {
    // An alignment started from an existing issue files its group BENEATH it
    // (DENE-452), so confirming puts the person back where they were working
    // rather than on the root it just created.
    mocks.drafts = [
      draftSummary({
        status: "ready",
        draft: {
          title: "Dark mode",
          description: "Add it.",
          status: "",
          priority: "",
          parent_issue_id: "issue-7",
        },
      }),
    ];
    mocks.finalizeIssueDraft.mockResolvedValue({
      draft: draftSummary({ status: "completed", issue_id: "issue-9" }),
      issue_id: "issue-9",
    });
    renderPage();
    const confirm = await screen.findByRole("button", { name: /Confirm and create/ });
    await waitFor(() => expect(confirm).toBeEnabled());
    await userEvent.click(confirm);

    await waitFor(() =>
      expect(mocks.replace).toHaveBeenCalledWith("/acme/issues/issue-7"),
    );
    expect(mocks.replace).not.toHaveBeenCalledWith("/acme/issues/issue-9");
  });

  it("sends one confirm for a double click, not two creates", async () => {
    mocks.drafts = [draftSummary({ status: "ready" })];
    let release: ((value: unknown) => void) | undefined;
    mocks.finalizeIssueDraft.mockImplementation(
      () => new Promise((resolve) => { release = resolve; }),
    );
    renderPage();
    const confirm = await screen.findByRole("button", { name: /Confirm and create/ });
    await waitFor(() => expect(confirm).toBeEnabled());

    await userEvent.click(confirm);
    // The second press lands while the first is still in flight. `isPending` is
    // what has to stop it — a local flag can drift from the request it names.
    await userEvent.click(confirm);
    expect(mocks.finalizeIssueDraft).toHaveBeenCalledTimes(1);

    release?.({
      draft: draftSummary({ status: "completed", issue_id: "issue-9" }),
      issue_id: "issue-9",
    });
    await waitFor(() => expect(mocks.replace).toHaveBeenCalledWith("/acme/issues/issue-9"));
  });

  it("lands on the created issue when one tick sends the confirm twice", async () => {
    // Two presses dispatched in a single task — a scripted double click, which
    // is how the acceptance run recorded two finalize POSTs in the same
    // millisecond. Both answer with the same issue, and the completed draft
    // then leaves the unfinished list, so the page has to navigate once and
    // stay landed: a page that does not is left on the alignment URL showing
    // "this alignment has finished" (DENE-317).
    mocks.drafts = [draftSummary({ status: "ready" })];
    mocks.finalizeIssueDraft.mockResolvedValue({
      draft: draftSummary({ status: "completed", issue_id: "issue-9" }),
      issue_id: "issue-9",
    });
    // The confirm retires the draft, so the follow-up refetch re-renders the
    // page with no row while the navigation is still in flight. It lands a beat
    // later on purpose: the render that still has the row is what the first
    // replace comes from, and the one without it is the render that used to
    // re-issue that replace.
    mocks.listIssueDrafts.mockImplementation(() =>
      mocks.finalizeIssueDraft.mock.calls.length > 0
        ? new Promise((resolve) => setTimeout(() => resolve([]), 20))
        : Promise.resolve(mocks.drafts),
    );
    renderPage();
    const confirm = await screen.findByRole("button", { name: /Confirm and create/ });
    await waitFor(() => expect(screen.getByRole("heading", { name: "Dark mode" })).toBeTruthy());
    await waitFor(() => expect(confirm).toBeEnabled());

    await act(async () => {
      // Raw dispatches: RTL's fireEvent wraps each one in `act`, which flushes
      // the disabled button between them and hides the very path under test.
      confirm.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
      confirm.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });
    expect(mocks.finalizeIssueDraft).toHaveBeenCalledTimes(2);

    await waitFor(() => expect(mocks.replace).toHaveBeenCalledWith("/acme/issues/issue-9"));
    // The row is gone from the list: this is the re-render that used to
    // re-issue the replace, because the effect's `paths` dependency is rebuilt
    // on every render and a router asked to replace the same URL forever never
    // commits. The header falls back to the unnamed-draft placeholder.
    await waitFor(() =>
      expect(screen.getByRole("heading", { name: "Align a new issue" })).toBeTruthy(),
    );
    await act(async () => {});
    expect(
      mocks.replace.mock.calls.filter(([path]) => path === "/acme/issues/issue-9"),
    ).toHaveLength(1);
  });

  it("keeps the draft on screen when finalize is refused", async () => {
    mocks.drafts = [draftSummary({ status: "ready" })];
    mocks.finalizeIssueDraft.mockRejectedValue(
      new ApiError(
        "this draft changed since you loaded it; reload and try again",
        409,
        "Conflict",
      ),
    );
    renderPage();
    const confirm = await screen.findByRole("button", { name: /Confirm and create/ });
    await waitFor(() => expect(confirm).toBeEnabled());
    await userEvent.click(confirm);

    expect(
      await screen.findByText("this draft changed since you loaded it; reload and try again"),
    ).toBeTruthy();
    expect(mocks.replace).not.toHaveBeenCalledWith(expect.stringContaining("/acme/issues/issue"));
  });

  it("keeps the draft when the issue was created but the body is unreadable", async () => {
    // No zod fallback on finalize: an unparseable 2xx must throw rather than
    // hand the router an empty id, and the retry is safe because the protocol
    // returns the same issue for every repeat.
    mocks.drafts = [draftSummary({ status: "ready" })];
    mocks.finalizeIssueDraft.mockRejectedValue(new Error("invalid finalize response"));
    renderPage();
    const confirm = await screen.findByRole("button", { name: /Confirm and create/ });
    await waitFor(() => expect(confirm).toBeEnabled());
    await userEvent.click(confirm);
    expect(await screen.findByText("invalid finalize response")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Confirm and create/ })).toBeTruthy();
  });
});

describe("IssueDraftPage group", () => {
  const GROUP_DRAFT: IssueDraftPayload = {
    title: "收件箱支持批量标记已读",
    description: "d",
    status: "",
    priority: "",
    children: [
      {
        key: "c1",
        title: "后端接口",
        description: "",
        status: "todo",
        priority: "none",
        stage: 1,
        assignee_type: "agent",
        assignee_id: "ag-1",
        assignee_hint: "backend",
      },
      {
        key: "c2",
        title: "前端页面",
        description: "",
        status: "backlog",
        priority: "none",
        stage: 2,
        assignee_type: "agent",
        assignee_id: "ag-2",
      },
    ],
  };

  it("shows the whole group and the rows that will start, before confirming", async () => {
    // The one thing a user cannot discover from a single-issue preview: this
    // confirm creates three issues and starts exactly one agent.
    mocks.drafts = [
      draftSummary({ status: "ready", draft: GROUP_DRAFT }),
    ];
    renderPage();

    expect(await screen.findByText("Sub-issues")).toBeTruthy();
    expect(
      (screen.getByLabelText("Sub-issue 1 title") as HTMLInputElement).value,
    ).toBe("后端接口");
    expect(
      (screen.getByLabelText("Sub-issue 2 title") as HTMLInputElement).value,
    ).toBe("前端页面");
    expect(screen.getAllByText("Runs immediately")).toHaveLength(1);
    expect(screen.getAllByText("Waits for its stage")).toHaveLength(1);
    expect(screen.getByText("Confirming creates 3 issues.")).toBeTruthy();
    expect(screen.getByText("Starting right away: 1.")).toBeTruthy();
    expect(
      screen.getByText("Created in Backlog, waiting for their stage: 1."),
    ).toBeTruthy();
  });

  it("does not offer a confirm for a group with an untitled sub-issue", async () => {
    mocks.drafts = [
      draftSummary({
        status: "ready",
        draft: {
          ...GROUP_DRAFT,
          children: [{ ...GROUP_DRAFT.children![1]!, title: "  " }],
        },
      }),
    ];
    renderPage();
    await screen.findByText("Sub-issues");
    expect(
      screen.getByRole("button", { name: /Confirm and create/ }),
    ).toBeDisabled();
  });

  it("lists the group the confirm created instead of only its parent", async () => {
    // A repeat confirm (a second tab, a retry) has to read back the same list —
    // the endpoint answers with the set precisely so the page cannot degrade to
    // "one issue" on the second look (design §5.2).
    mocks.drafts = [draftSummary({ status: "ready", draft: GROUP_DRAFT })];
    mocks.finalizeIssueDraft.mockResolvedValue({
      draft: draftSummary({
        status: "completed",
        draft: GROUP_DRAFT,
        issue_id: "issue-9",
      }),
      issue_id: "issue-9",
      issues: [
        { id: "issue-9", identifier: "MUL-9", title: "Parent", status: "todo" },
        { id: "issue-10", identifier: "MUL-10", title: "Child", status: "todo" },
        { id: "issue-11", identifier: "MUL-11", title: "Later", status: "backlog" },
      ],
    });
    renderPage();
    const confirm = await screen.findByRole("button", { name: /Confirm and create/ });
    await waitFor(() => expect(confirm).toBeEnabled());
    await userEvent.click(confirm);

    await waitFor(() => expect(screen.getByText("3 issues created")).toBeTruthy());
    expect(screen.getByRole("link", { name: /MUL-9/ })).toBeTruthy();
    expect(screen.getByRole("link", { name: /MUL-10/ })).toBeTruthy();
    expect(screen.getByRole("link", { name: /MUL-11/ })).toBeTruthy();
    // The page still lands on the group's root.
    expect(mocks.replace).toHaveBeenCalledWith("/acme/issues/issue-9");
  });

  it("keeps the dropped-seat notice when one tick sends the confirm twice", async () => {
    // Only the answer that INSERTED the rows can name the seats it could not
    // apply: the duplicate adopts the group the first one made and reports
    // nothing, because it created nothing. Reading that silence as "every seat
    // landed" would let a duplicate press erase the notice the creating answer
    // produced, which is the one thing the person had to be told (DENE-694).
    const created = {
      draft: draftSummary({
        status: "completed",
        draft: GROUP_DRAFT,
        issue_id: "issue-9",
      }),
      issue_id: "issue-9",
      issues: [
        { id: "issue-9", identifier: "MUL-9", title: "Parent", status: "todo" },
        { id: "issue-10", identifier: "MUL-10", title: "后端接口", status: "todo" },
        { id: "issue-11", identifier: "MUL-11", title: "前端页面", status: "backlog" },
      ],
    };
    mocks.drafts = [draftSummary({ status: "ready", draft: GROUP_DRAFT })];
    // `mockResolvedValueOnce` queues survive `clearAllMocks`, so the two answers
    // below are queued onto an empty mock rather than onto a leftover.
    mocks.finalizeIssueDraft.mockReset();
    mocks.finalizeIssueDraft
      .mockResolvedValueOnce({
        ...created,
        assignment_warnings: [
          { key: "c1", title: "后端接口", reason: "cannot invoke this agent" },
        ],
      })
      // The adopting answer of the same round, and the later one by
      // construction: it can only start once the first insert committed.
      .mockResolvedValueOnce({ ...created });
    renderPage();
    const confirm = await screen.findByRole("button", { name: /Confirm and create/ });
    await waitFor(() => expect(confirm).toBeEnabled());

    await act(async () => {
      // Raw dispatches for the same reason the double-confirm test above uses
      // them: RTL's fireEvent flushes the disabled button between the two.
      confirm.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
      confirm.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });
    expect(mocks.finalizeIssueDraft).toHaveBeenCalledTimes(2);

    await waitFor(() =>
      expect(
        screen.getByText(
          "“后端接口” could not be assigned, so it was created unassigned.",
        ),
      ).toBeTruthy(),
    );
  });

  it("stays on the panel that names the dropped seat instead of replacing the URL", async () => {
    // This page is the only place the dropped seat is ever visible — the issue
    // it made is an ordinary unassigned issue by the time the URL changes — so
    // the replace a clean confirm performs has to wait. The created rows are
    // still one click away, which is what the replace would have landed on.
    mocks.drafts = [draftSummary({ status: "ready", draft: GROUP_DRAFT })];
    mocks.finalizeIssueDraft.mockResolvedValue({
      draft: draftSummary({
        status: "completed",
        draft: GROUP_DRAFT,
        issue_id: "issue-9",
      }),
      issue_id: "issue-9",
      issues: [
        { id: "issue-9", identifier: "MUL-9", title: "Parent", status: "todo" },
        { id: "issue-10", identifier: "MUL-10", title: "后端接口", status: "todo" },
        { id: "issue-11", identifier: "MUL-11", title: "前端页面", status: "backlog" },
      ],
      assignment_warnings: [
        { key: "c2", title: "前端页面", reason: "cannot invoke this agent" },
      ],
    });
    renderPage();
    const confirm = await screen.findByRole("button", { name: /Confirm and create/ });
    await waitFor(() => expect(confirm).toBeEnabled());
    await userEvent.click(confirm);

    await waitFor(() =>
      expect(
        screen.getByText(
          "“前端页面” could not be assigned, so it was created unassigned.",
        ),
      ).toBeTruthy(),
    );
    expect(screen.getByRole("link", { name: /MUL-9/ })).toBeTruthy();
    expect(mocks.replace).not.toHaveBeenCalled();
  });
});

describe("IssueDraftPage loading", () => {
  it("shows a transcript skeleton while the conversation loads", async () => {
    mocks.messages = [];
    mocks.listChatMessages.mockImplementation(() => new Promise(() => {}));
    renderPage();
    expect(await screen.findByTestId("transcript-loading")).toBeTruthy();
  });
});

describe("IssueDraftPage recovery", () => {
  it("reads the draft back after a refresh instead of starting over", async () => {
    renderPage();
    // No local state seeds this: the title on screen came from the list fetch.
    expect(await screen.findByRole("heading", { name: "Dark mode" })).toBeTruthy();
  });

  it("says the alignment is finished when the draft is no longer unfinished", async () => {
    mocks.drafts = [];
    renderPage();
    expect(await screen.findByText("This alignment has finished")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Confirm and create/ })).toBeNull();
  });

  it("leaves when the conversation itself is gone", async () => {
    mocks.messagesError = new ApiError("API error: 404 Not Found", 404, "Not Found");
    renderPage();
    await waitFor(() => expect(mocks.replace).toHaveBeenCalledWith("/acme/issues"));
  });

  it("surfaces a failed list fetch with a retry instead of an empty page", async () => {
    mocks.draftsError = new ApiError("API error: 500 Internal Server Error", 500, "Internal Server Error");
    renderPage();
    expect(await screen.findByText("Could not load this alignment.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
  });
});

/**
 * The alignment policy is the carrier's prompt, made switchable and auditable.
 * It sits behind the header's ⋯ menu rather than on the header itself
 * (DENE-367): a style is something reached for, not a choice to make before the
 * first sentence. What these pin: the control is reachable, it reflects what the
 * server says is running (not what was clicked), the recorded prompt version is
 * on screen, and a carrier question is answerable in one click — with the
 * composed answer going out through the same envelope every other turn uses.
 */
async function openStyleMenu() {
  // Base UI portals the popup, so this is a click through the primitive rather
  // than a userEvent pointer sequence.
  fireEvent.click(
    await screen.findByRole("button", { name: "More alignment options" }),
  );
}

describe("IssueDraftPage policy", () => {
  it("shows which policy is running, with the prompt version it recorded", async () => {
    renderPage();
    expect(await screen.findByText("Aligning")).toBeTruthy();
    expect(
      screen.queryByRole("menuitemradio", { name: "Guided questions" }),
    ).toBeNull();

    await openStyleMenu();
    expect(
      await screen.findByRole("menuitemradio", { name: "Guided questions" }),
    ).toHaveAttribute("aria-checked", "true");
    expect(screen.getByText("Prompt question@1")).toBeTruthy();
  });

  it("switches to plain dialogue through the server, not in local state", async () => {
    mocks.switchIssueDraftPolicy.mockResolvedValue({
      ...draftSummary({ policy: { key: "conversation", version: "1", guided: false } }),
    });
    renderPage();
    await openStyleMenu();
    fireEvent.click(
      await screen.findByRole("menuitemradio", { name: "Plain conversation" }),
    );
    await waitFor(() =>
      expect(mocks.switchIssueDraftPolicy).toHaveBeenCalledWith("sess-1", {
        policy: "conversation",
      }),
    );
  });

  // The menu is rendered from the client's whitelist, not from the server's
  // registry, so a list that stopped at the two text-only styles would leave the
  // look round unreachable — the one style a user has to be able to ask for,
  // since it spends turns drawing instead of interviewing.
  it("offers every style with its own label, and switches to the look round", async () => {
    mocks.switchIssueDraftPolicy.mockResolvedValue({
      ...draftSummary({ policy: { key: "frontend", version: "1", guided: true } }),
    });
    renderPage();
    await openStyleMenu();

    const options = await screen.findAllByRole("menuitemradio");
    expect(options.map((option) => option.textContent)).toEqual([
      "Guided questions",
      "Plain conversation",
      "Front-end prototype",
    ]);

    fireEvent.click(
      screen.getByRole("menuitemradio", { name: "Front-end prototype" }),
    );
    await waitFor(() =>
      expect(mocks.switchIssueDraftPolicy).toHaveBeenCalledWith("sess-1", {
        policy: "frontend",
      }),
    );
  });

  it("reports the look round's own key and prompt version once it is recorded", async () => {
    mocks.drafts = [
      draftSummary({ policy: { key: "frontend", version: "1", guided: true } }),
    ];
    renderPage();
    await openStyleMenu();
    expect(
      await screen.findByRole("menuitemradio", { name: "Front-end prototype" }),
    ).toHaveAttribute("aria-checked", "true");
    expect(screen.getByText("Prompt frontend@1")).toBeTruthy();
  });

  it("surfaces a refused switch and leaves the running policy on screen", async () => {
    mocks.switchIssueDraftPolicy.mockRejectedValue(new Error("stop the current reply first"));
    renderPage();
    await openStyleMenu();
    fireEvent.click(
      await screen.findByRole("menuitemradio", { name: "Plain conversation" }),
    );
    expect(await screen.findByText("stop the current reply first")).toBeTruthy();
    // Selecting closes the menu so the refusal is visible; reopening must still
    // show the style the server is running, not the one that was clicked.
    await openStyleMenu();
    expect(
      await screen.findByRole("menuitemradio", { name: "Guided questions" }),
    ).toHaveAttribute("aria-checked", "true");
  });

  it("offers no control at all when the backend reports no policy", async () => {
    // An installed desktop client can talk to a backend that predates policies;
    // a switch that cannot land is worse than no switch, and an empty menu is
    // worse than no menu.
    mocks.drafts = [draftSummary({ policy: { key: "", version: "", guided: false } })];
    renderPage();
    await screen.findByText("Aligning");
    expect(screen.queryByRole("button", { name: "More alignment options" })).toBeNull();
    expect(
      screen.queryByRole("menuitemradio", { name: "Plain conversation" }),
    ).toBeNull();
    expect(
      screen.queryByRole("menuitemradio", { name: "Guided questions" }),
    ).toBeNull();
  });
});

/**
 * The page opens conversation-first: the transcript is the point, and the
 * structured draft is a sidecar. The stored layout keeps winning, or reopening
 * the page would undo a divider the user dragged.
 */
describe("IssueDraftPage layout", () => {
  it("opens with the conversation on the main width", async () => {
    renderPage();
    await screen.findByText("Aligning");
    expect(mocks.groupProps.defaultLayout).toEqual({ conversation: 70, preview: 30 });
    // Declared per panel too: the group derives its first layout from these
    // when it is measured only after mount, and the two must agree.
    expect(mocks.panelSizes.conversation).toBe("70%");
    expect(mocks.panelSizes.preview).toBe("30%");
  });

  it("keeps a layout the user already dragged", async () => {
    layoutStore.stored = { conversation: 22, preview: 78 };
    renderPage();
    await screen.findByText("Aligning");
    expect(mocks.groupProps.defaultLayout).toEqual({ conversation: 22, preview: 78 });
  });
});

describe("IssueDraftPage questions", () => {
  const questionReply =
    'Who should run it?\n<issue_draft_question>{"question":"Who should run it?","options":[{"label":"A bot","value":"Assign a bot","recommended":true},{"label":"Nobody yet","value":"Leave it unassigned"}]}</issue_draft_question>\n<issue_draft>{"title":"Dark mode"}</issue_draft>';

  it("renders the open question with its recommended answer marked", async () => {
    mocks.messages = [chatMessage({ id: "m2", content: questionReply })];
    renderPage();
    // Twice on purpose: the carrier's own prose and the answer card, which is
    // what makes the question answerable even when the prose omits it.
    await waitFor(() =>
      expect(screen.getAllByText("Who should run it?").length).toBeGreaterThan(0),
    );
    expect(screen.getByRole("button", { name: /A bot/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /A bot/ })).toBeTruthy();
    expect(screen.getByText("Recommended")).toBeTruthy();
    // The block itself must never reach the transcript.
    expect(screen.queryByText(/issue_draft_question/)).toBeNull();
  });

  it("sends the clicked option as the user's own answer", async () => {
    mocks.messages = [chatMessage({ id: "m2", content: questionReply })];
    mocks.sendChatMessage.mockResolvedValue({ message_id: "m9", task_id: "t9" });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: /Nobody yet/ }));
    await waitFor(() => expect(mocks.sendChatMessage).toHaveBeenCalledTimes(1));
    const wire = mocks.sendChatMessage.mock.calls[0]?.[1];
    expect(wire).toContain('"user_request":"Leave it unassigned"');
  });

  it("keeps the chips out of a plain-dialogue conversation", async () => {
    // The unguided policy does not interview, so a stray block must not turn
    // the page back into a questionnaire.
    mocks.drafts = [
      draftSummary({ policy: { key: "conversation", version: "1", guided: false } }),
    ];
    mocks.messages = [chatMessage({ id: "m2", content: questionReply })];
    renderPage();
    await screen.findByText("Aligning");
    expect(screen.queryByText("Who should run it?")).toBeNull();
  });
});

describe("IssueDraftPage draft persistence", () => {
  it("writes the carrier's proposal to the server draft as the conversation goes", async () => {
    // The draft is what finalize reads. A proposal that only exists in the
    // browser until someone presses a button is one refresh away from gone.
    mocks.messages = [
      chatMessage({
        id: "m2",
        content: 'Sure.\n<issue_draft>{"title":"Dark mode","priority":"high"}</issue_draft>',
      }),
    ];
    mocks.updateIssueDraft.mockImplementation(
      (_draftId: string, data: { draft: IssueDraftSummary["draft"] }) =>
        Promise.resolve({
          ...draftSummary({ revision: 4 }),
          draft: data.draft,
        }),
    );
    renderPage();
    await waitFor(() => expect(mocks.updateIssueDraft).toHaveBeenCalledTimes(1));
    expect(mocks.updateIssueDraft.mock.calls[0]?.[1]).toMatchObject({
      draft: { title: "Dark mode", description: "Add it.", priority: "high" },
      status: "draft",
      expected_revision: 3,
    });
  });
});

/**
 * A finished alignment is the same page in a read-only shape (DENE-371). The
 * read-back path is the chat sidebar's alignment records: those rows carry a
 * terminal status, so the page must render what was agreed rather than
 * redirect to the issue, and must not offer a next turn the server refuses.
 */
describe("IssueDraftPage reading a finished alignment", () => {
  it("shows the created issue instead of a composer", async () => {
    mocks.drafts = [draftSummary({ status: "completed", issue_id: "issue-9" })];
    renderPage();

    expect(await screen.findByText("This alignment produced an issue")).toBeTruthy();
    const link = screen.getByRole("link", { name: /Open the issue/ });
    expect(link.getAttribute("href")).toBe("/acme/issues/issue-9");
    // The conversation is still the point of the record.
    expect(screen.getByTestId("transcript")).toBeTruthy();
    // Everything that describes a next turn is gone: the composer, the confirm,
    // and the stage strip that would label a finished alignment "Aligning".
    expect(screen.queryByTestId("composer")).toBeNull();
    expect(screen.queryByRole("button", { name: /Confirm and create/ })).toBeNull();
    expect(screen.queryByText("Aligning")).toBeNull();
  });

  it("does not redirect away from a draft that already produced its issue", async () => {
    // The live flow replaces the alignment URL with the created issue. Reading a
    // record is the opposite intent — the user asked for the conversation.
    mocks.drafts = [draftSummary({ status: "completed", issue_id: "issue-9" })];
    renderPage();

    await screen.findByText("This alignment produced an issue");
    expect(mocks.replace).not.toHaveBeenCalledWith("/acme/issues/issue-9");
  });

  it("says a discarded alignment produced nothing", async () => {
    mocks.drafts = [draftSummary({ status: "abandoned" })];
    renderPage();

    expect(await screen.findByText("This alignment was given up")).toBeTruthy();
    expect(screen.getByText("No issue was created.")).toBeTruthy();
    expect(screen.queryByRole("link", { name: /Open the issue/ })).toBeNull();
    expect(screen.queryByTestId("composer")).toBeNull();
    // A stale event cannot make a finished alignment speak again.
    expect(mocks.sendChatMessage).not.toHaveBeenCalled();
  });
});

/**
 * DENE-415 / DENE-416: a round after the first. The draft row the page reads
 * carries `finalize_round`, so the round comes off the list — opening an
 * alignment is a read, and it never writes one back — and the group the
 * earlier rounds built has to be separated from what this round adds, or the
 * confirm promises creates it does not perform.
 */
describe("IssueDraftPage continuation round", () => {
  const SESSION = "0193a5f0-1c2d-7e3f-8a4b-5c6d7e8f9a0b";
  const child = (key: string, title: string) => ({
    key,
    title,
    description: "",
    status: "todo",
    priority: "none",
    stage: 1,
    assignee_type: null,
    assignee_id: null,
    assignee_hint: null,
  });

  it("reads the round it is on from its row and names what the earlier rounds built", async () => {
    mocks.drafts = [
      draftSummary({
        chat_session_id: SESSION,
        status: "ready",
        issue_id: "issue-1",
        finalize_round: 1,
        finalized_revision: 3,
        draft: {
          title: "Dark mode",
          description: "Add it.",
          status: "",
          priority: "",
          children: [child("c1", "后端开关"), child("c2", "前端主题")],
        },
      }),
    ];
    mocks.listChildIssues.mockResolvedValue({
      issues: [
        {
          id: "issue-2",
          identifier: "TES-2",
          title: "后端开关",
          status: "todo",
          stage: 1,
          origin_type: "issue_draft",
          origin_id: issueDraftNodeId(SESSION, "c1"),
        },
      ],
    });

    renderPage(SESSION);

    expect(await screen.findByText("Round 2")).toBeTruthy();
    // c1 already owns an issue, so it is shown as created and not as incoming.
    expect(await screen.findByText("Already created")).toBeTruthy();
    expect(screen.getByRole("link", { name: /TES-2/ })).toBeTruthy();
    await waitFor(() =>
      expect(screen.getByText("Confirming creates 1 new issue(s).")).toBeTruthy(),
    );
    // The round came off the list row: nothing had to be reopened to read it.
    expect(mocks.reopenIssueDraft).not.toHaveBeenCalled();
  });

  it("reads a continuation as round 1 on a backend whose list drops the round", async () => {
    // An installed desktop client can talk to a backend that predates the
    // field; the row then carries no round, and the page must not invent one —
    // nor go looking for it with a write.
    mocks.drafts = [
      draftSummary({
        chat_session_id: SESSION,
        status: "ready",
        issue_id: "issue-1",
        draft: {
          title: "Dark mode",
          description: "Add it.",
          status: "",
          priority: "",
          children: [child("c1", "后端开关")],
        },
      }),
    ];

    renderPage(SESSION);

    expect(await screen.findByText("Round 1")).toBeTruthy();
    expect(mocks.reopenIssueDraft).not.toHaveBeenCalled();
  });

  it("does not reopen a finished alignment just by opening it", async () => {
    // A record is read back, not continued: reopening on load would restart a
    // conversation the user only came to look at.
    mocks.drafts = [
      draftSummary({ status: "completed", issue_id: "issue-1", finalize_round: 1 }),
    ];
    renderPage();
    await screen.findByText("This alignment has finished. It is read-only.");
    expect(mocks.reopenIssueDraft).not.toHaveBeenCalled();
  });
});

describe("IssueDraftPage carried files", () => {
  const prototype = {
    id: "att-1",
    filename: "prototype.png",
    content_type: "image/png",
    download_url: "/api/attachments/att-1/download",
    markdown_url: "/api/attachments/att-1/download",
  } as unknown as NonNullable<ChatMessage["attachments"]>[number];

  const mock = {
    id: "att-2",
    filename: "mock.html",
    content_type: "text/html",
    download_url: "/api/attachments/att-2/download",
    markdown_url: "/api/attachments/att-2/download",
  } as unknown as NonNullable<ChatMessage["attachments"]>[number];

  it("reads the files off the transcript, from both sides and once each", async () => {
    // The user's upload rides a user message; the carrier's own prototype rides
    // its reply. Both are files this alignment produced, and the panel is the
    // last place that can say so before the confirm moves them to the parent.
    // The duplicate id is what a re-render or a replayed message would produce,
    // and one file must not become two rows.
    mocks.drafts = [draftSummary({ draft: { ...draftSummary().draft, title: "Dark mode" } })];
    mocks.messages = [
      chatMessage({ id: "m1", role: "user", attachments: [prototype] }),
      chatMessage({ id: "m2", role: "assistant", attachments: [mock, prototype] }),
    ];
    renderPage();

    expect(await screen.findByText("Reference files")).toBeTruthy();
    expect(
      screen.getByText(
        "After you confirm, these 2 files belong to the parent task. Sub-issues link them instead of uploading a copy.",
      ),
    ).toBeTruthy();
    expect(screen.getByText("prototype.png")).toBeTruthy();
    expect(screen.getByText("mock.html")).toBeTruthy();
  });

  it("says nothing when no turn ever carried a file", async () => {
    mocks.messages = [chatMessage()];
    renderPage();
    await screen.findByText("Aligning");
    expect(screen.queryByText("Reference files")).toBeNull();
  });
});
