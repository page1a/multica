// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import { useChatListWidthStore } from "@multica/core/chat/list-width-store";
import enCommon from "../../locales/en/common.json";
import enChat from "../../locales/en/chat.json";
import { ChatListSplit } from "./chat-list-split";

const RESOURCES = { en: { common: enCommon, chat: enChat } };
const CONTAINER_WIDTH = 1200;

function renderSplit(fitContentWidth: number | null = 499) {
  return render(
    <I18nProvider locale="en" resources={RESOURCES}>
      <ChatListSplit
        list={<div>list</div>}
        detail={<div>detail</div>}
        fitContentWidth={fitContentWidth}
      />
    </I18nProvider>,
  );
}

function listWidth(container: HTMLElement) {
  return container.querySelector<HTMLElement>("[data-slot='chat-list-pane']")!.style.width;
}

// jsdom's PointerEvent drops clientX, so drive the handlers with mouse-shaped
// events dispatched under the pointer event names.
function pointer(target: Element, type: string, clientX: number) {
  fireEvent(target, new MouseEvent(type, { bubbles: true, button: 0, clientX }));
}

function dragTo(handle: Element, from: number, to: number) {
  pointer(handle, "pointerdown", from);
  pointer(handle, "pointermove", to);
}

describe("ChatListSplit", () => {
  let clientWidth: PropertyDescriptor | undefined;

  beforeEach(() => {
    localStorage.clear();
    useChatListWidthStore.setState({ width: 300 });
    clientWidth = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientWidth");
    Object.defineProperty(HTMLElement.prototype, "clientWidth", {
      configurable: true,
      get: () => CONTAINER_WIDTH,
    });
  });

  afterEach(() => {
    vi.useRealTimers();
    if (clientWidth) Object.defineProperty(HTMLElement.prototype, "clientWidth", clientWidth);
  });

  it("starts at the 300px default", () => {
    const { container } = renderSplit();
    expect(listWidth(container)).toBe("300px");
  });

  it("drags past the old 480px cap and remembers the width on release", () => {
    const { container } = renderSplit();
    const handle = screen.getByRole("separator");

    dragTo(handle, 300, 700);
    expect(listWidth(container)).toBe("700px");
    expect(screen.getByRole("status")).toHaveTextContent("700px");
    // Nothing is saved until the pointer lets go.
    expect(useChatListWidthStore.getState().width).toBe(300);

    pointer(handle, "pointerup", 700);
    expect(useChatListWidthStore.getState().width).toBe(700);
    const saved = JSON.parse(localStorage.getItem("multica_chat_list_width") ?? "{}") as {
      state?: { width?: number };
    };
    expect(saved.state?.width).toBe(700);
  });

  it("stops where the conversation would drop under 360px and says why", () => {
    const { container } = renderSplit();
    const handle = screen.getByRole("separator");

    dragTo(handle, 300, 1100);
    expect(listWidth(container)).toBe("840px");
    expect(screen.getByRole("status")).toHaveTextContent("840px · limit reached");
    expect(
      screen.getByText("Limit: the conversation keeps at least 360px"),
    ).toBeInTheDocument();
    expect(container.querySelector("[data-slot='chat-list-fit-guide']")).toBeNull();
  });

  it("snaps to the width that fits every project and marks it", () => {
    const { container } = renderSplit();
    const handle = screen.getByRole("separator");

    // fitContentWidth 499 + the pane's 1px border = 500.
    dragTo(handle, 300, 507);
    expect(listWidth(container)).toBe("500px");
    expect(screen.getByRole("status")).toHaveTextContent("500px · fits every project");
    expect(container.querySelector("[data-slot='chat-list-fit-guide']")).toHaveStyle({
      left: "500px",
    });
    expect(container.querySelector("[data-slot='chat-list-limit-guide']")).toBeNull();

    pointer(handle, "pointermove", 530);
    expect(listWidth(container)).toBe("530px");
    expect(screen.getByRole("status")).toHaveTextContent(/^530px$/);
  });

  it("keeps the snap target it started the drag with", () => {
    // The project bar re-measures while the list resizes; a target that moved
    // mid-drag would let the snap and the measurement chase each other.
    const { container, rerender } = renderSplit();
    const handle = screen.getByRole("separator");
    pointer(handle, "pointerdown", 300);
    rerender(
      <I18nProvider locale="en" resources={RESOURCES}>
        <ChatListSplit list={<div>list</div>} detail={<div>detail</div>} fitContentWidth={519} />
      </I18nProvider>,
    );
    pointer(handle, "pointermove", 507);
    expect(listWidth(container)).toBe("500px");
  });

  it("double-click expands to the fit width, and again goes back to the default", () => {
    const { container } = renderSplit();
    const handle = screen.getByRole("separator");

    fireEvent.doubleClick(handle);
    expect(listWidth(container)).toBe("500px");
    expect(screen.getByRole("status")).toHaveTextContent("500px · fits every project");

    fireEvent.doubleClick(handle);
    expect(listWidth(container)).toBe("300px");
    expect(screen.getByRole("status")).toHaveTextContent("300px · back to default");
    expect(useChatListWidthStore.getState().width).toBe(300);
  });

  it("double-click goes to the limit when the window cannot fit every project", () => {
    const { container } = renderSplit(2000);
    fireEvent.doubleClick(screen.getByRole("separator"));
    expect(listWidth(container)).toBe("840px");
    expect(screen.getByRole("status")).toHaveTextContent(
      "840px · limit reached, the rest stay in More",
    );
  });

  it("paints a remembered width clamped to the window without overwriting it", () => {
    useChatListWidthStore.setState({ width: 1000 });
    const { container } = renderSplit();
    expect(listWidth(container)).toBe("840px");
    expect(useChatListWidthStore.getState().width).toBe(1000);
  });

  it("drops the drag feedback shortly after release", () => {
    vi.useFakeTimers();
    renderSplit();
    const handle = screen.getByRole("separator");
    dragTo(handle, 300, 400);
    pointer(handle, "pointerup", 400);
    expect(screen.getByRole("status")).toBeInTheDocument();
    act(() => {
      vi.advanceTimersByTime(800);
    });
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("resizes from the keyboard", () => {
    const { container } = renderSplit();
    const handle = screen.getByRole("separator");
    fireEvent.keyDown(handle, { key: "ArrowRight" });
    expect(listWidth(container)).toBe("316px");
    fireEvent.keyDown(handle, { key: "Enter" });
    expect(listWidth(container)).toBe("500px");
  });
});
