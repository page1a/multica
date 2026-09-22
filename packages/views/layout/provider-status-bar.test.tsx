// @vitest-environment jsdom

import { type ReactElement, type ReactNode } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { AgentRuntime } from "@multica/core/types";
import enRuntimes from "../locales/en/runtimes.json";
import {
  collectProviderQuotas,
  ProviderStatusBarView,
  quotaSourceLabel,
  resolveSelectedRuntime,
} from "./provider-status-bar";

const NOW = 1_800_000_000_000;

vi.mock("../i18n", () => ({
  useT: () => ({
    t: (
      sel: (r: typeof enRuntimes) => string,
      vars?: Record<string, string>,
    ) => {
      const template = sel(enRuntimes);
      return vars
        ? template.replace(/\{\{(\w+)\}\}/g, (_, key) => String(vars[key] ?? ""))
        : template;
    },
  }),
  useLocale: () => "en",
}));

vi.mock("../runtimes/components/provider-logo", () => ({
  ProviderLogo: ({ provider }: { provider: string }) => (
    <span>{provider}-logo</span>
  ),
}));

vi.mock("@multica/ui/components/ui/tooltip", () => ({
  Tooltip: ({ children }: { children: ReactNode }) => <>{children}</>,
  TooltipTrigger: ({ render }: { render?: ReactElement }) => render ?? null,
  TooltipContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
}));

vi.mock("@multica/ui/components/ui/dropdown-menu", async () => {
  const { createContext, useContext } = await import("react");
  const RadioContext = createContext<(value: string) => void>(() => {});
  return {
    DropdownMenu: ({ children }: { children: ReactNode }) => <>{children}</>,
    DropdownMenuTrigger: ({ render }: { render?: ReactElement }) => render ?? null,
    DropdownMenuContent: ({ children }: { children: ReactNode }) => (
      <div>{children}</div>
    ),
    DropdownMenuRadioGroup: ({
      children,
      onValueChange,
    }: {
      children: ReactNode;
      onValueChange?: (value: string) => void;
    }) => (
      <RadioContext.Provider value={onValueChange ?? (() => {})}>
        {children}
      </RadioContext.Provider>
    ),
    DropdownMenuRadioItem: ({
      children,
      value,
    }: {
      children: ReactNode;
      value: string;
    }) => {
      const onValueChange = useContext(RadioContext);
      return (
        <button type="button" onClick={() => onValueChange(value)}>
          {children}
        </button>
      );
    },
  };
});

function runtime(
  overrides: Partial<AgentRuntime> &
    Pick<AgentRuntime, "id" | "provider" | "plan_limits">,
): AgentRuntime {
  return {
    name: `${overrides.provider} (host)`,
    custom_name: null,
    device_info: "",
    ...overrides,
  } as AgentRuntime;
}

describe("quotaSourceLabel", () => {
  it("prefers a custom name, then the hostname, then the device", () => {
    expect(
      quotaSourceLabel({
        name: "Grok (box.local)",
        custom_name: "悟天",
        device_info: "box.local · darwin-arm64",
      }),
    ).toBe("悟天");
    expect(
      quotaSourceLabel({
        name: "Grok (box.local)",
        custom_name: "  ",
        device_info: "box.local · darwin-arm64",
      }),
    ).toBe("box.local");
    expect(
      quotaSourceLabel({
        name: "Grok",
        custom_name: null,
        device_info: "studio · linux-amd64",
      }),
    ).toBe("studio");
  });

  it("never renders the raw daemon name", () => {
    expect(
      quotaSourceLabel({
        name: "Grok",
        custom_name: null,
        device_info: "",
      }),
    ).toBe("—");
    expect(
      quotaSourceLabel({
        name: "Grok (box.local)",
        custom_name: null,
        device_info: "",
      }),
    ).not.toBe("Grok (box.local)");
  });
});

