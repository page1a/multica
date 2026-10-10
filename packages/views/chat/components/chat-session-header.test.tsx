import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import type { ChatSession } from "@multica/core/types";
import enChat from "../../locales/en/chat.json";
import enCommon from "../../locales/en/common.json";

const updateMutate = vi.hoisted(() => vi.fn());
const handoffMutate = vi.hoisted(() => vi.fn());
const copyTextMock = vi.hoisted(() => vi.fn(async () => true));
const accessRef = vi.hoisted(() => ({
  current: { mode: "project", visibility: "project", can_edit: true, has_project: true, shares: [] } as {
    mode: string;
    visibility: string;
    can_edit: boolean;
    has_project: boolean;
    shares: unknown[];
  },
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/api", () => ({
  api: { getChatAccess: vi.fn(async () => accessRef.current) },
}));
vi.mock("@multica/ui/lib/clipboard", () => ({ copyText: copyTextMock }));
vi.mock("./chat-access-dialog", () => ({
  ChatAccessDialog: ({ open }: { open: boolean }) =>
    open ? <div data-testid="chat-access-dialog" /> : null,
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    agentDetail: (id: string) => `/agents/${id}`,
    chatSession: (id: string) => `/acme/chat/${id}`,
  }),
}));

vi.mock("@multica/core/chat/mutations", () => ({
  useUpdateChatSession: () => ({ mutate: updateMutate }),
  useDeleteChatSession: () => ({ mutate: vi.fn() }),
  useSetChatSessionArchived: () => ({ mutate: vi.fn() }),
  useHandoffChatSession: () => ({ mutate: handoffMutate, isPending: false }),
  useSetChatTicket: () => ({ mutate: vi.fn(), isPending: false }),
}));

vi.mock("@multica/core/chat", () => ({
  useChatStore: (selector: (state: { setActiveSession: () => void }) => unknown) =>
    selector({ setActiveSession: vi.fn() }),
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (s: { user: { id: string } }) => unknown) =>
    selector({ user: { id: "user-1" } }),
}));

vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: () => null,
}));

vi.mock("../../navigation", () => ({
  AppLink: ({ children }: { children: React.ReactNode }) => <a>{children}</a>,
  useNavigation: () => ({
    getShareableUrl: (path: string) => `https://example.test${path}`,
  }),
}));

import { ChatSessionHeader } from "./chat-session-header";

const TEST_RESOURCES = { en: { chat: enChat, common: enCommon } };
const RENAME_LABEL = enChat.header.rename;
const MORE_LABEL = enChat.list.row_actions_aria;
const OUTSIDE_LABEL = "Outside control";

const session: ChatSession = {
  id: "session-1",
  workspace_id: "ws-1",
  agent_id: "agent-1",
  creator_id: "user-1",
  title: "Original title",
  status: "active",
  has_unread: false,
  unread_count: 0,
  last_message: null,
  pinned: false,
  created_at: new Date(0).toISOString(),
  updated_at: new Date(0).toISOString(),
};

function startRename(): HTMLInputElement {
  render(
    <>
      <QueryClientProvider client={new QueryClient()}>
        <I18nProvider locale="en" resources={TEST_RESOURCES}>
          <ChatSessionHeader session={session} agent={null} />
        </I18nProvider>
      </QueryClientProvider>
      <button type="button">{OUTSIDE_LABEL}</button>
    </>,
  );
  // The title is plain text; rename opens only from the ⋯ menu.
  expect(screen.queryByTitle(RENAME_LABEL)).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: MORE_LABEL }));
  fireEvent.click(screen.getByRole("menuitem", { name: RENAME_LABEL }));
  return screen.getByRole("textbox", { name: RENAME_LABEL });
}

