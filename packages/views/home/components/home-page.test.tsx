import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, within } from "@testing-library/react";
import type { BoardRow, InboxBoard } from "@multica/core/home";
import { renderWithI18n } from "../../test/i18n";

const push = vi.fn();
const mutate = vi.fn();
let searchParams = new URLSearchParams();
let board: InboxBoard;
let seenAt: string | null = null;
const markSeen = vi.fn();
const takeSnapshot = vi.fn();

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/auth", () => ({
  useAuthStore: (sel: (s: { user: { id: string } }) => unknown) => sel({ user: { id: "me" } }),
}));
vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    inbox: () => "/acme/inbox",
    issueDetail: (id: string) => `/acme/issues/${id}`,
  }),
}));
vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({ getActorName: (_t: string, id: string) => `name:${id}` }),
}));
vi.mock("@multica/core/issues/mutations", () => ({
  useCreateComment: () => ({ mutate, isPending: false }),
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
    useInboxBoard: () => ({ board, isLoading: false, isError: false }),
    useBoardUnreadSnapshot: (wsId: string) => {
      takeSnapshot(wsId);
      return undefined;
    },
    useDoneSeenStore: store,
  };
});
vi.mock("../../navigation", () => ({
  useNavigation: () => ({ searchParams, push, replace: vi.fn() }),
  AppLink: ({ href, children, ...rest }: { href: string; children: React.ReactNode }) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}));
vi.mock("../../inbox/components/inbox-page", () => ({
  InboxActivityPage: () => <div data-testid="activity-layer" />,
}));

import { HomePage } from "./home-page";
import { InboxPage } from "../../inbox/components/inbox-layers";

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
    at: "2026-09-26T08:00:00Z",
    timeline: [],
    unread: 0,
    children: [],
    ...over,
  };
}

beforeEach(() => {
  push.mockReset();
  mutate.mockReset();
  markSeen.mockReset();
  takeSnapshot.mockReset();
  searchParams = new URLSearchParams();
  seenAt = null;
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
    fresh: [],
    done: [
      row({ issueId: "880", lane: "done", at: "2026-09-26T10:00:00Z" }),
      row({ issueId: "879", lane: "done", at: "2026-09-26T06:00:00Z" }),
    ],
  };
});

describe("HomePage", () => {
  it("shows one row per issue in the four lanes", () => {
    renderWithI18n(<HomePage />);
    for (const lane of ["waiting", "stalled", "running", "done"]) {
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
  });

  it("reads the board on arrival and marks what was unread (DENE-901)", () => {
    board.running[0] = { ...board.running[0]!, unread: 2 };
    board.fresh = [row({ issueId: "900", lane: "fresh", unread: 1 })];
    renderWithI18n(<HomePage />);
    expect(takeSnapshot).toHaveBeenCalledWith("ws-1");
    expect(within(screen.getByTestId("board-lane-running")).getByText("2 new")).toBeInTheDocument();
    const fresh = screen.getByTestId("board-lane-fresh");
    expect(within(fresh).getByText("title 900")).toBeInTheDocument();
    expect(within(fresh).getByText("1 new")).toBeInTheDocument();
    const lanes = screen.getAllByTestId(/^board-lane-/).map((el) => el.dataset.testid);
    expect(lanes.indexOf("board-lane-fresh")).toBe(lanes.indexOf("board-lane-done") - 1);
    expect(screen.getAllByTestId("board-row-unread")).toHaveLength(2);
  });

  it("hides the new-activity lane when nothing is new", () => {
    renderWithI18n(<HomePage />);
    expect(screen.queryByTestId("board-lane-fresh")).toBeNull();
    expect(screen.queryByTestId("board-row-unread")).toBeNull();
  });

  it("opens the issue when a row is clicked", () => {
    renderWithI18n(<HomePage />);
    fireEvent.click(screen.getByTestId("board-row-stalled").firstElementChild!);
    expect(push).toHaveBeenCalledWith("/acme/issues/871");
  });

  it("expands a stalled row into its timeline without leaving the page", () => {
    renderWithI18n(<HomePage />);
    fireEvent.click(screen.getByRole("button", { name: "Why" }));
    expect(screen.getByTestId("board-timeline")).toHaveTextContent("PR merged：#370");
    expect(push).not.toHaveBeenCalled();
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
