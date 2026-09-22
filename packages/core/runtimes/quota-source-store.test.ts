// @vitest-environment jsdom
import { beforeAll, beforeEach, describe, expect, it } from "vitest";
import { useProviderQuotaSourceStore } from "./quota-source-store";

beforeAll(() => {
  if (typeof globalThis.localStorage?.setItem !== "function") {
    const values = new Map<string, string>();
    const storage: Storage = {
      get length() {
        return values.size;
      },
      clear: () => values.clear(),
      getItem: (k) => values.get(k) ?? null,
      key: (i) => Array.from(values.keys())[i] ?? null,
      removeItem: (k) => {
        values.delete(k);
      },
      setItem: (k, v) => {
        values.set(k, v);
      },
    };
    Object.defineProperty(globalThis, "localStorage", {
      configurable: true,
      value: storage,
    });
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      value: storage,
    });
  }
});

describe("provider quota source store", () => {
  beforeEach(() => {
    useProviderQuotaSourceStore.setState({ selectedByProvider: {} });
  });

  it("records the chosen runtime per provider", () => {
    useProviderQuotaSourceStore.getState().setSelectedRuntime("Grok", "rt-a");
    expect(
      useProviderQuotaSourceStore.getState().selectedByProvider.grok,
    ).toBe("rt-a");
  });

  it("keeps later providers without clobbering earlier ones", () => {
    const { setSelectedRuntime } = useProviderQuotaSourceStore.getState();
    setSelectedRuntime("grok", "rt-a");
    setSelectedRuntime("claude", "rt-b");
    expect(useProviderQuotaSourceStore.getState().selectedByProvider).toEqual({
      grok: "rt-a",
      claude: "rt-b",
    });
  });

  it("ignores empty provider or runtime ids", () => {
    const { setSelectedRuntime } = useProviderQuotaSourceStore.getState();
    setSelectedRuntime("  ", "rt-a");
    setSelectedRuntime("grok", "");
    expect(useProviderQuotaSourceStore.getState().selectedByProvider).toEqual(
      {},
    );
  });
});