describe("ChatSessionHeader rename keyboard behavior", () => {
  beforeEach(() => {
    updateMutate.mockReset();
  });

  it.each([
    ["standard composition signal", { isComposing: true, keyCode: 13 }],
    ["Safari composition signal", { isComposing: false, keyCode: 229 }],
  ])("keeps editing when Enter carries the %s", (_name, eventInit) => {
    const input = startRename();
    fireEvent.change(input, { target: { value: "yanjiu" } });

    fireEvent.keyDown(input, { key: "Enter", ...eventInit });

    expect(updateMutate).not.toHaveBeenCalled();
    expect(input).toBeInTheDocument();
    expect(input).toHaveValue("yanjiu");
  });

  it("submits once on a normal Enter", () => {
    const input = startRename();
    fireEvent.change(input, { target: { value: "Renamed chat" } });

    fireEvent.keyDown(input, { key: "Enter", isComposing: false, keyCode: 13 });

    expect(updateMutate).toHaveBeenCalledTimes(1);
    expect(updateMutate).toHaveBeenCalledWith({
      sessionId: "session-1",
      title: "Renamed chat",
    });
    expect(screen.queryByRole("textbox", { name: RENAME_LABEL })).not.toBeInTheDocument();
  });

  it("defers blur submission until an active composition ends", () => {
    const input = startRename();
    const outside = screen.getByRole("button", { name: OUTSIDE_LABEL });
    fireEvent.change(input, { target: { value: "yanjiu" } });
    fireEvent.compositionStart(input);

    act(() => {
      outside.focus();
    });

    expect(updateMutate).not.toHaveBeenCalled();
    expect(input).toBeInTheDocument();
    expect(outside).toHaveFocus();

    fireEvent.change(input, { target: { value: "研究" } });
    fireEvent.compositionEnd(input);

    expect(updateMutate).toHaveBeenCalledTimes(1);
    expect(updateMutate).toHaveBeenCalledWith({
      sessionId: "session-1",
      title: "研究",
    });
    expect(screen.queryByRole("textbox", { name: RENAME_LABEL })).not.toBeInTheDocument();
  });

  it("closes without saving a partial value when compositionend does not arrive", async () => {
    const input = startRename();
    const outside = screen.getByRole("button", { name: OUTSIDE_LABEL });
    fireEvent.change(input, { target: { value: "yanjiu" } });
    fireEvent.compositionStart(input);

    act(() => {
      outside.focus();
    });

    await waitFor(() => {
      expect(screen.queryByRole("textbox", { name: RENAME_LABEL })).not.toBeInTheDocument();
    });
    expect(updateMutate).not.toHaveBeenCalled();
    expect(screen.getByText("Original title")).toBeInTheDocument();
  });

  it("still submits the current value when focus moves outside", () => {
    const input = startRename();
    const outside = screen.getByRole("button", { name: OUTSIDE_LABEL });
    fireEvent.change(input, { target: { value: "Blurred title" } });

    act(() => {
      outside.focus();
    });

    expect(updateMutate).toHaveBeenCalledTimes(1);
    expect(updateMutate).toHaveBeenCalledWith({
      sessionId: "session-1",
      title: "Blurred title",
    });
    expect(outside).toHaveFocus();
  });

  it.each([
    ["standard composition signal", { isComposing: true, keyCode: 27 }],
    ["Safari composition signal", { isComposing: false, keyCode: 229 }],
  ])("keeps editing when Escape carries the %s", (_name, eventInit) => {
    const input = startRename();
    fireEvent.change(input, { target: { value: "yanjiu" } });

    fireEvent.keyDown(input, { key: "Escape", ...eventInit });

    expect(updateMutate).not.toHaveBeenCalled();
    expect(input).toBeInTheDocument();
    expect(input).toHaveValue("yanjiu");
  });

  it("still cancels the edit on Escape", () => {
    const input = startRename();
    fireEvent.change(input, { target: { value: "Discard me" } });

    fireEvent.keyDown(input, { key: "Escape" });

    expect(updateMutate).not.toHaveBeenCalled();
    expect(screen.queryByRole("textbox", { name: RENAME_LABEL })).not.toBeInTheDocument();
    expect(screen.getByText("Original title")).toBeInTheDocument();
  });
});

