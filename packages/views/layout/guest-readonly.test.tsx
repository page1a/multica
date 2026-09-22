import type { ReactElement } from "react";
import { describe, expect, it } from "vitest";
import { fireEvent, screen } from "@testing-library/react";
import { TooltipProvider } from "@multica/ui/components/ui/tooltip";
import { renderWithI18n } from "../test/i18n";
import {
  GuestBanner,
  GuestReadOnlyScope,
  NothingSharedEmpty,
  ResourceNotFound,
  WriteAction,
} from "./guest-readonly";

function renderGuest(ui: ReactElement, locale?: "en" | "zh-Hans") {
  return renderWithI18n(
    <TooltipProvider>
      <GuestReadOnlyScope isGuest>{ui}</GuestReadOnlyScope>
    </TooltipProvider>,
    locale ? { locale } : undefined,
  );
}

describe("GuestBanner", () => {
  it("is hidden until the viewer is known to be a guest", () => {
    const { container } = renderWithI18n(
      <GuestReadOnlyScope isGuest={false}>
        <GuestBanner />
      </GuestReadOnlyScope>,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("shows the view-only badge for a guest", () => {
    renderGuest(<GuestBanner />);
    expect(screen.getByTestId("guest-readonly-banner")).toHaveTextContent(
      "You're a guest — view only",
    );
  });

  it("uses the prototype Chinese copy", () => {
    renderGuest(<GuestBanner />, "zh-Hans");
    expect(screen.getByTestId("guest-readonly-banner")).toHaveTextContent(
      "你是访客，只能看不能改",
    );
  });
});

describe("WriteAction", () => {
  it("leaves the control enabled for members", () => {
    renderWithI18n(
      <TooltipProvider>
        <GuestReadOnlyScope isGuest={false}>
          <WriteAction>
            <button type="button">Edit</button>
          </WriteAction>
        </GuestReadOnlyScope>
      </TooltipProvider>,
    );
    expect(screen.queryByTestId("write-action")).toBeNull();
    expect(screen.getByRole("button", { name: "Edit" })).toBeEnabled();
  });

  it("keeps the control visible but inert, and explains on click", () => {
    renderGuest(
      <WriteAction>
        <button type="button">Edit</button>
      </WriteAction>,
    );
    const wrap = screen.getByTestId("write-action");
    expect(wrap).toHaveTextContent("Edit");
    expect(wrap).toHaveAttribute("aria-label", "Guests can't edit");
    fireEvent.click(wrap);
    expect(screen.getByRole("button", { name: "Edit" })).toBeTruthy();
  });
});

describe("NothingSharedEmpty", () => {
  it("explains that nothing has been shared, without a create CTA", () => {
    renderGuest(<NothingSharedEmpty />);
    expect(screen.getByTestId("nothing-shared-empty")).toHaveTextContent(
      "Nothing has been shared with you yet",
    );
    expect(screen.getByTestId("nothing-shared-empty")).toHaveTextContent(
      "adds you to a project",
    );
    expect(screen.queryByRole("button")).toBeNull();
  });
});

describe("ResourceNotFound", () => {
  it("says not found rather than permission denied", () => {
    renderGuest(<ResourceNotFound />);
    const node = screen.getByTestId("resource-not-found");
    expect(node).toHaveTextContent("Page not found");
    expect(node).toHaveTextContent("hasn't been shared with you");
    expect(node.textContent?.toLowerCase()).not.toContain("permission");
  });

  it("uses the prototype Chinese copy", () => {
    renderGuest(<ResourceNotFound />, "zh-Hans");
    const node = screen.getByTestId("resource-not-found");
    expect(node).toHaveTextContent("找不到这个页面");
    expect(node).toHaveTextContent("没有共享给你");
    expect(node.textContent).not.toContain("权限");
  });
});
