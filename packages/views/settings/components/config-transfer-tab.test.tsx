import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";
import {
  ApiError,
  CONFIG_BUNDLE_FORMAT,
  CONFIG_BUNDLE_SCHEMA_VERSION,
} from "@multica/core/api";

const exportWorkspaceConfig = vi.hoisted(() => vi.fn());
const importWorkspaceConfig = vi.hoisted(() => vi.fn());
const member = vi.hoisted(() => ({
  role: "owner" as "owner" | "admin" | "member",
}));
const desktop = vi.hoisted(() => ({ isDesktop: false }));

vi.mock("@multica/core/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/api")>();
  return {
    ...actual,
    api: {
      exportWorkspaceConfig,
      importWorkspaceConfig,
    },
  };
});

vi.mock("@multica/core/paths", async (importOriginal) => ({
  paths: (await importOriginal<typeof import("@multica/core/paths")>()).paths,
  useCurrentWorkspace: () => ({
    id: "ws-1",
    name: "Acme",
    slug: "acme",
  }),
}));

vi.mock("@multica/core/permissions", () => ({
  useCurrentMember: () => ({ role: member.role, isLoading: false }),
}));

vi.mock("../../navigation", () => ({
  AppLink: ({ href, children }: { href: string; children: ReactNode }) => (
    <a href={href}>{children}</a>
  ),
}));

vi.mock("../../platform", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../platform")>();
  return {
    ...actual,
    isDesktopShell: () => desktop.isDesktop,
  };
});

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

import { ConfigTransferTab } from "./config-transfer-tab";

const bundle = {
  format: CONFIG_BUNDLE_FORMAT,
  schema_version: CONFIG_BUNDLE_SCHEMA_VERSION,
  bundle_id: "bundle-1",
  exported_at: "2026-09-15T00:00:00Z",
  source: {
    workspace_id: "ws-src",
    slug: "acme",
    name: "Acme",
    issue_prefix: "ACM",
    exported_by: "user-1",
  },
  entities: { labels: [{ source_id: "l1", name: "bug" }] },
  secrets_omitted: [
    {
      entity: "agent",
      source_id: "a1",
      name: "Builder",
      field: "custom_env",
      reason: "secret_material",
    },
  ],
  stats: { labels: 1 },
};

const previewReport = {
  applied: false,
  bundle_id: "bundle-1",
  on_conflict: "skip",
  batches: [
    {
      entity_type: "labels",
      batch_status: "preview",
      items: [
        {
          source_id: "l1",
          name: "bug",
          action: "created",
          target_id: "new-1",
        },
      ],
    },
  ],
  unmapped_refs: [],
  secrets_to_fill: [
    {
      entity: "agent",
      target_id: "a1",
      name: "Builder",
      field: "custom_env",
      path: "/acme/agents/a1/settings",
    },
  ],
  warnings: [],
  stats: { created: 1, updated: 0, renamed: 0, skipped: 0, failed: 0 },
};

const conflictReport = {
  ...previewReport,
  batches: [
    {
      entity_type: "labels",
      batch_status: "preview",
      items: [
        {
          source_id: "l1",
          name: "bug",
          action: "skipped",
          reason: "exists",
        },
      ],
    },
  ],
  stats: { created: 0, updated: 0, renamed: 0, skipped: 1, failed: 0 },
};

const applyReport = {
  ...previewReport,
  applied: true,
  stats: { created: 1, updated: 0, renamed: 0, skipped: 0, failed: 0 },
};

function renderTab() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <ConfigTransferTab />
    </QueryClientProvider>,
  );
}

function jsonFile(body: unknown, name = "acme.json") {
  return new File([JSON.stringify(body)], name, { type: "application/json" });
}

beforeEach(() => {
  member.role = "owner";
  desktop.isDesktop = false;
  exportWorkspaceConfig.mockReset();
  importWorkspaceConfig.mockReset();
  vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:export");
  vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
});

