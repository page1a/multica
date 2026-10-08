import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import { fireEvent, render, renderHook, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const downloadById = vi.hoisted(() => vi.fn());
const toast = vi.hoisted(() => vi.fn());

vi.mock("./use-download-attachment", () => ({
  useDownloadAttachment: () => downloadById,
}));
vi.mock("sonner", () => ({ toast }));
vi.mock("../i18n", () => ({
  useT: () => ({
    t: (sel: (s: Record<string, Record<string, string>>) => string) =>
      sel({
        image: { download: "Download" },
        attachment: {
          preview: "Preview",
          remove: "Remove attachment",
          open_local: "Open",
          reveal_local_finder: "Show in Finder",
          reveal_local: "Show in folder",
          local_copy_gone: "Copy gone",
        },
        file_card: { uploading: "Uploading {{filename}}" },
      }),
  }),
}));

import { useLocalAttachment, type LocalAttachment } from "./use-attachment-actions";
import { AttachmentCard, AttachmentFileCard } from "./attachment-card";

const ID = "01a11579-ea96-76ba-8a38-13dba24d52ce";

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

function installBridge(available: boolean, ok = true) {
  const bridge = {
    status: vi.fn(async () => ({ available })),
    open: vi.fn(async () => (ok ? { ok: true } : { ok: false, reason: "not_found" })),
    reveal: vi.fn(async () => ({ ok: true })),
  };
  (window as unknown as { desktopAPI?: unknown }).desktopAPI = {
    localAttachment: bridge,
  };
  return bridge;
}

beforeEach(() => vi.clearAllMocks());
afterEach(() => {
  delete (window as unknown as { desktopAPI?: unknown }).desktopAPI;
});

describe("useLocalAttachment", () => {
  it("offers nothing on web, where there is no bridge", () => {
    const { result } = renderHook(() => useLocalAttachment(ID), { wrapper });
    expect(result.current).toBeNull();
  });

  it("offers nothing when this computer has no copy", async () => {
    const bridge = installBridge(false);
    const { result } = renderHook(() => useLocalAttachment(ID), { wrapper });
    await waitFor(() => expect(bridge.status).toHaveBeenCalledWith(ID));
    expect(result.current).toBeNull();
  });

  it("opens and reveals by attachment id when the copy is here", async () => {
    const bridge = installBridge(true);
    const { result } = renderHook(() => useLocalAttachment(ID), { wrapper });
    await waitFor(() => expect(result.current).not.toBeNull());
    result.current!.open();
    result.current!.reveal();
    await waitFor(() => expect(bridge.reveal).toHaveBeenCalledWith(ID));
    expect(bridge.open).toHaveBeenCalledWith(ID);
    expect(downloadById).not.toHaveBeenCalled();
  });

  it("downloads instead when the copy went away after it was offered", async () => {
    installBridge(true, false);
    const { result } = renderHook(() => useLocalAttachment(ID), { wrapper });
    await waitFor(() => expect(result.current).not.toBeNull());
    result.current!.open();
    await waitFor(() => expect(downloadById).toHaveBeenCalledWith(ID));
    expect(toast).toHaveBeenCalledWith("Copy gone");
  });
});

describe("attachment cards with a local copy", () => {
  const local: LocalAttachment = {
    open: vi.fn(),
    reveal: vi.fn(),
    revealLabel: "Show in Finder",
  };

  it("adds Open and Show in Finder to the row next to Download", () => {
    render(
      <AttachmentCard
        filename="report.zip"
        attachmentId={ID}
        href="https://cdn.example/report.zip"
        onPreview={() => {}}
        onDownload={() => {}}
        local={local}
      />,
    );
    fireEvent.mouseDown(screen.getByRole("button", { name: "Open" }));
    fireEvent.mouseDown(screen.getByRole("button", { name: "Show in Finder" }));
    expect(local.open).toHaveBeenCalledOnce();
    expect(local.reveal).toHaveBeenCalledOnce();
    expect(screen.getByRole("button", { name: "Download" })).toBeTruthy();
  });

  it("shows no local actions without a copy", () => {
    render(
      <AttachmentCard
        filename="report.zip"
        href="https://cdn.example/report.zip"
        onPreview={() => {}}
        onDownload={() => {}}
      />,
    );
    expect(screen.queryByRole("button", { name: "Open" })).toBeNull();
  });

  it("opens a file the viewer can't show from this computer instead of downloading", () => {
    const onDownload = vi.fn();
    render(
      <AttachmentFileCard
        filename="report.zip"
        canPreview={false}
        canDownload
        onPreview={() => {}}
        onDownload={onDownload}
        local={local}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "report.zip" }));
    expect(local.open).toHaveBeenCalledOnce();
    expect(onDownload).not.toHaveBeenCalled();
  });
});
