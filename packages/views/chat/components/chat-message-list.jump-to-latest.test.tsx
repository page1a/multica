import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import type { ChatMessage } from "@multica/core/types";
import type { ReactElement } from "react";
import enChat from "../../locales/en/chat.json";
import enCommon from "../../locales/en/common.json";

// Jump-to-latest wiring: the button rides on the same live-end edge the
// bottom-stick uses, counts replies that land while the reader is scrolled up,
// and clears once they are back. Virtuoso is stubbed (jsdom has no viewport);
// scrolling is driven through the real container's scroll events.
const scrollToIndex = vi.fn();
// Every render of the list body. The reader crossing the live-end edge must not
// add one: the host re-rendering mid-scroll is what pulled readers back down
// (DENE-1040).
let listRenders = 0;

vi.mock("react-virtuoso", () => ({
  Virtuoso: ({
    ref,
    data,
    itemContent,
    computeItemKey,
  }: {
    ref?: { current: unknown } | ((handle: unknown) => void);
    data: unknown[];
    itemContent: (i: number, item: unknown) => ReactElement;
    computeItemKey: (i: number, item: unknown) => string;
  }) => {
    listRenders++;
    const handle = { scrollToIndex };
    if (typeof ref === "function") ref(handle);
    else if (ref) ref.current = handle;
    return (
      <div>
        {data.map((item, i) => (
          <div key={computeItemKey(i, item)}>{itemContent(i, item)}</div>
        ))}
      </div>
    );
  },
}));

import { ChatMessageList } from "./chat-message-list";

const RESOURCES = { en: { chat: enChat, common: enCommon } };

function msg(id: string, role: "user" | "assistant", content: string): ChatMessage {
  return {
    id,
    chat_session_id: "session-1",
    role,
    content,
    task_id: null,
    created_at: "2026-09-29T00:00:00Z",
  } as ChatMessage;
}

function tree(messages: ChatMessage[]) {
  return (
    <I18nProvider locale="en" resources={RESOURCES}>
      <QueryClientProvider client={new QueryClient()}>
        <ChatMessageList messages={messages} pendingTask={null} availability="online" />
      </QueryClientProvider>
    </I18nProvider>
  );
}

function scrollTo(el: HTMLElement, distanceFromBottom: number) {
  const state = { scrollHeight: 3000, clientHeight: 600 };
  Object.defineProperties(el, {
    scrollHeight: { configurable: true, get: () => state.scrollHeight },
    clientHeight: { configurable: true, get: () => state.clientHeight },
    scrollTop: {
      configurable: true,
      get: () => state.scrollHeight - state.clientHeight - distanceFromBottom,
      set: () => {},
    },
  });
  act(() => {
    el.dispatchEvent(new Event("scroll"));
  });
}

const button = () => screen.getByTestId("scroll-to-bottom");
const isShown = () => button().getAttribute("aria-hidden") === "false";

beforeEach(() => {
  vi.useFakeTimers();
  scrollToIndex.mockClear();
  listRenders = 0;
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

function mount(messages: ChatMessage[]) {
  const view = render(tree(messages));
  // The list stays hidden until it lands on the newest row; the reveal
  // deadline is what releases it in jsdom.
  act(() => {
    vi.advanceTimersByTime(1200);
  });
  const el = view.container.querySelector<HTMLElement>("[data-tab-scroll-root]")!;
  return { view, el };
}

describe("ChatMessageList jump-to-latest", () => {
  it("is hidden at the bottom and appears once the reader scrolls up", () => {
    const { el } = mount([msg("m1", "user", "hi"), msg("m2", "assistant", "hello")]);

    scrollTo(el, 0);
    expect(isShown()).toBe(false);

    scrollTo(el, 900);
    expect(isShown()).toBe(true);

    scrollTo(el, 30);
    expect(isShown()).toBe(false);
  });

  it("counts replies that land while away and clears on return", () => {
    const base = [msg("m1", "user", "hi"), msg("m2", "assistant", "hello")];
    const { view, el } = mount(base);
    scrollTo(el, 900);
    expect(button()).toHaveAttribute("aria-label", "Jump to latest");

    const withOne = [...base, msg("m3", "assistant", "reply 1")];
    view.rerender(tree(withOne));
    expect(button()).toHaveAttribute("aria-label", "Jump to latest · 1 new message");

    view.rerender(tree([...withOne, msg("m4", "assistant", "reply 2")]));
    expect(button()).toHaveAttribute("aria-label", "Jump to latest · 2 new messages");

    scrollTo(el, 0);
    expect(isShown()).toBe(false);
    scrollTo(el, 900);
    expect(button()).toHaveAttribute("aria-label", "Jump to latest");
  });

  it("does not count the reader's own sends", () => {
    const base = [msg("m1", "assistant", "hello")];
    const { view, el } = mount(base);
    scrollTo(el, 900);

    view.rerender(tree([...base, msg("m2", "user", "question")]));
    expect(button()).toHaveAttribute("aria-label", "Jump to latest");
  });

  it("scrolls to the newest message through Virtuoso when clicked", () => {
    const { el } = mount([msg("m1", "assistant", "hello")]);
    scrollTo(el, 900);

    fireEvent.click(button());

    expect(scrollToIndex).toHaveBeenCalledWith({
      index: "LAST",
      align: "end",
      behavior: "smooth",
    });
  });

  // DENE-1040: the away flag flips in the middle of the reader's first scroll
  // up. If that re-rendered the list host, the reflow of the rows under them
  // (a wide table) would land on the follow latch at the worst moment.
  it("does not re-render the list when the reader crosses the live-end edge", () => {
    const { el } = mount([msg("m1", "user", "hi"), msg("m2", "assistant", "hello")]);
    // Off the very bottom first, so the scroll-edge fade has settled and only
    // the away flag is left to move.
    scrollTo(el, 300);
    expect(isShown()).toBe(true);
    const before = listRenders;

    scrollTo(el, 20);
    expect(isShown()).toBe(false);
    scrollTo(el, 300);
    expect(isShown()).toBe(true);

    expect(listRenders).toBe(before);
  });
});
