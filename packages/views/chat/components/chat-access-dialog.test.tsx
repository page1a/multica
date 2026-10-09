import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import { chatKeys } from "@multica/core/chat/queries";
import type { ChatSession } from "@multica/core/types";
import enChat from "../../locales/en/chat.json";
import enCommon from "../../locales/en/common.json";

const server = vi.hoisted(() => ({
  mode: "project",
  putChatAccess: vi.fn(),
}));
const toastError = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/api", () => ({
  api: {
    getChatAccess: vi.fn(async () => ({
      mode: server.mode,
      visibility: server.mode === "private" ? "private" : "project",
      can_edit: true,
      has_project: true,
      shares: [],
    })),
    putChatAccess: server.putChatAccess,
  },
}));
vi.mock("@multica/core/projects", () => ({
  projectListOptions: () => ({ queryKey: ["projects"], queryFn: async () => [] }),
}));
vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members"], queryFn: async () => [] }),
}));
vi.mock("sonner", () => ({ toast: { error: toastError } }));

import { ChatAccessDialog } from "./chat-access-dialog";

const session = { id: "session-1", creator_id: "user-1", title: "Plan", project_ids: ["p1"] } as ChatSession;

// Stands in for the header's share button: it reads the same access query.
function HeaderMode() {
  const { data } = useQuery({ queryKey: chatKeys.access("ws-1", session.id), queryFn: async (): Promise<{ mode: string } | null> => null });
  return <span data-testid="header-mode">{data?.mode ?? "-"}</span>;
}

function renderDialog() {
  const qc = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false } } });
  const onOpenChange = vi.fn();
  render(
    <QueryClientProvider client={qc}>
      <I18nProvider locale="en" resources={{ en: { chat: enChat, common: enCommon } }}>
        <ChatAccessDialog session={session} open onOpenChange={onOpenChange} />
        <HeaderMode />
      </I18nProvider>
    </QueryClientProvider>,
  );
  return { onOpenChange };
}

describe("ChatAccessDialog", () => {
  beforeEach(() => {
    server.mode = "project";
    server.putChatAccess.mockReset();
    toastError.mockReset();
  });

  it("refreshes the shared access query after saving, so the change shows", async () => {
    server.putChatAccess.mockImplementation(async (_id: string, body: { mode: string }) => {
      server.mode = body.mode;
    });
    const { onOpenChange } = renderDialog();
    await waitFor(() => expect(screen.getByTestId("header-mode").textContent).toBe("project"));

    fireEvent.click(screen.getByText(enChat.sharing.private));
    fireEvent.click(screen.getByText(enChat.sharing.save));

    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    await waitFor(() => expect(screen.getByTestId("header-mode").textContent).toBe("private"));
  });

  it("says why when the server refuses the save", async () => {
    server.putChatAccess.mockRejectedValue(new Error("only the chat creator can change this"));
    const { onOpenChange } = renderDialog();
    await waitFor(() => expect(screen.getByTestId("header-mode").textContent).toBe("project"));

    fireEvent.click(screen.getByText(enChat.sharing.private));
    fireEvent.click(screen.getByText(enChat.sharing.save));

    await waitFor(() => expect(toastError).toHaveBeenCalledWith("only the chat creator can change this"));
    expect(onOpenChange).not.toHaveBeenCalledWith(false);
  });
});
