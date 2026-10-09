import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import { chatKeys } from "@multica/core/chat/queries";
import enChat from "../../locales/en/chat.json";
import enCommon from "../../locales/en/common.json";

const api = vi.hoisted(() => ({
  getChatVisibilityNotice: vi.fn(),
  dismissChatVisibilityNotice: vi.fn(),
  makeChatSessionsPrivate: vi.fn(),
}));
const toastError = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/api", () => ({ api }));
vi.mock("sonner", () => ({ toast: { error: toastError } }));

// Stands in for the chat header's share button: it reads the chat's access query.
const readAccess = vi.fn(async () => ({ mode: "project" }));
function HeaderAccess() {
  useQuery({ queryKey: chatKeys.access("ws-1", "s1"), queryFn: readAccess });
  return null;
}

async function renderNotice() {
  // The notice claims itself once per module load; reload it for every test.
  vi.resetModules();
  const { ChatVisibilityNotice } = await import("./chat-visibility-notice");
  const qc = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <I18nProvider locale="en" resources={{ en: { chat: enChat, common: enCommon } }}>
        <ChatVisibilityNotice />
        <HeaderAccess />
      </I18nProvider>
    </QueryClientProvider>,
  );
  await screen.findByText(enChat.notice.title);
}

describe("ChatVisibilityNotice", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    api.getChatVisibilityNotice.mockResolvedValue({
      pending: true,
      count: 1,
      sessions: [{ id: "s1", title: "Plan" }],
    });
    api.dismissChatVisibilityNotice.mockResolvedValue(undefined);
    api.makeChatSessionsPrivate.mockResolvedValue({ updated: 1 });
  });

  it("refreshes each chat's access after making it private", async () => {
    await renderNotice();
    await waitFor(() => expect(readAccess).toHaveBeenCalledTimes(1));

    fireEvent.click(screen.getByText(enChat.notice.make_private));

    await waitFor(() => expect(api.makeChatSessionsPrivate).toHaveBeenCalledWith(["s1"]));
    await waitFor(() => expect(readAccess).toHaveBeenCalledTimes(2));
  });

  it("says why when making chats private is refused", async () => {
    api.makeChatSessionsPrivate.mockRejectedValue(new Error("only the chat creator can change this"));
    await renderNotice();

    fireEvent.click(screen.getByText(enChat.notice.make_private));

    await waitFor(() => expect(toastError).toHaveBeenCalledWith("only the chat creator can change this"));
    expect(screen.getByText(enChat.notice.title)).toBeInTheDocument();
  });

  it("says why when dismissing fails", async () => {
    api.dismissChatVisibilityNotice.mockRejectedValue(new Error("server unavailable"));
    await renderNotice();

    fireEvent.click(screen.getByText(enChat.notice.dismiss));

    await waitFor(() => expect(toastError).toHaveBeenCalledWith("server unavailable"));
  });
});