describe("resolveSelectedRuntime", () => {
  const runtimes = [
    { runtimeId: "newest" },
    { runtimeId: "older" },
  ];

  it("uses the persisted id when it still exists", () => {
    expect(resolveSelectedRuntime(runtimes, "older")?.runtimeId).toBe("older");
  });

  it("falls back to the first runtime when the persisted id is gone", () => {
    expect(resolveSelectedRuntime(runtimes, "missing")?.runtimeId).toBe(
      "newest",
    );
    expect(resolveSelectedRuntime(runtimes, undefined)?.runtimeId).toBe(
      "newest",
    );
  });
});

describe("collectProviderQuotas", () => {
  it("keeps only providers with a current real snapshot", () => {
    const result = collectProviderQuotas(
      [
        runtime({
          id: "grok-1",
          provider: "grok",
          plan_limits: {
            provider: "grok",
            status: "available",
            observed_at: NOW / 1000 - 60,
            windows: [{ name: "credits", used_percent: 25 }],
          },
        }),
        runtime({
          id: "codex-1",
          provider: "codex",
          plan_limits: null,
        }),
      ],
      NOW,
    );

    expect(result.map((item) => item.provider)).toEqual(["grok"]);
    expect(result[0]?.runtimes).toHaveLength(1);
  });

  it("keeps every runtime under a provider instead of dropping older snapshots", () => {
    const result = collectProviderQuotas(
      [
        runtime({
          id: "grok-old",
          provider: "grok",
          custom_name: "旧机器",
          plan_limits: {
            provider: "grok",
            status: "available",
            observed_at: NOW / 1000 - 120,
            windows: [{ name: "credits", used_percent: 89 }],
          },
        }),
        runtime({
          id: "grok-new",
          provider: "grok",
          custom_name: "悟天",
          plan_limits: {
            provider: "grok",
            status: "available",
            observed_at: NOW / 1000 - 30,
            windows: [{ name: "credits", used_percent: 1 }],
          },
        }),
      ],
      NOW,
    );

    expect(result).toHaveLength(1);
    expect(result[0]?.runtimes.map((item) => item.runtimeId)).toEqual([
      "grok-new",
      "grok-old",
    ]);
    expect(result[0]?.runtimes.map((item) => item.sourceLabel)).toEqual([
      "悟天",
      "旧机器",
    ]);
  });
});

describe("ProviderStatusBarView", () => {
  const providers = collectProviderQuotas(
    [
      runtime({
        id: "grok-old",
        provider: "grok",
        custom_name: "旧机器",
        plan_limits: {
          provider: "grok",
          status: "available",
          observed_at: NOW / 1000 - 120,
          windows: [{ name: "credits", used_percent: 89 }],
        },
      }),
      runtime({
        id: "grok-new",
        provider: "grok",
        custom_name: "悟天",
        plan_limits: {
          provider: "grok",
          status: "available",
          observed_at: NOW / 1000 - 30,
          windows: [{ name: "credits", used_percent: 1 }],
        },
      }),
    ],
    NOW,
  );

  it("defaults to the newest snapshot and lists every runtime", () => {
    render(<ProviderStatusBarView providers={providers} />);

    const trigger = screen.getByRole("button", { name: /Switch Grok quota/ });
    expect(trigger).toHaveTextContent("99%");
    expect(trigger).toHaveTextContent("悟天");
    expect(screen.getByRole("button", { name: /旧机器/ })).toHaveTextContent("11%");
  });

  it("shows the persisted runtime instead of silently taking the newest", () => {
    render(
      <ProviderStatusBarView
        providers={providers}
        selectedByProvider={{ grok: "grok-old" }}
      />,
    );

    expect(screen.getByRole("button", { name: /Switch Grok quota/ })).toHaveTextContent(
      "11%",
    );
    expect(screen.getByRole("button", { name: /Switch Grok quota/ })).toHaveTextContent(
      "旧机器",
    );
  });

  it("notifies when the user picks another runtime", async () => {
    const user = userEvent.setup();
    const onSelectRuntime = vi.fn();
    render(
      <ProviderStatusBarView
        providers={providers}
        onSelectRuntime={onSelectRuntime}
      />,
    );

    await user.click(screen.getByRole("button", { name: /旧机器/ }));
    expect(onSelectRuntime).toHaveBeenCalledWith("grok", "grok-old");
  });
});
