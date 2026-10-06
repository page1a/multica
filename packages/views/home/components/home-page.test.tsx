import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import type { BoardRow, InboxBoard } from "@multica/core/home";
import { renderWithI18n } from "../../test/i18n";

const push = vi.fn();
const mutate = vi.fn();
let searchParams = new URLSearchParams();
let board: InboxBoard;
let seenAt: string | null = null;
const markSeen = vi.fn();
const readBoard = vi.fn();
const PROJECTS = [
  { id: "p-1", title: "Multica 魔改" },
  { id: "p-2", title: "AI100" },
] as unknown as import("@multica/core/types").Project[];
let boardProjectId: string | null = null;
const setBoardProject = vi.fn();
const invalidateQueries = vi.fn();
const updateIssue = vi.fn(() => Promise.resolve({}));
const batchUpdateIssues = vi.fn(() => Promise.resolve({ updated: 1 }));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/auth", () => ({
  useAuthStore: (sel: (s: { user: { id: string } }) => unknown) => sel({ user: { id: "me" } }),
}));
vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    inbox: () => "/acme/inbox",
    chatWithPrompt: (prompt: string) => `/acme/chat?prompt=${encodeURIComponent(prompt)}`,
    issueDetail: (id: string) => `/acme/issues/${id}`,
  }),
}));
vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({ getActorName: (_t: string, id: string) => `name:${id}` }),
}));
vi.mock("@multica/core/issues/mutations", () => ({
  useCreateComment: () => ({ mutate, isPending: false }),
  useUpdateIssue: () => ({ mutateAsync: updateIssue, isPending: false }),
  useBatchUpdateIssues: () => ({ mutateAsync: batchUpdateIssues, isPending: false }),
}));
vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQueryClient: () => ({ invalidateQueries }),
}));
vi.mock("@multica/core/home", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/home")>();
  const store = Object.assign(
    (sel: (s: { seenAt: Record<string, string>; markSeen: typeof markSeen }) => unknown) =>
      sel({ seenAt: seenAt ? { "ws-1": seenAt } : {}, markSeen }),
    { getState: () => ({ seenAt: seenAt ? { "ws-1": seenAt } : {}, markSeen }) },
  );
  return {
    ...actual,
    useBoardProject: () => ({
      projectId: boardProjectId,
      project: PROJECTS.find((p) => p.id === boardProjectId) ?? null,
      projects: PROJECTS,
      setProjectId: setBoardProject,
    }),
    useInboxBoard: (wsId: string, opts?: { projectId?: string | null }) => {
      readBoard(wsId, opts?.projectId ?? null);
      return { board, isLoading: false, isError: false };
    },
    useDoneSeenStore: store,
  };
});
vi.mock("../../navigation", () => ({
  useNavigation: () => ({ searchParams, push, replace: vi.fn() }),
  resolveClickIntent: (e: { metaKey?: boolean; ctrlKey?: boolean }) =>
    e.metaKey || e.ctrlKey ? "new-tab" : "push",
  AppLink: ({ href, children, ...rest }: { href: string; children: React.ReactNode }) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}));
vi.mock("../../inbox/components/inbox-page", () => ({
  InboxActivityPage: ({ merged }: { merged?: boolean }) => (
    <div data-testid={merged ? "merged-inbox" : "activity-layer"} />
  ),
}));
let compact = true;
vi.mock("@multica/ui/hooks/use-mobile", () => ({
  useIsCompact: () => compact,
  useIsMobile: () => false,
}));

import { HomePage, InboxBoardLanes, type BoardLinking } from "./home-page";
import { InboxPage } from "../../inbox/components/inbox-layers";
import { IssuePeekActionsContext } from "../../issues/surface/peek-context";

