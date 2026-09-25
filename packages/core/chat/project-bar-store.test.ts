// @vitest-environment jsdom

import { beforeEach, describe, expect, it } from "vitest";
import { useChatProjectBarStore } from "./project-bar-store";

describe("useChatProjectBarStore", () => {
  beforeEach(() => {
    localStorage.clear();
    useChatProjectBarStore.setState({ byUser: {} });
  });

  it("stores pin order on the user, and a second user does not see it", () => {
    const store = useChatProjectBarStore.getState();
    store.pin("user-a", "p1");
    store.pin("user-a", "p2");
    store.pin("user-b", "p9");
    store.move("user-a", "p2", "p1");

    expect(useChatProjectBarStore.getState().byUser["user-a"]).toEqual(["p2", "p1"]);
    expect(useChatProjectBarStore.getState().byUser["user-b"]).toEqual(["p9"]);

    const raw = localStorage.getItem("multica_chat_project_bar");
    expect(raw).toBeTruthy();
    const saved = JSON.parse(raw!) as { state: { byUser: Record<string, string[]> } };
    expect(saved.state.byUser["user-a"]).toEqual(["p2", "p1"]);
    expect(JSON.stringify(saved)).not.toContain("workspace");
  });

  it("unpinning the last project forgets that user", () => {
    useChatProjectBarStore.getState().pin("user-a", "p1");
    useChatProjectBarStore.getState().unpin("user-a", "p1");
    expect(useChatProjectBarStore.getState().byUser["user-a"]).toBeUndefined();
  });
});
