// @vitest-environment jsdom
import { beforeAll, beforeEach, describe, expect, it } from "vitest";
import {
  DEFAULT_ISSUE_DETAIL_SECTIONS_OPEN,
  useIssueDetailSectionsStore,
} from "./issue-detail-sections-store";

const KEY = "multica_issue_detail_sections";

// Node 25 ships a partial `localStorage` shim under jsdom that's missing
// `clear`/`removeItem`; replace it with a real in-memory Storage so persist
// can round-trip values.
beforeAll(() => {
  if (typeof globalThis.localStorage?.setItem !== "function") {
    const values = new Map<string, string>();
    const storage: Storage = {
      get length() { return values.size; },
      clear: () => values.clear(),
      getItem: (k) => values.get(k) ?? null,
      key: (i) => Array.from(values.keys())[i] ?? null,
      removeItem: (k) => { values.delete(k); },
      setItem: (k, v) => { values.set(k, v); },
    };
    Object.defineProperty(globalThis, "localStorage", { configurable: true, value: storage });
    Object.defineProperty(window, "localStorage", { configurable: true, value: storage });
  }
});

describe("issue detail sections store", () => {
  beforeEach(() => {
    localStorage.clear();
    useIssueDetailSectionsStore.setState({ open: { ...DEFAULT_ISSUE_DETAIL_SECTIONS_OPEN } });
  });

  it("opens every section by default", () => {
    expect(useIssueDetailSectionsStore.getState().open).toEqual({
      properties: true,
      parentIssue: true,
      pullRequests: true,
      details: true,
    });
  });

  it("toggles one section and persists the fold as a preference", () => {
    useIssueDetailSectionsStore.getState().toggle("details");
    expect(useIssueDetailSectionsStore.getState().open).toEqual({
      ...DEFAULT_ISSUE_DETAIL_SECTIONS_OPEN,
      details: false,
    });
    const raw = JSON.parse(localStorage.getItem(KEY) ?? "null");
    expect(raw).toEqual({
      state: { open: { ...DEFAULT_ISSUE_DETAIL_SECTIONS_OPEN, details: false } },
      version: 1,
    });
  });

  it("fills sections missing from an older snapshot with their defaults", async () => {
    localStorage.setItem(
      KEY,
      JSON.stringify({ state: { open: { details: false } }, version: 1 }),
    );
    await useIssueDetailSectionsStore.persist.rehydrate();
    expect(useIssueDetailSectionsStore.getState().open).toEqual({
      ...DEFAULT_ISSUE_DETAIL_SECTIONS_OPEN,
      details: false,
    });
  });

  it("ignores malformed flags instead of collapsing the section", async () => {
    localStorage.setItem(
      KEY,
      JSON.stringify({
        state: { open: { properties: "no", pullRequests: false, unknown: false } },
        version: 1,
      }),
    );
    await useIssueDetailSectionsStore.persist.rehydrate();
    expect(useIssueDetailSectionsStore.getState().open).toEqual({
      ...DEFAULT_ISSUE_DETAIL_SECTIONS_OPEN,
      pullRequests: false,
    });
  });
});