function row(over: Partial<BoardRow> & { issueId: string; lane: BoardRow["lane"] }): BoardRow {
  return {
    identifier: `DENE-${over.issueId}`,
    title: `title ${over.issueId}`,
    parentIssueId: null,
    kind: over.lane,
    stuckKind: "",
    reason: "",
    before: "",
    from: null,
    fromName: "",
    next: null,
    nextName: "",
    at: "2026-09-26T08:00:00Z",
    timeline: [],
    unread: 0,
    children: [],
    ...over,
  };
}

beforeEach(() => {
  boardProjectId = null;
  setBoardProject.mockClear();
  push.mockReset();
  mutate.mockReset();
  markSeen.mockReset();
  readBoard.mockReset();
  searchParams = new URLSearchParams();
  seenAt = null;
  compact = true;
  board = {
    waiting: [
      row({
        issueId: "822",
        lane: "waiting",
        kind: "needs_human",
        reason: "Answer three questions first",
        fromName: "Goku",
      }),
    ],
    stalled: [
      row({
        issueId: "871",
        lane: "stalled",
        kind: "stalled_unclosed",
        stuckKind: "no_close",
        before: "PR merged",
        next: { type: "agent", id: "a-1" },
        timeline: [{ at: "2026-09-26T07:00:00Z", kind: "pr_merged", detail: "#370" }],
      }),
    ],
    running: [row({ issueId: "882", lane: "running", next: { type: "agent", id: "a-2" } })],
    blocked: [],
    todo: [row({ issueId: "890", lane: "todo", kind: "todo" })],
    fresh: [],
    done: [
      row({ issueId: "880", lane: "done", at: "2026-09-26T10:00:00Z" }),
      row({ issueId: "879", lane: "done", at: "2026-09-26T06:00:00Z" }),
    ],
  };
});

describe("HomePage project filter (DENE-1019)", () => {
  it("with every project shown, asks the AI about the whole inbox", () => {
    renderWithI18n(<HomePage />);
    expect(readBoard).toHaveBeenCalledWith("ws-1", null);
    expect(screen.getByTestId("board-project-filter")).toHaveTextContent("All projects");
    const ask = screen.getByTestId("board-ask-ai");
    expect(ask).toHaveTextContent("Walk me through it");
    expect(decodeURIComponent(ask.getAttribute("href") ?? "")).not.toContain("--project");
  });

  it("picking a project narrows the board and remembers the choice", () => {
    renderWithI18n(<HomePage />);
    fireEvent.click(within(screen.getByTestId("board-project-filter")).getByRole("button"));
    fireEvent.click(screen.getByText("AI100"));
    expect(setBoardProject).toHaveBeenCalledWith("p-2");
  });

  it("with a project picked, reads that project and sends the AI to the same one", () => {
    boardProjectId = "p-1";
    renderWithI18n(<HomePage />);
    expect(readBoard).toHaveBeenCalledWith("ws-1", "p-1");
    expect(screen.getByTestId("board-project-filter")).toHaveTextContent("Multica 魔改");
    const ask = screen.getByTestId("board-ask-ai");
    expect(ask).toHaveTextContent("Walk me through this project");
    const prompt = decodeURIComponent(ask.getAttribute("href") ?? "");
    expect(prompt).toContain("multica inbox board --project p-1");
    expect(prompt).toContain("Multica 魔改");
  });
});

