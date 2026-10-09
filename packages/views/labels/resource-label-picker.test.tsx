import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderWithI18n } from "../test/i18n";
import enLabels from "../locales/en/labels.json";

const api = vi.hoisted(() => ({
  listLabels: vi.fn(),
  listLabelsForResource: vi.fn(),
  attachLabelToResource: vi.fn(),
  detachLabelFromResource: vi.fn(),
}));
const toastError = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", () => ({ api }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("sonner", () => ({ toast: { error: toastError } }));

import { ResourceLabelPicker } from "./resource-label-picker";

const label = { id: "l1", name: "backend", color: "#888" };

describe("ResourceLabelPicker", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    api.listLabels.mockResolvedValue({ labels: [label] });
    api.listLabelsForResource.mockResolvedValue({ labels: [] });
  });

  it("says why when attaching a label is refused", async () => {
    api.attachLabelToResource.mockRejectedValue(new Error("only the agent owner can change labels"));
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    renderWithI18n(
      <QueryClientProvider client={qc}>
        <ResourceLabelPicker resourceType="agent" resourceId="a1" canEdit />
      </QueryClientProvider>,
    );

    fireEvent.click(await screen.findByRole("button", { name: enLabels.resource_picker.add }));
    fireEvent.click(await screen.findByText("backend"));

    await waitFor(() => expect(toastError).toHaveBeenCalledWith("only the agent owner can change labels"));
  });
});
