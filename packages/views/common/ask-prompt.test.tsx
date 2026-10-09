import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Ask } from "@multica/core/types";

const api = vi.hoisted(() => ({ answerAsk: vi.fn(), getAsk: vi.fn(), listAsks: vi.fn() }));
const toastError = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", () => ({ api }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("sonner", () => ({ toast: { error: toastError } }));

import { AskPrompt } from "./ask-prompt";

const ask = {
  id: "ask-1",
  title: "Which branch?",
  status: "open",
  mode: "needs_you",
  questions: [{ text: "Pick one", options: [{ id: "a", label: "kun" }] }],
} as unknown as Ask;

describe("AskPrompt", () => {
  beforeEach(() => vi.clearAllMocks());

  it("says why when the answer is refused", async () => {
    api.answerAsk.mockRejectedValue(new Error("this question was already answered"));
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <AskPrompt initialAsk={ask} />
      </QueryClientProvider>,
    );

    fireEvent.click(screen.getByRole("radio", { name: "kun" }));
    fireEvent.click(screen.getByRole("button", { name: "提交回答" }));

    await waitFor(() => expect(toastError).toHaveBeenCalledWith("this question was already answered"));
    expect(screen.getByRole("button", { name: "提交回答" })).toBeInTheDocument();
  });
});
