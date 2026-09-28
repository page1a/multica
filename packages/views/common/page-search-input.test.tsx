/**
 * @vitest-environment jsdom
 */
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { PageSearchInput } from "./page-search-input";

describe("PageSearchInput", () => {
  it("forwards typing and exposes an accessible clear action", () => {
    const onChange = vi.fn();
    const view = render(
      <PageSearchInput
        value=""
        onChange={onChange}
        placeholder="Search title or issue ID…"
        clearLabel="Clear search"
      />,
    );

    fireEvent.change(screen.getByRole("searchbox"), {
      target: { value: "MUL-4797" },
    });
    expect(onChange).toHaveBeenLastCalledWith("MUL-4797");

    view.rerender(
      <PageSearchInput
        value="MUL-4797"
        onChange={onChange}
        placeholder="Search title or issue ID…"
        clearLabel="Clear search"
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Clear search" }));
    expect(onChange).toHaveBeenLastCalledWith("");
  });

  it("shows the focus shortcut while idle and clears on Escape", () => {
    const onChange = vi.fn();
    const view = render(
      <PageSearchInput value="" onChange={onChange} placeholder="Search" clearLabel="Clear" />,
    );
    const box = screen.getByRole("searchbox");
    expect(box.hasAttribute("data-page-search")).toBe(true);
    expect(view.container.querySelector('[data-slot="shortcut-keycaps"]')).not.toBeNull();

    view.rerender(
      <PageSearchInput value="release" onChange={onChange} placeholder="Search" clearLabel="Clear" />,
    );
    box.focus();
    fireEvent.keyDown(box, { key: "Escape" });
    expect(onChange).toHaveBeenLastCalledWith("");
    expect(document.activeElement).not.toBe(box);
  });
});
