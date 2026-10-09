import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { InboxItem, Issue } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import enInbox from "../../locales/en/inbox.json";

const api = vi.hoisted(() => ({ keepIssueStall: vi.fn(), undoIssueStall: vi.fn() }));
const toastError = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", () => ({ api }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("sonner", () => ({ toast: { error: toastError } }));

import { IssueStallActionBanner } from "./issue-stall-action-banner";
import { StallActionNotice } from "../../inbox/components/stall-action-notice";

function wrap(ui: React.ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  renderWithI18n(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>);
}

describe("stall actions", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    api.keepIssueStall.mockRejectedValue(new Error("ticket already moved on"));
    api.undoIssueStall.mockRejectedValue(new Error("ticket already moved on"));
  });

  it("says why the ticket banner's keep was refused", async () => {
    wrap(<IssueStallActionBanner issue={{ id: "i1", metadata: { "stall.action": "announced" } } as unknown as Issue} />);
    fireEvent.click(screen.getByRole("button", { name: enInbox.detail.stall_keep }));
    await waitFor(() => expect(toastError).toHaveBeenCalledWith("ticket already moved on"));
  });

  it("says why the inbox notice's undo was refused", async () => {
    const item = { issue_id: "i1", title: "Handled", body: "", details: { actions: ["undo"] } } as unknown as InboxItem;
    wrap(<StallActionNotice item={item} />);
    fireEvent.click(screen.getByRole("button", { name: enInbox.detail.stall_undo }));
    await waitFor(() => expect(toastError).toHaveBeenCalledWith("ticket already moved on"));
  });
});