describe("HomePage", () => {
  it("offers waiting-row quick status actions and removes the row immediately", async () => {
    board.waiting[0] = {
      ...board.waiting[0]!,
      status: "in_review",
    };
    renderWithI18n(<InboxBoardLanes board={board} isLoading={false} isError={false} />);
    fireEvent.click(screen.getByRole("button", { name: "Complete" }));
    expect(updateIssue).toHaveBeenCalledWith({ id: "822", status: "done" });
    await waitFor(() => expect(within(screen.getByTestId("board-lane-waiting")).getByText("Nothing is waiting on you")).toBeInTheDocument());
  });

  it("batch-updates selected waiting rows", async () => {
    board.waiting = [
      ...board.waiting,
      row({ issueId: "823", lane: "waiting", kind: "needs_human", status: "in_review" }),
    ];
    renderWithI18n(<InboxBoardLanes board={board} isLoading={false} isError={false} />);
    fireEvent.click(screen.getAllByTestId("waiting-row-checkbox")[0]!);
    fireEvent.click(screen.getAllByTestId("waiting-row-checkbox")[1]!);
    fireEvent.click(within(screen.getByTestId("waiting-selection-toolbar")).getByRole("button", { name: "Complete" }));
    expect(batchUpdateIssues).toHaveBeenCalledWith({ ids: ["822", "823"], updates: { status: "done" } });
    await waitFor(() => expect(screen.queryByTestId("waiting-selection-toolbar")).toBeNull());
  });

  it("returns selected waiting rows to to-do", () => {
    board.waiting = [
      ...board.waiting,
      row({ issueId: "823", lane: "waiting", kind: "needs_human" }),
    ];
    renderWithI18n(<InboxBoardLanes board={board} isLoading={false} isError={false} />);
    fireEvent.click(screen.getAllByTestId("waiting-row-checkbox")[0]!);
    fireEvent.click(screen.getAllByTestId("waiting-row-checkbox")[1]!);
    fireEvent.click(within(screen.getByTestId("waiting-selection-toolbar")).getByRole("button", { name: "Return to to-do" }));
    expect(batchUpdateIssues).toHaveBeenCalledWith({ ids: ["822", "823"], updates: { status: "todo" } });
  });

  it("names the agent that will be woken when returning work to to-do", () => {
    board.waiting[0] = {
      ...board.waiting[0]!,
      next: { type: "agent", id: "agent-1" },
      nextName: "Trunks",
    };
    renderWithI18n(<InboxBoardLanes board={board} isLoading={false} isError={false} />);
    fireEvent.click(screen.getByRole("button", { name: "More actions" }));
    expect(screen.getByText("Return to to-do (wake Trunks)")).toBeInTheDocument();
  });

  it("shows one row per issue in the five lanes", () => {
    renderWithI18n(<HomePage />);
    for (const lane of ["waiting", "stalled", "running", "todo", "done"]) {
      expect(screen.getByTestId(`board-lane-${lane}`)).toBeInTheDocument();
    }
    const waiting = screen.getByTestId("board-lane-waiting");
    expect(within(waiting).getByText("Answer three questions first")).toBeInTheDocument();
    expect(within(waiting).getByText("Goku → you")).toBeInTheDocument();

    const stalled = screen.getByTestId("board-lane-stalled");
    expect(
      within(stalled).getByText("The run ended and the issue was not closed out"),
    ).toBeInTheDocument();
    expect(within(stalled).getByText("Before: PR merged")).toBeInTheDocument();
    expect(within(stalled).getByText("Next: name:a-1")).toBeInTheDocument();

    expect(within(screen.getByTestId("board-lane-running")).getByText("name:a-2")).toBeInTheDocument();

    // DENE-975: the viewer's todo work sits right after running.
    expect(within(screen.getByTestId("board-lane-todo")).getByText("title 890")).toBeInTheDocument();
    const lanes = screen.getAllByTestId(/^board-lane-/).map((el) => el.dataset.testid);
    expect(lanes.indexOf("board-lane-todo")).toBe(lanes.indexOf("board-lane-running") + 1);
  });

  it("reads the board on arrival and marks what was unread (DENE-901)", () => {
    board.running[0] = { ...board.running[0]!, unread: 2 };
    board.fresh = [row({ issueId: "900", lane: "fresh", unread: 1 })];
    renderWithI18n(<HomePage />);
    expect(readBoard).toHaveBeenCalledWith("ws-1", null);
    expect(within(screen.getByTestId("board-lane-running")).getByText("2 new")).toBeInTheDocument();
    const fresh = screen.getByTestId("board-lane-fresh");
    expect(within(fresh).getByText("title 900")).toBeInTheDocument();
    expect(within(fresh).getByText("1 new")).toBeInTheDocument();
    const lanes = screen.getAllByTestId(/^board-lane-/).map((el) => el.dataset.testid);
    expect(lanes.indexOf("board-lane-fresh")).toBe(lanes.indexOf("board-lane-done") - 1);
    expect(screen.getAllByTestId("board-row-unread")).toHaveLength(2);
  });

  it("shows tickets waiting on other tickets in their own lane, not as waiting on you (DENE-1409)", () => {
    board.blocked = [
      row({
        issueId: "1396",
        lane: "blocked",
        kind: "blocked",
        reason: "Resume once DENE-1393 merges",
        next: { type: "issue", id: "DENE-1393" },
      }),
    ];
    renderWithI18n(<HomePage />);
    const blocked = screen.getByTestId("board-lane-blocked");
    expect(within(blocked).getByText("Resume once DENE-1393 merges")).toBeInTheDocument();
    expect(within(blocked).getByText("Waiting on DENE-1393")).toBeInTheDocument();
    expect(within(screen.getByTestId("board-lane-waiting")).queryByText("title 1396")).toBeNull();
    const lanes = screen.getAllByTestId(/^board-lane-/).map((el) => el.dataset.testid);
    expect(lanes.indexOf("board-lane-blocked")).toBe(lanes.indexOf("board-lane-running") + 1);
  });

  it("hides the waiting-on-tickets lane when nothing waits", () => {
    renderWithI18n(<HomePage />);
    expect(screen.queryByTestId("board-lane-blocked")).toBeNull();
  });

  it("hides the new-activity lane when nothing is new", () => {
    renderWithI18n(<HomePage />);
    expect(screen.queryByTestId("board-lane-fresh")).toBeNull();
    expect(screen.queryByTestId("board-row-unread")).toBeNull();
  });

  it("opens the issue preview when a row is clicked", () => {
    renderWithI18n(<HomePage />);
    fireEvent.click(screen.getByTestId("board-row-stalled").firstElementChild!);
    expect(push).not.toHaveBeenCalled();
    expect(screen.getByTestId("board-row-stalled")).toHaveAttribute("data-highlighted");
  });

  it("publishes the board order as one cross-lane preview sequence", () => {
    const peek = {
      open: vi.fn(),
      toggle: vi.fn(),
      close: vi.fn(),
      publishColumns: vi.fn(),
    };
    renderWithI18n(
      <IssuePeekActionsContext.Provider value={peek}>
        <InboxBoardLanes board={board} isLoading={false} isError={false} />
      </IssuePeekActionsContext.Provider>,
    );
    expect(peek.publishColumns).toHaveBeenCalledWith([
      ["822", "871", "882", "890", "880", "879"],
    ]);
    fireEvent.click(screen.getByTestId("board-row-running").firstElementChild!);
    expect(peek.open).toHaveBeenCalledWith("882");
  });

  it("expands a stalled row into its timeline without leaving the page", () => {
    renderWithI18n(<HomePage />);
    fireEvent.click(screen.getByRole("button", { name: "Why" }));
    expect(screen.getByTestId("board-timeline")).toHaveTextContent("PR merged：#370");
    expect(push).not.toHaveBeenCalled();
  });

  it("keeps expanded child rows readable on narrow screens", () => {
    board.todo[0] = {
      ...board.todo[0]!,
      children: [row({ issueId: "891", lane: "todo" })],
    };
    renderWithI18n(<InboxBoardLanes board={board} isLoading={false} isError={false} />);

    fireEvent.click(screen.getByRole("button", { name: "1 sub-issue" }));

    const childRow = screen.getByText("title 891").closest('[role="link"]');
    expect(childRow).toHaveClass("pl-4", "sm:pl-8", "grid-cols-[auto_minmax(0,1fr)_auto]");
    expect(childRow?.parentElement?.parentElement?.parentElement).toHaveClass("px-4", "sm:pl-[8.75rem]");
  });

  it("replies to a waiting row in place", () => {
    renderWithI18n(<HomePage />);
    const waiting = screen.getByTestId("board-lane-waiting");
    fireEvent.click(within(waiting).getByRole("button", { name: "Reply" }));
    fireEvent.change(within(waiting).getByRole("textbox"), { target: { value: "go ahead" } });
    fireEvent.submit(within(waiting).getByRole("textbox").closest("form")!);
    expect(mutate).toHaveBeenCalledWith({ content: "go ahead" }, expect.anything());
  });

  it("folds done rows the viewer already saw and moves the mark", () => {
    seenAt = "2026-09-26T08:00:00Z";
    renderWithI18n(<HomePage />);
    const done = screen.getByTestId("board-lane-done");
    expect(within(done).getByText("title 880")).toBeInTheDocument();
    expect(within(done).queryByText("title 879")).toBeNull();
    fireEvent.click(within(done).getByRole("button", { name: "1 already seen" }));
    expect(within(done).getByText("title 879")).toBeInTheDocument();
    expect(markSeen).toHaveBeenCalledWith("ws-1", expect.any(String));
  });
});

