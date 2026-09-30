// @vitest-environment jsdom

import { beforeEach, describe, expect, it } from "vitest";
import { useChatProjectOpenStore } from "./project-open-store";

describe("useChatProjectOpenStore", () => {
  beforeEach(() => {
    localStorage.clear();
    useChatProjectOpenStore.setState({ byUser: {} });
  });

  it("remembers the last opened chat per project for one person", () => {
    const store = useChatProjectOpenStore.getState();
    store.remember("user-a", ["proj-x"], "chat-1");
    store.remember("user-a", ["proj-x", "proj-y"], "chat-2");
    store.remember("user-b", ["proj-x"], "chat-9");

    expect(store.recall("user-a", { type: "project", id: "proj-x" })).toBe("chat-2");
    expect(store.recall("user-a", { type: "project", id: "proj-y" })).toBe("chat-2");
    expect(store.recall("user-b", { type: "project", id: "proj-x" })).toBe("chat-9");
    expect(store.recall("user-a", { type: "all" })).toBeNull();
  });

  it("remembers an unbound chat under the no-project view", () => {
    useChatProjectOpenStore.getState().remember("user-a", [], "loose");
    expect(
      useChatProjectOpenStore.getState().recall("user-a", { type: "none" }),
    ).toBe("loose");
    expect(
      useChatProjectOpenStore.getState().recall("user-a", { type: "project", id: "proj-x" }),
    ).toBeNull();
  });
});