describe("ConfigTransferTab", () => {
  it("exports a JSON bundle and shows omitted secrets", async () => {
    const user = userEvent.setup();
    exportWorkspaceConfig.mockResolvedValue(bundle);
    renderTab();

    await user.click(
      screen.getByRole("button", { name: "Export configuration" }),
    );

    await waitFor(() =>
      expect(exportWorkspaceConfig).toHaveBeenCalledWith("ws-1"),
    );
    expect(URL.createObjectURL).toHaveBeenCalled();
    expect(
      screen.getByText(
        "These secrets are omitted from the file. After import, fill them in by hand:",
      ),
    ).toBeInTheDocument();
    expect(screen.getByText("agent · Builder · custom_env")).toBeInTheDocument();
  });

  it("previews a file then applies after confirmation", async () => {
    const user = userEvent.setup();
    importWorkspaceConfig
      .mockResolvedValueOnce({ report: previewReport })
      .mockResolvedValueOnce({ report: applyReport });
    renderTab();

    await user.upload(
      screen.getByTestId("config-transfer-file"),
      jsonFile(bundle),
    );

    expect(await screen.findByText("New")).toBeInTheDocument();
    expect(screen.getByText("1")).toBeInTheDocument();
    expect(
      screen.getByText(
        "Refill these secrets after import.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "agent · Builder · custom_env" })).toHaveAttribute(
      "href",
      "/acme/agents/a1?view=env",
    );
    expect(importWorkspaceConfig).toHaveBeenCalledWith(
      "ws-1",
      expect.objectContaining({ dry_run: true, on_conflict: "skip" }),
    );

    await user.click(
      screen.getByRole("button", { name: "Import into this workspace" }),
    );
    const dialog = await screen.findByRole("alertdialog");
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Import into this workspace" }),
    );

    await waitFor(() =>
      expect(importWorkspaceConfig).toHaveBeenCalledWith(
        "ws-1",
        expect.objectContaining({ dry_run: false, on_conflict: "skip" }),
      ),
    );
  });

  it("shows conflict preview counts and skip reasons", async () => {
    const user = userEvent.setup();
    importWorkspaceConfig.mockResolvedValue({ report: conflictReport });
    renderTab();

    await user.upload(
      screen.getByTestId("config-transfer-file"),
      jsonFile(bundle),
    );

    expect(await screen.findByText("Conflicts and skips")).toBeInTheDocument();
    expect(screen.getByText("Skipped")).toBeInTheDocument();
    expect(screen.getByText("bug")).toBeInTheDocument();
    expect(screen.getByText(/skipped · already exists/)).toBeInTheDocument();
  });

  it("blocks apply when the dry run reports a conflict error", async () => {
    const user = userEvent.setup();
    importWorkspaceConfig.mockResolvedValue({
      report: conflictReport,
      error: { code: "config_import_conflict", message: "import conflicts", status: 409 },
    });
    renderTab();

    await user.upload(
      screen.getByTestId("config-transfer-file"),
      jsonFile(bundle),
    );

    expect(
      await screen.findByText("Import stopped because of a conflict."),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Import into this workspace" }),
    ).toBeDisabled();
  });

  it("shows a failure alert when export fails", async () => {
    const user = userEvent.setup();
    exportWorkspaceConfig.mockRejectedValue(new Error("boom"));
    renderTab();

    await user.click(
      screen.getByRole("button", { name: "Export configuration" }),
    );

    expect(
      await screen.findByRole("alert"),
    ).toHaveTextContent("Couldn't export configuration.");
  });

  it("shows a readable hint when export returns 404 instead of the raw API error", async () => {
    const user = userEvent.setup();
    desktop.isDesktop = true;
    exportWorkspaceConfig.mockRejectedValue(
      new ApiError("API error: 404 Not Found", 404, "Not Found"),
    );
    renderTab();

    await user.click(
      screen.getByRole("button", { name: "Export configuration" }),
    );

    const hint = await screen.findByTestId("config-transfer-unsupported");
    expect(hint).toHaveTextContent(
      "This server does not support configuration export/import; a kun self-hosted instance is required.",
    );
    expect(hint).toHaveTextContent(
      'To migrate from official cloud, use "Migrate across environments (including chats)" above.',
    );
    expect(screen.queryByText(/API error: 404/)).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Export configuration" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Choose JSON file" }),
    ).not.toBeInTheDocument();
  });

  it("shows a readable hint when import returns 404 instead of the raw API error", async () => {
    const user = userEvent.setup();
    importWorkspaceConfig.mockRejectedValue(
      new ApiError("API error: 404 Not Found", 404, "Not Found"),
    );
    renderTab();

    await user.upload(
      screen.getByTestId("config-transfer-file"),
      jsonFile(bundle),
    );

    expect(
      await screen.findByTestId("config-transfer-unsupported"),
    ).toHaveTextContent(
      "This server does not support configuration export/import; a kun self-hosted instance is required.",
    );
    expect(screen.queryByText(/API error: 404/)).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Export configuration" }),
    ).not.toBeInTheDocument();
  });

  it("shows a failure alert for an invalid import file", async () => {
    const user = userEvent.setup();
    renderTab();

    await user.upload(
      screen.getByTestId("config-transfer-file"),
      jsonFile({ hello: "world" }),
    );

    expect(
      await screen.findByText("This file is not a Multica configuration bundle."),
    ).toBeInTheDocument();
    expect(importWorkspaceConfig).not.toHaveBeenCalled();
  });
});