describe("InboxPage layers", () => {
  it("shows the board by default", () => {
    renderWithI18n(<InboxPage />);
    expect(screen.getByTestId("board-lane-waiting")).toBeInTheDocument();
  });

  it.each(["layer=activity", "issue=issue-1", "view=archived"])(
    "keeps %s links on the activity list",
    (query) => {
      searchParams = new URLSearchParams(query);
      renderWithI18n(<InboxPage />);
      expect(screen.getByTestId("activity-layer")).toBeInTheDocument();
    },
  );
});

describe("InboxPage on wide screens (DENE-1004)", () => {
  it.each(["", "layer=activity", "issue=issue-1"])("shows the merged page for %s", (query) => {
    compact = false;
    searchParams = new URLSearchParams(query);
    renderWithI18n(<InboxPage />);
    expect(screen.getByTestId("merged-inbox")).toBeInTheDocument();
  });
});

describe("InboxBoardLanes linked to the list (DENE-1004)", () => {
  function linked(over: Partial<BoardLinking> = {}): BoardLinking {
    return { activeLane: null, onSelectIssue: vi.fn(), onToggleLane: vi.fn(), ...over };
  }

  it("opens rows in place instead of navigating away", () => {
    const linking = linked();
    renderWithI18n(<InboxBoardLanes board={board} isLoading={false} isError={false} linking={linking} />);
    fireEvent.click(screen.getByTestId("board-row-stalled").firstElementChild!);
    fireEvent.click(screen.getByText("title 822"));
    expect(linking.onSelectIssue).toHaveBeenNthCalledWith(1, "871");
    expect(linking.onSelectIssue).toHaveBeenNthCalledWith(2, "822");
    expect(push).not.toHaveBeenCalled();
  });

  it("filters by lane from the heading and marks the highlighted row", () => {
    const linking = linked({ activeLane: "running", highlightIssueId: "882" });
    renderWithI18n(<InboxBoardLanes board={board} isLoading={false} isError={false} linking={linking} />);
    const heading = screen.getByTestId("board-lane-filter-running");
    expect(heading).toHaveAttribute("aria-pressed", "true");
    fireEvent.click(screen.getByTestId("board-lane-filter-waiting"));
    expect(linking.onToggleLane).toHaveBeenCalledWith("waiting");
    expect(screen.getByTestId("board-row-running")).toHaveAttribute("data-highlighted");
  });

  it("unfolds seen done rows when the highlighted issue is among them", () => {
    seenAt = "2026-09-26T08:00:00Z";
    renderWithI18n(
      <InboxBoardLanes board={board} isLoading={false} isError={false} linking={linked({ highlightIssueId: "879" })} />,
    );
    expect(within(screen.getByTestId("board-lane-done")).getByText("title 879")).toBeInTheDocument();
  });
});
