import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../locales/en/common.json";
import zhCommon from "../locales/zh-Hans/common.json";
import { ScrollToBottomButton } from "./scroll-to-bottom-button";

function renderButton(
  props: Partial<React.ComponentProps<typeof ScrollToBottomButton>> = {},
  locale: "en" | "zh-Hans" = "en",
) {
  const onClick = vi.fn();
  render(
    <I18nProvider
      locale={locale}
      resources={{ en: { common: enCommon }, "zh-Hans": { common: zhCommon } }}
    >
      <ScrollToBottomButton visible onClick={onClick} {...props} />
    </I18nProvider>,
  );
  return { onClick };
}

describe("ScrollToBottomButton", () => {
  it("is inert and out of the tab order while hidden, but stays mounted for the fade", () => {
    const { onClick } = renderButton({ visible: false });
    const button = screen.getByTestId("scroll-to-bottom");
    expect(button).toHaveAttribute("aria-hidden", "true");
    expect(button).toHaveAttribute("tabindex", "-1");
    expect(button.className).toContain("pointer-events-none");
    expect(button.className).toContain("opacity-0");
    expect(onClick).not.toHaveBeenCalled();
  });

  it("calls onClick when visible", () => {
    const { onClick } = renderButton();
    fireEvent.click(screen.getByTestId("scroll-to-bottom"));
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it("keeps a 44px touch target", () => {
    renderButton();
    expect(screen.getByTestId("scroll-to-bottom").className).toMatch(/\bh-11\b.*\bmin-w-11\b/);
  });

  it("shows a count badge for new messages and comments", () => {
    renderButton({ newCount: 2 });
    expect(screen.getByText("2 new messages")).toBeInTheDocument();
  });

  it("uses comment wording for the issue timeline", () => {
    renderButton({ newCount: 1, unit: "comments" });
    expect(screen.getByText("1 new comment")).toBeInTheDocument();
  });

  it("has no badge without new items", () => {
    renderButton({ newCount: 0 });
    expect(screen.queryByText(/new/)).toBeNull();
  });

  it("speaks Chinese", () => {
    renderButton({ newCount: 3 }, "zh-Hans");
    expect(screen.getByText("3 条新消息")).toBeInTheDocument();
    expect(screen.getByTestId("scroll-to-bottom")).toHaveAttribute(
      "aria-label",
      "回到底部 · 3 条新消息",
    );
  });
});
