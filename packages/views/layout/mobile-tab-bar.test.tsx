import type React from "react";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { renderWithI18n } from "../test/i18n";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { MobileTabBar } from "./mobile-tab-bar";
import { isTextEntry, useTextEntryFocused } from "./use-is-phone";

const state = vi.hoisted(() => ({
  pathname: "/acme/chat",
  inboxUnread: 0,
  chatUnread: 0,
  openCreate: vi.fn(),
  setOpenMobile: vi.fn(),
}));

vi.mock("../navigation", () => ({
  AppLink: ({ children, href, ...rest }: { children: React.ReactNode; href: string }) => (
    <a href={href} {...rest}>{children}</a>
  ),
  useNavigation: () => ({ pathname: state.pathname }),
}));
vi.mock("@multica/core/paths", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/paths")>();
  return {
    ...actual,
    useCurrentWorkspace: () => ({ id: "ws-1", slug: "acme" }),
    useWorkspacePaths: () => actual.paths.workspace("acme"),
  };
});
vi.mock("@multica/core/inbox/queries", () => ({
  useInboxUnreadCount: () => state.inboxUnread,
}));
vi.mock("@multica/core/issues/stores/create-mode-store", () => ({
  openCreateIssueWithPreference: () => state.openCreate(),
}));
vi.mock("@multica/ui/components/ui/sidebar", () => ({
  useSidebar: () => ({ setOpenMobile: state.setOpenMobile }),
}));
vi.mock("./guest-readonly", () => ({
  WriteAction: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));
vi.mock("./nav-unread", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./nav-unread")>()),
  useChatNavUnreadCount: () => state.chatUnread,
}));

beforeEach(() => {
  state.pathname = "/acme/chat";
  state.inboxUnread = 0;
  state.chatUnread = 0;
  state.openCreate.mockReset();
  state.setOpenMobile.mockReset();
});

describe("MobileTabBar", () => {
  it("links Chat, Issues and Inbox and marks the current one", () => {
    state.pathname = "/acme/chat/s-1";
    renderWithI18n(<MobileTabBar />);
    const chat = screen.getByRole("link", { name: /chat/i });
    expect(chat.getAttribute("href")).toBe("/acme/chat");
    expect(chat.getAttribute("aria-current")).toBe("page");
    expect(screen.getByRole("link", { name: /issues/i }).getAttribute("href")).toBe("/acme/issues");
    expect(screen.getByRole("link", { name: /inbox/i }).getAttribute("aria-current")).toBeNull();
  });

  it("shows the inbox unread count, capped", () => {
    state.inboxUnread = 3;
    const { rerender } = renderWithI18n(<MobileTabBar />);
    expect(screen.getByRole("link", { name: /inbox/i }).textContent).toContain("3");
    state.inboxUnread = 140;
    rerender(<MobileTabBar />);
    expect(screen.getByRole("link", { name: /inbox/i }).textContent).toContain("99+");
  });

  it("opens the create-issue flow and the nav sheet", () => {
    renderWithI18n(<MobileTabBar />);
    fireEvent.click(screen.getByRole("button", { name: /new/i }));
    expect(state.openCreate).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: /more/i }));
    expect(state.setOpenMobile).toHaveBeenCalledWith(true);
  });
});

describe("text entry focus", () => {
  it("counts fields that raise a keyboard", () => {
    const text = document.createElement("input");
    const box = document.createElement("input");
    box.type = "checkbox";
    const area = document.createElement("textarea");
    const readOnly = document.createElement("textarea");
    readOnly.readOnly = true;
    expect(isTextEntry(text)).toBe(true);
    expect(isTextEntry(box)).toBe(false);
    expect(isTextEntry(area)).toBe(true);
    expect(isTextEntry(readOnly)).toBe(false);
    expect(isTextEntry(null)).toBe(false);
  });

  it("tracks focus in and out of a field", async () => {
    vi.useFakeTimers();
    function Probe() {
      return <span data-testid="typing">{String(useTextEntryFocused(true))}</span>;
    }
    render(
      <>
        <Probe />
        <textarea aria-label="composer" />
      </>,
    );
    const composer = screen.getByLabelText("composer");
    expect(screen.getByTestId("typing").textContent).toBe("false");
    act(() => composer.focus());
    expect(screen.getByTestId("typing").textContent).toBe("true");
    act(() => {
      composer.blur();
      vi.runAllTimers();
    });
    expect(screen.getByTestId("typing").textContent).toBe("false");
    vi.useRealTimers();
  });
});
