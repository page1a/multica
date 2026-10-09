import { fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ChatTicket } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { ChatReportBar } from "./chat-report-bar";

const tickets = vi.hoisted(() => ({ current: [] as ChatTicket[] }));

vi.mock("@multica/core/chat/queries", () => ({
  chatTicketsOptions: (wsId: string, id: string) => ({
    queryKey: ["chat", wsId, "tickets", id],
    queryFn: async () => ({ chat_session_id: id, tickets: tickets.current }),
  }),
}));
vi.mock("@multica/core/issue-statuses", () => ({
  useIssueStatuses: () => ({ labelOf: (key: string) => key }),
}));
vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({ issueDetail: (id: string) => `/ws/issues/${id}` }),
}));
vi.mock("../../navigation", () => ({
  AppLink: ({ href, children, ...rest }: { href: string; children: React.ReactNode }) => (
    <a href={href} {...rest}>{children}</a>
  ),
}));
vi.mock("@multica/ui/hooks/use-mobile", () => ({ useIsMobile: () => false }));

const now = Date.now();
const iso = (msAgo: number) => new Date(now - msAgo).toISOString();

function ticket(id: string, title: string, rest: Partial<ChatTicket>): ChatTicket {
  return {
    id, identifier: id, title, status: "todo", priority: "medium",
    assignee_type: null, assignee_id: null,
    created_at: iso(3 * 3_600_000), updated_at: iso(0),
    changed_at: iso(3 * 3_600_000), phase: "in_progress", needs_you: false,
    ...rest,
  };
}

function renderBar(projectTitle = "Alpha", onHear = vi.fn()) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  renderWithI18n(
    <QueryClientProvider client={qc}>
      <ChatReportBar wsId="ws" userId="u1" sessionId="chat-a" projectTitle={projectTitle} disabled={false} onHear={onHear} />
    </QueryClientProvider>,
  );
  return onHear;
}

describe("ChatReportBar", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("stays hidden while the chat has opened no tickets", async () => {
    tickets.current = [];
    renderBar();
    await waitFor(() => expect(screen.queryByText(/issues? from this chat/)).toBeNull());
  });

  it("lists the chat's tickets with fresh moves first and asks for the report", async () => {
    // The window was last opened 30 minutes ago: only the 10-minute-old move is fresh.
    localStorage.setItem("multica:chat-report-opened:u1:chat-a", iso(30 * 60_000));
    tickets.current = [
      ticket("DENE-1", "Old move", { status: "done", from_status: "todo", changed_at: iso(50 * 60_000), phase: "done" }),
      ticket("DENE-2", "Never moved", {}),
      ticket("DENE-3", "Fresh move", { status: "in_review", from_status: "in_progress", changed_at: iso(10 * 60_000), phase: "waiting_you", needs_you: true }),
    ];
    const onHear = renderBar();

    const trigger = await screen.findByText(/3 issues from this chat · 1 just changed/);
    expect(screen.getByText(/1 need you/)).toBeTruthy();
    fireEvent.click(trigger);

    const rows = await screen.findAllByRole("link");
    expect(rows.map((r) => r.getAttribute("href"))).toEqual(["/ws/issues/DENE-3", "/ws/issues/DENE-2", "/ws/issues/DENE-1"]);
    expect(rows[0]?.textContent).toContain("Just changed");
    expect(rows[0]?.textContent).toContain("in_progress → in_review");
    expect(rows[1]?.textContent).not.toContain("Just changed");
    expect(rows[2]?.textContent).toContain("todo → done");

    fireEvent.click(screen.getAllByRole("button", { name: "Hear report" })[0]!);
    expect(onHear).toHaveBeenCalledWith(expect.stringContaining('"Alpha"'));
  });

  it("hides the report button when the chat has no project", async () => {
    tickets.current = [ticket("DENE-1", "Only", {})];
    renderBar("");
    await screen.findByText(/1 issue from this chat/);
    expect(screen.queryByRole("button", { name: "Hear report" })).toBeNull();
  });
});
