// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import type { NavigationAdapter } from "../navigation";
import { NavigationProvider } from "../navigation";
import { BreadcrumbHeader } from "./breadcrumb-header";

vi.mock("../i18n", () => ({
  useT: () => ({ t: () => "Back" }),
}));

function makeAdapter(overrides: Partial<NavigationAdapter> = {}): NavigationAdapter {
  return {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/runtimes/rt-1",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (path) => path,
    ...overrides,
  };
}

describe("BreadcrumbHeader mobile navigation", () => {
  it("uses history for the back control and keeps actions in a touch-scroll row", () => {
    const adapter = makeAdapter({ canGoBack: () => true });

    render(
      <NavigationProvider value={adapter}>
        <BreadcrumbHeader
          segments={[{ href: "/acme/runtimes", label: "运行时" }]}
          leaf={<span>主机</span>}
          actions={
            <>
              <button type="button">编辑</button>
              <button type="button">删除</button>
            </>
          }
        />
      </NavigationProvider>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Back" }));

    expect(adapter.back).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: "编辑" })).toBeVisible();
    expect(screen.getByRole("button", { name: "删除" })).toBeVisible();
  });

  it("falls back to the last ancestor when there is no browser history", () => {
    const adapter = makeAdapter({ canGoBack: () => false });

    render(
      <NavigationProvider value={adapter}>
        <BreadcrumbHeader
          segments={[{ href: "/acme/agents", label: "智能体" }]}
          leaf={<span>详情</span>}
        />
      </NavigationProvider>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Back" }));

    expect(adapter.replace).toHaveBeenCalledWith("/acme/agents");
  });
});