describe("ChatSessionHeader sharing entry (DENE-1214)", () => {
  function renderHeader(over: Partial<ChatSession> = {}) {
    render(
      <QueryClientProvider client={new QueryClient()}>
        <I18nProvider locale="en" resources={TEST_RESOURCES}>
          <ChatSessionHeader session={{ ...session, ...over }} agent={null} />
        </I18nProvider>
      </QueryClientProvider>,
    );
  }

  beforeEach(() => {
    copyTextMock.mockClear();
  });

  it("shows who can see the chat and opens the access editor for its creator", async () => {
    accessRef.current = { mode: "project", visibility: "project", can_edit: true, has_project: true, shares: [] };
    renderHeader({ visibility: "project" });
    const trigger = screen.getByTestId("chat-share-trigger");
    expect(trigger).toHaveTextContent(enChat.sharing.trigger.project);
    await waitFor(() => expect(trigger).not.toHaveAttribute("aria-disabled"));
    fireEvent.click(trigger);
    expect(screen.getByTestId("chat-access-dialog")).toBeInTheDocument();
  });

  it("keeps the entry inert when the server says the viewer cannot edit", async () => {
    accessRef.current = { mode: "workspace", visibility: "workspace", can_edit: false, has_project: true, shares: [] };
    renderHeader({ creator_id: "someone-else" });
    const trigger = screen.getByTestId("chat-share-trigger");
    await waitFor(() => expect(trigger).toHaveTextContent(enChat.sharing.trigger.workspace));
    expect(trigger).toHaveAttribute("aria-disabled", "true");
    fireEvent.click(trigger);
    expect(screen.queryByTestId("chat-access-dialog")).not.toBeInTheDocument();
  });

  it("warns before copying a private chat's link and can switch to the editor", async () => {
    accessRef.current = { mode: "private", visibility: "private", can_edit: true, has_project: false, shares: [] };
    renderHeader({ visibility: "private" });
    await waitFor(() =>
      expect(screen.getByTestId("chat-share-trigger")).not.toHaveAttribute("aria-disabled"),
    );
    fireEvent.click(screen.getByTestId("chat-copy-link"));
    expect(await screen.findByTestId("private-link-prompt")).toBeInTheDocument();
    expect(copyTextMock).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: enCommon.share_guide.private_link.change_scope }));
    expect(screen.getByTestId("chat-access-dialog")).toBeInTheDocument();
  });

  it("copies a shared chat's link straight away", async () => {
    accessRef.current = { mode: "project", visibility: "project", can_edit: true, has_project: true, shares: [] };
    renderHeader({ visibility: "project" });
    await waitFor(() =>
      expect(screen.getByTestId("chat-share-trigger")).not.toHaveAttribute("aria-disabled"),
    );
    fireEvent.click(screen.getByTestId("chat-copy-link"));
    await waitFor(() => expect(copyTextMock).toHaveBeenCalledWith("https://example.test/acme/chat/session-1"));
    expect(screen.queryByTestId("private-link-prompt")).not.toBeInTheDocument();
  });
});

describe("ChatSessionHeader handoff (DENE-1350)", () => {
  const agents = [
    { id: "agent-1", name: "Current" },
    { id: "agent-2", name: "Gohan" },
  ] as unknown as import("@multica/core/types").Agent[];

  function renderHeader(over: Partial<ChatSession> = {}) {
    render(
      <QueryClientProvider client={new QueryClient()}>
        <I18nProvider locale="en" resources={TEST_RESOURCES}>
          <ChatSessionHeader session={{ ...session, ...over }} agent={null} handoffAgents={agents} />
        </I18nProvider>
      </QueryClientProvider>,
    );
  }

  beforeEach(() => handoffMutate.mockReset());

  it("lists the chat's own agent first, then the others", () => {
    renderHeader();
    fireEvent.click(screen.getByTestId("chat-handoff-trigger"));
    const items = screen.getAllByRole("menuitem").map((el) => el.textContent);
    expect(items.slice(0, 2)).toEqual(["Current", "Gohan"]);
    fireEvent.click(screen.getByRole("menuitem", { name: "Current" }));
    expect(handoffMutate).toHaveBeenCalledWith({ sessionId: "session-1", to: "agent-1" }, expect.anything());
  });

  it("offers no handoff to someone who does not own the chat", () => {
    renderHeader({ creator_id: "someone-else" });
    expect(screen.queryByTestId("chat-handoff-trigger")).not.toBeInTheDocument();
  });
});
