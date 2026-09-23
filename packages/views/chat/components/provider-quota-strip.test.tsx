// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import enChat from "../../locales/en/chat.json";
import { ProviderQuotaStrip } from "./provider-quota-strip";

const getRoutingHealth = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "ws-1", slug: "acme" }),
}));

vi.mock("@multica/core/api", () => ({
  api: { getRoutingHealth },
}));

function renderStrip() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <I18nProvider locale="en" resources={{ en: { chat: enChat } }}>
        <ProviderQuotaStrip />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  getRoutingHealth.mockReset();
});

describe("ProviderQuotaStrip", () => {
  it("shows unknown instead of a made-up percentage when a provider has no fresh reading", async () => {
    getRoutingHealth.mockResolvedValue({
      state: "enabled",
      provider_quotas: [
        { provider: "claude", status: "unknown", used_percent: null, remaining: null, reset_at: null, observed_at: null },
        { provider: "codex", status: "available", used_percent: 42, remaining: null, reset_at: "2026-09-23T00:00:00Z", observed_at: "2026-09-22T18:00:00Z" },
        { provider: "grok", status: "quota_exhausted", used_percent: 100, remaining: null, reset_at: null, observed_at: "2026-09-22T18:00:00Z" },
      ],
    });
    renderStrip();
    expect(await screen.findByText("unknown")).toBeInTheDocument();
    expect(screen.getByText("42%")).toBeInTheDocument();
    expect(screen.getByText("used up")).toBeInTheDocument();
    expect(screen.getByText("Claude")).toBeInTheDocument();
    expect(screen.getByText("Codex")).toBeInTheDocument();
    expect(screen.getByText("Grok")).toBeInTheDocument();
  });
});
