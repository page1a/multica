import { render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { OverflowActions, type OverflowItem } from "./overflow-actions";

// jsdom has no layout: give the bar a width and each item the width its label
// declares ("w40" is 40px wide, an empty one 0).
function mockLayout(barWidth: number) {
  const offsetWidth = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetWidth");
  const clientWidth = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientWidth");
  Object.defineProperty(HTMLElement.prototype, "clientWidth", {
    configurable: true,
    get(this: HTMLElement) {
      return this.hasAttribute("data-overflow-bar") ? barWidth : 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "offsetWidth", {
    configurable: true,
    get(this: HTMLElement) {
      const match = /w(\d+)/.exec(this.textContent ?? "");
      return this.hasAttribute("data-overflow-key") && match ? Number(match[1]) : 0;
    },
  });
  return () => {
    if (offsetWidth) Object.defineProperty(HTMLElement.prototype, "offsetWidth", offsetWidth);
    if (clientWidth) Object.defineProperty(HTMLElement.prototype, "clientWidth", clientWidth);
  };
}

const items = (...rest: OverflowItem[]) => rest;
const item = (key: string, label: string, priority?: number): OverflowItem => ({
  key,
  priority,
  node: label ? <button type="button">{label}</button> : null,
});

function Harness({ list }: { list: OverflowItem[] }) {
  return (
    <OverflowActions
      items={list}
      renderMore={(overflow: ReactNode) => (
        <div data-testid="more">{overflow}</div>
      )}
    />
  );
}

const inBar = () =>
  [...document.querySelectorAll("[data-overflow-bar] [data-overflow-key]")].map((el) => el.getAttribute("data-overflow-key"));
const inMenu = () =>
  [...document.querySelectorAll("[data-overflow-menu] [data-overflow-key]")].map((el) => el.getAttribute("data-overflow-key"));

describe("OverflowActions", () => {
  let restore = () => {};
  beforeEach(() => {
    restore = () => {};
  });
  afterEach(() => restore());

  it("keeps everything in the bar when it fits", () => {
    restore = mockLayout(300);
    render(<Harness list={items(item("a", "w100"), item("b", "w100"), item("c", "w80"))} />);
    expect(inBar()).toEqual(["a", "b", "c"]);
    expect(inMenu()).toEqual([]);
  });

  it("moves what does not fit into the menu, least important first", () => {
    restore = mockLayout(200);
    render(
      <Harness
        list={items(item("share", "w100", 3), item("pin", "w40", 1), item("thread", "w120", 4), item("agent", "w60", 2))}
      />,
    );
    // pin 40 + agent 60 + gap + share 100 > 200? 40+4+60=104, +4+100=208 > 200
    expect(inBar()).toEqual(["pin", "agent"]);
    expect(inMenu()).toEqual(["share", "thread"]);
    expect(screen.getByTestId("more")).toHaveTextContent("w100");
  });

  it("takes an item back when the bar grows", () => {
    restore = mockLayout(100);
    const list = items(item("a", "w80"), item("b", "w80"));
    const { rerender } = render(<Harness list={list} />);
    expect(inMenu()).toEqual(["b"]);
    restore();
    restore = mockLayout(400);
    rerender(<Harness list={[...list]} />);
    expect(inBar()).toEqual(["a", "b"]);
  });

  it("ignores items that render nothing", () => {
    restore = mockLayout(100);
    render(<Harness list={items(item("empty", ""), item("a", "w90"))} />);
    expect(inBar()).toEqual(["empty", "a"]);
    expect(inMenu()).toEqual([]);
  });
});
